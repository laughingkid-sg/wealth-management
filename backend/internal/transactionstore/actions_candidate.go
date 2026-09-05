package transactionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/zhengteck/wealth-builder/backend/internal/reconciliation"
)

// ErrCandidateNotFound is returned when a source candidate does not exist for
// the owner and source.
var ErrCandidateNotFound = errors.New("source candidate not found")

// SourceCandidateSummary is the safe per-candidate projection for the UI. Raw
// provider data stays private; only the parsed candidate's canonical fields and
// its reconciliation state are exposed.
type SourceCandidateSummary struct {
	ID                     uuid.UUID  `json:"id"`
	OutputOrdinal          int        `json:"output_ordinal"`
	Status                 string     `json:"status"`
	Kind                   string     `json:"transaction_kind"`
	Title                  string     `json:"title"`
	MerchantName           string     `json:"merchant_name"`
	OriginalAmountMinor    int64      `json:"original_amount_minor"`
	OriginalCurrency       string     `json:"original_currency"`
	OccurredAt             time.Time  `json:"occurred_at"`
	SuggestedAccountID     *uuid.UUID `json:"suggested_account_id"`
	SuggestedTransactionID *uuid.UUID `json:"suggested_transaction_id"`
	ReconciliationReason   string     `json:"reconciliation_reason"`
	MatchConfidence        *int16     `json:"match_confidence"`
	TransactionID          *uuid.UUID `json:"transaction_id"`
}

// recomputeSourceParseRollup sets data_sources.parse_status from the worst
// outcome across the source's candidates: failed > review_required > dangling >
// parsed (all linked, or zero candidates).
func recomputeSourceParseRollup(ctx context.Context, tx pgx.Tx, userID, sourceID uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		update private.data_sources d set parse_status = (
			select case
				when count(*) filter (where status = 'failed') > 0 then 'failed'
				when count(*) filter (where status = 'review_required') > 0 then 'review_required'
				when count(*) filter (where status = 'dangling') > 0 then 'dangling'
				else 'parsed'
			end
			from private.source_candidates where data_source_id = d.id and user_id = d.user_id
		), parse_error = null
		where d.id = $1 and d.user_id = $2`, sourceID, userID)
	return err
}

// ListSourceCandidates returns the candidates parsed from one source, oldest
// ordinal first, for the review/dangling queues UI.
func (s *Store) ListSourceCandidates(ctx context.Context, userID, sourceID uuid.UUID) ([]SourceCandidateSummary, error) {
	rows, err := s.pool.Query(ctx, `
		select id, output_ordinal, status, parsed_candidate, suggested_account_id,
			suggested_transaction_id, coalesce(reconciliation_reason, ''), match_confidence, transaction_id
		from private.source_candidates
		where user_id = $1 and data_source_id = $2 and origin = 'gmail_email'
		order by output_ordinal`, userID, sourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	summaries := make([]SourceCandidateSummary, 0)
	for rows.Next() {
		var summary SourceCandidateSummary
		var raw []byte
		if err := rows.Scan(&summary.ID, &summary.OutputOrdinal, &summary.Status, &raw,
			&summary.SuggestedAccountID, &summary.SuggestedTransactionID, &summary.ReconciliationReason,
			&summary.MatchConfidence, &summary.TransactionID); err != nil {
			return nil, err
		}
		var stored persistedSourceCandidate
		if err := json.Unmarshal(raw, &stored); err == nil {
			summary.Kind = string(stored.Candidate.Kind)
			summary.Title = stored.Candidate.Title
			summary.MerchantName = stored.Candidate.MerchantName
			summary.OriginalAmountMinor = stored.Candidate.OriginalAmountMinor
			summary.OriginalCurrency = stored.Candidate.OriginalCurrency
			summary.OccurredAt = stored.Candidate.OccurredAt
		}
		summaries = append(summaries, summary)
	}
	return summaries, rows.Err()
}

// lockActionableCandidate locks one candidate row and returns its status. A
// candidate is manually actionable only from a review_required or dangling state.
func lockActionableCandidate(ctx context.Context, tx pgx.Tx, userID, sourceID, candidateID uuid.UUID) (string, error) {
	var status string
	err := tx.QueryRow(ctx, `
		select status from private.source_candidates
		where id = $1 and user_id = $2 and data_source_id = $3 for update`, candidateID, userID, sourceID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrCandidateNotFound
	}
	return status, err
}

// AttachSourceCandidate links one candidate to an existing owned transaction and
// marks the candidate attached, then recomputes the source rollup.
func (s *Store) AttachSourceCandidate(ctx context.Context, userID, sourceID, candidateID, transactionID uuid.UUID) (uuid.UUID, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	status, err := lockActionableCandidate(ctx, tx, userID, sourceID, candidateID)
	if err != nil {
		return uuid.Nil, err
	}
	if status != "review_required" && status != "dangling" {
		return uuid.Nil, ErrSourceNotActionable
	}
	var linkID uuid.UUID
	err = tx.QueryRow(ctx, `
		insert into private.transaction_data_sources (user_id, transaction_id, data_source_id, bulk_import_candidate_id, role, matched_by)
		select $1, transaction.id, $2, $4, 'other', 'user'
		from public.transactions transaction
		where transaction.id = $3 and transaction.user_id = $1
		returning id`, userID, sourceID, transactionID, candidateID).Scan(&linkID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrTransactionNotFound
	}
	if err != nil {
		return uuid.Nil, err
	}
	if _, err = tx.Exec(ctx, `
		update private.source_candidates
		set status = 'attached', transaction_id = $3, suggested_account_id = null,
			suggested_transaction_id = null, reconciliation_reason = null
		where id = $1 and user_id = $2`, candidateID, userID, transactionID); err != nil {
		return uuid.Nil, err
	}
	if err = recomputeSourceParseRollup(ctx, tx, userID, sourceID); err != nil {
		return uuid.Nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return uuid.Nil, err
	}
	return linkID, nil
}

// CreateTransactionFromSourceCandidate creates a user-confirmed transaction from
// one persisted candidate and marks it created, then recomputes the rollup.
func (s *Store) CreateTransactionFromSourceCandidate(ctx context.Context, userID, sourceID, candidateID, accountID uuid.UUID) (Transaction, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Transaction{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	status, err := lockActionableCandidate(ctx, tx, userID, sourceID, candidateID)
	if err != nil {
		return Transaction{}, err
	}
	if status != "review_required" && status != "dangling" {
		return Transaction{}, ErrSourceNotActionable
	}
	var raw []byte
	if err = tx.QueryRow(ctx, `select parsed_candidate from private.source_candidates where id = $1 and user_id = $2`, candidateID, userID).Scan(&raw); err != nil {
		return Transaction{}, err
	}
	parsed, err := decodePersistedSourceCandidate(raw, userID)
	if err != nil {
		return Transaction{}, ErrSourceNotActionable
	}
	var lockedAccountID uuid.UUID
	err = tx.QueryRow(ctx, `
		select id from public.accounts where id = $1 and user_id = $2 and deleted_at is null for share`, accountID, userID).Scan(&lockedAccountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Transaction{}, ErrAccountNotFound
	}
	if err != nil {
		return Transaction{}, err
	}
	lineItems, err := json.Marshal(parsed.Candidate.LineItems)
	if err != nil {
		return Transaction{}, fmt.Errorf("encode source line items: %w", err)
	}
	details, err := json.Marshal(struct {
		References      []string                       `json:"references"`
		AccountEvidence reconciliation.AccountEvidence `json:"account_evidence"`
	}{References: parsed.Candidate.References, AccountEvidence: parsed.Candidate.AccountEvidence})
	if err != nil {
		return Transaction{}, fmt.Errorf("encode source details: %w", err)
	}
	categoryID, err := s.resolveCategoryLeaf(ctx, tx, parsed.Candidate.CategoryLeafName)
	if err != nil {
		return Transaction{}, err
	}
	var transaction Transaction
	if err = tx.QueryRow(ctx, `
		insert into public.transactions (user_id, account_id, transaction_kind, title, merchant_name,
			original_amount_minor, original_currency, sgd_amount_minor, occurred_at, category_id, line_items, details,
			review_status, match_confidence, creation_method)
		values ($1, $2, $3, $4, nullif($5, ''), $6, $7, $8, $9, $10, $11::jsonb, $12::jsonb, 'confirmed', $13, 'user_source')
		returning id, account_id, transaction_kind, title, merchant_name, original_amount_minor, original_currency,
			sgd_amount_minor, occurred_at, category_id, line_items, review_status, match_confidence, created_at, updated_at`,
		userID, accountID, string(parsed.Candidate.Kind), strings.TrimSpace(parsed.Candidate.Title),
		strings.TrimSpace(parsed.Candidate.MerchantName), parsed.Candidate.OriginalAmountMinor,
		parsed.Candidate.OriginalCurrency, parsed.Candidate.SGDAmountMinor, parsed.Candidate.OccurredAt,
		categoryID, string(lineItems), string(details), confidencePercent(parsed.Candidate.Confidence)).Scan(transactionFields(&transaction)...); err != nil {
		return Transaction{}, err
	}
	if _, err = tx.Exec(ctx, `
		insert into private.transaction_data_sources (user_id, transaction_id, data_source_id, bulk_import_candidate_id, role, match_confidence, matched_by)
		values ($1, $2, $3, $4, 'other', $5, 'user')`, userID, transaction.ID, sourceID, candidateID, confidencePercent(parsed.Candidate.Confidence)); err != nil {
		return Transaction{}, err
	}
	if _, err = tx.Exec(ctx, `
		update private.source_candidates
		set status = 'created', transaction_id = $3, account_id = $4,
			suggested_account_id = null, suggested_transaction_id = null, reconciliation_reason = null
		where id = $1 and user_id = $2`, candidateID, userID, transaction.ID, accountID); err != nil {
		return Transaction{}, err
	}
	if err = recomputeSourceParseRollup(ctx, tx, userID, sourceID); err != nil {
		return Transaction{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Transaction{}, err
	}
	return transaction, nil
}

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
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/zhengteck/wealth-builder/backend/internal/reconciliation"
)

var (
	ErrSourceNotFound      = errors.New("transaction source not found")
	ErrTransactionNotFound = errors.New("transaction not found")
	ErrSourceLinkNotFound  = errors.New("transaction source link not found")
	ErrSourceNotActionable = errors.New("transaction source cannot be actioned")
	ErrSourceAlreadyLinked = errors.New("transaction source is already linked")
)

// SourceEvidence is a safe source summary together with its active evidence-link ID.
// Raw provider data remains private and is never returned here.
type SourceEvidence struct {
	SourceLinkID uuid.UUID `json:"source_link_id"`
	SourceSummary
}

type Transaction struct {
	ID                  uuid.UUID
	AccountID           uuid.UUID
	TransactionKind     string
	Title               string
	MerchantName        *string
	OriginalAmountMinor int64
	OriginalCurrency    string
	SGDAmountMinor      *int64
	OccurredAt          time.Time
	CategoryID          *uuid.UUID
	LineItems           json.RawMessage
	ReviewStatus        string
	MatchConfidence     *int16
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type OptionalMinorAmount struct {
	Set   bool
	Value *int64
}

type OptionalUUID struct {
	Set   bool
	Value *uuid.UUID
}

// TransactionPatch represents only the canonical fields a user may edit.
// Details, review workflow and evidence links are intentionally absent.
type TransactionPatch struct {
	Title               *string
	AccountID           *uuid.UUID
	OccurredAt          *time.Time
	OriginalAmountMinor *int64
	OriginalCurrency    *string
	SGDAmountMinor      OptionalMinorAmount
	CategoryID          OptionalUUID
	LineItems           *json.RawMessage
}

func (s *Store) ListTransactionSources(ctx context.Context, userID, transactionID uuid.UUID) ([]SourceEvidence, error) {
	rows, err := s.pool.Query(ctx, `
		select link.id, source.id, source.source_type, source.provider, source.received_at,
			source.parse_status, source.parse_confidence, coalesce(source.raw_data ->> 'subject', ''),
			coalesce(source.raw_data ->> 'sender', ''), source.parse_error, source.created_at
		from private.transaction_data_sources link
		join private.data_sources source on source.id = link.data_source_id and source.user_id = link.user_id
		join public.transactions transaction on transaction.id = link.transaction_id and transaction.user_id = link.user_id
		where link.transaction_id = $1 and link.user_id = $2 and link.detached_at is null
		order by link.attached_at desc`, transactionID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]SourceEvidence, 0)
	for rows.Next() {
		var evidence SourceEvidence
		if err := rows.Scan(&evidence.SourceLinkID, &evidence.ID, &evidence.SourceType, &evidence.Provider,
			&evidence.ReceivedAt, &evidence.ParseStatus, &evidence.ParseConfidence, &evidence.Subject,
			&evidence.Sender, &evidence.ParseError, &evidence.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, evidence)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Store) AttachSource(ctx context.Context, userID, sourceID, transactionID uuid.UUID) (uuid.UUID, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	status, err := lockActionableSource(ctx, tx, userID, sourceID)
	if err != nil {
		return uuid.Nil, err
	}
	if status != "dangling" && status != "review_required" && status != "parsed" {
		return uuid.Nil, ErrSourceNotActionable
	}
	linked, err := sourceHasActiveLink(ctx, tx, userID, sourceID)
	if err != nil {
		return uuid.Nil, err
	}
	if linked {
		return uuid.Nil, ErrSourceAlreadyLinked
	}
	var linkID uuid.UUID
	err = tx.QueryRow(ctx, `
		insert into private.transaction_data_sources (user_id, transaction_id, data_source_id, role, matched_by)
		select $1, transaction.id, $2, 'other', 'user'
		from public.transactions transaction
		where transaction.id = $3 and transaction.user_id = $1
		returning id`, userID, sourceID, transactionID).Scan(&linkID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrTransactionNotFound
	}
	if err != nil {
		return uuid.Nil, err
	}
	if _, err = tx.Exec(ctx, `
		update private.data_sources
		set parse_status = 'parsed', suggested_account_id = null,
			suggested_transaction_id = null, reconciliation_reason = null,
			parse_error = null
		where id = $1 and user_id = $2`, sourceID, userID); err != nil {
		return uuid.Nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return uuid.Nil, err
	}
	return linkID, nil
}

// CreateTransactionFromSource creates a user-confirmed canonical transaction
// exclusively from a previously validated persisted parser candidate.
func (s *Store) CreateTransactionFromSource(ctx context.Context, userID, sourceID, accountID uuid.UUID) (Transaction, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Transaction{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	status, err := lockActionableSource(ctx, tx, userID, sourceID)
	if err != nil {
		return Transaction{}, err
	}
	if status != "dangling" && status != "review_required" {
		return Transaction{}, ErrSourceNotActionable
	}
	linked, err := sourceHasActiveLink(ctx, tx, userID, sourceID)
	if err != nil {
		return Transaction{}, err
	}
	if linked {
		return Transaction{}, ErrSourceAlreadyLinked
	}

	var candidateRaw []byte
	err = tx.QueryRow(ctx, `
		select parsed_candidate
		from private.source_parse_attempts
		where user_id = $1 and data_source_id = $2 and validation_status = 'valid'
		order by created_at desc
		limit 1`, userID, sourceID).Scan(&candidateRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		return Transaction{}, ErrSourceNotActionable
	}
	if err != nil {
		return Transaction{}, err
	}
	parsed, err := decodePersistedCandidate(candidateRaw, userID)
	if err != nil {
		return Transaction{}, ErrSourceNotActionable
	}
	var lockedAccountID uuid.UUID
	err = tx.QueryRow(ctx, `
		select id from public.accounts
		where id = $1 and user_id = $2 and deleted_at is null
		for share`, accountID, userID).Scan(&lockedAccountID)
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
	err = tx.QueryRow(ctx, `
		insert into public.transactions (user_id, account_id, transaction_kind, title, merchant_name,
			original_amount_minor, original_currency, sgd_amount_minor, occurred_at, category_id, line_items, details,
			review_status, match_confidence)
		values ($1, $2, $3, $4, nullif($5, ''), $6, $7, $8, $9, $10, $11::jsonb, $12::jsonb, 'confirmed', $13)
		returning id, account_id, transaction_kind, title, merchant_name, original_amount_minor, original_currency,
			sgd_amount_minor, occurred_at, category_id, line_items, review_status, match_confidence, created_at, updated_at`,
		userID, accountID, string(parsed.Candidate.Kind), strings.TrimSpace(parsed.Candidate.Title),
		strings.TrimSpace(parsed.Candidate.MerchantName), parsed.Candidate.OriginalAmountMinor,
		parsed.Candidate.OriginalCurrency, parsed.Candidate.SGDAmountMinor, parsed.Candidate.OccurredAt,
		categoryID, string(lineItems), string(details), confidencePercent(parsed.Candidate.Confidence)).Scan(transactionFields(&transaction)...)
	if err != nil {
		return Transaction{}, err
	}
	if _, err = tx.Exec(ctx, `
		insert into private.transaction_data_sources (user_id, transaction_id, data_source_id, role, match_confidence, matched_by)
		values ($1, $2, $3, 'other', $4, 'user')`, userID, transaction.ID, sourceID, confidencePercent(parsed.Candidate.Confidence)); err != nil {
		return Transaction{}, err
	}
	if _, err = tx.Exec(ctx, `
		update private.data_sources
		set parse_status = 'parsed', suggested_account_id = $3,
			suggested_transaction_id = null, reconciliation_reason = null,
			parse_error = null
		where id = $1 and user_id = $2`, sourceID, userID, accountID); err != nil {
		return Transaction{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Transaction{}, err
	}
	return transaction, nil
}

// UnmatchSourceLink deactivates an evidence link while retaining its audit
// row. A source with no other active evidence link returns to the dangling
// queue so the user can reattach it deliberately.
func (s *Store) UnmatchSourceLink(ctx context.Context, userID, linkID uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var sourceID uuid.UUID
	err = tx.QueryRow(ctx, `
		update private.transaction_data_sources
		set detached_at = now(), detached_by_user = true
		where id = $1 and user_id = $2 and detached_at is null
		returning data_source_id`, linkID, userID).Scan(&sourceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrSourceLinkNotFound
	}
	if err != nil {
		return err
	}
	var hasRemainingLink bool
	if err = tx.QueryRow(ctx, `
		select exists(
			select 1 from private.transaction_data_sources
			where user_id = $1 and data_source_id = $2 and detached_at is null
		)`, userID, sourceID).Scan(&hasRemainingLink); err != nil {
		return err
	}
	if !hasRemainingLink {
		if _, err = tx.Exec(ctx, `
			update private.data_sources
			set parse_status = 'dangling', suggested_transaction_id = null,
				reconciliation_reason = 'The source is not attached to a transaction.',
				parse_error = null
			where id = $1 and user_id = $2`, sourceID, userID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) PatchTransaction(ctx context.Context, userID, transactionID uuid.UUID, patch TransactionPatch) (Transaction, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Transaction{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if patch.AccountID != nil {
		if err = validatePatchedTransferAccount(ctx, tx, userID, transactionID, *patch.AccountID); err != nil {
			return Transaction{}, err
		}
	}
	if patch.CategoryID.Set && patch.CategoryID.Value != nil {
		if err = validatePatchedCategory(ctx, tx, *patch.CategoryID.Value); err != nil {
			return Transaction{}, err
		}
	}

	var transaction Transaction
	err = tx.QueryRow(ctx, `
		update public.transactions transaction
		set title = case when $3 then $4 else transaction.title end,
			account_id = case when $5 then $6 else transaction.account_id end,
			occurred_at = case when $7 then $8 else transaction.occurred_at end,
			original_amount_minor = case when $9 then $10 else transaction.original_amount_minor end,
			original_currency = case when $11 then $12 else transaction.original_currency end,
			sgd_amount_minor = case when $13 then $14 else transaction.sgd_amount_minor end,
			category_id = case when $15 then $16::uuid else transaction.category_id end,
			line_items = case when $17 then $18::jsonb else transaction.line_items end
		where transaction.id = $1 and transaction.user_id = $2
			and (not $5 or exists (select 1 from public.accounts account where account.id = $6 and account.user_id = $2 and account.deleted_at is null))
			and (not $15 or $16::uuid is null or exists (select 1 from public.transaction_categories category where category.id = $16::uuid and category.active))
		returning transaction.id, transaction.account_id, transaction.transaction_kind, transaction.title, transaction.merchant_name,
			transaction.original_amount_minor, transaction.original_currency, transaction.sgd_amount_minor, transaction.occurred_at,
			transaction.category_id, transaction.line_items, transaction.review_status, transaction.match_confidence,
			transaction.created_at, transaction.updated_at`,
		transactionID, userID,
		patch.Title != nil, nullableString(patch.Title),
		patch.AccountID != nil, patch.AccountID,
		patch.OccurredAt != nil, patch.OccurredAt,
		patch.OriginalAmountMinor != nil, patch.OriginalAmountMinor,
		patch.OriginalCurrency != nil, nullableString(patch.OriginalCurrency),
		patch.SGDAmountMinor.Set, patch.SGDAmountMinor.Value,
		patch.CategoryID.Set, patch.CategoryID.Value,
		patch.LineItems != nil, nullableJSONBytes(patch.LineItems),
	).Scan(transactionFields(&transaction)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return Transaction{}, ErrTransactionNotFound
	}
	if err != nil {
		return Transaction{}, mapTransferIntegrityError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return Transaction{}, mapTransferIntegrityError(err)
	}
	return transaction, nil
}

func validatePatchedTransferAccount(
	ctx context.Context,
	tx pgx.Tx,
	userID, transactionID, accountID uuid.UUID,
) error {
	var lockedAccountID uuid.UUID
	err := tx.QueryRow(ctx, `
		select id
		from public.accounts
		where id = $1 and user_id = $2 and deleted_at is null
		for share`, accountID, userID).Scan(&lockedAccountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrAccountNotFound
	}
	if err != nil {
		return err
	}

	var counterpartAccountID uuid.UUID
	err = tx.QueryRow(ctx, `
		select counterpart.account_id
		from private.transaction_links transfer
		join public.transactions counterpart
			on counterpart.user_id = transfer.user_id
			and counterpart.id = case
				when transfer.debit_transaction_id = $2 then transfer.credit_transaction_id
				else transfer.debit_transaction_id
			end
		where transfer.user_id = $1
			and $2 in (transfer.debit_transaction_id, transfer.credit_transaction_id)
		order by transfer.id
		limit 1
		for update of transfer`, userID, transactionID).Scan(&counterpartAccountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if accountID == counterpartAccountID {
		return ErrTransferSameAccount
	}
	return nil
}

func validatePatchedCategory(ctx context.Context, tx pgx.Tx, categoryID uuid.UUID) error {
	var lockedCategoryID uuid.UUID
	err := tx.QueryRow(ctx, `
		select id
		from public.transaction_categories
		where id = $1 and active
		for share`, categoryID).Scan(&lockedCategoryID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrCategoryNotFound
	}
	return err
}

func mapTransferIntegrityError(err error) error {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == "23514" &&
		postgresError.Message == "an internal transfer link requires two distinct accounts" {
		return ErrTransferSameAccount
	}
	return err
}

func lockActionableSource(ctx context.Context, tx pgx.Tx, userID, sourceID uuid.UUID) (string, error) {
	var status string
	err := tx.QueryRow(ctx, `select parse_status from private.data_sources where id = $1 and user_id = $2 for update`, sourceID, userID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrSourceNotFound
	}
	return status, err
}

func sourceHasActiveLink(ctx context.Context, tx pgx.Tx, userID, sourceID uuid.UUID) (bool, error) {
	var linked bool
	err := tx.QueryRow(ctx, `
		select exists(select 1 from private.transaction_data_sources where user_id = $1 and data_source_id = $2 and detached_at is null)`, userID, sourceID).Scan(&linked)
	return linked, err
}

func transactionFields(transaction *Transaction) []any {
	return []any{&transaction.ID, &transaction.AccountID, &transaction.TransactionKind, &transaction.Title,
		&transaction.MerchantName, &transaction.OriginalAmountMinor, &transaction.OriginalCurrency,
		&transaction.SGDAmountMinor, &transaction.OccurredAt, &transaction.CategoryID, &transaction.LineItems,
		&transaction.ReviewStatus, &transaction.MatchConfidence, &transaction.CreatedAt, &transaction.UpdatedAt}
}

func nullableString(value *string) *string { return value }

func nullableJSONBytes(value *json.RawMessage) string {
	if value == nil {
		return "[]"
	}
	return string(*value)
}

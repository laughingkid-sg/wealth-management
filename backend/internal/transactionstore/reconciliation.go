package transactionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/google/uuid"
	"github.com/zhengteck/wealth-builder/backend/internal/jobs"
	"github.com/zhengteck/wealth-builder/backend/internal/reconciliation"
)

// SourceParseInput contains only normalized provider content safe to send to
// the configured parser. Raw MIME and attachment bytes stay private.
type SourceParseInput struct {
	ID                uuid.UUID
	NormalizedContent string
}

type ReconciliationInput struct {
	SourceID     uuid.UUID
	Candidate    reconciliation.Candidate
	Accounts     []reconciliation.AccountIdentity
	Transactions []reconciliation.Transaction
}

type ParsedSourceResult struct {
	SourceID       uuid.UUID
	SyncRunID      *uuid.UUID
	Model          string
	ParsedResponse reconciliation.ParsedResponse
	RawResponse    json.RawMessage
}

type ReconciliationResult struct {
	SourceID  uuid.UUID
	SyncRunID *uuid.UUID
	Candidate reconciliation.Candidate
	Decision  reconciliation.Decision
}

func (s *Store) LoadSourceParseInput(ctx context.Context, userID, sourceID uuid.UUID) (SourceParseInput, error) {
	var input SourceParseInput
	var subject, sender, text string
	err := s.pool.QueryRow(ctx, `
		select id, coalesce(raw_data ->> 'subject', ''), coalesce(raw_data ->> 'sender', ''),
			coalesce(raw_data ->> 'text', '')
		from private.data_sources
		where id = $1 and user_id = $2 and source_type = 'gmail_email'`, sourceID, userID).Scan(&input.ID, &subject, &sender, &text)
	if err != nil {
		return SourceParseInput{}, err
	}
	input.NormalizedContent = normalizedEmailContent(subject, sender, text)
	if input.NormalizedContent == "" {
		return SourceParseInput{}, errors.New("source has no normalized email content")
	}
	return input, nil
}

func normalizedEmailContent(subject, sender, text string) string {
	parts := make([]string, 0, 3)
	if value := strings.TrimSpace(subject); value != "" {
		parts = append(parts, "subject: "+value)
	}
	if value := strings.TrimSpace(sender); value != "" {
		parts = append(parts, "sender: "+value)
	}
	if value := strings.TrimSpace(text); value != "" {
		parts = append(parts, "text: "+value)
	}
	return strings.Join(parts, "\n")
}

// SaveParsedSource records a validated parser result and queues reconciliation
// atomically. The parser call itself must have completed before this method.
func (s *Store) SaveParsedSource(ctx context.Context, userID uuid.UUID, result ParsedSourceResult) error {
	if result.SourceID == uuid.Nil || !json.Valid(result.RawResponse) {
		return errors.New("valid source ID and parser response are required")
	}
	confidence := confidencePercent(result.ParsedResponse.Candidate.Confidence)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var status string
	if err = tx.QueryRow(ctx, `select parse_status from private.data_sources where id = $1 and user_id = $2 for update`, result.SourceID, userID).Scan(&status); err != nil {
		return err
	}
	if status == "review_required" || status == "dangling" {
		return tx.Commit(ctx)
	}
	metadata := []byte(`{"provider":"alibaba_openai_compatible","thinking":false,"response_format":"json_object"}`)
	_, err = tx.Exec(ctx, `
		insert into private.source_parse_attempts (user_id, data_source_id, model_name, request_metadata, parsed_candidate, validation_status, started_at, completed_at)
		values ($1, $2, $3, $4::jsonb, $5::jsonb, 'valid', now(), now())`, userID, result.SourceID, result.Model, metadata, result.RawResponse)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		update private.data_sources set parse_status = 'parsed', parse_confidence = $3,
			parse_error = null, suggested_account_id = null
		where id = $1 and user_id = $2`, result.SourceID, userID, confidence)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]string{"data_source_id": result.SourceID.String()})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		insert into private.transaction_jobs (user_id, sync_run_id, data_source_id, job_type, payload)
		select $1, $2, $3, $4, $5::jsonb
		where not exists (
			select 1 from private.transaction_jobs
			where user_id = $1 and data_source_id = $3 and job_type = $4 and status in ('queued', 'running')
		)`, userID, result.SyncRunID, result.SourceID, string(jobs.KindReconcile), payload)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) RecordInvalidSourceParse(ctx context.Context, userID, sourceID uuid.UUID, model string, raw json.RawMessage, cause error) error {
	return s.recordSourceParseError(ctx, userID, sourceID, model, raw, "invalid", cause)
}

func (s *Store) RecordFailedSourceParse(ctx context.Context, userID, sourceID uuid.UUID, model string, cause error) error {
	return s.recordSourceParseError(ctx, userID, sourceID, model, nil, "failed", cause)
}

func (s *Store) recordSourceParseError(ctx context.Context, userID, sourceID uuid.UUID, model string, raw json.RawMessage, validationStatus string, cause error) error {
	if sourceID == uuid.Nil || cause == nil {
		return errors.New("source ID and parse error are required")
	}
	errorSummary := boundedError(cause)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `
		insert into private.source_parse_attempts (user_id, data_source_id, model_name, request_metadata, parsed_candidate, validation_status, error_summary, started_at, completed_at)
		values ($1, $2, $3, '{"provider":"alibaba_openai_compatible","thinking":false}'::jsonb,
			nullif($4::jsonb, 'null'::jsonb), $5, $6, now(), now())`, userID, sourceID, model, nullableJSON(raw), validationStatus, errorSummary)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		update private.data_sources set parse_status = 'failed', parse_confidence = null, parse_error = $3
		where id = $1 and user_id = $2`, sourceID, userID, errorSummary)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func nullableJSON(raw json.RawMessage) string {
	if !json.Valid(raw) {
		return "null"
	}
	return string(raw)
}

func (s *Store) LoadReconciliationInput(ctx context.Context, userID, sourceID uuid.UUID) (ReconciliationInput, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx, `
		select attempt.parsed_candidate
		from private.data_sources source
		join lateral (
			select parsed_candidate from private.source_parse_attempts
			where user_id = source.user_id and data_source_id = source.id and validation_status = 'valid'
			order by created_at desc limit 1
		) attempt on true
		where source.id = $1 and source.user_id = $2 and source.parse_status = 'parsed'`, sourceID, userID).Scan(&raw)
	if err != nil {
		return ReconciliationInput{}, err
	}
	parsed, err := reconciliation.DecodeParsedResponse(raw)
	if err != nil {
		return ReconciliationInput{}, fmt.Errorf("decode persisted parse result: %w", err)
	}
	if parsed.Candidate.UserID != userID.String() {
		return ReconciliationInput{}, errors.New("persisted parse candidate belongs to another user")
	}
	accounts, err := s.loadOwnedAccountIdentities(ctx, userID)
	if err != nil {
		return ReconciliationInput{}, err
	}
	transactions, err := s.loadOwnedTransactions(ctx, userID)
	if err != nil {
		return ReconciliationInput{}, err
	}
	return ReconciliationInput{SourceID: sourceID, Candidate: parsed.Candidate, Accounts: accounts, Transactions: transactions}, nil
}

func (s *Store) loadOwnedAccountIdentities(ctx context.Context, userID uuid.UUID) ([]reconciliation.AccountIdentity, error) {
	rows, err := s.pool.Query(ctx, `
		select id, coalesce(account_identifier, ''), metadata
		from public.accounts where user_id = $1 and deleted_at is null`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	accounts := make([]reconciliation.AccountIdentity, 0)
	for rows.Next() {
		var account reconciliation.AccountIdentity
		var metadata []byte
		if err := rows.Scan(&account.ID, &account.AccountIdentifier, &metadata); err != nil {
			return nil, err
		}
		account.UserID = userID.String()
		account.MetadataIdentifiers = safeMetadataIdentifiers(metadata)
		accounts = append(accounts, account)
	}
	return accounts, rows.Err()
}

func safeMetadataIdentifiers(raw []byte) []string {
	var metadata map[string]json.RawMessage
	if json.Unmarshal(raw, &metadata) != nil {
		return nil
	}
	values := make([]string, 0)
	for key, value := range metadata {
		name := strings.ToLower(key)
		if !strings.Contains(name, "identifier") && !strings.Contains(name, "card") && !strings.Contains(name, "bank") && !strings.Contains(name, "account") {
			continue
		}
		var text string
		if json.Unmarshal(value, &text) == nil {
			values = append(values, text)
			continue
		}
		var list []string
		if json.Unmarshal(value, &list) == nil {
			values = append(values, list...)
		}
	}
	return values
}

func (s *Store) loadOwnedTransactions(ctx context.Context, userID uuid.UUID) ([]reconciliation.Transaction, error) {
	rows, err := s.pool.Query(ctx, `
		select id, account_id, transaction_kind, coalesce(merchant_name, ''), original_amount_minor,
			original_currency, occurred_at, coalesce(details -> 'references', '[]'::jsonb)
		from public.transactions where user_id = $1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	transactions := make([]reconciliation.Transaction, 0)
	for rows.Next() {
		var transaction reconciliation.Transaction
		var references []byte
		if err := rows.Scan(&transaction.ID, &transaction.AccountID, &transaction.Kind, &transaction.MerchantName,
			&transaction.OriginalAmountMinor, &transaction.OriginalCurrency, &transaction.OccurredAt, &references); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(references, &transaction.References); err != nil {
			return nil, fmt.Errorf("decode transaction references: %w", err)
		}
		transaction.UserID = userID.String()
		transactions = append(transactions, transaction)
	}
	return transactions, rows.Err()
}

// PersistReconciliation applies one domain decision after all external work
// and matching reads have completed. It locks only the source row, verifies
// ownership again, and updates the visible sync-run counters atomically.
func (s *Store) PersistReconciliation(ctx context.Context, userID uuid.UUID, result ReconciliationResult) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var status string
	if err = tx.QueryRow(ctx, `select parse_status from private.data_sources where id = $1 and user_id = $2 for update`, result.SourceID, userID).Scan(&status); err != nil {
		return err
	}
	if status != "parsed" {
		return tx.Commit(ctx)
	}
	var linked bool
	if err = tx.QueryRow(ctx, `select exists(select 1 from private.transaction_data_sources where user_id = $1 and data_source_id = $2 and detached_at is null)`, userID, result.SourceID).Scan(&linked); err != nil {
		return err
	}
	if linked {
		return tx.Commit(ctx)
	}
	created, attached, dangling, review := 0, 0, 0, 0
	confidence := confidencePercent(result.Candidate.Confidence)
	score := int16(min(100, result.Decision.Score.Total()))
	suggestedAccount := nullableUUID(result.Decision.AccountID)
	switch result.Decision.Outcome {
	case reconciliation.OutcomeAttach:
		transactionID, err := uuid.Parse(result.Decision.TransactionID)
		if err != nil {
			return errors.New("reconciliation selected an invalid transaction")
		}
		command, err := tx.Exec(ctx, `
			insert into private.transaction_data_sources (user_id, transaction_id, data_source_id, role, match_confidence, matched_by)
			select $1, transaction.id, $2, 'other', $3, 'automatic'
			from public.transactions transaction where transaction.id = $4 and transaction.user_id = $1`, userID, result.SourceID, score, transactionID)
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return errors.New("reconciliation selected a transaction not owned by the source user")
		}
		attached = 1
	case reconciliation.OutcomeCreate:
		accountID, err := uuid.Parse(result.Decision.AccountID)
		if err != nil {
			return errors.New("reconciliation selected an invalid account")
		}
		lineItems, err := json.Marshal(result.Candidate.LineItems)
		if err != nil {
			return err
		}
		details, err := json.Marshal(map[string]any{"references": result.Candidate.References, "account_evidence": result.Candidate.AccountEvidence})
		if err != nil {
			return err
		}
		var transactionID uuid.UUID
		err = tx.QueryRow(ctx, `
			insert into public.transactions (user_id, account_id, transaction_kind, title, merchant_name,
				original_amount_minor, original_currency, sgd_amount_minor, occurred_at, line_items, details,
				review_status, match_confidence)
			values ($1, $2, $3, $4, nullif($5, ''), $6, $7, $8, $9, $10::jsonb, $11::jsonb, 'pending', $12)
			returning id`, userID, accountID, string(result.Candidate.Kind), result.Candidate.Title,
			result.Candidate.MerchantName, result.Candidate.OriginalAmountMinor, result.Candidate.OriginalCurrency,
			result.Candidate.SGDAmountMinor, result.Candidate.OccurredAt, lineItems, details, confidence).Scan(&transactionID)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `insert into private.transaction_data_sources (user_id, transaction_id, data_source_id, role, match_confidence, matched_by) values ($1, $2, $3, 'other', $4, 'automatic')`, userID, transactionID, result.SourceID, confidence)
		if err != nil {
			return err
		}
		created, attached = 1, 1
	case reconciliation.OutcomeReview:
		_, err = tx.Exec(ctx, `update private.data_sources set parse_status = 'review_required', suggested_account_id = $3 where id = $1 and user_id = $2`, result.SourceID, userID, suggestedAccount)
		if err != nil {
			return err
		}
		review = 1
	case reconciliation.OutcomeDangling:
		_, err = tx.Exec(ctx, `update private.data_sources set parse_status = 'dangling', suggested_account_id = null where id = $1 and user_id = $2`, result.SourceID, userID)
		if err != nil {
			return err
		}
		dangling = 1
	default:
		return fmt.Errorf("unsupported reconciliation outcome %q", result.Decision.Outcome)
	}
	if result.Decision.Outcome == reconciliation.OutcomeAttach || result.Decision.Outcome == reconciliation.OutcomeCreate {
		_, err = tx.Exec(ctx, `update private.data_sources set suggested_account_id = $3, parse_error = null where id = $1 and user_id = $2`, result.SourceID, userID, suggestedAccount)
		if err != nil {
			return err
		}
	}
	if result.SyncRunID != nil {
		_, err = tx.Exec(ctx, `
			update public.transaction_sync_runs set transactions_created_count = transactions_created_count + $3,
				sources_linked_count = sources_linked_count + $4, dangling_sources_count = dangling_sources_count + $5,
				review_required_count = review_required_count + $6
			where id = $1 and user_id = $2`, *result.SyncRunID, userID, created, attached, dangling, review)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func confidencePercent(value float64) int16 {
	return int16(math.Round(math.Max(0, math.Min(1, value)) * 100))
}

func nullableUUID(value string) *uuid.UUID {
	parsed, err := uuid.Parse(value)
	if err != nil {
		return nil
	}
	return &parsed
}

func boundedError(err error) string {
	value := strings.TrimSpace(err.Error())
	if len(value) > 2000 {
		return value[:2000]
	}
	return value
}

func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}

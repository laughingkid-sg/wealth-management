package creditcardstore

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
	"github.com/zhengteck/wealth-builder/backend/internal/bulkparse"
	"github.com/zhengteck/wealth-builder/backend/internal/creditcard"
	"github.com/zhengteck/wealth-builder/backend/internal/database"
)

type transaction struct {
	tx                        pgx.Tx
	confirmedPaymentCandidate *uuid.UUID
}

func (t *transaction) GetBillForUpdate(ctx context.Context, userID, billID uuid.UUID) (creditcard.Bill, error) {
	return loadBill(ctx, t.tx, userID, billID, true)
}

func (t *transaction) ProjectBillFromBulk(ctx context.Context, userID, documentID uuid.UUID, attemptGeneration int) (creditcard.BulkProjectionResult, error) {
	var lockedDocumentID uuid.UUID
	if err := t.tx.QueryRow(ctx, `
		select id from private.bulk_import_documents
		where id = $1 and user_id = $2 for update`, documentID, userID).Scan(&lockedDocumentID); err != nil {
		return creditcard.BulkProjectionResult{}, mapNotFound(err)
	}
	var existingID uuid.UUID
	err := t.tx.QueryRow(ctx, `
		select id from private.credit_card_statements
		where bulk_document_id = $1 and user_id = $2 for update`, documentID, userID).Scan(&existingID)
	if err == nil {
		bill, loadErr := loadBill(ctx, t.tx, userID, existingID, false)
		if loadErr != nil {
			return creditcard.BulkProjectionResult{}, loadErr
		}
		if bill.BulkAttemptGeneration != attemptGeneration {
			return creditcard.BulkProjectionResult{}, fmt.Errorf("%w: Bulk generation changed after bill creation", creditcard.ErrValidation)
		}
		return creditcard.BulkProjectionResult{Bill: bill}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return creditcard.BulkProjectionResult{}, err
	}

	var rawSummary []byte
	var storedGeneration int
	var documentType string
	var accountID uuid.UUID
	var accountRef, accountType string
	err = t.tx.QueryRow(ctx, `
		select document.document_summary, document.attempt_generation,
			batch.document_type_snapshot, selected.account_id,
			selected.account_ref, selected.account_type
		from private.bulk_import_documents document
		join public.bulk_import_batches batch
			on batch.id = document.batch_id and batch.user_id = document.user_id
		join private.bulk_import_batch_accounts selected
			on selected.batch_id = batch.id and selected.user_id = batch.user_id
		where document.id = $1 and document.user_id = $2
		for update of document`, documentID, userID).
		Scan(&rawSummary, &storedGeneration, &documentType, &accountID, &accountRef, &accountType)
	if err != nil {
		return creditcard.BulkProjectionResult{}, mapNotFound(err)
	}
	if storedGeneration != attemptGeneration || documentType != "credit_card_bill" || accountType != "credit_card" {
		return creditcard.BulkProjectionResult{}, fmt.Errorf("%w: invalid Credit Card Bulk generation", creditcard.ErrValidation)
	}
	var summary bulkparse.BillSummary
	if len(rawSummary) == 0 || string(rawSummary) == "null" {
		rawSummary = []byte(`{}`)
	}
	if err = json.Unmarshal(rawSummary, &summary); err != nil {
		return creditcard.BulkProjectionResult{}, fmt.Errorf("decode validated bill summary: %w", err)
	}
	// Contradictory document Account evidence is never allowed to retag the
	// selected Account. Leaving the header incomplete keeps the bill in Review.
	if summary.CardAccountRef != nil && *summary.CardAccountRef != accountRef {
		summary = bulkparse.BillSummary{}
	}
	periodStart, err := parseOptionalDate(summary.PeriodStart)
	if err != nil {
		return creditcard.BulkProjectionResult{}, err
	}
	periodEnd, err := parseOptionalDate(summary.PeriodEnd)
	if err != nil {
		return creditcard.BulkProjectionResult{}, err
	}
	statementDate, err := parseOptionalDate(summary.StatementDate)
	if err != nil {
		return creditcard.BulkProjectionResult{}, err
	}
	dueDate, err := parseOptionalDate(summary.DueDate)
	if err != nil {
		return creditcard.BulkProjectionResult{}, err
	}
	var failedCandidates, failedChunks int
	if err = t.tx.QueryRow(ctx, `select (select count(*)::int from private.source_candidates where user_id=$1 and document_id=$2 and attempt_generation=$3 and status='failed'),(select count(*)::int from private.bulk_import_chunks where user_id=$1 and document_id=$2 and attempt_generation=$3 and status='failed')`, userID, documentID, attemptGeneration).Scan(&failedCandidates, &failedChunks); err != nil {
		return creditcard.BulkProjectionResult{}, err
	}
	unresolvedCandidateCount := unresolvedBulkCount(failedCandidates, failedChunks)
	var billID uuid.UUID
	err = t.tx.QueryRow(ctx, `
		insert into private.credit_card_statements (
			user_id, account_id, bulk_document_id, bulk_attempt_generation,
			period_start, period_end, statement_date, due_date, settlement_currency,
			amount_due_minor, minimum_payment_minor, previous_balance_minor, unresolved_candidate_count, status
		) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, 'review')
		returning id`, userID, accountID, documentID, attemptGeneration,
		periodStart, periodEnd, statementDate, dueDate, summary.SettlementCurrency,
		summary.AmountDueMinor, summary.MinimumPaymentMinor, summary.PreviousBalanceMinor, unresolvedCandidateCount).Scan(&billID)
	if err != nil {
		return creditcard.BulkProjectionResult{}, err
	}
	projectionHasFailures, err := t.projectLines(ctx, userID, billID, documentID, attemptGeneration)
	if err != nil {
		return creditcard.BulkProjectionResult{}, err
	}
	bill, err := loadBill(ctx, t.tx, userID, billID, false)
	if err != nil {
		return creditcard.BulkProjectionResult{}, err
	}
	bill.ProjectionHasFailures = projectionHasFailures || unresolvedCandidateCount > 0
	return creditcard.BulkProjectionResult{Bill: bill, Created: true}, nil
}

func unresolvedBulkCount(failedCandidates, failedChunks int) int {
	return failedCandidates + failedChunks
}

func (t *transaction) projectLines(ctx context.Context, userID, billID, documentID uuid.UUID, generation int) (bool, error) {
	rows, err := t.tx.Query(ctx, `
		select id, fingerprint, parsed_candidate, status, transaction_id
		from private.source_candidates
		where user_id = $1 and document_id = $2 and attempt_generation = $3
		order by output_ordinal
		for update`, userID, documentID, generation)
	if err != nil {
		return false, err
	}
	type projected struct {
		id          uuid.UUID
		fingerprint []byte
		candidate   storedCandidate
		status      string
		transaction *uuid.UUID
	}
	items := make([]projected, 0)
	projectionHasFailures := false
	for rows.Next() {
		var item projected
		var raw []byte
		if err := rows.Scan(&item.id, &item.fingerprint, &raw, &item.status, &item.transaction); err != nil {
			rows.Close()
			return false, err
		}
		if err := json.Unmarshal(raw, &item.candidate); err != nil {
			rows.Close()
			return false, fmt.Errorf("decode pinned bill candidate: %w", err)
		}
		item.candidate.normalizeLegacyKeys()
		include, failed, statusErr := projectCandidateStatus(item.status)
		if statusErr != nil {
			rows.Close()
			return false, statusErr
		}
		if failed {
			projectionHasFailures = true
		}
		if !include {
			continue
		}
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	for _, item := range items {
		occurredOn, err := time.Parse("2006-01-02", item.candidate.OccurredOn)
		if err != nil {
			return false, fmt.Errorf("decode pinned bill candidate date: %w", err)
		}
		occurredAt := time.Date(occurredOn.Year(), occurredOn.Month(), occurredOn.Day(), 12, 0, 0, 0, time.UTC)
		status := "pending"
		transactionID := item.transaction
		if transactionID != nil && (item.status == "created" || item.status == "attached") {
			status = "linked"
		} else {
			transactionID = nil
		}
		if _, err = t.tx.Exec(ctx, `
			insert into private.credit_card_statement_lines (
				user_id, statement_id, bulk_candidate_id, line_index, line_kind,
				line_fingerprint, description, occurred_on, occurred_at, time_precision,
				amount_minor, currency, resolution_status, transaction_id
			) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'date', $10, $11, $12, $13)`,
			userID, billID, item.id, item.candidate.LineIndex, item.candidate.LineKind,
			item.fingerprint, strings.TrimSpace(item.candidate.Title), occurredOn, occurredAt,
			item.candidate.OriginalAmountMinor, item.candidate.OriginalCurrency, status, transactionID); err != nil {
			return false, err
		}
	}
	return projectionHasFailures, nil
}

func projectCandidateStatus(status string) (include, failed bool, err error) {
	switch status {
	case "created", "attached", "review_required":
		return true, false, nil
	case "duplicate", "cancelled", "superseded":
		return false, false, nil
	case "failed":
		return false, true, nil
	case "pending_reconciliation":
		return false, false, fmt.Errorf("%w: Bulk candidates are not terminal", creditcard.ErrValidation)
	default:
		return false, false, fmt.Errorf("%w: unsupported Bulk candidate status", creditcard.ErrValidation)
	}
}

func (t *transaction) GetLineForUpdate(ctx context.Context, userID, billID, lineID uuid.UUID) (creditcard.BillLine, error) {
	var id uuid.UUID
	if err := t.tx.QueryRow(ctx, `
		select id from private.credit_card_statement_lines
		where id = $1 and statement_id = $2 and user_id = $3 for update`, lineID, billID, userID).Scan(&id); err != nil {
		return creditcard.BillLine{}, mapNotFound(err)
	}
	lines, err := loadLines(ctx, t.tx, userID, billID)
	if err != nil {
		return creditcard.BillLine{}, err
	}
	for _, line := range lines {
		if line.ID == lineID {
			return line, nil
		}
	}
	return creditcard.BillLine{}, creditcard.ErrNotFound
}

func (t *transaction) GetTransactionForUpdate(ctx context.Context, userID, transactionID uuid.UUID) (creditcard.CanonicalTransaction, error) {
	return loadTransaction(ctx, t.tx, userID, transactionID, true)
}

func (t *transaction) SaveBill(ctx context.Context, userID uuid.UUID, bill creditcard.Bill, expectedVersion int) (creditcard.Bill, error) {
	var updatedAt time.Time
	err := t.tx.QueryRow(ctx, `
		update private.credit_card_statements set
			period_start = $3, period_end = $4, statement_date = $5, due_date = $6,
			settlement_currency = $7, amount_due_minor = $8, status = $9,
			payoff_transaction_link_id = $10, version = $11, void_reason = $12, paid_at = $13
		where id = $1 and user_id = $2 and version = $14
		returning updated_at`, bill.ID, userID, bill.PeriodStart, bill.PeriodEnd,
		bill.StatementDate, bill.DueDate, bill.SettlementCurrency, bill.AmountDueMinor,
		bill.Status, bill.PayoffTransferID, bill.Version, bill.VoidReason, bill.PaidAt, expectedVersion).Scan(&updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return creditcard.Bill{}, creditcard.ErrVersionConflict
	}
	if err != nil {
		return creditcard.Bill{}, err
	}
	bill.UpdatedAt = updatedAt
	if err = t.syncPaymentCandidates(ctx, userID, bill); err != nil {
		return creditcard.Bill{}, err
	}
	return bill, nil
}

func (t *transaction) syncPaymentCandidates(ctx context.Context, userID uuid.UUID, bill creditcard.Bill) error {
	keep := append([]uuid.UUID(nil), bill.AmbiguousPaymentCandidates...)
	if bill.PaymentCandidateTransactionID != nil {
		keep = append(keep, *bill.PaymentCandidateTransactionID)
	}
	if t.confirmedPaymentCandidate != nil {
		keep = append(keep, *t.confirmedPaymentCandidate)
	}
	keepArray := database.UUIDArrayLiteral(keep)
	if _, err := t.tx.Exec(ctx, `
		update private.credit_card_statement_payment_candidates
		set status = 'dismissed', selected_at = null, confirmed_at = null
		where user_id = $1 and statement_id = $2 and status <> 'confirmed'
			and not (bank_transaction_id = any($3::uuid[]))`, userID, bill.ID, keepArray); err != nil {
		return err
	}
	for _, candidateID := range bill.AmbiguousPaymentCandidates {
		if err := t.upsertPaymentCandidate(ctx, userID, bill.ID, candidateID, "suggested"); err != nil {
			return err
		}
	}
	if bill.PaymentCandidateTransactionID != nil {
		status := "suggested"
		if bill.PaymentCandidateSelected {
			status = "selected"
		}
		if err := t.upsertPaymentCandidate(ctx, userID, bill.ID, *bill.PaymentCandidateTransactionID, status); err != nil {
			return err
		}
	}
	if t.confirmedPaymentCandidate != nil {
		_, err := t.tx.Exec(ctx, `
			update private.credit_card_statement_payment_candidates
			set status = 'confirmed', selected_at = coalesce(selected_at, now()), confirmed_at = now()
			where user_id = $1 and statement_id = $2 and bank_transaction_id = $3`, userID, bill.ID, *t.confirmedPaymentCandidate)
		return err
	}
	return nil
}

func (t *transaction) upsertPaymentCandidate(ctx context.Context, userID, billID, transactionID uuid.UUID, status string) error {
	_, err := t.tx.Exec(ctx, `
		insert into private.credit_card_statement_payment_candidates
			(user_id, statement_id, bank_transaction_id, status, reason, selected_at)
		values ($1, $2, $3, $4, 'Exact amount, currency, date window, and Card-payment evidence',
			case when $4 = 'selected' then now() else null end)
		on conflict (statement_id, bank_transaction_id) do update set
			status = excluded.status,
			selected_at = case when excluded.status = 'selected' then coalesce(private.credit_card_statement_payment_candidates.selected_at, now()) else null end,
			confirmed_at = null,
			reason = excluded.reason`, userID, billID, transactionID, status)
	return err
}

func (t *transaction) SaveLine(ctx context.Context, userID uuid.UUID, line creditcard.BillLine) (creditcard.BillLine, error) {
	err := t.tx.QueryRow(ctx, `
		update private.credit_card_statement_lines set
			resolution_status = $4, resolution_reason = $5,
			link_exception_reason = $6, transaction_id = $7
		where id = $1 and statement_id = $2 and user_id = $3
		returning updated_at`, line.ID, line.BillID, userID, line.Status,
		line.ResolutionReason, line.LinkExceptionReason, line.TransactionID).Scan(&line.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return creditcard.BillLine{}, creditcard.ErrNotFound
	}
	if err != nil {
		if isUniqueViolation(err) {
			return creditcard.BillLine{}, creditcard.ErrDuplicateTransaction
		}
		return creditcard.BillLine{}, err
	}
	return line, nil
}

func (t *transaction) AppendBillEvent(ctx context.Context, userID uuid.UUID, event creditcard.BillEvent) error {
	details := make(map[string]string, len(event.Details)+1)
	for key, value := range event.Details {
		details[key] = value
	}
	if event.Reason != "" {
		details["reason"] = event.Reason
	}
	raw, err := json.Marshal(details)
	if err != nil {
		return err
	}
	_, err = t.tx.Exec(ctx, `
		insert into private.credit_card_statement_events
			(id, user_id, statement_id, event_index, event_type, actor_user_id, from_status, to_status, details, created_at)
		select $1, $2, $3, coalesce(max(event_index), 0) + 1, $4, $2, $5, $6, $7::jsonb, $8
		from private.credit_card_statement_events
		where user_id = $2 and statement_id = $3`, event.ID, userID, event.BillID, event.Kind,
		event.FromStatus, event.ToStatus, string(raw), event.CreatedAt)
	return err
}

func (t *transaction) DeleteReviewBill(ctx context.Context, userID, billID uuid.UUID, expectedVersion int) error {
	command, err := t.tx.Exec(ctx, `
		delete from private.credit_card_statements
		where id = $1 and user_id = $2 and status = 'review' and version = $3`, billID, userID, expectedVersion)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		return creditcard.ErrVersionConflict
	}
	return nil
}

func (t *transaction) IsTransactionLinkedToAnotherLine(ctx context.Context, userID, transactionID, lineID uuid.UUID) (bool, error) {
	var linked bool
	err := t.tx.QueryRow(ctx, `
		select exists(
			select 1 from private.credit_card_statement_lines
			where user_id = $1 and transaction_id = $2 and id <> $3
		)`, userID, transactionID, lineID).Scan(&linked)
	return linked, err
}

type storedCandidate struct {
	BillLineIndex       int                       `json:"bill_line_index"`
	BillLineKind        string                    `json:"bill_line_kind"`
	LegacyLineIndex     int                       `json:"line_index"`
	LegacyLineKind      string                    `json:"line_kind"`
	LineIndex           int                       `json:"-"`
	LineKind            string                    `json:"-"`
	TransactionKind     string                    `json:"transaction_kind"`
	Title               string                    `json:"title"`
	MerchantName        string                    `json:"merchant_name"`
	OriginalAmountMinor int64                     `json:"original_amount_minor"`
	OriginalCurrency    string                    `json:"original_currency"`
	SGDAmountMinor      *int64                    `json:"sgd_amount_minor"`
	OccurredOn          string                    `json:"occurred_on"`
	TimePrecision       string                    `json:"time_precision"`
	References          []string                  `json:"references"`
	LineItems           []bulkparse.LineItem      `json:"line_items"`
	AccountEvidence     bulkparse.AccountEvidence `json:"account_evidence"`
}

func (c *storedCandidate) normalizeLegacyKeys() {
	c.LineIndex, c.LineKind = c.BillLineIndex, c.BillLineKind
	if c.LineIndex == 0 {
		c.LineIndex = c.LegacyLineIndex
	}
	if c.LineKind == "" {
		c.LineKind = c.LegacyLineKind
	}
}

func parseOptionalDate(value *string) (*time.Time, error) {
	if value == nil {
		return nil, nil
	}
	parsed, err := time.Parse("2006-01-02", *value)
	if err != nil {
		return nil, fmt.Errorf("decode validated bill date: %w", err)
	}
	return &parsed, nil
}

func isUniqueViolation(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == "23505"
}

var _ creditcard.Tx = (*transaction)(nil)

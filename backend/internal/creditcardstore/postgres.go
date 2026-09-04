// Package creditcardstore persists Credit Card bill state in PostgreSQL.
package creditcardstore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zhengteck/wealth-builder/backend/internal/creditcard"
)

type Store struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) ListBills(ctx context.Context, userID, accountID uuid.UUID, cursor *string, limit int) (creditcard.BillPage, error) {
	cutoffDate := time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)
	cutoffID := uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff")
	if cursor != nil {
		decoded, err := decodeCursor(*cursor)
		if err != nil {
			return creditcard.BillPage{}, fmt.Errorf("%w: invalid bill cursor", creditcard.ErrValidation)
		}
		cutoffDate, cutoffID = decoded.PeriodEnd, decoded.ID
	}
	rows, err := s.pool.Query(ctx, `
		select id, account_id, period_start, period_end, statement_date, due_date,
			settlement_currency, amount_due_minor, unresolved_candidate_count, status, version, updated_at
		from private.credit_card_statements
		where user_id = $1 and account_id = $2
			and (coalesce(period_end, date '0001-01-01'), id) < ($3::date, $4)
		order by coalesce(period_end, date '0001-01-01') desc, id desc
		limit $5`, userID, accountID, cutoffDate, cutoffID, limit+1)
	if err != nil {
		return creditcard.BillPage{}, err
	}
	defer rows.Close()
	page := creditcard.BillPage{Bills: make([]creditcard.BillSummary, 0, limit)}
	for rows.Next() {
		var item creditcard.BillSummary
		if err := rows.Scan(&item.ID, &item.AccountID, &item.PeriodStart, &item.PeriodEnd, &item.StatementDate,
			&item.DueDate, &item.SettlementCurrency, &item.AmountDueMinor, &item.UnresolvedCandidateCount, &item.Status, &item.Version, &item.UpdatedAt); err != nil {
			return creditcard.BillPage{}, err
		}
		page.Bills = append(page.Bills, item)
	}
	if err := rows.Err(); err != nil {
		return creditcard.BillPage{}, err
	}
	if len(page.Bills) > limit {
		last := page.Bills[limit-1]
		periodEnd := time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)
		if last.PeriodEnd != nil {
			periodEnd = *last.PeriodEnd
		}
		encoded := encodeCursor(billCursor{PeriodEnd: periodEnd, ID: last.ID})
		page.NextCursor = &encoded
		page.Bills = page.Bills[:limit]
	}
	return page, nil
}

func (s *Store) GetBill(ctx context.Context, userID, billID uuid.UUID) (creditcard.Bill, error) {
	return loadBill(ctx, s.pool, userID, billID, false)
}

func (s *Store) Transact(ctx context.Context, callback func(creditcard.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	adapter := &transaction{tx: tx}
	if err = callback(adapter); err != nil {
		return err
	}
	return mapError(tx.Commit(ctx))
}

type querier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func loadBill(ctx context.Context, database querier, userID, billID uuid.UUID, lock bool) (creditcard.Bill, error) {
	query := `
		select id, account_id, bulk_document_id, bulk_attempt_generation,
			period_start, period_end, statement_date, due_date, settlement_currency,
			amount_due_minor, minimum_payment_minor, previous_balance_minor, unresolved_candidate_count,
			status, payoff_transaction_link_id, version, void_reason,
			created_at, updated_at, paid_at
		from private.credit_card_statements
		where id = $1 and user_id = $2`
	if lock {
		query += " for update"
	}
	var bill creditcard.Bill
	err := database.QueryRow(ctx, query, billID, userID).Scan(
		&bill.ID, &bill.AccountID, &bill.BulkDocumentID, &bill.BulkAttemptGeneration,
		&bill.PeriodStart, &bill.PeriodEnd, &bill.StatementDate, &bill.DueDate, &bill.SettlementCurrency,
		&bill.AmountDueMinor, &bill.MinimumPaymentMinor, &bill.PreviousBalanceMinor, &bill.UnresolvedCandidateCount,
		&bill.Status, &bill.PayoffTransferID, &bill.Version, &bill.VoidReason,
		&bill.CreatedAt, &bill.UpdatedAt, &bill.PaidAt,
	)
	if err != nil {
		return creditcard.Bill{}, mapNotFound(err)
	}
	bill.EvidenceURL = "/v1/bulk-import/documents/" + bill.BulkDocumentID.String()
	if bill.Lines, err = loadLines(ctx, database, userID, bill.ID); err != nil {
		return creditcard.Bill{}, err
	}
	if err = loadPaymentCandidates(ctx, database, userID, &bill); err != nil {
		return creditcard.Bill{}, err
	}
	if bill.Events, err = loadEvents(ctx, database, userID, bill.ID); err != nil {
		return creditcard.Bill{}, err
	}
	return bill, nil
}

func loadLines(ctx context.Context, database querier, userID, billID uuid.UUID) ([]creditcard.BillLine, error) {
	rows, err := database.Query(ctx, `
		select line.id, line.bulk_candidate_id, line.line_kind, line.resolution_status,
			line.resolution_reason, line.link_exception_reason, line.line_index,
			line.occurred_on, line.occurred_at, line.time_precision,
			line.description, line.amount_minor, line.currency,
			line.transaction_id, line.created_at, line.updated_at,
			txn.account_id, account.account_type, txn.transaction_kind,
			txn.original_currency, txn.original_amount_minor, txn.occurred_at, txn.time_precision,
			link.id, link.debit_transaction_id, link.credit_transaction_id,
			debit.account_id, debit_account.account_type,
			credit.account_id, debit.original_currency, debit.original_amount_minor, debit.occurred_at
		from private.credit_card_statement_lines line
		left join public.transactions txn
			on txn.id = line.transaction_id and txn.user_id = line.user_id
		left join public.accounts account
			on account.id = txn.account_id and account.user_id = txn.user_id
		left join private.transaction_links link
			on link.user_id = line.user_id
			and line.transaction_id in (link.debit_transaction_id, link.credit_transaction_id)
		left join public.transactions debit
			on debit.id = link.debit_transaction_id and debit.user_id = link.user_id
		left join public.accounts debit_account
			on debit_account.id = debit.account_id and debit_account.user_id = debit.user_id
		left join public.transactions credit
			on credit.id = link.credit_transaction_id and credit.user_id = link.user_id
		where line.user_id = $1 and line.statement_id = $2
		order by line.line_index`, userID, billID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]creditcard.BillLine, 0)
	for rows.Next() {
		line := creditcard.BillLine{BillID: billID}
		var accountID *uuid.UUID
		var accountType, direction, currency *string
		var amount *int64
		var occurredAt *time.Time
		var transactionTimePrecision *string
		var transferID, debitID, creditID, debitAccountID, creditAccountID *uuid.UUID
		var debitAccountType, transferCurrency *string
		var transferAmount *int64
		var transferOccurredAt *time.Time
		if err := rows.Scan(
			&line.ID, &line.BulkCandidateID, &line.Kind, &line.Status,
			&line.ResolutionReason, &line.LinkExceptionReason, &line.Index,
			&line.OccurredOn, &line.OccurredAt, &line.TimePrecision,
			&line.Description, &line.AmountMinor, &line.Currency,
			&line.TransactionID, &line.CreatedAt, &line.UpdatedAt,
			&accountID, &accountType, &direction, &currency, &amount, &occurredAt, &transactionTimePrecision,
			&transferID, &debitID, &creditID, &debitAccountID, &debitAccountType,
			&creditAccountID, &transferCurrency, &transferAmount, &transferOccurredAt,
		); err != nil {
			return nil, err
		}
		if line.TransactionID != nil && accountID != nil && accountType != nil && direction != nil && currency != nil && amount != nil && occurredAt != nil && transactionTimePrecision != nil {
			transaction := creditcard.CanonicalTransaction{ID: *line.TransactionID, AccountID: *accountID, AccountType: *accountType, Direction: creditcard.TransactionDirection(*direction), OriginalCurrency: *currency, OriginalAmountMinor: *amount, OccurredAt: *occurredAt, TimePrecision: *transactionTimePrecision}
			if transferID != nil && debitID != nil && creditID != nil && debitAccountID != nil && debitAccountType != nil && creditAccountID != nil && transferCurrency != nil && transferAmount != nil && transferOccurredAt != nil {
				transaction.Transfer = &creditcard.InternalTransfer{ID: *transferID, DebitTransactionID: *debitID, CreditTransactionID: *creditID, DebitAccountID: *debitAccountID, DebitAccountType: *debitAccountType, CreditAccountID: *creditAccountID, Currency: *transferCurrency, AmountMinor: *transferAmount, OccurredAt: *transferOccurredAt}
			}
			line.Transaction = &transaction
		}
		result = append(result, line)
	}
	return result, rows.Err()
}

func loadPaymentCandidates(ctx context.Context, database querier, userID uuid.UUID, bill *creditcard.Bill) error {
	rows, err := database.Query(ctx, `
		select bank_transaction_id, status
		from private.credit_card_statement_payment_candidates
		where user_id = $1 and statement_id = $2 and status in ('suggested', 'selected')
		order by created_at, id`, userID, bill.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	type candidate struct {
		id     uuid.UUID
		status string
	}
	items := make([]candidate, 0)
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.id, &item.status); err != nil {
			return err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, item := range items {
		if item.status == "selected" {
			bill.PaymentCandidateTransactionID = &item.id
			bill.PaymentCandidateSelected = true
			return nil
		}
	}
	if len(items) == 1 {
		bill.PaymentCandidateTransactionID = &items[0].id
	} else {
		for _, item := range items {
			bill.AmbiguousPaymentCandidates = append(bill.AmbiguousPaymentCandidates, item.id)
		}
	}
	return nil
}

func loadEvents(ctx context.Context, database querier, userID, billID uuid.UUID) ([]creditcard.BillEvent, error) {
	rows, err := database.Query(ctx, `
		select id, event_type, from_status, to_status, details, created_at
		from private.credit_card_statement_events
		where user_id = $1 and statement_id = $2
		order by event_index desc`, userID, billID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]creditcard.BillEvent, 0)
	for rows.Next() {
		var item creditcard.BillEvent
		var raw []byte
		if err := rows.Scan(&item.ID, &item.Kind, &item.FromStatus, &item.ToStatus, &raw, &item.CreatedAt); err != nil {
			return nil, err
		}
		item.BillID = billID
		var values map[string]any
		if err := json.Unmarshal(raw, &values); err != nil {
			return nil, err
		}
		item.Details = make(map[string]string, len(values))
		for key, value := range values {
			item.Details[key] = fmt.Sprint(value)
		}
		item.Reason = item.Details["reason"]
		result = append(result, item)
	}
	return result, rows.Err()
}

type billCursor struct {
	PeriodEnd time.Time `json:"period_end"`
	ID        uuid.UUID `json:"id"`
}

func encodeCursor(cursor billCursor) string {
	raw, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeCursor(value string) (billCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return billCursor{}, err
	}
	var cursor billCursor
	if err = json.Unmarshal(raw, &cursor); err != nil || cursor.ID == uuid.Nil || cursor.PeriodEnd.IsZero() {
		return billCursor{}, errors.New("invalid cursor")
	}
	return cursor, nil
}

func mapNotFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return creditcard.ErrNotFound
	}
	return mapError(err)
}

func mapError(err error) error {
	if err == nil {
		return nil
	}
	return err
}

var _ creditcard.Repository = (*Store)(nil)

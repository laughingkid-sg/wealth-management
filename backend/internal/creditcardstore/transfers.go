package creditcardstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/zhengteck/wealth-builder/backend/internal/accountbalancestore"
	"github.com/zhengteck/wealth-builder/backend/internal/creditcard"
	"github.com/zhengteck/wealth-builder/backend/internal/database"
)

func loadTransaction(ctx context.Context, tx pgx.Tx, userID, transactionID uuid.UUID, lock bool) (creditcard.CanonicalTransaction, error) {
	query := `
		select txn.id, txn.account_id, account.account_type,
			txn.transaction_kind, txn.original_currency,
			txn.original_amount_minor, txn.occurred_at, txn.time_precision,
			link.id, link.debit_transaction_id, link.credit_transaction_id,
			debit.account_id, debit_account.account_type, credit.account_id,
			debit.original_currency, debit.original_amount_minor, debit.occurred_at
		from public.transactions txn
		join public.accounts account
			on account.id = txn.account_id and account.user_id = txn.user_id
		left join private.transaction_links link
			on link.user_id = txn.user_id
			and txn.id in (link.debit_transaction_id, link.credit_transaction_id)
		left join public.transactions debit
			on debit.id = link.debit_transaction_id and debit.user_id = link.user_id
		left join public.accounts debit_account
			on debit_account.id = debit.account_id and debit_account.user_id = debit.user_id
		left join public.transactions credit
			on credit.id = link.credit_transaction_id and credit.user_id = link.user_id
		where txn.id = $1 and txn.user_id = $2`
	if lock {
		query += " for update of txn"
	}
	var result creditcard.CanonicalTransaction
	var transferID, debitID, creditID, debitAccountID, creditAccountID *uuid.UUID
	var debitAccountType, currency *string
	var amount *int64
	var occurredAt *time.Time
	err := tx.QueryRow(ctx, query, transactionID, userID).Scan(
		&result.ID, &result.AccountID, &result.AccountType, &result.Direction,
		&result.OriginalCurrency, &result.OriginalAmountMinor, &result.OccurredAt, &result.TimePrecision,
		&transferID, &debitID, &creditID, &debitAccountID, &debitAccountType,
		&creditAccountID, &currency, &amount, &occurredAt,
	)
	if err != nil {
		return creditcard.CanonicalTransaction{}, mapNotFound(err)
	}
	if transferID != nil && debitID != nil && creditID != nil && debitAccountID != nil && debitAccountType != nil && creditAccountID != nil && currency != nil && amount != nil && occurredAt != nil {
		result.Transfer = &creditcard.InternalTransfer{ID: *transferID, DebitTransactionID: *debitID, CreditTransactionID: *creditID, DebitAccountID: *debitAccountID, DebitAccountType: *debitAccountType, CreditAccountID: *creditAccountID, Currency: *currency, AmountMinor: *amount, OccurredAt: *occurredAt}
	}
	return result, nil
}

func (t *transaction) CreateTransactionFromPinnedCandidate(ctx context.Context, userID, billID, lineID, categoryID uuid.UUID) (creditcard.LineCreateResult, error) {
	var candidateID, accountID, documentID, sourceID uuid.UUID
	var generation int
	var lineKind, lineStatus, candidateStatus string
	var rawCandidate []byte
	err := t.tx.QueryRow(ctx, `
		select line.bulk_candidate_id, line.line_kind, line.resolution_status,
			statement.account_id, statement.bulk_document_id, statement.bulk_attempt_generation,
			candidate.data_source_id, candidate.status, candidate.parsed_candidate
		from private.credit_card_statement_lines line
		join private.credit_card_statements statement
			on statement.id = line.statement_id and statement.user_id = line.user_id
		join private.bulk_import_candidates candidate
			on candidate.id = line.bulk_candidate_id and candidate.user_id = line.user_id
		where line.id = $1 and line.statement_id = $2 and line.user_id = $3
		for update of line, candidate`, lineID, billID, userID).
		Scan(&candidateID, &lineKind, &lineStatus, &accountID, &documentID, &generation,
			&sourceID, &candidateStatus, &rawCandidate)
	if err != nil {
		return creditcard.LineCreateResult{}, mapNotFound(err)
	}
	if lineStatus != "pending" || (lineKind != "activity" && lineKind != "refund" && lineKind != "fee" && lineKind != "interest") ||
		candidateStatus != "review_required" {
		return creditcard.LineCreateResult{}, fmt.Errorf("%w: pinned candidate is not creatable", creditcard.ErrValidation)
	}
	var categoryExists bool
	if err = t.tx.QueryRow(ctx, `select exists(select 1 from public.transaction_categories where id = $1 and active)`, categoryID).Scan(&categoryExists); err != nil {
		return creditcard.LineCreateResult{}, err
	}
	if !categoryExists {
		return creditcard.LineCreateResult{}, fmt.Errorf("%w: category not found", creditcard.ErrValidation)
	}
	var candidate storedCandidate
	if err = json.Unmarshal(rawCandidate, &candidate); err != nil {
		return creditcard.LineCreateResult{}, fmt.Errorf("decode pinned bill candidate: %w", err)
	}
	candidate.normalizeLegacyKeys()
	if candidate.LineKind != lineKind || candidate.LineIndex < 1 || candidate.OriginalAmountMinor <= 0 {
		return creditcard.LineCreateResult{}, fmt.Errorf("%w: pinned candidate no longer matches the bill line", creditcard.ErrValidation)
	}
	direction := "debit"
	if lineKind == "refund" {
		direction = "credit"
	}
	occurredOn, err := time.Parse("2006-01-02", candidate.OccurredOn)
	if err != nil {
		return creditcard.LineCreateResult{}, fmt.Errorf("decode pinned candidate date: %w", err)
	}
	occurredAt := time.Date(occurredOn.Year(), occurredOn.Month(), occurredOn.Day(), 12, 0, 0, 0, time.UTC)
	lineItems, err := json.Marshal(candidate.LineItems)
	if err != nil {
		return creditcard.LineCreateResult{}, err
	}
	details, err := json.Marshal(map[string]any{"references": candidate.References, "account_evidence": candidate.AccountEvidence, "bulk_document_id": documentID, "bulk_attempt_generation": generation})
	if err != nil {
		return creditcard.LineCreateResult{}, err
	}
	var transactionID uuid.UUID
	err = t.tx.QueryRow(ctx, `
		insert into public.transactions (
			user_id, account_id, transaction_kind, title, merchant_name,
			original_amount_minor, original_currency, sgd_amount_minor, occurred_at,
			time_precision, category_id, line_items, details, review_status,
			creation_method
		) values ($1, $2, $3, $4, nullif($5, ''), $6, $7, $8, $9,
			'date', $10, $11::jsonb, $12::jsonb, 'confirmed', 'credit_card_statement')
		returning id`, userID, accountID, direction, strings.TrimSpace(candidate.Title),
		strings.TrimSpace(candidate.MerchantName), candidate.OriginalAmountMinor,
		candidate.OriginalCurrency, candidate.SGDAmountMinor, occurredAt, categoryID,
		string(lineItems), string(details)).Scan(&transactionID)
	if err != nil {
		return creditcard.LineCreateResult{}, err
	}
	if _, err = t.tx.Exec(ctx, `
		insert into private.transaction_data_sources (
			user_id, transaction_id, data_source_id, role, matched_by, bulk_import_candidate_id
		) values ($1, $2, $3, 'other', 'user', $4)`, userID, transactionID, sourceID, candidateID); err != nil {
		return creditcard.LineCreateResult{}, err
	}
	command, err := t.tx.Exec(ctx, `
		update private.bulk_import_candidates
		set status = 'created', transaction_id = $4, account_id = $5,
			reconciliation_reason = 'Created from reviewed Credit Card bill line'
		where id = $1 and user_id = $2 and document_id = $3
			and attempt_generation = $6 and transaction_id is null
			and status = 'review_required'`,
		candidateID, userID, documentID, transactionID, accountID, generation)
	if err != nil {
		return creditcard.LineCreateResult{}, err
	}
	if command.RowsAffected() != 1 {
		return creditcard.LineCreateResult{}, creditcard.ErrDuplicateTransaction
	}
	created, err := loadTransaction(ctx, t.tx, userID, transactionID, false)
	return creditcard.LineCreateResult{Transaction: created, BulkCandidateOutcomeID: candidateID}, err
}

const findExactPayoffTransfersQuery = `
		select link.id, link.debit_transaction_id, link.credit_transaction_id,
			debit.account_id, debit_account.account_type, credit.account_id,
			debit.original_currency, debit.original_amount_minor, debit.occurred_at
		from private.transaction_links link
		join public.transactions debit
			on debit.id = link.debit_transaction_id and debit.user_id = link.user_id
		join public.accounts debit_account
			on debit_account.id = debit.account_id and debit_account.user_id = debit.user_id
		join public.transactions credit
			on credit.id = link.credit_transaction_id and credit.user_id = link.user_id
		where link.user_id = $1 and link.link_type = 'internal_transfer'
			and debit_account.account_type = 'bank_account' and debit_account.deleted_at is null
			and debit.transaction_kind = 'debit' and credit.transaction_kind = 'credit'
			and credit.account_id = $2
			and debit.original_currency = $3 and credit.original_currency = $3
			and debit.original_amount_minor = $4 and credit.original_amount_minor = $4
			and (debit.occurred_at at time zone 'UTC')::date between $5::date and $6::date
			and (credit.occurred_at at time zone 'UTC')::date between $5::date and $6::date
			and abs(extract(epoch from credit.occurred_at - debit.occurred_at)) <= 600
			and not exists (
				select 1 from private.credit_card_statements other
				where other.user_id = link.user_id and other.payoff_transaction_link_id = link.id
			)
		order by debit.occurred_at, link.id
		for update of link, debit, credit`

func (t *transaction) FindExactPayoffTransfers(ctx context.Context, userID, cardAccountID uuid.UUID, currency string, amount int64, start, end time.Time) ([]creditcard.InternalTransfer, error) {
	rows, err := t.tx.Query(ctx, findExactPayoffTransfersQuery, userID, cardAccountID, currency, amount, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]creditcard.InternalTransfer, 0)
	for rows.Next() {
		var item creditcard.InternalTransfer
		if err := rows.Scan(&item.ID, &item.DebitTransactionID, &item.CreditTransactionID,
			&item.DebitAccountID, &item.DebitAccountType, &item.CreditAccountID,
			&item.Currency, &item.AmountMinor, &item.OccurredAt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (t *transaction) FindBankDebitCandidates(ctx context.Context, userID, billID uuid.UUID, currency string, amount int64, start, end time.Time) ([]creditcard.CanonicalTransaction, error) {
	rows, err := t.tx.Query(ctx, `
		select txn.id
		from private.credit_card_statements statement
		join public.accounts card
			on card.id = statement.account_id and card.user_id = statement.user_id
		join public.transactions txn
			on txn.user_id = statement.user_id
		join public.accounts account
			on account.id = txn.account_id and account.user_id = txn.user_id
		where statement.id = $1 and statement.user_id = $2
			and account.account_type = 'bank_account' and account.deleted_at is null
			and txn.transaction_kind = 'debit' and txn.review_status = 'confirmed'
			and txn.original_currency = $3 and txn.original_amount_minor = $4
			and (txn.occurred_at at time zone 'UTC')::date between $5::date and $6::date
			and not exists (
				select 1 from private.transaction_links link
				where link.user_id = txn.user_id
					and txn.id in (link.debit_transaction_id, link.credit_transaction_id)
			)
			and (
				lower(txn.title || ' ' || coalesce(txn.merchant_name, '') || ' ' || txn.details::text)
					~ '(credit[ -]?card|card[ -]?payment|payoff|statement[ -]?payment)'
				or position(lower(card.institution_name) in lower(txn.title || ' ' || coalesce(txn.merchant_name, '') || ' ' || txn.details::text)) > 0
			)
		order by txn.occurred_at, txn.id
		for update of txn`, billID, userID, currency, amount, start, end)
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, 0)
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := make([]creditcard.CanonicalTransaction, 0, len(ids))
	for _, id := range ids {
		transaction, err := loadTransaction(ctx, t.tx, userID, id, false)
		if err != nil {
			return nil, err
		}
		result = append(result, transaction)
		if err = t.upsertPaymentCandidate(ctx, userID, billID, id, "suggested"); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (t *transaction) CreateMissingCardLegAndTransfer(ctx context.Context, userID, billID, bankTransactionID, cardAccountID uuid.UUID, currency string, amount int64) (creditcard.PayoffResult, error) {
	bank, err := loadTransaction(ctx, t.tx, userID, bankTransactionID, true)
	if err != nil {
		return creditcard.PayoffResult{}, err
	}
	var statementAccount uuid.UUID
	var statementCurrency string
	var statementAmount int64
	var statementDate, dueDate time.Time
	if err = t.tx.QueryRow(ctx, `
		select account_id, settlement_currency, amount_due_minor, statement_date, due_date
		from private.credit_card_statements
		where id = $1 and user_id = $2 for update`, billID, userID).
		Scan(&statementAccount, &statementCurrency, &statementAmount, &statementDate, &dueDate); err != nil {
		return creditcard.PayoffResult{}, mapNotFound(err)
	}
	if bank.AccountType != "bank_account" || bank.Direction != creditcard.DirectionDebit || bank.Transfer != nil ||
		statementAccount != cardAccountID || statementCurrency != currency || statementAmount != amount ||
		bank.OriginalCurrency != currency || bank.OriginalAmountMinor != amount || !withinDate(bank.OccurredAt, statementDate, dueDate) {
		return creditcard.PayoffResult{}, fmt.Errorf("%w: Bank debit is not the current exact suggestion", creditcard.ErrValidation)
	}
	var candidateStatus string
	if err = t.tx.QueryRow(ctx, `
		select status from private.credit_card_statement_payment_candidates
		where user_id = $1 and statement_id = $2 and bank_transaction_id = $3
			and status in ('suggested', 'selected') for update`, userID, billID, bankTransactionID).Scan(&candidateStatus); err != nil {
		return creditcard.PayoffResult{}, mapNotFound(err)
	}
	cardName := "Credit Card"
	_ = t.tx.QueryRow(ctx, `select name from public.accounts where id = $1 and user_id = $2`, cardAccountID, userID).Scan(&cardName)
	precision := bank.TimePrecision
	if precision != "date" {
		precision = "exact"
	}
	creditID, err := insertTransferLeg(ctx, t.tx, userID, cardAccountID, "credit", "Payment to "+cardName, currency, amount, bank.OccurredAt, precision)
	if err != nil {
		return creditcard.PayoffResult{}, err
	}
	transfer, err := insertTransferLink(ctx, t.tx, userID, bankTransactionID, creditID)
	if err != nil {
		return creditcard.PayoffResult{}, err
	}
	transfer.DebitAccountID, transfer.DebitAccountType, transfer.CreditAccountID = bank.AccountID, bank.AccountType, cardAccountID
	transfer.Currency, transfer.AmountMinor, transfer.OccurredAt = currency, amount, bank.OccurredAt
	t.confirmedPaymentCandidate = &bankTransactionID
	return creditcard.PayoffResult{Transfer: transfer}, nil
}

func (t *transaction) CreateFullPayoffTransfer(ctx context.Context, userID, bankAccountID, cardAccountID uuid.UUID, currency string, amount int64, occurredAt time.Time) (creditcard.PayoffResult, error) {
	rows, err := t.tx.Query(ctx, `
		select id, account_type, name, deleted_at
		from public.accounts
		where user_id = $1 and id = any($2::uuid[])
		order by id for update`, userID, database.UUIDArrayLiteral([]uuid.UUID{bankAccountID, cardAccountID}))
	if err != nil {
		return creditcard.PayoffResult{}, err
	}
	type account struct {
		kind    string
		name    string
		deleted *time.Time
	}
	accounts := make(map[uuid.UUID]account, 2)
	for rows.Next() {
		var id uuid.UUID
		var item account
		if err := rows.Scan(&id, &item.kind, &item.name, &item.deleted); err != nil {
			rows.Close()
			return creditcard.PayoffResult{}, err
		}
		accounts[id] = item
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return creditcard.PayoffResult{}, err
	}
	bank, bankOK := accounts[bankAccountID]
	card, cardOK := accounts[cardAccountID]
	if !bankOK || !cardOK || bank.kind != "bank_account" || bank.deleted != nil || card.kind != "credit_card" || amount <= 0 {
		return creditcard.PayoffResult{}, fmt.Errorf("%w: payoff Accounts are not eligible", creditcard.ErrValidation)
	}
	debitID, err := insertTransferLeg(ctx, t.tx, userID, bankAccountID, "debit", "Payment to "+card.name, currency, amount, occurredAt, "exact")
	if err != nil {
		return creditcard.PayoffResult{}, err
	}
	creditID, err := insertTransferLeg(ctx, t.tx, userID, cardAccountID, "credit", "Payment from "+bank.name, currency, amount, occurredAt, "exact")
	if err != nil {
		return creditcard.PayoffResult{}, err
	}
	transfer, err := insertTransferLink(ctx, t.tx, userID, debitID, creditID)
	if err != nil {
		return creditcard.PayoffResult{}, err
	}
	transfer.DebitAccountID, transfer.DebitAccountType, transfer.CreditAccountID = bankAccountID, bank.kind, cardAccountID
	transfer.Currency, transfer.AmountMinor, transfer.OccurredAt = currency, amount, occurredAt
	return creditcard.PayoffResult{Transfer: transfer}, nil
}

func insertTransferLeg(ctx context.Context, tx pgx.Tx, userID, accountID uuid.UUID, direction, title, currency string, amount int64, occurredAt time.Time, precision string) (uuid.UUID, error) {
	var id uuid.UUID
	err := tx.QueryRow(ctx, `
		insert into public.transactions (
			user_id, account_id, transaction_kind, title, original_amount_minor,
			original_currency, occurred_at, time_precision, line_items, details,
			review_status, creation_method
		) values ($1, $2, $3, $4, $5, $6, $7, $8, '[]'::jsonb,
			'{}'::jsonb, 'confirmed', 'internal_transfer')
		returning id`, userID, accountID, direction, title, amount, currency, occurredAt, precision).Scan(&id)
	return id, err
}

func insertTransferLink(ctx context.Context, tx pgx.Tx, userID, debitID, creditID uuid.UUID) (creditcard.InternalTransfer, error) {
	var result creditcard.InternalTransfer
	err := tx.QueryRow(ctx, `
		insert into private.transaction_links
			(user_id, link_type, debit_transaction_id, credit_transaction_id)
		values ($1, 'internal_transfer', $2, $3)
		returning id`, userID, debitID, creditID).Scan(&result.ID)
	result.DebitTransactionID, result.CreditTransactionID = debitID, creditID
	return result, err
}

func (t *transaction) LockSystemPayoffExclusions(ctx context.Context, userID uuid.UUID, transactionIDs []uuid.UUID, reason string) error {
	return accountbalancestore.LockSystemExclusions(ctx, t.tx, userID, transactionIDs, reason)
}

func withinDate(value, start, end time.Time) bool {
	valueDate := time.Date(value.UTC().Year(), value.UTC().Month(), value.UTC().Day(), 0, 0, 0, 0, time.UTC)
	startDate := time.Date(start.UTC().Year(), start.UTC().Month(), start.UTC().Day(), 0, 0, 0, 0, time.UTC)
	endDate := time.Date(end.UTC().Year(), end.UTC().Month(), end.UTC().Day(), 0, 0, 0, 0, time.UTC)
	return !valueDate.Before(startDate) && !valueDate.After(endDate)
}

// Package accountbalancestore persists Account Balances state in PostgreSQL.
package accountbalancestore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zhengteck/wealth-builder/backend/internal/accountbalances"
	"github.com/zhengteck/wealth-builder/backend/internal/database"
	"github.com/zhengteck/wealth-builder/backend/internal/reconciliation"
)

type Store struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) ListFinancialAccounts(ctx context.Context, userID uuid.UUID) ([]accountbalances.FinancialAccount, error) {
	rows, err := s.pool.Query(ctx, `
		select id, name, account_type, side, opening_balances, opening_balance_as_of, opening_balance_version
		from public.accounts
		where user_id = $1 and deleted_at is null
		order by sort_order, created_at, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]accountbalances.FinancialAccount, 0)
	for rows.Next() {
		account, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, account)
	}
	return result, rows.Err()
}

func (s *Store) GetFinancialAccount(ctx context.Context, userID, accountID uuid.UUID) (accountbalances.FinancialAccount, error) {
	account, err := scanAccount(s.pool.QueryRow(ctx, `
		select id, name, account_type, side, opening_balances, opening_balance_as_of, opening_balance_version
		from public.accounts where id = $1 and user_id = $2 and deleted_at is null`, accountID, userID))
	return account, mapNotFound(err)
}

func (s *Store) ListConfirmedMovementsAfter(ctx context.Context, userID, accountID uuid.UUID, after time.Time) ([]accountbalances.ConfirmedMovement, error) {
	rows, err := s.pool.Query(ctx, `
		select id, transaction_kind, original_currency, original_amount_minor, occurred_at
		from public.transactions
		where user_id = $1 and account_id = $2 and review_status = 'confirmed' and occurred_at > $3
		order by occurred_at, id`, userID, accountID, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]accountbalances.ConfirmedMovement, 0)
	for rows.Next() {
		var item accountbalances.ConfirmedMovement
		if err := rows.Scan(&item.TransactionID, &item.Kind, &item.Currency, &item.MinorUnits, &item.OccurredAt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) ReplaceOpeningBalance(ctx context.Context, userID, accountID uuid.UUID, input accountbalances.ReplaceOpeningBalanceParams) (accountbalances.FinancialAccount, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return accountbalances.FinancialAccount{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	replay, err := claimOpeningBalanceIdempotency(ctx, tx, userID, accountID, input)
	if err != nil {
		return accountbalances.FinancialAccount{}, err
	}
	if replay != nil {
		if err = tx.Commit(ctx); err != nil {
			return accountbalances.FinancialAccount{}, err
		}
		return *replay, nil
	}

	current, err := scanAccount(tx.QueryRow(ctx, `
		select id, name, account_type, side, opening_balances, opening_balance_as_of, opening_balance_version
		from public.accounts where id = $1 and user_id = $2 and deleted_at is null for update`, accountID, userID))
	if err != nil {
		return accountbalances.FinancialAccount{}, mapNotFound(err)
	}
	if current.BaselineVersion != input.ExpectedVersion {
		return accountbalances.FinancialAccount{}, &accountbalances.VersionConflictError{Current: current}
	}
	if input.Revision.AccountID != accountID || input.Revision.ChangedByUserID != userID || input.Revision.Version != input.ExpectedVersion+1 {
		return accountbalances.FinancialAccount{}, fmt.Errorf("%w: inconsistent revision identity", accountbalances.ErrValidation)
	}
	_, err = tx.Exec(ctx, `
		insert into private.account_opening_balance_revisions
			(id, user_id, account_id, version, as_of, reason, changed_by_user_id, created_at)
		values ($1, $2, $3, $4, $5, $6, $2, $7)`,
		input.Revision.ID, userID, accountID, input.Revision.Version, input.AsOf,
		input.Revision.CorrectionReason, input.Revision.ChangedAt)
	if err != nil {
		return accountbalances.FinancialAccount{}, err
	}
	for _, amount := range input.Balances {
		if _, err = tx.Exec(ctx, `
			insert into private.account_opening_balance_revision_amounts
				(user_id, revision_id, currency, amount_minor, created_at)
			values ($1, $2, $3, $4, $5)`, userID, input.Revision.ID, amount.Currency, amount.MinorUnits, input.Revision.ChangedAt); err != nil {
			return accountbalances.FinancialAccount{}, err
		}
	}
	projection, err := encodeBalanceProjection(input.Balances)
	if err != nil {
		return accountbalances.FinancialAccount{}, err
	}
	updated, err := scanAccount(tx.QueryRow(ctx, `
		update public.accounts
		set opening_balances = $3::jsonb, opening_balance_as_of = $4, opening_balance_version = $5
		where id = $1 and user_id = $2 and opening_balance_version = $6
		returning id, name, account_type, side, opening_balances, opening_balance_as_of, opening_balance_version`,
		accountID, userID, projection, input.AsOf, input.Revision.Version, input.ExpectedVersion))
	if errors.Is(err, pgx.ErrNoRows) {
		return accountbalances.FinancialAccount{}, &accountbalances.VersionConflictError{Current: current}
	}
	if err != nil {
		return accountbalances.FinancialAccount{}, err
	}
	if err = completeOpeningBalanceIdempotency(ctx, tx, userID, accountID, input, updated); err != nil {
		return accountbalances.FinancialAccount{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return accountbalances.FinancialAccount{}, err
	}
	return updated, nil
}

type openingBalanceIdempotencyResponse struct {
	ID              uuid.UUID                      `json:"id"`
	Name            string                         `json:"name"`
	AccountType     string                         `json:"account_type"`
	Side            accountbalances.AccountSide    `json:"side"`
	Baseline        []openingBalanceResponseAmount `json:"baseline"`
	BaselineAsOf    *time.Time                     `json:"baseline_as_of"`
	BaselineVersion int                            `json:"baseline_version"`
}

type openingBalanceResponseAmount struct {
	Currency   string `json:"currency"`
	MinorUnits int64  `json:"minor_units"`
}

func claimOpeningBalanceIdempotency(ctx context.Context, tx pgx.Tx, userID, accountID uuid.UUID, input accountbalances.ReplaceOpeningBalanceParams) (*accountbalances.FinancialAccount, error) {
	if input.IdempotencyKey == "" || input.RequestHash == "" || input.ExpiresAt.IsZero() {
		return nil, fmt.Errorf("%w: opening balance idempotency metadata is required", accountbalances.ErrValidation)
	}
	keyDigest := sha256.Sum256([]byte(input.IdempotencyKey))
	requestHash, err := decodeSHA256(input.RequestHash)
	if err != nil {
		return nil, err
	}
	var recordID uuid.UUID
	err = tx.QueryRow(ctx, `
		insert into private.api_idempotency_records (
			user_id, operation, key_digest, request_hash, resource_type, resource_id, status, expires_at
		) values ($1, 'set_opening_balance', $2, $3, 'account_opening_balance', $4, 'processing', $5)
		on conflict (user_id, operation, key_digest) do nothing
		returning id`, userID, keyDigest[:], requestHash, accountID, input.ExpiresAt).Scan(&recordID)
	if err == nil {
		return nil, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	var storedHash []byte
	var resourceType *string
	var resourceID *uuid.UUID
	var status string
	var response []byte
	var expired bool
	err = tx.QueryRow(ctx, `
		select id, request_hash, resource_type, resource_id, status, response_body, expires_at <= now()
		from private.api_idempotency_records
		where user_id = $1 and operation = 'set_opening_balance' and key_digest = $2
		for update`, userID, keyDigest[:]).Scan(&recordID, &storedHash, &resourceType, &resourceID, &status, &response, &expired)
	if err != nil {
		return nil, err
	}
	if expired || status == "failed" {
		_, err = tx.Exec(ctx, `
			update private.api_idempotency_records set request_hash=$2, resource_type='account_opening_balance',
				resource_id=$3, status='processing', response_status=null, response_body=null, expires_at=$4
			where id=$1`, recordID, requestHash, accountID, input.ExpiresAt)
		return nil, err
	}
	if !bytes.Equal(storedHash, requestHash) || resourceType == nil || *resourceType != "account_opening_balance" || resourceID == nil || *resourceID != accountID {
		return nil, accountbalances.ErrIdempotencyConflict
	}
	if status == "processing" {
		return nil, accountbalances.ErrIdempotencyInFlight
	}
	if status != "completed" {
		return nil, fmt.Errorf("unknown opening balance idempotency status %q", status)
	}
	account, err := decodeOpeningBalanceResponse(response)
	if err != nil {
		return nil, fmt.Errorf("decode opening balance idempotency replay: %w", err)
	}
	return &account, nil
}

func completeOpeningBalanceIdempotency(ctx context.Context, tx pgx.Tx, userID, accountID uuid.UUID, input accountbalances.ReplaceOpeningBalanceParams, account accountbalances.FinancialAccount) error {
	keyDigest := sha256.Sum256([]byte(input.IdempotencyKey))
	requestHash, err := decodeSHA256(input.RequestHash)
	if err != nil {
		return err
	}
	response, err := encodeOpeningBalanceResponse(account)
	if err != nil {
		return err
	}
	command, err := tx.Exec(ctx, `
		update private.api_idempotency_records
		set status='completed', response_status=200, response_body=$5::jsonb
		where user_id=$1 and operation='set_opening_balance' and key_digest=$2 and request_hash=$3
			and resource_type='account_opening_balance' and resource_id=$4 and status='processing'`,
		userID, keyDigest[:], requestHash, accountID, string(response))
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return accountbalances.ErrIdempotencyConflict
	}
	return nil
}

func encodeOpeningBalanceResponse(account accountbalances.FinancialAccount) ([]byte, error) {
	response := openingBalanceIdempotencyResponse{
		ID: account.ID, Name: account.Name, AccountType: account.AccountType, Side: account.Side,
		BaselineAsOf: account.BaselineAsOf, BaselineVersion: account.BaselineVersion,
		Baseline: make([]openingBalanceResponseAmount, 0, len(account.Baseline)),
	}
	for _, amount := range account.Baseline {
		response.Baseline = append(response.Baseline, openingBalanceResponseAmount{Currency: amount.Currency, MinorUnits: amount.MinorUnits})
	}
	return json.Marshal(response)
}

func decodeOpeningBalanceResponse(raw []byte) (accountbalances.FinancialAccount, error) {
	var response openingBalanceIdempotencyResponse
	if len(raw) == 0 {
		return accountbalances.FinancialAccount{}, errors.New("missing stored response")
	}
	if err := json.Unmarshal(raw, &response); err != nil || response.ID == uuid.Nil || response.BaselineVersion < 1 {
		return accountbalances.FinancialAccount{}, errors.New("invalid stored response")
	}
	account := accountbalances.FinancialAccount{ID: response.ID, Name: response.Name, AccountType: response.AccountType, Side: response.Side, BaselineAsOf: response.BaselineAsOf, BaselineVersion: response.BaselineVersion}
	for _, amount := range response.Baseline {
		account.Baseline = append(account.Baseline, accountbalances.BalanceAmount{Currency: amount.Currency, MinorUnits: amount.MinorUnits})
	}
	return account, nil
}

func decodeSHA256(value string) ([]byte, error) {
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size {
		return nil, fmt.Errorf("%w: invalid request hash", accountbalances.ErrValidation)
	}
	return decoded, nil
}

func (s *Store) ListOpeningBalanceRevisions(ctx context.Context, userID, accountID uuid.UUID) ([]accountbalances.OpeningBalanceRevision, error) {
	rows, err := s.pool.Query(ctx, `
		select revision.id, revision.version, revision.as_of, revision.reason,
			revision.changed_by_user_id, revision.created_at,
			amount.currency, amount.amount_minor
		from private.account_opening_balance_revisions revision
		join private.account_opening_balance_revision_amounts amount
			on amount.revision_id = revision.id and amount.user_id = revision.user_id
		where revision.user_id = $1 and revision.account_id = $2
		order by revision.version, amount.currency`, userID, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]accountbalances.OpeningBalanceRevision, 0)
	for rows.Next() {
		var header accountbalances.OpeningBalanceRevision
		var amount accountbalances.BalanceAmount
		if err := rows.Scan(&header.ID, &header.Version, &header.AsOf, &header.CorrectionReason, &header.ChangedByUserID, &header.ChangedAt, &amount.Currency, &amount.MinorUnits); err != nil {
			return nil, err
		}
		header.AccountID = accountID
		if len(result) == 0 || result[len(result)-1].ID != header.ID {
			result = append(result, header)
		}
		result[len(result)-1].Balances = append(result[len(result)-1].Balances, amount)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(result) == 0 {
		var exists bool
		if err := s.pool.QueryRow(ctx, `select exists(select 1 from public.accounts where id = $1 and user_id = $2 and deleted_at is null)`, accountID, userID).Scan(&exists); err != nil {
			return nil, err
		}
		if !exists {
			return nil, accountbalances.ErrNotFound
		}
	}
	return result, nil
}

func (s *Store) GetTransactionForTreatment(ctx context.Context, userID, transactionID uuid.UUID) (accountbalances.TransactionForTreatment, error) {
	var result accountbalances.TransactionForTreatment
	var rawLineItems []byte
	var basis, source, reason *string
	var createdAt, updatedAt *time.Time
	err := s.pool.QueryRow(ctx, `
		select txn.id, txn.original_currency, txn.original_amount_minor,
			txn.line_items, treatment.spending_basis, treatment.source, treatment.reason,
			treatment.created_at, treatment.updated_at
		from public.transactions txn
		left join private.transaction_calculation_treatments treatment
			on treatment.transaction_id = txn.id and treatment.user_id = txn.user_id
		where txn.id = $1 and txn.user_id = $2`, transactionID, userID).
		Scan(&result.ID, &result.OriginalCurrency, &result.OriginalAmountMinor, &rawLineItems,
			&basis, &source, &reason, &createdAt, &updatedAt)
	if err != nil {
		return accountbalances.TransactionForTreatment{}, mapNotFound(err)
	}
	var lineItems []reconciliation.LineItem
	if err := json.Unmarshal(rawLineItems, &lineItems); err != nil {
		return accountbalances.TransactionForTreatment{}, fmt.Errorf("decode transaction line items: %w", err)
	}
	for _, item := range lineItems {
		result.LineItems = append(result.LineItems, accountbalances.LineItemAmount{Currency: item.Currency, LineTotalMinor: item.LineTotalMinor})
	}
	if basis != nil && source != nil && reason != nil && createdAt != nil && updatedAt != nil {
		result.Treatment = &accountbalances.CalculationTreatment{
			TransactionID: result.ID, Basis: accountbalances.SpendingBasis(*basis), Source: accountbalances.TreatmentSource(*source),
			Reason: *reason, CreatedAt: *createdAt, UpdatedAt: *updatedAt,
		}
	}
	return result, nil
}

func (s *Store) PutUserTreatment(ctx context.Context, userID, transactionID uuid.UUID, input accountbalances.PutUserTreatmentParams) (accountbalances.CalculationTreatment, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return accountbalances.CalculationTreatment{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var ownedID uuid.UUID
	if err = tx.QueryRow(ctx, `select id from public.transactions where id = $1 and user_id = $2 for update`, transactionID, userID).Scan(&ownedID); err != nil {
		return accountbalances.CalculationTreatment{}, mapNotFound(err)
	}
	var currentSource string
	var currentUpdated time.Time
	err = tx.QueryRow(ctx, `
		select source, updated_at from private.transaction_calculation_treatments
		where transaction_id = $1 and user_id = $2 for update`, transactionID, userID).Scan(&currentSource, &currentUpdated)
	switch {
	case errors.Is(err, pgx.ErrNoRows) && input.ExpectedUpdatedAt != nil:
		return accountbalances.CalculationTreatment{}, accountbalances.ErrTreatmentVersionConflict
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return accountbalances.CalculationTreatment{}, err
	case currentSource == string(accountbalances.TreatmentSystem):
		return accountbalances.CalculationTreatment{}, accountbalances.ErrSystemTreatmentImmutable
	case input.ExpectedUpdatedAt == nil || !currentUpdated.Equal(*input.ExpectedUpdatedAt):
		return accountbalances.CalculationTreatment{}, accountbalances.ErrTreatmentVersionConflict
	}
	var result accountbalances.CalculationTreatment
	err = tx.QueryRow(ctx, `
		insert into private.transaction_calculation_treatments
			(transaction_id, user_id, spending_basis, source, reason)
		values ($1, $2, $3, 'user', $4)
		on conflict (transaction_id) do update set
			spending_basis = excluded.spending_basis, source = 'user', reason = excluded.reason
		returning transaction_id, spending_basis, source, reason, created_at, updated_at`,
		transactionID, userID, input.Basis, input.Reason).
		Scan(&result.TransactionID, &result.Basis, &result.Source, &result.Reason, &result.CreatedAt, &result.UpdatedAt)
	if err != nil {
		return accountbalances.CalculationTreatment{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return accountbalances.CalculationTreatment{}, err
	}
	return result, nil
}

func (s *Store) LockSystemPayoffExclusions(ctx context.Context, userID uuid.UUID, transactionIDs []uuid.UUID, reason string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = lockSystemExclusions(ctx, tx, userID, transactionIDs, reason); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// LockSystemExclusions applies immutable payoff treatments inside a caller's
// existing transaction. Credit Card adapters use this instead of starting a
// nested Account Balances transaction.
func LockSystemExclusions(ctx context.Context, tx pgx.Tx, userID uuid.UUID, transactionIDs []uuid.UUID, reason string) error {
	return lockSystemExclusions(ctx, tx, userID, transactionIDs, reason)
}

func lockSystemExclusions(ctx context.Context, tx pgx.Tx, userID uuid.UUID, transactionIDs []uuid.UUID, reason string) error {
	if len(transactionIDs) != 2 || transactionIDs[0] == transactionIDs[1] {
		return fmt.Errorf("%w: exactly two payoff legs are required", accountbalances.ErrValidation)
	}
	transactionIDArray := database.UUIDArrayLiteral(transactionIDs)
	rows, err := tx.Query(ctx, `
		select id from public.transactions
		where user_id = $1 and id = any($2::uuid[])
		order by id for update`, userID, transactionIDArray)
	if err != nil {
		return err
	}
	count := 0
	for rows.Next() {
		count++
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if count != 2 {
		return accountbalances.ErrNotFound
	}
	if _, err = tx.Exec(ctx, `
		update private.transaction_calculation_treatments
		set spending_basis = 'exclude', source = 'system', reason = $3
		where user_id = $1 and transaction_id = any($2::uuid[]) and source = 'user'`, userID, transactionIDArray, reason); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `
		insert into private.transaction_calculation_treatments
			(transaction_id, user_id, spending_basis, source, reason)
		select payoff_leg.transaction_id, $1, 'exclude', 'system', $3
		from unnest($2::uuid[]) as payoff_leg(transaction_id)
		on conflict (transaction_id) do nothing`, userID, transactionIDArray, reason); err != nil {
		return err
	}
	var valid int
	if err = tx.QueryRow(ctx, `
		select count(*) from private.transaction_calculation_treatments
		where user_id = $1 and transaction_id = any($2::uuid[])
			and spending_basis = 'exclude' and source = 'system' and reason = $3`, userID, transactionIDArray, reason).Scan(&valid); err != nil {
		return err
	}
	if valid != 2 {
		return accountbalances.ErrSystemTreatmentImmutable
	}
	return nil
}

type rowScanner interface {
	Scan(...any) error
}

func scanAccount(row rowScanner) (accountbalances.FinancialAccount, error) {
	var account accountbalances.FinancialAccount
	var raw []byte
	err := row.Scan(&account.ID, &account.Name, &account.AccountType, &account.Side, &raw, &account.BaselineAsOf, &account.BaselineVersion)
	if err != nil {
		return accountbalances.FinancialAccount{}, err
	}
	account.Baseline, err = decodeBalanceProjection(raw)
	return account, err
}

func decodeBalanceProjection(raw []byte) ([]accountbalances.BalanceAmount, error) {
	var values map[string]string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, fmt.Errorf("decode opening balances: %w", err)
	}
	result := make([]accountbalances.BalanceAmount, 0, len(values))
	for currency, value := range values {
		amount, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("decode opening balance %s: %w", currency, err)
		}
		result = append(result, accountbalances.BalanceAmount{Currency: currency, MinorUnits: amount})
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Currency < result[right].Currency })
	return result, nil
}

func encodeBalanceProjection(values []accountbalances.BalanceAmount) (string, error) {
	projection := make(map[string]string, len(values))
	for _, value := range values {
		projection[value.Currency] = strconv.FormatInt(value.MinorUnits, 10)
	}
	raw, err := json.Marshal(projection)
	return string(raw), err
}

func mapNotFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return accountbalances.ErrNotFound
	}
	return err
}

var _ accountbalances.Repository = (*Store)(nil)

package accountbalances

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/zhengteck/wealth-builder/backend/internal/reconciliation"
)

const systemPayoffReason = "credit_card_payoff"

const idempotencyLifetime = 24 * time.Hour

var canonicalMinorUnits = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)
var safeIdempotencyKey = regexp.MustCompile(`^[\x21-\x7e]{32,128}$`)

type Clock func() time.Time

type Service struct {
	repository Repository
	now        Clock
}

func NewService(repository Repository, now Clock) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{repository: repository, now: now}
}

type CurrencyBalance struct {
	Currency   string
	MinorUnits int64
}

type BalanceView struct {
	AccountID       uuid.UUID
	AccountName     string
	State           string
	Side            AccountSide
	Version         int
	AsOf            *time.Time
	OpeningBalances []CurrencyBalance
	CurrentBalances []CurrencyBalance
}

type SetOpeningBalanceRequest struct {
	Balances         map[string]string
	AsOf             time.Time
	ExpectedVersion  int
	CorrectionReason *string
}

type SetTreatmentRequest struct {
	Basis             SpendingBasis
	Reason            string
	ExpectedUpdatedAt *time.Time
}

func ParseMinorUnits(raw string) (int64, error) {
	if !canonicalMinorUnits.MatchString(raw) || raw == "-0" {
		return 0, fmt.Errorf("%w: amount must be a canonical integer string", ErrValidation)
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: amount is outside bigint range", ErrValidation)
	}
	return value, nil
}

func normalizeBalances(values map[string]string, accountType string) ([]BalanceAmount, error) {
	if len(values) == 0 || len(values) > 20 {
		return nil, fmt.Errorf("%w: one to twenty currencies are required", ErrValidation)
	}
	result := make([]BalanceAmount, 0, len(values))
	for currency, raw := range values {
		if currency != strings.ToUpper(currency) || !reconciliation.IsISO4217(currency) {
			return nil, fmt.Errorf("%w: invalid currency %q", ErrValidation, currency)
		}
		amount, err := ParseMinorUnits(raw)
		if err != nil {
			return nil, err
		}
		if amount < 0 && accountType != "bank_account" {
			return nil, fmt.Errorf("%w: negative opening amounts require a bank account", ErrValidation)
		}
		result = append(result, BalanceAmount{Currency: currency, MinorUnits: amount})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Currency < result[j].Currency })
	return result, nil
}

func (s *Service) ListBalances(ctx context.Context, userID uuid.UUID) ([]BalanceView, error) {
	accounts, err := s.repository.ListFinancialAccounts(ctx, userID)
	if err != nil {
		return nil, err
	}
	views := make([]BalanceView, 0, len(accounts))
	for _, account := range accounts {
		view, err := s.balanceView(ctx, userID, account)
		if err != nil {
			return nil, err
		}
		views = append(views, view)
	}
	return views, nil
}

func (s *Service) GetBalance(ctx context.Context, userID, accountID uuid.UUID) (BalanceView, error) {
	account, err := s.repository.GetFinancialAccount(ctx, userID, accountID)
	if err != nil {
		return BalanceView{}, err
	}
	return s.balanceView(ctx, userID, account)
}

func (s *Service) balanceView(ctx context.Context, userID uuid.UUID, account FinancialAccount) (BalanceView, error) {
	view := BalanceView{
		AccountID: account.ID, AccountName: account.Name, Side: account.Side,
		Version: account.BaselineVersion, AsOf: account.BaselineAsOf,
	}
	if !account.Configured() {
		view.State = "unconfigured"
		return view, nil
	}
	view.State = "configured"
	totals := make(map[string]int64, len(account.Baseline))
	for _, value := range account.Baseline {
		totals[value.Currency] = value.MinorUnits
		view.OpeningBalances = append(view.OpeningBalances, CurrencyBalance(value))
	}
	movements, err := s.repository.ListConfirmedMovementsAfter(ctx, userID, account.ID, *account.BaselineAsOf)
	if err != nil {
		return BalanceView{}, err
	}
	for _, movement := range movements {
		if !movement.OccurredAt.After(*account.BaselineAsOf) || movement.MinorUnits < 0 {
			continue
		}
		delta := movement.MinorUnits
		if (account.Side == AccountAsset && movement.Kind == TransactionDebit) ||
			(account.Side == AccountLiability && movement.Kind == TransactionCredit) {
			delta = -delta
		}
		current := totals[movement.Currency]
		if delta > 0 && current > math.MaxInt64-delta || delta < 0 && current < math.MinInt64-delta {
			return BalanceView{}, fmt.Errorf("%w: calculated balance exceeds signed bigint range", ErrValidation)
		}
		totals[movement.Currency] = current + delta
	}
	for currency, amount := range totals {
		view.CurrentBalances = append(view.CurrentBalances, CurrencyBalance{Currency: currency, MinorUnits: amount})
	}
	sort.Slice(view.CurrentBalances, func(i, j int) bool { return view.CurrentBalances[i].Currency < view.CurrentBalances[j].Currency })
	return view, nil
}

func (s *Service) SetOpeningBalance(ctx context.Context, userID, accountID uuid.UUID, request SetOpeningBalanceRequest, idempotencyKey string) (BalanceView, error) {
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		return BalanceView{}, err
	}
	account, err := s.repository.GetFinancialAccount(ctx, userID, accountID)
	if err != nil {
		return BalanceView{}, err
	}
	if request.AsOf.IsZero() || request.AsOf.After(s.now().Add(time.Second)) {
		return BalanceView{}, fmt.Errorf("%w: as_of must not be in the future", ErrValidation)
	}
	balances, err := normalizeBalances(request.Balances, account.AccountType)
	if err != nil {
		return BalanceView{}, err
	}
	if request.ExpectedVersion == account.BaselineVersion && account.Configured() &&
		account.BaselineAsOf.Equal(request.AsOf.UTC()) && balanceAmountsEqual(account.Baseline, balances) {
		return BalanceView{}, fmt.Errorf("%w: an opening-balance correction must change the balances or as_of", ErrValidation)
	}
	var reason *string
	if request.ExpectedVersion == 0 {
		if request.CorrectionReason != nil {
			return BalanceView{}, fmt.Errorf("%w: first opening balance cannot have a correction reason", ErrValidation)
		}
	} else {
		if request.CorrectionReason == nil {
			return BalanceView{}, fmt.Errorf("%w: correction reason is required", ErrValidation)
		}
		trimmed := strings.TrimSpace(*request.CorrectionReason)
		if len(trimmed) == 0 || len(trimmed) > 500 {
			return BalanceView{}, fmt.Errorf("%w: correction reason must contain 1 to 500 characters", ErrValidation)
		}
		reason = &trimmed
	}
	nextVersion := request.ExpectedVersion + 1
	changedAt := s.now().UTC()
	revision := OpeningBalanceRevision{
		ID: uuid.New(), AccountID: accountID, Version: nextVersion, Balances: balances,
		AsOf: request.AsOf.UTC(), CorrectionReason: reason, ChangedByUserID: userID, ChangedAt: changedAt,
	}
	updated, err := s.repository.ReplaceOpeningBalance(ctx, userID, accountID, ReplaceOpeningBalanceParams{
		ExpectedVersion: request.ExpectedVersion, Balances: balances, AsOf: request.AsOf.UTC(), Revision: revision,
		IdempotencyKey: idempotencyKey, RequestHash: openingBalanceRequestHash(accountID, request.ExpectedVersion, balances, request.AsOf.UTC(), reason),
		ExpiresAt: changedAt.Add(idempotencyLifetime),
	})
	if err != nil {
		return BalanceView{}, err
	}
	return s.balanceView(ctx, userID, updated)
}

func (s *Service) GetCalculationTreatment(ctx context.Context, userID, transactionID uuid.UUID) (CalculationTreatmentView, error) {
	transaction, err := s.repository.GetTransactionForTreatment(ctx, userID, transactionID)
	if err != nil {
		return CalculationTreatmentView{}, err
	}
	view := CalculationTreatmentView{
		TransactionID: transaction.ID,
		Basis:         SpendingTransactionTotal,
		Source:        TreatmentDefault,
	}
	if transaction.Treatment == nil {
		return view, nil
	}
	reason := transaction.Treatment.Reason
	updatedAt := transaction.Treatment.UpdatedAt.UTC()
	view.Basis = transaction.Treatment.Basis
	view.Source = transaction.Treatment.Source
	view.Reason = &reason
	view.Immutable = transaction.Treatment.Source == TreatmentSystem
	view.UpdatedAt = &updatedAt
	return view, nil
}

func (s *Service) ListOpeningBalanceHistory(ctx context.Context, userID, accountID uuid.UUID) ([]OpeningBalanceRevision, error) {
	return s.repository.ListOpeningBalanceRevisions(ctx, userID, accountID)
}

func (s *Service) SetUserTreatment(ctx context.Context, userID, transactionID uuid.UUID, request SetTreatmentRequest) (CalculationTreatment, error) {
	transaction, err := s.repository.GetTransactionForTreatment(ctx, userID, transactionID)
	if err != nil {
		return CalculationTreatment{}, err
	}
	if transaction.Treatment != nil && transaction.Treatment.Source == TreatmentSystem {
		return CalculationTreatment{}, ErrSystemTreatmentImmutable
	}
	if request.Basis != SpendingTransactionTotal && request.Basis != SpendingLineItems && request.Basis != SpendingExclude {
		return CalculationTreatment{}, fmt.Errorf("%w: invalid spending basis", ErrValidation)
	}
	reason := strings.TrimSpace(request.Reason)
	if len(reason) == 0 || len(reason) > 500 {
		return CalculationTreatment{}, fmt.Errorf("%w: treatment reason must contain 1 to 500 characters", ErrValidation)
	}
	if request.Basis == SpendingLineItems {
		var sum int64
		if len(transaction.LineItems) == 0 {
			return CalculationTreatment{}, fmt.Errorf("%w: complete line items are required", ErrValidation)
		}
		for _, item := range transaction.LineItems {
			if item.LineTotalMinor == nil || item.Currency != transaction.OriginalCurrency || *item.LineTotalMinor < 0 {
				return CalculationTreatment{}, fmt.Errorf("%w: line items are incomplete or use another currency", ErrValidation)
			}
			if *item.LineTotalMinor > 0 && sum > transaction.OriginalAmountMinor-*item.LineTotalMinor {
				return CalculationTreatment{}, fmt.Errorf("%w: line-item total overflows or exceeds the transaction", ErrValidation)
			}
			sum += *item.LineTotalMinor
		}
		if sum != transaction.OriginalAmountMinor {
			return CalculationTreatment{}, fmt.Errorf("%w: line items must exactly equal the transaction total", ErrValidation)
		}
	}
	return s.repository.PutUserTreatment(ctx, userID, transactionID, PutUserTreatmentParams{
		Basis: request.Basis, Reason: reason, ExpectedUpdatedAt: request.ExpectedUpdatedAt,
	})
}

func (s *Service) LockPayoffTreatments(ctx context.Context, userID uuid.UUID, transactionIDs []uuid.UUID) error {
	if len(transactionIDs) != 2 || transactionIDs[0] == uuid.Nil || transactionIDs[1] == uuid.Nil || transactionIDs[0] == transactionIDs[1] {
		return fmt.Errorf("%w: exactly two distinct payoff legs are required", ErrValidation)
	}
	if err := s.repository.LockSystemPayoffExclusions(ctx, userID, transactionIDs, systemPayoffReason); err != nil {
		if errors.Is(err, ErrNotFound) {
			return err
		}
		return fmt.Errorf("lock payoff treatments: %w", err)
	}
	return nil
}

func validateIdempotencyKey(key string) error {
	if !safeIdempotencyKey.MatchString(key) {
		return fmt.Errorf("%w: Idempotency-Key must contain 32 to 128 visible ASCII characters", ErrValidation)
	}
	return nil
}

func openingBalanceRequestHash(accountID uuid.UUID, expectedVersion int, balances []BalanceAmount, asOf time.Time, reason *string) string {
	type hashedAmount struct {
		Currency   string `json:"currency"`
		MinorUnits string `json:"minor_units"`
	}
	hashedBalances := make([]hashedAmount, 0, len(balances))
	for _, balance := range balances {
		hashedBalances = append(hashedBalances, hashedAmount{Currency: balance.Currency, MinorUnits: strconv.FormatInt(balance.MinorUnits, 10)})
	}
	payload := struct {
		AccountID       uuid.UUID      `json:"account_id"`
		ExpectedVersion int            `json:"expected_version"`
		Balances        []hashedAmount `json:"balances"`
		AsOf            string         `json:"as_of"`
		Reason          *string        `json:"correction_reason"`
	}{AccountID: accountID, ExpectedVersion: expectedVersion, Balances: hashedBalances, AsOf: asOf.Format(time.RFC3339Nano), Reason: reason}
	raw, _ := json.Marshal(payload)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func balanceAmountsEqual(left, right []BalanceAmount) bool {
	if len(left) != len(right) {
		return false
	}
	values := make(map[string]int64, len(left))
	for _, amount := range left {
		values[amount.Currency] = amount.MinorUnits
	}
	for _, amount := range right {
		value, ok := values[amount.Currency]
		if !ok || value != amount.MinorUnits {
			return false
		}
	}
	return true
}

package accountbalances

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

type fakeRepository struct {
	accounts      []FinancialAccount
	movements     []ConfirmedMovement
	transaction   TransactionForTreatment
	replaced      *ReplaceOpeningBalanceParams
	putTreatment  *PutUserTreatmentParams
	lockedPayoffs []uuid.UUID
}

func (f *fakeRepository) ListFinancialAccounts(context.Context, uuid.UUID) ([]FinancialAccount, error) {
	return f.accounts, nil
}
func (f *fakeRepository) GetFinancialAccount(_ context.Context, _ uuid.UUID, id uuid.UUID) (FinancialAccount, error) {
	for _, account := range f.accounts {
		if account.ID == id {
			return account, nil
		}
	}
	return FinancialAccount{}, ErrNotFound
}
func (f *fakeRepository) ListConfirmedMovementsAfter(context.Context, uuid.UUID, uuid.UUID, time.Time) ([]ConfirmedMovement, error) {
	return f.movements, nil
}
func (f *fakeRepository) ReplaceOpeningBalance(_ context.Context, _ uuid.UUID, id uuid.UUID, input ReplaceOpeningBalanceParams) (FinancialAccount, error) {
	f.replaced = &input
	account, _ := f.GetFinancialAccount(context.Background(), uuid.Nil, id)
	if account.BaselineVersion != input.ExpectedVersion {
		return FinancialAccount{}, &VersionConflictError{Current: account}
	}
	account.Baseline = input.Balances
	account.BaselineAsOf = &input.AsOf
	account.BaselineVersion = input.Revision.Version
	return account, nil
}
func (f *fakeRepository) ListOpeningBalanceRevisions(context.Context, uuid.UUID, uuid.UUID) ([]OpeningBalanceRevision, error) {
	return nil, nil
}
func (f *fakeRepository) GetTransactionForTreatment(context.Context, uuid.UUID, uuid.UUID) (TransactionForTreatment, error) {
	return f.transaction, nil
}
func (f *fakeRepository) PutUserTreatment(_ context.Context, _, _ uuid.UUID, input PutUserTreatmentParams) (CalculationTreatment, error) {
	f.putTreatment = &input
	return CalculationTreatment{Basis: input.Basis, Source: TreatmentUser, Reason: input.Reason}, nil
}
func (f *fakeRepository) LockSystemPayoffExclusions(_ context.Context, _ uuid.UUID, ids []uuid.UUID, _ string) error {
	f.lockedPayoffs = append([]uuid.UUID(nil), ids...)
	return nil
}

func TestBalancesDistinguishUnconfiguredAndExplicitZero(t *testing.T) {
	accountA, accountB := uuid.New(), uuid.New()
	asOf := time.Date(2026, 9, 4, 10, 30, 0, 0, time.UTC)
	repository := &fakeRepository{accounts: []FinancialAccount{
		{ID: accountA, Name: "Not set", AccountType: "bank_account", Side: AccountAsset},
		{ID: accountB, Name: "Set zero", AccountType: "bank_account", Side: AccountAsset, Baseline: []BalanceAmount{{Currency: "SGD", MinorUnits: 0}}, BaselineAsOf: &asOf, BaselineVersion: 1},
	}, movements: []ConfirmedMovement{
		{Kind: TransactionCredit, Currency: "SGD", MinorUnits: 900, OccurredAt: asOf},
		{Kind: TransactionCredit, Currency: "SGD", MinorUnits: 1200, OccurredAt: asOf.Add(time.Nanosecond)},
		{Kind: TransactionDebit, Currency: "USD", MinorUnits: 200, OccurredAt: asOf.Add(time.Hour)},
	}}
	views, err := NewService(repository, nil).ListBalances(context.Background(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if views[0].State != "unconfigured" || views[0].CurrentBalances != nil {
		t.Fatalf("unconfigured account collapsed into a value: %#v", views[0])
	}
	if views[1].State != "configured" || len(views[1].OpeningBalances) != 1 || views[1].OpeningBalances[0].MinorUnits != 0 {
		t.Fatalf("explicit zero was not preserved: %#v", views[1])
	}
	if got := views[1].CurrentBalances; len(got) != 2 || got[0].MinorUnits != 1200 || got[1].MinorUnits != -200 {
		t.Fatalf("strict timestamp/multi-currency calculation mismatch: %#v", got)
	}
}

func TestOpeningBalanceCorrectionCreatesNormalizedRevision(t *testing.T) {
	id, userID := uuid.New(), uuid.New()
	asOf := time.Date(2026, 9, 1, 0, 0, 0, 123, time.UTC)
	repository := &fakeRepository{accounts: []FinancialAccount{{ID: id, Name: "Card", AccountType: "credit_card", Side: AccountLiability, BaselineAsOf: &asOf, BaselineVersion: 2}}}
	service := NewService(repository, func() time.Time { return time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC) })
	reason := "  corrected statement carry-over  "
	_, err := service.SetOpeningBalance(context.Background(), userID, id, SetOpeningBalanceRequest{Balances: map[string]string{"USD": "0", "SGD": "12345"}, AsOf: asOf, ExpectedVersion: 2, CorrectionReason: &reason}, "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if repository.replaced == nil || repository.replaced.Revision.Version != 3 || len(repository.replaced.Revision.Balances) != 2 || repository.replaced.Revision.Balances[0].Currency != "SGD" {
		t.Fatalf("revision was not normalized: %#v", repository.replaced)
	}
	if got := *repository.replaced.Revision.CorrectionReason; got != "corrected statement carry-over" {
		t.Fatalf("reason = %q", got)
	}
	_, err = service.SetOpeningBalance(context.Background(), userID, id, SetOpeningBalanceRequest{Balances: map[string]string{"SGD": "1"}, AsOf: asOf, ExpectedVersion: 1, CorrectionReason: &reason}, "abcdef0123456789abcdef0123456789")
	var conflict *VersionConflictError
	if !errors.As(err, &conflict) || conflict.Current.BaselineVersion != 2 {
		t.Fatalf("expected current version conflict, got %v", err)
	}
}

func TestOpeningBalanceCorrectionRejectsNormalizedNoOp(t *testing.T) {
	accountID, userID := uuid.New(), uuid.New()
	asOf := time.Date(2026, 9, 1, 0, 0, 0, 123, time.UTC)
	repository := &fakeRepository{accounts: []FinancialAccount{{
		ID: accountID, AccountType: "bank_account", Side: AccountAsset,
		Baseline:     []BalanceAmount{{Currency: "USD", MinorUnits: 0}, {Currency: "SGD", MinorUnits: 12345}},
		BaselineAsOf: &asOf, BaselineVersion: 2,
	}}}
	reason := "No actual change"
	_, err := NewService(repository, nil).SetOpeningBalance(context.Background(), userID, accountID, SetOpeningBalanceRequest{
		Balances: map[string]string{"SGD": "12345", "USD": "0"}, AsOf: asOf,
		ExpectedVersion: 2, CorrectionReason: &reason,
	}, "55555555555555555555555555555555")
	if !errors.Is(err, ErrValidation) || repository.replaced != nil {
		t.Fatalf("no-op correction error=%v replaced=%#v", err, repository.replaced)
	}
}

func TestCalculatedBalanceRejectsSignedOverflow(t *testing.T) {
	asOf := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	accountID := uuid.New()
	repository := &fakeRepository{
		accounts:  []FinancialAccount{{ID: accountID, AccountType: "bank_account", Side: AccountAsset, Baseline: []BalanceAmount{{Currency: "SGD", MinorUnits: 9223372036854775807}}, BaselineAsOf: &asOf, BaselineVersion: 1}},
		movements: []ConfirmedMovement{{Kind: TransactionCredit, Currency: "SGD", MinorUnits: 1, OccurredAt: asOf.Add(time.Second)}},
	}
	if _, err := NewService(repository, nil).GetBalance(context.Background(), uuid.New(), accountID); !errors.Is(err, ErrValidation) {
		t.Fatalf("overflow error = %v", err)
	}

	repository.accounts[0].Baseline[0].MinorUnits = -9223372036854775808
	repository.movements[0].Kind = TransactionDebit
	if _, err := NewService(repository, nil).GetBalance(context.Background(), uuid.New(), accountID); !errors.Is(err, ErrValidation) {
		t.Fatalf("underflow error = %v", err)
	}

	repository.accounts[0].Baseline[0].MinorUnits = 9223372036854775806
	repository.movements[0].Kind = TransactionCredit
	view, err := NewService(repository, nil).GetBalance(context.Background(), uuid.New(), accountID)
	if err != nil || view.CurrentBalances[0].MinorUnits != 9223372036854775807 {
		t.Fatalf("valid upper boundary rejected: %#v, %v", view, err)
	}
}

func TestOpeningBalanceRequestHashIncludesVersion(t *testing.T) {
	accountID := uuid.New()
	asOf := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	balances := []BalanceAmount{{Currency: "SGD", MinorUnits: 100}}
	if openingBalanceRequestHash(accountID, 1, balances, asOf, nil) == openingBalanceRequestHash(accountID, 2, balances, asOf, nil) {
		t.Fatal("expected_version was omitted from the idempotency request hash")
	}
}

func TestCalculationTreatmentViewReturnsDefaultAndImmutableSystemState(t *testing.T) {
	transactionID := uuid.New()
	repository := &fakeRepository{transaction: TransactionForTreatment{ID: transactionID}}
	service := NewService(repository, nil)
	view, err := service.GetCalculationTreatment(context.Background(), uuid.New(), transactionID)
	if err != nil || view.Basis != SpendingTransactionTotal || view.Source != TreatmentDefault || view.Immutable || view.UpdatedAt != nil {
		t.Fatalf("default treatment = %#v, error = %v", view, err)
	}
	updatedAt := time.Date(2026, 9, 4, 1, 2, 3, 456000000, time.UTC)
	repository.transaction.Treatment = &CalculationTreatment{TransactionID: transactionID, Basis: SpendingExclude, Source: TreatmentSystem, Reason: systemPayoffReason, UpdatedAt: updatedAt}
	view, err = service.GetCalculationTreatment(context.Background(), uuid.New(), transactionID)
	if err != nil || !view.Immutable || view.Source != TreatmentSystem || view.UpdatedAt == nil || !view.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("system treatment = %#v, error = %v", view, err)
	}
}

func TestTreatmentValidationAndPayoffLock(t *testing.T) {
	lineA, lineB := int64(700), int64(300)
	repository := &fakeRepository{transaction: TransactionForTreatment{OriginalCurrency: "SGD", OriginalAmountMinor: 1000, LineItems: []LineItemAmount{{Currency: "SGD", LineTotalMinor: &lineA}, {Currency: "SGD", LineTotalMinor: &lineB}}}}
	service := NewService(repository, nil)
	_, err := service.SetUserTreatment(context.Background(), uuid.New(), uuid.New(), SetTreatmentRequest{Basis: SpendingLineItems, Reason: "Itemised purchase"})
	if err != nil || repository.putTreatment == nil {
		t.Fatalf("valid treatment rejected: %v", err)
	}
	repository.transaction.Treatment = &CalculationTreatment{Source: TreatmentSystem}
	_, err = service.SetUserTreatment(context.Background(), uuid.New(), uuid.New(), SetTreatmentRequest{Basis: SpendingExclude, Reason: "change"})
	if !errors.Is(err, ErrSystemTreatmentImmutable) {
		t.Fatalf("system treatment was mutable: %v", err)
	}
	ids := []uuid.UUID{uuid.New(), uuid.New()}
	if err := service.LockPayoffTreatments(context.Background(), uuid.New(), ids); err != nil || len(repository.lockedPayoffs) != 2 {
		t.Fatalf("payoff exclusions were not locked: %v", err)
	}
}

func TestParseMinorUnitsRejectsNonCanonicalMoney(t *testing.T) {
	for _, raw := range []string{"1.00", "+1", "01", "-0", " 1", "9223372036854775808"} {
		if _, err := ParseMinorUnits(raw); !errors.Is(err, ErrValidation) {
			t.Errorf("ParseMinorUnits(%q) error = %v", raw, err)
		}
	}
}

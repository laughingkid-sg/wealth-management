package creditcard

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

type fakeIdempotency struct {
	hash   string
	bill   *Bill
	status int
}

type fakeStore struct {
	bill            Bill
	transactions    map[uuid.UUID]CanonicalTransaction
	exactTransfers  []InternalTransfer
	bankCandidates  []CanonicalTransaction
	createdLine     CanonicalTransaction
	events          []BillEvent
	exclusions      []uuid.UUID
	idempotency     map[string]fakeIdempotency
	missingLegCalls int
	fullPayoffCalls int
	createLineCalls int
	discarded       bool
	discardCalls    int
	projectionNew   bool
}

func (f *fakeStore) ListBills(context.Context, uuid.UUID, uuid.UUID, *string, int) (BillPage, error) {
	return BillPage{}, nil
}
func (f *fakeStore) GetBill(context.Context, uuid.UUID, uuid.UUID) (Bill, error) { return f.bill, nil }
func (f *fakeStore) Transact(_ context.Context, callback func(Tx) error) error   { return callback(f) }
func (f *fakeStore) GetBillForUpdate(context.Context, uuid.UUID, uuid.UUID) (Bill, error) {
	return f.bill, nil
}
func (f *fakeStore) ProjectBillFromBulk(context.Context, uuid.UUID, uuid.UUID, int) (BulkProjectionResult, error) {
	return BulkProjectionResult{Bill: f.bill, Created: f.projectionNew}, nil
}
func (f *fakeStore) GetLineForUpdate(_ context.Context, _ uuid.UUID, _ uuid.UUID, lineID uuid.UUID) (BillLine, error) {
	for _, line := range f.bill.Lines {
		if line.ID == lineID {
			return line, nil
		}
	}
	return BillLine{}, ErrNotFound
}
func (f *fakeStore) GetTransactionForUpdate(_ context.Context, _ uuid.UUID, id uuid.UUID) (CanonicalTransaction, error) {
	transaction, ok := f.transactions[id]
	if !ok {
		return CanonicalTransaction{}, ErrNotFound
	}
	return transaction, nil
}
func (f *fakeStore) SaveBill(_ context.Context, _ uuid.UUID, bill Bill, expected int) (Bill, error) {
	if f.bill.Version != expected {
		return Bill{}, ErrVersionConflict
	}
	f.bill = bill
	return bill, nil
}
func (f *fakeStore) SaveLine(_ context.Context, _ uuid.UUID, line BillLine) (BillLine, error) {
	for index := range f.bill.Lines {
		if f.bill.Lines[index].ID == line.ID {
			f.bill.Lines[index] = line
		}
	}
	return line, nil
}
func (f *fakeStore) AppendBillEvent(_ context.Context, _ uuid.UUID, event BillEvent) error {
	f.events = append(f.events, event)
	return nil
}
func (f *fakeStore) DeleteReviewBill(context.Context, uuid.UUID, uuid.UUID, int) error {
	f.discarded = true
	f.discardCalls++
	return nil
}
func (f *fakeStore) IsTransactionLinkedToAnotherLine(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (bool, error) {
	return false, nil
}
func (f *fakeStore) CreateTransactionFromPinnedCandidate(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID) (LineCreateResult, error) {
	f.createLineCalls++
	return LineCreateResult{Transaction: f.createdLine, BulkCandidateOutcomeID: uuid.New()}, nil
}
func (f *fakeStore) FindExactPayoffTransfers(context.Context, uuid.UUID, uuid.UUID, string, int64, time.Time, time.Time) ([]InternalTransfer, error) {
	return f.exactTransfers, nil
}
func (f *fakeStore) FindBankDebitCandidates(context.Context, uuid.UUID, uuid.UUID, string, int64, time.Time, time.Time) ([]CanonicalTransaction, error) {
	return f.bankCandidates, nil
}
func (f *fakeStore) CreateMissingCardLegAndTransfer(_ context.Context, _ uuid.UUID, _ uuid.UUID, bankTransactionID, cardAccountID uuid.UUID, currency string, amount int64) (PayoffResult, error) {
	f.missingLegCalls++
	creditID := uuid.New()
	return PayoffResult{Transfer: InternalTransfer{ID: uuid.New(), DebitTransactionID: bankTransactionID, CreditTransactionID: creditID, CreditAccountID: cardAccountID, Currency: currency, AmountMinor: amount}}, nil
}
func (f *fakeStore) CreateFullPayoffTransfer(_ context.Context, _ uuid.UUID, bankAccountID, cardAccountID uuid.UUID, currency string, amount int64, occurredAt time.Time) (PayoffResult, error) {
	f.fullPayoffCalls++
	return PayoffResult{Transfer: InternalTransfer{ID: uuid.New(), DebitTransactionID: uuid.New(), CreditTransactionID: uuid.New(), DebitAccountID: bankAccountID, CreditAccountID: cardAccountID, Currency: currency, AmountMinor: amount, OccurredAt: occurredAt}}, nil
}
func (f *fakeStore) LockSystemPayoffExclusions(_ context.Context, _ uuid.UUID, ids []uuid.UUID, _ string) error {
	f.exclusions = append([]uuid.UUID(nil), ids...)
	return nil
}
func (f *fakeStore) ClaimIdempotency(_ context.Context, _ uuid.UUID, key, _ string, _ uuid.UUID, hash string, _ time.Time) (IdempotencyClaim, error) {
	if f.idempotency == nil {
		f.idempotency = make(map[string]fakeIdempotency)
	}
	current, ok := f.idempotency[key]
	if !ok {
		f.idempotency[key] = fakeIdempotency{hash: hash}
		return IdempotencyClaim{State: IdempotencyAcquired}, nil
	}
	if current.hash != hash {
		return IdempotencyClaim{}, ErrIdempotencyConflict
	}
	if current.status == 0 {
		return IdempotencyClaim{State: IdempotencyBusy}, nil
	}
	return IdempotencyClaim{State: IdempotencyReplay, Bill: current.bill, ResponseStatus: current.status}, nil
}
func (f *fakeStore) CompleteIdempotency(_ context.Context, _ uuid.UUID, key, _ string, _ uuid.UUID, hash string, bill *Bill, status int) error {
	f.idempotency[key] = fakeIdempotency{hash: hash, bill: bill, status: status}
	return nil
}

func completeReviewBill() Bill {
	periodStart := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	statementDate := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	dueDate := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)
	currency, amount := "SGD", int64(123450)
	return Bill{ID: uuid.New(), AccountID: uuid.New(), PeriodStart: &periodStart, PeriodEnd: &periodEnd, StatementDate: &statementDate, DueDate: &dueDate, SettlementCurrency: &currency, AmountDueMinor: &amount, Status: BillReview, Version: 1}
}

func bankDebit(bill Bill, when time.Time) CanonicalTransaction {
	return CanonicalTransaction{ID: uuid.New(), AccountID: uuid.New(), AccountType: "bank_account", Direction: DirectionDebit, OriginalCurrency: *bill.SettlementCurrency, OriginalAmountMinor: *bill.AmountDueMinor, OccurredAt: when}
}

func TestResolvingLastLineAdvancesToUnpaidAndDetectsSuggestion(t *testing.T) {
	bill := completeReviewBill()
	line := BillLine{ID: uuid.New(), BillID: bill.ID, Kind: LineActivity, Status: LinePending}
	bill.Lines = []BillLine{line}
	candidate := bankDebit(bill, bill.StatementDate.AddDate(0, 0, 2))
	store := &fakeStore{bill: bill, bankCandidates: []CanonicalTransaction{candidate}}
	result, err := NewService(store, nil).IgnoreLine(context.Background(), uuid.New(), bill.ID, line.ID, ReasonedMutationRequest{ExpectedVersion: 1, Reason: "Duplicate issuer annotation"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != BillUnpaid || result.PaymentCandidateTransactionID == nil || *result.PaymentCandidateTransactionID != candidate.ID {
		t.Fatalf("last resolution did not run payment detection: %#v", result)
	}
	assertEventKinds(t, store.events, "line_ignored", "payment_candidates_found", "status_changed")
	if store.events[1].Details["bank_debit_candidate_count"] != "1" || store.events[2].FromStatus == nil || *store.events[2].FromStatus != BillReview || store.events[2].ToStatus == nil || *store.events[2].ToStatus != BillUnpaid {
		t.Fatalf("payment audit details = %#v", store.events)
	}
}

func TestUnresolvedCandidateCountKeepsBillInReviewAndSkipsPayment(t *testing.T) {
	bill := completeReviewBill()
	bill.UnresolvedCandidateCount = 1
	bill.Lines = []BillLine{{ID: uuid.New(), BillID: bill.ID, Kind: LineSummary, Status: LineIgnored}}
	transfer := InternalTransfer{ID: uuid.New(), DebitTransactionID: uuid.New(), CreditTransactionID: uuid.New()}
	store := &fakeStore{bill: bill, exactTransfers: []InternalTransfer{transfer}, bankCandidates: []CanonicalTransaction{bankDebit(bill, *bill.StatementDate)}}

	result, err := NewService(store, nil).CorrectHeader(context.Background(), uuid.New(), bill.ID, HeaderCorrectionRequest{
		ExpectedVersion: 1,
		Header:          CorrectHeaderInput{PeriodStart: bill.PeriodStart, Reason: "Confirmed statement header"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != BillReview || result.PayoffTransferID != nil || result.PaymentCandidateTransactionID != nil || len(result.AmbiguousPaymentCandidates) != 0 || len(store.exclusions) != 0 {
		t.Fatalf("unresolved bill entered payment resolution: %#v exclusions=%v", result, store.exclusions)
	}
	assertEventKinds(t, store.events, "header_corrected")
}

func TestExistingExactTransferMarksPaidAndLocksBothTreatments(t *testing.T) {
	bill := completeReviewBill()
	line := BillLine{ID: uuid.New(), BillID: bill.ID, Kind: LineSummary, Status: LineIgnored}
	bill.Lines = []BillLine{line}
	transfer := InternalTransfer{ID: uuid.New(), DebitTransactionID: uuid.New(), CreditTransactionID: uuid.New()}
	store := &fakeStore{bill: bill, exactTransfers: []InternalTransfer{transfer}}
	reason := "Confirmed dates"
	result, err := NewService(store, nil).CorrectHeader(context.Background(), uuid.New(), bill.ID, HeaderCorrectionRequest{ExpectedVersion: 1, Header: CorrectHeaderInput{PeriodStart: bill.PeriodStart, Reason: reason}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != BillPaid || result.PayoffTransferID == nil || *result.PayoffTransferID != transfer.ID || len(store.exclusions) != 2 {
		t.Fatalf("exact transfer was not atomically applied: %#v, exclusions=%v", result, store.exclusions)
	}
	assertEventKinds(t, store.events, "header_corrected", "payment_candidates_found", "payment_confirmed", "status_changed")
	if store.events[1].Details["exact_transfer_count"] != "1" || store.events[2].Details["detection"] != "automatic" {
		t.Fatalf("automatic payment audit details = %#v", store.events)
	}
}

func TestAmbiguousDebitSelectionAndIdempotentMissingLegCompletion(t *testing.T) {
	bill := completeReviewBill()
	first := bankDebit(bill, bill.StatementDate.AddDate(0, 0, 2))
	second := bankDebit(bill, bill.StatementDate.AddDate(0, 0, 3))
	store := &fakeStore{bill: bill, transactions: map[uuid.UUID]CanonicalTransaction{first.ID: first, second.ID: second}, bankCandidates: []CanonicalTransaction{first, second}}
	service := NewService(store, func() time.Time { return time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC) })
	review, err := service.CorrectHeader(context.Background(), uuid.New(), bill.ID, HeaderCorrectionRequest{ExpectedVersion: 1, Header: CorrectHeaderInput{PeriodStart: bill.PeriodStart, Reason: "Validated statement"}})
	if err != nil || review.Status != BillReview || len(review.AmbiguousPaymentCandidates) != 2 {
		t.Fatalf("ambiguous candidates not exposed for selection: %#v, %v", review, err)
	}
	selected, err := service.SelectPaymentCandidate(context.Background(), uuid.New(), bill.ID, PaymentCandidateRequest{ExpectedVersion: 2, BankTransactionID: second.ID}, "select-payment-candidate-key-0001")
	if err != nil || selected.Status != BillUnpaid || selected.PaymentCandidateTransactionID == nil || *selected.PaymentCandidateTransactionID != second.ID {
		t.Fatalf("candidate selection failed: %#v, %v", selected, err)
	}
	key := "0123456789abcdef0123456789abcdef"
	paid, err := service.ConfirmPaymentCandidate(context.Background(), uuid.New(), bill.ID, PaymentCandidateRequest{ExpectedVersion: 3, BankTransactionID: second.ID}, key)
	if err != nil || paid.Status != BillPaid || store.missingLegCalls != 1 || len(store.exclusions) != 2 {
		t.Fatalf("missing Card leg was not completed: %#v calls=%d exclusions=%v err=%v", paid, store.missingLegCalls, store.exclusions, err)
	}
	replayed, err := service.ConfirmPaymentCandidate(context.Background(), uuid.New(), bill.ID, PaymentCandidateRequest{ExpectedVersion: 3, BankTransactionID: second.ID}, key)
	if err != nil || replayed.Status != BillPaid || store.missingLegCalls != 1 {
		t.Fatalf("idempotent replay duplicated payoff: %#v calls=%d err=%v", replayed, store.missingLegCalls, err)
	}
}

func TestLineAttachUsesLineKindDateWindowAndExceptionAudit(t *testing.T) {
	bill := completeReviewBill()
	lineAmount, lineCurrency := int64(500), "SGD"
	lineDate := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	line := BillLine{ID: uuid.New(), BillID: bill.ID, Kind: LineActivity, Status: LinePending, OccurredOn: &lineDate, AmountMinor: &lineAmount, Currency: &lineCurrency}
	bill.Lines = []BillLine{line}
	transaction := CanonicalTransaction{ID: uuid.New(), AccountID: bill.AccountID, AccountType: "credit_card", Direction: DirectionDebit, OriginalCurrency: "SGD", OriginalAmountMinor: 500, OccurredAt: time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)}
	store := &fakeStore{bill: bill, transactions: map[uuid.UUID]CanonicalTransaction{transaction.ID: transaction}}
	service := NewService(store, nil)
	_, err := service.AttachLine(context.Background(), uuid.New(), bill.ID, line.ID, AttachLineRequest{ExpectedVersion: 1, TransactionID: transaction.ID})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("out-of-period line attached without audit reason: %v", err)
	}
	reason := "Issuer posted this purchase after period close"
	result, err := service.AttachLine(context.Background(), uuid.New(), bill.ID, line.ID, AttachLineRequest{ExpectedVersion: 1, TransactionID: transaction.ID, LinkExceptionReason: &reason})
	if err != nil || result.Status != BillUnpaid || result.Lines[0].LinkExceptionReason == nil {
		t.Fatalf("audited exception failed: %#v, %v", result, err)
	}
}

func TestCreateLineDelegatesPinnedCandidateWithoutReverseFlow(t *testing.T) {
	bill := completeReviewBill()
	candidateID := uuid.New()
	amount, currency := int64(700), "SGD"
	date := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	line := BillLine{ID: uuid.New(), BillID: bill.ID, BulkCandidateID: &candidateID, Kind: LineRefund, Status: LinePending, AmountMinor: &amount, Currency: &currency, OccurredOn: &date}
	bill.Lines = []BillLine{line}
	created := CanonicalTransaction{ID: uuid.New(), AccountID: bill.AccountID, AccountType: "credit_card", Direction: DirectionCredit, OriginalCurrency: currency, OriginalAmountMinor: amount, OccurredAt: date}
	store := &fakeStore{bill: bill, createdLine: created}
	service, userID := NewService(store, nil), uuid.New()
	categoryID, key := uuid.New(), "create-line-transaction-key-0001"
	request := CreateLineRequest{ExpectedVersion: 1, CategoryID: categoryID}
	result, err := service.CreateLineTransaction(context.Background(), userID, bill.ID, line.ID, request, key)
	if err != nil || store.createLineCalls != 1 || result.Lines[0].TransactionID == nil || *result.Lines[0].TransactionID != created.ID {
		t.Fatalf("pinned candidate operation was not used: %#v, calls=%d, err=%v", result, store.createLineCalls, err)
	}
	replayed, err := service.CreateLineTransaction(context.Background(), userID, bill.ID, line.ID, request, key)
	if err != nil || replayed.Version != result.Version || store.createLineCalls != 1 {
		t.Fatalf("create replay duplicated work: %#v, calls=%d, err=%v", replayed, store.createLineCalls, err)
	}
	request.ExpectedVersion++
	if _, err := service.CreateLineTransaction(context.Background(), userID, bill.ID, line.ID, request, key); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed create request reused a key: %v", err)
	}
}

func TestPayInFullVoidAndDiscardStatusRules(t *testing.T) {
	bill := completeReviewBill()
	bill.Status = BillUnpaid
	store := &fakeStore{bill: bill}
	service := NewService(store, nil)
	userID, bankAccountID := uuid.New(), uuid.New()
	payoffRequest := PayoffRequest{ExpectedVersion: 1, BankAccountID: bankAccountID}
	paid, err := service.PayInFull(context.Background(), userID, bill.ID, payoffRequest, "abcdef0123456789abcdef0123456789")
	if err != nil || paid.Status != BillPaid || store.fullPayoffCalls != 1 || len(store.exclusions) != 2 {
		t.Fatalf("full payoff failed: %#v err=%v", paid, err)
	}
	replayed, err := service.PayInFull(context.Background(), userID, bill.ID, payoffRequest, "abcdef0123456789abcdef0123456789")
	if err != nil || replayed.Status != BillPaid || store.fullPayoffCalls != 1 {
		t.Fatalf("payoff replay duplicated work: %#v calls=%d err=%v", replayed, store.fullPayoffCalls, err)
	}
	_, err = service.VoidBill(context.Background(), uuid.New(), bill.ID, ReasonedMutationRequest{ExpectedVersion: 2, Reason: "wrong import"}, "11111111111111111111111111111111")
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("paid bill could be voided: %v", err)
	}
	store.bill = completeReviewBill()
	discardID := store.bill.ID
	if err := service.DiscardReviewBill(context.Background(), userID, discardID, 1, "22222222222222222222222222222222"); err != nil || !store.discarded {
		t.Fatalf("review discard failed: %v", err)
	}
	if err := service.DiscardReviewBill(context.Background(), userID, discardID, 1, "22222222222222222222222222222222"); err != nil || store.discardCalls != 1 {
		t.Fatalf("discard replay duplicated deletion: calls=%d err=%v", store.discardCalls, err)
	}
}

func TestVoidAllowsOnlyUnpaidBills(t *testing.T) {
	serviceKey := "33333333333333333333333333333333"
	review := completeReviewBill()
	review.UnresolvedCandidateCount = 1
	store := &fakeStore{bill: review}
	service := NewService(store, nil)
	if _, err := service.VoidBill(context.Background(), uuid.New(), review.ID, ReasonedMutationRequest{ExpectedVersion: 1, Reason: "Incorrect statement"}, serviceKey); !errors.Is(err, ErrValidation) {
		t.Fatalf("Review/unresolved bill void error = %v", err)
	}

	unpaid := completeReviewBill()
	unpaid.Status = BillUnpaid
	store = &fakeStore{bill: unpaid}
	result, err := NewService(store, nil).VoidBill(context.Background(), uuid.New(), unpaid.ID, ReasonedMutationRequest{ExpectedVersion: 1, Reason: "Incorrect statement"}, "44444444444444444444444444444444")
	if err != nil || result.Status != BillVoid || result.VoidReason == nil {
		t.Fatalf("Unpaid bill was not voided: %#v, %v", result, err)
	}
	assertEventKinds(t, store.events, "voided", "status_changed")
}

func TestBulkProjectionAcceptsIdentityOnlyAndKeepsPendingLinesInReview(t *testing.T) {
	bill := completeReviewBill()
	bill.BulkDocumentID = uuid.New()
	bill.BulkAttemptGeneration = 4
	candidateID := uuid.New()
	bill.Lines = []BillLine{{ID: uuid.New(), BillID: bill.ID, BulkCandidateID: &candidateID, Kind: LineActivity, Status: LinePending, Index: 1, Description: "Statement purchase"}}
	store := &fakeStore{bill: bill, projectionNew: true}
	result, err := NewService(store, nil).ProjectBulkBill(context.Background(), uuid.New(), bill.BulkDocumentID, 4)
	if err != nil || result.Status != BillReview || result.Version != 2 || len(store.events) != 1 || store.events[0].Kind != "imported" {
		t.Fatalf("Bulk projection result=%#v events=%#v err=%v", result, store.events, err)
	}
}

func assertEventKinds(t *testing.T, events []BillEvent, expected ...string) {
	t.Helper()
	if len(events) != len(expected) {
		t.Fatalf("event count=%d, want %d: %#v", len(events), len(expected), events)
	}
	for index, kind := range expected {
		if events[index].Kind != kind {
			t.Fatalf("event[%d]=%q, want %q: %#v", index, events[index].Kind, kind, events)
		}
	}
}

package creditcard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/zhengteck/wealth-builder/backend/internal/reconciliation"
)

const (
	systemPayoffReason  = "credit_card_payoff"
	idempotencyLifetime = 24 * time.Hour
)

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

type AttachLineRequest struct {
	ExpectedVersion     int
	TransactionID       uuid.UUID
	LinkExceptionReason *string
}

type CreateLineRequest struct {
	ExpectedVersion int
	CategoryID      uuid.UUID
}

type ReasonedMutationRequest struct {
	ExpectedVersion int
	Reason          string
}

type HeaderCorrectionRequest struct {
	ExpectedVersion int
	Header          CorrectHeaderInput
}

type PaymentCandidateRequest struct {
	ExpectedVersion   int
	BankTransactionID uuid.UUID
}

type PayoffRequest struct {
	ExpectedVersion int
	BankAccountID   uuid.UUID
}

func (s *Service) ListBills(ctx context.Context, userID, accountID uuid.UUID, cursor *string, limit int) (BillPage, error) {
	if userID == uuid.Nil || accountID == uuid.Nil || limit < 1 || limit > 100 {
		return BillPage{}, fmt.Errorf("%w: invalid list request", ErrValidation)
	}
	return s.repository.ListBills(ctx, userID, accountID, cursor, limit)
}

func (s *Service) GetBill(ctx context.Context, userID, billID uuid.UUID) (Bill, error) {
	if userID == uuid.Nil || billID == uuid.Nil {
		return Bill{}, fmt.Errorf("%w: invalid bill", ErrValidation)
	}
	return s.repository.GetBill(ctx, userID, billID)
}

// ProjectBulkBill is the only bill-creation entry point. It accepts identity
// only; the Tx adapter must load the immutable Bulk generation and pinned
// candidate outcomes server-side. A repeated generation returns its existing
// bill without another projection or event.
func (s *Service) ProjectBulkBill(ctx context.Context, userID, documentID uuid.UUID, attemptGeneration int) (Bill, error) {
	if userID == uuid.Nil || documentID == uuid.Nil || attemptGeneration < 1 {
		return Bill{}, fmt.Errorf("%w: invalid Bulk bill identity", ErrValidation)
	}
	var result Bill
	err := s.repository.Transact(ctx, func(tx Tx) error {
		projection, err := tx.ProjectBillFromBulk(ctx, userID, documentID, attemptGeneration)
		if err != nil {
			return err
		}
		bill := projection.Bill
		if bill.ID == uuid.Nil || bill.AccountID == uuid.Nil || bill.BulkDocumentID != documentID || bill.BulkAttemptGeneration != attemptGeneration || bill.Version < 1 {
			return fmt.Errorf("%w: Bulk projection returned inconsistent identity", ErrValidation)
		}
		if !projection.Created {
			result = bill
			return nil
		}
		if bill.Status != BillReview {
			return fmt.Errorf("%w: a new Bulk projection must begin in Review", ErrValidation)
		}
		if err := validateProjectedLines(bill); err != nil {
			return err
		}
		fromStatus := bill.Status
		resolution := paymentResolution{}
		if bill.UnresolvedCandidateCount > 0 || bill.ProjectionHasFailures {
			bill.Status, bill.PayoffTransferID, bill.PaidAt = BillReview, nil, nil
		} else {
			bill, resolution, err = s.resolvePayment(ctx, tx, userID, bill)
			if err != nil {
				return err
			}
		}
		result, err = s.saveMutatedBill(ctx, tx, userID, bill, bill.Version)
		if err != nil {
			return err
		}
		if err := tx.AppendBillEvent(ctx, userID, newEvent(bill.ID, "imported", "", map[string]string{"bulk_document_id": documentID.String(), "attempt_generation": fmt.Sprint(attemptGeneration)}, s.now())); err != nil {
			return err
		}
		return s.appendPaymentResolutionEvents(ctx, tx, userID, bill.ID, fromStatus, result.Status, resolution)
	})
	return result, err
}

func (s *Service) CorrectHeader(ctx context.Context, userID, billID uuid.UUID, request HeaderCorrectionRequest) (Bill, error) {
	if request.Header.PeriodStart == nil && request.Header.PeriodEnd == nil && request.Header.StatementDate == nil && request.Header.DueDate == nil && request.Header.SettlementCurrency == nil && request.Header.AmountDueMinor == nil {
		return Bill{}, fmt.Errorf("%w: at least one header field is required", ErrValidation)
	}
	reason, err := boundedReason(request.Header.Reason)
	if err != nil {
		return Bill{}, err
	}
	request.Header.Reason = reason
	var result Bill
	err = s.repository.Transact(ctx, func(tx Tx) error {
		bill, err := tx.GetBillForUpdate(ctx, userID, billID)
		if err != nil {
			return err
		}
		if err := requireVersion(bill, request.ExpectedVersion); err != nil {
			return err
		}
		if bill.Status != BillReview {
			return fmt.Errorf("%w: only Review bills can be corrected", ErrValidation)
		}
		fromStatus := bill.Status
		applyHeaderCorrection(&bill, request.Header)
		if err := validateHeaderFields(bill, false); err != nil {
			return err
		}
		bill, resolution, err := s.resolvePayment(ctx, tx, userID, bill)
		if err != nil {
			return err
		}
		bill.Version++
		bill.UpdatedAt = s.now().UTC()
		result, err = tx.SaveBill(ctx, userID, bill, request.ExpectedVersion)
		if err != nil {
			return err
		}
		if err := tx.AppendBillEvent(ctx, userID, newEvent(bill.ID, "header_corrected", reason, nil, s.now())); err != nil {
			return err
		}
		return s.appendPaymentResolutionEvents(ctx, tx, userID, bill.ID, fromStatus, result.Status, resolution)
	})
	return result, err
}

func applyHeaderCorrection(bill *Bill, correction CorrectHeaderInput) {
	if correction.PeriodStart != nil {
		bill.PeriodStart = datePointer(*correction.PeriodStart)
	}
	if correction.PeriodEnd != nil {
		bill.PeriodEnd = datePointer(*correction.PeriodEnd)
	}
	if correction.StatementDate != nil {
		bill.StatementDate = datePointer(*correction.StatementDate)
	}
	if correction.DueDate != nil {
		bill.DueDate = datePointer(*correction.DueDate)
	}
	if correction.SettlementCurrency != nil {
		currency := strings.ToUpper(strings.TrimSpace(*correction.SettlementCurrency))
		bill.SettlementCurrency = &currency
	}
	if correction.AmountDueMinor != nil {
		amount := *correction.AmountDueMinor
		bill.AmountDueMinor = &amount
	}
}

func (s *Service) AttachLine(ctx context.Context, userID, billID, lineID uuid.UUID, request AttachLineRequest) (Bill, error) {
	var result Bill
	err := s.repository.Transact(ctx, func(tx Tx) error {
		bill, line, err := loadMutableLine(ctx, tx, userID, billID, lineID, request.ExpectedVersion)
		if err != nil {
			return err
		}
		transaction, err := tx.GetTransactionForUpdate(ctx, userID, request.TransactionID)
		if err != nil {
			return err
		}
		linked, err := tx.IsTransactionLinkedToAnotherLine(ctx, userID, transaction.ID, line.ID)
		if err != nil {
			return err
		}
		if linked {
			return ErrDuplicateTransaction
		}
		exception, err := validateLineTransaction(bill, line, transaction, request.LinkExceptionReason)
		if err != nil {
			return err
		}
		fromStatus := bill.Status
		line.Status, line.TransactionID, line.Transaction, line.LinkExceptionReason = LineLinked, &transaction.ID, &transaction, exception
		line.UpdatedAt = s.now().UTC()
		if _, err := tx.SaveLine(ctx, userID, line); err != nil {
			return err
		}
		replaceLine(&bill, line)
		bill, resolution, err := s.resolvePayment(ctx, tx, userID, bill)
		if err != nil {
			return err
		}
		result, err = s.saveMutatedBill(ctx, tx, userID, bill, request.ExpectedVersion)
		if err != nil {
			return err
		}
		if err := tx.AppendBillEvent(ctx, userID, newEvent(bill.ID, "line_linked", "", map[string]string{"line_id": line.ID.String(), "transaction_id": transaction.ID.String()}, s.now())); err != nil {
			return err
		}
		return s.appendPaymentResolutionEvents(ctx, tx, userID, bill.ID, fromStatus, result.Status, resolution)
	})
	return result, err
}

func (s *Service) CreateLineTransaction(ctx context.Context, userID, billID, lineID uuid.UUID, request CreateLineRequest, idempotencyKey string) (Bill, error) {
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		return Bill{}, err
	}
	hash := requestHash("create-line-transaction", billID, fmt.Sprint(request.ExpectedVersion)+"\x00"+lineID.String()+"\x00"+request.CategoryID.String())
	var result Bill
	err := s.repository.Transact(ctx, func(tx Tx) error {
		replay, acquired, err := claimBillMutation(ctx, tx, userID, idempotencyKey, "create_line_transaction", billID, hash, s.now())
		if err != nil {
			return err
		}
		if !acquired {
			result = *replay
			return nil
		}
		bill, line, err := loadMutableLine(ctx, tx, userID, billID, lineID, request.ExpectedVersion)
		if err != nil {
			return err
		}
		if request.CategoryID == uuid.Nil || line.BulkCandidateID == nil || (line.Kind != LineActivity && line.Kind != LineRefund && line.Kind != LineFee && line.Kind != LineInterest) {
			return fmt.Errorf("%w: this line cannot create a transaction", ErrValidation)
		}
		fromStatus := bill.Status
		created, err := tx.CreateTransactionFromPinnedCandidate(ctx, userID, bill.ID, line.ID, request.CategoryID)
		if err != nil {
			return err
		}
		if _, err := validateLineTransaction(bill, line, created.Transaction, nil); err != nil {
			return fmt.Errorf("pinned candidate returned an invalid transaction: %w", err)
		}
		line.Status, line.TransactionID, line.Transaction = LineLinked, &created.Transaction.ID, &created.Transaction
		line.UpdatedAt = s.now().UTC()
		if _, err := tx.SaveLine(ctx, userID, line); err != nil {
			return err
		}
		replaceLine(&bill, line)
		bill, resolution, err := s.resolvePayment(ctx, tx, userID, bill)
		if err != nil {
			return err
		}
		result, err = s.saveMutatedBill(ctx, tx, userID, bill, request.ExpectedVersion)
		if err != nil {
			return err
		}
		if err := tx.AppendBillEvent(ctx, userID, newEvent(bill.ID, "line_transaction_created", "", map[string]string{"line_id": line.ID.String(), "transaction_id": created.Transaction.ID.String()}, s.now())); err != nil {
			return err
		}
		if err := s.appendPaymentResolutionEvents(ctx, tx, userID, bill.ID, fromStatus, result.Status, resolution); err != nil {
			return err
		}
		return tx.CompleteIdempotency(ctx, userID, idempotencyKey, "create_line_transaction", billID, hash, &result, 200)
	})
	return result, err
}

func (s *Service) IgnoreLine(ctx context.Context, userID, billID, lineID uuid.UUID, request ReasonedMutationRequest) (Bill, error) {
	reason, err := boundedReason(request.Reason)
	if err != nil {
		return Bill{}, err
	}
	var result Bill
	err = s.repository.Transact(ctx, func(tx Tx) error {
		bill, line, err := loadMutableLine(ctx, tx, userID, billID, lineID, request.ExpectedVersion)
		if err != nil {
			return err
		}
		fromStatus := bill.Status
		line.Status, line.ResolutionReason, line.UpdatedAt = LineIgnored, &reason, s.now().UTC()
		if _, err := tx.SaveLine(ctx, userID, line); err != nil {
			return err
		}
		replaceLine(&bill, line)
		bill, resolution, err := s.resolvePayment(ctx, tx, userID, bill)
		if err != nil {
			return err
		}
		result, err = s.saveMutatedBill(ctx, tx, userID, bill, request.ExpectedVersion)
		if err != nil {
			return err
		}
		if err := tx.AppendBillEvent(ctx, userID, newEvent(bill.ID, "line_ignored", reason, map[string]string{"line_id": line.ID.String()}, s.now())); err != nil {
			return err
		}
		return s.appendPaymentResolutionEvents(ctx, tx, userID, bill.ID, fromStatus, result.Status, resolution)
	})
	return result, err
}

// SelectPaymentCandidate resolves an ambiguous debit-only result without
// inventing a transaction-only bill row. The selected debit remains advisory
// until ConfirmPaymentCandidate atomically creates the Card leg.
func (s *Service) SelectPaymentCandidate(ctx context.Context, userID, billID uuid.UUID, request PaymentCandidateRequest, idempotencyKey string) (Bill, error) {
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		return Bill{}, err
	}
	hash := requestHash("select-payment-candidate", billID, fmt.Sprint(request.ExpectedVersion)+"\x00"+request.BankTransactionID.String())
	var result Bill
	err := s.repository.Transact(ctx, func(tx Tx) error {
		replay, acquired, err := claimBillMutation(ctx, tx, userID, idempotencyKey, "select_payment_candidate", billID, hash, s.now())
		if err != nil {
			return err
		}
		if !acquired {
			result = *replay
			return nil
		}
		bill, err := tx.GetBillForUpdate(ctx, userID, billID)
		if err != nil {
			return err
		}
		if err := requireVersion(bill, request.ExpectedVersion); err != nil {
			return err
		}
		if bill.Status != BillReview || !containsUUID(bill.AmbiguousPaymentCandidates, request.BankTransactionID) {
			return fmt.Errorf("%w: payment candidate is not selectable", ErrValidation)
		}
		candidate, err := tx.GetTransactionForUpdate(ctx, userID, request.BankTransactionID)
		if err != nil {
			return err
		}
		if err := validateBankDebitCandidate(bill, candidate); err != nil {
			return err
		}
		fromStatus := bill.Status
		bill.PaymentCandidateTransactionID = &candidate.ID
		bill.PaymentCandidateSelected = true
		bill.AmbiguousPaymentCandidates = nil
		bill.Status = BillUnpaid
		result, err = s.saveMutatedBill(ctx, tx, userID, bill, request.ExpectedVersion)
		if err != nil {
			return err
		}
		if err := tx.AppendBillEvent(ctx, userID, newEvent(bill.ID, "payment_selected", "", map[string]string{"transaction_id": candidate.ID.String()}, s.now())); err != nil {
			return err
		}
		if err := s.appendStatusChangeEvent(ctx, tx, userID, bill.ID, fromStatus, result.Status); err != nil {
			return err
		}
		return tx.CompleteIdempotency(ctx, userID, idempotencyKey, "select_payment_candidate", billID, hash, &result, 200)
	})
	return result, err
}

func (s *Service) ConfirmPaymentCandidate(ctx context.Context, userID, billID uuid.UUID, request PaymentCandidateRequest, idempotencyKey string) (Bill, error) {
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		return Bill{}, err
	}
	hash := requestHash("confirm-payment-candidate", billID, fmt.Sprint(request.ExpectedVersion)+"\x00"+request.BankTransactionID.String())
	var result Bill
	err := s.repository.Transact(ctx, func(tx Tx) error {
		claim, err := tx.ClaimIdempotency(ctx, userID, idempotencyKey, "confirm_payment_candidate", billID, hash, s.now().UTC().Add(idempotencyLifetime))
		if err != nil {
			return err
		}
		if claim.State == IdempotencyReplay {
			if claim.Bill == nil || claim.ResponseStatus != 200 {
				return fmt.Errorf("invalid idempotency replay")
			}
			result = *claim.Bill
			return nil
		}
		if claim.State != IdempotencyAcquired {
			return ErrIdempotencyInFlight
		}
		bill, err := tx.GetBillForUpdate(ctx, userID, billID)
		if err != nil {
			return err
		}
		if err := requireVersion(bill, request.ExpectedVersion); err != nil {
			return err
		}
		if bill.Status != BillUnpaid || bill.PaymentCandidateTransactionID == nil || *bill.PaymentCandidateTransactionID != request.BankTransactionID {
			return fmt.Errorf("%w: payment suggestion changed", ErrValidation)
		}
		candidate, err := tx.GetTransactionForUpdate(ctx, userID, request.BankTransactionID)
		if err != nil {
			return err
		}
		if err := validateBankDebitCandidate(bill, candidate); err != nil {
			return err
		}
		fromStatus := bill.Status
		payoff, err := tx.CreateMissingCardLegAndTransfer(ctx, userID, bill.ID, candidate.ID, bill.AccountID, *bill.SettlementCurrency, *bill.AmountDueMinor)
		if err != nil {
			return err
		}
		if err := lockPayoffTreatments(ctx, tx, userID, payoff); err != nil {
			return err
		}
		markPaid(&bill, payoff.Transfer.ID, s.now())
		result, err = s.saveMutatedBill(ctx, tx, userID, bill, request.ExpectedVersion)
		if err != nil {
			return err
		}
		if err := tx.AppendBillEvent(ctx, userID, newEvent(bill.ID, "payment_confirmed", "", map[string]string{"transfer_id": payoff.Transfer.ID.String()}, s.now())); err != nil {
			return err
		}
		if err := s.appendStatusChangeEvent(ctx, tx, userID, bill.ID, fromStatus, result.Status); err != nil {
			return err
		}
		return tx.CompleteIdempotency(ctx, userID, idempotencyKey, "confirm_payment_candidate", billID, hash, &result, 200)
	})
	return result, err
}

func (s *Service) PayInFull(ctx context.Context, userID, billID uuid.UUID, request PayoffRequest, idempotencyKey string) (Bill, error) {
	if request.BankAccountID == uuid.Nil {
		return Bill{}, fmt.Errorf("%w: bank_account_id is required", ErrValidation)
	}
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		return Bill{}, err
	}
	hash := requestHash("payoff", billID, fmt.Sprint(request.ExpectedVersion)+"\x00"+request.BankAccountID.String())
	var result Bill
	err := s.repository.Transact(ctx, func(tx Tx) error {
		claim, err := tx.ClaimIdempotency(ctx, userID, idempotencyKey, "payoff", billID, hash, s.now().UTC().Add(idempotencyLifetime))
		if err != nil {
			return err
		}
		if claim.State == IdempotencyReplay {
			if claim.Bill == nil || claim.ResponseStatus != 200 {
				return fmt.Errorf("invalid idempotency replay")
			}
			result = *claim.Bill
			return nil
		}
		if claim.State != IdempotencyAcquired {
			return ErrIdempotencyInFlight
		}
		bill, err := tx.GetBillForUpdate(ctx, userID, billID)
		if err != nil {
			return err
		}
		if err := requireVersion(bill, request.ExpectedVersion); err != nil {
			return err
		}
		if bill.Status != BillUnpaid || !headerComplete(bill) {
			return fmt.Errorf("%w: only a complete Unpaid bill can be paid", ErrValidation)
		}
		fromStatus := bill.Status
		payoff, err := tx.CreateFullPayoffTransfer(ctx, userID, request.BankAccountID, bill.AccountID, *bill.SettlementCurrency, *bill.AmountDueMinor, s.now().UTC())
		if err != nil {
			return err
		}
		if err := lockPayoffTreatments(ctx, tx, userID, payoff); err != nil {
			return err
		}
		markPaid(&bill, payoff.Transfer.ID, s.now())
		result, err = s.saveMutatedBill(ctx, tx, userID, bill, request.ExpectedVersion)
		if err != nil {
			return err
		}
		if err := tx.AppendBillEvent(ctx, userID, newEvent(bill.ID, "payoff_created", "", map[string]string{"transfer_id": payoff.Transfer.ID.String()}, s.now())); err != nil {
			return err
		}
		if err := s.appendStatusChangeEvent(ctx, tx, userID, bill.ID, fromStatus, result.Status); err != nil {
			return err
		}
		return tx.CompleteIdempotency(ctx, userID, idempotencyKey, "payoff", billID, hash, &result, 200)
	})
	return result, err
}

func (s *Service) VoidBill(ctx context.Context, userID, billID uuid.UUID, request ReasonedMutationRequest, idempotencyKey string) (Bill, error) {
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		return Bill{}, err
	}
	reason, err := boundedReason(request.Reason)
	if err != nil {
		return Bill{}, err
	}
	hash := requestHash("void", billID, fmt.Sprint(request.ExpectedVersion)+"\x00"+reason)
	var result Bill
	err = s.repository.Transact(ctx, func(tx Tx) error {
		replay, acquired, err := claimBillMutation(ctx, tx, userID, idempotencyKey, "void", billID, hash, s.now())
		if err != nil {
			return err
		}
		if !acquired {
			result = *replay
			return nil
		}
		bill, err := tx.GetBillForUpdate(ctx, userID, billID)
		if err != nil {
			return err
		}
		if err := requireVersion(bill, request.ExpectedVersion); err != nil {
			return err
		}
		if bill.Status != BillUnpaid {
			return fmt.Errorf("%w: only Unpaid bills can be voided", ErrValidation)
		}
		fromStatus := bill.Status
		bill.Status, bill.PaymentCandidateTransactionID, bill.AmbiguousPaymentCandidates = BillVoid, nil, nil
		bill.PaymentCandidateSelected = false
		bill.VoidReason = &reason
		result, err = s.saveMutatedBill(ctx, tx, userID, bill, request.ExpectedVersion)
		if err != nil {
			return err
		}
		if err := tx.AppendBillEvent(ctx, userID, newEvent(bill.ID, "voided", reason, nil, s.now())); err != nil {
			return err
		}
		if err := s.appendStatusChangeEvent(ctx, tx, userID, bill.ID, fromStatus, result.Status); err != nil {
			return err
		}
		return tx.CompleteIdempotency(ctx, userID, idempotencyKey, "void", billID, hash, &result, 200)
	})
	return result, err
}

func (s *Service) DiscardReviewBill(ctx context.Context, userID, billID uuid.UUID, expectedVersion int, idempotencyKey string) error {
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		return err
	}
	hash := requestHash("discard", billID, fmt.Sprint(expectedVersion))
	return s.repository.Transact(ctx, func(tx Tx) error {
		claim, err := tx.ClaimIdempotency(ctx, userID, idempotencyKey, "discard", billID, hash, s.now().UTC().Add(idempotencyLifetime))
		if err != nil {
			return err
		}
		if claim.State == IdempotencyReplay {
			if claim.ResponseStatus != 204 || claim.Bill != nil {
				return fmt.Errorf("invalid idempotency replay")
			}
			return nil
		}
		if claim.State != IdempotencyAcquired {
			return ErrIdempotencyInFlight
		}
		bill, err := tx.GetBillForUpdate(ctx, userID, billID)
		if err != nil {
			return err
		}
		if err := requireVersion(bill, expectedVersion); err != nil {
			return err
		}
		if bill.Status != BillReview {
			return fmt.Errorf("%w: only Review bills can be discarded", ErrValidation)
		}
		if err := tx.DeleteReviewBill(ctx, userID, billID, expectedVersion); err != nil {
			return err
		}
		return tx.CompleteIdempotency(ctx, userID, idempotencyKey, "discard", billID, hash, nil, 204)
	})
}

func (s *Service) saveMutatedBill(ctx context.Context, tx Tx, userID uuid.UUID, bill Bill, expectedVersion int) (Bill, error) {
	bill.Version = expectedVersion + 1
	bill.UpdatedAt = s.now().UTC()
	return tx.SaveBill(ctx, userID, bill, expectedVersion)
}

func loadMutableLine(ctx context.Context, tx Tx, userID, billID, lineID uuid.UUID, expectedVersion int) (Bill, BillLine, error) {
	bill, err := tx.GetBillForUpdate(ctx, userID, billID)
	if err != nil {
		return Bill{}, BillLine{}, err
	}
	if err := requireVersion(bill, expectedVersion); err != nil {
		return Bill{}, BillLine{}, err
	}
	if bill.Status != BillReview {
		return Bill{}, BillLine{}, fmt.Errorf("%w: lines can be resolved only while the bill is in Review", ErrValidation)
	}
	line, err := tx.GetLineForUpdate(ctx, userID, billID, lineID)
	if err != nil {
		return Bill{}, BillLine{}, err
	}
	if line.Status != LinePending || line.Kind == LineSummary {
		return Bill{}, BillLine{}, fmt.Errorf("%w: line is not pending", ErrValidation)
	}
	return bill, line, nil
}

func validateLineTransaction(bill Bill, line BillLine, transaction CanonicalTransaction, suppliedException *string) (*string, error) {
	if transaction.AccountID != bill.AccountID || transaction.AccountType != "credit_card" {
		return nil, fmt.Errorf("%w: transaction must belong to this Credit Card", ErrValidation)
	}
	wantDirection := DirectionDebit
	if line.Kind == LineRefund || line.Kind == LinePayment {
		wantDirection = DirectionCredit
	}
	if transaction.Direction != wantDirection {
		return nil, fmt.Errorf("%w: transaction direction does not match the bill line", ErrValidation)
	}
	if line.Kind == LinePayment {
		if transaction.Transfer == nil || transaction.Transfer.CreditTransactionID != transaction.ID || transaction.Transfer.CreditAccountID != bill.AccountID ||
			transaction.Transfer.DebitAccountType != "bank_account" || transaction.Transfer.Currency != transaction.OriginalCurrency ||
			transaction.Transfer.AmountMinor != transaction.OriginalAmountMinor {
			return nil, fmt.Errorf("%w: payment lines require a Bank to Card transfer credit leg", ErrValidation)
		}
	}
	mismatch := false
	if line.AmountMinor != nil && transaction.OriginalAmountMinor != *line.AmountMinor {
		mismatch = true
	}
	if line.Currency != nil && transaction.OriginalCurrency != *line.Currency {
		mismatch = true
	}
	start, end := bill.PeriodStart, bill.PeriodEnd
	if line.Kind == LinePayment {
		start, end = bill.StatementDate, bill.DueDate
	}
	if start == nil || end == nil || !dateWithin(transaction.OccurredAt, *start, *end) {
		mismatch = true
	}
	if line.OccurredAt != nil && line.TimePrecision != nil {
		if *line.TimePrecision == "date" {
			mismatch = mismatch || !dateOnly(transaction.OccurredAt).Equal(dateOnly(*line.OccurredAt))
		} else {
			mismatch = mismatch || math.Abs(transaction.OccurredAt.Sub(*line.OccurredAt).Seconds()) > 600
		}
	} else if line.OccurredOn != nil && !dateOnly(transaction.OccurredAt).Equal(dateOnly(*line.OccurredOn)) {
		mismatch = true
	}
	if mismatch {
		reason, err := optionalBoundedReason(suppliedException, true)
		if err != nil {
			return nil, fmt.Errorf("%w: link_exception_reason is required for a non-exact match", ErrValidation)
		}
		return reason, nil
	}
	if suppliedException != nil {
		return nil, fmt.Errorf("%w: link_exception_reason is not allowed for an exact match", ErrValidation)
	}
	return nil, nil
}

type paymentResolution struct {
	Searched                  bool
	ExactTransferCount        int
	BankDebitCandidateCount   int
	AutomaticPayoffTransferID *uuid.UUID
}

func (s *Service) resolvePayment(ctx context.Context, tx Tx, userID uuid.UUID, bill Bill) (Bill, paymentResolution, error) {
	resolution := paymentResolution{}
	bill.PayoffTransferID, bill.PaidAt = nil, nil
	bill.PaymentCandidateTransactionID, bill.AmbiguousPaymentCandidates = nil, nil
	bill.PaymentCandidateSelected = false
	if bill.UnresolvedCandidateCount > 0 || !headerComplete(bill) || hasPendingLines(bill.Lines) {
		bill.Status = BillReview
		return bill, resolution, nil
	}
	if err := validateHeaderFields(bill, true); err != nil {
		bill.Status = BillReview
		return bill, resolution, nil
	}
	resolution.Searched = true
	matches, err := tx.FindExactPayoffTransfers(ctx, userID, bill.AccountID, *bill.SettlementCurrency, *bill.AmountDueMinor, *bill.StatementDate, *bill.DueDate)
	if err != nil {
		return Bill{}, paymentResolution{}, err
	}
	resolution.ExactTransferCount = len(matches)
	if len(matches) == 1 {
		payoff := PayoffResult{Transfer: matches[0], TreatmentTransactionIDs: []uuid.UUID{matches[0].DebitTransactionID, matches[0].CreditTransactionID}}
		if err := lockPayoffTreatments(ctx, tx, userID, payoff); err != nil {
			return Bill{}, paymentResolution{}, err
		}
		markPaid(&bill, matches[0].ID, s.now())
		resolution.AutomaticPayoffTransferID = &matches[0].ID
		return bill, resolution, nil
	}
	if len(matches) > 1 {
		bill.Status = BillReview
		return bill, resolution, nil
	}
	candidates, err := tx.FindBankDebitCandidates(ctx, userID, bill.ID, *bill.SettlementCurrency, *bill.AmountDueMinor, *bill.StatementDate, *bill.DueDate)
	if err != nil {
		return Bill{}, paymentResolution{}, err
	}
	resolution.BankDebitCandidateCount = len(candidates)
	switch len(candidates) {
	case 0:
		bill.Status = BillUnpaid
	case 1:
		if err := validateBankDebitCandidate(bill, candidates[0]); err != nil {
			return Bill{}, paymentResolution{}, err
		}
		bill.Status = BillUnpaid
		bill.PaymentCandidateTransactionID = &candidates[0].ID
		bill.PaymentCandidateSelected = false
	default:
		bill.Status = BillReview
		for _, candidate := range candidates {
			if validateBankDebitCandidate(bill, candidate) == nil {
				bill.AmbiguousPaymentCandidates = append(bill.AmbiguousPaymentCandidates, candidate.ID)
			}
		}
	}
	return bill, resolution, nil
}

func validateProjectedLines(bill Bill) error {
	seenIndexes := make(map[int]struct{}, len(bill.Lines))
	seenCandidates := make(map[uuid.UUID]struct{}, len(bill.Lines))
	for _, line := range bill.Lines {
		if line.ID == uuid.Nil || line.BillID != bill.ID || line.Index < 1 || strings.TrimSpace(line.Description) == "" || len(line.Description) > 500 {
			return fmt.Errorf("%w: Bulk projection returned an invalid bill line", ErrValidation)
		}
		if _, exists := seenIndexes[line.Index]; exists {
			return fmt.Errorf("%w: duplicate bill line index", ErrValidation)
		}
		seenIndexes[line.Index] = struct{}{}
		if line.Kind == LineSummary {
			if line.Status != LineIgnored || line.TransactionID != nil {
				return fmt.Errorf("%w: summary lines must be ignored", ErrValidation)
			}
			continue
		}
		if line.Kind != LineActivity && line.Kind != LineRefund && line.Kind != LineFee && line.Kind != LineInterest && line.Kind != LinePayment {
			return fmt.Errorf("%w: invalid bill line kind", ErrValidation)
		}
		if line.BulkCandidateID == nil {
			return fmt.Errorf("%w: evidence-backed line has no pinned candidate", ErrValidation)
		}
		if _, exists := seenCandidates[*line.BulkCandidateID]; exists {
			return fmt.Errorf("%w: duplicate pinned bill candidate", ErrValidation)
		}
		seenCandidates[*line.BulkCandidateID] = struct{}{}
		if line.Status != LinePending && line.Status != LineLinked && line.Status != LineIgnored {
			return fmt.Errorf("%w: invalid bill line resolution", ErrValidation)
		}
	}
	return nil
}

func validateBankDebitCandidate(bill Bill, transaction CanonicalTransaction) error {
	if !headerComplete(bill) || transaction.AccountType != "bank_account" || transaction.Direction != DirectionDebit ||
		transaction.OriginalCurrency != *bill.SettlementCurrency || transaction.OriginalAmountMinor != *bill.AmountDueMinor ||
		!dateWithin(transaction.OccurredAt, *bill.StatementDate, *bill.DueDate) || transaction.Transfer != nil {
		return fmt.Errorf("%w: transaction is not an exact Bank debit candidate", ErrValidation)
	}
	return nil
}

func validateHeaderFields(bill Bill, requireComplete bool) error {
	if bill.PeriodStart != nil && bill.PeriodEnd != nil && bill.PeriodStart.After(*bill.PeriodEnd) {
		return fmt.Errorf("%w: period_start must not follow period_end", ErrValidation)
	}
	if bill.StatementDate != nil && bill.DueDate != nil && bill.StatementDate.After(*bill.DueDate) {
		return fmt.Errorf("%w: due_date must not precede statement_date", ErrValidation)
	}
	if bill.SettlementCurrency != nil && (!reconciliation.IsISO4217(*bill.SettlementCurrency) || *bill.SettlementCurrency != strings.ToUpper(*bill.SettlementCurrency)) {
		return fmt.Errorf("%w: invalid settlement_currency", ErrValidation)
	}
	if bill.AmountDueMinor != nil && *bill.AmountDueMinor <= 0 {
		return fmt.Errorf("%w: amount_due_minor must be positive", ErrValidation)
	}
	if requireComplete && !headerComplete(bill) {
		return fmt.Errorf("%w: bill header is incomplete", ErrValidation)
	}
	return nil
}

func headerComplete(bill Bill) bool {
	return bill.PeriodStart != nil && bill.PeriodEnd != nil && bill.StatementDate != nil && bill.DueDate != nil && bill.SettlementCurrency != nil && bill.AmountDueMinor != nil
}

func hasPendingLines(lines []BillLine) bool {
	for _, line := range lines {
		if line.Status == LinePending {
			return true
		}
	}
	return false
}

func requireVersion(bill Bill, expected int) error {
	if expected < 1 || expected != bill.Version {
		return ErrVersionConflict
	}
	return nil
}

func replaceLine(bill *Bill, replacement BillLine) {
	for index := range bill.Lines {
		if bill.Lines[index].ID == replacement.ID {
			bill.Lines[index] = replacement
			return
		}
	}
}

func boundedReason(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) < 1 || len(value) > 500 {
		return "", fmt.Errorf("%w: reason must contain 1 to 500 characters", ErrValidation)
	}
	return value, nil
}

func optionalBoundedReason(value *string, required bool) (*string, error) {
	if value == nil {
		if required {
			return nil, ErrValidation
		}
		return nil, nil
	}
	trimmed, err := boundedReason(*value)
	if err != nil {
		return nil, err
	}
	return &trimmed, nil
}

func dateWithin(value, start, end time.Time) bool {
	date := dateOnly(value)
	return !date.Before(dateOnly(start)) && !date.After(dateOnly(end))
}

func dateOnly(value time.Time) time.Time {
	year, month, day := value.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

func datePointer(value time.Time) *time.Time {
	value = dateOnly(value)
	return &value
}

func markPaid(bill *Bill, transferID uuid.UUID, now time.Time) {
	paidAt := now.UTC()
	bill.Status = BillPaid
	bill.PayoffTransferID = &transferID
	bill.PaymentCandidateTransactionID = nil
	bill.PaymentCandidateSelected = false
	bill.AmbiguousPaymentCandidates = nil
	bill.VoidReason = nil
	bill.PaidAt = &paidAt
}

func lockPayoffTreatments(ctx context.Context, tx Tx, userID uuid.UUID, result PayoffResult) error {
	ids := result.TreatmentTransactionIDs
	if len(ids) == 0 {
		ids = []uuid.UUID{result.Transfer.DebitTransactionID, result.Transfer.CreditTransactionID}
	}
	if len(ids) != 2 || ids[0] == uuid.Nil || ids[1] == uuid.Nil || ids[0] == ids[1] {
		return fmt.Errorf("%w: payoff must return two transfer legs", ErrValidation)
	}
	return tx.LockSystemPayoffExclusions(ctx, userID, ids, systemPayoffReason)
}

func validateIdempotencyKey(key string) error {
	if !safeIdempotencyKey.MatchString(key) {
		return fmt.Errorf("%w: Idempotency-Key must contain 32 to 128 visible ASCII characters", ErrValidation)
	}
	return nil
}

func claimBillMutation(ctx context.Context, tx Tx, userID uuid.UUID, key, operation string, billID uuid.UUID, hash string, now time.Time) (*Bill, bool, error) {
	claim, err := tx.ClaimIdempotency(ctx, userID, key, operation, billID, hash, now.UTC().Add(idempotencyLifetime))
	if err != nil {
		return nil, false, err
	}
	if claim.State == IdempotencyReplay {
		if claim.Bill == nil || claim.ResponseStatus != 200 {
			return nil, false, fmt.Errorf("invalid idempotency replay")
		}
		return claim.Bill, false, nil
	}
	if claim.State != IdempotencyAcquired {
		return nil, false, ErrIdempotencyInFlight
	}
	return nil, true, nil
}

func requestHash(action string, resource uuid.UUID, value string) string {
	digest := sha256.Sum256([]byte(action + "\x00" + resource.String() + "\x00" + value))
	return hex.EncodeToString(digest[:])
}

func containsUUID(values []uuid.UUID, target uuid.UUID) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func newEvent(billID uuid.UUID, kind, reason string, details map[string]string, now time.Time) BillEvent {
	return BillEvent{ID: uuid.New(), BillID: billID, Kind: kind, Reason: reason, Details: details, CreatedAt: now.UTC()}
}

func (s *Service) appendPaymentResolutionEvents(ctx context.Context, tx Tx, userID, billID uuid.UUID, fromStatus, toStatus BillStatus, resolution paymentResolution) error {
	if resolution.Searched {
		details := map[string]string{
			"exact_transfer_count":       fmt.Sprint(resolution.ExactTransferCount),
			"bank_debit_candidate_count": fmt.Sprint(resolution.BankDebitCandidateCount),
		}
		if err := tx.AppendBillEvent(ctx, userID, newEvent(billID, "payment_candidates_found", "", details, s.now())); err != nil {
			return err
		}
		if resolution.AutomaticPayoffTransferID != nil {
			if err := tx.AppendBillEvent(ctx, userID, newEvent(billID, "payment_confirmed", "", map[string]string{
				"detection":   "automatic",
				"transfer_id": resolution.AutomaticPayoffTransferID.String(),
			}, s.now())); err != nil {
				return err
			}
		}
	}
	return s.appendStatusChangeEvent(ctx, tx, userID, billID, fromStatus, toStatus)
}

func (s *Service) appendStatusChangeEvent(ctx context.Context, tx Tx, userID, billID uuid.UUID, fromStatus, toStatus BillStatus) error {
	if fromStatus == toStatus {
		return nil
	}
	from, to := fromStatus, toStatus
	event := newEvent(billID, "status_changed", "", nil, s.now())
	event.FromStatus, event.ToStatus = &from, &to
	return tx.AppendBillEvent(ctx, userID, event)
}

func IsConflict(err error) bool {
	return errors.Is(err, ErrVersionConflict) || errors.Is(err, ErrIdempotencyConflict) || errors.Is(err, ErrDuplicateTransaction)
}

// Package creditcard owns the bill projection and settlement workflow. It does
// not own upload, extraction, canonical transaction, or transfer persistence.
package creditcard

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	ErrNotFound             = errors.New("credit card resource not found")
	ErrValidation           = errors.New("credit card validation failed")
	ErrVersionConflict      = errors.New("credit card version conflict")
	ErrIdempotencyConflict  = errors.New("idempotency key was reused for another request")
	ErrIdempotencyInFlight  = errors.New("idempotent request is still in progress")
	ErrDuplicateTransaction = errors.New("transaction is linked to another bill line")
)

type BillStatus string

const (
	BillReview BillStatus = "review"
	BillUnpaid BillStatus = "unpaid"
	BillPaid   BillStatus = "paid"
	BillVoid   BillStatus = "void"
)

type LineKind string

const (
	LineActivity LineKind = "activity"
	LineRefund   LineKind = "refund"
	LineFee      LineKind = "fee"
	LineInterest LineKind = "interest"
	LinePayment  LineKind = "payment"
	LineSummary  LineKind = "summary"
)

type LineStatus string

const (
	LinePending LineStatus = "pending"
	LineLinked  LineStatus = "linked"
	LineIgnored LineStatus = "ignored"
)

type Bill struct {
	ID                            uuid.UUID
	AccountID                     uuid.UUID
	BulkDocumentID                uuid.UUID
	BulkAttemptGeneration         int
	PeriodStart                   *time.Time
	PeriodEnd                     *time.Time
	StatementDate                 *time.Time
	DueDate                       *time.Time
	SettlementCurrency            *string
	AmountDueMinor                *int64
	MinimumPaymentMinor           *int64
	PreviousBalanceMinor          *int64
	UnresolvedCandidateCount      int
	Status                        BillStatus
	PayoffTransferID              *uuid.UUID
	PaymentCandidateTransactionID *uuid.UUID
	PaymentCandidateSelected      bool
	AmbiguousPaymentCandidates    []uuid.UUID
	Version                       int
	VoidReason                    *string
	CreatedAt                     time.Time
	UpdatedAt                     time.Time
	PaidAt                        *time.Time
	EvidenceURL                   string
	ProjectionHasFailures         bool
	Lines                         []BillLine
	Events                        []BillEvent
}

type BillLine struct {
	ID                  uuid.UUID
	BillID              uuid.UUID
	BulkCandidateID     *uuid.UUID
	Kind                LineKind
	Status              LineStatus
	ResolutionReason    *string
	LinkExceptionReason *string
	Index               int
	OccurredOn          *time.Time
	OccurredAt          *time.Time
	TimePrecision       *string
	Description         string
	AmountMinor         *int64
	Currency            *string
	TransactionID       *uuid.UUID
	Transaction         *CanonicalTransaction
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type BillSummary struct {
	ID                       uuid.UUID
	AccountID                uuid.UUID
	PeriodStart              *time.Time
	PeriodEnd                *time.Time
	StatementDate            *time.Time
	DueDate                  *time.Time
	SettlementCurrency       *string
	AmountDueMinor           *int64
	UnresolvedCandidateCount int
	Status                   BillStatus
	Version                  int
	UpdatedAt                time.Time
}

type BillPage struct {
	Bills      []BillSummary
	NextCursor *string
}

type TransactionDirection string

const (
	DirectionDebit  TransactionDirection = "debit"
	DirectionCredit TransactionDirection = "credit"
)

type CanonicalTransaction struct {
	ID                  uuid.UUID
	AccountID           uuid.UUID
	AccountType         string
	Direction           TransactionDirection
	OriginalCurrency    string
	OriginalAmountMinor int64
	OccurredAt          time.Time
	TimePrecision       string
	Transfer            *InternalTransfer
}

type InternalTransfer struct {
	ID                  uuid.UUID
	DebitTransactionID  uuid.UUID
	CreditTransactionID uuid.UUID
	DebitAccountID      uuid.UUID
	DebitAccountType    string
	CreditAccountID     uuid.UUID
	Currency            string
	AmountMinor         int64
	OccurredAt          time.Time
}

type BillEvent struct {
	ID         uuid.UUID         `json:"id"`
	BillID     uuid.UUID         `json:"bill_id"`
	Kind       string            `json:"event_type"`
	Reason     string            `json:"reason,omitempty"`
	FromStatus *BillStatus       `json:"from_status,omitempty"`
	ToStatus   *BillStatus       `json:"to_status,omitempty"`
	Details    map[string]string `json:"details"`
	CreatedAt  time.Time         `json:"created_at"`
}

type CorrectHeaderInput struct {
	PeriodStart        *time.Time
	PeriodEnd          *time.Time
	StatementDate      *time.Time
	DueDate            *time.Time
	SettlementCurrency *string
	AmountDueMinor     *int64
	Reason             string
}

type LineCreateResult struct {
	Transaction CanonicalTransaction
	// The concrete adapter delegates this to Bulk Import's pinned-candidate
	// operation, which also owns evidence attachment and candidate outcome.
	BulkCandidateOutcomeID uuid.UUID
}

type PayoffResult struct {
	Transfer                InternalTransfer
	TreatmentTransactionIDs []uuid.UUID
}

// BulkProjectionResult is returned by the narrow server-side Bulk bridge. The
// concrete adapter loads the typed summary, selected Account, pinned candidate
// outcomes, and evidence itself; no parsed or monetary browser payload crosses
// this boundary.
type BulkProjectionResult struct {
	Bill    Bill
	Created bool
}

type IdempotencyClaimState string

const (
	IdempotencyAcquired IdempotencyClaimState = "acquired"
	IdempotencyReplay   IdempotencyClaimState = "replay"
	IdempotencyBusy     IdempotencyClaimState = "busy"
)

type IdempotencyClaim struct {
	State          IdempotencyClaimState
	Bill           *Bill
	ResponseStatus int
}

// Repository owns transaction demarcation. A PostgreSQL adapter should pass one
// pgx.Tx-backed Tx to the callback and roll back on any returned error.
type Repository interface {
	ListBills(context.Context, uuid.UUID, uuid.UUID, *string, int) (BillPage, error)
	GetBill(context.Context, uuid.UUID, uuid.UUID) (Bill, error)
	Transact(context.Context, func(Tx) error) error
}

// Tx deliberately exposes composable primitives instead of the existing
// transactionstore.CreateInternalTransfer, which starts and commits its own
// transaction. Every method must scope reads and writes by userID.
type Tx interface {
	GetBillForUpdate(context.Context, uuid.UUID, uuid.UUID) (Bill, error)
	ProjectBillFromBulk(context.Context, uuid.UUID, uuid.UUID, int) (BulkProjectionResult, error)
	GetLineForUpdate(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (BillLine, error)
	GetTransactionForUpdate(context.Context, uuid.UUID, uuid.UUID) (CanonicalTransaction, error)
	SaveBill(context.Context, uuid.UUID, Bill, int) (Bill, error)
	SaveLine(context.Context, uuid.UUID, BillLine) (BillLine, error)
	AppendBillEvent(context.Context, uuid.UUID, BillEvent) error
	DeleteReviewBill(context.Context, uuid.UUID, uuid.UUID, int) error

	IsTransactionLinkedToAnotherLine(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (bool, error)
	CreateTransactionFromPinnedCandidate(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID) (LineCreateResult, error)

	// FindExactPayoffTransfers returns only structurally valid owned Bank-to-Card
	// transfers matching every supplied field and the inclusive date window.
	FindExactPayoffTransfers(context.Context, uuid.UUID, uuid.UUID, string, int64, time.Time, time.Time) ([]InternalTransfer, error)
	FindBankDebitCandidates(context.Context, uuid.UUID, uuid.UUID, string, int64, time.Time, time.Time) ([]CanonicalTransaction, error)
	CreateMissingCardLegAndTransfer(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, string, int64) (PayoffResult, error)
	// CreateFullPayoffTransfer must reject an inactive/non-bank source Account
	// and use the supplied exact currency, amount and occurrence timestamp.
	CreateFullPayoffTransfer(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, string, int64, time.Time) (PayoffResult, error)
	LockSystemPayoffExclusions(context.Context, uuid.UUID, []uuid.UUID, string) error

	ClaimIdempotency(context.Context, uuid.UUID, string, string, uuid.UUID, string, time.Time) (IdempotencyClaim, error)
	CompleteIdempotency(context.Context, uuid.UUID, string, string, uuid.UUID, string, *Bill, int) error
}

package accountbalances

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	ErrNotFound                 = errors.New("account balance resource not found")
	ErrValidation               = errors.New("account balance validation failed")
	ErrTreatmentVersionConflict = errors.New("calculation treatment version conflict")
	ErrSystemTreatmentImmutable = errors.New("system calculation treatment is immutable")
	ErrIdempotencyConflict      = errors.New("idempotency key was reused for another request")
	ErrIdempotencyInFlight      = errors.New("idempotent request is still in progress")
)

type AccountSide string

const (
	AccountAsset     AccountSide = "asset"
	AccountLiability AccountSide = "liability"
)

type TransactionKind string

const (
	TransactionDebit  TransactionKind = "debit"
	TransactionCredit TransactionKind = "credit"
)

type BalanceAmount struct {
	Currency   string `json:"currency"`
	MinorUnits int64  `json:"-"`
}

// OpeningBalanceRevision is an aggregate over one immutable revision header
// and its normalized per-currency child rows. It is not a JSON history blob.
type OpeningBalanceRevision struct {
	ID               uuid.UUID
	AccountID        uuid.UUID
	Version          int
	Balances         []BalanceAmount
	AsOf             time.Time
	CorrectionReason *string
	ChangedByUserID  uuid.UUID
	ChangedAt        time.Time
}

type FinancialAccount struct {
	ID              uuid.UUID
	Name            string
	AccountType     string
	Side            AccountSide
	Baseline        []BalanceAmount
	BaselineAsOf    *time.Time
	BaselineVersion int
}

func (a FinancialAccount) Configured() bool {
	return a.BaselineVersion > 0 && a.BaselineAsOf != nil
}

type ConfirmedMovement struct {
	TransactionID uuid.UUID
	Kind          TransactionKind
	Currency      string
	MinorUnits    int64
	OccurredAt    time.Time
}

type ReplaceOpeningBalanceParams struct {
	ExpectedVersion int
	Balances        []BalanceAmount
	AsOf            time.Time
	Revision        OpeningBalanceRevision
	IdempotencyKey  string
	RequestHash     string
	ExpiresAt       time.Time
}

type SpendingBasis string

const (
	SpendingTransactionTotal SpendingBasis = "transaction_total"
	SpendingLineItems        SpendingBasis = "line_items"
	SpendingExclude          SpendingBasis = "exclude"
)

type TreatmentSource string

const (
	TreatmentDefault TreatmentSource = "default"
	TreatmentUser    TreatmentSource = "user"
	TreatmentSystem  TreatmentSource = "system"
)

type LineItemAmount struct {
	Currency       string
	LineTotalMinor *int64
}

type TransactionForTreatment struct {
	ID                  uuid.UUID
	OriginalCurrency    string
	OriginalAmountMinor int64
	LineItems           []LineItemAmount
	Treatment           *CalculationTreatment
}

type CalculationTreatment struct {
	TransactionID uuid.UUID       `json:"transaction_id"`
	Basis         SpendingBasis   `json:"spending_basis"`
	Source        TreatmentSource `json:"source"`
	Reason        string          `json:"reason"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

type CalculationTreatmentView struct {
	TransactionID uuid.UUID       `json:"transaction_id"`
	Basis         SpendingBasis   `json:"spending_basis"`
	Source        TreatmentSource `json:"source"`
	Reason        *string         `json:"reason"`
	Immutable     bool            `json:"immutable"`
	UpdatedAt     *time.Time      `json:"updated_at"`
}

type PutUserTreatmentParams struct {
	Basis             SpendingBasis
	Reason            string
	ExpectedUpdatedAt *time.Time
}

// Repository is the persistence boundary for normalized account-baseline
// revisions and calculation treatments. ReplaceOpeningBalance and
// LockSystemPayoffExclusions must each be atomic in their concrete adapter.
type Repository interface {
	ListFinancialAccounts(context.Context, uuid.UUID) ([]FinancialAccount, error)
	GetFinancialAccount(context.Context, uuid.UUID, uuid.UUID) (FinancialAccount, error)
	ListConfirmedMovementsAfter(context.Context, uuid.UUID, uuid.UUID, time.Time) ([]ConfirmedMovement, error)
	// ReplaceOpeningBalance compares ExpectedVersion, updates the current
	// baseline, and appends one revision header plus normalized amount rows in a
	// single transaction. A race returns VersionConflictError with safe current
	// Account state.
	ReplaceOpeningBalance(context.Context, uuid.UUID, uuid.UUID, ReplaceOpeningBalanceParams) (FinancialAccount, error)
	ListOpeningBalanceRevisions(context.Context, uuid.UUID, uuid.UUID) ([]OpeningBalanceRevision, error)
	GetTransactionForTreatment(context.Context, uuid.UUID, uuid.UUID) (TransactionForTreatment, error)
	// PutUserTreatment compares ExpectedUpdatedAt and returns
	// ErrTreatmentVersionConflict on a stale write.
	PutUserTreatment(context.Context, uuid.UUID, uuid.UUID, PutUserTreatmentParams) (CalculationTreatment, error)
	LockSystemPayoffExclusions(context.Context, uuid.UUID, []uuid.UUID, string) error
}

type VersionConflictError struct {
	Current FinancialAccount
}

func (e *VersionConflictError) Error() string { return "opening balance version conflict" }

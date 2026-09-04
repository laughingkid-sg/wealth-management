// Package bulkimport owns the authenticated Bulk Import application contract.
// Database, Storage and provider adapters are injected so network calls never
// occur while an application transaction is open.
package bulkimport

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

const (
	MaxFilesPerBatch = 20
	MaxBatchBytes    = 50 * 1024 * 1024
	MaxFileBytes     = 5 * 1024 * 1024
	MaxPages         = 50
	MaxPromptRunes   = 8000
)

var (
	ErrNotFound          = errors.New("bulk import resource not found")
	ErrConflict          = errors.New("bulk import state conflict")
	ErrDuplicateFile     = errors.New("bulk import duplicate file checksum")
	ErrInvalid           = errors.New("invalid bulk import request")
	ErrVersionConflict   = errors.New("bulk import version conflict")
	ErrReadOnlyCandidate = errors.New("credit card bill candidates are read-only in Bulk Import")
)

type DocumentType string

const (
	DocumentPhysicalReceipt         DocumentType = "physical_receipt"
	DocumentInvoice                 DocumentType = "invoice"
	DocumentEWalletHistory          DocumentType = "e_wallet_history"
	DocumentBankStatement           DocumentType = "bank_statement"
	DocumentCreditCardBill          DocumentType = "credit_card_bill"
	DocumentTransactionConfirmation DocumentType = "transaction_confirmation"
	DocumentOther                   DocumentType = "other"
)

func (d DocumentType) Valid() bool {
	switch d {
	case DocumentPhysicalReceipt, DocumentInvoice, DocumentEWalletHistory,
		DocumentBankStatement, DocumentCreditCardBill, DocumentTransactionConfirmation, DocumentOther:
		return true
	default:
		return false
	}
}

type AccountSelection struct {
	AccountID       uuid.UUID `json:"account_id"`
	AccountRef      string    `json:"account_ref,omitempty"`
	Name            string    `json:"name,omitempty"`
	InstitutionName string    `json:"institution_name,omitempty"`
	AccountType     string    `json:"account_type,omitempty"`
	SortOrder       int       `json:"sort_order"`
}

type Template struct {
	ID            uuid.UUID          `json:"id"`
	UserID        uuid.UUID          `json:"-"`
	Title         string             `json:"title"`
	DocumentType  DocumentType       `json:"document_type"`
	ParsingPrompt string             `json:"parsing_prompt"`
	Version       int                `json:"version"`
	ArchivedAt    *time.Time         `json:"archived_at"`
	Accounts      []AccountSelection `json:"accounts"`
	CreatedAt     time.Time          `json:"created_at"`
	UpdatedAt     time.Time          `json:"updated_at"`
}

type TemplateInput struct {
	Title           string
	DocumentType    DocumentType
	ParsingPrompt   string
	AccountIDs      []uuid.UUID
	ExpectedVersion *int
}

type BatchStatus string

const (
	BatchDraft               BatchStatus = "draft"
	BatchQueued              BatchStatus = "queued"
	BatchRunning             BatchStatus = "running"
	BatchCancelling          BatchStatus = "cancelling"
	BatchCompleted           BatchStatus = "completed"
	BatchCompletedWithErrors BatchStatus = "completed_with_errors"
	BatchFailed              BatchStatus = "failed"
	BatchCancelled           BatchStatus = "cancelled"
)

type BatchCounters struct {
	Files            int `json:"files"`
	Documents        int `json:"documents"`
	Pages            int `json:"pages"`
	ParsedCandidates int `json:"parsed_candidates"`
	Created          int `json:"created"`
	Attached         int `json:"attached"`
	Review           int `json:"review"`
	Failed           int `json:"failed"`
	Duplicates       int `json:"duplicates"`
}

type Batch struct {
	ID                    uuid.UUID          `json:"id"`
	UserID                uuid.UUID          `json:"-"`
	TemplateID            *uuid.UUID         `json:"template_id"`
	TemplateVersion       int                `json:"template_version"`
	TitleSnapshot         string             `json:"title_snapshot"`
	DocumentTypeSnapshot  DocumentType       `json:"document_type_snapshot"`
	ParsingPromptSnapshot string             `json:"parsing_prompt_snapshot"`
	Status                BatchStatus        `json:"status"`
	Accounts              []AccountSelection `json:"accounts"`
	Documents             []Document         `json:"documents,omitempty"`
	Counters              BatchCounters      `json:"counters"`
	CancelRequestedAt     *time.Time         `json:"cancel_requested_at"`
	ErrorSummary          *string            `json:"error_summary"`
	StartedAt             *time.Time         `json:"started_at"`
	CompletedAt           *time.Time         `json:"completed_at"`
	CreatedAt             time.Time          `json:"created_at"`
	UpdatedAt             time.Time          `json:"updated_at"`
}

type BatchCursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

type BatchPage struct {
	Items      []Batch      `json:"items"`
	NextCursor *BatchCursor `json:"-"`
}

type FileStatus string

const (
	FileReserved       FileStatus = "reserved"
	FileUploaded       FileStatus = "uploaded"
	FileVerified       FileStatus = "verified"
	FileFailed         FileStatus = "failed"
	FileCleanupPending FileStatus = "cleanup_pending"
)

type File struct {
	ID                   uuid.UUID  `json:"id"`
	DocumentID           uuid.UUID  `json:"document_id"`
	SortOrder            int        `json:"sort_order"`
	DisplayFilename      string     `json:"display_filename"`
	DeclaredMIME         string     `json:"declared_mime_type"`
	DeclaredBytes        int64      `json:"declared_byte_size"`
	DeclaredSHA256       string     `json:"declared_sha256"`
	Status               FileStatus `json:"status"`
	ReservationExpiresAt time.Time  `json:"reservation_expires_at"`
	FinalizedAt          *time.Time `json:"finalized_at"`
}

type DocumentStatus string

const (
	DocumentDraft               DocumentStatus = "draft"
	DocumentQueued              DocumentStatus = "queued"
	DocumentPreparing           DocumentStatus = "preparing"
	DocumentParsing             DocumentStatus = "parsing"
	DocumentAggregating         DocumentStatus = "aggregating"
	DocumentReconciling         DocumentStatus = "reconciling"
	DocumentCompleted           DocumentStatus = "completed"
	DocumentCompletedWithErrors DocumentStatus = "completed_with_errors"
	DocumentFailed              DocumentStatus = "failed"
	DocumentCancelled           DocumentStatus = "cancelled"
)

type Document struct {
	ID                uuid.UUID          `json:"id"`
	BatchID           uuid.UUID          `json:"batch_id"`
	SourceScopeID     uuid.UUID          `json:"-"`
	DataSourceID      *uuid.UUID         `json:"data_source_id"`
	SortOrder         int                `json:"sort_order"`
	DisplayLabel      string             `json:"display_label"`
	Status            DocumentStatus     `json:"status"`
	AttemptGeneration int                `json:"attempt_generation"`
	PageCount         int                `json:"page_count"`
	CandidateCount    int                `json:"candidate_count"`
	CreatedCount      int                `json:"created_count"`
	AttachedCount     int                `json:"attached_count"`
	ReviewCount       int                `json:"review_count"`
	FailedCount       int                `json:"failed_count"`
	DuplicateCount    int                `json:"duplicate_count"`
	Files             []File             `json:"files"`
	DocumentSummary   json.RawMessage    `json:"document_summary,omitempty"`
	SpecializedResult *SpecializedResult `json:"specialized_result,omitempty"`
}

type SpecializedResult struct {
	Kind       string    `json:"kind"`
	ResourceID uuid.UUID `json:"resource_id"`
	Path       string    `json:"path"`
}

type ReservationInput struct {
	DisplayFilename      string
	MIMEType             string
	ByteSize             int64
	SHA256               string
	IntentionalDuplicate bool
}

type Reservation struct {
	File      File              `json:"file"`
	UploadURL string            `json:"upload_url"`
	Method    string            `json:"method"`
	Headers   map[string]string `json:"headers"`
}

type ReservedFile struct {
	File          File
	SourceScopeID uuid.UUID
	ObjectPath    string
}

type ObjectMetadata struct {
	ByteSize    int64
	ContentType string
	ETag        string
}

type EvidenceObject struct {
	ID            uuid.UUID
	SourceScopeID uuid.UUID
	Filename      string
	MIMEType      string
	ByteSize      int64
	SHA256        string
	ObjectPath    string
}

type EvidenceFile struct {
	ID        uuid.UUID `json:"id"`
	Filename  string    `json:"filename"`
	MIMEType  string    `json:"mime_type"`
	ByteSize  int64     `json:"byte_size"`
	SHA256    string    `json:"sha256"`
	SignedURL string    `json:"signed_url"`
}

type DocumentLayout struct {
	DocumentID uuid.UUID
	Label      string
	FileIDs    []uuid.UUID
}

type CandidateStatus string

const (
	CandidatePending    CandidateStatus = "pending_reconciliation"
	CandidateCreated    CandidateStatus = "created"
	CandidateAttached   CandidateStatus = "attached"
	CandidateReview     CandidateStatus = "review_required"
	CandidateDuplicate  CandidateStatus = "duplicate"
	CandidateFailed     CandidateStatus = "failed"
	CandidateCancelled  CandidateStatus = "cancelled"
	CandidateSuperseded CandidateStatus = "superseded"
)

type Candidate struct {
	ID                uuid.UUID       `json:"id"`
	BatchID           uuid.UUID       `json:"batch_id"`
	DocumentID        uuid.UUID       `json:"document_id"`
	AttemptGeneration int             `json:"attempt_generation"`
	Ordinal           int             `json:"ordinal"`
	Fingerprint       string          `json:"fingerprint"`
	ParsedCandidate   json.RawMessage `json:"parsed_candidate"`
	AccountID         *uuid.UUID      `json:"account_id"`
	Status            CandidateStatus `json:"status"`
	TransactionID     *uuid.UUID      `json:"transaction_id"`
	DuplicateOfID     *uuid.UUID      `json:"duplicate_of_candidate_id"`
	Reason            *string         `json:"reconciliation_reason"`
}

type CandidateAction string

const (
	CandidateSetAccount       CandidateAction = "set_account"
	CandidateAttach           CandidateAction = "attach"
	CandidateCreate           CandidateAction = "create"
	CandidateInternalTransfer CandidateAction = "internal_transfer"
)

type CandidateResolution struct {
	Action             CandidateAction
	AccountID          *uuid.UUID
	TransactionID      *uuid.UUID
	DebitAccountID     *uuid.UUID
	CreditAccountID    *uuid.UUID
	CategoryID         *uuid.UUID
	ExpectedGeneration int
}

type PromptPreview struct {
	SystemPrompt string          `json:"system_prompt"`
	Request      json.RawMessage `json:"request"`
}

type DebugAttempt struct {
	ID                    uuid.UUID       `json:"id"`
	ChunkID               uuid.UUID       `json:"chunk_id"`
	ChunkIndex            int             `json:"chunk_index"`
	Generation            int             `json:"attempt_generation"`
	ModelName             *string         `json:"model_name"`
	Status                string          `json:"status"`
	Metadata              json.RawMessage `json:"request_metadata"`
	ParsedCandidate       json.RawMessage `json:"parsed_candidate"`
	AssembledSystemPrompt *string         `json:"assembled_system_prompt"`
	NormalizedInput       *string         `json:"normalized_input"`
	ProviderRequest       *string         `json:"provider_request"`
	ProviderResponse      *string         `json:"provider_response"`
	ModelOutput           *string         `json:"model_output"`
	PromptComponents      json.RawMessage `json:"prompt_components"`
	ErrorSummary          *string         `json:"error_summary"`
	StartedAt             *time.Time      `json:"started_at"`
	CompletedAt           *time.Time      `json:"completed_at"`
	CreatedAt             time.Time       `json:"created_at"`
	TruncatedFields       []string        `json:"truncated_fields"`
}

type DebugField struct {
	SourceID  uuid.UUID `json:"source_id"`
	AttemptID uuid.UUID `json:"attempt_id"`
	Field     string    `json:"field"`
	Value     *string   `json:"value"`
	MaxBytes  int       `json:"max_bytes"`
}

type PostProcessInput struct {
	UserID            uuid.UUID
	BatchID           uuid.UUID
	DocumentID        uuid.UUID
	AttemptGeneration int
	DocumentType      DocumentType
	DocumentSummary   json.RawMessage
	CandidateIDs      []uuid.UUID
}

// Store methods each own at most one short database transaction. Implementations
// must return before Service performs Storage or provider I/O.
type Store interface {
	ListTemplates(context.Context, uuid.UUID, bool) ([]Template, error)
	CreateTemplate(context.Context, uuid.UUID, TemplateInput) (Template, error)
	UpdateTemplate(context.Context, uuid.UUID, uuid.UUID, TemplateInput) (Template, error)
	SetTemplateArchived(context.Context, uuid.UUID, uuid.UUID, bool) (Template, error)
	CreateBatch(context.Context, uuid.UUID, uuid.UUID, []uuid.UUID) (Batch, error)
	ListBatches(context.Context, uuid.UUID, *BatchCursor, int) (BatchPage, error)
	GetBatch(context.Context, uuid.UUID, uuid.UUID) (Batch, error)
	ReserveFile(context.Context, uuid.UUID, uuid.UUID, ReservationInput, time.Time) (ReservedFile, error)
	MarkReservationFailed(context.Context, uuid.UUID, uuid.UUID, string) error
	FinalizeFile(context.Context, uuid.UUID, uuid.UUID, ObjectMetadata) (File, error)
	ReplaceDocumentLayout(context.Context, uuid.UUID, uuid.UUID, []DocumentLayout) (Batch, error)
	SubmitBatch(context.Context, uuid.UUID, uuid.UUID) (Batch, error)
	CancelBatch(context.Context, uuid.UUID, uuid.UUID) (Batch, error)
	RetryDocument(context.Context, uuid.UUID, uuid.UUID) (Document, error)
	DeleteDocument(context.Context, uuid.UUID, uuid.UUID) error
	DeleteBatch(context.Context, uuid.UUID, uuid.UUID) error
	ListCandidates(context.Context, uuid.UUID, uuid.UUID) ([]Candidate, error)
	ResolveCandidate(context.Context, uuid.UUID, uuid.UUID, CandidateResolution) (Candidate, error)
	LoadPromptPreview(context.Context, uuid.UUID, uuid.UUID, []uuid.UUID) (Template, []AccountSelection, error)
	ListDebugAttempts(context.Context, uuid.UUID, uuid.UUID) ([]DebugAttempt, error)
	GetDebugAttemptField(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, string) (DebugField, error)
	LoadDocumentEvidence(context.Context, uuid.UUID, uuid.UUID) ([]EvidenceObject, error)
}

type Storage interface {
	CreateSignedUpload(context.Context, uuid.UUID, uuid.UUID, string, time.Duration) (string, error)
	Stat(context.Context, uuid.UUID, uuid.UUID, string) (ObjectMetadata, error)
	SignURL(context.Context, uuid.UUID, uuid.UUID, string, int) (string, error)
}

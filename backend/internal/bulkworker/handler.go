// Package bulkworker orchestrates Bulk Import jobs around short repository
// operations. Storage, rendering and provider calls always happen between
// repository calls, never inside a database transaction.
package bulkworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/zhengteck/wealth-builder/backend/internal/bulkimport"
	"github.com/zhengteck/wealth-builder/backend/internal/bulkparse"
	"github.com/zhengteck/wealth-builder/backend/internal/bulkprompt"
	"github.com/zhengteck/wealth-builder/backend/internal/jobs"
	"github.com/zhengteck/wealth-builder/backend/internal/providers"
)

var ErrWorkAlreadyApplied = errors.New("bulk work already applied")
var ErrOriginalVerification = errors.New("bulk original verification failed")

type Kind string

const (
	KindPrepare     Kind = "bulk_document_prepare"
	KindChunkParse  Kind = "bulk_document_chunk_parse"
	KindAggregate   Kind = "bulk_document_aggregate"
	KindReconcile   Kind = "bulk_candidate_reconciliation"
	KindPostProcess Kind = "bulk_document_post_process"
)

type Work struct {
	Kind        Kind
	UserID      uuid.UUID
	BatchID     uuid.UUID
	DocumentID  uuid.UUID
	ChunkID     uuid.UUID
	CandidateID uuid.UUID
	Generation  int
}

type OriginalFile struct {
	FileID        uuid.UUID
	ObjectPath    string
	Filename      string
	MIMEType      string
	ByteSize      int64
	SHA256        string
	SourceScopeID uuid.UUID
}

type PreparedPage struct {
	ManifestPath  string
	ObjectPath    string
	SourceScopeID uuid.UUID
	Filename      string
	MIMEType      string
	Content       []byte
}

type PreparedDocument struct {
	Pages []PreparedPage
}

type ChunkInput struct {
	DocumentType   bulkimport.DocumentType
	ChunkIndex     int
	PageManifest   []string
	TemplatePrompt string
	Accounts       []bulkprompt.AccountDescriptor
	Pages          []PreparedPage
}

type ChunkResult struct {
	Decoded          bulkparse.Response
	RawModelOutput   json.RawMessage
	ProviderRequest  json.RawMessage
	ProviderResponse json.RawMessage
	ModelName        string
	Prompt           bulkprompt.Assembly
}

// ChunkFailure preserves the same secret-free provider audit envelope as a
// valid attempt. Provider adapters return only JSON bodies; authorization
// headers and credentials never enter this boundary.
type ChunkFailure struct {
	Class            string
	Detail           string
	Terminal         bool
	ModelName        string
	ProviderRequest  json.RawMessage
	ProviderResponse json.RawMessage
	ModelOutput      json.RawMessage
	Prompt           bulkprompt.Assembly
}

type Repository interface {
	IsCancelled(context.Context, Work) (bool, error)
	LoadOriginals(context.Context, Work) ([]OriginalFile, error)
	RecordPrepareFailure(context.Context, Work, string) error
	RecordPrepared(context.Context, Work, PreparedDocument) error
	LoadChunk(context.Context, Work) (ChunkInput, error)
	RecordChunkResult(context.Context, Work, ChunkResult) error
	RecordChunkFailure(context.Context, Work, ChunkFailure) error
	AggregateDocument(context.Context, Work) error
	ReconcileCandidate(context.Context, Work) error
	LoadPostProcessInput(context.Context, Work) (bulkimport.PostProcessInput, error)
	RecordGenericPostProcess(context.Context, bulkimport.PostProcessInput) error
}

type Storage interface {
	Download(context.Context, uuid.UUID, uuid.UUID, string) ([]byte, error)
	Upload(context.Context, uuid.UUID, uuid.UUID, string, []byte) (string, error)
}

type Renderer interface {
	Prepare(context.Context, []OriginalFile, Storage, uuid.UUID) (PreparedDocument, error)
}

type Parser interface {
	ParseTransactionEvidence(context.Context, string, string, []providers.AttachmentInput) (providers.ParsedCandidate, error)
}

type CreditCardPostProcessor interface {
	ProcessCreditCardBill(context.Context, bulkimport.PostProcessInput) error
}

type Handler struct {
	Repository  Repository
	Renderer    Renderer
	Parser      Parser
	CreditCard  CreditCardPostProcessor
	BlobStorage Storage
}

// JobHandler adapts the shared durable queue without trusting JSON payloads for
// owner or resource identity. Bulk scope is carried by typed, FK-checked job
// columns and copied into Work here.
type JobHandler struct{ Handler Handler }

func (h JobHandler) Handle(ctx context.Context, job jobs.Job) error {
	if job.BatchID == nil || job.DocumentID == nil || job.Generation == nil {
		return errors.New("bulk job has incomplete typed scope")
	}
	work := Work{
		Kind: Kind(job.Kind), UserID: job.UserID, BatchID: *job.BatchID,
		DocumentID: *job.DocumentID, Generation: *job.Generation,
	}
	if job.ChunkID != nil {
		work.ChunkID = *job.ChunkID
	}
	if job.CandidateID != nil {
		work.CandidateID = *job.CandidateID
	}
	return h.Handler.Handle(ctx, work)
}

func (h Handler) Handle(ctx context.Context, work Work) error {
	if h.Repository == nil || work.UserID == uuid.Nil || work.DocumentID == uuid.Nil || work.Generation < 1 {
		return errors.New("bulk worker is not configured")
	}
	cancelled, err := h.Repository.IsCancelled(ctx, work)
	if err != nil {
		return fmt.Errorf("check bulk cancellation: %w", err)
	}
	if cancelled {
		return nil
	}
	switch work.Kind {
	case KindPrepare:
		return h.prepare(ctx, work)
	case KindChunkParse:
		return h.parseChunk(ctx, work)
	case KindAggregate:
		return h.Repository.AggregateDocument(ctx, work)
	case KindReconcile:
		if work.CandidateID == uuid.Nil {
			return errors.New("candidate job has no candidate ID")
		}
		return h.Repository.ReconcileCandidate(ctx, work)
	case KindPostProcess:
		return h.postProcess(ctx, work)
	default:
		return fmt.Errorf("unsupported bulk job kind %q", work.Kind)
	}
}

func (h Handler) prepare(ctx context.Context, work Work) error {
	if h.Renderer == nil || h.BlobStorage == nil {
		return errors.New("bulk renderer is not configured")
	}
	files, err := h.Repository.LoadOriginals(ctx, work)
	if err != nil {
		if errors.Is(err, ErrWorkAlreadyApplied) {
			return nil
		}
		return fmt.Errorf("load bulk originals: %w", err)
	}
	prepared, err := h.Renderer.Prepare(ctx, files, h.BlobStorage, work.UserID)
	if err != nil {
		if errors.Is(err, ErrOriginalVerification) {
			if recordErr := h.Repository.RecordPrepareFailure(ctx, work, "original_verification_failed"); recordErr != nil {
				return fmt.Errorf("record bulk original verification failure: %w", recordErr)
			}
			return nil
		}
		return fmt.Errorf("prepare bulk document: %w", err)
	}
	if len(prepared.Pages) == 0 || len(prepared.Pages) > bulkimport.MaxPages {
		return errors.New("prepared document has an invalid page count")
	}
	for index := range prepared.Pages {
		page := &prepared.Pages[index]
		if page.SourceScopeID == uuid.Nil || page.MIMEType == "" || len(page.Content) == 0 {
			return errors.New("prepared page is incomplete")
		}
		page.ObjectPath, err = h.BlobStorage.Upload(ctx, work.UserID, page.SourceScopeID, page.MIMEType, page.Content)
		if err != nil {
			return fmt.Errorf("store prepared bulk page: %w", err)
		}
		page.Content = nil
	}
	cancelled, err := h.Repository.IsCancelled(ctx, work)
	if err != nil {
		return err
	}
	if cancelled {
		return nil
	}
	return h.Repository.RecordPrepared(ctx, work, prepared)
}

func (h Handler) parseChunk(ctx context.Context, work Work) error {
	if h.Parser == nil || work.ChunkID == uuid.Nil {
		return errors.New("bulk parser job is not configured")
	}
	input, err := h.Repository.LoadChunk(ctx, work)
	if err != nil {
		return fmt.Errorf("load bulk chunk: %w", err)
	}
	for index := range input.Pages {
		page := &input.Pages[index]
		if len(page.Content) != 0 {
			continue
		}
		if h.BlobStorage == nil {
			return errors.New("bulk page storage is not configured")
		}
		page.Content, err = h.BlobStorage.Download(ctx, work.UserID, page.SourceScopeID, page.ObjectPath)
		if err != nil {
			return fmt.Errorf("load prepared bulk page: %w", err)
		}
	}
	assembly, err := bulkprompt.Assemble(bulkprompt.Input{
		DocumentType: string(input.DocumentType), ChunkIndex: input.ChunkIndex,
		PageManifest: input.PageManifest, TemplatePrompt: input.TemplatePrompt, Accounts: input.Accounts,
	})
	if err != nil {
		if recordErr := h.Repository.RecordChunkFailure(ctx, work, ChunkFailure{Class: "prompt_contract_invalid", Detail: err.Error(), Terminal: true}); recordErr != nil {
			return fmt.Errorf("record invalid bulk prompt: %w", recordErr)
		}
		return nil
	}
	attachments := make([]providers.AttachmentInput, 0, len(input.Pages))
	for _, page := range input.Pages {
		attachments = append(attachments, providers.AttachmentInput{Filename: page.Filename, MIMEType: page.MIMEType, Content: page.Content})
	}
	providerResult, err := h.Parser.ParseTransactionEvidence(ctx, assembly.SystemPrompt, string(assembly.UserMessage), attachments)
	if err != nil {
		if recordErr := h.Repository.RecordChunkFailure(ctx, work, chunkFailureFromProvider("provider_transient", err, false, assembly, providerResult)); recordErr != nil {
			return fmt.Errorf("record transient bulk provider failure: %w", recordErr)
		}
		return fmt.Errorf("parse bulk chunk: %w", err)
	}
	parserType := bulkparse.GenericDocument
	if input.DocumentType == bulkimport.DocumentCreditCardBill {
		parserType = bulkparse.CreditCardBill
	}
	refs := make([]string, 0, len(input.Accounts))
	for _, account := range input.Accounts {
		refs = append(refs, account.AccountRef)
	}
	decoded, err := bulkparse.DecodeWithContext(providerResult.JSON, bulkparse.DecodeContext{
		DocumentType:       parserType,
		AllowedAccountRefs: refs,
		PageManifest:       input.PageManifest,
	})
	if err != nil {
		if recordErr := h.Repository.RecordChunkFailure(ctx, work, chunkFailureFromProvider("model_output_invalid", err, true, assembly, providerResult)); recordErr != nil {
			return fmt.Errorf("record invalid bulk output: %w", recordErr)
		}
		return nil
	}
	cancelled, err := h.Repository.IsCancelled(ctx, work)
	if err != nil {
		return err
	}
	if cancelled {
		return nil
	}
	return h.Repository.RecordChunkResult(ctx, work, ChunkResult{
		Decoded: decoded, RawModelOutput: append(json.RawMessage(nil), providerResult.JSON...),
		ProviderRequest:  append(json.RawMessage(nil), providerResult.ProviderRequest...),
		ProviderResponse: append(json.RawMessage(nil), providerResult.ProviderResponse...), ModelName: providerResult.Model, Prompt: assembly,
	})
}

func chunkFailureFromProvider(class string, cause error, terminal bool, prompt bulkprompt.Assembly, result providers.ParsedCandidate) ChunkFailure {
	detail := ""
	if cause != nil {
		detail = cause.Error()
	}
	return ChunkFailure{
		Class: class, Detail: detail, Terminal: terminal, ModelName: result.Model, Prompt: prompt,
		ProviderRequest:  append(json.RawMessage(nil), result.ProviderRequest...),
		ProviderResponse: append(json.RawMessage(nil), result.ProviderResponse...),
		ModelOutput:      append(json.RawMessage(nil), result.JSON...),
	}
}

func (h Handler) postProcess(ctx context.Context, work Work) error {
	input, err := h.Repository.LoadPostProcessInput(ctx, work)
	if err != nil {
		return err
	}
	if input.DocumentType == bulkimport.DocumentCreditCardBill {
		if h.CreditCard == nil {
			return errors.New("credit card post-processor is not configured")
		}
		if err := h.CreditCard.ProcessCreditCardBill(ctx, input); err != nil {
			return err
		}
	}
	return h.Repository.RecordGenericPostProcess(ctx, input)
}

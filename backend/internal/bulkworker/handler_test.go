package bulkworker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/zhengteck/wealth-builder/backend/internal/bulkimport"
	"github.com/zhengteck/wealth-builder/backend/internal/bulkprompt"
	"github.com/zhengteck/wealth-builder/backend/internal/jobs"
	"github.com/zhengteck/wealth-builder/backend/internal/providers"
)

type repositoryStub struct {
	cancelled      bool
	chunk          ChunkInput
	result         *ChunkResult
	failure        *ChunkFailure
	post           bulkimport.PostProcessInput
	generic        bool
	reconciled     bool
	prepareFailure string
}

func (s *repositoryStub) IsCancelled(context.Context, Work) (bool, error) { return s.cancelled, nil }
func (s *repositoryStub) LoadOriginals(context.Context, Work) ([]OriginalFile, error) {
	return []OriginalFile{{FileID: uuid.New()}}, nil
}
func (s *repositoryStub) RecordPrepareFailure(_ context.Context, _ Work, class string) error {
	s.prepareFailure = class
	return nil
}
func (s *repositoryStub) RecordPrepared(context.Context, Work, PreparedDocument) error { return nil }
func (s *repositoryStub) LoadChunk(context.Context, Work) (ChunkInput, error)          { return s.chunk, nil }
func (s *repositoryStub) RecordChunkResult(_ context.Context, _ Work, result ChunkResult) error {
	s.result = &result
	return nil
}
func (s *repositoryStub) RecordChunkFailure(_ context.Context, _ Work, failure ChunkFailure) error {
	s.failure = &failure
	return nil
}
func (s *repositoryStub) AggregateDocument(context.Context, Work) error { return nil }
func (s *repositoryStub) ReconcileCandidate(context.Context, Work) error {
	s.reconciled = true
	return nil
}
func (s *repositoryStub) LoadPostProcessInput(context.Context, Work) (bulkimport.PostProcessInput, error) {
	return s.post, nil
}
func (s *repositoryStub) RecordGenericPostProcess(context.Context, bulkimport.PostProcessInput) error {
	s.generic = true
	return nil
}

type parserStub struct {
	result providers.ParsedCandidate
	err    error
	calls  int
}

func (s *parserStub) ParseTransactionEvidence(context.Context, string, string, []providers.AttachmentInput) (providers.ParsedCandidate, error) {
	s.calls++
	return s.result, s.err
}

type creditCardStub struct {
	input bulkimport.PostProcessInput
	calls int
}

func (s *creditCardStub) ProcessCreditCardBill(_ context.Context, input bulkimport.PostProcessInput) error {
	s.input, s.calls = input, s.calls+1
	return nil
}

func TestChunkInvalidModelOutputIsPersistedTerminalWithoutBlindRetry(t *testing.T) {
	repo := &repositoryStub{chunk: validChunkInput()}
	parser := &parserStub{result: providers.ParsedCandidate{JSON: []byte(`{"schema_version":1,"document_summary":null,"transactions":[{"unknown":true}]}`), Model: "test-model", ProviderRequest: []byte(`{"request":true}`), ProviderResponse: []byte(`{"response":true}`)}}
	handler := Handler{Repository: repo, Parser: parser}
	err := handler.Handle(context.Background(), Work{Kind: KindChunkParse, UserID: uuid.New(), DocumentID: uuid.New(), ChunkID: uuid.New(), Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	if repo.failure == nil || repo.failure.Class != "model_output_invalid" || !repo.failure.Terminal || repo.result != nil || parser.calls != 1 {
		t.Fatalf("failure=%#v result=%v calls=%d", repo.failure, repo.result, parser.calls)
	}
	if !strings.Contains(repo.failure.Detail, "unknown field") {
		t.Fatalf("decoder detail was not preserved: %#v", repo.failure)
	}
	if repo.failure.ModelName != "test-model" || string(repo.failure.ProviderRequest) != `{"request":true}` || string(repo.failure.ProviderResponse) != `{"response":true}` || string(repo.failure.ModelOutput) != string(parser.result.JSON) || repo.failure.Prompt.SystemPrompt == "" {
		t.Fatalf("invalid-output audit envelope was not preserved: %#v", repo.failure)
	}
}

func TestChunkProviderFailureIsRetryableAndNoResultIsCommitted(t *testing.T) {
	repo := &repositoryStub{chunk: validChunkInput()}
	parser := &parserStub{result: providers.ParsedCandidate{Model: "test-model", ProviderRequest: []byte(`{"request":true}`), ProviderResponse: []byte(`{"error":"temporary"}`)}, err: errors.New("temporary")}
	err := (Handler{Repository: repo, Parser: parser}).Handle(context.Background(), Work{Kind: KindChunkParse, UserID: uuid.New(), DocumentID: uuid.New(), ChunkID: uuid.New(), Generation: 1})
	if err == nil || repo.failure == nil || repo.failure.Class != "provider_transient" || repo.failure.Terminal || repo.result != nil {
		t.Fatalf("err=%v failure=%#v", err, repo.failure)
	}
	if repo.failure.ModelName != "test-model" || len(repo.failure.ProviderRequest) == 0 || len(repo.failure.ProviderResponse) == 0 || repo.failure.Prompt.SystemPrompt == "" {
		t.Fatalf("transient-failure audit envelope was not preserved: %#v", repo.failure)
	}
}

type verificationRenderer struct{}

func (verificationRenderer) Prepare(context.Context, []OriginalFile, Storage, uuid.UUID) (PreparedDocument, error) {
	return PreparedDocument{}, ErrOriginalVerification
}

type unusedStorage struct{}

func (unusedStorage) Download(context.Context, uuid.UUID, uuid.UUID, string) ([]byte, error) {
	return nil, nil
}
func (unusedStorage) Upload(context.Context, uuid.UUID, uuid.UUID, string, []byte) (string, error) {
	return "", nil
}

func TestPrepareVerificationFailureIsPersistedWithoutBlindRetry(t *testing.T) {
	repo := &repositoryStub{}
	err := (Handler{Repository: repo, Renderer: verificationRenderer{}, BlobStorage: unusedStorage{}}).Handle(context.Background(), Work{Kind: KindPrepare, UserID: uuid.New(), BatchID: uuid.New(), DocumentID: uuid.New(), Generation: 1})
	if err != nil || repo.prepareFailure != "original_verification_failed" {
		t.Fatalf("err=%v failure=%q", err, repo.prepareFailure)
	}
}

func TestCancellationPreventsProviderCall(t *testing.T) {
	repo := &repositoryStub{cancelled: true, chunk: validChunkInput()}
	parser := &parserStub{}
	err := (Handler{Repository: repo, Parser: parser}).Handle(context.Background(), Work{Kind: KindChunkParse, UserID: uuid.New(), DocumentID: uuid.New(), ChunkID: uuid.New(), Generation: 1})
	if err != nil || parser.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, parser.calls)
	}
}

func TestJobHandlerRequiresTypedBulkScope(t *testing.T) {
	err := (JobHandler{}).Handle(context.Background(), jobs.Job{UserID: uuid.New(), Kind: jobs.KindBulkDocumentPrepare, Payload: []byte(`{"document_id":"untrusted"}`)})
	if err == nil {
		t.Fatal("expected typed-scope rejection")
	}
}

func TestCreditCardPostProcessEmitsIDsOnlyToDedicatedBoundary(t *testing.T) {
	input := bulkimport.PostProcessInput{UserID: uuid.New(), BatchID: uuid.New(), DocumentID: uuid.New(), AttemptGeneration: 3, DocumentType: bulkimport.DocumentCreditCardBill, DocumentSummary: json.RawMessage(`{"safe":true}`), CandidateIDs: []uuid.UUID{uuid.New()}}
	repo, card := &repositoryStub{post: input}, &creditCardStub{}
	err := (Handler{Repository: repo, CreditCard: card}).Handle(context.Background(), Work{Kind: KindPostProcess, UserID: input.UserID, DocumentID: input.DocumentID, Generation: 3})
	if err != nil {
		t.Fatal(err)
	}
	if card.calls != 1 || card.input.DocumentID != input.DocumentID || !repo.generic {
		t.Fatalf("card=%#v generic=%t", card, repo.generic)
	}
}

func validChunkInput() ChunkInput {
	return ChunkInput{
		DocumentType: bulkimport.DocumentOther, ChunkIndex: 0, PageManifest: []string{"file[0].page[1]"},
		TemplatePrompt: "Extract every transaction.",
		Accounts:       []bulkprompt.AccountDescriptor{{AccountRef: "account_1", Name: "Current", Institution: "Bank", AccountType: "bank_account"}},
		Pages:          []PreparedPage{{Filename: "page.png", MIMEType: "image/png", Content: []byte("png")}},
	}
}

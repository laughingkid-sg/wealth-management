package transactionworker

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/zhengteck/wealth-builder/backend/internal/attachmentstorage"
	"github.com/zhengteck/wealth-builder/backend/internal/jobs"
	"github.com/zhengteck/wealth-builder/backend/internal/providers"
	"github.com/zhengteck/wealth-builder/backend/internal/reconciliation"
	"github.com/zhengteck/wealth-builder/backend/internal/transactionstore"
)

type repositoryStub struct {
	parseInput      transactionstore.SourceParseInput
	parseResult     *transactionstore.ParsedSourceResult
	invalid         bool
	failed          bool
	reconcileInput  transactionstore.ReconciliationInput
	reconcileResult *transactionstore.ReconciliationResult
}

func (s *repositoryStub) LoadSourceParseInput(context.Context, uuid.UUID, uuid.UUID) (transactionstore.SourceParseInput, error) {
	return s.parseInput, nil
}
func (s *repositoryStub) SaveParsedSource(_ context.Context, _ uuid.UUID, result transactionstore.ParsedSourceResult) error {
	s.parseResult = &result
	return nil
}
func (s *repositoryStub) RecordInvalidSourceParse(context.Context, uuid.UUID, uuid.UUID, string, json.RawMessage, error) error {
	s.invalid = true
	return nil
}
func (s *repositoryStub) RecordFailedSourceParse(context.Context, uuid.UUID, uuid.UUID, string, error) error {
	s.failed = true
	return nil
}
func (s *repositoryStub) LoadReconciliationInput(context.Context, uuid.UUID, uuid.UUID) (transactionstore.ReconciliationInput, error) {
	return s.reconcileInput, nil
}
func (s *repositoryStub) PersistReconciliation(_ context.Context, _ uuid.UUID, result transactionstore.ReconciliationResult) error {
	s.reconcileResult = &result
	return nil
}

type parserStub struct {
	result providers.ParsedCandidate
	err    error
}

type attachmentDownloadStub map[string][]byte

func (s attachmentDownloadStub) Download(_ context.Context, request attachmentstorage.ObjectRequest) ([]byte, error) {
	content, ok := s[request.ObjectPath]
	if !ok {
		return nil, errors.New("attachment not found")
	}
	return content, nil
}

func (s parserStub) ParseTransactionEvidence(context.Context, string, []providers.AttachmentInput) (providers.ParsedCandidate, error) {
	return s.result, s.err
}

func TestHandlerPersistsValidatedResultAndQueuesReconciliation(t *testing.T) {
	userID, sourceID := uuid.New(), uuid.New()
	raw := validResponseJSON(userID)
	repository := &repositoryStub{parseInput: transactionstore.SourceParseInput{ID: sourceID, NormalizedContent: "receipt"}}
	handler := Handler{Repository: repository, Parser: parserStub{result: providers.ParsedCandidate{JSON: raw, Model: "qwen3.8-flash"}}}
	payload, _ := json.Marshal(map[string]string{"data_source_id": sourceID.String()})
	if err := handler.Handle(context.Background(), jobs.Job{Kind: jobs.KindSourceParse, UserID: userID, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if repository.parseResult == nil || repository.invalid || repository.parseResult.ParsedResponse.Candidate.UserID != userID.String() {
		t.Fatalf("unexpected parser persistence %#v invalid=%v", repository.parseResult, repository.invalid)
	}
}

func TestHandlerRecordsInvalidModelResultWithoutRetry(t *testing.T) {
	userID, sourceID := uuid.New(), uuid.New()
	repository := &repositoryStub{parseInput: transactionstore.SourceParseInput{ID: sourceID, NormalizedContent: "receipt"}}
	handler := Handler{Repository: repository, Parser: parserStub{result: providers.ParsedCandidate{JSON: []byte(`{"unexpected":true}`), Model: "qwen3.8-flash"}}}
	payload, _ := json.Marshal(map[string]string{"data_source_id": sourceID.String()})
	if err := handler.Handle(context.Background(), jobs.Job{Kind: jobs.KindSourceParse, UserID: userID, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if !repository.invalid || repository.parseResult != nil {
		t.Fatalf("invalid=%v parseResult=%#v", repository.invalid, repository.parseResult)
	}
}

func TestHandlerRetriesProviderFailureAfterRecordingAttempt(t *testing.T) {
	userID, sourceID := uuid.New(), uuid.New()
	repository := &repositoryStub{parseInput: transactionstore.SourceParseInput{ID: sourceID, NormalizedContent: "receipt"}}
	handler := Handler{Repository: repository, Parser: parserStub{err: errors.New("temporary provider failure")}}
	payload, _ := json.Marshal(map[string]string{"data_source_id": sourceID.String()})
	if err := handler.Handle(context.Background(), jobs.Job{Kind: jobs.KindSourceParse, UserID: userID, Payload: payload}); err == nil || !repository.failed {
		t.Fatalf("error=%v failed=%v", err, repository.failed)
	}
}

func TestLoadParseAttachmentsBoundsVisualCountAndAggregateBytes(t *testing.T) {
	userID, sourceID := uuid.New(), uuid.New()
	megabyte := 1024 * 1024
	for _, testCase := range []struct {
		name      string
		sizes     []int
		wantCount int
		wantBytes int
	}{
		{name: "aggregate limit skips optional visual", sizes: []int{3 * megabyte, 3 * megabyte, 2 * megabyte}, wantCount: 2, wantBytes: 5 * megabyte},
		{name: "visual count limit", sizes: []int{megabyte, megabyte, megabyte, megabyte, megabyte, megabyte}, wantCount: 5, wantBytes: 5 * megabyte},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			attachments := make([]transactionstore.SourceAttachment, 0, len(testCase.sizes))
			downloads := make(attachmentDownloadStub, len(testCase.sizes))
			for index, size := range testCase.sizes {
				path := "attachment-" + strconv.Itoa(index)
				attachments = append(attachments, transactionstore.SourceAttachment{
					Filename: "invoice-" + strconv.Itoa(index) + ".png", MIMEType: "image/png",
					ObjectPath: path, StorageStatus: "stored", ParseEligible: true,
				})
				downloads[path] = make([]byte, size)
			}
			handler := Handler{Attachments: downloads}
			visuals, usage, err := handler.loadParseAttachments(context.Background(), userID, sourceID, attachments)
			if err != nil {
				t.Fatal(err)
			}
			total := 0
			for _, visual := range visuals {
				total += len(visual.Content)
			}
			if len(visuals) != testCase.wantCount || len(usage) != testCase.wantCount || total != testCase.wantBytes {
				t.Fatalf("visuals=%d usage=%d total=%d; want %d, %d, %d", len(visuals), len(usage), total, testCase.wantCount, testCase.wantCount, testCase.wantBytes)
			}
		})
	}
}

func TestHandlerPersistsReconciliationDecision(t *testing.T) {
	userID, sourceID, accountID := uuid.New(), uuid.New(), uuid.New()
	candidate := validCandidate(userID)
	repository := &repositoryStub{reconcileInput: transactionstore.ReconciliationInput{
		SourceID: sourceID, Candidate: candidate,
		Accounts: []reconciliation.AccountIdentity{{ID: accountID.String(), UserID: userID.String(), CardLastFour: "1234"}},
	}}
	handler := Handler{Repository: repository}
	payload, _ := json.Marshal(map[string]string{"data_source_id": sourceID.String()})
	if err := handler.Handle(context.Background(), jobs.Job{Kind: jobs.KindReconcile, UserID: userID, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if repository.reconcileResult == nil || repository.reconcileResult.Decision.Outcome != reconciliation.OutcomeCreate {
		t.Fatalf("unexpected reconciliation result %#v", repository.reconcileResult)
	}
}

func validResponseJSON(userID uuid.UUID) []byte {
	response := reconciliation.ParsedResponse{Candidate: validCandidate(userID), Evidence: []reconciliation.FieldEvidence{
		{Field: "transaction_kind", SourcePath: "text.kind", Confidence: .9}, {Field: "title", SourcePath: "text.title", Confidence: .9},
		{Field: "merchant_name", SourcePath: "text.merchant", Confidence: .9},
		{Field: "original_amount_minor", SourcePath: "text.amount", Confidence: .9}, {Field: "original_currency", SourcePath: "text.currency", Confidence: .9},
		{Field: "occurred_at", SourcePath: "text.time", Confidence: .9},
		{Field: "account_evidence", SourcePath: "text.card_last_four", Confidence: .9},
	}}
	raw, _ := json.Marshal(response)
	return raw
}

func validCandidate(userID uuid.UUID) reconciliation.Candidate {
	return reconciliation.Candidate{UserID: userID.String(), Kind: reconciliation.KindDebit, Title: "Coffee", MerchantName: "Cafe", OriginalAmountMinor: 500, OriginalCurrency: "SGD", OccurredAt: time.Now().UTC(), AccountEvidence: reconciliation.AccountEvidence{CardLastFour: "1234"}, Confidence: .9, AutoEligible: true}
}

package transactionworker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
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
		{Field: "original_amount", SourcePath: "text.amount", Confidence: .9}, {Field: "original_currency", SourcePath: "text.currency", Confidence: .9},
		{Field: "occurred_at", SourcePath: "text.time", Confidence: .9},
	}}
	raw, _ := json.Marshal(response)
	return raw
}

func validCandidate(userID uuid.UUID) reconciliation.Candidate {
	return reconciliation.Candidate{UserID: userID.String(), Kind: reconciliation.KindDebit, Title: "Coffee", MerchantName: "Cafe", OriginalAmountMinor: 500, OriginalCurrency: "SGD", OccurredAt: time.Now().UTC(), AccountEvidence: reconciliation.AccountEvidence{CardLastFour: "1234"}, Confidence: .9}
}

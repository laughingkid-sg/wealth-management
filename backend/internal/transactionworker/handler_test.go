package transactionworker

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/zhengteck/wealth-builder/backend/internal/attachmentstorage"
	"github.com/zhengteck/wealth-builder/backend/internal/jobs"
	"github.com/zhengteck/wealth-builder/backend/internal/parserrules"
	"github.com/zhengteck/wealth-builder/backend/internal/providers"
	"github.com/zhengteck/wealth-builder/backend/internal/reconciliation"
	"github.com/zhengteck/wealth-builder/backend/internal/transactionstore"
)

type repositoryStub struct {
	parseInput      transactionstore.SourceParseInput
	parseResult     *transactionstore.ParsedSourceResult
	invalid         bool
	failed          bool
	audit           transactionstore.SourceParseAudit
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
func (s *repositoryStub) RecordInvalidSourceParse(_ context.Context, _ uuid.UUID, audit transactionstore.SourceParseAudit, _ error) error {
	s.invalid = true
	s.audit = audit
	return nil
}
func (s *repositoryStub) RecordFailedSourceParse(_ context.Context, _ uuid.UUID, audit transactionstore.SourceParseAudit, _ error) error {
	s.failed = true
	s.audit = audit
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

type attachmentCleanupStub struct {
	requests []attachmentstorage.ObjectRequest
	err      error
}

func (s *attachmentCleanupStub) Delete(_ context.Context, requests []attachmentstorage.ObjectRequest) error {
	s.requests = append([]attachmentstorage.ObjectRequest(nil), requests...)
	return s.err
}

func (s parserStub) ParseTransactionEvidence(context.Context, string, string, []providers.AttachmentInput) (providers.ParsedCandidate, error) {
	return s.result, s.err
}

func TestSourceAttachmentCleanupValidatesAndDeletesOneOwnedBatch(t *testing.T) {
	userID, sourceID := uuid.New(), uuid.New()
	paths := []string{
		userID.String() + "/" + sourceID.String() + "/first.pdf",
		userID.String() + "/" + sourceID.String() + "/second.png",
	}
	payload, err := json.Marshal(jobs.SourceAttachmentCleanupPayload{SourceID: sourceID.String(), ObjectPaths: paths})
	if err != nil {
		t.Fatal(err)
	}
	storage := &attachmentCleanupStub{}
	handler := Handler{CleanupAttachments: storage}
	if err = handler.Handle(context.Background(), jobs.Job{
		Kind: jobs.KindSourceAttachmentCleanup, UserID: userID, Payload: payload,
	}); err != nil {
		t.Fatal(err)
	}
	if len(storage.requests) != 2 || storage.requests[0].ObjectPath != paths[0] || storage.requests[1].ObjectPath != paths[1] {
		t.Fatalf("cleanup requests = %#v", storage.requests)
	}
}

func TestSourceAttachmentCleanupFailureIsReturnedForBoundedJobRetry(t *testing.T) {
	userID, sourceID := uuid.New(), uuid.New()
	payload, _ := json.Marshal(jobs.SourceAttachmentCleanupPayload{
		SourceID:    sourceID.String(),
		ObjectPaths: []string{userID.String() + "/" + sourceID.String() + "/receipt.pdf"},
	})
	storage := &attachmentCleanupStub{err: errors.New("storage unavailable")}
	err := (Handler{CleanupAttachments: storage}).Handle(context.Background(), jobs.Job{
		Kind: jobs.KindSourceAttachmentCleanup, UserID: userID, Payload: payload,
	})
	if err == nil || !strings.Contains(err.Error(), "delete source attachments") || len(storage.requests) != 1 {
		t.Fatalf("cleanup error = %v, requests = %#v", err, storage.requests)
	}
}

func TestSourceAttachmentCleanupRejectsCrossSourcePathBeforeStorage(t *testing.T) {
	userID, sourceID := uuid.New(), uuid.New()
	payload, _ := json.Marshal(jobs.SourceAttachmentCleanupPayload{
		SourceID:    sourceID.String(),
		ObjectPaths: []string{userID.String() + "/" + uuid.NewString() + "/receipt.pdf"},
	})
	storage := &attachmentCleanupStub{}
	err := (Handler{CleanupAttachments: storage}).Handle(context.Background(), jobs.Job{
		Kind: jobs.KindSourceAttachmentCleanup, UserID: userID, Payload: payload,
	})
	if err == nil || len(storage.requests) != 0 {
		t.Fatalf("cleanup error = %v, requests = %#v", err, storage.requests)
	}
}

type parserCapture struct {
	result          providers.ParsedCandidate
	called          bool
	systemPrompt    string
	normalizedInput string
}

func (s *parserCapture) ParseTransactionEvidence(_ context.Context, systemPrompt, normalizedInput string, _ []providers.AttachmentInput) (providers.ParsedCandidate, error) {
	s.called, s.systemPrompt, s.normalizedInput = true, systemPrompt, normalizedInput
	return s.result, nil
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

func TestHandlerDemotesBareLLMCardSuffixBeforeAccountMatching(t *testing.T) {
	userID, sourceID := uuid.New(), uuid.New()
	raw := validResponseJSON(userID)
	repository := &repositoryStub{parseInput: transactionstore.SourceParseInput{
		ID: sourceID, NormalizedContent: "subject: Order 1234\ntext: Cafe charged SGD 5.00",
	}}
	handler := Handler{Repository: repository, Parser: parserStub{result: providers.ParsedCandidate{JSON: raw, Model: "qwen3.8-flash"}}}
	payload, _ := json.Marshal(map[string]string{"data_source_id": sourceID.String()})
	if err := handler.Handle(context.Background(), jobs.Job{Kind: jobs.KindSourceParse, UserID: userID, Payload: payload}); err != nil {
		t.Fatal(err)
	}

	result := repository.parseResult
	if repository.invalid || result == nil {
		t.Fatalf("invalid=%t result=%#v", repository.invalid, result)
	}
	evidence := result.ParsedResponse.Candidate.AccountEvidence
	if evidence.CardLastFour != "" || len(evidence.AdditionalIdentifiers) != 1 || evidence.AdditionalIdentifiers[0] != "1234" {
		t.Fatalf("unsafe card evidence was not demoted: %#v", evidence)
	}
	if result.AutoEligible || string(result.ModelOutput) != string(raw) {
		t.Fatalf("eligibility=%t model output changed=%t", result.AutoEligible, string(result.ModelOutput) != string(raw))
	}
	decision, err := reconciliation.Reconcile(result.ParsedResponse.Candidate, []reconciliation.AccountIdentity{{
		ID: "account", UserID: userID.String(), MatchingKeys: []reconciliation.AccountMatchingKey{{KeyType: "card_last_four", NormalizedValue: "1234"}},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Outcome != reconciliation.OutcomeDangling {
		t.Fatalf("demoted suffix participated in matching: %#v", decision)
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
	if repository.audit.Model == "" || repository.audit.AssembledSystemPrompt == "" ||
		repository.audit.NormalizedInput != "receipt" || len(repository.audit.PromptComponents) == 0 ||
		len(repository.audit.ModelOutput) == 0 {
		t.Fatalf("invalid attempt lost available audit data: %#v", repository.audit)
	}
}

func TestHandlerRecoversOnlyInvalidOptionalCategoryCitation(t *testing.T) {
	userID, sourceID := uuid.New(), uuid.New()
	responseWithCategory := func(t *testing.T) []byte {
		t.Helper()
		var parsed reconciliation.ParsedResponse
		if err := json.Unmarshal(validResponseJSON(userID), &parsed); err != nil {
			t.Fatal(err)
		}
		parsed.Candidate.AccountEvidence.CardLastFour = "2562"
		parsed.Candidate.CategoryLeafName = "Groceries"
		parsed.Evidence = append(parsed.Evidence, reconciliation.FieldEvidence{
			Field: "category_leaf_name", SourcePath: "merchant_name", Confidence: .9,
		})
		raw, err := json.Marshal(parsed)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}

	t.Run("invalid category path is discarded", func(t *testing.T) {
		repository := &repositoryStub{parseInput: transactionstore.SourceParseInput{
			ID: sourceID, NormalizedContent: "text: FairPrice Mastercard (**** 2562)",
		}}
		handler := Handler{Repository: repository, Parser: parserStub{result: providers.ParsedCandidate{
			JSON: responseWithCategory(t), Model: "qwen3.8-flash",
		}}}
		payload, _ := json.Marshal(map[string]string{"data_source_id": sourceID.String()})
		if err := handler.Handle(context.Background(), jobs.Job{Kind: jobs.KindSourceParse, UserID: userID, Payload: payload}); err != nil {
			t.Fatal(err)
		}
		if repository.invalid || repository.parseResult == nil {
			t.Fatalf("invalid=%t result=%#v", repository.invalid, repository.parseResult)
		}
		parsed := repository.parseResult.ParsedResponse
		if parsed.Candidate.CategoryLeafName != "" || parsed.Candidate.AccountEvidence.CardLastFour != "2562" {
			t.Fatalf("unexpected recovered candidate: %#v", parsed.Candidate)
		}
		for _, evidence := range parsed.Evidence {
			if evidence.Field == "category_leaf_name" {
				t.Fatalf("invalid category evidence remained: %#v", evidence)
			}
		}
	})

	t.Run("invalid required-field path still fails", func(t *testing.T) {
		var parsed reconciliation.ParsedResponse
		if err := json.Unmarshal(responseWithCategory(t), &parsed); err != nil {
			t.Fatal(err)
		}
		for index := range parsed.Evidence {
			if parsed.Evidence[index].Field == "original_currency" {
				parsed.Evidence[index].SourcePath = "original_currency"
			}
		}
		raw, _ := json.Marshal(parsed)
		repository := &repositoryStub{parseInput: transactionstore.SourceParseInput{
			ID: sourceID, NormalizedContent: "text: FairPrice Mastercard (**** 2562)",
		}}
		handler := Handler{Repository: repository, Parser: parserStub{result: providers.ParsedCandidate{
			JSON: raw, Model: "qwen3.8-flash",
		}}}
		payload, _ := json.Marshal(map[string]string{"data_source_id": sourceID.String()})
		if err := handler.Handle(context.Background(), jobs.Job{Kind: jobs.KindSourceParse, UserID: userID, Payload: payload}); err != nil {
			t.Fatal(err)
		}
		if !repository.invalid || repository.parseResult != nil {
			t.Fatalf("invalid=%t result=%#v", repository.invalid, repository.parseResult)
		}
	})
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

func TestHandlerAssemblesSelectedPromptAndPersistsExactAudit(t *testing.T) {
	userID, sourceID := uuid.New(), uuid.New()
	raw := validResponseJSON(userID)
	globalID, userRuleID := uuid.NewString(), uuid.NewString()
	config := json.RawMessage(`{"extractors":{"card_last_four":{"pattern":"Mastercard\\s*\\(\\*{4}\\s*([0-9]{4})\\)","group":1}}}`)
	normalized := "subject: Your FairPrice Group app receipt\nsender: FairPrice <no-reply@fairprice.com.sg>\ntext: Cafe Mastercard (**** 2562) SGD 5.00"
	repository := &repositoryStub{parseInput: transactionstore.SourceParseInput{
		ID: sourceID, Subject: "Your FairPrice Group app receipt",
		Sender: "FairPrice <no-reply@fairprice.com.sg>", Content: "Cafe Mastercard (**** 2562) SGD 5.00",
		NormalizedContent: normalized, DefaultInstructions: "prefer explicit receipt facts", DefaultInstructionsVersion: 3,
		Rules:     []parserrules.Rule{{ID: globalID, Version: 2, Priority: 50, ContentMatcher: `FairPrice`, PromptFragment: "global guidance", ExtractionConfig: config}},
		UserRules: []parserrules.UserRule{{ID: userRuleID, Name: "FairPrice receipts", Version: 4, Priority: 10, SenderMatchType: "domain", SenderMatchValue: "fairprice.com.sg", SubjectMatcher: `app receipt`, ContentMatcher: `Mastercard`, PromptFragment: "user rule guidance"}},
	}}
	parser := &parserCapture{result: providers.ParsedCandidate{
		JSON: raw, Model: "qwen3.8-flash", ProviderRequest: json.RawMessage(`{"request":"exact"}`),
		ProviderResponse: json.RawMessage(`{"response":"exact"}`),
	}}
	handler := Handler{Repository: repository, Parser: parser}
	payload, _ := json.Marshal(map[string]string{"data_source_id": sourceID.String()})
	if err := handler.Handle(context.Background(), jobs.Job{Kind: jobs.KindSourceParse, UserID: userID, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if !parser.called || parser.normalizedInput != normalized {
		t.Fatalf("parser call = %t input=%q", parser.called, parser.normalizedInput)
	}
	for _, wanted := range []string{"global guidance", "prefer explicit receipt facts", "user rule guidance"} {
		if !strings.Contains(parser.systemPrompt, wanted) {
			t.Fatalf("system prompt omitted %q", wanted)
		}
	}
	if strings.Contains(parser.systemPrompt, normalized) {
		t.Fatal("email source content was promoted into the system prompt")
	}
	result := repository.parseResult
	if result == nil || result.ParsedResponse.Candidate.AccountEvidence.CardLastFour != "2562" || !result.AutoEligible {
		t.Fatalf("deterministic rule did not run after Qwen: %#v", result)
	}
	if result.RuleID != globalID || result.RuleVersion != 2 || result.UserRuleID != userRuleID || result.UserRuleVersion != 4 {
		t.Fatalf("rule provenance = %#v", result.SourceParseAudit)
	}
	if string(result.ProviderRequest) != `{"request":"exact"}` || string(result.ProviderResponse) != `{"response":"exact"}` || string(result.ModelOutput) != string(raw) {
		t.Fatalf("audit bodies changed: %#v", result.SourceParseAudit)
	}
	if !strings.Contains(string(result.PromptComponents), `"version":3`) {
		t.Fatalf("default instruction version missing from prompt components: %s", result.PromptComponents)
	}
}

func TestHandlerTreatsEqualTopUserRulePriorityAsConfigurationFailure(t *testing.T) {
	userID, sourceID := uuid.New(), uuid.New()
	rules := []parserrules.UserRule{
		{ID: uuid.NewString(), Version: 1, Priority: 5, SenderMatchType: "domain", SenderMatchValue: "example.test"},
		{ID: uuid.NewString(), Version: 1, Priority: 5, SenderMatchType: "domain", SenderMatchValue: "example.test"},
	}
	repository := &repositoryStub{parseInput: transactionstore.SourceParseInput{
		ID: sourceID, Sender: "alerts@example.test", NormalizedContent: "receipt", UserRules: rules,
	}}
	parser := &parserCapture{}
	handler := Handler{Repository: repository, Parser: parser}
	payload, _ := json.Marshal(map[string]string{"data_source_id": sourceID.String()})
	if err := handler.Handle(context.Background(), jobs.Job{Kind: jobs.KindSourceParse, UserID: userID, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if !repository.failed || parser.called {
		t.Fatalf("failed=%t parser.called=%t", repository.failed, parser.called)
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
				path := userID.String() + "/" + sourceID.String() + "/attachment-" + strconv.Itoa(index)
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
		Accounts: []reconciliation.AccountIdentity{{ID: accountID.String(), UserID: userID.String(), MatchingKeys: []reconciliation.AccountMatchingKey{{KeyType: "card_last_four", NormalizedValue: "1234"}}}},
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

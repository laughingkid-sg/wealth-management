package bulkstore

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/zhengteck/wealth-builder/backend/internal/bulkparse"
	"github.com/zhengteck/wealth-builder/backend/internal/bulkprompt"
	"github.com/zhengteck/wealth-builder/backend/internal/bulkworker"
)

func TestClaimBulkChunkSQLQualifiesTimestampAgainstUpdateFromRelations(t *testing.T) {
	if !strings.Contains(claimBulkChunkSQL, "coalesce(c.started_at,now())") || strings.Contains(claimBulkChunkSQL, "coalesce(started_at,now())") {
		t.Fatalf("claim SQL must qualify started_at: %s", claimBulkChunkSQL)
	}
}

func TestBuildChunkFailureAuditPreservesInvalidAndTransientAttemptEnvelope(t *testing.T) {
	work := bulkworker.Work{UserID: uuid.New(), BatchID: uuid.New(), DocumentID: uuid.New(), ChunkID: uuid.New(), Generation: 2}
	prompt := bulkprompt.Assembly{
		PlatformVersion: 1, SchemaVersion: 1, SystemPrompt: "system contract",
		UserMessage:        json.RawMessage(`{"document":{"page_manifest":["file[0].page[1]"]}}`),
		VisualPlaceholders: []string{"page placeholder"},
	}
	audit, err := buildChunkFailureAudit(work, bulkworker.ChunkFailure{
		Class: "model_output_invalid", Terminal: true, ModelName: "test-model", Prompt: prompt,
		ProviderRequest: json.RawMessage(`{"request":true}`), ProviderResponse: json.RawMessage(`{"response":true}`),
		ModelOutput: json.RawMessage(`not-json`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if audit.ModelName != "test-model" || audit.SystemPrompt != prompt.SystemPrompt || audit.NormalizedInput != string(prompt.UserMessage) {
		t.Fatalf("audit envelope lost scalar fields: %#v", audit)
	}
	for name, raw := range map[string][]byte{
		"request_metadata": audit.RequestMetadata, "provider_request": audit.ProviderRequest,
		"provider_response": audit.ProviderResponse, "model_output": audit.ModelOutput,
		"prompt_components": audit.PromptComponents,
	} {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil || object == nil {
			t.Fatalf("%s was not stored as an object: %s", name, raw)
		}
	}
	var wrapped map[string]string
	if err := json.Unmarshal(audit.ModelOutput, &wrapped); err != nil || wrapped["raw_model_output"] != "not-json" {
		t.Fatalf("raw invalid model output was not preserved: %s", audit.ModelOutput)
	}
}

func TestChunkFailureAuditEnforcesDatabaseByteCeilings(t *testing.T) {
	base := chunkFailureAudit{RequestMetadata: []byte(`{}`), ProviderRequest: []byte(`{}`), ProviderResponse: []byte(`{}`), ModelOutput: []byte(`{}`), PromptComponents: []byte(`{}`)}
	tests := []struct {
		name   string
		mutate func(*chunkFailureAudit)
	}{
		{name: "request metadata", mutate: func(value *chunkFailureAudit) {
			value.RequestMetadata = []byte(strings.Repeat("x", maxChunkAuditRequestMetadataBytes+1))
		}},
		{name: "system prompt", mutate: func(value *chunkFailureAudit) {
			value.SystemPrompt = strings.Repeat("x", maxChunkAuditSystemPromptBytes+1)
		}},
		{name: "normalized input", mutate: func(value *chunkFailureAudit) {
			value.NormalizedInput = strings.Repeat("x", maxChunkAuditNormalizedInputBytes+1)
		}},
		{name: "provider request", mutate: func(value *chunkFailureAudit) {
			value.ProviderRequest = []byte(strings.Repeat("x", maxChunkAuditProviderRequestBytes+1))
		}},
		{name: "provider response", mutate: func(value *chunkFailureAudit) {
			value.ProviderResponse = []byte(strings.Repeat("x", maxChunkAuditProviderResponseBytes+1))
		}},
		{name: "model output", mutate: func(value *chunkFailureAudit) {
			value.ModelOutput = []byte(strings.Repeat("x", maxChunkAuditModelOutputBytes+1))
		}},
		{name: "prompt components", mutate: func(value *chunkFailureAudit) {
			value.PromptComponents = []byte(strings.Repeat("x", maxChunkAuditPromptComponentsBytes+1))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := base
			test.mutate(&value)
			if err := validateChunkFailureAudit(value); err == nil {
				t.Fatal("expected byte ceiling rejection")
			}
		})
	}
}

func TestChunkFailureSummaryIncludesBoundedDiagnosticDetail(t *testing.T) {
	summary := chunkFailureSummary(bulkworker.ChunkFailure{
		Class:  "model_output_invalid",
		Detail: strings.Repeat("diagnostic", 400),
	})
	if !strings.HasPrefix(summary, "model_output_invalid: diagnostic") || len(summary) != 2000 {
		t.Fatalf("unexpected summary length=%d value=%q", len(summary), summary)
	}
}

func TestDocumentGlobalLineIndexesRemainStableAcrossChunkOverlap(t *testing.T) {
	first := 0
	indexes := documentGlobalLineIndexes([]bulkparse.Deduped{
		{Index: 0}, {Index: 1}, {Index: 2, DuplicateOf: &first}, {Index: 3},
	})
	want := []int{1, 2, 1, 3}
	for index := range want {
		if indexes[index] != want[index] {
			t.Fatalf("indexes = %#v, want %#v", indexes, want)
		}
	}
}

func TestBillSummaryConflictRemainsUnresolvedAcrossABAChunks(t *testing.T) {
	a, b := "2026-08-31", "2026-09-01"
	conflicts := make(map[string]bool)
	var summary *bulkparse.BillSummary
	for _, value := range []*string{&a, &b, &a} {
		summary = mergeBillSummary(summary, &bulkparse.BillSummary{PeriodEnd: value}, conflicts)
	}
	if summary.PeriodEnd != nil || !conflicts["period_end"] {
		t.Fatalf("summary=%#v conflicts=%#v", summary, conflicts)
	}
}

func TestClaimedPrepareMakesCancellationDrainBeforeCancelled(t *testing.T) {
	status, err := cancellationTarget("queued", true)
	if err != nil || status != "cancelling" {
		t.Fatalf("status=%q err=%v", status, err)
	}
	status, err = cancellationTarget("queued", false)
	if err != nil || status != "cancelled" {
		t.Fatalf("idle status=%q err=%v", status, err)
	}
}

func TestLayoutTemporarySortOrdersStayPositiveAndCollisionFree(t *testing.T) {
	seen := make(map[int]bool)
	for index := 0; index < 6; index++ {
		order, err := temporarySortOrder(12, index)
		if err != nil || order < 0 || seen[order] {
			t.Fatalf("index=%d order=%d err=%v seen=%v", index, order, err, seen)
		}
		seen[order] = true
	}
	if _, err := temporarySortOrder(-1, 0); err == nil {
		t.Fatal("negative temporary base accepted")
	}
}

func TestBulkDebugFieldContractIncludesStoredRequestAndOutput(t *testing.T) {
	for _, field := range []string{"request_metadata", "parsed_candidate", "assembled_system_prompt", "normalized_input", "provider_request", "provider_response", "model_output", "prompt_components"} {
		expression, maxBytes, ok := bulkDebugFieldSpec(field)
		if !ok || expression == "" || maxBytes < 1 {
			t.Fatalf("field %q: expression=%q max=%d ok=%t", field, expression, maxBytes, ok)
		}
	}
	if _, _, ok := bulkDebugFieldSpec("service_role_key"); ok {
		t.Fatal("unsupported debug field accepted")
	}
}

func TestDocumentFailureOutcomeCountsFailedChunksWithoutDoubleCountingCandidates(t *testing.T) {
	failed, status := documentFailureOutcome(2, 1)
	if failed != 3 || status != "completed_with_errors" {
		t.Fatalf("failed=%d status=%q", failed, status)
	}
	failed, status = documentFailureOutcome(0, 0)
	if failed != 0 || status != "completed" {
		t.Fatalf("successful failed=%d status=%q", failed, status)
	}
}

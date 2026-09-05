package transactionworker

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/zhengteck/wealth-builder/backend/internal/reconciliation"
	"github.com/zhengteck/wealth-builder/backend/internal/scriptstore"
)

func sampleParsed() reconciliation.ParsedResponse {
	return reconciliation.ParsedResponse{
		Candidate: reconciliation.Candidate{
			UserID:              "user-1",
			Kind:                reconciliation.KindDebit,
			Title:               "coffee",
			MerchantName:        "the coffee shop",
			OriginalAmountMinor: 450,
			OriginalCurrency:    "SGD",
			OccurredAt:          time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC),
			Confidence:          0.9,
			AutoEligible:        true,
		},
		Evidence: []reconciliation.FieldEvidence{{Field: "title", SourcePath: "subject", Confidence: 0.9}},
	}
}

func postScript(v int) fakeResolver {
	return fakeResolver{script: scriptstore.ActiveScript{Key: "transaction_post_process", Version: v, Source: "x"}}
}

func TestPostprocessInertWithoutScript(t *testing.T) {
	in := sampleParsed()
	got, note := Handler{}.postprocessCandidate(context.Background(), in)
	if note != "" || got.Candidate.MerchantName != in.Candidate.MerchantName {
		t.Fatalf("inert handler mutated candidate: note=%q merchant=%q", note, got.Candidate.MerchantName)
	}
	h := Handler{Engine: fakeEngine{}, Scripts: fakeResolver{err: scriptstore.ErrNoActiveScript}}
	got, note = h.postprocessCandidate(context.Background(), in)
	if note != "" || got.Candidate.MerchantName != in.Candidate.MerchantName {
		t.Fatalf("no-active-script mutated candidate: note=%q", note)
	}
}

func TestPostprocessAppliesFullTransformedCandidate(t *testing.T) {
	in := sampleParsed()
	// A well-behaved script echoes the full candidate with a normalized merchant.
	full := in.Candidate
	full.MerchantName = "THE COFFEE SHOP"
	out, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	h := Handler{Engine: fakeEngine{out: out}, Scripts: postScript(2)}
	got, note := h.postprocessCandidate(context.Background(), in)
	if note != "transaction_post_process:v2" {
		t.Fatalf("note = %q", note)
	}
	if got.Candidate.MerchantName != "THE COFFEE SHOP" {
		t.Fatalf("merchant not applied: %q", got.Candidate.MerchantName)
	}
	// Server-owned fields survive even though the script's JSON never carries them.
	if got.Candidate.UserID != "user-1" || got.Candidate.Confidence != 0.9 || !got.Candidate.AutoEligible {
		t.Fatalf("server-owned fields not preserved: %+v", got.Candidate)
	}
}

func TestPostprocessFallsBackOnBadOutput(t *testing.T) {
	in := sampleParsed()
	h := Handler{Engine: fakeEngine{out: json.RawMessage(`{"merchant_name":"x","unknown":1}`)}, Scripts: postScript(1)}
	got, note := h.postprocessCandidate(context.Background(), in)
	if note != "fallback:invalid_output" {
		t.Fatalf("note = %q, want fallback:invalid_output", note)
	}
	if got.Candidate.MerchantName != in.Candidate.MerchantName {
		t.Fatalf("candidate should be unchanged on fallback")
	}
}

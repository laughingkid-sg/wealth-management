package transactionstore

import (
	"math"
	"testing"

	"github.com/google/uuid"
)

func TestDecodePersistedCandidateBindsOwnerAndAcceptsValidatedRuleEvidence(t *testing.T) {
	userID := uuid.New()
	raw := []byte(`{
		"candidate": {
			"transaction_kind": "debit",
			"title": "Card purchase",
			"merchant_name": "Merchant",
			"original_amount_minor": 1250,
			"original_currency": "SGD",
			"occurred_at": "2026-09-02T12:00:00Z",
			"references": [],
			"account_evidence": {"card_last_four":"4242","masked_bank_reference":"","additional_identifiers":[]},
			"line_items": []
		},
		"evidence": [
			{"field":"transaction_kind","source_path":"rule:rule-id:v1","confidence":1},
			{"field":"title","source_path":"subject","confidence":0.91},
			{"field":"merchant_name","source_path":"text.merchant","confidence":0.9},
			{"field":"original_amount_minor","source_path":"text.amount","confidence":0.89},
			{"field":"original_currency","source_path":"rule:rule-id:v1","confidence":1},
			{"field":"occurred_at","source_path":"sender.date","confidence":0.88},
			{"field":"account_evidence","source_path":"rule:rule-id:v1","confidence":1}
		]
	}`)
	parsed, err := decodePersistedCandidate(raw, userID)
	if err != nil {
		t.Fatalf("decodePersistedCandidate() error = %v", err)
	}
	if parsed.Candidate.UserID != userID.String() {
		t.Fatalf("bound user = %q, want %q", parsed.Candidate.UserID, userID)
	}
	if math.Abs(parsed.Candidate.Confidence-0.88) > 0.0001 {
		t.Fatalf("recomputed confidence = %f, want 0.88", parsed.Candidate.Confidence)
	}
}

func TestDecodePersistedCandidateStillRejectsUntrustedEvidencePaths(t *testing.T) {
	raw := []byte(`{
		"candidate": {
			"transaction_kind":"debit","title":"Purchase",
			"merchant_name":"","original_amount_minor":100,
			"original_currency":"SGD","occurred_at":"2026-09-02T12:00:00Z",
			"references":[],"account_evidence":{"card_last_four":"","masked_bank_reference":"","additional_identifiers":[]},
			"line_items":[]
		},
		"evidence":[
			{"field":"transaction_kind","source_path":"https://attacker.example","confidence":1},
			{"field":"title","source_path":"email.subject","confidence":1},
			{"field":"original_amount_minor","source_path":"email.text","confidence":1},
			{"field":"original_currency","source_path":"email.text","confidence":1},
			{"field":"occurred_at","source_path":"sender.date","confidence":1}
		]
	}`)
	if _, err := decodePersistedCandidate(raw, uuid.New()); err == nil {
		t.Fatal("untrusted persisted evidence path unexpectedly passed validation")
	}
}

func TestUniqueCategoryLeafKeepsKnownExactMatch(t *testing.T) {
	categoryID := uuid.New()
	resolved := uniqueCategoryLeaf([]uuid.UUID{categoryID})
	if resolved == nil || *resolved != categoryID {
		t.Fatalf("uniqueCategoryLeaf() = %#v, want %s", resolved, categoryID)
	}
}

func TestUniqueCategoryLeafIgnoresUnknownOrAmbiguousOptionalSuggestion(t *testing.T) {
	if resolved := uniqueCategoryLeaf(nil); resolved != nil {
		t.Fatalf("unknown category resolved to %s", *resolved)
	}
	if resolved := uniqueCategoryLeaf([]uuid.UUID{uuid.New(), uuid.New()}); resolved != nil {
		t.Fatalf("ambiguous category resolved to %s", *resolved)
	}
}

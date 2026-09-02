package transactionstore

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

func TestApplySourceSuggestionProjectsLatestValidatedCandidate(t *testing.T) {
	source := SourceSummary{}
	applySourceSuggestion(&source, json.RawMessage(`{
		"candidate": {
			"transaction_kind": "debit",
			"title": "Coffee beans",
			"merchant_name": "Roaster",
			"original_amount_minor": 1899,
			"original_currency": "SGD",
			"category_leaf_name": "Coffee Shops",
			"occurred_at": "2026-09-02T12:00:00Z",
			"references": [],
			"account_evidence": {},
			"line_items": []
		},
		"evidence": []
	}`))
	if source.SuggestedTitle == nil || *source.SuggestedTitle != "Coffee beans" {
		t.Fatalf("suggested title = %#v", source.SuggestedTitle)
	}
	if source.SuggestedAmountMinor == nil || *source.SuggestedAmountMinor != 1899 {
		t.Fatalf("suggested amount = %#v", source.SuggestedAmountMinor)
	}
	if source.SuggestedCurrency == nil || *source.SuggestedCurrency != "SGD" {
		t.Fatalf("suggested currency = %#v", source.SuggestedCurrency)
	}
	if source.SuggestedCategoryLeafName == nil || *source.SuggestedCategoryLeafName != "Coffee Shops" {
		t.Fatalf("suggested category = %#v", source.SuggestedCategoryLeafName)
	}
}

func TestDecodeJSONInt64AcceptsOnlyIntegerRepresentations(t *testing.T) {
	for name, input := range map[string]json.RawMessage{
		"fraction": json.RawMessage(`18.99`),
		"object":   json.RawMessage(`{"value":1899}`),
		"trailing": json.RawMessage(`1899 true`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, ok := decodeJSONInt64(input); ok {
				t.Fatalf("decodeJSONInt64(%s) unexpectedly succeeded", input)
			}
		})
	}
	if value, ok := decodeJSONInt64(json.RawMessage(`"1899"`)); !ok || value != 1899 {
		t.Fatalf("string integer = %d, %t", value, ok)
	}
}

func TestDedupeUUIDsKeepsOneLinkPerLeg(t *testing.T) {
	sourceID := uuid.New()
	result := dedupeUUIDs([]uuid.UUID{sourceID, uuid.Nil, sourceID})
	if len(result) != 1 || result[0] != sourceID {
		t.Fatalf("dedupeUUIDs() = %#v", result)
	}
	// Separate calls intentionally retain the same source so one piece of
	// evidence can support both legs of an internal transfer.
	otherLeg := dedupeUUIDs([]uuid.UUID{sourceID})
	if len(otherLeg) != 1 || otherLeg[0] != sourceID {
		t.Fatalf("second transfer leg = %#v", otherLeg)
	}
}

func TestAttachmentRecordIDHasStableFallbacks(t *testing.T) {
	if got := attachmentRecordID("provider-id", "checksum", "path"); got != "provider-id" {
		t.Fatalf("provider ID fallback = %q", got)
	}
	if got := attachmentRecordID("", "checksum", "path"); got != "checksum" {
		t.Fatalf("checksum fallback = %q", got)
	}
	first := attachmentRecordID("", "", "owner/source/object")
	second := attachmentRecordID("", "", "owner/source/object")
	if first == "" || first != second {
		t.Fatalf("object fallback is unstable: %q, %q", first, second)
	}
}

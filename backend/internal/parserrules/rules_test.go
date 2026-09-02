package parserrules

import (
	"encoding/json"
	"testing"
)

func TestMatchAndApplyUsesHighestPriorityValidRE2Rule(t *testing.T) {
	config, _ := json.Marshal(ExtractionConfig{Constants: map[string]string{"transaction_kind": "debit", "original_currency": "SGD"}, Extractors: map[string]CaptureField{"original_amount_minor": {Pattern: `amount: ([0-9]+)`, Group: 1}}})
	rule, ok := MatchAndApply("alerts@bank.test", "amount: 648", []Rule{
		{ID: "low", Version: 1, Priority: 1, SenderMatcher: `bank`, ExtractionConfig: config},
		{ID: "high", Version: 2, Priority: 10, SenderMatcher: `bank`, ExtractionConfig: config},
	})
	if !ok || rule.ID != "high" || rule.Version != 2 || rule.Values["original_amount_minor"][0] != "648" {
		t.Fatalf("MatchAndApply() = %#v, %t", rule, ok)
	}
}

func TestMatchAndApplyRejectsInvalidRuleSafely(t *testing.T) {
	rule, ok := MatchAndApply("alerts@bank.test", "amount: 648", []Rule{{ID: "invalid", Version: 1, SenderMatcher: "(", ExtractionConfig: json.RawMessage(`{}`)}})
	if ok || rule.ID != "" {
		t.Fatalf("invalid rule applied: %#v", rule)
	}
}

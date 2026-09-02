package parserrules

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestMatchAndApplyUsesHighestPriorityValidRE2Rule(t *testing.T) {
	config, _ := json.Marshal(ExtractionConfig{Constants: map[string]string{"transaction_kind": "debit", "original_currency": "SGD"}, Extractors: map[string]CaptureField{"original_amount_minor": {Pattern: `amount: ([0-9]+)`, Group: 1}}})
	rule, ok, err := MatchAndApply("alerts@bank.test", "amount: 648", []Rule{
		{ID: "low", Version: 1, Priority: 1, SenderMatcher: `bank`, ExtractionConfig: config},
		{ID: "high", Version: 2, Priority: 10, SenderMatcher: `bank`, ExtractionConfig: config},
	})
	if err != nil || !ok || rule.ID != "high" || rule.Version != 2 || rule.Values["original_amount_minor"][0] != "648" {
		t.Fatalf("MatchAndApply() = %#v, %t", rule, ok)
	}
}

func TestMatchAndApplyRejectsInvalidRuleSafely(t *testing.T) {
	rule, ok, err := MatchAndApply("alerts@bank.test", "amount: 648", []Rule{{ID: "invalid", Version: 1, SenderMatcher: "(", ExtractionConfig: json.RawMessage(`{}`)}})
	if err != nil || ok || rule.ID != "" {
		t.Fatalf("invalid rule applied: %#v", rule)
	}
}

func TestMatchAndApplyRejectsEqualTopPriorityAndExtractsFairPriceCard(t *testing.T) {
	config, _ := json.Marshal(ExtractionConfig{Extractors: map[string]CaptureField{
		"card_last_four": {Pattern: `(?i)Mastercard\s*\(\s*\*{4}\s*([0-9]{4})\s*\)`, Group: 1},
	}})
	content := "Your FairPrice Group app receipt\nPayment method: Mastercard\n(****   2562)"
	rule, ok, err := MatchAndApply("FairPrice <no-reply@fairprice.com.sg>", content, []Rule{{
		ID: "fairprice", Version: 1, Priority: 50, SenderMatcher: `(?i)fairprice\.com\.sg`,
		ContentMatcher: `(?is)FairPrice.*Mastercard`, ExtractionConfig: config,
	}})
	if err != nil || !ok || rule.Values["card_last_four"][0] != "2562" {
		t.Fatalf("FairPrice match = %#v, %t, %v", rule, ok, err)
	}
	_, _, err = MatchAndApply("alerts@bank.test", "amount", []Rule{
		{ID: "a", Version: 1, Priority: 10, ExtractionConfig: json.RawMessage(`{}`)},
		{ID: "b", Version: 1, Priority: 10, ExtractionConfig: json.RawMessage(`{}`)},
	})
	if !errors.Is(err, ErrAmbiguousTopPriority) {
		t.Fatalf("tie error = %v", err)
	}
}

func TestMatchUserRuleUsesANDConditionsAndRejectsEqualTopPriority(t *testing.T) {
	rules := []UserRule{{
		ID: "rule-a", Name: "FairPrice", Version: 2, Priority: 100,
		SenderMatchType: "domain", SenderMatchValue: "fairprice.com.sg",
		SubjectMatcher: `(?i)app receipt`, ContentMatcher: `(?i)Mastercard`,
	}}
	rule, ok, err := MatchUserRule(
		"FairPrice <no-reply@fairprice.com.sg>", "Your app receipt", "Mastercard 2562", rules,
	)
	if err != nil || !ok || rule.ID != "rule-a" {
		t.Fatalf("user rule match = %#v, %t, %v", rule, ok, err)
	}
	if _, ok, err = MatchUserRule(
		"FairPrice <no-reply@fairprice.com.sg>", "Other subject", "Mastercard 2562", rules,
	); err != nil || ok {
		t.Fatalf("AND mismatch = %t, %v", ok, err)
	}
	rules = append(rules, UserRule{
		ID: "rule-b", Name: "Tie", Version: 1, Priority: 100,
		SenderMatchType: "domain", SenderMatchValue: "fairprice.com.sg",
		SubjectMatcher: `(?i)app receipt`, ContentMatcher: `(?i)Mastercard`,
	})
	if _, _, err = MatchUserRule(
		"FairPrice <no-reply@fairprice.com.sg>", "Your app receipt", "Mastercard 2562", rules,
	); !errors.Is(err, ErrAmbiguousTopPriority) {
		t.Fatalf("tie error = %v", err)
	}
}

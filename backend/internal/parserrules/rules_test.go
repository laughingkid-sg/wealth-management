package parserrules

import (
	"errors"
	"testing"
)

func TestMatchAndApplyUsesHighestPriorityMatchingRule(t *testing.T) {
	rule, ok, err := MatchAndApply("alerts@bank.test", "amount: 648", []Rule{
		{ID: "low", Version: 1, Priority: 1, SenderMatcher: `bank`, PromptFragment: "low"},
		{ID: "high", Version: 2, Priority: 10, SenderMatcher: `bank`, PromptFragment: "high"},
	})
	if err != nil || !ok || rule.ID != "high" || rule.Version != 2 || rule.PromptFragment != "high" {
		t.Fatalf("MatchAndApply() = %#v, %t", rule, ok)
	}
}

func TestMatchAndApplyRejectsInvalidRuleSafely(t *testing.T) {
	rule, ok, err := MatchAndApply("alerts@bank.test", "amount: 648", []Rule{{ID: "invalid", Version: 1, SenderMatcher: "("}})
	if err != nil || ok || rule.ID != "" {
		t.Fatalf("invalid rule applied: %#v", rule)
	}
}

func TestMatchAndApplyMatchesSenderAndContentAndRejectsEqualTopPriority(t *testing.T) {
	content := "Your FairPrice Group app receipt\nPayment method: Mastercard\n(****   2562)"
	rule, ok, err := MatchAndApply("FairPrice <no-reply@fairprice.com.sg>", content, []Rule{{
		ID: "fairprice", Version: 1, Priority: 50, SenderMatcher: `(?i)fairprice\.com\.sg`,
		ContentMatcher: `(?is)FairPrice.*Mastercard`, PromptFragment: "Read the card.",
	}})
	if err != nil || !ok || rule.ID != "fairprice" || rule.PromptFragment != "Read the card." {
		t.Fatalf("FairPrice match = %#v, %t, %v", rule, ok, err)
	}
	// A rule whose content matcher does not match is skipped.
	if _, matched, matchErr := MatchAndApply("x@bank.test", "unrelated", []Rule{{ID: "c", Version: 1, Priority: 5, ContentMatcher: `nope`}}); matchErr != nil || matched {
		t.Fatalf("non-matching content applied: %t %v", matched, matchErr)
	}
	// Two matching rules at the same highest priority are ambiguous.
	_, _, err = MatchAndApply("alerts@bank.test", "amount", []Rule{
		{ID: "a", Version: 1, Priority: 10, SenderMatcher: `bank`},
		{ID: "b", Version: 1, Priority: 10, SenderMatcher: `bank`},
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

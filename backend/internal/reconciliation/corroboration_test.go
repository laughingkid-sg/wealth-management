package reconciliation

import (
	"testing"
	"time"
)

func TestDeriveAutoEligibilityRequiresDirectTextCorroboration(t *testing.T) {
	candidate := Candidate{AccountEvidence: AccountEvidence{CardLastFour: "1234"}, OriginalAmountMinor: 648, OriginalCurrency: "SGD", MerchantName: "DigitalOcean", References: []string{"INV-42"}}
	if !DeriveAutoEligibility(candidate, "Card ending 1234 paid S$6.48 at DigitalOcean") {
		t.Fatal("valid literal corroboration was rejected")
	}
	if !DeriveAutoEligibility(candidate, "Card ending 1234 paid SGD 6.48 ref INV-42") {
		t.Fatal("reference corroboration was rejected")
	}
	for _, source := range []string{"card ending 12345 paid S$6.48 at DigitalOcean", "card ending 1234 paid $6.48 at DigitalOcean", "card ending 1234 paid S$6.48 at Other"} {
		if DeriveAutoEligibility(candidate, source) {
			t.Fatalf("unsafe source authorized: %q", source)
		}
	}
}

func TestReconcileBlocksAutomaticActionsWhenIneligible(t *testing.T) {
	candidate := validCandidate(time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC))
	candidate.AccountEvidence.CardLastFour = "1234"
	candidate.AutoEligible = false
	decision, err := Reconcile(candidate, []AccountIdentity{accountIdentity("account", "user-1", "card_last_four", "1234")}, nil)
	if err != nil || decision.Outcome != OutcomeReview {
		t.Fatalf("create gate = %#v, %v", decision, err)
	}
}

func TestDeriveAutoEligibilityUsesISOMinorUnitsAndGroupedAmounts(t *testing.T) {
	for _, test := range []struct {
		candidate Candidate
		source    string
	}{
		{Candidate{AccountEvidence: AccountEvidence{CardLastFour: "1234"}, OriginalAmountMinor: 123456, OriginalCurrency: "SGD", MerchantName: "Shop"}, "card 1234 SGD 1,234.56 Shop"},
		{Candidate{AccountEvidence: AccountEvidence{CardLastFour: "1234"}, OriginalAmountMinor: 648, OriginalCurrency: "JPY", MerchantName: "Shop"}, "card 1234 JPY 648 Shop"},
		{Candidate{AccountEvidence: AccountEvidence{CardLastFour: "1234"}, OriginalAmountMinor: 6480, OriginalCurrency: "KWD", MerchantName: "Shop"}, "card 1234 KWD 6.480 Shop"},
	} {
		if !DeriveAutoEligibility(test.candidate, test.source) {
			t.Fatalf("corroboration rejected %q", test.source)
		}
	}
}

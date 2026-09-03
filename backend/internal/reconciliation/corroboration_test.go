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
	if !DeriveAutoEligibility(candidate, "DigitalOcean charged Mastercard (**** 1234) S$6.48") {
		t.Fatal("masked-card corroboration was rejected")
	}
	if !DeriveAutoEligibility(candidate, "Card ending 1234 paid SGD 6.48 ref INV-42") {
		t.Fatal("reference corroboration was rejected")
	}
	for _, source := range []string{
		"order 1234 paid S$6.48 at DigitalOcean",
		"card ending 12345 paid S$6.48 at DigitalOcean",
		"card ending 1234 paid $6.48 at DigitalOcean",
		"card ending 1234 paid S$6.48 at Other",
		"Mastercard (**** 1234) and Visa (**** 9876) paid S$6.48 at DigitalOcean",
	} {
		if DeriveAutoEligibility(candidate, source) {
			t.Fatalf("unsafe source authorized: %q", source)
		}
	}
}

func TestSanitizeAccountEvidenceForMatchingRequiresOneContextualCardSuffix(t *testing.T) {
	for _, test := range []struct {
		name        string
		source      string
		wantCard    string
		wantAudited bool
	}{
		{name: "masked card", source: "Mastercard\n(**** 2562)", wantCard: "2562"},
		{name: "explicit suffix", source: "Card ending in 2562", wantCard: "2562"},
		{name: "bare number beside corroborated card", source: "Order 9876 paid with Mastercard (**** 2562)", wantCard: "2562"},
		{name: "unrelated bare digits", source: "Order number 2562", wantAudited: true},
		{name: "different contextual suffix", source: "Mastercard (**** 9876)", wantAudited: true},
		{name: "conflicting card contexts", source: "Mastercard (**** 2562), Visa (**** 9876)", wantAudited: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			evidence := SanitizeAccountEvidenceForMatching(AccountEvidence{CardLastFour: "2562"}, test.source)
			if evidence.CardLastFour != test.wantCard {
				t.Fatalf("card_last_four = %q, want %q", evidence.CardLastFour, test.wantCard)
			}
			audited := len(evidence.AdditionalIdentifiers) == 1 && evidence.AdditionalIdentifiers[0] == "2562"
			if audited != test.wantAudited {
				t.Fatalf("additional identifiers = %#v, want audited=%t", evidence.AdditionalIdentifiers, test.wantAudited)
			}
		})
	}
}

func TestReconcileBlocksAutomaticCreationWhenIneligible(t *testing.T) {
	candidate := validCandidate(time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC))
	candidate.AccountEvidence.CardLastFour = "1234"
	candidate.AutoEligible = false
	decision, err := Reconcile(candidate, []AccountIdentity{accountIdentity("account", "user-1", "card_last_four", "1234")}, nil)
	if err != nil || decision.Outcome != OutcomeReview {
		t.Fatalf("create gate = %#v, %v", decision, err)
	}
}

func TestReconcileAttachesUniquePairBeforeAutoEligibilityGate(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	candidate := validCandidate(now)
	candidate.AccountEvidence.CardLastFour = "2562"
	candidate.AutoEligible = false
	candidate.OriginalAmountMinor = 1095
	candidate.OriginalCurrency = "SGD"

	decision, err := Reconcile(candidate, []AccountIdentity{accountIdentity("citi-rewards", "user-1", "card_last_four", "2562")}, []Transaction{{
		ID: "existing", UserID: "user-1", AccountID: "citi-rewards", Kind: KindDebit,
		OriginalAmountMinor: 1095, OriginalCurrency: "SGD", OccurredAt: now.Add(2 * time.Second),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Outcome != OutcomeAttach || decision.TransactionID != "existing" {
		t.Fatalf("Reconcile() = %#v, want attachment before automatic-creation eligibility", decision)
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

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

func TestSanitizeAccountEvidenceForMatchingAcceptsBoundedSixDigitAmexEnding(t *testing.T) {
	for _, test := range []struct {
		name        string
		source      string
		wantCard    string
		wantAudited bool
	}{
		{name: "six digit card ending", source: "Card ending: 721001", wantCard: "1001"},
		{name: "six unlabelled digits", source: "Reference: 721001", wantAudited: true},
		{name: "contradictory last four label", source: "Card last four: 721001", wantAudited: true},
		{name: "different final four", source: "Card ending: 729876", wantAudited: true},
		{name: "conflicting card endings", source: "Card ending: 721001; Card ending: 729876", wantAudited: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			evidence := SanitizeAccountEvidenceForMatching(AccountEvidence{CardLastFour: "1001"}, test.source)
			if evidence.CardLastFour != test.wantCard {
				t.Fatalf("card_last_four = %q, want %q", evidence.CardLastFour, test.wantCard)
			}
			audited := len(evidence.AdditionalIdentifiers) == 1 && evidence.AdditionalIdentifiers[0] == "1001"
			if audited != test.wantAudited {
				t.Fatalf("additional identifiers = %#v, want audited=%t", evidence.AdditionalIdentifiers, test.wantAudited)
			}
		})
	}
}

func TestAmexCompactAlertIsEligibleAfterAccountSanitization(t *testing.T) {
	candidate := Candidate{
		AccountEvidence:     AccountEvidence{CardLastFour: "1001"},
		OriginalAmountMinor: 700,
		OriginalCurrency:    "SGD",
		MerchantName:        "SUSHI EXPRESS AMK",
	}
	source := "A transaction of SGD7 has been approved on your American Express Card ending: 721001 at SUSHI EXPRESS AMK."
	candidate.AccountEvidence = SanitizeAccountEvidenceForMatching(candidate.AccountEvidence, source)
	if candidate.AccountEvidence.CardLastFour != "1001" {
		t.Fatalf("card_last_four = %q, want 1001", candidate.AccountEvidence.CardLastFour)
	}
	if !DeriveAutoEligibility(candidate, source) {
		t.Fatal("Amex compact alert was not eligible after account sanitization")
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

func TestHasQualifiedAmountAcceptsOmittedTrailingFractionalZeroes(t *testing.T) {
	for _, test := range []struct {
		name   string
		minor  int64
		code   string
		source string
	}{
		{name: "compact ISO whole", minor: 700, code: "SGD", source: "Paid SGD7"},
		{name: "spaced ISO whole", minor: 700, code: "SGD", source: "Paid SGD 7"},
		{name: "one decimal", minor: 700, code: "SGD", source: "Paid SGD7.0"},
		{name: "canonical decimals", minor: 700, code: "SGD", source: "Paid SGD 7.00"},
		{name: "currency after amount", minor: 700, code: "SGD", source: "Paid 7 SGD"},
		{name: "symbol whole", minor: 700, code: "SGD", source: "Paid S$7"},
		{name: "trailing sentence punctuation", minor: 700, code: "SGD", source: "Paid SGD7, at the counter"},
		{name: "leading sentence punctuation", minor: 700, code: "SGD", source: "Paid (7 SGD)"},
		{name: "grouped whole", minor: 123400, code: "SGD", source: "Paid SGD 1,234"},
		{name: "zero decimal currency", minor: 700, code: "JPY", source: "Paid JPY700"},
		{name: "three decimals omit one zero", minor: 6480, code: "KWD", source: "Paid KWD6.48"},
		{name: "three decimals omit all zeroes", minor: 7000, code: "KWD", source: "Paid KWD 7"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if !hasQualifiedAmount(test.minor, test.code, test.source) {
				t.Fatalf("hasQualifiedAmount(%d, %q, %q) = false", test.minor, test.code, test.source)
			}
		})
	}
}

func TestHasQualifiedAmountRejectsDifferentNumericAmounts(t *testing.T) {
	for _, test := range []struct {
		name   string
		minor  int64
		code   string
		source string
	}{
		{name: "extra whole digit", minor: 700, code: "SGD", source: "Paid SGD70"},
		{name: "different fractional amount", minor: 700, code: "SGD", source: "Paid SGD7.01"},
		{name: "grouped larger amount", minor: 700, code: "SGD", source: "Paid SGD7,000"},
		{name: "alphabetic suffix", minor: 700, code: "SGD", source: "Paid SGD7abc"},
		{name: "alphabetic prefix", minor: 700, code: "SGD", source: "Paid abc7 SGD"},
		{name: "nonzero fractional digit omitted", minor: 701, code: "SGD", source: "Paid SGD7"},
		{name: "zero decimal rejects fraction", minor: 700, code: "JPY", source: "Paid JPY700.0"},
		{name: "three decimal nonzero digit omitted", minor: 6481, code: "KWD", source: "Paid KWD6.48"},
		{name: "ambiguous dollar symbol", minor: 700, code: "SGD", source: "Paid $7"},
		{name: "different safe symbol", minor: 700, code: "SGD", source: "Paid US$7"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if hasQualifiedAmount(test.minor, test.code, test.source) {
				t.Fatalf("hasQualifiedAmount(%d, %q, %q) = true", test.minor, test.code, test.source)
			}
		})
	}
}

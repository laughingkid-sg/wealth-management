package reconciliation

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestReconcileAttachesHighConfidenceMatch(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	candidate := validCandidate(now)
	candidate.AccountEvidence.CardLastFour = "•••• 4242"
	candidate.References = []string{"bank-123"}

	decision, err := Reconcile(candidate, []AccountIdentity{accountIdentity("account-1", "user-1", "card_last_four", "4242")}, []Transaction{{
		ID:                  "transaction-1",
		UserID:              "user-1",
		AccountID:           "account-1",
		Kind:                KindDebit,
		MerchantName:        "DigitalOcean",
		OriginalAmountMinor: 648,
		OriginalCurrency:    "USD",
		OccurredAt:          now.Add(8 * time.Minute),
		References:          []string{"BANK-123"},
	}})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if decision.Outcome != OutcomeAttach || decision.TransactionID != "transaction-1" {
		t.Fatalf("Reconcile() = %#v, want attachment to transaction-1", decision)
	}
	if decision.Score.Total() != 177 {
		t.Fatalf("match score = %d, want 177", decision.Score.Total())
	}
}

func TestReconcileNeverMatchesMerchantInvoiceWithoutAccountEvidence(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	candidate := validCandidate(now)
	candidate.MerchantName = "DigitalOcean"
	candidate.AccountEvidence = AccountEvidence{}

	decision, err := Reconcile(candidate, []AccountIdentity{accountIdentity("account-1", "user-1", "card_last_four", "4242")}, []Transaction{{
		ID:                  "transaction-1",
		UserID:              "user-1",
		AccountID:           "account-1",
		Kind:                KindDebit,
		MerchantName:        "DigitalOcean",
		OriginalAmountMinor: 648,
		OriginalCurrency:    "USD",
		OccurredAt:          now,
	}})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if decision.Outcome != OutcomeDangling || decision.TransactionID != "" {
		t.Fatalf("Reconcile() = %#v, want dangling source", decision)
	}
}

func TestReconcileCreatesReliableUnmatchedCandidate(t *testing.T) {
	candidate := validCandidate(time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC))
	candidate.AccountEvidence.MaskedBankReference = "Account ending 1818"

	decision, err := Reconcile(candidate, []AccountIdentity{accountIdentity("account-1", "user-1", "bank_account_suffix", "accountending1818")}, nil)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if decision.Outcome != OutcomeCreate || decision.AccountID != "account-1" {
		t.Fatalf("Reconcile() = %#v, want create for account-1", decision)
	}
}

func TestReconcileUsesTenMinuteWindowOnlyAsSupportingSignal(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	candidate := validCandidate(now)
	candidate.AccountEvidence.CardLastFour = "4242"
	candidate.OriginalAmountMinor = 700
	candidate.OriginalCurrency = "SGD"
	candidate.MerchantName = "Different merchant"

	decision, err := Reconcile(candidate, []AccountIdentity{accountIdentity("account-1", "user-1", "card_last_four", "4242")}, []Transaction{{
		ID:                  "transaction-1",
		UserID:              "user-1",
		AccountID:           "account-1",
		Kind:                KindDebit,
		MerchantName:        "Another merchant",
		OriginalAmountMinor: 600,
		OriginalCurrency:    "USD",
		OccurredAt:          now.Add(9 * time.Minute),
	}})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if decision.Outcome != OutcomeCreate || decision.TransactionID != "" {
		t.Fatalf("Reconcile() = %#v, want creation because time alone is not a plausible collision", decision)
	}
}

func TestReconcileUnrelatedSameAccountActivityDoesNotSuppressCreation(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	candidate := validCandidate(now)
	candidate.AccountEvidence.CardLastFour = "4242"

	decision, err := Reconcile(candidate, []AccountIdentity{
		accountIdentity("account-1", "user-1", "card_last_four", "4242"),
	}, []Transaction{{
		ID: "unrelated", UserID: "user-1", AccountID: "account-1", Kind: KindDebit,
		MerchantName: "Grocer", OriginalAmountMinor: 9900, OriginalCurrency: "SGD",
		OccurredAt: now.Add(12 * time.Hour), References: []string{"different-reference"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Outcome != OutcomeCreate || decision.AccountID != "account-1" {
		t.Fatalf("Reconcile() = %#v, want reliable unmatched creation", decision)
	}
}

func TestReconcilePlausibleAmountCurrencyAndTimeCollisionRequiresReview(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	candidate := validCandidate(now)
	candidate.AccountEvidence.CardLastFour = "4242"
	candidate.MerchantName = ""

	decision, err := Reconcile(candidate, []AccountIdentity{
		accountIdentity("account-1", "user-1", "card_last_four", "4242"),
	}, []Transaction{{
		ID: "plausible", UserID: "user-1", AccountID: "account-1", Kind: KindDebit,
		OriginalAmountMinor: candidate.OriginalAmountMinor, OriginalCurrency: candidate.OriginalCurrency,
		OccurredAt: now.Add(5 * time.Minute),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Outcome != OutcomeReview || decision.TransactionID != "" {
		t.Fatalf("Reconcile() = %#v, want review for a plausible but unsafe collision", decision)
	}
}

func TestReconcileDoesNotAutoAttachOnAccountAmountAndCurrencyOnly(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	candidate := validCandidate(now)
	candidate.AccountEvidence.CardLastFour = "4242"
	candidate.OriginalAmountMinor = 648
	candidate.OriginalCurrency = "USD"
	candidate.MerchantName = ""
	decision, err := Reconcile(candidate, []AccountIdentity{accountIdentity("account-1", "user-1", "card_last_four", "4242")}, []Transaction{{
		ID: "transaction-1", UserID: "user-1", AccountID: "account-1", Kind: KindDebit,
		OriginalAmountMinor: 648, OriginalCurrency: "USD", OccurredAt: now,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Outcome != OutcomeReview {
		t.Fatalf("Reconcile() = %#v, want review for unsafe account+amount+currency match", decision)
	}
}

func TestReconcileReturnsReviewForAmbiguousAccountEvidence(t *testing.T) {
	candidate := validCandidate(time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC))
	candidate.AccountEvidence.CardLastFour = "4242"

	decision, err := Reconcile(candidate, []AccountIdentity{
		accountIdentity("account-1", "user-1", "card_last_four", "4242"),
		accountIdentity("account-2", "user-1", "card_last_four", "4242"),
	}, nil)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if decision.Outcome != OutcomeReview || !strings.Contains(decision.Reason, "more than one") {
		t.Fatalf("Reconcile() = %#v, want ambiguous-account review", decision)
	}
}

func TestAccountMatchingKeysAreTypedExactAndNeverTruncatePAN(t *testing.T) {
	if normalized, err := NormalizeAccountMatchingKey("card_last_four", "•••• 25 62"); err != nil || normalized != "2562" {
		t.Fatalf("masked card normalization = %q, %v", normalized, err)
	}
	if _, err := NormalizeAccountMatchingKey("card_last_four", "5555444433332562"); err == nil {
		t.Fatal("full PAN was truncated instead of rejected")
	}
	if normalized, err := NormalizeAccountMatchingKey("bank_account_suffix", "  ACCT-*• 12-AB/9  "); err != nil || normalized != "acct12ab/9" {
		t.Fatalf("bank suffix normalization = %q, %v", normalized, err)
	}

	candidate := validCandidate(time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC))
	candidate.AccountEvidence.CardLastFour = "2562"
	decision, err := Reconcile(candidate, []AccountIdentity{
		accountIdentity("bank-only", "user-1", "bank_account_suffix", "2562"),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Outcome != OutcomeDangling {
		t.Fatalf("cross-type key matched: %#v", decision)
	}
	candidate.AccountEvidence.CardLastFour = "5555444433332562"
	decision, err = Reconcile(candidate, []AccountIdentity{
		accountIdentity("card", "user-1", "card_last_four", "2562"),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Outcome != OutcomeDangling {
		t.Fatalf("full PAN matched by truncation: %#v", decision)
	}
}

func TestReconcileRejectsCrossUserTransaction(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	candidate := validCandidate(now)
	candidate.AccountEvidence.CardLastFour = "4242"

	decision, err := Reconcile(candidate, []AccountIdentity{accountIdentity("account-1", "user-1", "card_last_four", "4242")}, []Transaction{{
		ID: "other-transaction", UserID: "other-user", AccountID: "account-1", Kind: KindDebit, OriginalAmountMinor: 648, OriginalCurrency: "USD", OccurredAt: now,
	}})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if decision.Outcome != OutcomeCreate {
		t.Fatalf("Reconcile() = %#v, want create rather than cross-user attachment", decision)
	}
}

func TestValidateLineItemRequiresIntegerQuantityAndSafeDetails(t *testing.T) {
	amount := int64(-1)
	err := ValidateLineItem(LineItem{
		SchemaVersion:  1,
		Description:    "Cloud hosting",
		Quantity:       0,
		UnitPriceMinor: &amount,
		Currency:       "usd",
		Details:        json.RawMessage(`[]`),
	})
	if err == nil {
		t.Fatal("ValidateLineItem() error = nil, want validation failure")
	}
	for _, want := range []string{"quantity", "cannot be negative", "currency", "JSON object"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ValidateLineItem() error = %q, missing %q", err, want)
		}
	}
}

func TestValidateParsedResponseRequiresCitedCoreFields(t *testing.T) {
	candidate := validCandidate(time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC))
	err := ValidateParsedResponse(ParsedResponse{Candidate: candidate, Evidence: []FieldEvidence{{Field: "title", SourcePath: "subject", Confidence: 1}}})
	if err == nil || !strings.Contains(err.Error(), "missing required field evidence") {
		t.Fatalf("ValidateParsedResponse() error = %v, want missing-citations error", err)
	}
}

func TestValidateParsedResponseAcceptsCitedCoreFields(t *testing.T) {
	candidate := validCandidate(time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC))
	err := ValidateParsedResponse(ParsedResponse{Candidate: candidate, Evidence: []FieldEvidence{
		{Field: "transaction_kind", SourcePath: "text.kind", Confidence: 0.95},
		{Field: "title", SourcePath: "subject", Confidence: 1},
		{Field: "merchant_name", SourcePath: "text.merchant", Confidence: 0.95},
		{Field: "original_amount_minor", SourcePath: "text.amount", Confidence: 0.95},
		{Field: "original_currency", SourcePath: "text.currency", Confidence: 0.95},
		{Field: "occurred_at", SourcePath: "sender.date", Confidence: 0.9},
	}})
	if err != nil {
		t.Fatalf("ValidateParsedResponse() error = %v", err)
	}
}

func TestValidateParsedResponseRejectsUnknownEvidenceFieldAndPath(t *testing.T) {
	candidate := validCandidate(time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC))
	valid := []FieldEvidence{
		{Field: "transaction_kind", SourcePath: "text.kind", Confidence: .9}, {Field: "title", SourcePath: "subject", Confidence: .9},
		{Field: "merchant_name", SourcePath: "text.merchant", Confidence: .9}, {Field: "original_amount_minor", SourcePath: "text.amount", Confidence: .9},
		{Field: "original_currency", SourcePath: "text.currency", Confidence: .9}, {Field: "occurred_at", SourcePath: "sender.date", Confidence: .9},
	}
	if err := ValidateParsedResponse(ParsedResponse{Candidate: candidate, Evidence: append(valid, FieldEvidence{Field: "made_up", SourcePath: "text.x", Confidence: 1})}); err == nil || !strings.Contains(err.Error(), "unknown evidence field") {
		t.Fatalf("unknown field error = %v", err)
	}
	if err := ValidateParsedResponse(ParsedResponse{Candidate: candidate, Evidence: append(valid, FieldEvidence{Field: "references", SourcePath: "headers.message_id", Confidence: 1})}); err == nil || !strings.Contains(err.Error(), "invalid source path") {
		t.Fatalf("path error = %v", err)
	}
}

func TestDiscardInvalidOptionalCategoryCitationIsCategoryOnly(t *testing.T) {
	base := func() ParsedResponse {
		candidate := validCandidate(time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC))
		candidate.CategoryLeafName = "Groceries"
		return ParsedResponse{Candidate: candidate, Evidence: []FieldEvidence{
			{Field: "transaction_kind", SourcePath: "text.kind", Confidence: .9},
			{Field: "title", SourcePath: "subject", Confidence: .9},
			{Field: "merchant_name", SourcePath: "text.merchant", Confidence: .9},
			{Field: "original_amount_minor", SourcePath: "text.amount", Confidence: .9},
			{Field: "original_currency", SourcePath: "text.currency", Confidence: .9},
			{Field: "occurred_at", SourcePath: "received_at", Confidence: .9},
		}}
	}

	invalidCategory := base()
	invalidCategory.Evidence = append(invalidCategory.Evidence, FieldEvidence{
		Field: "category_leaf_name", SourcePath: "merchant_name", Confidence: .9,
	})
	if !DiscardInvalidOptionalCategoryCitation(&invalidCategory) {
		t.Fatal("invalid optional category was not discarded")
	}
	if invalidCategory.Candidate.CategoryLeafName != "" {
		t.Fatalf("category = %q, want empty", invalidCategory.Candidate.CategoryLeafName)
	}
	for _, evidence := range invalidCategory.Evidence {
		if evidence.Field == "category_leaf_name" {
			t.Fatalf("category evidence remained: %#v", evidence)
		}
	}
	if err := ValidateParsedResponseAfterRule(invalidCategory); err != nil {
		t.Fatalf("recovered response did not validate: %v", err)
	}
	missingCategoryCitation := base()
	if !DiscardInvalidOptionalCategoryCitation(&missingCategoryCitation) || missingCategoryCitation.Candidate.CategoryLeafName != "" {
		t.Fatalf("uncited category was not discarded: %#v", missingCategoryCitation)
	}

	validCategory := base()
	validCategory.Evidence = append(validCategory.Evidence, FieldEvidence{
		Field: "category_leaf_name", SourcePath: "text.category", Confidence: .9,
	})
	if DiscardInvalidOptionalCategoryCitation(&validCategory) || validCategory.Candidate.CategoryLeafName != "Groceries" {
		t.Fatalf("valid category was discarded: %#v", validCategory)
	}

	for _, field := range []string{"original_currency", "merchant_name"} {
		t.Run("invalid non-category path "+field, func(t *testing.T) {
			invalidOtherField := base()
			invalidOtherField.Evidence = append(invalidOtherField.Evidence, FieldEvidence{
				Field: "category_leaf_name", SourcePath: "merchant_name", Confidence: .9,
			})
			for index := range invalidOtherField.Evidence {
				if invalidOtherField.Evidence[index].Field == field {
					invalidOtherField.Evidence[index].SourcePath = field
				}
			}
			DiscardInvalidOptionalCategoryCitation(&invalidOtherField)
			if err := ValidateEvidenceEntries(invalidOtherField); err == nil || !strings.Contains(err.Error(), field+" evidence has an invalid source path") {
				t.Fatalf("non-category citation was not rejected: %v", err)
			}
		})
	}
}

func TestEvidenceSourcePathUsesExactSourceInputGrammar(t *testing.T) {
	for _, path := range []string{"subject", "sender.address", "text.payment_method", "attachment[0].ocr.line[3]", "received_at"} {
		if !validEvidenceSourcePath(path, false) {
			t.Fatalf("valid source path %q was rejected", path)
		}
	}
	for _, path := range []string{"merchant_name", "category_leaf_name", "FairPrice", "Coffee Shops", "text.", "attachment[receipt]"} {
		if validEvidenceSourcePath(path, false) {
			t.Fatalf("invalid source path %q was accepted", path)
		}
	}
}

func TestAggregateConfidenceUsesMinimumRecognizedCitation(t *testing.T) {
	confidence := AggregateConfidence([]FieldEvidence{
		{Field: "title", SourcePath: "subject", Confidence: .4},
		{Field: "transaction_kind", SourcePath: "text.kind", Confidence: .95},
		{Field: "unknown", SourcePath: "text.noise", Confidence: 1},
	})
	if confidence != .4 {
		t.Fatalf("AggregateConfidence() = %v, want .4", confidence)
	}
}

func TestIsISO4217UsesCurrencyRegistry(t *testing.T) {
	for _, code := range []string{"SGD", "JPY", "KWD"} {
		if !IsISO4217(code) {
			t.Fatalf("IsISO4217(%q) = false", code)
		}
	}
	for _, code := range []string{"ZZZ", "usd", "USDD"} {
		if IsISO4217(code) {
			t.Fatalf("IsISO4217(%q) = true", code)
		}
	}
}

func TestDecodeParsedResponseRejectsUnknownFields(t *testing.T) {
	raw := []byte(`{
  "candidate": {
    "user_id": "user-1",
    "transaction_kind": "debit",
    "title": "DigitalOcean payment",
    "merchant_name": "DigitalOcean",
    "original_amount_minor": 648,
    "original_currency": "USD",
    "occurred_at": "2026-09-02T12:00:00Z",
    "confidence": 0.9,
    "invented_field": "reject me"
  },
  "evidence": []
}`)
	if _, err := DecodeParsedResponse(raw); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("DecodeParsedResponse() error = %v, want unknown-field error", err)
	}
}

func validCandidate(occurredAt time.Time) Candidate {
	return Candidate{
		UserID:              "user-1",
		Kind:                KindDebit,
		Title:               "DigitalOcean payment",
		MerchantName:        "DigitalOcean",
		OriginalAmountMinor: 648,
		OriginalCurrency:    "USD",
		OccurredAt:          occurredAt,
		Confidence:          0.9,
		AutoEligible:        true,
	}
}

func accountIdentity(id, userID, keyType, normalized string) AccountIdentity {
	return AccountIdentity{ID: id, UserID: userID, MatchingKeys: []AccountMatchingKey{{KeyType: keyType, NormalizedValue: normalized}}}
}

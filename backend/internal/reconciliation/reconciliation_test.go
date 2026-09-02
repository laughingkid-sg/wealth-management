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

	decision, err := Reconcile(candidate, []AccountIdentity{{ID: "account-1", UserID: "user-1", CardLastFour: "4242"}}, []Transaction{{
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

	decision, err := Reconcile(candidate, []AccountIdentity{{ID: "account-1", UserID: "user-1", CardLastFour: "4242"}}, []Transaction{{
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

	decision, err := Reconcile(candidate, []AccountIdentity{{ID: "account-1", UserID: "user-1", BankAccountReference: "XXXX1818"}}, nil)
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

	decision, err := Reconcile(candidate, []AccountIdentity{{ID: "account-1", UserID: "user-1", CardLastFour: "4242"}}, []Transaction{{
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
	if decision.Outcome != OutcomeReview || decision.Score.Total() != 65 {
		t.Fatalf("Reconcile() = %#v, want low-confidence review with only account and time signals", decision)
	}
}

func TestReconcileReturnsReviewForAmbiguousAccountEvidence(t *testing.T) {
	candidate := validCandidate(time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC))
	candidate.AccountEvidence.CardLastFour = "4242"

	decision, err := Reconcile(candidate, []AccountIdentity{
		{ID: "account-1", UserID: "user-1", CardLastFour: "4242"},
		{ID: "account-2", UserID: "user-1", MetadataIdentifiers: []string{"Card 4242"}},
	}, nil)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if decision.Outcome != OutcomeReview || !strings.Contains(decision.Reason, "more than one") {
		t.Fatalf("Reconcile() = %#v, want ambiguous-account review", decision)
	}
}

func TestReconcileRejectsCrossUserTransaction(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	candidate := validCandidate(now)
	candidate.AccountEvidence.CardLastFour = "4242"

	decision, err := Reconcile(candidate, []AccountIdentity{{ID: "account-1", UserID: "user-1", CardLastFour: "4242"}}, []Transaction{{
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
	err := ValidateParsedResponse(ParsedResponse{Candidate: candidate, Evidence: []FieldEvidence{{Field: "title", SourcePath: "payload.subject", Confidence: 1}}})
	if err == nil || !strings.Contains(err.Error(), "missing required field evidence") {
		t.Fatalf("ValidateParsedResponse() error = %v, want missing-citations error", err)
	}
}

func TestValidateParsedResponseAcceptsCitedCoreFields(t *testing.T) {
	candidate := validCandidate(time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC))
	err := ValidateParsedResponse(ParsedResponse{Candidate: candidate, Evidence: []FieldEvidence{
		{Field: "transaction_kind", SourcePath: "body.kind", Confidence: 0.95},
		{Field: "title", SourcePath: "subject", Confidence: 1},
		{Field: "original_amount", SourcePath: "body.amount", Confidence: 0.95},
		{Field: "original_currency", SourcePath: "body.currency", Confidence: 0.95},
		{Field: "occurred_at", SourcePath: "headers.date", Confidence: 0.9},
	}})
	if err != nil {
		t.Fatalf("ValidateParsedResponse() error = %v", err)
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
	}
}

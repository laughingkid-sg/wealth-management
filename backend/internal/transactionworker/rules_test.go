package transactionworker

import (
	"testing"
	"time"

	"github.com/zhengteck/wealth-builder/backend/internal/parserrules"
	"github.com/zhengteck/wealth-builder/backend/internal/reconciliation"
)

func TestApplyDeterministicRuleOverridesModelAndAddsEvidence(t *testing.T) {
	candidate := reconciliation.Candidate{Kind: reconciliation.KindCredit, Title: "guess", OriginalAmountMinor: 1, OriginalCurrency: "USD", OccurredAt: time.Now()}
	evidence := []reconciliation.FieldEvidence{{Field: "transaction_kind", SourcePath: "text.kind", Confidence: .2}}
	err := applyDeterministicRule(&candidate, &evidence, parserrules.AppliedRule{ID: "rule", Version: 1, Values: map[string][]string{"transaction_kind": {"debit"}, "original_amount_minor": {"648"}, "original_currency": {"sgd"}}})
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Kind != reconciliation.KindDebit || candidate.OriginalAmountMinor != 648 || candidate.OriginalCurrency != "SGD" {
		t.Fatalf("candidate = %#v", candidate)
	}
	if len(evidence) != 3 {
		t.Fatalf("evidence = %#v", evidence)
	}
	for _, item := range evidence {
		if item.SourcePath != "rule:rule:v1" {
			t.Fatalf("evidence = %#v", evidence)
		}
	}
}

func TestRuleEvidencePassesFinalValidation(t *testing.T) {
	candidate := reconciliation.Candidate{Kind: reconciliation.KindCredit, Title: "guess", MerchantName: "merchant", OriginalAmountMinor: 1, OriginalCurrency: "USD", OccurredAt: time.Now()}
	evidence := []reconciliation.FieldEvidence{{Field: "transaction_kind", SourcePath: "text.kind", Confidence: .2}, {Field: "title", SourcePath: "subject", Confidence: .8}, {Field: "merchant_name", SourcePath: "text.merchant", Confidence: .8}, {Field: "original_amount_minor", SourcePath: "text.amount", Confidence: .8}, {Field: "original_currency", SourcePath: "text.currency", Confidence: .8}, {Field: "occurred_at", SourcePath: "text.time", Confidence: .8}}
	err := applyDeterministicRule(&candidate, &evidence, parserrules.AppliedRule{ID: "rule-id", Version: 1, Values: map[string][]string{"transaction_kind": {"debit"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciliation.ValidateParsedResponseAfterRule(reconciliation.ParsedResponse{Candidate: candidate, Evidence: evidence}); err != nil {
		t.Fatalf("final rule validation = %v", err)
	}
}

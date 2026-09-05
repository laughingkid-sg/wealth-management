package reconciliation

import (
	"strings"
	"testing"
)

func TestScriptEvidenceSourcePathAcceptedOnlyAfterRule(t *testing.T) {
	const path = "script:transaction_post_process:v2"
	if validEvidenceSourcePath(path, false) {
		t.Fatalf("script source path %q must be rejected before rule/script application", path)
	}
	if !validEvidenceSourcePath(path, true) {
		t.Fatalf("script source path %q must be accepted after rule/script application", path)
	}
	// A malformed script path is rejected even after application.
	for _, bad := range []string{"script:", "script:key", "script:key:v0", "script:key:2"} {
		if validEvidenceSourcePath(bad, true) {
			t.Fatalf("malformed script source path %q was accepted", bad)
		}
	}
}

func TestDecodeParsedResponseBatchDecodesMultipleTransactions(t *testing.T) {
	raw := []byte(`{"transactions":[
      {"candidate":{"transaction_kind":"debit","title":"A","merchant_name":"A","original_amount_minor":100,"original_currency":"SGD","occurred_at":"2026-09-02T12:00:00Z"},"evidence":[]},
      {"candidate":{"transaction_kind":"credit","title":"B","merchant_name":"B","original_amount_minor":200,"original_currency":"SGD","occurred_at":"2026-09-02T12:05:00Z"},"evidence":[]}
    ]}`)
	batch, err := DecodeParsedResponseBatchForRuleApplication(raw)
	if err != nil {
		t.Fatalf("DecodeParsedResponseBatchForRuleApplication() error = %v", err)
	}
	if len(batch) != 2 {
		t.Fatalf("decoded %d transactions, want 2", len(batch))
	}
	if batch[0].Candidate.OriginalAmountMinor != 100 || batch[1].Candidate.Kind != KindCredit {
		t.Fatalf("decoded candidates were not preserved: %+v", batch)
	}
}

func TestDecodeParsedResponseBatchAcceptsEmptyArray(t *testing.T) {
	batch, err := DecodeParsedResponseBatchForRuleApplication([]byte(`{"transactions":[]}`))
	if err != nil {
		t.Fatalf("empty batch error = %v, want nil (benign no-transaction result)", err)
	}
	if len(batch) != 0 {
		t.Fatalf("empty batch decoded %d transactions, want 0", len(batch))
	}
}

func TestDecodeParsedResponseBatchRejectsUnknownAndTrailing(t *testing.T) {
	if _, err := DecodeParsedResponseBatchForRuleApplication([]byte(`{"transactions":[],"extra":1}`)); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field error = %v, want unknown-field error", err)
	}
	if _, err := DecodeParsedResponseBatchForRuleApplication([]byte(`{"transactions":[]}{"transactions":[]}`)); err == nil {
		t.Fatalf("multiple JSON values must be rejected")
	}
}

func TestDecodeParsedResponseBatchEnforcesTransactionCap(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"transactions":[`)
	for i := 0; i <= maxTransactionsPerSource; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"candidate":{"transaction_kind":"debit","title":"A","merchant_name":"A","original_amount_minor":100,"original_currency":"SGD","occurred_at":"2026-09-02T12:00:00Z"},"evidence":[]}`)
	}
	b.WriteString(`]}`)
	if _, err := DecodeParsedResponseBatchForRuleApplication([]byte(b.String())); err == nil || !strings.Contains(err.Error(), "maximum") {
		t.Fatalf("over-cap batch error = %v, want maximum-exceeded error", err)
	}
}

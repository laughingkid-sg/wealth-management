package transactionstore

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestNormalizedSanitizedHTMLTextPreservesFinanceEmailContent(t *testing.T) {
	got := normalizedSanitizedHTMLText(`<div>Payment <strong>received</strong></div><p>Amount: S$6.48</p>`)
	if got != "Payment received Amount: S$6.48" {
		t.Fatalf("normalizedSanitizedHTMLText() = %q", got)
	}
}

func TestNormalizedEmailContentFitsProviderLimitAtGmailBounds(t *testing.T) {
	content := normalizedEmailContent(
		strings.Repeat("s", 8*1024),
		strings.Repeat("f", 8*1024),
		strings.Repeat("界", (224*1024)/len("界")),
		time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC),
	)
	if len(content) > 256*1024 {
		t.Fatalf("normalized Gmail evidence bytes = %d, exceeds 256 KiB", len(content))
	}
	if !utf8.ValidString(content) {
		t.Fatal("normalized Gmail evidence is not valid UTF-8")
	}
}

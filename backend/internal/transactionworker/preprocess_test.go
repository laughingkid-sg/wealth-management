package transactionworker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/zhengteck/wealth-builder/backend/internal/scriptstore"
	"github.com/zhengteck/wealth-builder/backend/internal/transactionstore"
)

type fakeEngine struct {
	out json.RawMessage
	err error
}

func (f fakeEngine) Run(_ context.Context, _ string, _ json.RawMessage) (json.RawMessage, error) {
	return f.out, f.err
}

type fakeResolver struct {
	script scriptstore.ActiveScript
	err    error
}

func (f fakeResolver) LoadActiveScript(_ context.Context, _ string) (scriptstore.ActiveScript, error) {
	return f.script, f.err
}

func sampleParseInput() transactionstore.SourceParseInput {
	return transactionstore.SourceParseInput{
		Subject: "Receipt", Sender: "shop@example.com", Content: "You paid 12.34",
		ReceivedAt: time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC),
		NormalizedContent: "subject: Receipt\nsender: shop@example.com\ntext: You paid 12.34",
	}
}

func TestPreprocessInertWithoutEngineOrScript(t *testing.T) {
	in := sampleParseInput()

	// No engine/resolver configured.
	content, note := Handler{}.preprocessNormalizedContent(context.Background(), in)
	if content != in.NormalizedContent || note != "" {
		t.Fatalf("inert handler = (%q,%q), want original + empty note", content, note)
	}

	// Resolver reports no active script.
	h := Handler{Engine: fakeEngine{}, Scripts: fakeResolver{err: scriptstore.ErrNoActiveScript}}
	content, note = h.preprocessNormalizedContent(context.Background(), in)
	if content != in.NormalizedContent || note != "" {
		t.Fatalf("no-active-script = (%q,%q), want original + empty note", content, note)
	}
}

func TestPreprocessAppliesCleanedContent(t *testing.T) {
	in := sampleParseInput()
	h := Handler{
		Engine:  fakeEngine{out: json.RawMessage(`{"normalized_content":"CLEANED"}`)},
		Scripts: fakeResolver{script: scriptstore.ActiveScript{Key: "email_pre_process", Version: 3, Source: "x"}},
	}
	content, note := h.preprocessNormalizedContent(context.Background(), in)
	if content != "CLEANED" {
		t.Fatalf("content = %q, want CLEANED", content)
	}
	if note != "email_pre_process:v3" {
		t.Fatalf("note = %q, want email_pre_process:v3", note)
	}
}

func TestPreprocessFallsBackOnErrorsAndBadOutput(t *testing.T) {
	in := sampleParseInput()
	base := scriptstore.ActiveScript{Key: "email_pre_process", Version: 1, Source: "x"}
	cases := []struct {
		name string
		eng  fakeEngine
		res  fakeResolver
		want string
	}{
		{"run error", fakeEngine{err: errors.New("boom")}, fakeResolver{script: base}, "fallback:run_error"},
		{"load error", fakeEngine{}, fakeResolver{err: errors.New("db down")}, "fallback:load_error"},
		{"unknown field", fakeEngine{out: json.RawMessage(`{"normalized_content":"x","extra":1}`)}, fakeResolver{script: base}, "fallback:invalid_output"},
		{"empty content", fakeEngine{out: json.RawMessage(`{"normalized_content":"  "}`)}, fakeResolver{script: base}, "fallback:invalid_output"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := Handler{Engine: tc.eng, Scripts: tc.res}
			content, note := h.preprocessNormalizedContent(context.Background(), in)
			if content != in.NormalizedContent {
				t.Fatalf("content = %q, want original (fallback)", content)
			}
			if note != tc.want {
				t.Fatalf("note = %q, want %q", note, tc.want)
			}
		})
	}
}

package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestAlibabaQwenClientSendsBoundedMultimodalJSONRequest(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer secret" || r.Header.Get("Content-Type") != "application/json" {
			t.Fatal("missing authentication or JSON content type")
		}
		var request struct {
			Model          string `json:"model"`
			EnableThinking bool   `json:"enable_thinking"`
			ResponseFormat struct {
				Type string `json:"type"`
			} `json:"response_format"`
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBytes)).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Model != qwenFlashModel || request.EnableThinking || request.ResponseFormat.Type != "json_object" {
			t.Fatalf("unsafe parse request: %#v", request)
		}
		if len(request.Messages) != 2 || request.Messages[0].Role != "system" || request.Messages[1].Role != "user" {
			t.Fatalf("unexpected messages %#v", request.Messages)
		}
		var systemPrompt string
		if err := json.Unmarshal(request.Messages[0].Content, &systemPrompt); err != nil || systemPrompt != parserSystemPrompt {
			t.Fatal("request did not send the strict parser system prompt")
		}
		var parts []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			ImageURL struct {
				URL string `json:"url"`
			} `json:"image_url"`
		}
		if err := json.Unmarshal(request.Messages[1].Content, &parts); err != nil {
			t.Fatal(err)
		}
		if len(parts) != 2 || parts[0].Type != "text" || parts[0].Text != "subject: receipt" {
			t.Fatalf("unexpected text part %#v", parts)
		}
		if parts[1].Type != "image_url" || parts[1].ImageURL.URL != "data:image/png;base64,AQI=" {
			t.Fatalf("unexpected image part %#v", parts[1])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]string{"content": " {\n  \"candidate\": {}\n } "},
			}},
		})
	}))
	defer server.Close()

	client := newTestAlibabaClient(t, server)
	result, err := client.ParseTransactionEvidence(context.Background(), "subject: receipt", []AttachmentInput{{
		Filename: "receipt.png", MIMEType: "image/png; charset=binary", Content: []byte{1, 2},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(result.JSON), " {\n  \"candidate\": {}\n } "; got != want {
		t.Fatalf("raw JSON changed: got %q want %q", got, want)
	}
	if result.Model != qwenFlashModel {
		t.Fatalf("model = %q", result.Model)
	}
}

func TestParserSystemPromptDefinesStrictNestedJSONContract(t *testing.T) {
	required := []string{
		"no extra keys at any level",
		`"account_evidence": {`,
		"account_evidence is always an object, never a string or null",
		`"line_items": [`,
		`"schema_version": 1`,
		`"details": {}`,
		"unquoted base-10 integer minor units",
		"occurred_at is an RFC3339 string",
		"references is [] when absent",
		"line_items is [] unless",
		"Evidence objects contain exactly field, source_path, and confidence",
		"Do not add note, reason, rationale, amount_minor",
	}
	for _, snippet := range required {
		if !strings.Contains(parserSystemPrompt, snippet) {
			t.Fatalf("parser prompt is missing strict contract clause %q", snippet)
		}
	}
}

func TestAlibabaQwenClientRejectsUnsafeInputsBeforeNetwork(t *testing.T) {
	client, err := NewAlibabaQwenClient(&http.Client{}, mustURL(t, "https://example.test/v1"), "key", qwenFlashModel)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name        string
		evidence    string
		attachments []AttachmentInput
	}{
		{name: "blank evidence", evidence: " \t"},
		{name: "oversized evidence", evidence: strings.Repeat("a", maxEvidenceBytes+1)},
		{name: "too many attachments", evidence: "receipt", attachments: make([]AttachmentInput, maxAttachments+1)},
		{name: "empty attachment", evidence: "receipt", attachments: []AttachmentInput{{MIMEType: "image/png"}}},
		{name: "unsupported attachment", evidence: "receipt", attachments: []AttachmentInput{{MIMEType: "application/pdf", Content: []byte("pdf")}}},
		{name: "invalid MIME type", evidence: "receipt", attachments: []AttachmentInput{{MIMEType: "not a mime type", Content: []byte("image")}}},
		{name: "oversized attachment", evidence: "receipt", attachments: []AttachmentInput{{MIMEType: "image/jpeg", Content: make([]byte, maxAttachmentBytes+1)}}},
		{name: "over total attachment limit", evidence: "receipt", attachments: []AttachmentInput{
			{MIMEType: "image/jpeg", Content: make([]byte, maxTotalAttachmentBytes/2+1)},
			{MIMEType: "image/png", Content: make([]byte, maxTotalAttachmentBytes/2+1)},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := client.ParseTransactionEvidence(context.Background(), test.evidence, test.attachments); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestAlibabaQwenClientRejectsUntrustedResponsesWithoutLeakingBody(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "provider error", status: http.StatusBadRequest, body: `{"error":{"message":"secret provider detail"}}`},
		{name: "invalid JSON envelope", status: http.StatusOK, body: `{`},
		{name: "zero choices", status: http.StatusOK, body: `{"choices":[]}`},
		{name: "non JSON model content", status: http.StatusOK, body: `{"choices":[{"message":{"content":"not JSON"}}]}`},
		{name: "array model content", status: http.StatusOK, body: `{"choices":[{"message":{"content":"[]"}}]}`},
		{name: "multiple choices", status: http.StatusOK, body: `{"choices":[{"message":{"content":"{}"}},{"message":{"content":"{}"}}]}`},
		{name: "oversized body", status: http.StatusOK, body: strings.Repeat("x", maxResponseBytes+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			client := newTestAlibabaClient(t, server)
			_, err := client.ParseTransactionEvidence(context.Background(), "receipt", nil)
			if err == nil {
				t.Fatal("expected rejection")
			}
			if strings.Contains(err.Error(), "secret provider detail") {
				t.Fatalf("provider response leaked in error %q", err)
			}
		})
	}
}

func TestAlibabaQwenClientHonorsCallerContext(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	client := newTestAlibabaClient(t, server)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.ParseTransactionEvidence(ctx, "receipt", nil); err == nil {
		t.Fatal("expected cancelled request")
	}
}

func TestNewAlibabaQwenClientValidationAndTimeout(t *testing.T) {
	server := httptest.NewTLSServer(http.NotFoundHandler())
	defer server.Close()
	tests := []struct {
		name  string
		http  *http.Client
		base  *url.URL
		key   string
		model string
	}{
		{name: "nil HTTP client", base: mustURL(t, server.URL), key: "key", model: qwenFlashModel},
		{name: "HTTP base URL", http: &http.Client{}, base: mustURL(t, "http://example.test"), key: "key", model: qwenFlashModel},
		{name: "blank API key", http: &http.Client{}, base: mustURL(t, server.URL), model: qwenFlashModel},
		{name: "wrong model", http: &http.Client{}, base: mustURL(t, server.URL), key: "key", model: "other"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewAlibabaQwenClient(test.http, test.base, test.key, test.model); err == nil {
				t.Fatal("expected constructor rejection")
			}
		})
	}
	client, err := NewAlibabaQwenClient(server.Client(), mustURL(t, server.URL+"/v1"), "key", qwenFlashModel)
	if err != nil {
		t.Fatal(err)
	}
	if client.httpClient.Timeout != qwenRequestTimeout {
		t.Fatalf("timeout = %v, want %v", client.httpClient.Timeout, qwenRequestTimeout)
	}
}

func newTestAlibabaClient(t *testing.T, server *httptest.Server) *AlibabaQwenClient {
	t.Helper()
	client, err := NewAlibabaQwenClient(server.Client(), mustURL(t, server.URL+"/v1"), "secret", qwenFlashModel)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func mustURL(t *testing.T, value string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

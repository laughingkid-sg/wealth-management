package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestAlibabaQwenClientDisablesThinkingAndRequestsJSON(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("unexpected request %s authorization %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		var request chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Model != qwenFlashModel || request.EnableThinking || request.ResponseFormat.Type != "json_object" {
			t.Fatalf("unsafe parse request: %#v", request)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{\"candidate\":{}}"}}]}`))
	}))
	defer server.Close()
	base, err := url.Parse(server.URL + "/v1")
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewAlibabaQwenClient(server.Client(), base, "secret", qwenFlashModel)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.ParseTransactionEvidence(context.Background(), "subject: receipt", nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(result.JSON) != `{"candidate":{}}` || result.Model != qwenFlashModel {
		t.Fatalf("unexpected result %#v", result)
	}
}

func TestAlibabaQwenClientRejectsAttachments(t *testing.T) {
	base, _ := url.Parse("https://example.test/v1")
	client, err := NewAlibabaQwenClient(&http.Client{}, base, "key", qwenFlashModel)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ParseTransactionEvidence(context.Background(), "text", []AttachmentInput{{Filename: "receipt.pdf"}}); err == nil {
		t.Fatal("expected attachment rejection")
	}
}

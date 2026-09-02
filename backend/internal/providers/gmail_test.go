package providers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestLocalDevelopmentTokenSourceAllowsOnlyDevelopment(t *testing.T) {
	if _, err := NewLocalDevelopmentTokenSource("production", "token"); err == nil {
		t.Fatal("production token source unexpectedly allowed")
	}
	source, err := NewLocalDevelopmentTokenSource("development", "token")
	if err != nil {
		t.Fatalf("NewLocalDevelopmentTokenSource() error = %v", err)
	}
	if got, ok := source.DevelopmentRefreshToken(); !ok || got != "token" {
		t.Fatalf("DevelopmentRefreshToken() = %q, %t", got, ok)
	}
}

func TestGoogleOAuthClientExchangesRefreshTokenWithoutLeakingIt(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/token" {
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		form, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatal(err)
		}
		if got := form.Get("grant_type"); got != "refresh_token" {
			t.Fatalf("grant_type = %q", got)
		}
		if got := form.Get("refresh_token"); got != "refresh-secret" {
			t.Fatalf("refresh_token = %q", got)
		}
		if got := form.Get("client_id"); got != "client-id" {
			t.Fatalf("client_id = %q", got)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"access_token":"short-lived","token_type":"Bearer","expires_in":3600}`))
	}))
	defer server.Close()

	client, err := newGoogleOAuthClient(server.Client(), server.URL+"/token", "client-id", "client-secret")
	if err != nil {
		t.Fatalf("newGoogleOAuthClient() error = %v", err)
	}
	token, err := client.ExchangeRefreshToken(context.Background(), "refresh-secret")
	if err != nil {
		t.Fatalf("ExchangeRefreshToken() error = %v", err)
	}
	if token.Value != "short-lived" || token.TokenType != "Bearer" || token.ExpiresAt.Before(time.Now()) {
		t.Fatalf("unexpected token result: %#v", token)
	}
}

func TestGmailHTTPClientListsLabelMessages(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assertGmailAuthorization(t, request)
		switch request.URL.Path {
		case "/gmail/v1/users/me/labels":
			writeJSON(t, writer, map[string]any{"labels": []map[string]string{{"id": "Label_42", "name": "odin-finance"}}})
		case "/gmail/v1/users/me/messages":
			if got := request.URL.Query().Get("labelIds"); got != "Label_42" {
				t.Fatalf("labelIds = %q", got)
			}
			if got := request.URL.Query().Get("pageToken"); got != "cursor" {
				t.Fatalf("pageToken = %q", got)
			}
			writeJSON(t, writer, map[string]any{"messages": []map[string]string{{"id": "one", "threadId": "thread-one"}}, "nextPageToken": "next"})
		case "/gmail/v1/users/me/messages/one":
			if got := request.URL.Query().Get("format"); got != "metadata" {
				t.Fatalf("format = %q", got)
			}
			writeJSON(t, writer, map[string]string{"threadId": "thread-one", "internalDate": "1720000000123"})
		default:
			t.Fatalf("unexpected Gmail path %q", request.URL.Path)
		}
	}))
	defer server.Close()

	client, err := newGmailHTTPClient(server.Client(), server.URL+"/gmail/v1")
	if err != nil {
		t.Fatalf("newGmailHTTPClient() error = %v", err)
	}
	refs, next, err := client.ListLabelMessages(context.Background(), "access-secret", "odin-finance", "cursor", 5)
	if err != nil {
		t.Fatalf("ListLabelMessages() error = %v", err)
	}
	if next != "next" || len(refs) != 1 || refs[0].ID != "one" || refs[0].ThreadID != "thread-one" {
		t.Fatalf("unexpected listed refs: %#v, next=%q", refs, next)
	}
	if got := refs[0].ReceivedAt; !got.Equal(time.UnixMilli(1720000000123).UTC()) {
		t.Fatalf("ReceivedAt = %v", got)
	}
}

func TestGmailHTTPClientGetsNormalizedMessageAndEligibleAttachments(t *testing.T) {
	encoded := func(value string) string { return base64.RawURLEncoding.EncodeToString([]byte(value)) }
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assertGmailAuthorization(t, request)
		switch request.URL.Path {
		case "/gmail/v1/users/me/messages/message-1":
			switch request.URL.Query().Get("format") {
			case "raw":
				writeJSON(t, writer, map[string]string{"id": "message-1", "threadId": "thread-1", "internalDate": "1720000000123", "raw": encoded("Subject: Receipt\r\n\r\nraw body")})
			case "full":
				writeJSON(t, writer, map[string]any{
					"id": "message-1", "threadId": "thread-1", "internalDate": "1720000000123",
					"payload": map[string]any{
						"mimeType": "multipart/mixed",
						"headers":  []map[string]string{{"name": "Subject", "value": "  DigitalOcean receipt "}, {"name": "From", "value": "billing@example.test"}},
						"parts": []any{
							map[string]any{"mimeType": "text/plain; charset=UTF-8", "body": map[string]any{"data": encoded("Paid\r\nS$6.48")}},
							map[string]any{"mimeType": "text/html", "body": map[string]any{"data": encoded("<p>Paid</p>\r\n")}},
							map[string]any{"mimeType": "application/pdf", "filename": "invoice.pdf", "body": map[string]any{"attachmentId": "pdf-1", "size": 12}},
							map[string]any{"mimeType": "application/pdf", "filename": "too-large.pdf", "body": map[string]any{"attachmentId": "large", "size": maxGmailAttachmentBytes + 1}},
							map[string]any{"mimeType": "text/plain", "filename": "notes.txt", "body": map[string]any{"attachmentId": "text", "size": 10}},
						},
					},
				})
			default:
				t.Fatalf("unexpected message format %q", request.URL.Query().Get("format"))
			}
		case "/gmail/v1/users/me/messages/message-1/attachments/pdf-1":
			writeJSON(t, writer, map[string]string{"data": encoded("%PDF-1.7\n...")})
		case "/gmail/v1/users/me/messages/message-1/attachments/large", "/gmail/v1/users/me/messages/message-1/attachments/text":
			t.Fatalf("ineligible attachment was fetched: %s", request.URL.Path)
		default:
			t.Fatalf("unexpected Gmail path %q", request.URL.Path)
		}
	}))
	defer server.Close()

	client, err := newGmailHTTPClient(server.Client(), server.URL+"/gmail/v1")
	if err != nil {
		t.Fatalf("newGmailHTTPClient() error = %v", err)
	}
	message, err := client.GetMessage(context.Background(), "access-secret", "message-1")
	if err != nil {
		t.Fatalf("GetMessage() error = %v", err)
	}
	if message.RawMIME != "Subject: Receipt\r\n\r\nraw body" || message.Text != "Paid\nS$6.48" || message.HTML != "<p>Paid</p>" {
		t.Fatalf("unexpected normalized message: %#v", message)
	}
	if got := message.Headers["subject"]; got != "DigitalOcean receipt" {
		t.Fatalf("subject header = %q", got)
	}
	if len(message.Attachments) != 1 || message.Attachments[0].Filename != "invoice.pdf" || string(message.Attachments[0].Content) != "%PDF-1.7\n..." {
		t.Fatalf("unexpected attachments: %#v", message.Attachments)
	}
}

func TestGmailHTTPClientRejectsEmptyAccessToken(t *testing.T) {
	client, err := newGmailHTTPClient(&http.Client{}, "https://gmail.example.test/gmail/v1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.ListLabelMessages(context.Background(), "", "odin-finance", "", 5); err == nil || strings.Contains(err.Error(), "access-secret") {
		t.Fatalf("expected safe empty-token error, got %v", err)
	}
}

func assertGmailAuthorization(t *testing.T, request *http.Request) {
	t.Helper()
	if got := request.Header.Get("Authorization"); got != "Bearer access-secret" {
		t.Fatalf("Authorization = %q", got)
	}
}

func writeJSON(t *testing.T, writer http.ResponseWriter, value any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Fatal(err)
	}
}

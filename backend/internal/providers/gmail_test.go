package providers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
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
	profileCaptured := false
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assertGmailAuthorization(t, request)
		switch request.URL.Path {
		case "/gmail/v1/users/me/labels":
			writeJSON(t, writer, map[string]any{"labels": []map[string]string{{"id": "Label_42", "name": "odin-finance"}}})
		case "/gmail/v1/users/me/messages":
			if !profileCaptured {
				t.Fatal("initial message list ran before capturing history cursor")
			}
			if got := request.URL.Query().Get("labelIds"); got != "Label_42" {
				t.Fatalf("labelIds = %q", got)
			}
			if got := request.URL.Query().Get("pageToken"); got != "" {
				t.Fatalf("initial sync used a page token: %q", got)
			}
			writeJSON(t, writer, map[string]any{"messages": []map[string]string{{"id": "one", "threadId": "thread-one"}}})
		case "/gmail/v1/users/me/messages/one":
			if got := request.URL.Query().Get("format"); got != "metadata" {
				t.Fatalf("format = %q", got)
			}
			writeJSON(t, writer, map[string]string{"threadId": "thread-one", "internalDate": "1720000000123"})
		case "/gmail/v1/users/me/profile":
			profileCaptured = true
			writeJSON(t, writer, map[string]string{"historyId": "900"})
		default:
			t.Fatalf("unexpected Gmail path %q", request.URL.Path)
		}
	}))
	defer server.Close()

	client, err := newGmailHTTPClient(server.Client(), server.URL+"/gmail/v1")
	if err != nil {
		t.Fatalf("newGmailHTTPClient() error = %v", err)
	}
	refs, next, err := client.ListLabelMessages(context.Background(), "access-secret", "odin-finance", "", 5)
	if err != nil {
		t.Fatalf("ListLabelMessages() error = %v", err)
	}
	if next != "history:900" || len(refs) != 1 || refs[0].ID != "one" || refs[0].ThreadID != "thread-one" {
		t.Fatalf("unexpected listed refs: %#v, next=%q", refs, next)
	}
	if got := refs[0].ReceivedAt; !got.Equal(time.UnixMilli(1720000000123).UTC()) {
		t.Fatalf("ReceivedAt = %v", got)
	}
}

func TestGmailHTTPClientUsesHistoryCursorAndDeduplicatesAddedMessages(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assertGmailAuthorization(t, request)
		switch request.URL.Path {
		case "/gmail/v1/users/me/labels":
			writeJSON(t, writer, map[string]any{"labels": []map[string]string{{"id": "Label_42", "name": "odin-finance"}}})
		case "/gmail/v1/users/me/history":
			if request.URL.Query().Get("startHistoryId") != "900" || request.URL.Query().Get("historyTypes") != "" {
				t.Fatalf("unexpected history query: %s", request.URL.RawQuery)
			}
			writeJSON(t, writer, map[string]any{"historyId": "901", "history": []any{map[string]any{"messagesAdded": []any{
				map[string]any{"message": map[string]string{"id": "one", "threadId": "t1"}},
				map[string]any{"message": map[string]string{"id": "one", "threadId": "t1"}},
				map[string]any{"message": map[string]string{"id": "two", "threadId": "t2"}},
			}, "labelsAdded": []any{
				map[string]any{"message": map[string]string{"id": "two", "threadId": "t2"}, "labelIds": []string{"Label_42"}},
				map[string]any{"message": map[string]string{"id": "three", "threadId": "t3"}, "labelIds": []string{"Label_42"}},
				map[string]any{"message": map[string]string{"id": "ignored", "threadId": "t4"}, "labelIds": []string{"other"}},
			}}}})
		case "/gmail/v1/users/me/messages/one", "/gmail/v1/users/me/messages/two", "/gmail/v1/users/me/messages/three":
			writeJSON(t, writer, map[string]string{"internalDate": "1720000000123"})
		default:
			t.Fatalf("unexpected Gmail path %q", request.URL.Path)
		}
	}))
	defer server.Close()
	client, err := newGmailHTTPClient(server.Client(), server.URL+"/gmail/v1")
	if err != nil {
		t.Fatal(err)
	}
	refs, next, err := client.ListLabelMessages(context.Background(), "access-secret", "odin-finance", "history:900", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 3 || next != "history:901" {
		t.Fatalf("history result = %#v, %q", refs, next)
	}
}

func TestGmailHTTPClientFallsBackWhenHistoryCursorExpired(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assertGmailAuthorization(t, request)
		switch request.URL.Path {
		case "/gmail/v1/users/me/labels":
			writeJSON(t, writer, map[string]any{"labels": []map[string]string{{"id": "Label_42", "name": "odin-finance"}}})
		case "/gmail/v1/users/me/history":
			writer.WriteHeader(http.StatusNotFound)
		case "/gmail/v1/users/me/messages":
			if request.URL.Query().Get("pageToken") != "" {
				t.Fatal("legacy/history cursor leaked into fallback")
			}
			writeJSON(t, writer, map[string]any{"messages": []map[string]string{{"id": "one", "threadId": "thread-one"}}})
		case "/gmail/v1/users/me/messages/one":
			writeJSON(t, writer, map[string]string{"threadId": "thread-one", "internalDate": "1720000000123"})
		case "/gmail/v1/users/me/profile":
			writeJSON(t, writer, map[string]string{"historyId": "901"})
		default:
			t.Fatalf("unexpected Gmail path %q", request.URL.Path)
		}
	}))
	defer server.Close()
	client, err := newGmailHTTPClient(server.Client(), server.URL+"/gmail/v1")
	if err != nil {
		t.Fatal(err)
	}
	refs, next, err := client.ListLabelMessages(context.Background(), "access-secret", "odin-finance", "history:expired", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || next != "history:901" {
		t.Fatalf("fallback result = %#v, %q", refs, next)
	}
}

func TestGmailHTTPClientRecoversAllLabelMessagesAfterExpiredHistory(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assertGmailAuthorization(t, request)
		switch request.URL.Path {
		case "/gmail/v1/users/me/labels":
			writeJSON(t, writer, map[string]any{"labels": []map[string]string{{"id": "Label_42", "name": "odin-finance"}}})
		case "/gmail/v1/users/me/history":
			writer.WriteHeader(http.StatusNotFound)
		case "/gmail/v1/users/me/profile":
			writeJSON(t, writer, map[string]string{"historyId": "999"})
		case "/gmail/v1/users/me/messages":
			if request.URL.Query().Get("maxResults") != "100" {
				t.Fatalf("recovery maxResults=%q", request.URL.Query().Get("maxResults"))
			}
			messages := make([]map[string]string, 0, 6)
			for i := 1; i <= 6; i++ {
				messages = append(messages, map[string]string{"id": "m" + strconv.Itoa(i)})
			}
			writeJSON(t, writer, map[string]any{"messages": messages})
		default:
			if strings.HasPrefix(request.URL.Path, "/gmail/v1/users/me/messages/m") {
				writeJSON(t, writer, map[string]string{"internalDate": "1720000000123"})
				return
			}
			t.Fatalf("unexpected path %q", request.URL.Path)
		}
	}))
	defer server.Close()
	client, err := newGmailHTTPClient(server.Client(), server.URL+"/gmail/v1")
	if err != nil {
		t.Fatal(err)
	}
	refs, next, err := client.ListLabelMessages(context.Background(), "access-secret", "odin-finance", "history:gone", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 6 || next != "history:999" {
		t.Fatalf("recovery = %d, %q", len(refs), next)
	}
}

func TestGmailHTTPClientRecoversFullLabelForLegacyCursor(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assertGmailAuthorization(t, request)
		switch request.URL.Path {
		case "/gmail/v1/users/me/labels":
			writeJSON(t, writer, map[string]any{"labels": []map[string]string{{"id": "Label_42", "name": "odin-finance"}}})
		case "/gmail/v1/users/me/profile":
			writeJSON(t, writer, map[string]string{"historyId": "1000"})
		case "/gmail/v1/users/me/messages":
			if got := request.URL.Query().Get("maxResults"); got != "100" {
				t.Fatalf("legacy recovery maxResults = %q, want 100", got)
			}
			if got := request.URL.Query().Get("pageToken"); got != "" {
				t.Fatalf("legacy cursor was sent as Gmail page token: %q", got)
			}
			messages := make([]map[string]string, 0, 6)
			for i := 1; i <= 6; i++ {
				messages = append(messages, map[string]string{"id": "legacy-" + strconv.Itoa(i)})
			}
			writeJSON(t, writer, map[string]any{"messages": messages})
		default:
			if strings.HasPrefix(request.URL.Path, "/gmail/v1/users/me/messages/legacy-") {
				writeJSON(t, writer, map[string]string{"internalDate": "1720000000123"})
				return
			}
			t.Fatalf("unexpected path %q", request.URL.Path)
		}
	}))
	defer server.Close()
	client, err := newGmailHTTPClient(server.Client(), server.URL+"/gmail/v1")
	if err != nil {
		t.Fatal(err)
	}
	refs, next, err := client.ListLabelMessages(context.Background(), "access-secret", "odin-finance", "legacy-page-token", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 6 || next != "history:1000" {
		t.Fatalf("legacy recovery = %d refs, next %q", len(refs), next)
	}
}

func TestGmailHTTPClientSkipsVanishedMetadataDuringInitialAndRecovery(t *testing.T) {
	for _, mode := range []string{"initial", "recovery"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				assertGmailAuthorization(t, request)
				switch request.URL.Path {
				case "/gmail/v1/users/me/profile":
					writeJSON(t, writer, map[string]string{"historyId": "1001"})
				case "/gmail/v1/users/me/messages":
					writeJSON(t, writer, map[string]any{"messages": []map[string]string{{"id": "gone"}, {"id": "present"}}})
				case "/gmail/v1/users/me/messages/gone":
					writer.WriteHeader(http.StatusNotFound)
				case "/gmail/v1/users/me/messages/present":
					writeJSON(t, writer, map[string]string{"internalDate": "1720000000123"})
				default:
					t.Fatalf("unexpected path %q", request.URL.Path)
				}
			}))
			defer server.Close()
			client, err := newGmailHTTPClient(server.Client(), server.URL+"/gmail/v1")
			if err != nil {
				t.Fatal(err)
			}
			var refs []GmailMessageRef
			var next string
			if mode == "initial" {
				refs, next, err = client.initialLabelMessages(context.Background(), "access-secret", "Label_42", 5)
			} else {
				refs, next, err = client.recoverLabelMessages(context.Background(), "access-secret", "Label_42")
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(refs) != 1 || refs[0].ID != "present" || next != "history:1001" {
				t.Fatalf("refs = %#v, next = %q", refs, next)
			}
		})
	}
}

func TestGmailHTTPClientFailsRecoveryOnNonNotFoundMetadataError(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assertGmailAuthorization(t, request)
		switch request.URL.Path {
		case "/gmail/v1/users/me/profile":
			writeJSON(t, writer, map[string]string{"historyId": "1002"})
		case "/gmail/v1/users/me/messages":
			writeJSON(t, writer, map[string]any{"messages": []map[string]string{{"id": "unavailable"}}})
		case "/gmail/v1/users/me/messages/unavailable":
			writer.WriteHeader(http.StatusInternalServerError)
		default:
			t.Fatalf("unexpected path %q", request.URL.Path)
		}
	}))
	defer server.Close()
	client, err := newGmailHTTPClient(server.Client(), server.URL+"/gmail/v1")
	if err != nil {
		t.Fatal(err)
	}
	refs, next, err := client.recoverLabelMessages(context.Background(), "access-secret", "Label_42")
	if err == nil || refs != nil || next != "" {
		t.Fatalf("recovery = %#v, %q, %v; want no refs/cursor and an error", refs, next, err)
	}
}

func TestGmailHTTPClientGetsNormalizedMessageAndEligibleAttachments(t *testing.T) {
	encoded := func(value string) string { return base64.RawURLEncoding.EncodeToString([]byte(value)) }
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assertGmailAuthorization(t, request)
		switch request.URL.Path {
		case "/gmail/v1/users/me/messages/message-1":
			switch request.URL.Query().Get("format") {
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
	if message.Text != "Paid\nS$6.48" || message.HTML != "<p>Paid</p>" {
		t.Fatalf("unexpected normalized message: %#v", message)
	}
	if message.BodyTruncated {
		t.Fatal("ordinary Gmail message was marked truncated")
	}
	if got := message.Headers["subject"]; got != "DigitalOcean receipt" {
		t.Fatalf("subject header = %q", got)
	}
	if len(message.Attachments) != 1 || message.Attachments[0].Filename != "invoice.pdf" || string(message.Attachments[0].Content) != "%PDF-1.7\n..." {
		t.Fatalf("unexpected attachments: %#v", message.Attachments)
	}
}

func TestGmailHTTPClientBoundsCumulativeDecodedBodyWithoutSplittingUTF8(t *testing.T) {
	encoded := func(value string) string { return base64.RawURLEncoding.EncodeToString([]byte(value)) }
	plain := strings.Repeat("a", maxGmailBodyBytes-4)
	html := "x界tail"
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assertGmailAuthorization(t, request)
		writeJSON(t, writer, map[string]any{
			"id": "large-message", "internalDate": "1720000000123",
			"payload": map[string]any{
				"mimeType": "multipart/alternative",
				"headers": []map[string]string{
					{"name": "Subject", "value": strings.Repeat("s", maxGmailHeaderValueBytes+100)},
					{"name": "From", "value": "billing@example.test"},
				},
				"parts": []any{
					map[string]any{"mimeType": "text/plain", "body": map[string]any{"data": encoded(plain)}},
					map[string]any{"mimeType": "text/html", "body": map[string]any{"data": encoded(html)}},
				},
			},
		})
	}))
	defer server.Close()

	client, err := newGmailHTTPClient(server.Client(), server.URL+"/gmail/v1")
	if err != nil {
		t.Fatal(err)
	}
	message, err := client.GetMessage(context.Background(), "access-secret", "large-message")
	if err != nil {
		t.Fatal(err)
	}
	if !message.BodyTruncated {
		t.Fatal("oversized cumulative body was not marked truncated")
	}
	if len(message.Text)+len(message.HTML) != maxGmailBodyBytes {
		t.Fatalf("bounded body bytes = %d, want %d", len(message.Text)+len(message.HTML), maxGmailBodyBytes)
	}
	if message.HTML != "x界" || !utf8.ValidString(message.Text) || !utf8.ValidString(message.HTML) {
		t.Fatalf("body was not truncated on a UTF-8 boundary: html=%q", message.HTML)
	}
	if len(message.Headers["subject"]) != maxGmailHeaderValueBytes {
		t.Fatalf("subject bytes = %d, want %d", len(message.Headers["subject"]), maxGmailHeaderValueBytes)
	}
}

func TestGmailHTTPClientClassifiesOnlyVanishedMessages(t *testing.T) {
	for _, testCase := range []struct {
		name            string
		messageID       string
		wantUnavailable bool
	}{
		{name: "full message", messageID: "gone", wantUnavailable: true},
		{name: "attachment", messageID: "attachment-gone", wantUnavailable: true},
		{name: "other provider error", messageID: "temporary", wantUnavailable: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				assertGmailAuthorization(t, request)
				switch request.URL.Path {
				case "/gmail/v1/users/me/messages/gone":
					writer.WriteHeader(http.StatusNotFound)
				case "/gmail/v1/users/me/messages/temporary":
					writer.WriteHeader(http.StatusBadGateway)
				case "/gmail/v1/users/me/messages/attachment-gone":
					writeJSON(t, writer, map[string]any{
						"id": "attachment-gone", "internalDate": "1720000000123",
						"payload": map[string]any{"mimeType": "multipart/mixed", "parts": []any{
							map[string]any{"mimeType": "application/pdf", "filename": "invoice.pdf", "body": map[string]any{"attachmentId": "gone-attachment", "size": 12}},
						}},
					})
				case "/gmail/v1/users/me/messages/attachment-gone/attachments/gone-attachment":
					writer.WriteHeader(http.StatusNotFound)
				default:
					t.Fatalf("unexpected path %q", request.URL.Path)
				}
			}))
			defer server.Close()
			client, err := newGmailHTTPClient(server.Client(), server.URL+"/gmail/v1")
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.GetMessage(context.Background(), "access-secret", testCase.messageID)
			if err == nil || errors.Is(err, ErrGmailMessageUnavailable) != testCase.wantUnavailable {
				t.Fatalf("GetMessage() error = %v, unavailable = %t", err, errors.Is(err, ErrGmailMessageUnavailable))
			}
		})
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

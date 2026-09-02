package ingestion

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/zhengteck/wealth-builder/backend/internal/jobs"
	"github.com/zhengteck/wealth-builder/backend/internal/providers"
	"github.com/zhengteck/wealth-builder/backend/internal/secret"
	"github.com/zhengteck/wealth-builder/backend/internal/transactionstore"
)

type repositoryStub struct {
	source   transactionstore.IngestedSource
	complete bool
}

func (s *repositoryStub) GetGmailConnection(context.Context, uuid.UUID) (transactionstore.GmailConnection, error) {
	return transactionstore.GmailConnection{}, transactionstore.ErrGmailConnectionRequired
}
func (s *repositoryStub) StoreIngestedSource(_ context.Context, source transactionstore.IngestedSource) (uuid.UUID, bool, error) {
	s.source = source
	return uuid.New(), true, nil
}
func (s *repositoryStub) StartSyncRun(context.Context, uuid.UUID, uuid.UUID) error { return nil }
func (s *repositoryStub) CompleteSyncRun(context.Context, uuid.UUID, uuid.UUID, int, int) error {
	s.complete = true
	return nil
}
func (s *repositoryStub) RecordSyncFailure(context.Context, uuid.UUID, uuid.UUID, bool) error {
	return nil
}
func (s *repositoryStub) UpdateConnectionCursor(context.Context, uuid.UUID, string) error { return nil }

type tokenStub struct{}

func (tokenStub) ExchangeRefreshToken(context.Context, string) (providers.OAuthAccessToken, error) {
	return providers.OAuthAccessToken{Value: "access"}, nil
}

type gmailStub struct{}

func (gmailStub) ListLabelMessages(context.Context, string, string, string, int) ([]providers.GmailMessageRef, string, error) {
	return []providers.GmailMessageRef{{ID: "message"}}, "", nil
}
func (gmailStub) GetMessage(context.Context, string, string) (providers.GmailMessage, error) {
	return providers.GmailMessage{ID: "message", ReceivedAt: time.Now(), HTML: `<p onclick="x()">Paid</p><img src="https://tracker.test">`, Text: "Paid", Headers: map[string]string{"subject": "Receipt", "from": "billing@example.test"}, Attachments: []providers.GmailAttachment{{ID: "a", Filename: "invoice.pdf", MIMEType: "application/pdf", Size: 3, Content: []byte("pdf")}}}, nil
}

func TestGmailIngestionStoresSanitizedIdempotentSourceShape(t *testing.T) {
	cipher, err := secret.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	store := &repositoryStub{}
	runID := uuid.New()
	payload, _ := json.Marshal(map[string]string{"sync_run_id": runID.String()})
	handler := GmailIngestionHandler{Repository: store, Gmail: gmailStub{}, Tokens: tokenStub{}, Cipher: cipher, Label: "odin-finance", InitialBackfillMax: 5, DevelopmentRefreshToken: "refresh"}
	if err := handler.Handle(context.Background(), jobs.Job{Kind: jobs.KindGmailIngest, UserID: uuid.New(), Payload: payload, Attempts: 1}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if !store.complete {
		t.Fatal("sync run was not completed")
	}
	var raw map[string]any
	if err := json.Unmarshal(store.source.RawData, &raw); err != nil {
		t.Fatal(err)
	}
	if html := raw["html_sanitized"].(string); html != "<p>Paid</p>" {
		t.Fatalf("html_sanitized = %q", html)
	}
	attachments := raw["attachments"].([]any)
	if attachments[0].(map[string]any)["parse_eligible"] != true {
		t.Fatalf("attachment metadata = %#v", attachments)
	}
}

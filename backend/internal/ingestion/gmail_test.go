package ingestion

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/zhengteck/wealth-builder/backend/internal/attachmentstorage"
	"github.com/zhengteck/wealth-builder/backend/internal/jobs"
	"github.com/zhengteck/wealth-builder/backend/internal/providers"
	"github.com/zhengteck/wealth-builder/backend/internal/secret"
	"github.com/zhengteck/wealth-builder/backend/internal/transactionstore"
)

type repositoryStub struct {
	source   transactionstore.IngestedSource
	complete bool
	sourceID uuid.UUID
}

func (s *repositoryStub) GetGmailConnection(context.Context, uuid.UUID) (transactionstore.GmailConnection, error) {
	return transactionstore.GmailConnection{}, transactionstore.ErrGmailConnectionRequired
}
func (s *repositoryStub) StoreIngestedSource(_ context.Context, source transactionstore.IngestedSource) (uuid.UUID, bool, error) {
	s.source = source
	return s.sourceID, true, nil
}
func (s *repositoryStub) FindIngestedSourceID(context.Context, uuid.UUID, string) (uuid.UUID, error) {
	return s.sourceID, nil
}
func (s *repositoryStub) UpdateIngestedSourceRawData(_ context.Context, _ uuid.UUID, _ uuid.UUID, raw json.RawMessage) error {
	s.source.RawData = raw
	return nil
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

type attachmentUploaderStub struct{}

func (attachmentUploaderStub) Upload(_ context.Context, request attachmentstorage.UploadRequest) (attachmentstorage.UploadResult, error) {
	return attachmentstorage.UploadResult{ObjectPath: request.UserID.String() + "/" + request.SourceID.String() + "/stored.pdf", SHA256: "stored-checksum", ByteSize: int64(len(request.Content))}, nil
}

func (gmailStub) ListLabelMessages(context.Context, string, string, string, int) ([]providers.GmailMessageRef, string, error) {
	return []providers.GmailMessageRef{{ID: "message"}}, "", nil
}
func (gmailStub) GetMessage(context.Context, string, string) (providers.GmailMessage, error) {
	return providers.GmailMessage{ID: "message", ReceivedAt: time.Now(), HTML: `<p onclick="x()">Paid</p><img src="https://tracker.test">`, Text: "Paid", Headers: map[string]string{"subject": "Receipt", "from": "billing@example.test"}, Attachments: []providers.GmailAttachment{{ID: "a", Filename: "invoice.pdf", MIMEType: "application/pdf", Size: 3, Content: []byte("ATTACHMENT_BYTES_SHOULD_NEVER_APPEAR")}}}, nil
}

func TestGmailIngestionStoresSanitizedIdempotentSourceShape(t *testing.T) {
	cipher, err := secret.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	store := &repositoryStub{sourceID: uuid.New()}
	runID := uuid.New()
	payload, _ := json.Marshal(map[string]string{"sync_run_id": runID.String()})
	handler := GmailIngestionHandler{Repository: store, Gmail: gmailStub{}, Tokens: tokenStub{}, Cipher: cipher, Attachments: attachmentUploaderStub{}, Label: "odin-finance", InitialBackfillMax: 5, DevelopmentRefreshToken: "refresh"}
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
	if strings.Contains(string(store.source.RawData), "ATTACHMENT_BYTES_SHOULD_NEVER_APPEAR") {
		t.Fatal("attachment bytes were persisted in source JSON")
	}
	if html := raw["html_sanitized"].(string); html != "<p>Paid</p>" {
		t.Fatalf("html_sanitized = %q", html)
	}
	attachments := raw["attachments"].([]any)
	if attachments[0].(map[string]any)["parse_eligible"] != true {
		t.Fatalf("attachment metadata = %#v", attachments)
	}
	attachment := attachments[0].(map[string]any)
	if attachment["storage_status"] != "stored" || attachment["object_path"] == nil || attachment["sha256"] != "stored-checksum" {
		t.Fatalf("stored attachment metadata = %#v", attachment)
	}
}

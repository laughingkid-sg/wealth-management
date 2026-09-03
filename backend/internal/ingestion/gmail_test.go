package ingestion

import (
	"context"
	"encoding/json"
	"errors"
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
	source     transactionstore.IngestedSource
	storeErr   error
	complete   bool
	sourceID   uuid.UUID
	enqueued   bool
	cursor     string
	failures   int
	connection *transactionstore.GmailConnection
}

func (s *repositoryStub) GetGmailConnection(context.Context, uuid.UUID) (transactionstore.GmailConnection, error) {
	if s.connection != nil {
		return *s.connection, nil
	}
	return transactionstore.GmailConnection{}, transactionstore.ErrGmailConnectionRequired
}
func (s *repositoryStub) StoreIngestedSource(_ context.Context, source transactionstore.IngestedSource) (uuid.UUID, bool, error) {
	s.source = source
	return s.sourceID, true, s.storeErr
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
	s.failures++
	return nil
}
func (s *repositoryStub) UpdateConnectionCursor(_ context.Context, _ uuid.UUID, cursor string) error {
	s.cursor = cursor
	return nil
}
func (s *repositoryStub) EnqueueSourceParse(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error {
	s.enqueued = true
	return nil
}

type tokenStub struct{}

func (tokenStub) ExchangeRefreshToken(context.Context, string) (providers.OAuthAccessToken, error) {
	return providers.OAuthAccessToken{Value: "access"}, nil
}

type gmailStub struct{}

type messageErrorGmailStub struct{ err error }

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

func (messageErrorGmailStub) ListLabelMessages(context.Context, string, string, string, int) ([]providers.GmailMessageRef, string, error) {
	return []providers.GmailMessageRef{{ID: "vanished"}}, "history:next", nil
}
func (s messageErrorGmailStub) GetMessage(context.Context, string, string) (providers.GmailMessage, error) {
	return providers.GmailMessage{}, s.err
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
	if !store.enqueued {
		t.Fatal("new source parse was not queued after attachment persistence")
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
	if html := raw["html_raw"].(string); html != `<p onclick="x()">Paid</p><img src="https://tracker.test">` {
		t.Fatalf("html_raw = %q", html)
	}
	if truncated, ok := raw["body_truncated"].(bool); !ok || truncated {
		t.Fatalf("ordinary body_truncated = %#v", raw["body_truncated"])
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

func TestGmailIngestionDoesNotRecreatePermanentlyDeletedProviderMessage(t *testing.T) {
	cipher, err := secret.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	store := &repositoryStub{storeErr: transactionstore.ErrSourcePermanentlyDeleted}
	runID := uuid.New()
	payload, _ := json.Marshal(map[string]string{"sync_run_id": runID.String()})
	handler := GmailIngestionHandler{
		Repository: store, Gmail: gmailStub{}, Tokens: tokenStub{}, Cipher: cipher,
		Attachments: attachmentUploaderStub{}, Label: "odin-finance", InitialBackfillMax: 5,
		DevelopmentRefreshToken: "refresh",
	}
	if err = handler.Handle(context.Background(), jobs.Job{
		Kind: jobs.KindGmailIngest, UserID: uuid.New(), Payload: payload, Attempts: 1,
	}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if !store.complete || store.enqueued || store.failures != 0 {
		t.Fatalf("complete=%t enqueued=%t failures=%d", store.complete, store.enqueued, store.failures)
	}
}

func TestMarshalRawDataPersistsBodyTruncationMarker(t *testing.T) {
	rawData, err := marshalRawData(providers.GmailMessage{
		ID: "message", Headers: map[string]string{}, BodyTruncated: true,
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err = json.Unmarshal(rawData, &raw); err != nil {
		t.Fatal(err)
	}
	if truncated, ok := raw["body_truncated"].(bool); !ok || !truncated {
		t.Fatalf("body_truncated = %#v", raw["body_truncated"])
	}
}

func TestGmailIngestionSkipsOnlyVanishedMessagesAndCommitsCursor(t *testing.T) {
	cipher, err := secret.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	userID, runID := uuid.New(), uuid.New()
	encrypted, err := cipher.Encrypt([]byte("refresh"), gmailTokenAssociatedData(userID))
	if err != nil {
		t.Fatal(err)
	}
	store := &repositoryStub{sourceID: uuid.New(), connection: &transactionstore.GmailConnection{EncryptedRefreshToken: encrypted}}
	payload, _ := json.Marshal(map[string]string{"sync_run_id": runID.String()})
	handler := GmailIngestionHandler{Repository: store, Gmail: messageErrorGmailStub{err: providers.ErrGmailMessageUnavailable}, Tokens: tokenStub{}, Cipher: cipher, Attachments: attachmentUploaderStub{}, Label: "odin-finance", InitialBackfillMax: 5}
	if err := handler.Handle(context.Background(), jobs.Job{Kind: jobs.KindGmailIngest, UserID: userID, Payload: payload, Attempts: 1}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if !store.complete || store.cursor != "history:next" || store.failures != 0 {
		t.Fatalf("complete=%t cursor=%q failures=%d", store.complete, store.cursor, store.failures)
	}
}

func TestGmailIngestionRetainsCursorOnOtherGetMessageFailures(t *testing.T) {
	cipher, err := secret.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	userID, runID := uuid.New(), uuid.New()
	encrypted, err := cipher.Encrypt([]byte("refresh"), gmailTokenAssociatedData(userID))
	if err != nil {
		t.Fatal(err)
	}
	store := &repositoryStub{sourceID: uuid.New(), connection: &transactionstore.GmailConnection{EncryptedRefreshToken: encrypted}}
	payload, _ := json.Marshal(map[string]string{"sync_run_id": runID.String()})
	handler := GmailIngestionHandler{Repository: store, Gmail: messageErrorGmailStub{err: errors.New("temporary Gmail failure")}, Tokens: tokenStub{}, Cipher: cipher, Attachments: attachmentUploaderStub{}, Label: "odin-finance", InitialBackfillMax: 5}
	if err := handler.Handle(context.Background(), jobs.Job{Kind: jobs.KindGmailIngest, UserID: userID, Payload: payload, Attempts: 1}); err == nil {
		t.Fatal("Handle() error = nil, want retryable provider error")
	}
	if store.complete || store.cursor != "" || store.failures != 1 {
		t.Fatalf("complete=%t cursor=%q failures=%d", store.complete, store.cursor, store.failures)
	}
}

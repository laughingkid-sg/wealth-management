// Package ingestion executes the Gmail ingestion job without holding database locks during provider calls.
package ingestion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/zhengteck/wealth-builder/backend/internal/emailcontent"
	"github.com/zhengteck/wealth-builder/backend/internal/jobs"
	"github.com/zhengteck/wealth-builder/backend/internal/providers"
	"github.com/zhengteck/wealth-builder/backend/internal/secret"
	"github.com/zhengteck/wealth-builder/backend/internal/transactionstore"
)

const maxJobAttempts = 5

type Repository interface {
	GetGmailConnection(context.Context, uuid.UUID) (transactionstore.GmailConnection, error)
	StoreIngestedSource(context.Context, transactionstore.IngestedSource) (uuid.UUID, bool, error)
	StartSyncRun(context.Context, uuid.UUID, uuid.UUID) error
	CompleteSyncRun(context.Context, uuid.UUID, uuid.UUID, int, int) error
	RecordSyncFailure(context.Context, uuid.UUID, uuid.UUID, bool) error
	UpdateConnectionCursor(context.Context, uuid.UUID, string) error
}

type TokenExchanger interface {
	ExchangeRefreshToken(context.Context, string) (providers.OAuthAccessToken, error)
}

type GmailIngestionHandler struct {
	Repository              Repository
	Gmail                   providers.GmailClient
	Tokens                  TokenExchanger
	Cipher                  *secret.Cipher
	Label                   string
	InitialBackfillMax      int
	DevelopmentRefreshToken string
}

func (h GmailIngestionHandler) Handle(ctx context.Context, job jobs.Job) error {
	if job.Kind != jobs.KindGmailIngest {
		return fmt.Errorf("unsupported job kind %q", job.Kind)
	}
	if h.Repository == nil || h.Gmail == nil || h.Tokens == nil || h.Cipher == nil {
		return errors.New("Gmail ingestion handler is not configured")
	}
	var payload struct {
		SyncRunID string `json:"sync_run_id"`
	}
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return fmt.Errorf("decode Gmail ingestion payload: %w", err)
	}
	runID, err := uuid.Parse(payload.SyncRunID)
	if err != nil {
		return errors.New("Gmail ingestion job has invalid sync run ID")
	}
	if err := h.Repository.StartSyncRun(ctx, job.UserID, runID); err != nil {
		return fmt.Errorf("start sync run: %w", err)
	}

	accessToken, cursor, hasConnection, err := h.accessToken(ctx, job.UserID)
	if err != nil {
		return h.fail(ctx, job, runID, err)
	}
	refs, nextCursor, err := h.Gmail.ListLabelMessages(ctx, accessToken, h.Label, cursor, h.InitialBackfillMax)
	if err != nil {
		return h.fail(ctx, job, runID, err)
	}

	saved := 0
	for _, ref := range refs {
		message, err := h.Gmail.GetMessage(ctx, accessToken, ref.ID)
		if err != nil {
			return h.fail(ctx, job, runID, err)
		}
		rawData, err := marshalRawData(message)
		if err != nil {
			return h.fail(ctx, job, runID, err)
		}
		_, inserted, err := h.Repository.StoreIngestedSource(ctx, transactionstore.IngestedSource{
			UserID: job.UserID, ProviderMessageID: message.ID, ProviderThreadID: message.ThreadID,
			ReceivedAt: message.ReceivedAt, RawData: rawData,
		})
		if err != nil {
			return h.fail(ctx, job, runID, err)
		}
		if inserted {
			saved++
		}
	}
	if hasConnection && nextCursor != "" {
		if err := h.Repository.UpdateConnectionCursor(ctx, job.UserID, nextCursor); err != nil {
			return h.fail(ctx, job, runID, err)
		}
	}
	if err := h.Repository.CompleteSyncRun(ctx, job.UserID, runID, len(refs), saved); err != nil {
		return fmt.Errorf("complete sync run: %w", err)
	}
	return nil
}

func (h GmailIngestionHandler) accessToken(ctx context.Context, userID uuid.UUID) (string, string, bool, error) {
	connection, err := h.Repository.GetGmailConnection(ctx, userID)
	if err == nil {
		refreshToken, err := h.Cipher.Decrypt(connection.EncryptedRefreshToken, gmailTokenAssociatedData(userID))
		if err != nil {
			return "", "", false, errors.New("stored Gmail connection cannot be decrypted")
		}
		token, err := h.Tokens.ExchangeRefreshToken(ctx, string(refreshToken))
		if err != nil {
			return "", "", false, err
		}
		return token.Value, valueOrEmpty(connection.SyncCursor), true, nil
	}
	if !errors.Is(err, transactionstore.ErrGmailConnectionRequired) {
		return "", "", false, err
	}
	if strings.TrimSpace(h.DevelopmentRefreshToken) == "" {
		return "", "", false, transactionstore.ErrGmailConnectionRequired
	}
	token, err := h.Tokens.ExchangeRefreshToken(ctx, h.DevelopmentRefreshToken)
	if err != nil {
		return "", "", false, err
	}
	return token.Value, "", false, nil
}

func (h GmailIngestionHandler) fail(ctx context.Context, job jobs.Job, runID uuid.UUID, cause error) error {
	if recordErr := h.Repository.RecordSyncFailure(ctx, job.UserID, runID, job.Attempts >= maxJobAttempts); recordErr != nil {
		return fmt.Errorf("record Gmail ingestion failure: %w", recordErr)
	}
	return fmt.Errorf("Gmail ingestion failed: %w", cause)
}

func gmailTokenAssociatedData(userID uuid.UUID) []byte {
	return []byte("gmail-refresh-token:" + userID.String())
}
func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func marshalRawData(message providers.GmailMessage) (json.RawMessage, error) {
	attachments := make([]map[string]any, 0, len(message.Attachments))
	for _, attachment := range message.Attachments {
		digest := sha256.Sum256(attachment.Content)
		attachments = append(attachments, map[string]any{
			"provider_attachment_id": attachment.ID, "filename": attachment.Filename,
			"mime_type": attachment.MIMEType, "byte_size": attachment.Size,
			"sha256":         hex.EncodeToString(digest[:]),
			"parse_eligible": attachmentFilenameEligible(attachment.Filename),
			"storage_status": "deferred",
		})
	}
	data := map[string]any{
		"provider_message_id": message.ID, "provider_thread_id": message.ThreadID,
		"headers": message.Headers, "subject": message.Headers["subject"], "sender": message.Headers["from"],
		"raw_mime": message.RawMIME, "html_sanitized": emailcontent.SanitizeHTML(message.HTML),
		"text": message.Text, "attachments": attachments,
	}
	return json.Marshal(data)
}

func attachmentFilenameEligible(filename string) bool {
	lower := strings.ToLower(filename)
	return strings.Contains(lower, "receipt") || strings.Contains(lower, "invoice")
}

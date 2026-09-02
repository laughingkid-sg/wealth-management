// Package attachmentstorage provides the server-only boundary for the private
// transaction attachment bucket. It never creates browser-accessible URLs.
package attachmentstorage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"
)

const (
	Bucket       = "transaction-attachments"
	MaxBytes int = 5 * 1024 * 1024
)

// UploadRequest contains bytes that Gmail has already selected as attachment
// evidence. Upload validates the bucket's content contract again at this
// privileged boundary.
type UploadRequest struct {
	UserID   uuid.UUID
	SourceID uuid.UUID
	MIMEType string
	Content  []byte
}

// UploadResult is safe to persist in private source metadata. It contains no
// signed URL, credential, or attachment bytes.
type UploadResult struct {
	ObjectPath string
	SHA256     string
	ByteSize   int64
}

// Uploader permits Gmail ingestion tests to exercise persistence without a
// live Storage service.
type Uploader interface {
	Upload(context.Context, UploadRequest) (UploadResult, error)
}

// Client calls Supabase Storage with a server-only service key. Do not use it
// from browser-facing code.
type Client struct {
	httpClient *http.Client
	baseURL    *url.URL
	serviceKey string
}

func New(httpClient *http.Client, supabaseURL *url.URL, serviceRoleKey string) (*Client, error) {
	if supabaseURL == nil || (supabaseURL.Scheme != "https" && supabaseURL.Scheme != "http") || supabaseURL.Host == "" {
		return nil, errors.New("Supabase Storage URL must be an absolute HTTP(S) URL")
	}
	if strings.TrimSpace(serviceRoleKey) == "" {
		return nil, errors.New("Supabase service-role key is required for attachment storage")
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	base := *supabaseURL
	base.RawQuery = ""
	base.Fragment = ""
	return &Client{httpClient: httpClient, baseURL: &base, serviceKey: serviceRoleKey}, nil
}

func (c *Client) Upload(ctx context.Context, request UploadRequest) (UploadResult, error) {
	if c == nil || c.httpClient == nil || c.baseURL == nil || strings.TrimSpace(c.serviceKey) == "" {
		return UploadResult{}, errors.New("attachment storage client is not configured")
	}
	if request.UserID == uuid.Nil || request.SourceID == uuid.Nil {
		return UploadResult{}, errors.New("attachment storage requires user and source IDs")
	}
	if len(request.Content) > MaxBytes {
		return UploadResult{}, fmt.Errorf("attachment exceeds %d-byte limit", MaxBytes)
	}
	extension, ok := extensionForMIMEType(request.MIMEType)
	if !ok {
		return UploadResult{}, fmt.Errorf("unsupported attachment MIME type %q", request.MIMEType)
	}

	digest := sha256.Sum256(request.Content)
	checksum := hex.EncodeToString(digest[:])
	objectPath := strings.Join([]string{request.UserID.String(), request.SourceID.String(), checksum + "." + extension}, "/")
	endpoint := c.objectURL(objectPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(request.Content))
	if err != nil {
		return UploadResult{}, fmt.Errorf("create attachment storage request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.serviceKey)
	req.Header.Set("apikey", c.serviceKey)
	req.Header.Set("Content-Type", normalizedMIMEType(request.MIMEType))
	// Deterministic paths make retries safe after a job lease or network error.
	req.Header.Set("x-upsert", "true")

	response, err := c.httpClient.Do(req)
	if err != nil {
		return UploadResult{}, fmt.Errorf("send attachment storage request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return UploadResult{}, fmt.Errorf("attachment storage returned status %d", response.StatusCode)
	}
	return UploadResult{ObjectPath: objectPath, SHA256: checksum, ByteSize: int64(len(request.Content))}, nil
}

func (c *Client) objectURL(objectPath string) string {
	base := *c.baseURL
	base.Path = strings.TrimRight(base.Path, "/") + "/storage/v1/object/" + Bucket + "/" + objectPath
	base.RawPath = ""
	return base.String()
}

func normalizedMIMEType(value string) string {
	return strings.ToLower(strings.TrimSpace(strings.Split(value, ";")[0]))
}

func extensionForMIMEType(value string) (string, bool) {
	switch normalizedMIMEType(value) {
	case "application/pdf":
		return "pdf", true
	case "image/bmp":
		return "bmp", true
	case "image/jpeg":
		return "jpg", true
	case "image/png":
		return "png", true
	case "image/tiff":
		return "tiff", true
	case "image/webp":
		return "webp", true
	case "image/heic":
		return "heic", true
	default:
		return "", false
	}
}

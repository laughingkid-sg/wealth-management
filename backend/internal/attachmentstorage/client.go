// Package attachmentstorage provides the server-only boundary for the private
// transaction attachment bucket. It never creates browser-accessible URLs.
package attachmentstorage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
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

type ObjectRequest struct {
	UserID     uuid.UUID
	SourceID   uuid.UUID
	ObjectPath string
}

// Delete removes all supplied owned-source objects in one Storage API call.
// A bulk call avoids committing a partially deleted attachment set client-side.
func (c *Client) Delete(ctx context.Context, requests []ObjectRequest) error {
	if len(requests) == 0 {
		return nil
	}
	if len(requests) > 1000 {
		return errors.New("too many attachment objects to delete")
	}
	paths := make([]string, 0, len(requests))
	for _, request := range requests {
		if err := c.validateObjectRequest(request); err != nil {
			return err
		}
		paths = append(paths, request.ObjectPath)
	}
	body, err := json.Marshal(map[string][]string{"prefixes": paths})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.deleteURL(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create attachment delete request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.serviceKey)
	req.Header.Set("apikey", c.serviceKey)
	req.Header.Set("Content-Type", "application/json")
	response, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send attachment delete request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return &HTTPStatusError{operation: "deletion", StatusCode: response.StatusCode}
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	return nil
}

// HTTPStatusError preserves only the safe HTTP status needed for operational
// classification. Provider response bodies and object paths are never retained.
type HTTPStatusError struct {
	operation  string
	StatusCode int
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("attachment %s returned status %d", e.operation, e.StatusCode)
}

func HTTPStatusCode(err error) (int, bool) {
	var statusError *HTTPStatusError
	if !errors.As(err, &statusError) {
		return 0, false
	}
	return statusError.StatusCode, true
}

// Download reads a private attachment only after its caller has performed ownership checks.
func (c *Client) Download(ctx context.Context, request ObjectRequest) ([]byte, error) {
	if err := c.validateObjectRequest(request); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.objectURL(request.ObjectPath), nil)
	if err != nil {
		return nil, fmt.Errorf("create attachment download request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.serviceKey)
	req.Header.Set("apikey", c.serviceKey)
	response, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send attachment download request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return nil, &HTTPStatusError{operation: "download", StatusCode: response.StatusCode}
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, int64(MaxBytes)+1))
	if err != nil {
		return nil, fmt.Errorf("read attachment download: %w", err)
	}
	if len(content) > MaxBytes {
		return nil, fmt.Errorf("attachment download exceeds %d-byte limit", MaxBytes)
	}
	return content, nil
}

// SignURL returns a short-lived private Storage URL. Authorization is still the caller's responsibility.
func (c *Client) SignURL(ctx context.Context, request ObjectRequest, expiresInSeconds int) (string, error) {
	if err := c.validateObjectRequest(request); err != nil {
		return "", err
	}
	if expiresInSeconds < 1 || expiresInSeconds > 300 {
		return "", errors.New("attachment URL expiry must be between 1 and 300 seconds")
	}
	body, err := json.Marshal(map[string]int{"expiresIn": expiresInSeconds})
	if err != nil {
		return "", err
	}
	endpoint := c.signURL(request.ObjectPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create attachment signing request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.serviceKey)
	req.Header.Set("apikey", c.serviceKey)
	req.Header.Set("Content-Type", "application/json")
	response, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("send attachment signing request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return "", &HTTPStatusError{operation: "signing", StatusCode: response.StatusCode}
	}
	var result struct {
		SignedURL string `json:"signedURL"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&result); err != nil || result.SignedURL == "" {
		return "", errors.New("attachment signing response is invalid")
	}
	return c.normalizeSignedURL(result.SignedURL)
}

func (c *Client) normalizeSignedURL(raw string) (string, error) {
	signed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || signed.Opaque != "" || signed.User != nil || signed.Fragment != "" {
		return "", errors.New("attachment signing response is invalid")
	}
	base := *c.baseURL
	base.RawQuery = ""
	base.Fragment = ""
	storagePrefix := strings.TrimRight(base.Path, "/") + "/storage/v1/"
	if !signed.IsAbs() {
		if signed.Host != "" || signed.Scheme != "" {
			return "", errors.New("attachment signing response is invalid")
		}
		relative := *signed
		relative.Path = strings.TrimLeft(relative.Path, "/")
		if strings.HasPrefix(relative.Path, "storage/v1/") {
			base.Path = strings.TrimSuffix(storagePrefix, "storage/v1/")
		} else {
			base.Path = storagePrefix
		}
		signed = base.ResolveReference(&relative)
	}
	expectedPathPrefix := storagePrefix + "object/sign/" + Bucket + "/"
	if signed.Scheme != c.baseURL.Scheme || !strings.EqualFold(signed.Host, c.baseURL.Host) ||
		!strings.HasPrefix(signed.Path, expectedPathPrefix) || signed.RawQuery == "" {
		return "", errors.New("attachment signing response is invalid")
	}
	return signed.String(), nil
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
	if len(request.Content) == 0 {
		return UploadResult{}, errors.New("attachment content is empty")
	}
	if !hasExpectedMagic(request.MIMEType, request.Content) {
		return UploadResult{}, fmt.Errorf("attachment content does not match MIME type %q", normalizedMIMEType(request.MIMEType))
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
		return UploadResult{}, &HTTPStatusError{operation: "upload", StatusCode: response.StatusCode}
	}
	return UploadResult{ObjectPath: objectPath, SHA256: checksum, ByteSize: int64(len(request.Content))}, nil
}

func (c *Client) objectURL(objectPath string) string {
	base := *c.baseURL
	base.Path = strings.TrimRight(base.Path, "/") + "/storage/v1/object/" + Bucket + "/" + objectPath
	base.RawPath = ""
	return base.String()
}

func (c *Client) signURL(objectPath string) string {
	base := *c.baseURL
	base.Path = strings.TrimRight(base.Path, "/") + "/storage/v1/object/sign/" + Bucket + "/" + objectPath
	base.RawPath = ""
	return base.String()
}

func (c *Client) deleteURL() string {
	base := *c.baseURL
	base.Path = strings.TrimRight(base.Path, "/") + "/storage/v1/object/" + Bucket
	base.RawPath = ""
	return base.String()
}

func (c *Client) validateObjectRequest(request ObjectRequest) error {
	if c == nil || c.httpClient == nil || c.baseURL == nil || strings.TrimSpace(c.serviceKey) == "" {
		return errors.New("attachment storage client is not configured")
	}
	return ValidateObjectRequest(request)
}

// ValidateObjectRequest enforces the owner/source prefix independently of a
// configured client. Durable cleanup workers use it before making any Storage
// request so a malformed outbox payload can never address another source.
func ValidateObjectRequest(request ObjectRequest) error {
	if request.UserID == uuid.Nil || request.SourceID == uuid.Nil {
		return errors.New("attachment storage requires user and source IDs")
	}
	prefix := request.UserID.String() + "/" + request.SourceID.String() + "/"
	if !strings.HasPrefix(request.ObjectPath, prefix) || len(request.ObjectPath) <= len(prefix) ||
		strings.Contains(request.ObjectPath, "..") || strings.ContainsAny(request.ObjectPath, "\r\n\x00") {
		return errors.New("attachment object path is outside the owned source")
	}
	return nil
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

func hasExpectedMagic(mimeType string, content []byte) bool {
	switch normalizedMIMEType(mimeType) {
	case "application/pdf":
		return bytes.HasPrefix(content, []byte("%PDF-"))
	case "image/bmp":
		return len(content) >= 2 && bytes.Equal(content[:2], []byte("BM"))
	case "image/jpeg":
		return len(content) >= 3 && content[0] == 0xff && content[1] == 0xd8 && content[2] == 0xff
	case "image/png":
		return len(content) >= 8 && bytes.Equal(content[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	case "image/tiff":
		return len(content) >= 4 && (bytes.Equal(content[:4], []byte{'I', 'I', 0x2a, 0x00}) ||
			bytes.Equal(content[:4], []byte{'M', 'M', 0x00, 0x2a}))
	case "image/webp":
		return len(content) >= 12 && bytes.Equal(content[:4], []byte("RIFF")) && bytes.Equal(content[8:12], []byte("WEBP"))
	case "image/heic":
		return hasHEICBrand(content)
	default:
		return false
	}
}

func hasHEICBrand(content []byte) bool {
	if len(content) < 16 || !bytes.Equal(content[4:8], []byte("ftyp")) {
		return false
	}
	boxSize := int(binary.BigEndian.Uint32(content[:4]))
	if boxSize < 16 || boxSize > len(content) {
		return false
	}
	if isHEICBrand(content[8:12]) {
		return true
	}
	// Bytes 12..15 are the ftyp minor version, not a compatible brand.
	for offset := 16; offset+4 <= boxSize; offset += 4 {
		if isHEICBrand(content[offset : offset+4]) {
			return true
		}
	}
	return false
}

func isHEICBrand(value []byte) bool {
	switch string(value) {
	case "heic", "heix", "hevc", "hevx", "heim", "heis":
		return true
	default:
		return false
	}
}

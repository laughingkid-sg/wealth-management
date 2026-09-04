package attachmentstorage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

type SignedObjectMetadata struct {
	ByteSize    int64
	ContentType string
	ETag        string
}

// CreateSignedUploadURL signs exactly one already-reserved owner/source path.
// The returned URL contains only the provider token, never the service role.
func (c *Client) CreateSignedUploadURL(ctx context.Context, request ObjectRequest) (string, error) {
	if err := c.validateObjectRequest(request); err != nil {
		return "", err
	}
	body := bytes.NewReader([]byte(`{}`))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.signedUploadURL(request.ObjectPath), body)
	if err != nil {
		return "", fmt.Errorf("create signed upload request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.serviceKey)
	req.Header.Set("apikey", c.serviceKey)
	req.Header.Set("Content-Type", "application/json")
	response, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("send signed upload request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return "", &HTTPStatusError{operation: "upload signing", StatusCode: response.StatusCode}
	}
	var result struct {
		URL       string `json:"url"`
		SignedURL string `json:"signedURL"`
		Token     string `json:"token"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 8192))
	if err := decoder.Decode(&result); err != nil {
		return "", errors.New("signed upload response is invalid")
	}
	raw := strings.TrimSpace(result.URL)
	if raw == "" {
		raw = strings.TrimSpace(result.SignedURL)
	}
	if raw == "" && strings.TrimSpace(result.Token) != "" {
		endpoint := c.signedUploadURL(request.ObjectPath)
		parsed, _ := url.Parse(endpoint)
		query := parsed.Query()
		query.Set("token", result.Token)
		parsed.RawQuery = query.Encode()
		raw = parsed.String()
	}
	return c.normalizeSignedUploadURL(raw, request.ObjectPath)
}

func (c *Client) StatObject(ctx context.Context, request ObjectRequest) (SignedObjectMetadata, error) {
	if err := c.validateObjectRequest(request); err != nil {
		return SignedObjectMetadata{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, c.objectURL(request.ObjectPath), nil)
	if err != nil {
		return SignedObjectMetadata{}, fmt.Errorf("create attachment metadata request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.serviceKey)
	req.Header.Set("apikey", c.serviceKey)
	response, err := c.httpClient.Do(req)
	if err != nil {
		return SignedObjectMetadata{}, fmt.Errorf("send attachment metadata request: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode != http.StatusOK {
		return SignedObjectMetadata{}, &HTTPStatusError{operation: "metadata", StatusCode: response.StatusCode}
	}
	byteSize := response.ContentLength
	if byteSize < 0 {
		if value := response.Header.Get("Content-Length"); value != "" {
			byteSize, err = strconv.ParseInt(value, 10, 64)
		}
	}
	if err != nil || byteSize < 0 || byteSize > int64(MaxBytes) {
		return SignedObjectMetadata{}, errors.New("attachment metadata size is invalid")
	}
	return SignedObjectMetadata{ByteSize: byteSize, ContentType: response.Header.Get("Content-Type"), ETag: response.Header.Get("ETag")}, nil
}

func (c *Client) signedUploadURL(objectPath string) string {
	base := *c.baseURL
	base.Path = strings.TrimRight(base.Path, "/") + "/storage/v1/object/upload/sign/" + Bucket + "/" + objectPath
	base.RawPath = ""
	return base.String()
}

func (c *Client) normalizeSignedUploadURL(raw, objectPath string) (string, error) {
	signed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || raw == "" || signed.Opaque != "" || signed.User != nil || signed.Fragment != "" {
		return "", errors.New("signed upload response is invalid")
	}
	base := *c.baseURL
	base.RawQuery = ""
	base.Fragment = ""
	storagePrefix := strings.TrimRight(base.Path, "/") + "/storage/v1/"
	if !signed.IsAbs() {
		if signed.Host != "" || signed.Scheme != "" {
			return "", errors.New("signed upload response is invalid")
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
	expected := storagePrefix + "object/upload/sign/" + Bucket + "/" + objectPath
	if signed.Scheme != c.baseURL.Scheme || !strings.EqualFold(signed.Host, c.baseURL.Host) || signed.Path != expected || signed.RawQuery == "" {
		return "", errors.New("signed upload response is invalid")
	}
	return signed.String(), nil
}

func BulkObjectRequest(userID, sourceScopeID uuid.UUID, objectPath string) ObjectRequest {
	return ObjectRequest{UserID: userID, SourceID: sourceScopeID, ObjectPath: objectPath}
}

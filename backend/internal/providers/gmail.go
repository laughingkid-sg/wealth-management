// Package providers contains provider boundaries. Implementations keep tokens server-side.
package providers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

const (
	GmailReadonlyScope         = "https://www.googleapis.com/auth/gmail.readonly"
	defaultGoogleTokenURL      = "https://oauth2.googleapis.com/token"
	defaultGmailAPIBaseURL     = "https://gmail.googleapis.com/gmail/v1"
	maxGmailAttachmentBytes    = 5 * 1024 * 1024
	defaultProviderHTTPTimeout = 20 * time.Second
)

type GmailMessageRef struct {
	ID, ThreadID string
	ReceivedAt   time.Time
}

type GmailMessage struct {
	ID, ThreadID, RawMIME, HTML, Text string
	ReceivedAt                        time.Time
	Attachments                       []GmailAttachment
	// Headers contains available message headers, keyed case-insensitively in
	// their canonical lower-case form (for example, "subject" and "from").
	// It is source evidence, not trusted transaction data.
	Headers map[string]string
}

type GmailAttachment struct {
	ID, Filename, MIMEType string
	Size                   int64
	Content                []byte
}

// GmailClient never exposes refresh tokens to callers outside server-side connection storage.
type GmailClient interface {
	ListLabelMessages(context.Context, string, string, string, int) ([]GmailMessageRef, string, error)
	GetMessage(context.Context, string, string) (GmailMessage, error)
}

// GmailConnectionTokenSource obtains a short-lived access token from an encrypted connection.
type GmailConnectionTokenSource interface {
	AccessToken(context.Context, string) (string, error)
}

// OAuthAccessToken is deliberately limited to the short-lived access token.
// Refresh tokens stay in encrypted connection storage and must not be returned
// from this provider boundary.
type OAuthAccessToken struct {
	Value     string
	TokenType string
	ExpiresAt time.Time
}

// OAuthCodeExchange is the server-only result of a Google authorization-code exchange.
// RefreshToken must be encrypted before persistence and never returned to a browser.
type OAuthCodeExchange struct {
	RefreshToken string
	Metadata     json.RawMessage
}

// GoogleOAuthClient exchanges a server-side refresh token for a short-lived
// Gmail access token. It does not persist or log either token.
type GoogleOAuthClient struct {
	httpClient   *http.Client
	tokenURL     string
	clientID     string
	clientSecret string
}

func NewGoogleOAuthClient(httpClient *http.Client, clientID, clientSecret string) (*GoogleOAuthClient, error) {
	return newGoogleOAuthClient(httpClient, defaultGoogleTokenURL, clientID, clientSecret)
}

func newGoogleOAuthClient(httpClient *http.Client, tokenURL, clientID, clientSecret string) (*GoogleOAuthClient, error) {
	if strings.TrimSpace(clientID) == "" || strings.TrimSpace(clientSecret) == "" {
		return nil, errors.New("Google OAuth client credentials are required")
	}
	parsed, err := url.Parse(tokenURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return nil, errors.New("Google OAuth token URL must be an absolute HTTPS URL")
	}
	return &GoogleOAuthClient{
		httpClient:   providerHTTPClient(httpClient),
		tokenURL:     parsed.String(),
		clientID:     clientID,
		clientSecret: clientSecret,
	}, nil
}

func (c *GoogleOAuthClient) ExchangeRefreshToken(ctx context.Context, refreshToken string) (OAuthAccessToken, error) {
	if c == nil {
		return OAuthAccessToken{}, errors.New("Google OAuth client is nil")
	}
	if strings.TrimSpace(refreshToken) == "" {
		return OAuthAccessToken{}, errors.New("Google OAuth refresh token is empty")
	}
	form := url.Values{
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return OAuthAccessToken{}, fmt.Errorf("create Google OAuth refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	response, err := c.httpClient.Do(req)
	if err != nil {
		return OAuthAccessToken{}, fmt.Errorf("send Google OAuth refresh request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return OAuthAccessToken{}, fmt.Errorf("Google OAuth refresh request returned status %d", response.StatusCode)
	}
	var payload struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := decodeJSON(response.Body, &payload); err != nil {
		return OAuthAccessToken{}, fmt.Errorf("decode Google OAuth refresh response: %w", err)
	}
	if strings.TrimSpace(payload.AccessToken) == "" {
		return OAuthAccessToken{}, errors.New("Google OAuth refresh response omitted an access token")
	}
	if payload.ExpiresIn < 1 {
		return OAuthAccessToken{}, errors.New("Google OAuth refresh response has an invalid expiry")
	}
	return OAuthAccessToken{
		Value:     payload.AccessToken,
		TokenType: payload.TokenType,
		ExpiresAt: time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second),
	}, nil
}

func (c *GoogleOAuthClient) ExchangeAuthorizationCode(ctx context.Context, code, redirectURL, verifier string) (OAuthCodeExchange, error) {
	if c == nil {
		return OAuthCodeExchange{}, errors.New("Google OAuth client is nil")
	}
	if strings.TrimSpace(code) == "" || strings.TrimSpace(redirectURL) == "" || strings.TrimSpace(verifier) == "" {
		return OAuthCodeExchange{}, errors.New("Google OAuth code, redirect URL, and verifier are required")
	}
	form := url.Values{
		"client_id": {c.clientID}, "client_secret": {c.clientSecret}, "grant_type": {"authorization_code"},
		"code": {code}, "redirect_uri": {redirectURL}, "code_verifier": {verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return OAuthCodeExchange{}, fmt.Errorf("create Google OAuth code request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	response, err := c.httpClient.Do(req)
	if err != nil {
		return OAuthCodeExchange{}, fmt.Errorf("send Google OAuth code request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return OAuthCodeExchange{}, fmt.Errorf("Google OAuth code request returned status %d", response.StatusCode)
	}
	var payload struct {
		RefreshToken string `json:"refresh_token"`
		Scope        string `json:"scope"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := decodeJSON(response.Body, &payload); err != nil {
		return OAuthCodeExchange{}, fmt.Errorf("decode Google OAuth code response: %w", err)
	}
	if strings.TrimSpace(payload.RefreshToken) == "" {
		return OAuthCodeExchange{}, errors.New("Google OAuth code response omitted a refresh token")
	}
	metadata, err := json.Marshal(map[string]any{"scope": payload.Scope, "token_type": payload.TokenType, "expires_in": payload.ExpiresIn})
	if err != nil {
		return OAuthCodeExchange{}, err
	}
	return OAuthCodeExchange{RefreshToken: payload.RefreshToken, Metadata: metadata}, nil
}

// RefreshTokenLookup retrieves an already-decrypted refresh token for one
// connection. Its implementation belongs to the encrypted connection store.
type RefreshTokenLookup func(context.Context, string) (string, error)

// GoogleOAuthTokenSource adapts encrypted connection storage to the Gmail
// client. It intentionally performs no token caching: the caller owns any
// cache so token invalidation is immediate and testable.
type GoogleOAuthTokenSource struct {
	oauthClient *GoogleOAuthClient
	lookup      RefreshTokenLookup
}

func NewGoogleOAuthTokenSource(oauthClient *GoogleOAuthClient, lookup RefreshTokenLookup) (*GoogleOAuthTokenSource, error) {
	if oauthClient == nil {
		return nil, errors.New("Google OAuth client is required")
	}
	if lookup == nil {
		return nil, errors.New("refresh token lookup is required")
	}
	return &GoogleOAuthTokenSource{oauthClient: oauthClient, lookup: lookup}, nil
}

func (s *GoogleOAuthTokenSource) AccessToken(ctx context.Context, connectionID string) (string, error) {
	if s == nil || s.oauthClient == nil || s.lookup == nil {
		return "", errors.New("Google OAuth token source is not configured")
	}
	if strings.TrimSpace(connectionID) == "" {
		return "", errors.New("Gmail connection ID is empty")
	}
	refreshToken, err := s.lookup(ctx, connectionID)
	if err != nil {
		return "", fmt.Errorf("load Gmail connection refresh token: %w", err)
	}
	token, err := s.oauthClient.ExchangeRefreshToken(ctx, refreshToken)
	if err != nil {
		return "", err
	}
	return token.Value, nil
}

// GmailHTTPClient makes Gmail API requests for one server-side connection at a
// time. The worker supplies its short-lived access token per request, keeping
// this client safe to reuse across separate connections.
type GmailHTTPClient struct {
	httpClient *http.Client
	baseURL    *url.URL
}

func NewGmailHTTPClient(httpClient *http.Client) (*GmailHTTPClient, error) {
	return newGmailHTTPClient(httpClient, defaultGmailAPIBaseURL)
}

func newGmailHTTPClient(httpClient *http.Client, baseURL string) (*GmailHTTPClient, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return nil, errors.New("Gmail API URL must be an absolute HTTPS URL")
	}
	if !strings.HasSuffix(parsed.Path, "/") {
		parsed.Path += "/"
	}
	return &GmailHTTPClient{
		httpClient: providerHTTPClient(httpClient),
		baseURL:    parsed,
	}, nil
}

// ListLabelMessages returns newest-first messages. label may be a Gmail label
// ID or its visible name (the transaction application passes "odin-finance").
func (c *GmailHTTPClient) ListLabelMessages(ctx context.Context, accessToken, label, pageToken string, maxResults int) ([]GmailMessageRef, string, error) {
	if c == nil {
		return nil, "", errors.New("Gmail client is nil")
	}
	if strings.TrimSpace(accessToken) == "" {
		return nil, "", errors.New("Gmail access token is empty")
	}
	if strings.TrimSpace(label) == "" {
		return nil, "", errors.New("Gmail label is empty")
	}
	if maxResults < 1 || maxResults > 500 {
		return nil, "", errors.New("Gmail maximum results must be between 1 and 500")
	}
	labelID, err := c.resolveLabelID(ctx, accessToken, label)
	if err != nil {
		return nil, "", err
	}
	query := url.Values{"labelIds": {labelID}, "maxResults": {strconv.Itoa(maxResults)}}
	if strings.TrimSpace(pageToken) != "" {
		query.Set("pageToken", pageToken)
	}
	var listed struct {
		Messages []struct {
			ID       string `json:"id"`
			ThreadID string `json:"threadId"`
		} `json:"messages"`
		NextPageToken string `json:"nextPageToken"`
	}
	if err := c.getJSON(ctx, accessToken, "users/me/messages", query, &listed); err != nil {
		return nil, "", err
	}
	refs := make([]GmailMessageRef, 0, len(listed.Messages))
	for _, message := range listed.Messages {
		if message.ID == "" {
			return nil, "", errors.New("Gmail list response contains a message without an ID")
		}
		metadata, err := c.messageMetadata(ctx, accessToken, message.ID)
		if err != nil {
			return nil, "", err
		}
		threadID := message.ThreadID
		if threadID == "" {
			threadID = metadata.ThreadID
		}
		refs = append(refs, GmailMessageRef{ID: message.ID, ThreadID: threadID, ReceivedAt: metadata.ReceivedAt})
	}
	return refs, listed.NextPageToken, nil
}

func (c *GmailHTTPClient) GetMessage(ctx context.Context, accessToken, messageID string) (GmailMessage, error) {
	if c == nil {
		return GmailMessage{}, errors.New("Gmail client is nil")
	}
	if strings.TrimSpace(messageID) == "" {
		return GmailMessage{}, errors.New("Gmail message ID is empty")
	}
	if strings.TrimSpace(accessToken) == "" {
		return GmailMessage{}, errors.New("Gmail access token is empty")
	}
	var raw struct {
		ID           string `json:"id"`
		ThreadID     string `json:"threadId"`
		InternalDate string `json:"internalDate"`
		Raw          string `json:"raw"`
	}
	if err := c.getJSON(ctx, accessToken, "users/me/messages/"+url.PathEscape(messageID), url.Values{"format": {"raw"}}, &raw); err != nil {
		return GmailMessage{}, err
	}
	rawMIME, err := decodeGmailBase64(raw.Raw)
	if err != nil {
		return GmailMessage{}, fmt.Errorf("decode Gmail raw message: %w", err)
	}
	var full gmailMessagePayload
	if err := c.getJSON(ctx, accessToken, "users/me/messages/"+url.PathEscape(messageID), url.Values{"format": {"full"}}, &full); err != nil {
		return GmailMessage{}, err
	}
	receivedAt, err := parseInternalDate(firstNonEmpty(full.InternalDate, raw.InternalDate))
	if err != nil {
		return GmailMessage{}, err
	}
	message := GmailMessage{
		ID:         firstNonEmpty(full.ID, raw.ID, messageID),
		ThreadID:   firstNonEmpty(full.ThreadID, raw.ThreadID),
		RawMIME:    string(rawMIME),
		ReceivedAt: receivedAt,
		Headers:    normalizedHeaders(full.Payload.Headers),
	}
	if err := c.collectPayload(ctx, accessToken, message.ID, full.Payload, &message); err != nil {
		return GmailMessage{}, err
	}
	message.HTML = normalizeEmailContent(message.HTML)
	message.Text = normalizeEmailContent(message.Text)
	return message, nil
}

type gmailMessageMetadata struct {
	ThreadID     string    `json:"threadId"`
	InternalDate string    `json:"internalDate"`
	ReceivedAt   time.Time `json:"-"`
}

func (c *GmailHTTPClient) messageMetadata(ctx context.Context, accessToken, messageID string) (gmailMessageMetadata, error) {
	var metadata gmailMessageMetadata
	err := c.getJSON(ctx, accessToken, "users/me/messages/"+url.PathEscape(messageID), url.Values{"format": {"metadata"}}, &metadata)
	if err != nil {
		return gmailMessageMetadata{}, err
	}
	receivedAt, err := parseInternalDate(metadata.InternalDate)
	if err != nil {
		return gmailMessageMetadata{}, err
	}
	metadata.ReceivedAt = receivedAt
	return metadata, nil
}

func (c *GmailHTTPClient) resolveLabelID(ctx context.Context, accessToken, label string) (string, error) {
	var response struct {
		Labels []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := c.getJSON(ctx, accessToken, "users/me/labels", nil, &response); err != nil {
		return "", err
	}
	for _, candidate := range response.Labels {
		if candidate.ID == label || candidate.Name == label {
			if candidate.ID == "" {
				break
			}
			return candidate.ID, nil
		}
	}
	return "", errors.New("Gmail label was not found")
}

type gmailMessagePayload struct {
	ID           string       `json:"id"`
	ThreadID     string       `json:"threadId"`
	InternalDate string       `json:"internalDate"`
	Payload      gmailPayload `json:"payload"`
}

type gmailPayload struct {
	MIMEType string           `json:"mimeType"`
	Filename string           `json:"filename"`
	Headers  []gmailHeader    `json:"headers"`
	Body     gmailMessageBody `json:"body"`
	Parts    []gmailPayload   `json:"parts"`
}

type gmailHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type gmailMessageBody struct {
	AttachmentID string `json:"attachmentId"`
	Size         int64  `json:"size"`
	Data         string `json:"data"`
}

func (c *GmailHTTPClient) collectPayload(ctx context.Context, accessToken, messageID string, payload gmailPayload, message *GmailMessage) error {
	if message == nil {
		return errors.New("Gmail message output is nil")
	}
	mediaType := normalizedMediaType(payload.MIMEType)
	if payload.Filename != "" {
		attachment, include, err := c.attachment(ctx, accessToken, messageID, payload, mediaType)
		if err != nil {
			return err
		}
		if include {
			message.Attachments = append(message.Attachments, attachment)
		}
		// A named part is an attachment, even when Gmail also gives it a text
		// MIME type. Do not accidentally mix a document into the email body.
		return nil
	}

	if len(payload.Parts) > 0 {
		for _, part := range payload.Parts {
			if err := c.collectPayload(ctx, accessToken, messageID, part, message); err != nil {
				return err
			}
		}
		return nil
	}
	if payload.Body.Data == "" {
		return nil
	}
	content, err := decodeGmailBase64(payload.Body.Data)
	if err != nil {
		return fmt.Errorf("decode Gmail message body: %w", err)
	}
	switch mediaType {
	case "text/plain":
		message.Text = appendEmailContent(message.Text, string(content))
	case "text/html":
		message.HTML = appendEmailContent(message.HTML, string(content))
	}
	return nil
}

func (c *GmailHTTPClient) attachment(ctx context.Context, accessToken, messageID string, payload gmailPayload, mediaType string) (GmailAttachment, bool, error) {
	if !supportedGmailAttachmentType(mediaType) || payload.Body.Size > maxGmailAttachmentBytes {
		return GmailAttachment{}, false, nil
	}
	content, err := c.attachmentContent(ctx, accessToken, messageID, payload.Body)
	if err != nil {
		return GmailAttachment{}, false, err
	}
	if int64(len(content)) > maxGmailAttachmentBytes {
		return GmailAttachment{}, false, nil
	}
	return GmailAttachment{
		ID:       payload.Body.AttachmentID,
		Filename: path.Base(payload.Filename),
		MIMEType: mediaType,
		Size:     int64(len(content)),
		Content:  content,
	}, true, nil
}

func (c *GmailHTTPClient) attachmentContent(ctx context.Context, accessToken, messageID string, body gmailMessageBody) ([]byte, error) {
	if body.Data != "" {
		content, err := decodeGmailBase64(body.Data)
		if err != nil {
			return nil, fmt.Errorf("decode Gmail attachment: %w", err)
		}
		return content, nil
	}
	if body.AttachmentID == "" {
		return nil, errors.New("Gmail attachment has no content")
	}
	var response struct {
		Data string `json:"data"`
	}
	resource := "users/me/messages/" + url.PathEscape(messageID) + "/attachments/" + url.PathEscape(body.AttachmentID)
	if err := c.getJSON(ctx, accessToken, resource, nil, &response); err != nil {
		return nil, err
	}
	content, err := decodeGmailBase64(response.Data)
	if err != nil {
		return nil, fmt.Errorf("decode Gmail attachment: %w", err)
	}
	return content, nil
}

func (c *GmailHTTPClient) getJSON(ctx context.Context, accessToken, resource string, query url.Values, target any) error {
	if strings.TrimSpace(accessToken) == "" {
		return errors.New("Gmail access token is empty")
	}
	relative, err := url.Parse(resource)
	if err != nil {
		return fmt.Errorf("create Gmail resource URL: %w", err)
	}
	endpoint := c.baseURL.ResolveReference(relative)
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return fmt.Errorf("create Gmail request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	response, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send Gmail request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("Gmail API request returned status %d", response.StatusCode)
	}
	if err := decodeJSON(response.Body, target); err != nil {
		return fmt.Errorf("decode Gmail API response: %w", err)
	}
	return nil
}

func providerHTTPClient(client *http.Client) *http.Client {
	if client != nil {
		return client
	}
	return &http.Client{Timeout: defaultProviderHTTPTimeout}
}

func decodeJSON(reader io.Reader, target any) error {
	decoder := json.NewDecoder(io.LimitReader(reader, 32*1024*1024))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return nil
}

func decodeGmailBase64(value string) ([]byte, error) {
	if value == "" {
		return nil, errors.New("Gmail base64 content is empty")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err == nil {
		return decoded, nil
	}
	decoded, paddedErr := base64.URLEncoding.DecodeString(value)
	if paddedErr != nil {
		return nil, errors.New("Gmail base64 content is invalid")
	}
	return decoded, nil
}

func parseInternalDate(value string) (time.Time, error) {
	milliseconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil || milliseconds < 0 {
		return time.Time{}, errors.New("Gmail message has an invalid internal date")
	}
	return time.UnixMilli(milliseconds).UTC(), nil
}

func normalizedHeaders(headers []gmailHeader) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	result := make(map[string]string, len(headers))
	for _, header := range headers {
		name := strings.ToLower(strings.TrimSpace(header.Name))
		if name == "" || strings.TrimSpace(header.Value) == "" {
			continue
		}
		if _, exists := result[name]; !exists {
			result[name] = strings.TrimSpace(header.Value)
		}
	}
	return result
}

func normalizedMediaType(value string) string {
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return strings.ToLower(strings.TrimSpace(value))
	}
	return strings.ToLower(mediaType)
}

func supportedGmailAttachmentType(mediaType string) bool {
	switch mediaType {
	case "application/pdf", "image/bmp", "image/jpeg", "image/png", "image/tiff", "image/webp", "image/heic":
		return true
	default:
		return false
	}
}

func appendEmailContent(current, next string) string {
	if current == "" {
		return next
	}
	return current + "\n\n" + next
}

func normalizeEmailContent(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	return strings.TrimSpace(value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// DevelopmentRefreshToken is intentionally unavailable outside local development.
type DevelopmentRefreshToken interface {
	DevelopmentRefreshToken() (string, bool)
}

// LocalDevelopmentTokenSource supports the explicitly configured local test account.
// It must never be constructed for a deployed environment or persisted to application data.
type LocalDevelopmentTokenSource struct {
	refreshToken string
}

func NewLocalDevelopmentTokenSource(environment, refreshToken string) (*LocalDevelopmentTokenSource, error) {
	if environment != "development" {
		return nil, errors.New("development refresh tokens are unavailable outside development")
	}
	if refreshToken == "" {
		return nil, errors.New("development refresh token is empty")
	}
	return &LocalDevelopmentTokenSource{refreshToken: refreshToken}, nil
}

func (s *LocalDevelopmentTokenSource) DevelopmentRefreshToken() (string, bool) {
	if s == nil || s.refreshToken == "" {
		return "", false
	}
	return s.refreshToken, true
}

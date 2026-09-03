package providers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/zhengteck/wealth-builder/backend/internal/prompts"
)

const (
	qwenFlashModel              = "qwen3.8-flash"
	qwenRequestTimeout          = 30 * time.Second
	maxEvidenceBytes            = 256 * 1024
	maxAttachments              = 5
	maxAttachmentBytes          = 5 * 1024 * 1024
	maxTotalAttachmentBytes     = 5 * 1024 * 1024
	maxRequestBytes             = 8 * 1024 * 1024
	maxResponseBytes            = 1 * 1024 * 1024
	ParserPlatformPromptVersion = prompts.TransactionParserVersion
)

// AlibabaQwenClient calls Alibaba's OpenAI-compatible chat endpoint. It sends
// source text plus a bounded set of already-authorized receipt images.
type AlibabaQwenClient struct {
	httpClient *http.Client
	baseURL    *url.URL
	apiKey     string
	model      string
}

func NewAlibabaQwenClient(httpClient *http.Client, baseURL *url.URL, apiKey, model string) (*AlibabaQwenClient, error) {
	if httpClient == nil {
		return nil, errors.New("Alibaba HTTP client is required")
	}
	if baseURL == nil || baseURL.Scheme != "https" || baseURL.Host == "" {
		return nil, errors.New("Alibaba API URL must be an absolute HTTPS URL")
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("Alibaba API key is required")
	}
	if model != qwenFlashModel {
		return nil, fmt.Errorf("Alibaba model must be %q", qwenFlashModel)
	}
	// Copy the caller's transport/TLS configuration, but do not permit an
	// unbounded request lifetime in a background worker.
	clientCopy := *httpClient
	if clientCopy.Timeout <= 0 || clientCopy.Timeout > qwenRequestTimeout {
		clientCopy.Timeout = qwenRequestTimeout
	}
	copyURL := *baseURL
	if !strings.HasSuffix(copyURL.Path, "/") {
		copyURL.Path += "/"
	}
	return &AlibabaQwenClient{httpClient: &clientCopy, baseURL: &copyURL, apiKey: apiKey, model: model}, nil
}

func (c *AlibabaQwenClient) ParseTransactionEvidence(ctx context.Context, assembledSystemPrompt, normalizedContent string, attachments []AttachmentInput) (ParsedCandidate, error) {
	result := ParsedCandidate{}
	if c == nil || c.httpClient == nil || c.baseURL == nil {
		return result, errors.New("Alibaba Qwen client is not configured")
	}
	result.Model = c.model
	if strings.TrimSpace(assembledSystemPrompt) == "" {
		return result, errors.New("assembled parser system prompt is required")
	}
	if len(assembledSystemPrompt) > 64*1024 {
		return result, errors.New("assembled parser system prompt exceeds parser limit")
	}
	if strings.TrimSpace(normalizedContent) == "" {
		return result, errors.New("normalized source content is required")
	}
	if len(normalizedContent) > maxEvidenceBytes {
		return result, errors.New("normalized source content exceeds parser limit")
	}
	parts, err := buildMultimodalParts(normalizedContent, attachments)
	if err != nil {
		return result, err
	}
	body, err := marshalTransactionParserRequest(c.model, assembledSystemPrompt, parts)
	if err != nil {
		return result, fmt.Errorf("encode Alibaba parse request: %w", err)
	}
	if len(body) > maxRequestBytes {
		return result, errors.New("Alibaba parse request exceeds size limit")
	}
	result.ProviderRequest = append([]byte(nil), body...)
	endpoint := c.baseURL.ResolveReference(&url.URL{Path: "chat/completions"})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return result, fmt.Errorf("create Alibaba parse request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+c.apiKey)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return result, fmt.Errorf("send Alibaba parse request: %w", err)
	}
	defer response.Body.Close()
	encodedResponse, err := readLimited(response.Body, maxResponseBytes)
	if err != nil {
		return result, err
	}
	result.ProviderResponse = auditJSONObject(encodedResponse, "raw_body")
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return result, fmt.Errorf("Alibaba parse request returned status %d", response.StatusCode)
	}
	var decoded chatCompletionResponse
	if err := json.Unmarshal(encodedResponse, &decoded); err != nil {
		return result, fmt.Errorf("decode Alibaba parse response: %w", err)
	}
	if len(decoded.Choices) != 1 || strings.TrimSpace(decoded.Choices[0].Message.Content) == "" {
		return result, errors.New("Alibaba parse response did not contain one JSON result")
	}
	rawJSON := []byte(decoded.Choices[0].Message.Content)
	result.JSON = append([]byte(nil), rawJSON...)
	var object map[string]json.RawMessage
	if err := json.Unmarshal(rawJSON, &object); err != nil || object == nil {
		return result, errors.New("Alibaba parse response result was not a JSON object")
	}
	return result, nil
}

// auditJSONObject retains an exact JSON object when possible. An invalid or
// non-object provider body is encoded as a JSON object containing the exact
// text so the database's object-only audit constraint can still preserve it.
func auditJSONObject(raw []byte, fallbackField string) []byte {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) == nil && object != nil {
		return append([]byte(nil), raw...)
	}
	encoded, _ := json.Marshal(map[string]string{fallbackField: string(raw)})
	return encoded
}

func buildMultimodalParts(evidence string, attachments []AttachmentInput) ([]chatContentPart, error) {
	if len(attachments) > maxAttachments {
		return nil, errors.New("too many visual attachments for Alibaba parser")
	}
	parts := make([]chatContentPart, 0, len(attachments)+1)
	parts = append(parts, chatContentPart{Type: "text", Text: evidence})
	totalAttachmentBytes := 0
	for _, attachment := range attachments {
		if len(attachment.Content) == 0 {
			return nil, errors.New("visual attachment content is required")
		}
		if len(attachment.Content) > maxAttachmentBytes {
			return nil, errors.New("visual attachment exceeds parser limit")
		}
		totalAttachmentBytes += len(attachment.Content)
		if totalAttachmentBytes > maxTotalAttachmentBytes {
			return nil, errors.New("visual attachments exceed parser limit")
		}
		mediaType, _, err := mime.ParseMediaType(attachment.MIMEType)
		if err != nil || !supportedImageMIMEType(mediaType) {
			return nil, errors.New("visual attachment must use a supported image MIME type")
		}
		parts = append(parts, chatContentPart{
			Type:     "image_url",
			ImageURL: &chatImageURL{URL: "data:" + mediaType + ";base64," + base64Encode(attachment.Content)},
		})
	}
	return parts, nil
}

const (
	PreviewEmailContentPlaceholder = "<EMAIL CONTENT OMITTED FROM PREVIEW>"
	PreviewAttachmentPlaceholder   = "<ELIGIBLE RECEIPT OR INVOICE IMAGE OMITTED FROM PREVIEW>"
)

// BuildTransactionParserRequestTemplate returns the exact provider request
// envelope used by production parsing while replacing source-owned dynamic
// content with explicit placeholders. It performs no network or database I/O.
func BuildTransactionParserRequestTemplate(assembledSystemPrompt string, includeVisualAttachment bool) (json.RawMessage, error) {
	if strings.TrimSpace(assembledSystemPrompt) == "" {
		return nil, errors.New("assembled parser system prompt is required")
	}
	parts := []chatContentPart{{Type: "text", Text: PreviewEmailContentPlaceholder}}
	if includeVisualAttachment {
		parts = append(parts, chatContentPart{
			Type:     "image_url",
			ImageURL: &chatImageURL{URL: PreviewAttachmentPlaceholder},
		})
	}
	body, err := marshalTransactionParserRequest(qwenFlashModel, assembledSystemPrompt, parts)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(body), nil
}

func marshalTransactionParserRequest(model, assembledSystemPrompt string, parts []chatContentPart) ([]byte, error) {
	return json.Marshal(chatCompletionRequest{
		Model:          model,
		EnableThinking: false,
		ResponseFormat: responseFormat{Type: "json_object"},
		Messages: []chatRequestMessage{
			{Role: "system", Content: assembledSystemPrompt},
			{Role: "user", Content: parts},
		},
	})
}

func supportedImageMIMEType(value string) bool {
	switch strings.ToLower(value) {
	case "image/jpeg", "image/png", "image/webp", "image/gif":
		return true
	default:
		return false
	}
}

func base64Encode(content []byte) string {
	return base64.StdEncoding.EncodeToString(content)
}

func readLimited(reader io.Reader, max int64) ([]byte, error) {
	content, err := io.ReadAll(io.LimitReader(reader, max+1))
	if err != nil {
		return nil, fmt.Errorf("read Alibaba parse response: %w", err)
	}
	if int64(len(content)) > max {
		return nil, errors.New("Alibaba parse response exceeds size limit")
	}
	return content, nil
}

type ParserPromptFragments struct {
	GlobalRule     string
	DefaultUser    string
	UserSourceRule string
}

// AssembleParserSystemPrompt keeps the immutable platform contract first and
// appends only the selected configuration fragments in a stable order. Source
// email text and attachment bytes are deliberately not accepted here; they
// remain in the user message.
func AssembleParserSystemPrompt(fragments ParserPromptFragments) string {
	sections := []string{ParserPlatformPrompt()}
	for _, fragment := range []struct {
		label string
		text  string
	}{
		{label: "GLOBAL SOURCE GUIDANCE", text: fragments.GlobalRule},
		{label: "USER DEFAULT INSTRUCTIONS", text: fragments.DefaultUser},
		{label: "USER SOURCE-RULE GUIDANCE", text: fragments.UserSourceRule},
	} {
		if value := strings.TrimSpace(fragment.text); value != "" {
			sections = append(sections, fragment.label+":\n"+value)
		}
	}
	return strings.Join(sections, "\n\n")
}

func ParserPlatformPrompt() string { return prompts.TransactionParserSystem() }

type chatCompletionRequest struct {
	Model          string               `json:"model"`
	Messages       []chatRequestMessage `json:"messages"`
	ResponseFormat responseFormat       `json:"response_format"`
	EnableThinking bool                 `json:"enable_thinking"`
}

type chatRequestMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type chatContentPart struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *chatImageURL `json:"image_url,omitempty"`
}

type chatImageURL struct {
	URL string `json:"url"`
}

type chatResponseMessage struct {
	Content string `json:"content"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type chatCompletionResponse struct {
	Choices []struct {
		Message chatResponseMessage `json:"message"`
	} `json:"choices"`
}

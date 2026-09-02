package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const qwenFlashModel = "qwen3.8-flash"

// AlibabaQwenClient calls Alibaba's OpenAI-compatible chat endpoint. It is
// intentionally limited to normalized email text; attachments remain in
// private storage until a separately reviewed attachment-processing flow is
// introduced.
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
	copyURL := *baseURL
	if !strings.HasSuffix(copyURL.Path, "/") {
		copyURL.Path += "/"
	}
	return &AlibabaQwenClient{httpClient: httpClient, baseURL: &copyURL, apiKey: apiKey, model: model}, nil
}

func (c *AlibabaQwenClient) ParseTransactionEvidence(ctx context.Context, normalizedContent string, attachments []AttachmentInput) (ParsedCandidate, error) {
	if c == nil || c.httpClient == nil || c.baseURL == nil {
		return ParsedCandidate{}, errors.New("Alibaba Qwen client is not configured")
	}
	if strings.TrimSpace(normalizedContent) == "" {
		return ParsedCandidate{}, errors.New("normalized source content is required")
	}
	if len(attachments) != 0 {
		return ParsedCandidate{}, errors.New("attachments are not supported by the email parser")
	}
	body, err := json.Marshal(chatCompletionRequest{
		Model:          c.model,
		EnableThinking: false,
		ResponseFormat: responseFormat{Type: "json_object"},
		Messages: []chatMessage{
			{Role: "system", Content: parserSystemPrompt},
			{Role: "user", Content: normalizedContent},
		},
	})
	if err != nil {
		return ParsedCandidate{}, fmt.Errorf("encode Alibaba parse request: %w", err)
	}
	endpoint := c.baseURL.ResolveReference(&url.URL{Path: "chat/completions"})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return ParsedCandidate{}, fmt.Errorf("create Alibaba parse request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+c.apiKey)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return ParsedCandidate{}, fmt.Errorf("send Alibaba parse request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return ParsedCandidate{}, fmt.Errorf("Alibaba parse request returned status %d", response.StatusCode)
	}
	var decoded chatCompletionResponse
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	if err := decoder.Decode(&decoded); err != nil {
		return ParsedCandidate{}, fmt.Errorf("decode Alibaba parse response: %w", err)
	}
	if len(decoded.Choices) != 1 || strings.TrimSpace(decoded.Choices[0].Message.Content) == "" {
		return ParsedCandidate{}, errors.New("Alibaba parse response did not contain one JSON result")
	}
	return ParsedCandidate{JSON: []byte(decoded.Choices[0].Message.Content), Model: c.model}, nil
}

const parserSystemPrompt = `Extract transaction evidence from the provided normalized email text. Return exactly one JSON object and no Markdown. The object must have candidate and evidence fields. candidate must contain user_id, transaction_kind (debit or credit), title, merchant_name, original_amount_minor, original_currency, occurred_at, references, account_evidence, line_items, and confidence. Do not invent facts. Every required canonical field must have an evidence entry with its original source path and a confidence between 0 and 1.`

type chatCompletionRequest struct {
	Model          string         `json:"model"`
	Messages       []chatMessage  `json:"messages"`
	ResponseFormat responseFormat `json:"response_format"`
	EnableThinking bool           `json:"enable_thinking"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type chatCompletionResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
}

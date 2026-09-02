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
)

const (
	qwenFlashModel          = "qwen3.8-flash"
	qwenRequestTimeout      = 30 * time.Second
	maxEvidenceBytes        = 256 * 1024
	maxAttachments          = 5
	maxAttachmentBytes      = 5 * 1024 * 1024
	maxTotalAttachmentBytes = 5 * 1024 * 1024
	maxRequestBytes         = 8 * 1024 * 1024
	maxResponseBytes        = 1 * 1024 * 1024
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

func (c *AlibabaQwenClient) ParseTransactionEvidence(ctx context.Context, normalizedContent string, attachments []AttachmentInput) (ParsedCandidate, error) {
	if c == nil || c.httpClient == nil || c.baseURL == nil {
		return ParsedCandidate{}, errors.New("Alibaba Qwen client is not configured")
	}
	if strings.TrimSpace(normalizedContent) == "" {
		return ParsedCandidate{}, errors.New("normalized source content is required")
	}
	if len(normalizedContent) > maxEvidenceBytes {
		return ParsedCandidate{}, errors.New("normalized source content exceeds parser limit")
	}
	parts, err := buildMultimodalParts(normalizedContent, attachments)
	if err != nil {
		return ParsedCandidate{}, err
	}
	body, err := json.Marshal(chatCompletionRequest{
		Model:          c.model,
		EnableThinking: false,
		ResponseFormat: responseFormat{Type: "json_object"},
		Messages: []chatRequestMessage{
			{Role: "system", Content: parserSystemPrompt},
			{Role: "user", Content: parts},
		},
	})
	if err != nil {
		return ParsedCandidate{}, fmt.Errorf("encode Alibaba parse request: %w", err)
	}
	if len(body) > maxRequestBytes {
		return ParsedCandidate{}, errors.New("Alibaba parse request exceeds size limit")
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
	encodedResponse, err := readLimited(response.Body, maxResponseBytes)
	if err != nil {
		return ParsedCandidate{}, err
	}
	var decoded chatCompletionResponse
	if err := json.Unmarshal(encodedResponse, &decoded); err != nil {
		return ParsedCandidate{}, fmt.Errorf("decode Alibaba parse response: %w", err)
	}
	if len(decoded.Choices) != 1 || strings.TrimSpace(decoded.Choices[0].Message.Content) == "" {
		return ParsedCandidate{}, errors.New("Alibaba parse response did not contain one JSON result")
	}
	rawJSON := []byte(decoded.Choices[0].Message.Content)
	var object map[string]json.RawMessage
	if err := json.Unmarshal(rawJSON, &object); err != nil || object == nil {
		return ParsedCandidate{}, errors.New("Alibaba parse response result was not a JSON object")
	}
	return ParsedCandidate{JSON: rawJSON, Model: c.model}, nil
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

const parserSystemPrompt = `Extract transaction evidence from the supplied email text and receipt images. Return exactly one JSON object and no Markdown. Do not invent facts. Do not include user_id or aggregate confidence: the server binds ownership and derives confidence.

Return exactly this shape and no extra keys at any level:
{
  "candidate": {
    "transaction_kind": "debit or credit",
    "title": "string",
    "merchant_name": "string",
    "original_amount_minor": 1234,
    "original_currency": "SGD",
    "sgd_amount_minor": null,
    "occurred_at": "RFC3339 timestamp",
    "references": ["string"],
    "account_evidence": {
      "card_last_four": "string",
      "masked_bank_reference": "string",
      "additional_identifiers": ["string"]
    },
    "line_items": [
      {
        "schema_version": 1,
        "description": "string",
        "quantity": 1,
        "unit_price_minor": 1234,
        "line_total_minor": 1234,
        "tax_minor": 0,
        "discount_minor": 0,
        "currency": "SGD",
        "details": {}
      }
    ],
    "category_leaf_name": "optional exact taxonomy value"
  },
  "evidence": [
    {
      "field": "one allowed field name",
      "source_path": "source citation path",
      "confidence": 0.9
    }
  ]
}

All money fields are unquoted base-10 integer minor units, never major-unit decimals. For example, 12.34 SGD is original_amount_minor 1234. transaction_kind is exactly debit or credit. Currency is a canonical uppercase ISO 4217 code. occurred_at is an RFC3339 string; use received_at only when the source has no explicit event timestamp. sgd_amount_minor is an integer when stated by the source and null otherwise.

account_evidence is always an object, never a string or null. Use empty strings and additional_identifiers: [] when no account identifier is present. references is [] when absent. line_items is [] unless the source provides reliable item-level detail. Every line item has schema_version 1, a non-empty description, a positive integer quantity, an uppercase ISO 4217 currency, and details: {}. Optional line-item money fields may be omitted or null; when present they are non-negative integer minor units.

Evidence objects contain exactly field, source_path, and confidence. Do not add note, reason, rationale, amount_minor, or any other key. Evidence.field is exactly one of transaction_kind, title, merchant_name, original_amount_minor, original_currency, sgd_amount_minor, occurred_at, references, account_evidence, line_items, category_leaf_name. Every populated decisive candidate field needs an evidence entry. Evidence.source_path begins with subject, sender, text, or attachment, or is exactly received_at. Evidence.confidence is an unquoted number between 0 and 1.

category_leaf_name is omitted when unsupported by the source, or is exactly one of: Paychecks, Interest, Business Income, Other Income, Charity, Gifts, Auto Payment, Public Transit, Gas, Auto Maintenance, Parking & Tolls, Taxi & Ride Shares, Mortgage, Rent, Home Improvement, Garbage, Water, Gas & Electric, Internet & Cable, Phone, Groceries, Restaurants & Bars, Coffee Shops, Travel & Vacation, Entertainment & Recreation, Personal, Pets, Fun Money, Shopping, Clothing, Furniture & Housewares, Electronics, Child Care, Child Activities, Student Loans, Education, Medical, Dentist, Fitness, Loan Repayment, Financial & Legal Services, Financial Fees, Cash & ATM, Insurance, Taxes, Uncategorized, Check, Miscellaneous, Advertising & Promotion, Business Utilities & Communication, Employee Wages & Contract Labor, Business Travel & Meals, Business Auto Expenses, Business Insurance, Office Supplies & Expenses, Office Rent, Postage & Shipping, Transfer, Credit Card Payment, Balance Adjustments.`

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

package transactions

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/zhengteck/wealth-builder/backend/internal/reconciliation"
	"github.com/zhengteck/wealth-builder/backend/internal/transactionstore"
)

func requiredString(raw json.RawMessage, field string) (string, error) {
	var value string
	if isJSONNull(raw) || json.Unmarshal(raw, &value) != nil || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s must be a non-empty string", field)
	}
	return strings.TrimSpace(value), nil
}

func requiredUUID(raw json.RawMessage, field string) (uuid.UUID, error) {
	value, err := requiredString(raw, field)
	if err != nil {
		return uuid.Nil, err
	}
	parsed, err := uuid.Parse(value)
	if err != nil {
		return uuid.Nil, fmt.Errorf("%s must be a UUID", field)
	}
	return parsed, nil
}

func requiredMinorAmount(raw json.RawMessage, field string, allowZero bool) (int64, error) {
	value, err := requiredString(raw, field)
	if err != nil {
		return 0, err
	}
	if strings.HasPrefix(value, "+") || strings.HasPrefix(value, "-") || strings.ContainsAny(value, ".eE") {
		return 0, fmt.Errorf("%s must be a decimal integer string", field)
	}
	amount, err := strconv.ParseInt(value, 10, 64)
	if err != nil || amount < 0 || (!allowZero && amount == 0) {
		return 0, fmt.Errorf("%s must be a positive decimal integer string", field)
	}
	return amount, nil
}

func validateLineItems(raw json.RawMessage) (json.RawMessage, error) {
	var input []lineItemRequest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || input == nil {
		return nil, errors.New("line_items must be an array")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, errors.New("line_items must contain one JSON value")
	}
	result := make([]reconciliation.LineItem, 0, len(input))
	for index, item := range input {
		decoded, err := item.toLineItem()
		if err != nil {
			return nil, fmt.Errorf("line_items[%d]: %w", index, err)
		}
		result = append(result, decoded)
	}
	if err := reconciliation.ValidateLineItems(result); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

type lineItemRequest struct {
	SchemaVersion  int             `json:"schema_version"`
	Description    string          `json:"description"`
	Quantity       int             `json:"quantity"`
	UnitPriceMinor *string         `json:"unit_price_minor,omitempty"`
	LineTotalMinor *string         `json:"line_total_minor,omitempty"`
	TaxMinor       *string         `json:"tax_minor,omitempty"`
	DiscountMinor  *string         `json:"discount_minor,omitempty"`
	Currency       string          `json:"currency"`
	Details        json.RawMessage `json:"details"`
}

func (item lineItemRequest) toLineItem() (reconciliation.LineItem, error) {
	result := reconciliation.LineItem{SchemaVersion: item.SchemaVersion, Description: item.Description,
		Quantity: item.Quantity, Currency: item.Currency, Details: item.Details}
	for _, field := range []struct {
		name  string
		input *string
		out   **int64
	}{
		{"unit_price_minor", item.UnitPriceMinor, &result.UnitPriceMinor},
		{"line_total_minor", item.LineTotalMinor, &result.LineTotalMinor},
		{"tax_minor", item.TaxMinor, &result.TaxMinor},
		{"discount_minor", item.DiscountMinor, &result.DiscountMinor},
	} {
		if field.input == nil {
			continue
		}
		amount, err := parseMinorString(*field.input, field.name, true)
		if err != nil {
			return reconciliation.LineItem{}, err
		}
		*field.out = &amount
	}
	return result, nil
}

func parseMinorString(value, field string, allowZero bool) (int64, error) {
	raw, _ := json.Marshal(value)
	return requiredMinorAmount(raw, field, allowZero)
}

func transactionResponse(transaction transactionstore.Transaction) transactionJSON {
	response := transactionJSON{
		ID: transaction.ID.String(), AccountID: transaction.AccountID.String(), TransactionKind: transaction.TransactionKind,
		Title: transaction.Title, MerchantName: transaction.MerchantName, OriginalAmountMinor: strconv.FormatInt(transaction.OriginalAmountMinor, 10),
		OriginalCurrency: transaction.OriginalCurrency, OccurredAt: transaction.OccurredAt, ReviewStatus: transaction.ReviewStatus,
		MatchConfidence: transaction.MatchConfidence, CreatedAt: transaction.CreatedAt, UpdatedAt: transaction.UpdatedAt,
		LineItems: lineItemsResponse(transaction.LineItems),
	}
	if transaction.CategoryID != nil {
		categoryID := transaction.CategoryID.String()
		response.CategoryID = &categoryID
	}
	if transaction.SGDAmountMinor != nil {
		amount := strconv.FormatInt(*transaction.SGDAmountMinor, 10)
		response.SGDAmountMinor = &amount
	}
	return response
}

type transferLinkJSON struct {
	ID                       string  `json:"id"`
	LinkType                 string  `json:"link_type"`
	Role                     string  `json:"role"`
	CounterpartTransactionID string  `json:"counterpart_transaction_id"`
	CounterpartAccountID     string  `json:"counterpart_account_id"`
	CounterpartTitle         string  `json:"counterpart_title"`
	CounterpartAccountName   *string `json:"counterpart_account_name"`
}

type transactionListJSON struct {
	transactionJSON
	AccountName        string            `json:"account_name"`
	CategoryName       *string           `json:"category_name"`
	CategoryParentName *string           `json:"category_parent_name"`
	Details            json.RawMessage   `json:"details"`
	SourceCount        int               `json:"source_count"`
	TransferLink       *transferLinkJSON `json:"transfer_link"`
}

func transactionListResponse(transaction transactionstore.TransactionListRecord) transactionListJSON {
	response := transactionListJSON{
		transactionJSON: transactionResponse(transaction.Transaction),
		AccountName:     transaction.AccountName, CategoryName: transaction.CategoryName,
		CategoryParentName: transaction.CategoryParentName,
		Details:            transaction.Details, SourceCount: transaction.SourceCount,
	}
	if len(response.Details) == 0 {
		response.Details = json.RawMessage("{}")
	}
	if transaction.TransferLink != nil {
		response.TransferLink = &transferLinkJSON{
			ID: transaction.TransferLink.ID.String(), LinkType: transaction.TransferLink.LinkType,
			Role:                     transaction.TransferLink.Role,
			CounterpartTransactionID: transaction.TransferLink.CounterpartTransactionID.String(),
			CounterpartAccountID:     transaction.TransferLink.CounterpartAccountID.String(),
			CounterpartTitle:         transaction.TransferLink.CounterpartTitle,
			CounterpartAccountName:   transaction.TransferLink.CounterpartAccountName,
		}
	}
	return response
}

func internalTransferResponse(transfer transactionstore.InternalTransfer) map[string]any {
	return map[string]any{
		"link": map[string]any{
			"id": transfer.ID.String(), "link_type": transfer.LinkType,
			"debit_transaction_id":  transfer.Debit.ID.String(),
			"credit_transaction_id": transfer.Credit.ID.String(),
			"created_at":            transfer.CreatedAt,
		},
		"debit":  transactionResponse(transfer.Debit),
		"credit": transactionResponse(transfer.Credit),
	}
}

type sourceSummaryJSON struct {
	ID                        string    `json:"id"`
	SourceType                string    `json:"source_type"`
	Provider                  string    `json:"provider"`
	ReceivedAt                time.Time `json:"received_at"`
	ParseStatus               string    `json:"parse_status"`
	ParseConfidence           *int16    `json:"parse_confidence"`
	Subject                   string    `json:"subject"`
	Sender                    string    `json:"sender"`
	ParseError                *string   `json:"parse_error"`
	ReconciliationReason      *string   `json:"reconciliation_reason"`
	SuggestedTitle            *string   `json:"suggested_title"`
	SuggestedAmountMinor      *string   `json:"suggested_amount_minor"`
	SuggestedCurrency         *string   `json:"suggested_currency"`
	SuggestedAccountID        *string   `json:"suggested_account_id"`
	SuggestedAccountName      *string   `json:"suggested_account_name"`
	SuggestedTransactionID    *string   `json:"suggested_transaction_id"`
	SuggestedCategoryLeafName *string   `json:"suggested_category_leaf_name"`
	CreatedAt                 time.Time `json:"created_at"`
}

func sourceSummaryResponse(source transactionstore.SourceSummary) sourceSummaryJSON {
	response := sourceSummaryJSON{
		ID: source.ID.String(), SourceType: source.SourceType, Provider: source.Provider,
		ReceivedAt: source.ReceivedAt, ParseStatus: source.ParseStatus,
		ParseConfidence: source.ParseConfidence, Subject: source.Subject, Sender: source.Sender,
		ParseError: source.ParseError, ReconciliationReason: source.ReconciliationReason,
		SuggestedTitle: source.SuggestedTitle, SuggestedCurrency: source.SuggestedCurrency,
		SuggestedAccountName:      source.SuggestedAccountName,
		SuggestedCategoryLeafName: source.SuggestedCategoryLeafName, CreatedAt: source.CreatedAt,
	}
	if source.SuggestedAmountMinor != nil {
		value := strconv.FormatInt(*source.SuggestedAmountMinor, 10)
		response.SuggestedAmountMinor = &value
	}
	if source.SuggestedAccountID != nil {
		value := source.SuggestedAccountID.String()
		response.SuggestedAccountID = &value
	}
	if source.SuggestedTransactionID != nil {
		value := source.SuggestedTransactionID.String()
		response.SuggestedTransactionID = &value
	}
	return response
}

const attachmentURLExpirySeconds = 300

type attachmentJSON struct {
	ID            string `json:"id"`
	Filename      string `json:"filename"`
	MIMEType      string `json:"mime_type"`
	ByteSize      int64  `json:"byte_size"`
	SHA256        string `json:"sha256"`
	ParseEligible bool   `json:"parse_eligible"`
	ParseStatus   string `json:"parse_status"`
	StorageStatus string `json:"storage_status"`
	SignedURL     string `json:"signed_url"`
}

type transactionJSON struct {
	ID                  string             `json:"id"`
	AccountID           string             `json:"account_id"`
	TransactionKind     string             `json:"transaction_kind"`
	Title               string             `json:"title"`
	MerchantName        *string            `json:"merchant_name"`
	OriginalAmountMinor string             `json:"original_amount_minor"`
	OriginalCurrency    string             `json:"original_currency"`
	SGDAmountMinor      *string            `json:"sgd_amount_minor"`
	OccurredAt          time.Time          `json:"occurred_at"`
	CategoryID          *string            `json:"category_id"`
	LineItems           []lineItemResponse `json:"line_items"`
	ReviewStatus        string             `json:"review_status"`
	MatchConfidence     *int16             `json:"match_confidence"`
	CreatedAt           time.Time          `json:"created_at"`
	UpdatedAt           time.Time          `json:"updated_at"`
}

func lineItemsResponse(raw json.RawMessage) []lineItemResponse {
	stored, err := decodeStoredLineItems(raw)
	if err != nil {
		return []lineItemResponse{}
	}
	result := make([]lineItemResponse, 0, len(stored))
	for _, item := range stored {
		result = append(result, lineItemResponse{
			SchemaVersion: item.SchemaVersion, Description: item.Description, Quantity: item.Quantity,
			UnitPriceMinor: minorString(item.UnitPriceMinor), LineTotalMinor: minorString(item.LineTotalMinor),
			TaxMinor: minorString(item.TaxMinor), DiscountMinor: minorString(item.DiscountMinor),
			Currency: item.Currency, Details: item.Details,
		})
	}
	return result
}

// storedLineItem is deliberately separate from the write-boundary LineItem
// type. Postgres permits bigint-safe decimal strings for browser-created rows,
// while model and API writes continue to require their existing representations.
type storedLineItem struct {
	SchemaVersion  int             `json:"schema_version"`
	Description    string          `json:"description"`
	Quantity       int             `json:"quantity"`
	UnitPriceMinor json.RawMessage `json:"unit_price_minor"`
	LineTotalMinor json.RawMessage `json:"line_total_minor"`
	TaxMinor       json.RawMessage `json:"tax_minor"`
	DiscountMinor  json.RawMessage `json:"discount_minor"`
	Currency       string          `json:"currency"`
	Details        json.RawMessage `json:"details"`
}

func decodeStoredLineItems(raw json.RawMessage) ([]reconciliation.LineItem, error) {
	var input []storedLineItem
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || input == nil {
		return nil, errors.New("stored line_items must be an array")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, errors.New("stored line_items must contain one JSON value")
	}

	result := make([]reconciliation.LineItem, 0, len(input))
	for index, item := range input {
		decoded := reconciliation.LineItem{
			SchemaVersion: item.SchemaVersion,
			Description:   item.Description,
			Quantity:      item.Quantity,
			Currency:      item.Currency,
			Details:       item.Details,
		}
		for _, amount := range []struct {
			name string
			raw  json.RawMessage
			out  **int64
		}{
			{"unit_price_minor", item.UnitPriceMinor, &decoded.UnitPriceMinor},
			{"line_total_minor", item.LineTotalMinor, &decoded.LineTotalMinor},
			{"tax_minor", item.TaxMinor, &decoded.TaxMinor},
			{"discount_minor", item.DiscountMinor, &decoded.DiscountMinor},
		} {
			value, err := decodeStoredMinorAmount(amount.raw)
			if err != nil {
				return nil, fmt.Errorf("stored line_items[%d].%s: %w", index, amount.name, err)
			}
			*amount.out = value
		}
		result = append(result, decoded)
	}
	if err := reconciliation.ValidateLineItems(result); err != nil {
		return nil, fmt.Errorf("stored line_items are invalid: %w", err)
	}
	return result, nil
}

func decodeStoredMinorAmount(raw json.RawMessage) (*int64, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}

	decimal := string(trimmed)
	if trimmed[0] == '"' {
		if err := json.Unmarshal(trimmed, &decimal); err != nil {
			return nil, errors.New("must be a JSON integer, decimal-integer string, or null")
		}
	}
	if decimal == "" {
		return nil, errors.New("must be a non-negative decimal integer")
	}
	for _, digit := range decimal {
		if digit < '0' || digit > '9' {
			return nil, errors.New("must be a non-negative decimal integer")
		}
	}

	canonical := strings.TrimLeft(decimal, "0")
	if canonical == "" {
		canonical = "0"
	}
	if len(canonical) > 19 {
		return nil, errors.New("must fit in a signed 64-bit integer")
	}
	value, err := strconv.ParseInt(canonical, 10, 64)
	if err != nil {
		return nil, errors.New("must fit in a signed 64-bit integer")
	}
	return &value, nil
}

type lineItemResponse struct {
	SchemaVersion  int             `json:"schema_version"`
	Description    string          `json:"description"`
	Quantity       int             `json:"quantity"`
	UnitPriceMinor *string         `json:"unit_price_minor,omitempty"`
	LineTotalMinor *string         `json:"line_total_minor,omitempty"`
	TaxMinor       *string         `json:"tax_minor,omitempty"`
	DiscountMinor  *string         `json:"discount_minor,omitempty"`
	Currency       string          `json:"currency"`
	Details        json.RawMessage `json:"details"`
}

func minorString(value *int64) *string {
	if value == nil {
		return nil
	}
	result := strconv.FormatInt(*value, 10)
	return &result
}

func isJSONNull(raw json.RawMessage) bool { return strings.TrimSpace(string(raw)) == "null" }

func validCurrency(value string) bool {
	return reconciliation.IsISO4217(value)
}

func syncRunResponse(run transactionstore.SyncRun) map[string]any {
	return map[string]any{
		"id": run.ID, "status": run.Status, "messages_discovered": run.MessagesFoundCount,
		"messages_ingested": run.SourcesSavedCount, "sources_parsed": run.SourcesParsedCount,
		"sources_failed":       run.SourcesFailedCount,
		"transactions_created": run.TransactionsCreatedCount, "sources_review": run.ReviewRequiredCount,
		"sources_dangling": run.DanglingSourcesCount, "error_summary": run.ErrorSummary,
		"started_at": run.StartedAt, "ingestion_completed_at": run.IngestionCompletedAt,
		"completed_at": run.CompletedAt,
	}
}

type encodedPageCursor struct {
	Version   int       `json:"v"`
	Scope     string    `json:"scope"`
	Timestamp time.Time `json:"timestamp"`
	ID        string    `json:"id"`
}

func pageLimit(raw string) (int, error) {
	if raw == "" {
		return 50, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > 100 {
		return 0, errors.New("limit must be an integer from 1 to 100")
	}
	return value, nil
}

func encodeCursor(scope string, timestamp time.Time, id uuid.UUID) string {
	encoded, _ := json.Marshal(encodedPageCursor{Version: 1, Scope: scope, Timestamp: timestamp.UTC(), ID: id.String()})
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeCursor(raw, scope string) (time.Time, uuid.UUID, error) {
	if len(raw) > 1024 {
		return time.Time{}, uuid.Nil, errors.New("cursor is too long")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return time.Time{}, uuid.Nil, err
	}
	var cursor encodedPageCursor
	decoder := json.NewDecoder(bytes.NewReader(decoded))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&cursor); err != nil {
		return time.Time{}, uuid.Nil, err
	}
	if err = decoder.Decode(&struct{}{}); err != io.EOF {
		return time.Time{}, uuid.Nil, errors.New("cursor contains trailing data")
	}
	id, err := uuid.Parse(cursor.ID)
	if err != nil || id == uuid.Nil || cursor.Version != 1 || cursor.Scope != scope || cursor.Timestamp.IsZero() {
		return time.Time{}, uuid.Nil, errors.New("cursor does not match this listing")
	}
	return cursor.Timestamp, id, nil
}

func transactionCursorScope(kind, reviewStatus string, accountID *uuid.UUID, search string) string {
	account := ""
	if accountID != nil {
		account = accountID.String()
	}
	return strings.Join([]string{"transactions", kind, reviewStatus, account, search}, "\x00")
}

func safeValidationMessage(err error) string {
	message := strings.TrimSpace(err.Error())
	if message == "" || len(message) > 300 || strings.ContainsAny(message, "\r\n") {
		return "request body is invalid"
	}
	return message
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	if strings.ContainsAny(message, "\r\n") {
		message = "Request could not be completed."
	}
	writeJSON(w, status, map[string]string{"error": message})
}

// Package transactions exposes only authenticated, operational transaction workflow endpoints.
package transactions

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
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/zhengteck/wealth-builder/backend/internal/auth"
	"github.com/zhengteck/wealth-builder/backend/internal/reconciliation"
	"github.com/zhengteck/wealth-builder/backend/internal/transactionstore"
)

type Repository interface {
	CreateSyncRun(context.Context, uuid.UUID, bool) (transactionstore.SyncRun, error)
	GetSyncRun(context.Context, uuid.UUID, uuid.UUID) (transactionstore.SyncRun, error)
	ListSources(context.Context, uuid.UUID, string) ([]transactionstore.SourceSummary, error)
	GetSanitizedEmail(context.Context, uuid.UUID, uuid.UUID) (transactionstore.SanitizedEmail, error)
	ListTransactionSources(context.Context, uuid.UUID, uuid.UUID) ([]transactionstore.SourceEvidence, error)
	AttachSource(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (uuid.UUID, error)
	CreateTransactionFromSource(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (transactionstore.Transaction, error)
	UnmatchSourceLink(context.Context, uuid.UUID, uuid.UUID) error
	PatchTransaction(context.Context, uuid.UUID, uuid.UUID, transactionstore.TransactionPatch) (transactionstore.Transaction, error)
}

type GmailOAuthFlow interface {
	Begin(context.Context, uuid.UUID) (string, error)
	Complete(context.Context, string, string) error
}

type Handler struct {
	repository            Repository
	allowDevelopmentToken bool
	gmailOAuth            GmailOAuthFlow
	frontendOrigin        *url.URL
}

func NewHandler(repository Repository, allowDevelopmentToken bool, gmailOAuth GmailOAuthFlow, frontendOrigin *url.URL) *Handler {
	return &Handler{repository: repository, allowDevelopmentToken: allowDevelopmentToken, gmailOAuth: gmailOAuth, frontendOrigin: frontendOrigin}
}

func (h *Handler) Register(mux *http.ServeMux, verifier auth.Verifier) {
	requireUser := func(next http.HandlerFunc) http.Handler { return auth.RequireUser(verifier, next) }
	mux.Handle("POST /v1/transactions/gmail/sync-runs", requireUser(h.createSyncRun))
	mux.Handle("POST /v1/transactions/gmail/connect", requireUser(h.beginGmailConnection))
	mux.Handle("GET /v1/transactions/gmail/oauth/callback", http.HandlerFunc(h.completeGmailConnection))
	mux.Handle("GET /v1/transactions/sync-runs/{id}", requireUser(h.getSyncRun))
	mux.Handle("GET /v1/transactions/sources", requireUser(h.listSources))
	mux.Handle("GET /v1/transactions/sources/{id}/email", requireUser(h.getSourceEmail))
	// ServeMux's segment patterns make /{id}/sources ambiguous with the
	// established /sync-runs/{id} route. This narrow subtree handler preserves
	// both public paths while rejecting every other transaction subroute.
	mux.Handle("GET /v1/transactions/", requireUser(h.transactionSubroute))
	mux.Handle("POST /v1/transactions/sources/{id}/attach", requireUser(h.attachSource))
	mux.Handle("POST /v1/transactions/sources/{id}/create-transaction", requireUser(h.createTransactionFromSource))
	mux.Handle("POST /v1/transactions/source-links/{id}/unmatch", requireUser(h.unmatchSourceLink))
	mux.Handle("PATCH /v1/transactions/{id}", requireUser(h.patchTransaction))
}

func (h *Handler) beginGmailConnection(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	if h.gmailOAuth == nil {
		writeError(w, http.StatusServiceUnavailable, "Gmail connection is not available.")
		return
	}
	authorizationURL, err := h.gmailOAuth.Begin(r.Context(), user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not begin Gmail connection.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"authorization_url": authorizationURL})
}

func (h *Handler) completeGmailConnection(w http.ResponseWriter, r *http.Request) {
	if h.gmailOAuth == nil || h.frontendOrigin == nil {
		http.Error(w, "Gmail connection is not available.", http.StatusServiceUnavailable)
		return
	}
	state, code := r.URL.Query().Get("state"), r.URL.Query().Get("code")
	if r.URL.Query().Get("error") != "" || state == "" || code == "" || h.gmailOAuth.Complete(r.Context(), state, code) != nil {
		h.redirectGmailResult(w, r, "connection_failed")
		return
	}
	h.redirectGmailResult(w, r, "connected")
}

func (h *Handler) redirectGmailResult(w http.ResponseWriter, r *http.Request, result string) {
	target := *h.frontendOrigin
	query := target.Query()
	query.Set("gmail", result)
	target.RawQuery = query.Encode()
	http.Redirect(w, r, target.String(), http.StatusSeeOther)
}

func (h *Handler) createSyncRun(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	run, err := h.repository.CreateSyncRun(r.Context(), user.ID, h.allowDevelopmentToken)
	if errors.Is(err, transactionstore.ErrGmailConnectionRequired) {
		writeError(w, http.StatusConflict, "Connect Gmail before starting a refresh.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not start Gmail refresh.")
		return
	}
	writeJSON(w, http.StatusAccepted, syncRunResponse(run))
}

func (h *Handler) getSyncRun(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	runID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "Sync run not found.")
		return
	}
	run, err := h.repository.GetSyncRun(r.Context(), user.ID, runID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Sync run not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not load sync run.")
		return
	}
	writeJSON(w, http.StatusOK, syncRunResponse(run))
}

func (h *Handler) listSources(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	status := r.URL.Query().Get("status")
	if status == "review" {
		status = "review_required"
	}
	if status != "" && status != "dangling" && status != "review_required" {
		writeError(w, http.StatusBadRequest, "status must be dangling or review")
		return
	}
	sources, err := h.repository.ListSources(r.Context(), user.ID, status)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not load sources.")
		return
	}
	writeJSON(w, http.StatusOK, sources)
}

func (h *Handler) getSourceEmail(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	sourceID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "Source not found.")
		return
	}
	email, err := h.repository.GetSanitizedEmail(r.Context(), user.ID, sourceID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Source not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not load source email.")
		return
	}
	writeJSON(w, http.StatusOK, email)
}

func (h *Handler) listTransactionSources(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	transactionID, err := transactionIDFromSourcesRoute(r)
	if err != nil {
		writeError(w, http.StatusNotFound, "Transaction not found.")
		return
	}
	sources, err := h.repository.ListTransactionSources(r.Context(), user.ID, transactionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not load transaction sources.")
		return
	}
	writeJSON(w, http.StatusOK, sources)
}

func (h *Handler) transactionSubroute(w http.ResponseWriter, r *http.Request) {
	const prefix = "/v1/transactions/"
	path := strings.TrimPrefix(r.URL.Path, prefix)
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[1] != "sources" || parts[0] == "" {
		writeError(w, http.StatusNotFound, "Endpoint not found.")
		return
	}
	h.listTransactionSources(w, r.WithContext(r.Context()))
}

func (h *Handler) attachSource(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	sourceID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "Source not found.")
		return
	}
	var request struct {
		TransactionID string `json:"transaction_id"`
	}
	if err := decodeRequestJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "transaction_id is required.")
		return
	}
	transactionID, err := uuid.Parse(request.TransactionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "transaction_id must be a UUID.")
		return
	}
	linkID, err := h.repository.AttachSource(r.Context(), user.ID, sourceID, transactionID)
	if writeActionError(w, err, "Source", "Could not attach source.") {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"source_link_id": linkID.String()})
}

func (h *Handler) createTransactionFromSource(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	sourceID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "Source not found.")
		return
	}
	var request struct {
		AccountID string `json:"account_id"`
	}
	if err := decodeRequestJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "account_id is required.")
		return
	}
	accountID, err := uuid.Parse(request.AccountID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "account_id must be a UUID.")
		return
	}
	transaction, err := h.repository.CreateTransactionFromSource(r.Context(), user.ID, sourceID, accountID)
	if writeActionError(w, err, "Source", "Could not create transaction from source.") {
		return
	}
	writeJSON(w, http.StatusCreated, transactionResponse(transaction))
}

func (h *Handler) unmatchSourceLink(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	linkID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "Source link not found.")
		return
	}
	err = h.repository.UnmatchSourceLink(r.Context(), user.ID, linkID)
	if writeActionError(w, err, "Source link", "Could not unmatch source.") {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) patchTransaction(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	transactionID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "Transaction not found.")
		return
	}
	patch, err := decodeTransactionPatch(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid transaction update: "+err.Error())
		return
	}
	transaction, err := h.repository.PatchTransaction(r.Context(), user.ID, transactionID, patch)
	if writeActionError(w, err, "Transaction", "Could not update transaction.") {
		return
	}
	writeJSON(w, http.StatusOK, transactionResponse(transaction))
}

func transactionIDFromSourcesRoute(r *http.Request) (uuid.UUID, error) {
	const prefix = "/v1/transactions/"
	path := strings.TrimPrefix(r.URL.Path, prefix)
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[1] != "sources" {
		return uuid.Nil, errors.New("invalid transaction source route")
	}
	return uuid.Parse(parts[0])
}

func writeActionError(w http.ResponseWriter, err error, resource, fallback string) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, transactionstore.ErrSourceNotFound) || errors.Is(err, transactionstore.ErrTransactionNotFound) || errors.Is(err, transactionstore.ErrSourceLinkNotFound) {
		writeError(w, http.StatusNotFound, resource+" not found.")
		return true
	}
	if errors.Is(err, transactionstore.ErrSourceNotActionable) || errors.Is(err, transactionstore.ErrSourceAlreadyLinked) {
		writeError(w, http.StatusConflict, "This source is no longer available for that action.")
		return true
	}
	writeError(w, http.StatusInternalServerError, fallback)
	return true
}

const maxActionRequestBytes = 1 << 20

func decodeRequestJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxActionRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func decodeTransactionPatch(w http.ResponseWriter, r *http.Request) (transactionstore.TransactionPatch, error) {
	var fields map[string]json.RawMessage
	if err := decodeRequestJSON(w, r, &fields); err != nil {
		return transactionstore.TransactionPatch{}, err
	}
	if len(fields) == 0 {
		return transactionstore.TransactionPatch{}, errors.New("at least one editable field is required")
	}
	allowed := map[string]bool{
		"title": true, "account_id": true, "occurred_at": true, "original_amount_minor": true,
		"original_currency": true, "sgd_amount_minor": true, "category_id": true, "line_items": true,
	}
	for name := range fields {
		if !allowed[name] {
			return transactionstore.TransactionPatch{}, fmt.Errorf("%s cannot be edited", name)
		}
	}
	var patch transactionstore.TransactionPatch
	if raw, present := fields["title"]; present {
		value, err := requiredString(raw, "title")
		if err != nil || utf8.RuneCountInString(value) > 250 {
			return patch, errors.New("title must be 1 to 250 characters")
		}
		patch.Title = &value
	}
	if raw, present := fields["account_id"]; present {
		value, err := requiredUUID(raw, "account_id")
		if err != nil {
			return patch, err
		}
		patch.AccountID = &value
	}
	if raw, present := fields["occurred_at"]; present {
		value, err := requiredString(raw, "occurred_at")
		if err != nil {
			return patch, err
		}
		occurredAt, err := time.Parse(time.RFC3339, value)
		if err != nil {
			return patch, errors.New("occurred_at must be an RFC3339 timestamp")
		}
		patch.OccurredAt = &occurredAt
	}
	if raw, present := fields["original_amount_minor"]; present {
		value, err := requiredMinorAmount(raw, "original_amount_minor", false)
		if err != nil {
			return patch, err
		}
		patch.OriginalAmountMinor = &value
	}
	if raw, present := fields["original_currency"]; present {
		value, err := requiredString(raw, "original_currency")
		if err != nil || !validCurrency(value) {
			return patch, errors.New("original_currency must be a three-letter uppercase ISO code")
		}
		patch.OriginalCurrency = &value
	}
	if raw, present := fields["sgd_amount_minor"]; present {
		patch.SGDAmountMinor.Set = true
		if !isJSONNull(raw) {
			value, err := requiredMinorAmount(raw, "sgd_amount_minor", false)
			if err != nil {
				return patch, err
			}
			patch.SGDAmountMinor.Value = &value
		}
	}
	if raw, present := fields["category_id"]; present {
		patch.CategoryID.Set = true
		if !isJSONNull(raw) {
			value, err := requiredUUID(raw, "category_id")
			if err != nil {
				return patch, err
			}
			patch.CategoryID.Value = &value
		}
	}
	if raw, present := fields["line_items"]; present {
		lineItems, err := validateLineItems(raw)
		if err != nil {
			return patch, err
		}
		patch.LineItems = &lineItems
	}
	return patch, nil
}

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
		if err = reconciliation.ValidateLineItem(decoded); err != nil {
			return nil, fmt.Errorf("line_items[%d]: %w", index, err)
		}
		result = append(result, decoded)
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
	var stored []reconciliation.LineItem
	if json.Unmarshal(raw, &stored) != nil {
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
	if len(value) != 3 {
		return false
	}
	for _, character := range value {
		if character < 'A' || character > 'Z' {
			return false
		}
	}
	return true
}

func syncRunResponse(run transactionstore.SyncRun) map[string]any {
	return map[string]any{
		"id": run.ID, "status": run.Status, "messages_discovered": run.MessagesFoundCount,
		"messages_ingested": run.SourcesSavedCount, "sources_parsed": 0,
		"transactions_created": run.TransactionsCreatedCount, "sources_review": run.ReviewRequiredCount,
		"sources_dangling": run.DanglingSourcesCount, "error_summary": run.ErrorSummary,
		"started_at": run.StartedAt, "completed_at": run.CompletedAt,
	}
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

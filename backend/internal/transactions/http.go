// Package transactions exposes only authenticated, operational transaction workflow endpoints.
package transactions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/zhengteck/wealth-builder/backend/internal/attachmentstorage"
	"github.com/zhengteck/wealth-builder/backend/internal/auth"
	"github.com/zhengteck/wealth-builder/backend/internal/parserrules"
	"github.com/zhengteck/wealth-builder/backend/internal/reconciliation"
	"github.com/zhengteck/wealth-builder/backend/internal/transactionstore"
)

type Repository interface {
	CreateSyncRun(context.Context, uuid.UUID, bool) (transactionstore.SyncRun, error)
	GetSyncRun(context.Context, uuid.UUID, uuid.UUID) (transactionstore.SyncRun, error)
	GetLatestSyncRun(context.Context, uuid.UUID) (transactionstore.SyncRun, error)
	GetGmailConnectionStatus(context.Context, uuid.UUID) (transactionstore.GmailConnectionStatus, error)
	ListSourcesPage(context.Context, uuid.UUID, string, *transactionstore.SourcePageCursor, int) (transactionstore.SourcePage, error)
	RetrySourceParse(context.Context, uuid.UUID, uuid.UUID) error
	GetSanitizedEmail(context.Context, uuid.UUID, uuid.UUID) (transactionstore.SanitizedEmail, error)
	ListSourceAttachments(context.Context, uuid.UUID, uuid.UUID) ([]transactionstore.AttachmentRecord, error)
	ListTransactionsPage(context.Context, uuid.UUID, transactionstore.TransactionListFilter) (transactionstore.TransactionPage, error)
	ListTransactionSources(context.Context, uuid.UUID, uuid.UUID) ([]transactionstore.SourceEvidence, error)
	AttachSource(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (uuid.UUID, error)
	CreateTransactionFromSource(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (transactionstore.Transaction, error)
	ListSourceCandidates(context.Context, uuid.UUID, uuid.UUID) ([]transactionstore.SourceCandidateSummary, error)
	AttachSourceCandidate(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID) (uuid.UUID, error)
	CreateTransactionFromSourceCandidate(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID) (transactionstore.Transaction, error)
	UnmatchSourceLink(context.Context, uuid.UUID, uuid.UUID) error
	PatchTransaction(context.Context, uuid.UUID, uuid.UUID, transactionstore.TransactionPatch) (transactionstore.Transaction, error)
	CreateInternalTransfer(context.Context, uuid.UUID, transactionstore.InternalTransferInput) (transactionstore.InternalTransfer, error)
	GetTransactionSettings(context.Context, uuid.UUID) (transactionstore.TransactionSettings, error)
	ListGlobalSourceParserRules(context.Context) ([]transactionstore.GlobalSourceParserRule, error)
	GetGlobalSourceParserRule(context.Context, uuid.UUID) (transactionstore.GlobalSourceParserRule, error)
	CreateGlobalSourceParserRule(context.Context, uuid.UUID, transactionstore.GlobalSourceParserRuleInput) (transactionstore.GlobalSourceParserRule, error)
	UpdateGlobalSourceParserRule(context.Context, uuid.UUID, uuid.UUID, transactionstore.GlobalSourceParserRuleInput) (transactionstore.GlobalSourceParserRule, error)
	GetDefaultParserInstructions(context.Context, uuid.UUID) (transactionstore.DefaultParserInstructions, error)
	GetUserSourceParserRule(context.Context, uuid.UUID, uuid.UUID) (transactionstore.UserSourceParserRule, error)
	ListPromptPreviewSources(context.Context, uuid.UUID, int) ([]transactionstore.PromptPreviewSource, error)
	LoadSourceParseInput(context.Context, uuid.UUID, uuid.UUID) (transactionstore.SourceParseInput, error)
	PutDefaultParserInstructions(context.Context, uuid.UUID, string) (transactionstore.DefaultParserInstructions, error)
	CreateUserSourceParserRule(context.Context, uuid.UUID, transactionstore.UserSourceParserRuleInput) (transactionstore.UserSourceParserRule, error)
	UpdateUserSourceParserRule(context.Context, uuid.UUID, uuid.UUID, transactionstore.UserSourceParserRuleInput) (transactionstore.UserSourceParserRule, error)
	RetireUserSourceParserRule(context.Context, uuid.UUID, uuid.UUID) error
	CreateAccountMatchingKey(context.Context, uuid.UUID, transactionstore.AccountMatchingKeyInput) (transactionstore.AccountMatchingKey, error)
	SetAccountMatchingKeyActive(context.Context, uuid.UUID, uuid.UUID, bool) (transactionstore.AccountMatchingKey, error)
	GetSourceParseDebug(context.Context, uuid.UUID, uuid.UUID) (transactionstore.SourceParseDebug, error)
	GetSourceParseAuditField(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, string) (transactionstore.SourceParseAuditField, error)
	StageSourceDeletion(context.Context, uuid.UUID, uuid.UUID) (transactionstore.SourceDeletionResult, error)
}

type GmailOAuthFlow interface {
	Begin(context.Context, uuid.UUID) (string, error)
	Complete(context.Context, string, string) error
}

type AttachmentStorage interface {
	SignURL(context.Context, attachmentstorage.ObjectRequest, int) (string, error)
}

type Handler struct {
	repository            Repository
	allowDevelopmentToken bool
	gmailOAuth            GmailOAuthFlow
	frontendOrigin        *url.URL
	attachmentStorage     AttachmentStorage
}

func NewHandler(repository Repository, allowDevelopmentToken bool, gmailOAuth GmailOAuthFlow, frontendOrigin *url.URL, attachmentStores ...AttachmentStorage) *Handler {
	var attachmentStore AttachmentStorage
	if len(attachmentStores) > 0 {
		attachmentStore = attachmentStores[0]
	}
	return &Handler{
		repository: repository, allowDevelopmentToken: allowDevelopmentToken,
		gmailOAuth: gmailOAuth, frontendOrigin: frontendOrigin,
		attachmentStorage: attachmentStore,
	}
}

func (h *Handler) Register(mux *http.ServeMux, verifier auth.Verifier) {
	requireUser := func(next http.HandlerFunc) http.Handler { return auth.RequireUser(verifier, next) }
	mux.Handle("POST /v1/transactions/gmail/sync-runs", requireUser(h.createSyncRun))
	mux.Handle("POST /v1/transactions/gmail/connect", requireUser(h.beginGmailConnection))
	mux.Handle("GET /v1/transactions/gmail/connection", requireUser(h.getGmailConnection))
	mux.Handle("GET /v1/transactions/gmail/oauth/callback", http.HandlerFunc(h.completeGmailConnection))
	mux.Handle("GET /v1/transactions/sync-runs/latest", requireUser(h.getLatestSyncRun))
	mux.Handle("GET /v1/transactions/sync-runs/{id}", requireUser(h.getSyncRun))
	mux.Handle("GET /v1/transactions", requireUser(h.listTransactions))
	mux.Handle("GET /v1/transactions/sources", requireUser(h.listSources))
	mux.Handle("GET /v1/transactions/sources/{id}/email", requireUser(h.getSourceEmail))
	mux.Handle("GET /v1/transactions/sources/{id}/attachments", requireUser(h.listSourceAttachments))
	mux.Handle("GET /v1/transactions/sources/{id}/debug", requireUser(h.getSourceDebug))
	mux.Handle("GET /v1/transactions/sources/{id}/debug/attempts/{attempt_id}/fields/{field}", requireUser(h.getSourceDebugField))
	mux.Handle("DELETE /v1/transactions/sources/{id}", requireUser(h.deleteSource))
	mux.Handle("GET /v1/transactions/settings", requireUser(h.getTransactionSettings))
	mux.Handle("GET /v1/transactions/global-settings", requireUser(h.getGlobalSettings))
	mux.Handle("POST /v1/transactions/global-settings/source-rules", requireUser(h.createGlobalSourceRule))
	mux.Handle("PUT /v1/transactions/global-settings/source-rules/{id}", requireUser(h.updateGlobalSourceRule))
	mux.Handle("GET /v1/transactions/prompt-preview/sources", requireUser(h.listPromptPreviewSources))
	mux.Handle("POST /v1/transactions/prompt-preview", requireUser(h.previewPrompt))
	mux.Handle("PUT /v1/transactions/settings/default-instructions", requireUser(h.putDefaultInstructions))
	mux.Handle("POST /v1/transactions/settings/source-rules", requireUser(h.createSourceRule))
	mux.Handle("PUT /v1/transactions/settings/source-rules/{id}", requireUser(h.updateSourceRule))
	mux.Handle("DELETE /v1/transactions/settings/source-rules/{id}", requireUser(h.deleteSourceRule))
	mux.Handle("POST /v1/transactions/settings/matching-keys", requireUser(h.createMatchingKey))
	mux.Handle("PATCH /v1/transactions/settings/matching-keys/{id}", requireUser(h.patchMatchingKey))
	// ServeMux's segment patterns make /{id}/sources ambiguous with the
	// established /sync-runs/{id} route. This narrow subtree handler preserves
	// both public paths while rejecting every other transaction subroute.
	mux.Handle("GET /v1/transactions/", requireUser(h.transactionSubroute))
	mux.Handle("POST /v1/transactions/sources/{id}/attach", requireUser(h.attachSource))
	mux.Handle("POST /v1/transactions/sources/{id}/create-transaction", requireUser(h.createTransactionFromSource))
	mux.Handle("GET /v1/transactions/sources/{id}/candidates", requireUser(h.listSourceCandidates))
	mux.Handle("POST /v1/transactions/sources/{id}/candidates/{candidate_id}/attach", requireUser(h.attachSourceCandidate))
	mux.Handle("POST /v1/transactions/sources/{id}/candidates/{candidate_id}/create-transaction", requireUser(h.createTransactionFromSourceCandidate))
	mux.Handle("POST /v1/transactions/sources/{id}/retry", requireUser(h.retrySourceParse))
	mux.Handle("POST /v1/transactions/source-links/{id}/unmatch", requireUser(h.unmatchSourceLink))
	mux.Handle("POST /v1/transactions/internal-transfers", requireUser(h.createInternalTransfer))
	mux.Handle("PATCH /v1/transactions/{id}", requireUser(h.patchTransaction))
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
	if status == "" {
		status = "dangling"
	}
	if status != "dangling" && status != "review_required" && status != "failed" {
		writeError(w, http.StatusBadRequest, "status must be dangling, review, or failed")
		return
	}
	limit, err := pageLimit(r.URL.Query().Get("limit"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var cursor *transactionstore.SourcePageCursor
	if rawCursor := r.URL.Query().Get("cursor"); rawCursor != "" {
		timestamp, id, decodeErr := decodeCursor(rawCursor, "sources:"+status)
		if decodeErr != nil {
			writeError(w, http.StatusBadRequest, "Invalid source cursor.")
			return
		}
		cursor = &transactionstore.SourcePageCursor{ReceivedAt: timestamp, ID: id}
	}
	page, err := h.repository.ListSourcesPage(r.Context(), user.ID, status, cursor, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not load sources.")
		return
	}
	items := make([]sourceSummaryJSON, 0, len(page.Items))
	for _, source := range page.Items {
		items = append(items, sourceSummaryResponse(source))
	}
	var nextCursor *string
	if page.HasMore && len(page.Items) > 0 {
		last := page.Items[len(page.Items)-1]
		encoded := encodeCursor("sources:"+status, last.ReceivedAt, last.ID)
		nextCursor = &encoded
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": nextCursor})
}

func (h *Handler) listTransactions(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	query := r.URL.Query()
	kind := query.Get("kind")
	if kind != "" && kind != "debit" && kind != "credit" {
		writeError(w, http.StatusBadRequest, "kind must be debit or credit")
		return
	}
	reviewStatus := query.Get("review")
	if reviewStatus != "" && reviewStatus != "confirmed" && reviewStatus != "review_required" && reviewStatus != "pending" {
		writeError(w, http.StatusBadRequest, "review must be confirmed, review_required, or pending")
		return
	}
	search := strings.TrimSpace(query.Get("search"))
	if utf8.RuneCountInString(search) > 100 {
		writeError(w, http.StatusBadRequest, "search must be at most 100 characters")
		return
	}
	var accountID *uuid.UUID
	if rawAccountID := query.Get("account_id"); rawAccountID != "" {
		parsed, err := uuid.Parse(rawAccountID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "account_id must be a UUID")
			return
		}
		accountID = &parsed
	}
	limit, err := pageLimit(query.Get("limit"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	scope := transactionCursorScope(kind, reviewStatus, accountID, search)
	var cursor *transactionstore.TransactionPageCursor
	if rawCursor := query.Get("cursor"); rawCursor != "" {
		timestamp, id, decodeErr := decodeCursor(rawCursor, scope)
		if decodeErr != nil {
			writeError(w, http.StatusBadRequest, "Invalid transaction cursor.")
			return
		}
		cursor = &transactionstore.TransactionPageCursor{OccurredAt: timestamp, ID: id}
	}
	page, err := h.repository.ListTransactionsPage(r.Context(), user.ID, transactionstore.TransactionListFilter{
		Kind: kind, ReviewStatus: reviewStatus, AccountID: accountID,
		Search: search, Cursor: cursor, Limit: limit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not load transactions.")
		return
	}
	items := make([]transactionListJSON, 0, len(page.Items))
	for _, transaction := range page.Items {
		items = append(items, transactionListResponse(transaction))
	}
	var nextCursor *string
	if page.HasMore && len(page.Items) > 0 {
		last := page.Items[len(page.Items)-1]
		encoded := encodeCursor(scope, last.OccurredAt, last.ID)
		nextCursor = &encoded
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": nextCursor})
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

func (h *Handler) listSourceAttachments(w http.ResponseWriter, r *http.Request) {
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
	if h.attachmentStorage == nil {
		writeError(w, http.StatusServiceUnavailable, "Attachment access is not available.")
		return
	}
	attachments, err := h.repository.ListSourceAttachments(r.Context(), user.ID, sourceID)
	if errors.Is(err, transactionstore.ErrSourceNotFound) {
		writeError(w, http.StatusNotFound, "Source not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not load source attachments.")
		return
	}
	items := make([]attachmentJSON, 0, len(attachments))
	for _, attachment := range attachments {
		request, requestErr := attachmentstorage.ObjectRequestFromPath(user.ID, attachment.ObjectPath)
		if requestErr != nil {
			writeError(w, http.StatusBadGateway, "Could not prepare attachment access.")
			return
		}
		signedURL, signErr := h.attachmentStorage.SignURL(r.Context(), request, attachmentURLExpirySeconds)
		if signErr != nil {
			writeError(w, http.StatusBadGateway, "Could not prepare attachment access.")
			return
		}
		items = append(items, attachmentJSON{
			ID: attachment.ID, Filename: attachment.Filename, MIMEType: attachment.MIMEType,
			ByteSize: attachment.ByteSize, SHA256: attachment.SHA256,
			ParseEligible: attachment.ParseEligible, ParseStatus: attachment.ParseStatus,
			StorageStatus: "stored", SignedURL: signedURL,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": nil})
}

func (h *Handler) getSourceDebug(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
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
	debug, err := h.repository.GetSourceParseDebug(r.Context(), user.ID, sourceID)
	if errors.Is(err, transactionstore.ErrSourceNotFound) {
		writeError(w, http.StatusNotFound, "Source not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not load source debug information.")
		return
	}
	writeJSON(w, http.StatusOK, debug)
}

func (h *Handler) getSourceDebugField(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	sourceID, sourceErr := uuid.Parse(r.PathValue("id"))
	attemptID, attemptErr := uuid.Parse(r.PathValue("attempt_id"))
	if sourceErr != nil || attemptErr != nil || sourceID == uuid.Nil || attemptID == uuid.Nil {
		writeError(w, http.StatusNotFound, "Debug field not found.")
		return
	}
	field, err := h.repository.GetSourceParseAuditField(
		r.Context(), user.ID, sourceID, attemptID, r.PathValue("field"),
	)
	if errors.Is(err, transactionstore.ErrSourceDebugFieldUnsupported) {
		writeError(w, http.StatusBadRequest, "Unsupported debug field.")
		return
	}
	if errors.Is(err, transactionstore.ErrSourceNotFound) {
		writeError(w, http.StatusNotFound, "Debug field not found.")
		return
	}
	if errors.Is(err, transactionstore.ErrSourceDebugFieldTooLarge) {
		writeError(w, http.StatusUnprocessableEntity, "Stored debug field exceeds its permitted size.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not load source debug field.")
		return
	}
	writeJSON(w, http.StatusOK, field)
}

func (h *Handler) deleteSource(w http.ResponseWriter, r *http.Request) {
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
	result, err := h.repository.StageSourceDeletion(r.Context(), user.ID, sourceID)
	if errors.Is(err, transactionstore.ErrSourceNotFound) {
		writeError(w, http.StatusNotFound, "Source not found.")
		return
	}
	if errors.Is(err, transactionstore.ErrSourceDeletionIngestionActive) {
		writeError(w, http.StatusConflict, "Wait for Gmail sync to finish before deleting this source.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not stage source deletion.")
		return
	}
	status := http.StatusOK
	if result.CleanupPending {
		status = http.StatusAccepted
	}
	writeJSON(w, status, result)
}

func (h *Handler) getTransactionSettings(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	settings, err := h.repository.GetTransactionSettings(r.Context(), user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not load transaction settings.")
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (h *Handler) putDefaultInstructions(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	var request struct {
		DefaultInstructions *string `json:"default_instructions"`
	}
	if err := decodeRequestJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid default-instructions request.")
		return
	}
	if request.DefaultInstructions == nil {
		writeError(w, http.StatusBadRequest, "default_instructions is required")
		return
	}
	instructions := strings.TrimSpace(*request.DefaultInstructions)
	if utf8.RuneCountInString(instructions) > 4000 {
		writeError(w, http.StatusBadRequest, "default_instructions must be at most 4000 characters")
		return
	}
	saved, err := h.repository.PutDefaultParserInstructions(r.Context(), user.ID, instructions)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not save default parser instructions.")
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

type sourceRuleRequest struct {
	Name             string  `json:"name"`
	Provider         string  `json:"provider"`
	SenderMatchType  string  `json:"sender_match_type"`
	SenderMatchValue string  `json:"sender_match_value"`
	SubjectMatcher   *string `json:"subject_matcher"`
	ContentMatcher   *string `json:"content_matcher"`
	PromptFragment   string  `json:"prompt_fragment"`
	Priority         int64   `json:"priority"`
	Active           *bool   `json:"active"`
}

func (h *Handler) createSourceRule(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	input, err := decodeSourceRuleRequest(w, r)
	if err != nil {
		return
	}
	rule, err := h.repository.CreateUserSourceParserRule(r.Context(), user.ID, input)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not create source rule.")
		return
	}
	writeJSON(w, http.StatusCreated, rule)
}

func (h *Handler) updateSourceRule(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	ruleID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "Source rule not found.")
		return
	}
	input, err := decodeSourceRuleRequest(w, r)
	if err != nil {
		return
	}
	rule, err := h.repository.UpdateUserSourceParserRule(r.Context(), user.ID, ruleID, input)
	if errors.Is(err, transactionstore.ErrUserSourceRuleNotFound) {
		writeError(w, http.StatusNotFound, "Source rule not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not update source rule.")
		return
	}
	writeJSON(w, http.StatusOK, rule)
}

func (h *Handler) deleteSourceRule(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	ruleID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "Source rule not found.")
		return
	}
	err = h.repository.RetireUserSourceParserRule(r.Context(), user.ID, ruleID)
	if errors.Is(err, transactionstore.ErrUserSourceRuleNotFound) {
		writeError(w, http.StatusNotFound, "Source rule not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not delete source rule.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func decodeSourceRuleRequest(w http.ResponseWriter, r *http.Request) (transactionstore.UserSourceParserRuleInput, error) {
	var request sourceRuleRequest
	if err := decodeRequestJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid source-rule request.")
		return transactionstore.UserSourceParserRuleInput{}, err
	}
	input, err := request.validate()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return transactionstore.UserSourceParserRuleInput{}, err
	}
	return input, nil
}

func (request sourceRuleRequest) validate() (transactionstore.UserSourceParserRuleInput, error) {
	name := strings.TrimSpace(request.Name)
	if length := utf8.RuneCountInString(name); length < 1 || length > 100 {
		return transactionstore.UserSourceParserRuleInput{}, errors.New("name must be between 1 and 100 characters")
	}
	if request.Provider != "gmail" {
		return transactionstore.UserSourceParserRuleInput{}, errors.New("provider must be gmail")
	}
	if request.SenderMatchType != "exact" && request.SenderMatchType != "domain" && request.SenderMatchType != "regex" {
		return transactionstore.UserSourceParserRuleInput{}, errors.New("sender_match_type must be exact, domain, or regex")
	}
	senderValue := strings.TrimSpace(request.SenderMatchValue)
	if length := utf8.RuneCountInString(senderValue); length < 1 || length > 500 {
		return transactionstore.UserSourceParserRuleInput{}, errors.New("sender_match_value must be between 1 and 500 characters")
	}
	switch request.SenderMatchType {
	case "exact":
		address, err := mail.ParseAddress(senderValue)
		if err != nil || !strings.Contains(address.Address, "@") {
			return transactionstore.UserSourceParserRuleInput{}, errors.New("sender_match_value must be a valid email address for exact matching")
		}
	case "domain":
		domain := strings.TrimPrefix(strings.ToLower(senderValue), "@")
		valid, _ := regexp.MatchString(`^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$`, domain)
		if !valid || !strings.Contains(domain, ".") || strings.Contains(domain, "..") {
			return transactionstore.UserSourceParserRuleInput{}, errors.New("sender_match_value must be a valid domain")
		}
		senderValue = domain
	case "regex":
		if err := parserrules.ValidateRE2(senderValue); err != nil {
			return transactionstore.UserSourceParserRuleInput{}, errors.New("sender_match_value must be a valid RE2 expression")
		}
	}
	subject, err := validatedOptionalMatcher(request.SubjectMatcher, "subject_matcher")
	if err != nil {
		return transactionstore.UserSourceParserRuleInput{}, err
	}
	content, err := validatedOptionalMatcher(request.ContentMatcher, "content_matcher")
	if err != nil {
		return transactionstore.UserSourceParserRuleInput{}, err
	}
	prompt := strings.TrimSpace(request.PromptFragment)
	if utf8.RuneCountInString(prompt) > 4000 {
		return transactionstore.UserSourceParserRuleInput{}, errors.New("prompt_fragment must be at most 4000 characters")
	}
	if request.Priority < math.MinInt32 || request.Priority > math.MaxInt32 {
		return transactionstore.UserSourceParserRuleInput{}, errors.New("priority is outside the supported range")
	}
	if request.Active == nil {
		return transactionstore.UserSourceParserRuleInput{}, errors.New("active is required")
	}
	return transactionstore.UserSourceParserRuleInput{
		Name: name, Provider: request.Provider, SenderMatchType: request.SenderMatchType,
		SenderMatchValue: senderValue, SubjectMatcher: subject, ContentMatcher: content,
		PromptFragment: prompt, Priority: int(request.Priority), Active: *request.Active,
	}, nil
}

func validatedOptionalMatcher(value *string, field string) (*string, error) {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil, nil
	}
	trimmed := strings.TrimSpace(*value)
	if utf8.RuneCountInString(trimmed) > 1000 {
		return nil, fmt.Errorf("%s must be at most 1000 characters", field)
	}
	if parserrules.ValidateRE2(trimmed) != nil {
		return nil, fmt.Errorf("%s must be a valid RE2 expression", field)
	}
	return &trimmed, nil
}

func (h *Handler) createMatchingKey(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	var request struct {
		AccountID    string `json:"account_id"`
		KeyType      string `json:"key_type"`
		DisplayValue string `json:"display_value"`
	}
	if err := decodeRequestJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid matching-key request.")
		return
	}
	accountID, err := uuid.Parse(request.AccountID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "account_id must be a UUID")
		return
	}
	display := strings.TrimSpace(request.DisplayValue)
	if length := utf8.RuneCountInString(display); length < 1 || length > 100 {
		writeError(w, http.StatusBadRequest, "display_value must be between 1 and 100 characters")
		return
	}
	if _, err = reconciliation.NormalizeAccountMatchingKey(request.KeyType, display); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	key, err := h.repository.CreateAccountMatchingKey(r.Context(), user.ID, transactionstore.AccountMatchingKeyInput{
		AccountID: accountID, KeyType: request.KeyType, DisplayValue: display,
	})
	if errors.Is(err, transactionstore.ErrAccountNotFound) {
		writeError(w, http.StatusUnprocessableEntity, "The selected account is unavailable.")
		return
	}
	if errors.Is(err, transactionstore.ErrMatchingKeyConflict) {
		writeError(w, http.StatusConflict, "That matching key already exists.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not create matching key.")
		return
	}
	writeJSON(w, http.StatusCreated, key)
}

func (h *Handler) patchMatchingKey(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	keyID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "Matching key not found.")
		return
	}
	var request struct {
		Active *bool `json:"active"`
	}
	if err := decodeRequestJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid matching-key request.")
		return
	}
	if request.Active == nil {
		writeError(w, http.StatusBadRequest, "active is required")
		return
	}
	key, err := h.repository.SetAccountMatchingKeyActive(r.Context(), user.ID, keyID, *request.Active)
	if errors.Is(err, transactionstore.ErrMatchingKeyNotFound) {
		writeError(w, http.StatusNotFound, "Matching key not found.")
		return
	}
	if errors.Is(err, transactionstore.ErrMatchingKeyConflict) {
		writeError(w, http.StatusConflict, "That matching key cannot be reactivated.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not update matching key.")
		return
	}
	writeJSON(w, http.StatusOK, key)
}

func (h *Handler) retrySourceParse(w http.ResponseWriter, r *http.Request) {
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
	err = h.repository.RetrySourceParse(r.Context(), user.ID, sourceID)
	if writeActionError(w, err, "Source", "Could not retry source parsing.") {
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
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

func (h *Handler) listSourceCandidates(w http.ResponseWriter, r *http.Request) {
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
	candidates, err := h.repository.ListSourceCandidates(r.Context(), user.ID, sourceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not load source candidates.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"candidates": candidates})
}

func (h *Handler) attachSourceCandidate(w http.ResponseWriter, r *http.Request) {
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
	candidateID, err := uuid.Parse(r.PathValue("candidate_id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "Candidate not found.")
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
	linkID, err := h.repository.AttachSourceCandidate(r.Context(), user.ID, sourceID, candidateID, transactionID)
	if writeActionError(w, err, "Candidate", "Could not attach candidate.") {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"source_link_id": linkID.String()})
}

func (h *Handler) createTransactionFromSourceCandidate(w http.ResponseWriter, r *http.Request) {
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
	candidateID, err := uuid.Parse(r.PathValue("candidate_id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "Candidate not found.")
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
	transaction, err := h.repository.CreateTransactionFromSourceCandidate(r.Context(), user.ID, sourceID, candidateID, accountID)
	if writeActionError(w, err, "Candidate", "Could not create transaction from candidate.") {
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

func (h *Handler) createInternalTransfer(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	var request internalTransferRequest
	if err := decodeRequestJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid internal transfer: "+safeValidationMessage(err))
		return
	}
	debit, err := request.Debit.toStoreInput("debit")
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid internal transfer: "+err.Error())
		return
	}
	credit, err := request.Credit.toStoreInput("credit")
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid internal transfer: "+err.Error())
		return
	}
	if debit.AccountID == credit.AccountID {
		writeError(w, http.StatusBadRequest, "Invalid internal transfer: debit and credit accounts must be different")
		return
	}
	transfer, err := h.repository.CreateInternalTransfer(r.Context(), user.ID, transactionstore.InternalTransferInput{
		Debit: debit, Credit: credit,
	})
	if errors.Is(err, transactionstore.ErrAccountNotFound) ||
		errors.Is(err, transactionstore.ErrTransferSameAccount) ||
		errors.Is(err, transactionstore.ErrCategoryNotFound) ||
		errors.Is(err, transactionstore.ErrSourceNotFound) ||
		errors.Is(err, transactionstore.ErrSourceNotActionable) ||
		errors.Is(err, transactionstore.ErrSourceAlreadyLinked) {
		writeError(w, http.StatusUnprocessableEntity, "An Account, category, or source is unavailable for this transfer.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not create internal transfer.")
		return
	}
	writeJSON(w, http.StatusCreated, internalTransferResponse(transfer))
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
	if errors.Is(err, transactionstore.ErrSourceNotFound) || errors.Is(err, transactionstore.ErrTransactionNotFound) || errors.Is(err, transactionstore.ErrSourceLinkNotFound) || errors.Is(err, transactionstore.ErrCandidateNotFound) {
		writeError(w, http.StatusNotFound, resource+" not found.")
		return true
	}
	if errors.Is(err, transactionstore.ErrSourceNotActionable) || errors.Is(err, transactionstore.ErrSourceAlreadyLinked) {
		writeError(w, http.StatusConflict, "This source is no longer available for that action.")
		return true
	}
	if errors.Is(err, transactionstore.ErrAccountNotFound) {
		writeError(w, http.StatusUnprocessableEntity, "The selected account is unavailable.")
		return true
	}
	if errors.Is(err, transactionstore.ErrCategoryNotFound) {
		writeError(w, http.StatusUnprocessableEntity, "The selected category is unavailable.")
		return true
	}
	if errors.Is(err, transactionstore.ErrTransferSameAccount) {
		writeError(w, http.StatusUnprocessableEntity, "Internal transfer legs must use different accounts.")
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
		"title": true, "merchant_name": true, "user_notes": true,
		"account_id": true, "occurred_at": true, "original_amount_minor": true,
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
	if raw, present := fields["merchant_name"]; present {
		patch.MerchantName.Set = true
		if !isJSONNull(raw) {
			value, err := requiredString(raw, "merchant_name")
			if err != nil || utf8.RuneCountInString(value) > 250 {
				return patch, errors.New("merchant_name must be null or a non-empty string of at most 250 characters")
			}
			patch.MerchantName.Value = &value
		}
	}
	if raw, present := fields["user_notes"]; present {
		patch.UserNotes.Set = true
		if !isJSONNull(raw) {
			var value string
			if json.Unmarshal(raw, &value) != nil {
				return patch, errors.New("user_notes must be a string or null")
			}
			value = strings.TrimSpace(value)
			if utf8.RuneCountInString(value) > 4000 {
				return patch, errors.New("user_notes must be at most 4000 characters")
			}
			if value != "" {
				patch.UserNotes.Value = &value
			}
		}
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

type internalTransferRequest struct {
	Debit  transferLegRequest `json:"debit"`
	Credit transferLegRequest `json:"credit"`
}

type transferLegRequest struct {
	AccountID           string          `json:"account_id"`
	Title               string          `json:"title"`
	MerchantName        *string         `json:"merchant_name"`
	OriginalAmountMinor json.RawMessage `json:"original_amount_minor"`
	OriginalCurrency    string          `json:"original_currency"`
	SGDAmountMinor      json.RawMessage `json:"sgd_amount_minor"`
	OccurredAt          string          `json:"occurred_at"`
	CategoryID          *string         `json:"category_id"`
	LineItems           json.RawMessage `json:"line_items"`
	SourceIDs           []string        `json:"source_ids"`
}

func (request transferLegRequest) toStoreInput(legName string) (transactionstore.TransferLegInput, error) {
	field := func(name string) string { return legName + "." + name }
	accountID, err := uuid.Parse(strings.TrimSpace(request.AccountID))
	if err != nil || accountID == uuid.Nil {
		return transactionstore.TransferLegInput{}, fmt.Errorf("%s must be a UUID", field("account_id"))
	}
	title := strings.TrimSpace(request.Title)
	if title == "" || utf8.RuneCountInString(title) > 250 {
		return transactionstore.TransferLegInput{}, fmt.Errorf("%s must be 1 to 250 characters", field("title"))
	}
	var merchantName *string
	if request.MerchantName != nil {
		value := strings.TrimSpace(*request.MerchantName)
		if value == "" || utf8.RuneCountInString(value) > 250 {
			return transactionstore.TransferLegInput{}, fmt.Errorf("%s must be 1 to 250 characters", field("merchant_name"))
		}
		merchantName = &value
	}
	originalAmount, err := requiredMinorAmount(request.OriginalAmountMinor, field("original_amount_minor"), false)
	if err != nil {
		return transactionstore.TransferLegInput{}, err
	}
	currency := strings.TrimSpace(request.OriginalCurrency)
	if !validCurrency(currency) {
		return transactionstore.TransferLegInput{}, fmt.Errorf("%s must be a three-letter uppercase ISO code", field("original_currency"))
	}
	var sgdAmount *int64
	if len(request.SGDAmountMinor) > 0 && !isJSONNull(request.SGDAmountMinor) {
		value, amountErr := requiredMinorAmount(request.SGDAmountMinor, field("sgd_amount_minor"), false)
		if amountErr != nil {
			return transactionstore.TransferLegInput{}, amountErr
		}
		sgdAmount = &value
	}
	occurredAt, err := time.Parse(time.RFC3339, strings.TrimSpace(request.OccurredAt))
	if err != nil {
		return transactionstore.TransferLegInput{}, fmt.Errorf("%s must be an RFC3339 timestamp", field("occurred_at"))
	}
	var categoryID *uuid.UUID
	if request.CategoryID != nil {
		value, parseErr := uuid.Parse(strings.TrimSpace(*request.CategoryID))
		if parseErr != nil || value == uuid.Nil {
			return transactionstore.TransferLegInput{}, fmt.Errorf("%s must be a UUID or null", field("category_id"))
		}
		categoryID = &value
	}
	lineItems := json.RawMessage("[]")
	if len(request.LineItems) > 0 {
		lineItems, err = validateLineItems(request.LineItems)
		if err != nil {
			return transactionstore.TransferLegInput{}, fmt.Errorf("%s: %w", legName, err)
		}
	}
	if len(request.SourceIDs) > 20 {
		return transactionstore.TransferLegInput{}, fmt.Errorf("%s may contain at most 20 source_ids", legName)
	}
	sourceIDs := make([]uuid.UUID, 0, len(request.SourceIDs))
	for index, rawID := range request.SourceIDs {
		value, parseErr := uuid.Parse(strings.TrimSpace(rawID))
		if parseErr != nil || value == uuid.Nil {
			return transactionstore.TransferLegInput{}, fmt.Errorf("%s.source_ids[%d] must be a UUID", legName, index)
		}
		sourceIDs = append(sourceIDs, value)
	}
	return transactionstore.TransferLegInput{
		AccountID: accountID, Title: title, MerchantName: merchantName,
		OriginalAmountMinor: originalAmount, OriginalCurrency: currency,
		SGDAmountMinor: sgdAmount, OccurredAt: occurredAt, CategoryID: categoryID,
		LineItems: lineItems, SourceIDs: sourceIDs,
	}, nil
}

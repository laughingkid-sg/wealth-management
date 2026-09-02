// Package transactions exposes only authenticated, operational transaction workflow endpoints.
package transactions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/zhengteck/wealth-builder/backend/internal/auth"
	"github.com/zhengteck/wealth-builder/backend/internal/transactionstore"
)

type Repository interface {
	CreateSyncRun(context.Context, uuid.UUID, bool) (transactionstore.SyncRun, error)
	GetSyncRun(context.Context, uuid.UUID, uuid.UUID) (transactionstore.SyncRun, error)
	ListSources(context.Context, uuid.UUID, string) ([]transactionstore.SourceSummary, error)
	GetSanitizedEmail(context.Context, uuid.UUID, uuid.UUID) (transactionstore.SanitizedEmail, error)
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

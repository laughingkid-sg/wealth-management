package transactions

import (
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/zhengteck/wealth-builder/backend/internal/auth"
	"github.com/zhengteck/wealth-builder/backend/internal/transactionstore"
)

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

func (h *Handler) getGmailConnection(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	connection, err := h.repository.GetGmailConnectionStatus(r.Context(), user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not load Gmail connection.")
		return
	}
	writeJSON(w, http.StatusOK, connection)
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
	if errors.Is(err, transactionstore.ErrSyncRunInProgress) {
		writeError(w, http.StatusConflict, "A Gmail refresh is already in progress.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not start Gmail refresh.")
		return
	}
	writeJSON(w, http.StatusAccepted, syncRunResponse(run))
}

func (h *Handler) getLatestSyncRun(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	run, err := h.repository.GetLatestSyncRun(r.Context(), user.ID)
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

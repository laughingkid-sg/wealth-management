package transactions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/zhengteck/wealth-builder/backend/internal/auth"
	"github.com/zhengteck/wealth-builder/backend/internal/scriptstore"
)

// ScriptRepository is the script-management surface (satisfied by *scriptstore.Store).
type ScriptRepository interface {
	ListScripts(context.Context) ([]scriptstore.ScriptSummary, error)
	ListVersions(context.Context, string) ([]scriptstore.ScriptVersion, error)
	GetVersion(context.Context, string, int) (scriptstore.ScriptVersion, error)
	CreateVersion(context.Context, string, string, string, uuid.UUID) (scriptstore.ScriptVersion, error)
	Activate(context.Context, string, int) error
}

// ScriptRunner runs a script for dry-runs (satisfied by *scriptengine.Engine).
type ScriptRunner interface {
	Run(context.Context, string, json.RawMessage) (json.RawMessage, error)
}

func scriptVersionResponse(v scriptstore.ScriptVersion) map[string]any {
	return map[string]any{
		"script_key": v.Key, "version": v.Version, "source": v.Source, "checksum": v.Checksum,
		"is_active": v.IsActive, "notes": v.Notes, "created_at": v.CreatedAt, "updated_at": v.UpdatedAt,
	}
}

func (h *Handler) scriptsReady(w http.ResponseWriter) bool {
	if h.scripts == nil {
		writeError(w, http.StatusServiceUnavailable, "Script management is not enabled.")
		return false
	}
	return true
}

func (h *Handler) listScripts(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.UserFromContext(r.Context()); !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	if !h.scriptsReady(w) {
		return
	}
	summaries, err := h.scripts.ListScripts(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not list scripts.")
		return
	}
	items := make([]map[string]any, 0, len(summaries))
	for _, s := range summaries {
		items = append(items, map[string]any{"script_key": s.Key, "active_version": s.ActiveVersion, "version_count": s.VersionCount})
	}
	writeJSON(w, http.StatusOK, map[string]any{"scripts": items})
}

func (h *Handler) listScriptVersions(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.UserFromContext(r.Context()); !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	if !h.scriptsReady(w) {
		return
	}
	key := r.PathValue("key")
	if !scriptstore.ValidateKey(key) {
		writeError(w, http.StatusBadRequest, "Invalid script key.")
		return
	}
	versions, err := h.scripts.ListVersions(r.Context(), key)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not list script versions.")
		return
	}
	items := make([]map[string]any, 0, len(versions))
	for _, v := range versions {
		items = append(items, scriptVersionResponse(v))
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": items})
}

func (h *Handler) getScriptVersion(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.UserFromContext(r.Context()); !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	if !h.scriptsReady(w) {
		return
	}
	key := r.PathValue("key")
	version, err := strconv.Atoi(r.PathValue("version"))
	if !scriptstore.ValidateKey(key) || err != nil || version < 1 {
		writeError(w, http.StatusNotFound, "Script version not found.")
		return
	}
	v, err := h.scripts.GetVersion(r.Context(), key, version)
	if errors.Is(err, scriptstore.ErrScriptNotFound) {
		writeError(w, http.StatusNotFound, "Script version not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not load script version.")
		return
	}
	writeJSON(w, http.StatusOK, scriptVersionResponse(v))
}

func (h *Handler) createScriptVersion(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	if !h.scriptsReady(w) {
		return
	}
	key := r.PathValue("key")
	if !scriptstore.ValidateKey(key) {
		writeError(w, http.StatusBadRequest, "Invalid script key.")
		return
	}
	var request struct {
		Source string `json:"source"`
		Notes  string `json:"notes"`
	}
	if err := decodeRequestJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "source is required.")
		return
	}
	v, err := h.scripts.CreateVersion(r.Context(), key, request.Source, request.Notes, user.ID)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "Could not create script version: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, scriptVersionResponse(v))
}

func (h *Handler) activateScriptVersion(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.UserFromContext(r.Context()); !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	if !h.scriptsReady(w) {
		return
	}
	key := r.PathValue("key")
	if !scriptstore.ValidateKey(key) {
		writeError(w, http.StatusBadRequest, "Invalid script key.")
		return
	}
	var request struct {
		Version int `json:"version"`
	}
	if err := decodeRequestJSON(w, r, &request); err != nil || request.Version < 1 {
		writeError(w, http.StatusBadRequest, "version is required.")
		return
	}
	err := h.scripts.Activate(r.Context(), key, request.Version)
	if errors.Is(err, scriptstore.ErrScriptNotFound) {
		writeError(w, http.StatusNotFound, "Script version not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not activate script version.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"script_key": key, "active_version": request.Version})
}

// dryRunScript runs a draft script against sample input without persisting.
// The engine sandbox bounds execution; failures return the sandboxed error.
func (h *Handler) dryRunScript(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.UserFromContext(r.Context()); !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	if h.engine == nil {
		writeError(w, http.StatusServiceUnavailable, "Script dry-run is not enabled.")
		return
	}
	var request struct {
		Source string          `json:"source"`
		Input  json.RawMessage `json:"input"`
	}
	if err := decodeRequestJSON(w, r, &request); err != nil || request.Source == "" {
		writeError(w, http.StatusBadRequest, "source is required.")
		return
	}
	if len(request.Input) > 0 && !json.Valid(request.Input) {
		writeError(w, http.StatusBadRequest, "input must be valid JSON.")
		return
	}
	output, err := h.engine.Run(r.Context(), request.Source, request.Input)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "output": json.RawMessage(output)})
}

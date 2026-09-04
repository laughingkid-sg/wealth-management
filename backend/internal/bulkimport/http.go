package bulkimport

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/zhengteck/wealth-builder/backend/internal/auth"
)

const maxRequestBytes = 1 << 20

type Application interface {
	ListTemplates(context.Context, uuid.UUID, bool) ([]Template, error)
	CreateTemplate(context.Context, uuid.UUID, TemplateInput) (Template, error)
	UpdateTemplate(context.Context, uuid.UUID, uuid.UUID, TemplateInput) (Template, error)
	SetTemplateArchived(context.Context, uuid.UUID, uuid.UUID, bool) (Template, error)
	CreateBatch(context.Context, uuid.UUID, uuid.UUID, []uuid.UUID) (Batch, error)
	ListBatches(context.Context, uuid.UUID, *BatchCursor, int) (BatchPage, error)
	GetBatch(context.Context, uuid.UUID, uuid.UUID) (Batch, error)
	ReserveFile(context.Context, uuid.UUID, uuid.UUID, ReservationInput) (Reservation, error)
	FinalizeFile(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (File, error)
	ReplaceDocumentLayout(context.Context, uuid.UUID, uuid.UUID, []DocumentLayout) (Batch, error)
	SubmitBatch(context.Context, uuid.UUID, uuid.UUID) (Batch, error)
	CancelBatch(context.Context, uuid.UUID, uuid.UUID) (Batch, error)
	RetryDocument(context.Context, uuid.UUID, uuid.UUID) (Document, error)
	DeleteDocument(context.Context, uuid.UUID, uuid.UUID) error
	DeleteBatch(context.Context, uuid.UUID, uuid.UUID) error
	ListCandidates(context.Context, uuid.UUID, uuid.UUID) ([]Candidate, error)
	ResolveCandidate(context.Context, uuid.UUID, uuid.UUID, CandidateResolution) (Candidate, error)
	PreviewPrompt(context.Context, uuid.UUID, uuid.UUID, []uuid.UUID) (PromptPreview, error)
	ListDebugAttempts(context.Context, uuid.UUID, uuid.UUID) ([]DebugAttempt, error)
	GetDebugAttemptField(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, string) (DebugField, error)
	GetDocumentEvidence(context.Context, uuid.UUID, uuid.UUID) ([]EvidenceFile, error)
}

type Handler struct{ Application Application }

func (h Handler) Register(mux *http.ServeMux, verifier auth.Verifier) {
	requireUser := func(next http.HandlerFunc) http.Handler { return auth.RequireUser(verifier, next) }
	mux.Handle("GET /v1/transactions/bulk-import/templates", requireUser(h.templates))
	mux.Handle("POST /v1/transactions/bulk-import/templates", requireUser(h.templates))
	mux.Handle("PATCH /v1/transactions/bulk-import/templates/{id}", requireUser(h.templateUpdate))
	mux.Handle("POST /v1/transactions/bulk-import/templates/{id}/archive", requireUser(h.templateArchive(true)))
	mux.Handle("POST /v1/transactions/bulk-import/templates/{id}/restore", requireUser(h.templateArchive(false)))
	mux.Handle("GET /v1/transactions/bulk-import/batches", requireUser(h.batches))
	mux.Handle("POST /v1/transactions/bulk-import/batches", requireUser(h.batches))
	mux.Handle("GET /v1/transactions/bulk-import/batches/{id}", requireUser(h.batch))
	mux.Handle("DELETE /v1/transactions/bulk-import/batches/{id}", requireUser(h.batch))
	mux.Handle("POST /v1/transactions/bulk-import/batches/{id}/files/reservations", requireUser(h.reserveFile))
	mux.Handle("POST /v1/transactions/bulk-import/batches/{id}/files/{file_id}/finalize", requireUser(h.finalizeFile))
	mux.Handle("PUT /v1/transactions/bulk-import/batches/{id}/documents", requireUser(h.replaceDocuments))
	mux.Handle("POST /v1/transactions/bulk-import/batches/{id}/submit", requireUser(h.batchAction("submit")))
	mux.Handle("POST /v1/transactions/bulk-import/batches/{id}/cancel", requireUser(h.batchAction("cancel")))
	mux.Handle("GET /v1/transactions/bulk-import/batches/{id}/candidates", requireUser(h.candidates))
	mux.Handle("POST /v1/transactions/bulk-import/candidates/{id}/resolve", requireUser(h.resolveCandidate))
	mux.Handle("POST /v1/transactions/bulk-import/documents/{id}/retry", requireUser(h.retryDocument))
	mux.Handle("DELETE /v1/transactions/bulk-import/documents/{id}", requireUser(h.deleteDocument))
	mux.Handle("POST /v1/transactions/bulk-import/prompt-preview", requireUser(h.promptPreview))
	mux.Handle("GET /v1/transactions/sources/{id}/debug/bulk-attempts", requireUser(h.debugAttempts))
	mux.Handle("GET /v1/transactions/sources/{id}/debug/bulk-attempts/{attempt_id}/fields/{field}", requireUser(h.debugAttemptField))
	mux.Handle("GET /v1/bulk-import/documents/{id}", requireUser(h.documentEvidence))
}

func (h Handler) documentEvidence(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(r)
	if !ok {
		writeAPIError(w, errors.New("unauthorized"))
		return
	}
	documentID, err := pathUUID(r, "id")
	if err != nil {
		writeAPIError(w, ErrNotFound)
		return
	}
	items, err := h.Application.GetDocumentEvidence(r.Context(), user.ID, documentID)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeAPIJSON(w, http.StatusOK, map[string]any{"document_id": documentID, "items": items})
}

func currentUser(r *http.Request) (auth.User, bool) { return auth.UserFromContext(r.Context()) }

func (h Handler) templates(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(r)
	if !ok {
		writeAPIError(w, errors.New("unauthorized"))
		return
	}
	if r.Method == http.MethodGet {
		include, err := strconv.ParseBool(defaultString(r.URL.Query().Get("include_archived"), "false"))
		if err != nil {
			writeAPIError(w, ErrInvalid)
			return
		}
		items, err := h.Application.ListTemplates(r.Context(), user.ID, include)
		if err != nil {
			writeAPIError(w, err)
			return
		}
		writeAPIJSON(w, http.StatusOK, map[string]any{"items": items})
		return
	}
	input, err := decodeTemplateRequest(w, r, false)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	created, err := h.Application.CreateTemplate(r.Context(), user.ID, input)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusCreated, created)
}

func (h Handler) templateUpdate(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(r)
	if !ok {
		writeAPIError(w, errors.New("unauthorized"))
		return
	}
	id, err := pathUUID(r, "id")
	if err != nil {
		writeAPIError(w, ErrNotFound)
		return
	}
	input, err := decodeTemplateRequest(w, r, true)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	updated, err := h.Application.UpdateTemplate(r.Context(), user.ID, id, input)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, updated)
}

func (h Handler) templateArchive(archived bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := currentUser(r)
		if !ok {
			writeAPIError(w, errors.New("unauthorized"))
			return
		}
		id, err := pathUUID(r, "id")
		if err != nil {
			writeAPIError(w, ErrNotFound)
			return
		}
		if err = decodeEmptyBody(w, r); err != nil {
			writeAPIError(w, err)
			return
		}
		result, err := h.Application.SetTemplateArchived(r.Context(), user.ID, id, archived)
		if err != nil {
			writeAPIError(w, err)
			return
		}
		writeAPIJSON(w, http.StatusOK, result)
	}
}

func (h Handler) batches(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(r)
	if !ok {
		writeAPIError(w, errors.New("unauthorized"))
		return
	}
	if r.Method == http.MethodGet {
		limit, err := parseLimit(r.URL.Query().Get("limit"), 25)
		if err != nil {
			writeAPIError(w, ErrInvalid)
			return
		}
		cursor, err := decodeBatchCursor(r.URL.Query().Get("cursor"))
		if err != nil {
			writeAPIError(w, ErrInvalid)
			return
		}
		page, err := h.Application.ListBatches(r.Context(), user.ID, cursor, limit)
		if err != nil {
			writeAPIError(w, err)
			return
		}
		response := map[string]any{"items": page.Items, "next_cursor": nil}
		if page.NextCursor != nil {
			response["next_cursor"] = encodeBatchCursor(*page.NextCursor)
		}
		writeAPIJSON(w, http.StatusOK, response)
		return
	}
	var request struct {
		TemplateID string   `json:"template_id"`
		AccountIDs []string `json:"account_ids,omitempty"`
	}
	if err := decodeJSONBody(w, r, &request); err != nil {
		writeAPIError(w, err)
		return
	}
	templateID, err := uuid.Parse(request.TemplateID)
	if err != nil {
		writeAPIError(w, ErrInvalid)
		return
	}
	accounts, err := parseUUIDs(request.AccountIDs)
	if err != nil {
		writeAPIError(w, ErrInvalid)
		return
	}
	batch, err := h.Application.CreateBatch(r.Context(), user.ID, templateID, accounts)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusCreated, batch)
}

func (h Handler) batch(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(r)
	if !ok {
		writeAPIError(w, errors.New("unauthorized"))
		return
	}
	id, err := pathUUID(r, "id")
	if err != nil {
		writeAPIError(w, ErrNotFound)
		return
	}
	if r.Method == http.MethodDelete {
		if err = decodeEmptyBody(w, r); err == nil {
			err = h.Application.DeleteBatch(r.Context(), user.ID, id)
		}
		if err != nil {
			writeAPIError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	result, err := h.Application.GetBatch(r.Context(), user.ID, id)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}

func (h Handler) reserveFile(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(r)
	if !ok {
		writeAPIError(w, errors.New("unauthorized"))
		return
	}
	batchID, err := pathUUID(r, "id")
	if err != nil {
		writeAPIError(w, ErrNotFound)
		return
	}
	var request struct {
		Filename             string `json:"filename"`
		MIMEType             string `json:"mime_type"`
		ByteSize             int64  `json:"byte_size"`
		SHA256               string `json:"sha256"`
		IntentionalDuplicate bool   `json:"intentional_duplicate,omitempty"`
	}
	if err = decodeJSONBody(w, r, &request); err != nil {
		writeAPIError(w, err)
		return
	}
	result, err := h.Application.ReserveFile(r.Context(), user.ID, batchID, ReservationInput{DisplayFilename: request.Filename, MIMEType: request.MIMEType, ByteSize: request.ByteSize, SHA256: request.SHA256, IntentionalDuplicate: request.IntentionalDuplicate})
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusCreated, result)
}

func (h Handler) finalizeFile(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(r)
	if !ok {
		writeAPIError(w, errors.New("unauthorized"))
		return
	}
	batchID, err := pathUUID(r, "id")
	if err != nil {
		writeAPIError(w, ErrNotFound)
		return
	}
	fileID, err := pathUUID(r, "file_id")
	if err != nil {
		writeAPIError(w, ErrNotFound)
		return
	}
	if err = decodeEmptyBody(w, r); err != nil {
		writeAPIError(w, err)
		return
	}
	result, err := h.Application.FinalizeFile(r.Context(), user.ID, batchID, fileID)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}

func (h Handler) replaceDocuments(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(r)
	if !ok {
		writeAPIError(w, errors.New("unauthorized"))
		return
	}
	batchID, err := pathUUID(r, "id")
	if err != nil {
		writeAPIError(w, ErrNotFound)
		return
	}
	var request struct {
		Documents []struct {
			ID      string   `json:"id"`
			Label   string   `json:"label,omitempty"`
			FileIDs []string `json:"file_ids"`
		} `json:"documents"`
	}
	if err = decodeJSONBody(w, r, &request); err != nil {
		writeAPIError(w, err)
		return
	}
	layout := make([]DocumentLayout, 0, len(request.Documents))
	for _, item := range request.Documents {
		id, parseErr := uuid.Parse(item.ID)
		if parseErr != nil {
			writeAPIError(w, ErrInvalid)
			return
		}
		files, parseErr := parseUUIDs(item.FileIDs)
		if parseErr != nil {
			writeAPIError(w, ErrInvalid)
			return
		}
		layout = append(layout, DocumentLayout{DocumentID: id, Label: item.Label, FileIDs: files})
	}
	result, err := h.Application.ReplaceDocumentLayout(r.Context(), user.ID, batchID, layout)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}

func (h Handler) batchAction(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := currentUser(r)
		if !ok {
			writeAPIError(w, errors.New("unauthorized"))
			return
		}
		id, err := pathUUID(r, "id")
		if err != nil {
			writeAPIError(w, ErrNotFound)
			return
		}
		if err = decodeEmptyBody(w, r); err != nil {
			writeAPIError(w, err)
			return
		}
		var result Batch
		if action == "submit" {
			result, err = h.Application.SubmitBatch(r.Context(), user.ID, id)
		} else {
			result, err = h.Application.CancelBatch(r.Context(), user.ID, id)
		}
		if err != nil {
			writeAPIError(w, err)
			return
		}
		writeAPIJSON(w, http.StatusOK, result)
	}
}

func (h Handler) candidates(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(r)
	if !ok {
		writeAPIError(w, errors.New("unauthorized"))
		return
	}
	id, err := pathUUID(r, "id")
	if err != nil {
		writeAPIError(w, ErrNotFound)
		return
	}
	items, err := h.Application.ListCandidates(r.Context(), user.ID, id)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h Handler) resolveCandidate(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(r)
	if !ok {
		writeAPIError(w, errors.New("unauthorized"))
		return
	}
	id, err := pathUUID(r, "id")
	if err != nil {
		writeAPIError(w, ErrNotFound)
		return
	}
	var request struct {
		Action             CandidateAction `json:"action"`
		AccountID          *string         `json:"account_id,omitempty"`
		TransactionID      *string         `json:"transaction_id,omitempty"`
		DebitAccountID     *string         `json:"debit_account_id,omitempty"`
		CreditAccountID    *string         `json:"credit_account_id,omitempty"`
		CategoryID         *string         `json:"category_id,omitempty"`
		ExpectedGeneration int             `json:"expected_generation"`
	}
	if err = decodeJSONBody(w, r, &request); err != nil {
		writeAPIError(w, err)
		return
	}
	resolution := CandidateResolution{Action: request.Action, ExpectedGeneration: request.ExpectedGeneration}
	for raw, target := range map[*string]**uuid.UUID{request.AccountID: &resolution.AccountID, request.TransactionID: &resolution.TransactionID, request.DebitAccountID: &resolution.DebitAccountID, request.CreditAccountID: &resolution.CreditAccountID, request.CategoryID: &resolution.CategoryID} {
		if raw != nil {
			value, parseErr := uuid.Parse(*raw)
			if parseErr != nil {
				writeAPIError(w, ErrInvalid)
				return
			}
			*target = &value
		}
	}
	result, err := h.Application.ResolveCandidate(r.Context(), user.ID, id, resolution)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}

func (h Handler) retryDocument(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(r)
	if !ok {
		writeAPIError(w, errors.New("unauthorized"))
		return
	}
	id, err := pathUUID(r, "id")
	if err != nil {
		writeAPIError(w, ErrNotFound)
		return
	}
	if err = decodeEmptyBody(w, r); err != nil {
		writeAPIError(w, err)
		return
	}
	result, err := h.Application.RetryDocument(r.Context(), user.ID, id)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}

func (h Handler) deleteDocument(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(r)
	if !ok {
		writeAPIError(w, errors.New("unauthorized"))
		return
	}
	id, err := pathUUID(r, "id")
	if err != nil {
		writeAPIError(w, ErrNotFound)
		return
	}
	if err = decodeEmptyBody(w, r); err == nil {
		err = h.Application.DeleteDocument(r.Context(), user.ID, id)
	}
	if err != nil {
		writeAPIError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h Handler) promptPreview(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(r)
	if !ok {
		writeAPIError(w, errors.New("unauthorized"))
		return
	}
	var request struct {
		TemplateID string   `json:"template_id"`
		AccountIDs []string `json:"account_ids,omitempty"`
	}
	if err := decodeJSONBody(w, r, &request); err != nil {
		writeAPIError(w, err)
		return
	}
	templateID, err := uuid.Parse(request.TemplateID)
	if err != nil {
		writeAPIError(w, ErrInvalid)
		return
	}
	accounts, err := parseUUIDs(request.AccountIDs)
	if err != nil {
		writeAPIError(w, ErrInvalid)
		return
	}
	result, err := h.Application.PreviewPrompt(r.Context(), user.ID, templateID, accounts)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}

func (h Handler) debugAttempts(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	user, ok := currentUser(r)
	if !ok {
		writeAPIError(w, errors.New("unauthorized"))
		return
	}
	id, err := pathUUID(r, "id")
	if err != nil {
		writeAPIError(w, ErrNotFound)
		return
	}
	result, err := h.Application.ListDebugAttempts(r.Context(), user.ID, id)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]any{"items": result})
}

func (h Handler) debugAttemptField(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	user, ok := currentUser(r)
	if !ok {
		writeAPIError(w, errors.New("unauthorized"))
		return
	}
	sourceID, err := pathUUID(r, "id")
	if err != nil {
		writeAPIError(w, ErrNotFound)
		return
	}
	attemptID, err := pathUUID(r, "attempt_id")
	if err != nil {
		writeAPIError(w, ErrNotFound)
		return
	}
	result, err := h.Application.GetDebugAttemptField(r.Context(), user.ID, sourceID, attemptID, r.PathValue("field"))
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}

func decodeTemplateRequest(w http.ResponseWriter, r *http.Request, requireVersion bool) (TemplateInput, error) {
	var request struct {
		Title           string       `json:"title"`
		DocumentType    DocumentType `json:"document_type"`
		ParsingPrompt   string       `json:"parsing_prompt"`
		AccountIDs      []string     `json:"account_ids"`
		ExpectedVersion *int         `json:"expected_version,omitempty"`
	}
	if err := decodeJSONBody(w, r, &request); err != nil {
		return TemplateInput{}, err
	}
	accounts, err := parseUUIDs(request.AccountIDs)
	if err != nil {
		return TemplateInput{}, ErrInvalid
	}
	if requireVersion && request.ExpectedVersion == nil {
		return TemplateInput{}, ErrInvalid
	}
	return TemplateInput{Title: request.Title, DocumentType: request.DocumentType, ParsingPrompt: request.ParsingPrompt, AccountIDs: accounts, ExpectedVersion: request.ExpectedVersion}, nil
}

func decodeJSONBody(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return ErrInvalid
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return ErrInvalid
	}
	return nil
}

func decodeEmptyBody(w http.ResponseWriter, r *http.Request) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1)
	content, err := io.ReadAll(r.Body)
	if err != nil || len(bytes.TrimSpace(content)) != 0 {
		return ErrInvalid
	}
	return nil
}

func pathUUID(r *http.Request, name string) (uuid.UUID, error) { return uuid.Parse(r.PathValue(name)) }
func parseUUIDs(values []string) ([]uuid.UUID, error) {
	result := make([]uuid.UUID, 0, len(values))
	for _, raw := range values {
		value, err := uuid.Parse(raw)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, nil
}
func parseLimit(raw string, fallback int) (int, error) {
	if raw == "" {
		return fallback, nil
	}
	return strconv.Atoi(raw)
}
func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func encodeBatchCursor(cursor BatchCursor) string {
	raw, _ := json.Marshal(map[string]string{"created_at": cursor.CreatedAt.UTC().Format(time.RFC3339Nano), "id": cursor.ID.String()})
	return base64.RawURLEncoding.EncodeToString(raw)
}
func decodeBatchCursor(raw string) (*BatchCursor, error) {
	if raw == "" {
		return nil, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, err
	}
	var value struct {
		CreatedAt string `json:"created_at"`
		ID        string `json:"id"`
	}
	decoder := json.NewDecoder(bytes.NewReader(decoded))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&value); err != nil {
		return nil, err
	}
	createdAt, err := time.Parse(time.RFC3339Nano, value.CreatedAt)
	if err != nil {
		return nil, err
	}
	id, err := uuid.Parse(value.ID)
	if err != nil {
		return nil, err
	}
	return &BatchCursor{CreatedAt: createdAt, ID: id}, nil
}

func writeAPIError(w http.ResponseWriter, err error) {
	status, message := http.StatusInternalServerError, "Bulk Import request could not be completed."
	switch {
	case errors.Is(err, ErrInvalid):
		status, message = http.StatusBadRequest, "Bulk Import request is invalid."
	case errors.Is(err, ErrNotFound):
		status, message = http.StatusNotFound, "Bulk Import resource was not found."
	case errors.Is(err, ErrDuplicateFile):
		writeAPIJSON(w, http.StatusConflict, map[string]string{
			"code":  "duplicate_file",
			"error": "A file with this checksum has already been uploaded.",
		})
		return
	case errors.Is(err, ErrConflict), errors.Is(err, ErrVersionConflict), errors.Is(err, ErrReadOnlyCandidate):
		status, message = http.StatusConflict, err.Error()
	case strings.Contains(err.Error(), "unauthorized"):
		status, message = http.StatusUnauthorized, "authentication required"
	}
	writeAPIJSON(w, status, map[string]string{"error": message})
}

func writeAPIJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

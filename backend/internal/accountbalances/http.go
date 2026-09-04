package accountbalances

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/zhengteck/wealth-builder/backend/internal/auth"
)

const maxRequestBytes = 1 << 20

type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler { return &Handler{service: service} }

func (h *Handler) Register(mux *http.ServeMux, verifier auth.Verifier) {
	requireUser := func(next http.HandlerFunc) http.Handler { return auth.RequireUser(verifier, next) }
	mux.Handle("GET /v1/accounts/balances", requireUser(h.listBalances))
	mux.Handle("PUT /v1/accounts/{id}/opening-balance", requireUser(h.setOpeningBalance))
	mux.Handle("GET /v1/accounts/{id}/opening-balance/history", requireUser(h.listOpeningBalanceHistory))
	mux.Handle("GET /v1/transaction-calculation-treatments/{id}", requireUser(h.getTreatment))
	mux.Handle("PUT /v1/transaction-calculation-treatments/{id}", requireUser(h.setTreatment))
}

type balanceAmountJSON struct {
	Currency   string `json:"currency"`
	MinorUnits string `json:"minor_units"`
}

type balanceJSON struct {
	AccountID       uuid.UUID           `json:"account_id"`
	AccountName     string              `json:"account_name"`
	State           string              `json:"state"`
	Side            AccountSide         `json:"side"`
	Version         int                 `json:"version"`
	AsOf            *time.Time          `json:"as_of"`
	OpeningBalances []balanceAmountJSON `json:"opening_balances"`
	CurrentBalances []balanceAmountJSON `json:"current_balances"`
}

func balanceResponse(view BalanceView) balanceJSON {
	result := balanceJSON{
		AccountID: view.AccountID, AccountName: view.AccountName, State: view.State,
		Side: view.Side, Version: view.Version, AsOf: view.AsOf,
		OpeningBalances: make([]balanceAmountJSON, 0, len(view.OpeningBalances)),
		CurrentBalances: make([]balanceAmountJSON, 0, len(view.CurrentBalances)),
	}
	for _, value := range view.OpeningBalances {
		result.OpeningBalances = append(result.OpeningBalances, balanceAmountJSON{value.Currency, strconv.FormatInt(value.MinorUnits, 10)})
	}
	for _, value := range view.CurrentBalances {
		result.CurrentBalances = append(result.CurrentBalances, balanceAmountJSON{value.Currency, strconv.FormatInt(value.MinorUnits, 10)})
	}
	return result
}

func (h *Handler) listBalances(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	views, err := h.service.ListBalances(r.Context(), user.ID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	result := make([]balanceJSON, 0, len(views))
	for _, view := range views {
		result = append(result, balanceResponse(view))
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": result})
}

func (h *Handler) setOpeningBalance(w http.ResponseWriter, r *http.Request) {
	user, accountID, ok := userAndPathID(w, r)
	if !ok {
		return
	}
	var payload struct {
		Balances         map[string]string `json:"balances"`
		AsOf             string            `json:"as_of"`
		ExpectedVersion  int               `json:"expected_version"`
		CorrectionReason *string           `json:"correction_reason"`
	}
	if err := decodeStrict(w, r, &payload); err != nil {
		return
	}
	asOf, err := time.Parse(time.RFC3339Nano, payload.AsOf)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "as_of must be an RFC3339 timestamp")
		return
	}
	view, err := h.service.SetOpeningBalance(r.Context(), user.ID, accountID, SetOpeningBalanceRequest{
		Balances: payload.Balances, AsOf: asOf, ExpectedVersion: payload.ExpectedVersion, CorrectionReason: payload.CorrectionReason,
	}, r.Header.Get("Idempotency-Key"))
	if err != nil {
		var conflict *VersionConflictError
		if errors.As(err, &conflict) {
			current, currentErr := h.service.GetBalance(r.Context(), user.ID, accountID)
			if currentErr != nil {
				writeServiceError(w, currentErr)
				return
			}
			w.Header().Set("ETag", versionETag(current.Version))
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":   map[string]string{"message": "the resource changed; reload and retry"},
				"current": balanceResponse(current),
			})
			return
		}
		writeServiceError(w, err)
		return
	}
	w.Header().Set("ETag", versionETag(view.Version))
	writeJSON(w, http.StatusOK, balanceResponse(view))
}

func (h *Handler) getTreatment(w http.ResponseWriter, r *http.Request) {
	user, transactionID, ok := userAndPathID(w, r)
	if !ok {
		return
	}
	view, err := h.service.GetCalculationTreatment(r.Context(), user.ID, transactionID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if view.UpdatedAt == nil {
		w.Header().Set("ETag", `"t-0"`)
	} else {
		w.Header().Set("ETag", treatmentETag(*view.UpdatedAt))
	}
	writeJSON(w, http.StatusOK, view)
}

func (h *Handler) listOpeningBalanceHistory(w http.ResponseWriter, r *http.Request) {
	user, accountID, ok := userAndPathID(w, r)
	if !ok {
		return
	}
	revisions, err := h.service.ListOpeningBalanceHistory(r.Context(), user.ID, accountID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	type revisionJSON struct {
		ID               uuid.UUID           `json:"id"`
		Version          int                 `json:"version"`
		Balances         []balanceAmountJSON `json:"balances"`
		AsOf             time.Time           `json:"as_of"`
		CorrectionReason *string             `json:"correction_reason"`
		ChangedAt        time.Time           `json:"changed_at"`
	}
	result := make([]revisionJSON, 0, len(revisions))
	for _, revision := range revisions {
		item := revisionJSON{ID: revision.ID, Version: revision.Version, AsOf: revision.AsOf, CorrectionReason: revision.CorrectionReason, ChangedAt: revision.ChangedAt, Balances: make([]balanceAmountJSON, 0, len(revision.Balances))}
		for _, amount := range revision.Balances {
			item.Balances = append(item.Balances, balanceAmountJSON{amount.Currency, strconv.FormatInt(amount.MinorUnits, 10)})
		}
		result = append(result, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"revisions": result})
}

func (h *Handler) setTreatment(w http.ResponseWriter, r *http.Request) {
	user, transactionID, ok := userAndPathID(w, r)
	if !ok {
		return
	}
	var payload struct {
		Basis  SpendingBasis `json:"spending_basis"`
		Reason string        `json:"reason"`
	}
	if err := decodeStrict(w, r, &payload); err != nil {
		return
	}
	expected, err := parseTreatmentETag(r.Header.Get("If-Match"))
	if err != nil {
		writeError(w, http.StatusPreconditionRequired, "a valid If-Match header is required")
		return
	}
	treatment, err := h.service.SetUserTreatment(r.Context(), user.ID, transactionID, SetTreatmentRequest{Basis: payload.Basis, Reason: payload.Reason, ExpectedUpdatedAt: expected})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	w.Header().Set("ETag", treatmentETag(treatment.UpdatedAt))
	writeJSON(w, http.StatusOK, treatment)
}

func userAndPathID(w http.ResponseWriter, r *http.Request) (auth.User, uuid.UUID, bool) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return auth.User{}, uuid.Nil, false
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid resource id")
		return auth.User{}, uuid.Nil, false
	}
	return user, id, true
}

func versionETag(version int) string { return fmt.Sprintf(`"v-%d"`, version) }

func treatmentETag(updatedAt time.Time) string { return fmt.Sprintf(`"t-%d"`, updatedAt.UnixNano()) }

func parseTreatmentETag(value string) (*time.Time, error) {
	if value == `"t-0"` {
		return nil, nil
	}
	if !strings.HasPrefix(value, `"t-`) || !strings.HasSuffix(value, `"`) {
		return nil, errors.New("invalid ETag")
	}
	nanos, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(value, `"t-`), `"`), 10, 64)
	if err != nil || nanos <= 0 {
		return nil, errors.New("invalid ETag")
	}
	valueTime := time.Unix(0, nanos).UTC()
	return &valueTime, nil
}

func decodeStrict(w http.ResponseWriter, r *http.Request, destination any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON request")
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "request must contain one JSON object")
		return errors.New("multiple JSON values")
	}
	return nil
}

func writeServiceError(w http.ResponseWriter, err error) {
	var conflict *VersionConflictError
	switch {
	case errors.As(err, &conflict):
		w.Header().Set("ETag", versionETag(conflict.Current.BaselineVersion))
		writeError(w, http.StatusConflict, "the resource changed; reload and retry")
	case errors.Is(err, ErrTreatmentVersionConflict):
		writeError(w, http.StatusPreconditionFailed, "the treatment changed; reload and retry")
	case errors.Is(err, ErrSystemTreatmentImmutable):
		writeError(w, http.StatusConflict, "system payoff exclusions cannot be changed")
	case errors.Is(err, ErrIdempotencyConflict), errors.Is(err, ErrIdempotencyInFlight):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, "resource not found")
	case errors.Is(err, ErrValidation):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "request could not be completed")
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"message": message}})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

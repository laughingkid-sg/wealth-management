package creditcard

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/zhengteck/wealth-builder/backend/internal/auth"
)

const maxRequestBytes = 1 << 20

var canonicalMinorUnits = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)

type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler { return &Handler{service: service} }

func (h *Handler) Register(mux *http.ServeMux, verifier auth.Verifier) {
	requireUser := func(next http.HandlerFunc) http.Handler { return auth.RequireUser(verifier, next) }
	mux.Handle("GET /v1/accounts/{account_id}/credit-card-statements", requireUser(h.listBills))
	mux.Handle("GET /v1/credit-card-statements/{id}", requireUser(h.getBill))
	mux.Handle("PATCH /v1/credit-card-statements/{id}", requireUser(h.correctHeader))
	mux.Handle("POST /v1/credit-card-statements/{id}/lines/{line_id}/attach", requireUser(h.attachLine))
	mux.Handle("POST /v1/credit-card-statements/{id}/lines/{line_id}/create-transaction", requireUser(h.createLineTransaction))
	mux.Handle("POST /v1/credit-card-statements/{id}/lines/{line_id}/ignore", requireUser(h.ignoreLine))
	mux.Handle("POST /v1/credit-card-statements/{id}/payment-candidate/select", requireUser(h.selectPaymentCandidate))
	mux.Handle("POST /v1/credit-card-statements/{id}/payment-candidate/confirm", requireUser(h.confirmPaymentCandidate))
	mux.Handle("POST /v1/credit-card-statements/{id}/payoff", requireUser(h.payInFull))
	mux.Handle("POST /v1/credit-card-statements/{id}/void", requireUser(h.voidBill))
	mux.Handle("DELETE /v1/credit-card-statements/{id}", requireUser(h.discardBill))
}

type billSummaryJSON struct {
	ID                       uuid.UUID  `json:"id"`
	AccountID                uuid.UUID  `json:"account_id"`
	PeriodStart              *string    `json:"period_start"`
	PeriodEnd                *string    `json:"period_end"`
	StatementDate            *string    `json:"statement_date"`
	DueDate                  *string    `json:"due_date"`
	SettlementCurrency       *string    `json:"settlement_currency"`
	AmountDueMinor           *string    `json:"amount_due_minor"`
	UnresolvedCandidateCount int        `json:"unresolved_candidate_count"`
	Status                   BillStatus `json:"status"`
	Version                  int        `json:"version"`
	UpdatedAt                time.Time  `json:"updated_at"`
}

type lineJSON struct {
	ID                  uuid.UUID                 `json:"id"`
	Kind                LineKind                  `json:"line_kind"`
	Status              LineStatus                `json:"resolution_status"`
	ResolutionReason    *string                   `json:"resolution_reason"`
	LinkExceptionReason *string                   `json:"link_exception_reason"`
	Index               int                       `json:"line_index"`
	OccurredOn          *string                   `json:"occurred_on"`
	Description         string                    `json:"description"`
	AmountMinor         *string                   `json:"amount_minor"`
	Currency            *string                   `json:"currency"`
	Transaction         *canonicalTransactionJSON `json:"transaction"`
}

type canonicalTransactionJSON struct {
	ID                  uuid.UUID            `json:"id"`
	AccountID           uuid.UUID            `json:"account_id"`
	Direction           TransactionDirection `json:"direction"`
	OriginalCurrency    string               `json:"original_currency"`
	OriginalAmountMinor string               `json:"original_amount_minor"`
	OccurredAt          time.Time            `json:"occurred_at"`
}

type billJSON struct {
	billSummaryJSON
	BulkDocumentID                uuid.UUID   `json:"bulk_document_id"`
	BulkAttemptGeneration         int         `json:"bulk_attempt_generation"`
	PayoffTransferID              *uuid.UUID  `json:"payoff_transfer_id"`
	PaymentCandidateTransactionID *uuid.UUID  `json:"payment_candidate_transaction_id"`
	AmbiguousPaymentCandidates    []uuid.UUID `json:"ambiguous_payment_candidates"`
	PaidAt                        *time.Time  `json:"paid_at"`
	VoidReason                    *string     `json:"void_reason"`
	MinimumPaymentMinor           *string     `json:"minimum_payment_minor"`
	PreviousBalanceMinor          *string     `json:"previous_balance_minor"`
	EvidenceURL                   string      `json:"evidence_url"`
	Lines                         []lineJSON  `json:"lines"`
	Events                        []BillEvent `json:"events"`
}

func summaryResponse(bill BillSummary) billSummaryJSON {
	return billSummaryJSON{
		ID: bill.ID, AccountID: bill.AccountID, PeriodStart: dateString(bill.PeriodStart), PeriodEnd: dateString(bill.PeriodEnd),
		StatementDate: dateString(bill.StatementDate), DueDate: dateString(bill.DueDate), SettlementCurrency: bill.SettlementCurrency,
		AmountDueMinor: minorString(bill.AmountDueMinor), UnresolvedCandidateCount: bill.UnresolvedCandidateCount, Status: bill.Status, Version: bill.Version, UpdatedAt: bill.UpdatedAt,
	}
}

func billResponse(bill Bill) billJSON {
	response := billJSON{
		billSummaryJSON: summaryResponse(BillSummary{ID: bill.ID, AccountID: bill.AccountID, PeriodStart: bill.PeriodStart, PeriodEnd: bill.PeriodEnd, StatementDate: bill.StatementDate, DueDate: bill.DueDate, SettlementCurrency: bill.SettlementCurrency, AmountDueMinor: bill.AmountDueMinor, UnresolvedCandidateCount: bill.UnresolvedCandidateCount, Status: bill.Status, Version: bill.Version, UpdatedAt: bill.UpdatedAt}),
		BulkDocumentID:  bill.BulkDocumentID, BulkAttemptGeneration: bill.BulkAttemptGeneration, PayoffTransferID: bill.PayoffTransferID,
		PaymentCandidateTransactionID: bill.PaymentCandidateTransactionID, AmbiguousPaymentCandidates: nonNilUUIDs(bill.AmbiguousPaymentCandidates),
		PaidAt: bill.PaidAt, VoidReason: bill.VoidReason, MinimumPaymentMinor: minorString(bill.MinimumPaymentMinor), PreviousBalanceMinor: minorString(bill.PreviousBalanceMinor), EvidenceURL: bill.EvidenceURL, Lines: make([]lineJSON, 0, len(bill.Lines)), Events: nonNilEvents(bill.Events),
	}
	for _, line := range bill.Lines {
		item := lineJSON{ID: line.ID, Kind: line.Kind, Status: line.Status, ResolutionReason: line.ResolutionReason, LinkExceptionReason: line.LinkExceptionReason, Index: line.Index, OccurredOn: dateString(line.OccurredOn), Description: line.Description, AmountMinor: minorString(line.AmountMinor), Currency: line.Currency}
		if line.Transaction != nil {
			item.Transaction = &canonicalTransactionJSON{ID: line.Transaction.ID, AccountID: line.Transaction.AccountID, Direction: line.Transaction.Direction, OriginalCurrency: line.Transaction.OriginalCurrency, OriginalAmountMinor: strconv.FormatInt(line.Transaction.OriginalAmountMinor, 10), OccurredAt: line.Transaction.OccurredAt}
		}
		response.Lines = append(response.Lines, item)
	}
	return response
}

func (h *Handler) listBills(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(w, r)
	if !ok {
		return
	}
	accountID, err := uuid.Parse(r.PathValue("account_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid Account id")
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid limit")
			return
		}
	}
	var cursor *string
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		cursor = &raw
	}
	page, err := h.service.ListBills(r.Context(), user.ID, accountID, cursor, limit)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	items := make([]billSummaryJSON, 0, len(page.Bills))
	for _, bill := range page.Bills {
		items = append(items, summaryResponse(bill))
	}
	writeJSON(w, http.StatusOK, map[string]any{"bills": items, "next_cursor": page.NextCursor})
}

func (h *Handler) getBill(w http.ResponseWriter, r *http.Request) {
	user, billID, ok := userAndBillID(w, r)
	if !ok {
		return
	}
	bill, err := h.service.GetBill(r.Context(), user.ID, billID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeBill(w, http.StatusOK, bill)
}

func (h *Handler) correctHeader(w http.ResponseWriter, r *http.Request) {
	user, billID, version, ok := mutationContext(w, r)
	if !ok {
		return
	}
	var payload struct {
		PeriodStart        *string `json:"period_start"`
		PeriodEnd          *string `json:"period_end"`
		StatementDate      *string `json:"statement_date"`
		DueDate            *string `json:"due_date"`
		SettlementCurrency *string `json:"settlement_currency"`
		AmountDueMinor     *string `json:"amount_due_minor"`
		Reason             string  `json:"reason"`
	}
	if decodeStrict(w, r, &payload) != nil {
		return
	}
	header, err := parseHeader(payload.PeriodStart, payload.PeriodEnd, payload.StatementDate, payload.DueDate, payload.SettlementCurrency, payload.AmountDueMinor, payload.Reason)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	bill, err := h.service.CorrectHeader(r.Context(), user.ID, billID, HeaderCorrectionRequest{ExpectedVersion: version, Header: header})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeBill(w, http.StatusOK, bill)
}

func (h *Handler) attachLine(w http.ResponseWriter, r *http.Request) {
	user, billID, lineID, version, ok := lineMutationContext(w, r)
	if !ok {
		return
	}
	var payload struct {
		TransactionID       uuid.UUID `json:"transaction_id"`
		LinkExceptionReason *string   `json:"link_exception_reason"`
	}
	if decodeStrict(w, r, &payload) != nil {
		return
	}
	bill, err := h.service.AttachLine(r.Context(), user.ID, billID, lineID, AttachLineRequest{ExpectedVersion: version, TransactionID: payload.TransactionID, LinkExceptionReason: payload.LinkExceptionReason})
	writeMutationResult(w, bill, err)
}

func (h *Handler) createLineTransaction(w http.ResponseWriter, r *http.Request) {
	user, billID, lineID, version, ok := lineMutationContext(w, r)
	if !ok {
		return
	}
	var payload struct {
		CategoryID uuid.UUID `json:"category_id"`
	}
	if decodeStrict(w, r, &payload) != nil {
		return
	}
	bill, err := h.service.CreateLineTransaction(r.Context(), user.ID, billID, lineID, CreateLineRequest{ExpectedVersion: version, CategoryID: payload.CategoryID}, r.Header.Get("Idempotency-Key"))
	writeMutationResult(w, bill, err)
}

func (h *Handler) ignoreLine(w http.ResponseWriter, r *http.Request) {
	user, billID, lineID, version, ok := lineMutationContext(w, r)
	if !ok {
		return
	}
	var payload struct {
		Reason string `json:"reason"`
	}
	if decodeStrict(w, r, &payload) != nil {
		return
	}
	bill, err := h.service.IgnoreLine(r.Context(), user.ID, billID, lineID, ReasonedMutationRequest{ExpectedVersion: version, Reason: payload.Reason})
	writeMutationResult(w, bill, err)
}

func (h *Handler) selectPaymentCandidate(w http.ResponseWriter, r *http.Request) {
	h.paymentCandidate(w, r, false)
}

func (h *Handler) confirmPaymentCandidate(w http.ResponseWriter, r *http.Request) {
	h.paymentCandidate(w, r, true)
}

func (h *Handler) paymentCandidate(w http.ResponseWriter, r *http.Request, confirm bool) {
	user, billID, version, ok := mutationContext(w, r)
	if !ok {
		return
	}
	var payload struct {
		BankTransactionID uuid.UUID `json:"bank_transaction_id"`
	}
	if decodeStrict(w, r, &payload) != nil {
		return
	}
	request := PaymentCandidateRequest{ExpectedVersion: version, BankTransactionID: payload.BankTransactionID}
	var bill Bill
	var err error
	if confirm {
		bill, err = h.service.ConfirmPaymentCandidate(r.Context(), user.ID, billID, request, r.Header.Get("Idempotency-Key"))
	} else {
		bill, err = h.service.SelectPaymentCandidate(r.Context(), user.ID, billID, request, r.Header.Get("Idempotency-Key"))
	}
	writeMutationResult(w, bill, err)
}

func (h *Handler) payInFull(w http.ResponseWriter, r *http.Request) {
	user, billID, version, ok := mutationContext(w, r)
	if !ok {
		return
	}
	var payload struct {
		BankAccountID uuid.UUID `json:"bank_account_id"`
	}
	if decodeStrict(w, r, &payload) != nil {
		return
	}
	bill, err := h.service.PayInFull(r.Context(), user.ID, billID, PayoffRequest{ExpectedVersion: version, BankAccountID: payload.BankAccountID}, r.Header.Get("Idempotency-Key"))
	writeMutationResult(w, bill, err)
}

func (h *Handler) voidBill(w http.ResponseWriter, r *http.Request) {
	user, billID, version, ok := mutationContext(w, r)
	if !ok {
		return
	}
	var payload struct {
		Reason string `json:"reason"`
	}
	if decodeStrict(w, r, &payload) != nil {
		return
	}
	bill, err := h.service.VoidBill(r.Context(), user.ID, billID, ReasonedMutationRequest{ExpectedVersion: version, Reason: payload.Reason}, r.Header.Get("Idempotency-Key"))
	writeMutationResult(w, bill, err)
}

func (h *Handler) discardBill(w http.ResponseWriter, r *http.Request) {
	user, billID, version, ok := mutationContext(w, r)
	if !ok {
		return
	}
	if err := decodeEmpty(w, r); err != nil {
		return
	}
	if err := h.service.DiscardReviewBill(r.Context(), user.ID, billID, version, r.Header.Get("Idempotency-Key")); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func parseHeader(periodStart, periodEnd, statementDate, dueDate, currency, amount *string, reason string) (CorrectHeaderInput, error) {
	result := CorrectHeaderInput{SettlementCurrency: currency, Reason: reason}
	var err error
	if result.PeriodStart, err = parseDatePointer(periodStart); err != nil {
		return CorrectHeaderInput{}, err
	}
	if result.PeriodEnd, err = parseDatePointer(periodEnd); err != nil {
		return CorrectHeaderInput{}, err
	}
	if result.StatementDate, err = parseDatePointer(statementDate); err != nil {
		return CorrectHeaderInput{}, err
	}
	if result.DueDate, err = parseDatePointer(dueDate); err != nil {
		return CorrectHeaderInput{}, err
	}
	if amount != nil {
		parsed, parseErr := parseMinorUnits(*amount)
		if parseErr != nil {
			return CorrectHeaderInput{}, parseErr
		}
		result.AmountDueMinor = &parsed
	}
	return result, nil
}

func parseDatePointer(value *string) (*time.Time, error) {
	if value == nil {
		return nil, nil
	}
	parsed, err := time.Parse("2006-01-02", *value)
	if err != nil || parsed.Format("2006-01-02") != *value {
		return nil, fmt.Errorf("%w: dates must use YYYY-MM-DD", ErrValidation)
	}
	return &parsed, nil
}

func parseMinorUnits(value string) (int64, error) {
	if !canonicalMinorUnits.MatchString(value) || value == "-0" {
		return 0, fmt.Errorf("%w: money must be a canonical integer string", ErrValidation)
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: amount is outside bigint range", ErrValidation)
	}
	return parsed, nil
}

func currentUser(w http.ResponseWriter, r *http.Request) (auth.User, bool) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
	}
	return user, ok
}

func userAndBillID(w http.ResponseWriter, r *http.Request) (auth.User, uuid.UUID, bool) {
	user, ok := currentUser(w, r)
	if !ok {
		return auth.User{}, uuid.Nil, false
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid bill id")
		return auth.User{}, uuid.Nil, false
	}
	return user, id, true
}

func mutationContext(w http.ResponseWriter, r *http.Request) (auth.User, uuid.UUID, int, bool) {
	user, id, ok := userAndBillID(w, r)
	if !ok {
		return auth.User{}, uuid.Nil, 0, false
	}
	version, err := parseVersionETag(r.Header.Get("If-Match"))
	if err != nil {
		writeError(w, http.StatusPreconditionRequired, "a valid If-Match header is required")
		return auth.User{}, uuid.Nil, 0, false
	}
	return user, id, version, true
}

func lineMutationContext(w http.ResponseWriter, r *http.Request) (auth.User, uuid.UUID, uuid.UUID, int, bool) {
	user, billID, version, ok := mutationContext(w, r)
	if !ok {
		return auth.User{}, uuid.Nil, uuid.Nil, 0, false
	}
	lineID, err := uuid.Parse(r.PathValue("line_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid line id")
		return auth.User{}, uuid.Nil, uuid.Nil, 0, false
	}
	return user, billID, lineID, version, true
}

func parseVersionETag(value string) (int, error) {
	if !strings.HasPrefix(value, `"v-`) || !strings.HasSuffix(value, `"`) {
		return 0, errors.New("invalid ETag")
	}
	version, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(value, `"v-`), `"`))
	if err != nil || version < 1 {
		return 0, errors.New("invalid ETag")
	}
	return version, nil
}

func versionETag(version int) string { return fmt.Sprintf(`"v-%d"`, version) }

func writeMutationResult(w http.ResponseWriter, bill Bill, err error) {
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeBill(w, http.StatusOK, bill)
}

func writeBill(w http.ResponseWriter, status int, bill Bill) {
	w.Header().Set("ETag", versionETag(bill.Version))
	writeJSON(w, status, billResponse(bill))
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

func decodeEmpty(w http.ResponseWriter, r *http.Request) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	var value any
	err := json.NewDecoder(r.Body).Decode(&value)
	if errors.Is(err, io.EOF) {
		return nil
	}
	writeError(w, http.StatusBadRequest, "request body must be empty")
	return errors.New("non-empty body")
}

func writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrVersionConflict):
		writeError(w, http.StatusPreconditionFailed, "the bill changed; reload and retry")
	case errors.Is(err, ErrIdempotencyConflict), errors.Is(err, ErrIdempotencyInFlight), errors.Is(err, ErrDuplicateTransaction):
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

func dateString(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := value.UTC().Format("2006-01-02")
	return &formatted
}

func minorString(value *int64) *string {
	if value == nil {
		return nil
	}
	formatted := strconv.FormatInt(*value, 10)
	return &formatted
}

func nonNilUUIDs(values []uuid.UUID) []uuid.UUID {
	if values == nil {
		return []uuid.UUID{}
	}
	return values
}

func nonNilEvents(values []BillEvent) []BillEvent {
	if values == nil {
		return []BillEvent{}
	}
	return values
}

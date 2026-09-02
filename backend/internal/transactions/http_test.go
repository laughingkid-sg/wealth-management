package transactions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/zhengteck/wealth-builder/backend/internal/attachmentstorage"
	"github.com/zhengteck/wealth-builder/backend/internal/auth"
	"github.com/zhengteck/wealth-builder/backend/internal/transactionstore"
)

type verifierFunc func(context.Context, string) (auth.User, error)

func (f verifierFunc) Verify(ctx context.Context, token string) (auth.User, error) {
	return f(ctx, token)
}

type repositoryStub struct {
	run               transactionstore.SyncRun
	createErr         error
	latestErr         error
	connection        transactionstore.GmailConnectionStatus
	sources           []transactionstore.SourceSummary
	sourcePage        transactionstore.SourcePage
	sourceStatus      string
	sourceCursor      *transactionstore.SourcePageCursor
	sourceLimit       int
	attachments       []transactionstore.AttachmentRecord
	transactionPage   transactionstore.TransactionPage
	transactionFilter transactionstore.TransactionListFilter
	transfer          transactionstore.InternalTransfer
	transferInput     transactionstore.InternalTransferInput
	evidenceSources   []transactionstore.SourceEvidence
	transaction       transactionstore.Transaction
	actionErr         error
	attachedSourceID  uuid.UUID
	attachedToID      uuid.UUID
	createdSourceID   uuid.UUID
	createdAccountID  uuid.UUID
	unmatchedLinkID   uuid.UUID
	retriedSourceID   uuid.UUID
	patchedID         uuid.UUID
	patch             transactionstore.TransactionPatch
}

type attachmentSignerStub struct {
	request attachmentstorage.ObjectRequest
	expires int
	url     string
	err     error
}

func (s *attachmentSignerStub) SignURL(_ context.Context, request attachmentstorage.ObjectRequest, expires int) (string, error) {
	s.request, s.expires = request, expires
	return s.url, s.err
}

type oauthStub struct {
	authorizationURL string
	completeErr      error
	state, code      string
}

func (s *oauthStub) Begin(context.Context, uuid.UUID) (string, error) { return s.authorizationURL, nil }
func (s *oauthStub) Complete(_ context.Context, state, code string) error {
	s.state, s.code = state, code
	return s.completeErr
}

func (r *repositoryStub) CreateSyncRun(context.Context, uuid.UUID, bool) (transactionstore.SyncRun, error) {
	return r.run, r.createErr
}
func (r *repositoryStub) GetSyncRun(context.Context, uuid.UUID, uuid.UUID) (transactionstore.SyncRun, error) {
	return r.run, nil
}
func (r *repositoryStub) GetLatestSyncRun(context.Context, uuid.UUID) (transactionstore.SyncRun, error) {
	return r.run, r.latestErr
}
func (r *repositoryStub) GetGmailConnectionStatus(context.Context, uuid.UUID) (transactionstore.GmailConnectionStatus, error) {
	return r.connection, nil
}
func (r *repositoryStub) ListSourcesPage(_ context.Context, _ uuid.UUID, status string, cursor *transactionstore.SourcePageCursor, limit int) (transactionstore.SourcePage, error) {
	r.sourceStatus, r.sourceCursor, r.sourceLimit = status, cursor, limit
	if r.sourcePage.Items != nil {
		return r.sourcePage, nil
	}
	return transactionstore.SourcePage{Items: r.sources}, nil
}
func (r *repositoryStub) RetrySourceParse(_ context.Context, _ uuid.UUID, sourceID uuid.UUID) error {
	r.retriedSourceID = sourceID
	return r.actionErr
}
func (r *repositoryStub) GetSanitizedEmail(context.Context, uuid.UUID, uuid.UUID) (transactionstore.SanitizedEmail, error) {
	return transactionstore.SanitizedEmail{}, pgx.ErrNoRows
}
func (r *repositoryStub) ListSourceAttachments(context.Context, uuid.UUID, uuid.UUID) ([]transactionstore.AttachmentRecord, error) {
	return r.attachments, r.actionErr
}
func (r *repositoryStub) ListTransactionsPage(_ context.Context, _ uuid.UUID, filter transactionstore.TransactionListFilter) (transactionstore.TransactionPage, error) {
	r.transactionFilter = filter
	return r.transactionPage, r.actionErr
}
func (r *repositoryStub) ListTransactionSources(context.Context, uuid.UUID, uuid.UUID) ([]transactionstore.SourceEvidence, error) {
	return r.evidenceSources, r.actionErr
}
func (r *repositoryStub) AttachSource(_ context.Context, _ uuid.UUID, sourceID, transactionID uuid.UUID) (uuid.UUID, error) {
	r.attachedSourceID, r.attachedToID = sourceID, transactionID
	return uuid.New(), r.actionErr
}
func (r *repositoryStub) CreateTransactionFromSource(_ context.Context, _ uuid.UUID, sourceID, accountID uuid.UUID) (transactionstore.Transaction, error) {
	r.createdSourceID, r.createdAccountID = sourceID, accountID
	return r.transaction, r.actionErr
}
func (r *repositoryStub) UnmatchSourceLink(_ context.Context, _ uuid.UUID, linkID uuid.UUID) error {
	r.unmatchedLinkID = linkID
	return r.actionErr
}
func (r *repositoryStub) PatchTransaction(_ context.Context, _ uuid.UUID, transactionID uuid.UUID, patch transactionstore.TransactionPatch) (transactionstore.Transaction, error) {
	r.patchedID, r.patch = transactionID, patch
	return r.transaction, r.actionErr
}
func (r *repositoryStub) CreateInternalTransfer(_ context.Context, _ uuid.UUID, input transactionstore.InternalTransferInput) (transactionstore.InternalTransfer, error) {
	r.transferInput = input
	return r.transfer, r.actionErr
}

func TestCreateSyncRunReturnsAcceptedForVerifiedUser(t *testing.T) {
	userID := uuid.New()
	repository := &repositoryStub{run: transactionstore.SyncRun{ID: uuid.New(), Status: "queued", CreatedAt: time.Now()}}
	mux := http.NewServeMux()
	NewHandler(repository, false, nil, nil).Register(mux, verifierFunc(func(_ context.Context, token string) (auth.User, error) {
		if token != "valid" {
			return auth.User{}, errors.New("invalid")
		}
		return auth.User{ID: userID}, nil
	}))
	request := httptest.NewRequest(http.MethodPost, "/v1/transactions/gmail/sync-runs", nil)
	request.Header.Set("Authorization", "Bearer valid")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusAccepted)
	}
}

func TestCreateSyncRunRequiresConnection(t *testing.T) {
	repository := &repositoryStub{createErr: transactionstore.ErrGmailConnectionRequired}
	mux := http.NewServeMux()
	NewHandler(repository, false, nil, nil).Register(mux, verifierFunc(func(context.Context, string) (auth.User, error) { return auth.User{ID: uuid.New()}, nil }))
	request := httptest.NewRequest(http.MethodPost, "/v1/transactions/gmail/sync-runs", nil)
	request.Header.Set("Authorization", "Bearer valid")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusConflict)
	}
}

func TestCreateSyncRunRejectsConcurrentRefresh(t *testing.T) {
	repository := &repositoryStub{createErr: transactionstore.ErrSyncRunInProgress}
	mux := authenticatedMux(t, repository, uuid.New())
	request := httptest.NewRequest(http.MethodPost, "/v1/transactions/gmail/sync-runs", nil)
	request.Header.Set("Authorization", "Bearer valid")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "already in progress") {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestLatestSyncRunReturnsRealDownstreamCounters(t *testing.T) {
	repository := &repositoryStub{run: transactionstore.SyncRun{
		ID: uuid.New(), Status: "completed", SourcesParsedCount: 3, SourcesFailedCount: 1,
		CompletedAt: timePointer(time.Now()), CreatedAt: time.Now(),
	}}
	mux := authenticatedMux(t, repository, uuid.New())
	request := httptest.NewRequest(http.MethodGet, "/v1/transactions/sync-runs/latest", nil)
	request.Header.Set("Authorization", "Bearer valid")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	var body struct {
		SourcesParsed int `json:"sources_parsed"`
		SourcesFailed int `json:"sources_failed"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.SourcesParsed != 3 || body.SourcesFailed != 1 {
		t.Fatalf("counters = %#v", body)
	}
}

func TestGmailConnectionStatusIsSafeProjection(t *testing.T) {
	email := "owner@example.test"
	repository := &repositoryStub{connection: transactionstore.GmailConnectionStatus{
		Connected: true, Status: "active", Email: &email, SelectedLabel: "odin-finance",
	}}
	mux := authenticatedMux(t, repository, uuid.New())
	request := httptest.NewRequest(http.MethodGet, "/v1/transactions/gmail/connection", nil)
	request.Header.Set("Authorization", "Bearer valid")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"email":"owner@example.test"`) {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "refresh_token") {
		t.Fatalf("connection response exposed token material: %s", response.Body.String())
	}
}

func TestSourcesRejectUnsupportedFilter(t *testing.T) {
	mux := http.NewServeMux()
	NewHandler(&repositoryStub{}, false, nil, nil).Register(mux, verifierFunc(func(context.Context, string) (auth.User, error) { return auth.User{ID: uuid.New()}, nil }))
	request := httptest.NewRequest(http.MethodGet, "/v1/transactions/sources?status=all", nil)
	request.Header.Set("Authorization", "Bearer valid")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
}

func TestFailedSourcesUseKeysetCursorAndDecimalSuggestions(t *testing.T) {
	receivedAt := time.Date(2026, time.September, 2, 5, 4, 3, 0, time.UTC)
	amount := int64(12345)
	sourceID := uuid.New()
	repository := &repositoryStub{sourcePage: transactionstore.SourcePage{
		Items: []transactionstore.SourceSummary{{
			ID: sourceID, SourceType: "gmail_email", Provider: "gmail", ReceivedAt: receivedAt,
			ParseStatus: "failed", SuggestedAmountMinor: &amount, CreatedAt: receivedAt,
		}},
		HasMore: true,
	}}
	mux := authenticatedMux(t, repository, uuid.New())
	request := httptest.NewRequest(http.MethodGet, "/v1/transactions/sources?status=failed&limit=1", nil)
	request.Header.Set("Authorization", "Bearer valid")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	var page struct {
		Items []struct {
			SuggestedAmountMinor string `json:"suggested_amount_minor"`
		} `json:"items"`
		NextCursor *string `json:"next_cursor"`
	}
	if err := json.NewDecoder(response.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if repository.sourceStatus != "failed" || repository.sourceLimit != 1 || len(page.Items) != 1 || page.Items[0].SuggestedAmountMinor != "12345" || page.NextCursor == nil {
		t.Fatalf("source page = %#v, filter=%q limit=%d", page, repository.sourceStatus, repository.sourceLimit)
	}
	request = httptest.NewRequest(http.MethodGet, "/v1/transactions/sources?status=failed&limit=1&cursor="+url.QueryEscape(*page.NextCursor), nil)
	request.Header.Set("Authorization", "Bearer valid")
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK || repository.sourceCursor == nil || repository.sourceCursor.ID != sourceID || !repository.sourceCursor.ReceivedAt.Equal(receivedAt) {
		t.Fatalf("keyset cursor was not decoded: status=%d cursor=%#v", response.Code, repository.sourceCursor)
	}
}

func TestGmailCallbackRedirectsOnlyToConfiguredFrontend(t *testing.T) {
	frontend, _ := url.Parse("https://app.example.test/dashboard")
	flow := &oauthStub{}
	mux := http.NewServeMux()
	NewHandler(&repositoryStub{}, false, flow, frontend).Register(mux, verifierFunc(func(context.Context, string) (auth.User, error) { return auth.User{}, nil }))
	request := httptest.NewRequest(http.MethodGet, "/v1/transactions/gmail/oauth/callback?state=state-secret&code=code-secret", nil)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusSeeOther)
	}
	location := response.Header().Get("Location")
	if location != "https://app.example.test/dashboard?gmail=connected" || strings.Contains(location, "secret") {
		t.Fatalf("unsafe callback redirect: %q", location)
	}
	if flow.state != "state-secret" || flow.code != "code-secret" {
		t.Fatal("callback values were not passed to OAuth flow")
	}
}

func TestGmailConnectRequiresAuthenticatedUser(t *testing.T) {
	mux := http.NewServeMux()
	NewHandler(&repositoryStub{}, false, &oauthStub{authorizationURL: "https://accounts.google.com/example"}, &url.URL{Scheme: "https", Host: "app.example.test"}).Register(mux, verifierFunc(func(context.Context, string) (auth.User, error) { return auth.User{}, errors.New("invalid") }))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/transactions/gmail/connect", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestAttachSourcePassesOnlySelectedTransaction(t *testing.T) {
	userID, sourceID, transactionID := uuid.New(), uuid.New(), uuid.New()
	repository := &repositoryStub{}
	mux := authenticatedMux(t, repository, userID)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/transactions/sources/"+sourceID.String()+"/attach", strings.NewReader(`{"transaction_id":"`+transactionID.String()+`"}`))
	request.Header.Set("Authorization", "Bearer valid")
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if repository.attachedSourceID != sourceID || repository.attachedToID != transactionID {
		t.Fatalf("attach arguments = (%s, %s)", repository.attachedSourceID, repository.attachedToID)
	}
}

func TestListTransactionSourcesReturnsActiveLinkID(t *testing.T) {
	transactionID, sourceID, linkID := uuid.New(), uuid.New(), uuid.New()
	repository := &repositoryStub{evidenceSources: []transactionstore.SourceEvidence{{
		SourceLinkID:  linkID,
		SourceSummary: transactionstore.SourceSummary{ID: sourceID, SourceType: "gmail_email", Provider: "gmail", ReceivedAt: time.Now()},
	}}}
	mux := authenticatedMux(t, repository, uuid.New())
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/transactions/"+transactionID.String()+"/sources", nil)
	request.Header.Set("Authorization", "Bearer valid")
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	var body []struct {
		SourceLinkID string `json:"source_link_id"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 1 || body[0].SourceLinkID != linkID.String() {
		t.Fatalf("source links = %#v", body)
	}
}

func TestAttachSourceRejectsUnknownRequestFields(t *testing.T) {
	sourceID := uuid.New()
	mux := authenticatedMux(t, &repositoryStub{}, uuid.New())
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/transactions/sources/"+sourceID.String()+"/attach", strings.NewReader(`{"transaction_id":"`+uuid.New().String()+`","title":"ignored"}`))
	request.Header.Set("Authorization", "Bearer valid")
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
}

func TestCreateTransactionFromSourceDerivesFieldsServerSide(t *testing.T) {
	userID, sourceID, accountID := uuid.New(), uuid.New(), uuid.New()
	repository := &repositoryStub{transaction: transactionstore.Transaction{ID: uuid.New(), AccountID: accountID, TransactionKind: "debit", Title: "Source title", OriginalAmountMinor: 1234, OriginalCurrency: "SGD", OccurredAt: time.Now(), LineItems: []byte("[]"), ReviewStatus: "confirmed", CreatedAt: time.Now(), UpdatedAt: time.Now()}}
	mux := authenticatedMux(t, repository, userID)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/transactions/sources/"+sourceID.String()+"/create-transaction", strings.NewReader(`{"account_id":"`+accountID.String()+`"}`))
	request.Header.Set("Authorization", "Bearer valid")
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusCreated)
	}
	if repository.createdSourceID != sourceID || repository.createdAccountID != accountID {
		t.Fatalf("create arguments = (%s, %s)", repository.createdSourceID, repository.createdAccountID)
	}
	var body map[string]json.RawMessage
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if string(body["original_amount_minor"]) != `"1234"` {
		t.Fatalf("amount response = %s, want decimal string", body["original_amount_minor"])
	}
}

func TestCreateTransactionFromSourceReportsUnavailableAccount(t *testing.T) {
	repository := &repositoryStub{actionErr: transactionstore.ErrAccountNotFound}
	mux := authenticatedMux(t, repository, uuid.New())
	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/transactions/sources/"+uuid.New().String()+"/create-transaction",
		strings.NewReader(`{"account_id":"`+uuid.New().String()+`"}`),
	)
	request.Header.Set("Authorization", "Bearer valid")
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "selected account") || strings.Contains(response.Body.String(), "Source not found") {
		t.Fatalf("response body = %s", response.Body.String())
	}
}

func TestUnmatchSourceLinkReturnsNoContent(t *testing.T) {
	linkID := uuid.New()
	repository := &repositoryStub{}
	mux := authenticatedMux(t, repository, uuid.New())
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/transactions/source-links/"+linkID.String()+"/unmatch", nil)
	request.Header.Set("Authorization", "Bearer valid")
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
	}
	if repository.unmatchedLinkID != linkID {
		t.Fatalf("unmatched link = %s, want %s", repository.unmatchedLinkID, linkID)
	}
}

func TestPatchTransactionValidatesDecimalMoneyAndLineItems(t *testing.T) {
	transactionID := uuid.New()
	repository := &repositoryStub{transaction: transactionstore.Transaction{ID: transactionID, AccountID: uuid.New(), TransactionKind: "debit", Title: "Coffee", OriginalAmountMinor: 450, OriginalCurrency: "SGD", OccurredAt: time.Now(), LineItems: []byte("[]"), ReviewStatus: "confirmed", CreatedAt: time.Now(), UpdatedAt: time.Now()}}
	mux := authenticatedMux(t, repository, uuid.New())
	payload := []byte(`{"title":"Coffee","original_amount_minor":"450","sgd_amount_minor":null,"line_items":[{"schema_version":1,"description":"Coffee","quantity":1,"unit_price_minor":"450","currency":"SGD","details":{}}]}`)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPatch, "/v1/transactions/"+transactionID.String(), bytes.NewReader(payload))
	request.Header.Set("Authorization", "Bearer valid")
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if repository.patchedID != transactionID || repository.patch.OriginalAmountMinor == nil || *repository.patch.OriginalAmountMinor != 450 || !repository.patch.SGDAmountMinor.Set || repository.patch.SGDAmountMinor.Value != nil {
		t.Fatalf("patch = %#v", repository.patch)
	}
}

func TestPatchTransactionRejectsNumericMoney(t *testing.T) {
	mux := authenticatedMux(t, &repositoryStub{}, uuid.New())
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPatch, "/v1/transactions/"+uuid.New().String(), strings.NewReader(`{"original_amount_minor":450}`))
	request.Header.Set("Authorization", "Bearer valid")
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
}

func TestPatchTransactionRejectsUnknownCurrency(t *testing.T) {
	mux := authenticatedMux(t, &repositoryStub{}, uuid.New())
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPatch, "/v1/transactions/"+uuid.New().String(), strings.NewReader(`{"original_currency":"ZZZ"}`))
	request.Header.Set("Authorization", "Bearer valid")
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestPatchTransactionRejectsAccountThatWouldCollapseTransferLegs(t *testing.T) {
	repository := &repositoryStub{actionErr: transactionstore.ErrTransferSameAccount}
	mux := authenticatedMux(t, repository, uuid.New())
	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPatch,
		"/v1/transactions/"+uuid.New().String(),
		strings.NewReader(`{"account_id":"`+uuid.New().String()+`"}`),
	)
	request.Header.Set("Authorization", "Bearer valid")
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "different accounts") {
		t.Fatalf("response body = %s", response.Body.String())
	}
}

func TestPatchTransactionReportsUnavailableCategory(t *testing.T) {
	repository := &repositoryStub{actionErr: transactionstore.ErrCategoryNotFound}
	mux := authenticatedMux(t, repository, uuid.New())
	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPatch,
		"/v1/transactions/"+uuid.New().String(),
		strings.NewReader(`{"category_id":"`+uuid.New().String()+`"}`),
	)
	request.Header.Set("Authorization", "Bearer valid")
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "selected category") {
		t.Fatalf("response body = %s", response.Body.String())
	}
}

func TestListTransactionsReturnsFullProjectionAndTransferLink(t *testing.T) {
	occurredAt := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	transactionID, accountID := uuid.New(), uuid.New()
	counterpartID, counterpartAccountID, linkID := uuid.New(), uuid.New(), uuid.New()
	counterpartAccountName := "Savings"
	repository := &repositoryStub{transactionPage: transactionstore.TransactionPage{Items: []transactionstore.TransactionListRecord{{
		Transaction: transactionstore.Transaction{
			ID: transactionID, AccountID: accountID, TransactionKind: "debit", Title: "Transfer out",
			OriginalAmountMinor: 5000, OriginalCurrency: "SGD", OccurredAt: occurredAt,
			LineItems: json.RawMessage("[]"), ReviewStatus: "confirmed", CreatedAt: occurredAt, UpdatedAt: occurredAt,
		},
		Details: json.RawMessage(`{"reference":"safe"}`), AccountName: "Current", SourceCount: 2,
		TransferLink: &transactionstore.TransferLinkProjection{
			ID: linkID, LinkType: "internal_transfer", Role: "debit",
			CounterpartTransactionID: counterpartID, CounterpartAccountID: counterpartAccountID,
			CounterpartTitle: "Transfer in", CounterpartAccountName: &counterpartAccountName,
		},
	}}}}
	mux := authenticatedMux(t, repository, uuid.New())
	request := httptest.NewRequest(http.MethodGet, "/v1/transactions?kind=debit&review=confirmed&account_id="+accountID.String()+"&search=Transfer&limit=25", nil)
	request.Header.Set("Authorization", "Bearer valid")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	var body map[string]json.RawMessage
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(body["items"], &items); err != nil || len(items) != 1 {
		t.Fatalf("items = %s, err=%v", body["items"], err)
	}
	if string(items[0]["original_amount_minor"]) != `"5000"` || string(items[0]["source_count"]) != "2" || !strings.Contains(string(items[0]["transfer_link"]), counterpartAccountName) {
		t.Fatalf("transaction projection = %s", body["items"])
	}
	if repository.transactionFilter.AccountID == nil || *repository.transactionFilter.AccountID != accountID || repository.transactionFilter.Search != "Transfer" || repository.transactionFilter.Limit != 25 {
		t.Fatalf("transaction filter = %#v", repository.transactionFilter)
	}
}

func TestAttachmentListingSignsOwnedPathWithoutExposingIt(t *testing.T) {
	userID, sourceID := uuid.New(), uuid.New()
	objectPath := userID.String() + "/" + sourceID.String() + "/private-receipt.png"
	repository := &repositoryStub{attachments: []transactionstore.AttachmentRecord{{
		ID: "attachment-1", Filename: "receipt.png", MIMEType: "image/png", ByteSize: 321,
		SHA256: "abcdef", ParseEligible: true, ParseStatus: "parsed", ObjectPath: objectPath,
	}}}
	signer := &attachmentSignerStub{url: "https://storage.example.test/signed/opaque"}
	mux := authenticatedMux(t, repository, userID, signer)
	request := httptest.NewRequest(http.MethodGet, "/v1/transactions/sources/"+sourceID.String()+"/attachments", nil)
	request.Header.Set("Authorization", "Bearer valid")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if strings.Contains(body, "object_path") || strings.Contains(body, objectPath) {
		t.Fatalf("attachment response exposed storage path: %s", body)
	}
	if signer.expires != 300 || signer.request.UserID != userID || signer.request.SourceID != sourceID || signer.request.ObjectPath != objectPath {
		t.Fatalf("sign request = %#v, expires=%d", signer.request, signer.expires)
	}
}

func TestRetrySourceQueuesPersistedFailure(t *testing.T) {
	sourceID := uuid.New()
	repository := &repositoryStub{}
	mux := authenticatedMux(t, repository, uuid.New())
	request := httptest.NewRequest(http.MethodPost, "/v1/transactions/sources/"+sourceID.String()+"/retry", nil)
	request.Header.Set("Authorization", "Bearer valid")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || repository.retriedSourceID != sourceID {
		t.Fatalf("response=%d %s retried=%s", response.Code, response.Body.String(), repository.retriedSourceID)
	}
}

func TestCreateInternalTransferValidatesAndReturnsBothLegs(t *testing.T) {
	userID := uuid.New()
	debitAccountID, creditAccountID, sourceID := uuid.New(), uuid.New(), uuid.New()
	now := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
	debitID, creditID, linkID := uuid.New(), uuid.New(), uuid.New()
	repository := &repositoryStub{transfer: transactionstore.InternalTransfer{
		ID: linkID, LinkType: "internal_transfer", CreatedAt: now,
		Debit:  transactionstore.Transaction{ID: debitID, AccountID: debitAccountID, TransactionKind: "debit", Title: "Transfer out", OriginalAmountMinor: 2500, OriginalCurrency: "SGD", OccurredAt: now, LineItems: json.RawMessage("[]"), ReviewStatus: "confirmed", CreatedAt: now, UpdatedAt: now},
		Credit: transactionstore.Transaction{ID: creditID, AccountID: creditAccountID, TransactionKind: "credit", Title: "Transfer in", OriginalAmountMinor: 2500, OriginalCurrency: "SGD", OccurredAt: now, LineItems: json.RawMessage("[]"), ReviewStatus: "confirmed", CreatedAt: now, UpdatedAt: now},
	}}
	payload := fmt.Sprintf(`{"debit":{"account_id":%q,"title":"Transfer out","original_amount_minor":"2500","original_currency":"SGD","occurred_at":%q,"line_items":[],"source_ids":[%q]},"credit":{"account_id":%q,"title":"Transfer in","original_amount_minor":"2500","original_currency":"SGD","occurred_at":%q,"line_items":[],"source_ids":[%q]}}`, debitAccountID, now.Format(time.RFC3339), sourceID, creditAccountID, now.Format(time.RFC3339), sourceID)
	mux := authenticatedMux(t, repository, userID)
	request := httptest.NewRequest(http.MethodPost, "/v1/transactions/internal-transfers", strings.NewReader(payload))
	request.Header.Set("Authorization", "Bearer valid")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	if len(repository.transferInput.Debit.SourceIDs) != 1 || repository.transferInput.Debit.SourceIDs[0] != sourceID || len(repository.transferInput.Credit.SourceIDs) != 1 || repository.transferInput.Credit.SourceIDs[0] != sourceID {
		t.Fatalf("same source was not accepted for both legs: %#v", repository.transferInput)
	}
	body := response.Body.String()
	if !strings.Contains(body, debitID.String()) || !strings.Contains(body, creditID.String()) || !strings.Contains(body, linkID.String()) {
		t.Fatalf("transfer response omitted a leg or link: %s", body)
	}
}

func TestCreateInternalTransferRejectsNumericMoneyAndUnknownCurrency(t *testing.T) {
	accountID := uuid.New()
	now := time.Now().UTC().Format(time.RFC3339)
	testCases := map[string]struct {
		amount   string
		currency string
	}{
		"numeric amount":   {amount: "2500", currency: `"SGD"`},
		"unknown currency": {amount: `"2500"`, currency: `"ZZZ"`},
	}
	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			leg := fmt.Sprintf(`{"account_id":%q,"title":"Transfer","original_amount_minor":%s,"original_currency":%s,"occurred_at":%q,"line_items":[]}`, accountID, testCase.amount, testCase.currency, now)
			mux := authenticatedMux(t, &repositoryStub{}, uuid.New())
			request := httptest.NewRequest(http.MethodPost, "/v1/transactions/internal-transfers", strings.NewReader(`{"debit":`+leg+`,"credit":`+leg+`}`))
			request.Header.Set("Authorization", "Bearer valid")
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestCreateInternalTransferRejectsSameAccountBeforeStore(t *testing.T) {
	accountID := uuid.New()
	now := time.Now().UTC().Format(time.RFC3339)
	leg := fmt.Sprintf(`{"account_id":%q,"title":"Transfer","original_amount_minor":"2500","original_currency":"SGD","occurred_at":%q,"line_items":[]}`, accountID, now)
	repository := &repositoryStub{}
	mux := authenticatedMux(t, repository, uuid.New())
	request := httptest.NewRequest(http.MethodPost, "/v1/transactions/internal-transfers", strings.NewReader(`{"debit":`+leg+`,"credit":`+leg+`}`))
	request.Header.Set("Authorization", "Bearer valid")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	if repository.transferInput.Debit.AccountID != uuid.Nil || repository.transferInput.Credit.AccountID != uuid.Nil {
		t.Fatalf("same-account transfer reached store: %#v", repository.transferInput)
	}
}

func authenticatedMux(t *testing.T, repository Repository, userID uuid.UUID, signers ...AttachmentSigner) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	NewHandler(repository, false, nil, nil, signers...).Register(mux, verifierFunc(func(_ context.Context, token string) (auth.User, error) {
		if token != "valid" {
			return auth.User{}, errors.New("invalid")
		}
		return auth.User{ID: userID}, nil
	}))
	return mux
}

func timePointer(value time.Time) *time.Time { return &value }

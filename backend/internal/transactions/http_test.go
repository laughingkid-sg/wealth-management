package transactions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/zhengteck/wealth-builder/backend/internal/auth"
	"github.com/zhengteck/wealth-builder/backend/internal/transactionstore"
)

type verifierFunc func(context.Context, string) (auth.User, error)

func (f verifierFunc) Verify(ctx context.Context, token string) (auth.User, error) {
	return f(ctx, token)
}

type repositoryStub struct {
	run              transactionstore.SyncRun
	createErr        error
	sources          []transactionstore.SourceSummary
	evidenceSources  []transactionstore.SourceEvidence
	transaction      transactionstore.Transaction
	actionErr        error
	attachedSourceID uuid.UUID
	attachedToID     uuid.UUID
	createdSourceID  uuid.UUID
	createdAccountID uuid.UUID
	unmatchedLinkID  uuid.UUID
	patchedID        uuid.UUID
	patch            transactionstore.TransactionPatch
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
func (r *repositoryStub) ListSources(context.Context, uuid.UUID, string) ([]transactionstore.SourceSummary, error) {
	return r.sources, nil
}
func (r *repositoryStub) GetSanitizedEmail(context.Context, uuid.UUID, uuid.UUID) (transactionstore.SanitizedEmail, error) {
	return transactionstore.SanitizedEmail{}, pgx.ErrNoRows
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

func authenticatedMux(t *testing.T, repository Repository, userID uuid.UUID) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	NewHandler(repository, false, nil, nil).Register(mux, verifierFunc(func(_ context.Context, token string) (auth.User, error) {
		if token != "valid" {
			return auth.User{}, errors.New("invalid")
		}
		return auth.User{ID: userID}, nil
	}))
	return mux
}

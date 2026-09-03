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
	"github.com/zhengteck/wealth-builder/backend/internal/parserrules"
	"github.com/zhengteck/wealth-builder/backend/internal/providers"
	"github.com/zhengteck/wealth-builder/backend/internal/transactionstore"
)

type verifierFunc func(context.Context, string) (auth.User, error)

func (f verifierFunc) Verify(ctx context.Context, token string) (auth.User, error) {
	return f(ctx, token)
}

type repositoryStub struct {
	run                  transactionstore.SyncRun
	createErr            error
	latestErr            error
	connection           transactionstore.GmailConnectionStatus
	sources              []transactionstore.SourceSummary
	sourcePage           transactionstore.SourcePage
	sourceStatus         string
	sourceCursor         *transactionstore.SourcePageCursor
	sourceLimit          int
	attachments          []transactionstore.AttachmentRecord
	transactionPage      transactionstore.TransactionPage
	transactionFilter    transactionstore.TransactionListFilter
	transfer             transactionstore.InternalTransfer
	transferInput        transactionstore.InternalTransferInput
	evidenceSources      []transactionstore.SourceEvidence
	transaction          transactionstore.Transaction
	actionErr            error
	attachedSourceID     uuid.UUID
	attachedToID         uuid.UUID
	createdSourceID      uuid.UUID
	createdAccountID     uuid.UUID
	unmatchedLinkID      uuid.UUID
	retriedSourceID      uuid.UUID
	patchedID            uuid.UUID
	patch                transactionstore.TransactionPatch
	settings             transactionstore.TransactionSettings
	globalRules          []transactionstore.GlobalSourceParserRule
	globalRule           transactionstore.GlobalSourceParserRule
	globalRuleInput      transactionstore.GlobalSourceParserRuleInput
	globalRuleID         uuid.UUID
	globalEditorID       uuid.UUID
	defaultLoaded        transactionstore.DefaultParserInstructions
	defaultLoadedUserID  uuid.UUID
	previewSources       []transactionstore.PromptPreviewSource
	previewSourceLimit   int
	parseInput           transactionstore.SourceParseInput
	parseInputUserID     uuid.UUID
	parseInputSourceID   uuid.UUID
	loadedUserRuleUserID uuid.UUID
	mutationCalls        int
	defaultSaved         transactionstore.DefaultParserInstructions
	rule                 transactionstore.UserSourceParserRule
	ruleInput            transactionstore.UserSourceParserRuleInput
	ruleID               uuid.UUID
	matchingKey          transactionstore.AccountMatchingKey
	matchingKeyInput     transactionstore.AccountMatchingKeyInput
	matchingKeyID        uuid.UUID
	matchingKeyActive    bool
	debug                transactionstore.SourceParseDebug
	debugField           transactionstore.SourceParseAuditField
	debugSourceID        uuid.UUID
	debugAttemptID       uuid.UUID
	debugFieldName       string
	deletionResult       transactionstore.SourceDeletionResult
	stagedSourceID       uuid.UUID
}

type attachmentSignerStub struct {
	request attachmentstorage.ObjectRequest
	expires int
	url     string
	err     error
	deleted []attachmentstorage.ObjectRequest
}

func (s *attachmentSignerStub) SignURL(_ context.Context, request attachmentstorage.ObjectRequest, expires int) (string, error) {
	s.request, s.expires = request, expires
	return s.url, s.err
}

func (s *attachmentSignerStub) Delete(_ context.Context, requests []attachmentstorage.ObjectRequest) error {
	s.deleted = append([]attachmentstorage.ObjectRequest(nil), requests...)
	return s.err
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
	r.mutationCalls++
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
	r.mutationCalls++
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
	r.mutationCalls++
	r.attachedSourceID, r.attachedToID = sourceID, transactionID
	return uuid.New(), r.actionErr
}
func (r *repositoryStub) CreateTransactionFromSource(_ context.Context, _ uuid.UUID, sourceID, accountID uuid.UUID) (transactionstore.Transaction, error) {
	r.mutationCalls++
	r.createdSourceID, r.createdAccountID = sourceID, accountID
	return r.transaction, r.actionErr
}
func (r *repositoryStub) UnmatchSourceLink(_ context.Context, _ uuid.UUID, linkID uuid.UUID) error {
	r.mutationCalls++
	r.unmatchedLinkID = linkID
	return r.actionErr
}
func (r *repositoryStub) PatchTransaction(_ context.Context, _ uuid.UUID, transactionID uuid.UUID, patch transactionstore.TransactionPatch) (transactionstore.Transaction, error) {
	r.mutationCalls++
	r.patchedID, r.patch = transactionID, patch
	return r.transaction, r.actionErr
}
func (r *repositoryStub) CreateInternalTransfer(_ context.Context, _ uuid.UUID, input transactionstore.InternalTransferInput) (transactionstore.InternalTransfer, error) {
	r.mutationCalls++
	r.transferInput = input
	return r.transfer, r.actionErr
}
func (r *repositoryStub) GetTransactionSettings(context.Context, uuid.UUID) (transactionstore.TransactionSettings, error) {
	return r.settings, r.actionErr
}
func (r *repositoryStub) ListGlobalSourceParserRules(context.Context) ([]transactionstore.GlobalSourceParserRule, error) {
	return r.globalRules, r.actionErr
}
func (r *repositoryStub) GetGlobalSourceParserRule(_ context.Context, ruleID uuid.UUID) (transactionstore.GlobalSourceParserRule, error) {
	r.globalRuleID = ruleID
	return r.globalRule, r.actionErr
}
func (r *repositoryStub) CreateGlobalSourceParserRule(_ context.Context, userID uuid.UUID, input transactionstore.GlobalSourceParserRuleInput) (transactionstore.GlobalSourceParserRule, error) {
	r.mutationCalls++
	r.globalEditorID, r.globalRuleInput = userID, input
	return r.globalRule, r.actionErr
}
func (r *repositoryStub) UpdateGlobalSourceParserRule(_ context.Context, userID, ruleID uuid.UUID, input transactionstore.GlobalSourceParserRuleInput) (transactionstore.GlobalSourceParserRule, error) {
	r.mutationCalls++
	r.globalEditorID, r.globalRuleID, r.globalRuleInput = userID, ruleID, input
	return r.globalRule, r.actionErr
}
func (r *repositoryStub) GetDefaultParserInstructions(_ context.Context, userID uuid.UUID) (transactionstore.DefaultParserInstructions, error) {
	r.defaultLoadedUserID = userID
	return r.defaultLoaded, r.actionErr
}
func (r *repositoryStub) GetUserSourceParserRule(_ context.Context, userID, ruleID uuid.UUID) (transactionstore.UserSourceParserRule, error) {
	r.loadedUserRuleUserID, r.ruleID = userID, ruleID
	return r.rule, r.actionErr
}
func (r *repositoryStub) ListPromptPreviewSources(_ context.Context, _ uuid.UUID, limit int) ([]transactionstore.PromptPreviewSource, error) {
	r.previewSourceLimit = limit
	return r.previewSources, r.actionErr
}
func (r *repositoryStub) LoadSourceParseInput(_ context.Context, userID, sourceID uuid.UUID) (transactionstore.SourceParseInput, error) {
	r.parseInputUserID, r.parseInputSourceID = userID, sourceID
	return r.parseInput, r.actionErr
}
func (r *repositoryStub) PutDefaultParserInstructions(_ context.Context, _ uuid.UUID, instructions string) (transactionstore.DefaultParserInstructions, error) {
	r.mutationCalls++
	r.defaultSaved.DefaultInstructions = instructions
	return r.defaultSaved, r.actionErr
}
func (r *repositoryStub) CreateUserSourceParserRule(_ context.Context, _ uuid.UUID, input transactionstore.UserSourceParserRuleInput) (transactionstore.UserSourceParserRule, error) {
	r.mutationCalls++
	r.ruleInput = input
	return r.rule, r.actionErr
}
func (r *repositoryStub) UpdateUserSourceParserRule(_ context.Context, _ uuid.UUID, ruleID uuid.UUID, input transactionstore.UserSourceParserRuleInput) (transactionstore.UserSourceParserRule, error) {
	r.mutationCalls++
	r.ruleID, r.ruleInput = ruleID, input
	return r.rule, r.actionErr
}
func (r *repositoryStub) RetireUserSourceParserRule(_ context.Context, _ uuid.UUID, ruleID uuid.UUID) error {
	r.mutationCalls++
	r.ruleID = ruleID
	return r.actionErr
}
func (r *repositoryStub) CreateAccountMatchingKey(_ context.Context, _ uuid.UUID, input transactionstore.AccountMatchingKeyInput) (transactionstore.AccountMatchingKey, error) {
	r.mutationCalls++
	r.matchingKeyInput = input
	return r.matchingKey, r.actionErr
}
func (r *repositoryStub) SetAccountMatchingKeyActive(_ context.Context, _ uuid.UUID, keyID uuid.UUID, active bool) (transactionstore.AccountMatchingKey, error) {
	r.mutationCalls++
	r.matchingKeyID, r.matchingKeyActive = keyID, active
	return r.matchingKey, r.actionErr
}
func (r *repositoryStub) GetSourceParseDebug(context.Context, uuid.UUID, uuid.UUID) (transactionstore.SourceParseDebug, error) {
	return r.debug, r.actionErr
}
func (r *repositoryStub) GetSourceParseAuditField(_ context.Context, _ uuid.UUID, sourceID, attemptID uuid.UUID, field string) (transactionstore.SourceParseAuditField, error) {
	r.debugSourceID, r.debugAttemptID, r.debugFieldName = sourceID, attemptID, field
	return r.debugField, r.actionErr
}
func (r *repositoryStub) StageSourceDeletion(_ context.Context, _ uuid.UUID, sourceID uuid.UUID) (transactionstore.SourceDeletionResult, error) {
	r.mutationCalls++
	r.stagedSourceID = sourceID
	return r.deletionResult, r.actionErr
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

func TestValidateLineItemsRejectsCollectionAndDescriptionBounds(t *testing.T) {
	validItem := lineItemRequest{
		SchemaVersion: 1,
		Description:   "Coffee",
		Quantity:      1,
		Currency:      "SGD",
		Details:       json.RawMessage(`{}`),
	}
	tooMany := make([]lineItemRequest, 101)
	for index := range tooMany {
		tooMany[index] = validItem
	}
	encoded, err := json.Marshal(tooMany)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = validateLineItems(encoded); err == nil || !strings.Contains(err.Error(), "at most 100 items") {
		t.Fatalf("validateLineItems(101 items) error = %v", err)
	}

	validItem.Description = "  " + strings.Repeat("界", 250) + "  "
	encoded, err = json.Marshal([]lineItemRequest{validItem})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = validateLineItems(encoded); err != nil {
		t.Fatalf("validateLineItems(250 Unicode characters) error = %v", err)
	}

	validItem.Description = strings.Repeat("界", 251)
	encoded, err = json.Marshal([]lineItemRequest{validItem})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = validateLineItems(encoded); err == nil || !strings.Contains(err.Error(), "description must be at most 250 characters") {
		t.Fatalf("validateLineItems(251 Unicode characters) error = %v", err)
	}

	validItem.Description = "Oversized metadata"
	validItem.Details = json.RawMessage(`{"blob":"` + strings.Repeat("x", 256*1024) + `"}`)
	encoded, err = json.Marshal([]lineItemRequest{validItem})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = validateLineItems(encoded); err == nil || !strings.Contains(err.Error(), "serialized line_items must not exceed 262144 bytes") {
		t.Fatalf("validateLineItems(oversized details) error = %v", err)
	}
}

func TestTransactionResponseDecodesStoredLineItemMoneyRepresentations(t *testing.T) {
	raw := json.RawMessage(`[
		{"schema_version":1,"description":"Coffee","quantity":2,"unit_price_minor":"000625","line_total_minor":1250,"tax_minor":null,"currency":"SGD","details":{"sku":"latte"}},
		{"schema_version":1,"description":"Voucher","quantity":1,"line_total_minor":"0","tax_minor":0,"discount_minor":"9223372036854775807","currency":"SGD","details":{}}
	]`)

	response := transactionResponse(transactionstore.Transaction{LineItems: raw})
	if len(response.LineItems) != 2 {
		t.Fatalf("line items = %#v, want two preserved items", response.LineItems)
	}
	first := response.LineItems[0]
	if first.UnitPriceMinor == nil || *first.UnitPriceMinor != "625" ||
		first.LineTotalMinor == nil || *first.LineTotalMinor != "1250" {
		t.Fatalf("first line item money = unit:%v total:%v", first.UnitPriceMinor, first.LineTotalMinor)
	}
	if first.TaxMinor != nil || first.DiscountMinor != nil {
		t.Fatalf("null/missing optional money = tax:%v discount:%v, want nil", first.TaxMinor, first.DiscountMinor)
	}
	second := response.LineItems[1]
	if second.UnitPriceMinor != nil || second.LineTotalMinor == nil || *second.LineTotalMinor != "0" ||
		second.TaxMinor == nil || *second.TaxMinor != "0" ||
		second.DiscountMinor == nil || *second.DiscountMinor != "9223372036854775807" {
		t.Fatalf("second line item money = unit:%v total:%v tax:%v discount:%v",
			second.UnitPriceMinor, second.LineTotalMinor, second.TaxMinor, second.DiscountMinor)
	}
	if string(first.Details) != `{"sku":"latte"}` {
		t.Fatalf("details = %s, want source value preserved", first.Details)
	}
}

func TestPatchTransactionResponsePreservesStoredStringLineItems(t *testing.T) {
	transactionID := uuid.New()
	repository := &repositoryStub{transaction: transactionstore.Transaction{
		ID: transactionID, AccountID: uuid.New(), TransactionKind: "debit", Title: "Coffee edited",
		OriginalAmountMinor: 1250, OriginalCurrency: "SGD", OccurredAt: time.Now(),
		LineItems:    json.RawMessage(`[{"schema_version":1,"description":"Coffee","quantity":2,"unit_price_minor":"625","line_total_minor":"1250","currency":"SGD","details":{}}]`),
		ReviewStatus: "confirmed", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}}
	mux := authenticatedMux(t, repository, uuid.New())
	request := httptest.NewRequest(http.MethodPatch, "/v1/transactions/"+transactionID.String(), strings.NewReader(`{"title":"Coffee edited"}`))
	request.Header.Set("Authorization", "Bearer valid")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	var body transactionJSON
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.LineItems) != 1 || body.LineItems[0].UnitPriceMinor == nil ||
		*body.LineItems[0].UnitPriceMinor != "625" || body.LineItems[0].LineTotalMinor == nil ||
		*body.LineItems[0].LineTotalMinor != "1250" {
		t.Fatalf("PATCH line items = %#v, want valid stored values preserved", body.LineItems)
	}
}

func TestStoredLineItemDecoderFailsSafely(t *testing.T) {
	validPrefix := `[{"schema_version":1,"description":"Coffee","quantity":1,"unit_price_minor":`
	validSuffix := `,"currency":"SGD","details":{}}]`
	tests := map[string]string{
		"negative number":   `-1`,
		"negative string":   `"-1"`,
		"fraction number":   `1.5`,
		"fraction string":   `"1.5"`,
		"exponent number":   `1e2`,
		"nondecimal string": `"one"`,
		"overflow number":   `9223372036854775808`,
		"overflow string":   `"9223372036854775808"`,
		"wrong JSON type":   `{}`,
	}
	for name, amount := range tests {
		t.Run(name, func(t *testing.T) {
			raw := json.RawMessage(validPrefix + amount + validSuffix)
			if _, err := decodeStoredLineItems(raw); err == nil {
				t.Fatal("decodeStoredLineItems() error = nil, want invalid stored data rejected")
			}
			if got := lineItemsResponse(raw); len(got) != 0 {
				t.Fatalf("lineItemsResponse() = %#v, want fail-closed empty response", got)
			}
		})
	}
}

func TestPatchTransactionAcceptsTrimmedMerchantAndUserNotes(t *testing.T) {
	transactionID := uuid.New()
	repository := &repositoryStub{transaction: transactionstore.Transaction{
		ID: transactionID, AccountID: uuid.New(), TransactionKind: "debit", Title: "Groceries",
		OriginalAmountMinor: 450, OriginalCurrency: "SGD", OccurredAt: time.Now(),
		LineItems: json.RawMessage("[]"), ReviewStatus: "confirmed", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}}
	mux := authenticatedMux(t, repository, uuid.New())
	request := httptest.NewRequest(
		http.MethodPatch,
		"/v1/transactions/"+transactionID.String(),
		strings.NewReader(`{"merchant_name":"  FairPrice  ","user_notes":"  Family groceries  "}`),
	)
	request.Header.Set("Authorization", "Bearer valid")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	if !repository.patch.MerchantName.Set || repository.patch.MerchantName.Value == nil ||
		*repository.patch.MerchantName.Value != "FairPrice" {
		t.Fatalf("merchant patch = %#v", repository.patch.MerchantName)
	}
	if !repository.patch.UserNotes.Set || repository.patch.UserNotes.Value == nil ||
		*repository.patch.UserNotes.Value != "Family groceries" {
		t.Fatalf("notes patch = %#v", repository.patch.UserNotes)
	}
}

func TestPatchTransactionClearsNullableMerchantAndEmptyUserNotes(t *testing.T) {
	repository := &repositoryStub{transaction: transactionstore.Transaction{
		ID: uuid.New(), AccountID: uuid.New(), TransactionKind: "debit", Title: "Groceries",
		OriginalAmountMinor: 450, OriginalCurrency: "SGD", OccurredAt: time.Now(),
		LineItems: json.RawMessage("[]"), ReviewStatus: "confirmed", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}}
	mux := authenticatedMux(t, repository, uuid.New())
	request := httptest.NewRequest(
		http.MethodPatch,
		"/v1/transactions/"+repository.transaction.ID.String(),
		strings.NewReader(`{"merchant_name":null,"user_notes":"   "}`),
	)
	request.Header.Set("Authorization", "Bearer valid")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	if !repository.patch.MerchantName.Set || repository.patch.MerchantName.Value != nil ||
		!repository.patch.UserNotes.Set || repository.patch.UserNotes.Value != nil {
		t.Fatalf("nullable patch = %#v", repository.patch)
	}
}

func TestPatchTransactionRejectsInvalidMerchantAndUserNotes(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "empty merchant", body: `{"merchant_name":"   "}`},
		{name: "long merchant", body: fmt.Sprintf(`{"merchant_name":%q}`, strings.Repeat("m", 251))},
		{name: "numeric merchant", body: `{"merchant_name":42}`},
		{name: "long notes", body: fmt.Sprintf(`{"user_notes":%q}`, strings.Repeat("n", 4001))},
		{name: "numeric notes", body: `{"user_notes":42}`},
		{name: "wrong field name", body: `{"notes":"not the API field"}`},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			mux := authenticatedMux(t, &repositoryStub{}, uuid.New())
			request := httptest.NewRequest(http.MethodPatch, "/v1/transactions/"+uuid.NewString(), strings.NewReader(testCase.body))
			request.Header.Set("Authorization", "Bearer valid")
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
		})
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

func TestTransactionSettingsCRUDUsesStrictValidatedContracts(t *testing.T) {
	userID, ruleID, accountID, keyID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	active := true
	repository := &repositoryStub{
		settings:     transactionstore.TransactionSettings{DefaultInstructions: "existing", DefaultInstructionsVersion: 2},
		defaultSaved: transactionstore.DefaultParserInstructions{DefaultInstructionsVersion: 3},
		rule:         transactionstore.UserSourceParserRule{ID: ruleID, Name: "FairPrice", Provider: "gmail", SenderMatchType: "domain", SenderMatchValue: "fairprice.com.sg", Active: true, Version: 1},
		matchingKey:  transactionstore.AccountMatchingKey{ID: keyID, AccountID: accountID, AccountName: "Card", KeyType: "card_last_four", DisplayValue: "•••• 2562", NormalizedValue: "2562", Active: true},
	}
	mux := authenticatedMux(t, repository, userID)

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/transactions/settings", nil)
	request.Header.Set("Authorization", "Bearer valid")
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"default_instructions_version":2`) {
		t.Fatalf("settings response = %d %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPut, "/v1/transactions/settings/default-instructions", strings.NewReader(`{"default_instructions":" explicit facts "}`))
	request.Header.Set("Authorization", "Bearer valid")
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK || repository.defaultSaved.DefaultInstructions != "explicit facts" || !strings.Contains(response.Body.String(), `"default_instructions_version":3`) {
		t.Fatalf("default response=%d %s saved=%#v", response.Code, response.Body.String(), repository.defaultSaved)
	}

	rulePayload, _ := json.Marshal(sourceRuleRequest{
		Name: "FairPrice", Provider: "gmail", SenderMatchType: "domain",
		SenderMatchValue: "@FairPrice.com.sg", SubjectMatcher: stringPointer(`(?i)app receipt`),
		ContentMatcher: stringPointer(`Mastercard`), PromptFragment: "receipt guidance",
		Priority: 100, Active: &active,
	})
	response = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/v1/transactions/settings/source-rules", bytes.NewReader(rulePayload))
	request.Header.Set("Authorization", "Bearer valid")
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || repository.ruleInput.SenderMatchValue != "fairprice.com.sg" || repository.ruleInput.SubjectMatcher == nil {
		t.Fatalf("source rule response=%d %s input=%#v", response.Code, response.Body.String(), repository.ruleInput)
	}

	response = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/v1/transactions/settings/matching-keys", strings.NewReader(fmt.Sprintf(`{"account_id":%q,"key_type":"card_last_four","display_value":"•••• 2562"}`, accountID)))
	request.Header.Set("Authorization", "Bearer valid")
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || repository.matchingKeyInput.DisplayValue != "•••• 2562" {
		t.Fatalf("matching key response=%d %s input=%#v", response.Code, response.Body.String(), repository.matchingKeyInput)
	}

	response = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPatch, "/v1/transactions/settings/matching-keys/"+keyID.String(), strings.NewReader(`{"active":false}`))
	request.Header.Set("Authorization", "Bearer valid")
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK || repository.matchingKeyID != keyID || repository.matchingKeyActive {
		t.Fatalf("matching key patch=%d %s id=%s active=%t", response.Code, response.Body.String(), repository.matchingKeyID, repository.matchingKeyActive)
	}
}

func TestSourceRuleAndMatchingKeyValidationRejectUnsafeConfiguration(t *testing.T) {
	tests := []struct {
		name, method, path, body string
	}{
		{name: "invalid sender regex", method: http.MethodPost, path: "/v1/transactions/settings/source-rules", body: `{"name":"bad","provider":"gmail","sender_match_type":"regex","sender_match_value":"(","prompt_fragment":"","priority":0,"active":true}`},
		{name: "invalid content regex", method: http.MethodPost, path: "/v1/transactions/settings/source-rules", body: `{"name":"bad","provider":"gmail","sender_match_type":"domain","sender_match_value":"example.test","content_matcher":"(?<=lookbehind)","prompt_fragment":"","priority":0,"active":true}`},
		{name: "full PAN", method: http.MethodPost, path: "/v1/transactions/settings/matching-keys", body: fmt.Sprintf(`{"account_id":%q,"key_type":"card_last_four","display_value":"5555444433332562"}`, uuid.New())},
		{name: "unknown key mutation", method: http.MethodPatch, path: "/v1/transactions/settings/matching-keys/" + uuid.NewString(), body: `{"active":true,"account_id":"` + uuid.NewString() + `"}`},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			mux := authenticatedMux(t, &repositoryStub{}, uuid.New())
			request := httptest.NewRequest(testCase.method, testCase.path, strings.NewReader(testCase.body))
			request.Header.Set("Authorization", "Bearer valid")
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestSourceDebugAndDeletionAreOwnerScopedWorkflows(t *testing.T) {
	userID, sourceID := uuid.New(), uuid.New()
	attemptID := uuid.New()
	exactProviderRequest := "{\n  \"z\": 9007199254740993,\n  \"a\": true\n}"
	exactProviderResponse := "{\"choices\": [{\"message\": {\"content\": \"{}\"}}]}"
	exactModelOutput := "{ \"large_id\": 9007199254740993 }"
	repository := &repositoryStub{
		debug: transactionstore.SourceParseDebug{SourceID: sourceID, Attempts: []transactionstore.ParseAttemptDebug{{
			ID: attemptID, ValidationStatus: "valid", PromptComponents: json.RawMessage(`{}`), CreatedAt: time.Now(),
			ProviderRequest: &exactProviderRequest, ProviderResponse: &exactProviderResponse, ModelOutput: &exactModelOutput,
		}}, HasMore: true, Truncated: true},
		debugField: transactionstore.SourceParseAuditField{
			SourceID: sourceID, AttemptID: attemptID, Field: "provider_request",
			Value: &exactProviderRequest, MaxBytes: 10485760,
		},
		deletionResult: transactionstore.SourceDeletionResult{Status: "cleanup_pending", CleanupPending: true},
	}
	storage := &attachmentSignerStub{}
	mux := authenticatedMux(t, repository, userID, storage)
	request := httptest.NewRequest(http.MethodGet, "/v1/transactions/sources/"+sourceID.String()+"/debug", nil)
	request.Header.Set("Authorization", "Bearer valid")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" ||
		!strings.Contains(response.Body.String(), `"attempts":[`) ||
		!strings.Contains(response.Body.String(), `"has_more":true`) || !strings.Contains(response.Body.String(), `"truncated":true`) ||
		strings.Contains(response.Body.String(), `"items"`) {
		t.Fatalf("debug response = %d %s", response.Code, response.Body.String())
	}
	var debugResponse struct {
		Attempts []struct {
			ProviderRequest  *string `json:"provider_request"`
			ProviderResponse *string `json:"provider_response"`
			ModelOutput      *string `json:"model_output"`
		} `json:"attempts"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &debugResponse); err != nil || len(debugResponse.Attempts) != 1 {
		t.Fatalf("decode debug response: %v body=%s", err, response.Body.String())
	}
	attempt := debugResponse.Attempts[0]
	if attempt.ProviderRequest == nil || *attempt.ProviderRequest != exactProviderRequest ||
		attempt.ProviderResponse == nil || *attempt.ProviderResponse != exactProviderResponse ||
		attempt.ModelOutput == nil || *attempt.ModelOutput != exactModelOutput {
		t.Fatalf("exact audit JSON strings were not preserved: %#v", attempt)
	}

	request = httptest.NewRequest(http.MethodGet,
		"/v1/transactions/sources/"+sourceID.String()+"/debug/attempts/"+attemptID.String()+"/fields/provider_request", nil)
	request.Header.Set("Authorization", "Bearer valid")
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" ||
		repository.debugSourceID != sourceID || repository.debugAttemptID != attemptID ||
		repository.debugFieldName != "provider_request" {
		t.Fatalf("exact field response=%d source=%s attempt=%s field=%q body=%s", response.Code,
			repository.debugSourceID, repository.debugAttemptID, repository.debugFieldName, response.Body.String())
	}
	var exactField struct {
		Value    *string `json:"value"`
		MaxBytes int     `json:"max_bytes"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &exactField); err != nil || exactField.Value == nil ||
		*exactField.Value != exactProviderRequest || exactField.MaxBytes != 10485760 {
		t.Fatalf("exact field decode error=%v field=%#v", err, exactField)
	}

	request = httptest.NewRequest(http.MethodDelete, "/v1/transactions/sources/"+sourceID.String(), nil)
	request.Header.Set("Authorization", "Bearer valid")
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || repository.stagedSourceID != sourceID ||
		!strings.Contains(response.Body.String(), `"status":"cleanup_pending"`) ||
		!strings.Contains(response.Body.String(), `"cleanup_pending":true`) || len(storage.deleted) != 0 {
		t.Fatalf("delete response=%d staged=%s body=%s storage=%#v", response.Code, repository.stagedSourceID, response.Body.String(), storage.deleted)
	}
}

func TestSourceDebugFieldRejectsUnsupportedField(t *testing.T) {
	userID, sourceID, attemptID := uuid.New(), uuid.New(), uuid.New()
	repository := &repositoryStub{actionErr: transactionstore.ErrSourceDebugFieldUnsupported}
	mux := authenticatedMux(t, repository, userID)
	request := httptest.NewRequest(http.MethodGet,
		"/v1/transactions/sources/"+sourceID.String()+"/debug/attempts/"+attemptID.String()+"/fields/account_catalog", nil)
	request.Header.Set("Authorization", "Bearer valid")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "Unsupported debug field") {
		t.Fatalf("response=%d %s", response.Code, response.Body.String())
	}
}

func TestSourceDebugFieldDoesNotRevealMissingOrCrossOwnerAttempt(t *testing.T) {
	userID, sourceID, attemptID := uuid.New(), uuid.New(), uuid.New()
	repository := &repositoryStub{actionErr: transactionstore.ErrSourceNotFound}
	mux := authenticatedMux(t, repository, userID)
	request := httptest.NewRequest(http.MethodGet,
		"/v1/transactions/sources/"+sourceID.String()+"/debug/attempts/"+attemptID.String()+"/fields/model_output", nil)
	request.Header.Set("Authorization", "Bearer valid")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), "Debug field not found") {
		t.Fatalf("response=%d %s", response.Code, response.Body.String())
	}
}

func TestSourceDeletionReturnsConflictDuringActiveGmailIngestion(t *testing.T) {
	userID, sourceID := uuid.New(), uuid.New()
	repository := &repositoryStub{actionErr: transactionstore.ErrSourceDeletionIngestionActive}
	mux := authenticatedMux(t, repository, userID)
	request := httptest.NewRequest(http.MethodDelete, "/v1/transactions/sources/"+sourceID.String(), nil)
	request.Header.Set("Authorization", "Bearer valid")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "Wait for Gmail sync") {
		t.Fatalf("response=%d %s", response.Code, response.Body.String())
	}
}

func TestSourceDeletionWithoutAttachmentsCompletesImmediately(t *testing.T) {
	userID, sourceID := uuid.New(), uuid.New()
	repository := &repositoryStub{deletionResult: transactionstore.SourceDeletionResult{Status: "completed"}}
	mux := authenticatedMux(t, repository, userID)
	request := httptest.NewRequest(http.MethodDelete, "/v1/transactions/sources/"+sourceID.String(), nil)
	request.Header.Set("Authorization", "Bearer valid")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"completed"`) ||
		!strings.Contains(response.Body.String(), `"cleanup_pending":false`) {
		t.Fatalf("response=%d %s", response.Code, response.Body.String())
	}
}

func TestGlobalSettingsListsExactRuleProjectionWithoutCaching(t *testing.T) {
	userID, ruleID := uuid.New(), uuid.New()
	updatedBy := uuid.New()
	repository := &repositoryStub{globalRules: []transactionstore.GlobalSourceParserRule{{
		ID: ruleID, Name: "Masked card", Provider: "gmail",
		SenderMatcher: stringPointer(`@example\.test$`), ContentMatcher: stringPointer(`\*{4} 2562`),
		PromptFragment: "Read the payment method.", ExtractionConfig: json.RawMessage(`{"extractors":{}}`),
		Version: 3, Priority: 50, Active: true, UpdatedByUserID: &updatedBy,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}}}
	mux := authenticatedMux(t, repository, userID)
	request := httptest.NewRequest(http.MethodGet, "/v1/transactions/global-settings", nil)
	request.Header.Set("Authorization", "Bearer valid")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("response = %d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	var body struct {
		Rules []transactionstore.GlobalSourceParserRule `json:"rules"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || len(body.Rules) != 1 {
		t.Fatalf("decode error=%v body=%s", err, response.Body.String())
	}
	if body.Rules[0].ID != ruleID || body.Rules[0].Name != "Masked card" ||
		string(body.Rules[0].ExtractionConfig) != `{"extractors":{}}` ||
		body.Rules[0].UpdatedByUserID == nil || *body.Rules[0].UpdatedByUserID != updatedBy {
		t.Fatalf("rule projection = %#v", body.Rules[0])
	}
}

func TestCreateGlobalSourceRuleValidatesAndBindsAuthenticatedEditor(t *testing.T) {
	userID := uuid.New()
	repository := &repositoryStub{globalRule: transactionstore.GlobalSourceParserRule{ID: uuid.New(), Name: "FairPrice", Provider: "gmail"}}
	mux := authenticatedMux(t, repository, userID)
	body := `{
		"name":"  FairPrice  ","provider":"gmail",
		"sender_matcher":"  @fairprice\\.com\\.sg$  ",
		"content_matcher":"  Mastercard  ",
		"prompt_fragment":"  Read the receipt.  ",
		"priority":50,"active":true
	}`
	request := httptest.NewRequest(http.MethodPost, "/v1/transactions/global-settings/source-rules", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer valid")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	input := repository.globalRuleInput
	if repository.globalEditorID != userID || input.Name != "FairPrice" || input.Provider != "gmail" ||
		input.SenderMatcher == nil || *input.SenderMatcher != `@fairprice\.com\.sg$` ||
		input.ContentMatcher == nil || *input.ContentMatcher != "Mastercard" ||
		input.PromptFragment != "Read the receipt." || input.Priority != 50 || !input.Active ||
		input.ExpectedVersion != 0 {
		t.Fatalf("editor=%s input=%#v", repository.globalEditorID, input)
	}
	if repository.mutationCalls != 1 {
		t.Fatalf("mutation calls = %d", repository.mutationCalls)
	}
}

func TestGlobalSourceRuleRejectsInvalidMatcherAndReadOnlyExtractionConfig(t *testing.T) {
	for name, body := range map[string]string{
		"invalid RE2":                 `{"name":"Rule","provider":"gmail","sender_matcher":"[","prompt_fragment":"","priority":0,"active":true}`,
		"read-only extraction config": `{"name":"Rule","provider":"gmail","prompt_fragment":"","extraction_config":{},"priority":0,"active":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			repository := &repositoryStub{}
			mux := authenticatedMux(t, repository, uuid.New())
			request := httptest.NewRequest(http.MethodPost, "/v1/transactions/global-settings/source-rules", strings.NewReader(body))
			request.Header.Set("Authorization", "Bearer valid")
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest || repository.mutationCalls != 0 {
				t.Fatalf("response=%d mutations=%d body=%s", response.Code, repository.mutationCalls, response.Body.String())
			}
		})
	}
}

func TestUpdateGlobalSourceRuleMapsOptimisticConflictTo409(t *testing.T) {
	ruleID := uuid.New()
	repository := &repositoryStub{actionErr: transactionstore.ErrGlobalSourceRuleConflict}
	mux := authenticatedMux(t, repository, uuid.New())
	body := `{"name":"Rule","provider":"gmail","prompt_fragment":"","priority":0,"active":false,"expected_version":4}`
	request := httptest.NewRequest(http.MethodPut, "/v1/transactions/global-settings/source-rules/"+ruleID.String(), strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer valid")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusConflict || repository.globalRuleID != ruleID ||
		repository.globalRuleInput.ExpectedVersion != 4 || !strings.Contains(response.Body.String(), "Reload") {
		t.Fatalf("response=%d id=%s input=%#v body=%s", response.Code, repository.globalRuleID, repository.globalRuleInput, response.Body.String())
	}
}

func TestPromptPreviewSourcesUsesOwnedRecentEmailContract(t *testing.T) {
	userID, sourceID := uuid.New(), uuid.New()
	repository := &repositoryStub{previewSources: []transactionstore.PromptPreviewSource{{
		ID: sourceID, Subject: "Receipt", Sender: "store@example.test",
		ReceivedAt: time.Date(2026, 9, 3, 1, 2, 3, 0, time.UTC), ParseStatus: "parsed",
	}}}
	mux := authenticatedMux(t, repository, userID)
	request := httptest.NewRequest(http.MethodGet, "/v1/transactions/prompt-preview/sources", nil)
	request.Header.Set("Authorization", "Bearer valid")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" || repository.previewSourceLimit != 100 {
		t.Fatalf("response=%d limit=%d headers=%v body=%s", response.Code, repository.previewSourceLimit, response.Header(), response.Body.String())
	}
	var body struct {
		Sources []transactionstore.PromptPreviewSource `json:"sources"`
		Items   json.RawMessage                        `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || len(body.Sources) != 1 || body.Items != nil || body.Sources[0].ID != sourceID {
		t.Fatalf("decode error=%v response=%#v body=%s", err, body, response.Body.String())
	}
}

func TestAutomaticPromptPreviewReusesProductionSelectionAndOmitsDynamicContent(t *testing.T) {
	userID, sourceID := uuid.New(), uuid.New()
	globalID, userRuleID := uuid.NewString(), uuid.NewString()
	repository := &repositoryStub{parseInput: transactionstore.SourceParseInput{
		ID: sourceID, Subject: "Your FairPrice Group app receipt",
		Sender: "FairPrice <receipt@fairprice.com.sg>", Content: "BODY-SHOULD-NOT-APPEAR Mastercard (**** 2562)",
		ReceivedAt: time.Date(2026, 9, 3, 1, 2, 3, 0, time.UTC), ParseStatus: "parsed",
		NormalizedContent:   "subject: Your FairPrice Group app receipt\nsender: FairPrice <receipt@fairprice.com.sg>\ntext: BODY-SHOULD-NOT-APPEAR Mastercard (**** 2562)",
		DefaultInstructions: "Prefer explicit source facts.", DefaultInstructionsVersion: 3,
		Rules: []parserrules.Rule{
			{ID: uuid.NewString(), Name: "Higher non-match", Version: 1, Priority: 100, ContentMatcher: "DigitalOcean", ExtractionConfig: json.RawMessage(`{}`)},
			{ID: globalID, Name: "Masked card", Version: 2, Priority: 50, ContentMatcher: `Mastercard \(\*{4} 2562\)`, PromptFragment: "Read the payment method.", ExtractionConfig: json.RawMessage(`{}`)},
		},
		UserRules: []parserrules.UserRule{{
			ID: userRuleID, Name: "FairPrice", Version: 4, Priority: 10,
			SenderMatchType: "domain", SenderMatchValue: "fairprice.com.sg",
			SubjectMatcher: "app receipt", PromptFragment: "Use item details.",
		}},
		Attachments: []transactionstore.SourceAttachment{{
			Filename: "receipt.png", MIMEType: "image/png", ObjectPath: userID.String() + "/" + sourceID.String() + "/receipt.png",
			StorageStatus: "stored", ParseEligible: true,
		}},
	}}
	mux := authenticatedMux(t, repository, userID)
	request := httptest.NewRequest(http.MethodPost, "/v1/transactions/prompt-preview", strings.NewReader(`{"mode":"automatic","data_source_id":"`+sourceID.String()+`"}`))
	request.Header.Set("Authorization", "Bearer valid")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("response=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	if repository.parseInputUserID != userID || repository.parseInputSourceID != sourceID || repository.mutationCalls != 0 {
		t.Fatalf("load owner=%s source=%s mutations=%d", repository.parseInputUserID, repository.parseInputSourceID, repository.mutationCalls)
	}
	if strings.Contains(response.Body.String(), "BODY-SHOULD-NOT-APPEAR") {
		t.Fatalf("dynamic email content leaked: %s", response.Body.String())
	}
	var body struct {
		AssembledSystemPrompt string                               `json:"assembled_system_prompt"`
		SelectedSource        transactionstore.PromptPreviewSource `json:"selected_source"`
		Selection             struct {
			GlobalRule *promptPreviewSelectionItem `json:"global_rule"`
			UserRule   *promptPreviewSelectionItem `json:"user_rule"`
		} `json:"selection"`
		ProviderRequest struct {
			Model          string `json:"model"`
			EnableThinking bool   `json:"enable_thinking"`
			ResponseFormat struct {
				Type string `json:"type"`
			} `json:"response_format"`
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		} `json:"provider_request"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.SelectedSource.ID != sourceID || body.Selection.GlobalRule == nil || body.Selection.GlobalRule.ID != globalID ||
		body.Selection.UserRule == nil || body.Selection.UserRule.ID != userRuleID {
		t.Fatalf("preview selection = %#v source=%#v", body.Selection, body.SelectedSource)
	}
	if body.ProviderRequest.Model != "qwen3.8-flash" || body.ProviderRequest.EnableThinking || body.ProviderRequest.ResponseFormat.Type != "json_object" || len(body.ProviderRequest.Messages) != 2 {
		t.Fatalf("provider request = %#v", body.ProviderRequest)
	}
	var systemPrompt string
	if err := json.Unmarshal(body.ProviderRequest.Messages[0].Content, &systemPrompt); err != nil || systemPrompt != body.AssembledSystemPrompt {
		t.Fatalf("system prompt mismatch error=%v", err)
	}
	var userParts []struct {
		Text     string `json:"text"`
		ImageURL *struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if err := json.Unmarshal(body.ProviderRequest.Messages[1].Content, &userParts); err != nil || len(userParts) != 2 ||
		userParts[0].Text != providers.PreviewEmailContentPlaceholder || userParts[1].ImageURL == nil ||
		userParts[1].ImageURL.URL != providers.PreviewAttachmentPlaceholder {
		t.Fatalf("provider placeholders missing: parts=%#v error=%v", userParts, err)
	}
}

func TestManualPromptPreviewLoadsOnlyOwnedConfigurationAndAllowsInactiveRules(t *testing.T) {
	userID, globalID, userRuleID := uuid.New(), uuid.New(), uuid.New()
	repository := &repositoryStub{
		globalRule: transactionstore.GlobalSourceParserRule{
			ID: globalID, Name: "Inactive global", Version: 2, Active: false, PromptFragment: "Global guidance.",
		},
		defaultLoaded: transactionstore.DefaultParserInstructions{DefaultInstructions: "Default guidance.", DefaultInstructionsVersion: 3},
		rule: transactionstore.UserSourceParserRule{
			ID: userRuleID, Name: "Inactive user", Version: 4, Active: false, PromptFragment: "User guidance.",
		},
	}
	mux := authenticatedMux(t, repository, userID)
	body := `{"mode":"manual","global_rule_id":"` + globalID.String() + `","include_user_default":true,"user_rule_id":"` + userRuleID.String() + `"}`
	request := httptest.NewRequest(http.MethodPost, "/v1/transactions/prompt-preview", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer valid")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK || repository.globalRuleID != globalID || repository.ruleID != userRuleID ||
		repository.defaultLoadedUserID != userID || repository.loadedUserRuleUserID != userID || repository.mutationCalls != 0 {
		t.Fatalf("response=%d global=%s user=%s defaultOwner=%s ruleOwner=%s mutations=%d body=%s",
			response.Code, repository.globalRuleID, repository.ruleID, repository.defaultLoadedUserID,
			repository.loadedUserRuleUserID, repository.mutationCalls, response.Body.String())
	}
	for _, fragment := range []string{"Global guidance.", "Default guidance.", "User guidance."} {
		if !strings.Contains(response.Body.String(), fragment) {
			t.Fatalf("preview omitted %q: %s", fragment, response.Body.String())
		}
	}
}

func TestPromptPreviewHidesCrossOwnerSourceAndRule(t *testing.T) {
	for name, testCase := range map[string]struct {
		body string
		err  error
	}{
		"automatic source": {body: `{"mode":"automatic","data_source_id":"` + uuid.NewString() + `"}`, err: pgx.ErrNoRows},
		"manual user rule": {body: `{"mode":"manual","include_user_default":false,"user_rule_id":"` + uuid.NewString() + `"}`, err: transactionstore.ErrUserSourceRuleNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			repository := &repositoryStub{actionErr: testCase.err}
			mux := authenticatedMux(t, repository, uuid.New())
			request := httptest.NewRequest(http.MethodPost, "/v1/transactions/prompt-preview", strings.NewReader(testCase.body))
			request.Header.Set("Authorization", "Bearer valid")
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			if response.Code != http.StatusNotFound || repository.mutationCalls != 0 {
				t.Fatalf("response=%d mutations=%d body=%s", response.Code, repository.mutationCalls, response.Body.String())
			}
		})
	}
}

func TestPromptPreviewRequiresAuthentication(t *testing.T) {
	repository := &repositoryStub{}
	mux := authenticatedMux(t, repository, uuid.New())
	request := httptest.NewRequest(http.MethodPost, "/v1/transactions/prompt-preview", strings.NewReader(`{"mode":"manual","include_user_default":false}`))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || repository.mutationCalls != 0 {
		t.Fatalf("response=%d mutations=%d body=%s", response.Code, repository.mutationCalls, response.Body.String())
	}
}

func authenticatedMux(t *testing.T, repository Repository, userID uuid.UUID, signers ...AttachmentStorage) *http.ServeMux {
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
func stringPointer(value string) *string     { return &value }

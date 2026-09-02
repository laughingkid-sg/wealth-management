package transactions

import (
	"context"
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
	run       transactionstore.SyncRun
	createErr error
	sources   []transactionstore.SourceSummary
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

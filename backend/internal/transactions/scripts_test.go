package transactions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/zhengteck/wealth-builder/backend/internal/auth"
	"github.com/zhengteck/wealth-builder/backend/internal/scriptstore"
)

type fakeScriptRepo struct {
	created   scriptstore.ScriptVersion
	activated int
}

func (f *fakeScriptRepo) ListScripts(context.Context) ([]scriptstore.ScriptSummary, error) {
	return []scriptstore.ScriptSummary{{Key: "email_pre_process", ActiveVersion: 2, VersionCount: 3}}, nil
}
func (f *fakeScriptRepo) ListVersions(context.Context, string) ([]scriptstore.ScriptVersion, error) {
	return []scriptstore.ScriptVersion{{Key: "email_pre_process", Version: 1}}, nil
}
func (f *fakeScriptRepo) GetVersion(_ context.Context, key string, version int) (scriptstore.ScriptVersion, error) {
	return scriptstore.ScriptVersion{Key: key, Version: version, Source: "output := input"}, nil
}
func (f *fakeScriptRepo) CreateVersion(_ context.Context, key, source, _ string, _ uuid.UUID) (scriptstore.ScriptVersion, error) {
	f.created = scriptstore.ScriptVersion{Key: key, Version: 4, Source: source, Checksum: scriptstore.Checksum(source)}
	return f.created, nil
}
func (f *fakeScriptRepo) Activate(_ context.Context, _ string, version int) error {
	f.activated = version
	return nil
}

type fakeScriptRunner struct{}

func (fakeScriptRunner) Run(_ context.Context, _ string, input json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`{"echo":true}`), nil
}

func scriptMux(t *testing.T, repo ScriptRepository, runner ScriptRunner) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	NewHandler(&repositoryStub{}, false, nil, nil).WithScripts(repo, runner).
		Register(mux, verifierFunc(func(context.Context, string) (auth.User, error) { return auth.User{ID: uuid.New()}, nil }))
	return mux
}

func do(t *testing.T, mux http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, path, reader)
	request.Header.Set("Authorization", "Bearer valid")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}

func TestScriptEndpointsManageVersions(t *testing.T) {
	repo := &fakeScriptRepo{}
	mux := scriptMux(t, repo, fakeScriptRunner{})

	if got := do(t, mux, http.MethodGet, "/v1/transactions/scripts", "").Code; got != http.StatusOK {
		t.Fatalf("list scripts status = %d", got)
	}
	create := do(t, mux, http.MethodPost, "/v1/transactions/scripts/email_pre_process/versions", `{"source":"output := input","notes":"first"}`)
	if create.Code != http.StatusCreated || repo.created.Version != 4 {
		t.Fatalf("create version status = %d created = %#v", create.Code, repo.created)
	}
	activate := do(t, mux, http.MethodPost, "/v1/transactions/scripts/email_pre_process/activate", `{"version":4}`)
	if activate.Code != http.StatusOK || repo.activated != 4 {
		t.Fatalf("activate status = %d activated = %d", activate.Code, repo.activated)
	}
}

func TestScriptDryRunReturnsOutput(t *testing.T) {
	mux := scriptMux(t, &fakeScriptRepo{}, fakeScriptRunner{})
	response := do(t, mux, http.MethodPost, "/v1/transactions/scripts/dry-run", `{"source":"output := input","input":{"a":1}}`)
	if response.Code != http.StatusOK {
		t.Fatalf("dry-run status = %d", response.Code)
	}
	var decoded struct {
		OK     bool            `json:"ok"`
		Output json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.OK || !strings.Contains(string(decoded.Output), "echo") {
		t.Fatalf("unexpected dry-run body: %s", response.Body.String())
	}
}

func TestScriptEndpointsDisabledWithoutStore(t *testing.T) {
	mux := http.NewServeMux()
	NewHandler(&repositoryStub{}, false, nil, nil).
		Register(mux, verifierFunc(func(context.Context, string) (auth.User, error) { return auth.User{ID: uuid.New()}, nil }))
	if got := do(t, mux, http.MethodGet, "/v1/transactions/scripts", "").Code; got != http.StatusServiceUnavailable {
		t.Fatalf("disabled list scripts status = %d, want 503", got)
	}
}

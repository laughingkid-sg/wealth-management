package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

type verifierFunc func(context.Context, string) (User, error)

func (f verifierFunc) Verify(ctx context.Context, token string) (User, error) { return f(ctx, token) }

func TestRequireUserRejectsMissingBearerToken(t *testing.T) {
	handler := RequireUser(verifierFunc(func(context.Context, string) (User, error) { return User{}, nil }), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestRequireUserPutsVerifiedUserInContext(t *testing.T) {
	want := User{ID: uuid.New()}
	handler := RequireUser(verifierFunc(func(_ context.Context, token string) (User, error) {
		if token != "valid" {
			return User{}, errors.New("bad token")
		}
		return want, nil
	}), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := UserFromContext(r.Context())
		if !ok || got != want {
			t.Fatalf("UserFromContext() = %#v, %t", got, ok)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer valid")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
	}
}

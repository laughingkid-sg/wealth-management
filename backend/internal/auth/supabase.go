// Package auth verifies browser bearer tokens using Supabase Auth's user endpoint.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"
)

type User struct {
	ID uuid.UUID
}

type Verifier interface {
	Verify(context.Context, string) (User, error)
}

type userContextKey struct{}

// SupabaseUserVerifier is safe for both symmetric and asymmetric Supabase signing keys.
// It follows Supabase's recommended Auth-user verification path for shared-secret projects.
type SupabaseUserVerifier struct {
	userEndpoint string
	apiKey       string
	client       *http.Client
}

func NewSupabaseUserVerifier(supabaseURL *url.URL, serviceRoleKey string, client *http.Client) *SupabaseUserVerifier {
	base := strings.TrimRight(supabaseURL.String(), "/")
	return &SupabaseUserVerifier{userEndpoint: base + "/auth/v1/user", apiKey: serviceRoleKey, client: client}
}

func (v *SupabaseUserVerifier) Verify(ctx context.Context, token string) (User, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, v.userEndpoint, nil)
	if err != nil {
		return User{}, err
	}
	request.Header.Set("apikey", v.apiKey)
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := v.client.Do(request)
	if err != nil {
		return User{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return User{}, errors.New("invalid access token")
	}
	var payload struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return User{}, errors.New("invalid auth response")
	}
	id, err := uuid.Parse(payload.ID)
	if err != nil {
		return User{}, errors.New("invalid auth user ID")
	}
	return User{ID: id}, nil
}

func RequireUser(verifier Verifier, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(header, prefix) || strings.TrimSpace(strings.TrimPrefix(header, prefix)) == "" {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		user, err := verifier.Verify(r.Context(), strings.TrimSpace(strings.TrimPrefix(header, prefix)))
		if err != nil {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userContextKey{}, user)))
	})
}

func UserFromContext(ctx context.Context) (User, bool) {
	user, ok := ctx.Value(userContextKey{}).(User)
	return user, ok
}

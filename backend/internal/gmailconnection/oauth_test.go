package gmailconnection

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/zhengteck/wealth-builder/backend/internal/providers"
	"github.com/zhengteck/wealth-builder/backend/internal/secret"
)

const (
	testGoogleClientID = "gmail-client-id"
	testRedirectURL    = "http://localhost:8080/v1/transactions/gmail/oauth/callback"
)

type oauthStateRepositoryStub struct {
	savedUserID    uuid.UUID
	savedDigest    []byte
	savedVerifier  []byte
	savedExpiresAt time.Time
	saveErr        error

	state        OAuthState
	consumeErr   error
	consumeCalls int
}

func (s *oauthStateRepositoryStub) SaveOAuthState(_ context.Context, userID uuid.UUID, digest, encryptedVerifier []byte, expiresAt time.Time) error {
	s.savedUserID = userID
	s.savedDigest = append([]byte(nil), digest...)
	s.savedVerifier = append([]byte(nil), encryptedVerifier...)
	s.savedExpiresAt = expiresAt
	return s.saveErr
}

func (s *oauthStateRepositoryStub) ConsumeOAuthState(_ context.Context, digest []byte, _ time.Time) (OAuthState, error) {
	s.consumeCalls++
	if s.consumeErr != nil {
		return OAuthState{}, s.consumeErr
	}
	if len(s.savedDigest) == 0 || string(digest) != string(s.savedDigest) {
		return OAuthState{}, errors.New("OAuth state not found")
	}
	if s.consumeCalls > 1 {
		return OAuthState{}, errors.New("OAuth state already consumed")
	}
	return s.state, nil
}

type oauthConnectionRepositoryStub struct {
	encrypted []byte
	metadata  json.RawMessage
	label     string
	calls     int
}

func (s *oauthConnectionRepositoryStub) UpsertGmailConnection(_ context.Context, _ uuid.UUID, encrypted []byte, metadata json.RawMessage, label string) error {
	s.calls++
	s.encrypted = append([]byte(nil), encrypted...)
	s.metadata = append(json.RawMessage(nil), metadata...)
	s.label = label
	return nil
}

type oauthCodeExchangerStub struct {
	result providers.OAuthCodeExchange
	err    error

	calls    int
	code     string
	redirect string
	verifier string
}

func (s *oauthCodeExchangerStub) ExchangeAuthorizationCode(_ context.Context, code, redirectURL, verifier string) (providers.OAuthCodeExchange, error) {
	s.calls++
	s.code = code
	s.redirect = redirectURL
	s.verifier = verifier
	return s.result, s.err
}

func newOAuthServiceForTest(t *testing.T, stateRepository *oauthStateRepositoryStub, connectionRepository *oauthConnectionRepositoryStub, exchanger *oauthCodeExchangerStub, now time.Time) (*OAuthService, *secret.Cipher) {
	t.Helper()
	cipher, err := secret.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	connections, err := New(connectionRepository, cipher, "odin-finance")
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewOAuthService(stateRepository, connections, cipher, exchanger, testGoogleClientID, testRedirectURL, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return service, cipher
}

func TestOAuthBeginStoresOnlyDigestAndEncryptedPKCEVerifier(t *testing.T) {
	now := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
	states := &oauthStateRepositoryStub{}
	connections := &oauthConnectionRepositoryStub{}
	service, cipher := newOAuthServiceForTest(t, states, connections, &oauthCodeExchangerStub{}, now)
	userID := uuid.New()

	authorizationURL, err := service.Begin(context.Background(), userID)
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	parsed, err := url.Parse(authorizationURL)
	if err != nil {
		t.Fatalf("authorization URL = %q: %v", authorizationURL, err)
	}
	if parsed.Scheme != "https" || parsed.Host != "accounts.google.com" || parsed.Path != "/o/oauth2/v2/auth" {
		t.Fatalf("authorization URL targets %q", authorizationURL)
	}
	query := parsed.Query()
	if got := query.Get("client_id"); got != testGoogleClientID {
		t.Fatalf("client_id = %q", got)
	}
	if got := query.Get("redirect_uri"); got != testRedirectURL {
		t.Fatalf("redirect_uri = %q", got)
	}
	if got := query.Get("response_type"); got != "code" {
		t.Fatalf("response_type = %q", got)
	}
	if got := query.Get("access_type"); got != "offline" {
		t.Fatalf("access_type = %q, want offline for a refresh token", got)
	}
	if got := query.Get("scope"); got != providers.GmailReadonlyScope {
		t.Fatalf("scope = %q", got)
	}
	if got := query.Get("code_challenge_method"); got != "S256" {
		t.Fatalf("code_challenge_method = %q", got)
	}
	stateRaw := query.Get("state")
	if stateRaw == "" {
		t.Fatal("authorization URL omitted state")
	}
	if states.savedUserID != userID || len(states.savedDigest) != sha256.Size || len(states.savedVerifier) == 0 {
		t.Fatalf("saved state is incomplete: %#v", states)
	}
	if string(states.savedVerifier) == stateRaw {
		t.Fatal("raw OAuth value was persisted instead of an encrypted verifier")
	}
	wantDigest := sha256.Sum256([]byte(stateRaw))
	if string(states.savedDigest) != string(wantDigest[:]) {
		t.Fatal("persisted OAuth state digest does not match authorization state")
	}
	verifierBytes, err := cipher.Decrypt(states.savedVerifier, stateAssociatedData(userID, states.savedDigest))
	if err != nil {
		t.Fatalf("stored PKCE verifier cannot be decrypted with its owner: %v", err)
	}
	verifier := string(verifierBytes)
	if len(verifier) < 43 || len(verifier) > 128 {
		t.Fatalf("PKCE verifier length = %d, want 43..128", len(verifier))
	}
	challengeDigest := sha256.Sum256([]byte(verifier))
	wantChallenge := base64.RawURLEncoding.EncodeToString(challengeDigest[:])
	if got := query.Get("code_challenge"); got != wantChallenge {
		t.Fatalf("code_challenge = %q, want S256 challenge %q", got, wantChallenge)
	}
	if !states.savedExpiresAt.After(now) {
		t.Fatalf("state expiry = %s, must be after %s", states.savedExpiresAt, now)
	}
}

func TestOAuthCompleteConsumesStateOnceAndPersistsRefreshToken(t *testing.T) {
	now := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
	states := &oauthStateRepositoryStub{}
	connections := &oauthConnectionRepositoryStub{}
	exchanger := &oauthCodeExchangerStub{result: providers.OAuthCodeExchange{
		RefreshToken: "refresh-token-that-must-never-be-returned",
		Metadata:     json.RawMessage(`{"scope":"https://www.googleapis.com/auth/gmail.readonly"}`),
	}}
	service, _ := newOAuthServiceForTest(t, states, connections, exchanger, now)
	userID := uuid.New()
	authorizationURL, err := service.Begin(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	stateRaw := mustStateFromAuthorizationURL(t, authorizationURL)
	states.state = OAuthState{UserID: userID, EncryptedVerifier: states.savedVerifier}

	if err := service.Complete(context.Background(), stateRaw, "authorization-code"); err != nil {
		t.Fatalf("first Complete() error = %v", err)
	}
	if exchanger.calls != 1 || exchanger.code != "authorization-code" || exchanger.redirect != testRedirectURL || exchanger.verifier == "" {
		t.Fatalf("unexpected code exchange: %#v", exchanger)
	}
	if connections.calls != 1 || string(connections.encrypted) == exchanger.result.RefreshToken || connections.label != "odin-finance" {
		t.Fatalf("refresh token persistence is unsafe: %#v", connections)
	}
	if err := service.Complete(context.Background(), stateRaw, "second-authorization-code"); err == nil {
		t.Fatal("reused OAuth state unexpectedly completed")
	} else if strings.Contains(err.Error(), stateRaw) || strings.Contains(err.Error(), "second-authorization-code") || strings.Contains(err.Error(), exchanger.result.RefreshToken) {
		t.Fatalf("callback error leaked a secret: %v", err)
	}
	if exchanger.calls != 1 || connections.calls != 1 {
		t.Fatalf("reused state performed side effects: exchanges=%d persistences=%d", exchanger.calls, connections.calls)
	}
}

func TestOAuthCompleteExchangeFailureNeverPersistsOrLeaksTokens(t *testing.T) {
	now := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
	states := &oauthStateRepositoryStub{}
	connections := &oauthConnectionRepositoryStub{}
	providerSecret := "provider-token-not-for-clients"
	exchanger := &oauthCodeExchangerStub{err: errors.New("Google exchange rejected " + providerSecret)}
	service, _ := newOAuthServiceForTest(t, states, connections, exchanger, now)
	userID := uuid.New()
	authorizationURL, err := service.Begin(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	stateRaw := mustStateFromAuthorizationURL(t, authorizationURL)
	states.state = OAuthState{UserID: userID, EncryptedVerifier: states.savedVerifier}

	err = service.Complete(context.Background(), stateRaw, "authorization-code-not-for-errors")
	if err == nil {
		t.Fatal("Complete() unexpectedly succeeded after a code exchange error")
	}
	if connections.calls != 0 {
		t.Fatalf("refresh token was persisted after failed exchange: %#v", connections)
	}
	if strings.Contains(err.Error(), providerSecret) || strings.Contains(err.Error(), "authorization-code-not-for-errors") || strings.Contains(err.Error(), stateRaw) {
		t.Fatalf("callback error leaked a token, code, or state: %v", err)
	}
}

func mustStateFromAuthorizationURL(t *testing.T, rawURL string) string {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	state := parsed.Query().Get("state")
	if state == "" {
		t.Fatal("authorization URL did not contain state")
	}
	return state
}

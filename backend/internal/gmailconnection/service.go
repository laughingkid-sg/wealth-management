// Package gmailconnection persists OAuth refresh tokens only after a validated,
// single-use OAuth callback.
package gmailconnection

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/google/uuid"
	"github.com/zhengteck/wealth-builder/backend/internal/providers"
	"github.com/zhengteck/wealth-builder/backend/internal/secret"
)

type Repository interface {
	UpsertGmailConnection(context.Context, uuid.UUID, []byte, json.RawMessage, string) error
}

type Service struct {
	repository Repository
	cipher     *secret.Cipher
	label      string
}

func New(repository Repository, cipher *secret.Cipher, label string) (*Service, error) {
	if repository == nil || cipher == nil || label == "" {
		return nil, errors.New("Gmail connection service is not configured")
	}
	return &Service{repository: repository, cipher: cipher, label: label}, nil
}

// StoreRefreshToken writes a token encrypted with owner-bound associated data.
// It must be called only after a validated, single-use OAuth state callback.
func (s *Service) StoreRefreshToken(ctx context.Context, userID uuid.UUID, refreshToken string, metadata json.RawMessage) error {
	if refreshToken == "" {
		return errors.New("Gmail refresh token is empty")
	}
	if !json.Valid(metadata) {
		return errors.New("Gmail token metadata must be JSON")
	}
	encrypted, err := s.cipher.Encrypt([]byte(refreshToken), associatedData(userID))
	if err != nil {
		return err
	}
	return s.repository.UpsertGmailConnection(ctx, userID, encrypted, metadata, s.label)
}

func associatedData(userID uuid.UUID) []byte { return []byte("gmail-refresh-token:" + userID.String()) }

type OAuthState struct {
	UserID            uuid.UUID
	EncryptedVerifier []byte
}

type OAuthRepository interface {
	SaveOAuthState(context.Context, uuid.UUID, []byte, []byte, time.Time) error
	ConsumeOAuthState(context.Context, []byte, time.Time) (OAuthState, error)
}

type OAuthCodeExchanger interface {
	ExchangeAuthorizationCode(context.Context, string, string, string) (providers.OAuthCodeExchange, error)
}

type OAuthService struct {
	states      OAuthRepository
	connections *Service
	exchanger   OAuthCodeExchanger
	cipher      *secret.Cipher
	clientID    string
	redirectURL string
	stateTTL    time.Duration
	now         func() time.Time
}

func NewOAuthService(states OAuthRepository, connections *Service, cipher *secret.Cipher, exchanger OAuthCodeExchanger, clientID, redirectURL string, now func() time.Time) (*OAuthService, error) {
	parsed, err := url.Parse(redirectURL)
	if states == nil || connections == nil || exchanger == nil || cipher == nil || clientID == "" || err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("Gmail OAuth service is not configured")
	}
	if now == nil {
		now = time.Now
	}
	return &OAuthService{states: states, connections: connections, exchanger: exchanger, cipher: cipher, clientID: clientID, redirectURL: parsed.String(), stateTTL: 10 * time.Minute, now: now}, nil
}

// Begin persists only a state digest and encrypted PKCE verifier, then returns a Google URL.
func (s *OAuthService) Begin(ctx context.Context, userID uuid.UUID) (string, error) {
	stateBytes, err := randomBytes(32)
	if err != nil {
		return "", err
	}
	verifierBytes, err := randomBytes(32)
	if err != nil {
		return "", err
	}
	stateRaw := base64.RawURLEncoding.EncodeToString(stateBytes)
	verifier := base64.RawURLEncoding.EncodeToString(verifierBytes)
	digest := sha256.Sum256([]byte(stateRaw))
	challengeHash := sha256.Sum256([]byte(verifier))
	encryptedVerifier, err := s.cipher.Encrypt([]byte(verifier), stateAssociatedData(userID, digest[:]))
	if err != nil {
		return "", err
	}
	if err := s.states.SaveOAuthState(ctx, userID, digest[:], encryptedVerifier, s.now().Add(s.stateTTL)); err != nil {
		return "", err
	}
	query := url.Values{
		"client_id": {s.clientID}, "redirect_uri": {s.redirectURL}, "response_type": {"code"},
		"scope": {providers.GmailReadonlyScope}, "state": {stateRaw},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(challengeHash[:])}, "code_challenge_method": {"S256"},
		"access_type": {"offline"}, "prompt": {"consent"},
	}
	return "https://accounts.google.com/o/oauth2/v2/auth?" + query.Encode(), nil
}

// Complete atomically consumes a state before exchanging its authorization code.
func (s *OAuthService) Complete(ctx context.Context, stateRaw, code string) error {
	if stateRaw == "" || code == "" {
		return errors.New("Google OAuth state and code are required")
	}
	digest := sha256.Sum256([]byte(stateRaw))
	state, err := s.states.ConsumeOAuthState(ctx, digest[:], s.now())
	if err != nil {
		return fmt.Errorf("consume Gmail OAuth state: %w", err)
	}
	verifier, err := s.cipher.Decrypt(state.EncryptedVerifier, stateAssociatedData(state.UserID, digest[:]))
	if err != nil {
		return errors.New("stored Gmail OAuth verifier cannot be decrypted")
	}
	token, err := s.exchanger.ExchangeAuthorizationCode(ctx, code, s.redirectURL, string(verifier))
	if err != nil {
		return errors.New("Google authorization code exchange failed")
	}
	if err := s.connections.StoreRefreshToken(ctx, state.UserID, token.RefreshToken, token.Metadata); err != nil {
		return errors.New("Gmail connection could not be saved")
	}
	return nil
}

func stateAssociatedData(userID uuid.UUID, digest []byte) []byte {
	return []byte("gmail-oauth-state:" + userID.String() + ":" + hex.EncodeToString(digest))
}

func randomBytes(length int) ([]byte, error) {
	value := make([]byte, length)
	_, err := rand.Read(value)
	return value, err
}

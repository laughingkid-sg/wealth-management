// Package config loads only server-side configuration for the transactions API and worker.
package config

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAlibabaBaseURL = "https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1"
	defaultAlibabaModel   = "qwen3.8-flash"
	defaultGmailLabel     = "odin-finance"
)

// Config contains server-only configuration. Do not marshal or log it.
type Config struct {
	Environment string
	APIAddress  string

	SupabaseURL            *url.URL
	SupabaseDBURL          *url.URL
	SupabaseServiceRoleKey string

	GoogleOAuthClientID     string
	GoogleOAuthClientSecret string
	GoogleOAuthRedirectURL  *url.URL
	TokenEncryptionKey      []byte
	GoogleTestRefreshToken  string

	AlibabaTokenPlanAPIKey string
	AlibabaBaseURL         *url.URL
	AlibabaModel           string
	FrontendOrigin         *url.URL
	GmailSyncLabel         string
	InitialBackfillMax     int
	WorkerPollInterval     time.Duration
}

// LoadFromEnv validates the complete runtime contract before either process starts.
func LoadFromEnv() (Config, error) {
	getRequired := func(name string) (string, error) {
		value := strings.TrimSpace(os.Getenv(name))
		if value == "" {
			return "", fmt.Errorf("%s is required", name)
		}
		return value, nil
	}
	parseURL := func(name, raw string) (*url.URL, error) {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return nil, fmt.Errorf("%s must be an absolute URL", name)
		}
		return parsed, nil
	}

	var cfg Config
	cfg.Environment = strings.TrimSpace(os.Getenv("APP_ENV"))
	if cfg.Environment == "" {
		cfg.Environment = "development"
	}
	cfg.APIAddress = strings.TrimSpace(os.Getenv("API_ADDRESS"))
	if cfg.APIAddress == "" {
		cfg.APIAddress = ":8080"
	}

	var err error
	if raw, err := getRequired("SUPABASE_URL"); err != nil {
		return Config{}, err
	} else if cfg.SupabaseURL, err = parseURL("SUPABASE_URL", raw); err != nil {
		return Config{}, err
	}
	if cfg.SupabaseURL.Scheme != "https" && cfg.Environment != "development" {
		return Config{}, fmt.Errorf("SUPABASE_URL must use https outside development")
	}

	if raw, err := getRequired("SUPABASE_DB_URL"); err != nil {
		return Config{}, err
	} else if cfg.SupabaseDBURL, err = validateTransactionPoolerURL(raw); err != nil {
		return Config{}, err
	}
	if cfg.SupabaseServiceRoleKey, err = getRequired("SUPABASE_SERVICE_ROLE_KEY"); err != nil {
		return Config{}, err
	}

	if cfg.GoogleOAuthClientID, err = getRequired("GOOGLE_OAUTH_CLIENT_ID"); err != nil {
		return Config{}, err
	}
	if cfg.GoogleOAuthClientSecret, err = getRequired("GOOGLE_OAUTH_CLIENT_SECRET"); err != nil {
		return Config{}, err
	}
	if raw, err := getRequired("GOOGLE_OAUTH_REDIRECT_URL"); err != nil {
		return Config{}, err
	} else if cfg.GoogleOAuthRedirectURL, err = parseURL("GOOGLE_OAUTH_REDIRECT_URL", raw); err != nil {
		return Config{}, err
	}
	if cfg.GoogleOAuthRedirectURL.Scheme != "https" && !(cfg.Environment == "development" && cfg.GoogleOAuthRedirectURL.Hostname() == "localhost") {
		return Config{}, fmt.Errorf("GOOGLE_OAUTH_REDIRECT_URL must use https outside localhost development")
	}

	key, err := getRequired("TRANSACTION_TOKEN_ENCRYPTION_KEY")
	if err != nil {
		return Config{}, err
	}
	if cfg.TokenEncryptionKey, err = base64.StdEncoding.DecodeString(key); err != nil || len(cfg.TokenEncryptionKey) != 32 {
		return Config{}, fmt.Errorf("TRANSACTION_TOKEN_ENCRYPTION_KEY must be base64-encoded 32 bytes")
	}
	cfg.GoogleTestRefreshToken = strings.TrimSpace(os.Getenv("GOOGLE_TEST_REFRESH_TOKEN"))
	if cfg.GoogleTestRefreshToken != "" && cfg.Environment != "development" {
		return Config{}, fmt.Errorf("GOOGLE_TEST_REFRESH_TOKEN is allowed only in development")
	}

	if cfg.AlibabaTokenPlanAPIKey, err = getRequired("ALIBABA_TOKEN_PLAN_API_KEY"); err != nil {
		return Config{}, err
	}
	baseURL := environmentOr("ALIBABA_TOKEN_PLAN_BASE_URL", defaultAlibabaBaseURL)
	if cfg.AlibabaBaseURL, err = parseURL("ALIBABA_TOKEN_PLAN_BASE_URL", baseURL); err != nil {
		return Config{}, err
	}
	if cfg.AlibabaBaseURL.Scheme != "https" {
		return Config{}, fmt.Errorf("ALIBABA_TOKEN_PLAN_BASE_URL must use https")
	}
	cfg.AlibabaModel = environmentOr("ALIBABA_TOKEN_PLAN_MODEL", defaultAlibabaModel)
	if cfg.AlibabaModel != defaultAlibabaModel {
		return Config{}, fmt.Errorf("ALIBABA_TOKEN_PLAN_MODEL must be %q", defaultAlibabaModel)
	}
	if raw, err := getRequired("FRONTEND_ORIGIN"); err != nil {
		return Config{}, err
	} else if cfg.FrontendOrigin, err = parseURL("FRONTEND_ORIGIN", raw); err != nil {
		return Config{}, err
	}
	if cfg.FrontendOrigin.Scheme != "https" && !(cfg.Environment == "development" && cfg.FrontendOrigin.Hostname() == "localhost") {
		return Config{}, fmt.Errorf("FRONTEND_ORIGIN must use https outside localhost development")
	}
	cfg.GmailSyncLabel = environmentOr("GMAIL_SYNC_LABEL", defaultGmailLabel)
	if cfg.GmailSyncLabel != defaultGmailLabel {
		return Config{}, fmt.Errorf("GMAIL_SYNC_LABEL must be %q", defaultGmailLabel)
	}
	if cfg.InitialBackfillMax, err = requiredPositiveInt("GMAIL_INITIAL_BACKFILL_MAX_MESSAGES"); err != nil {
		return Config{}, err
	}
	if cfg.InitialBackfillMax != 5 {
		return Config{}, fmt.Errorf("GMAIL_INITIAL_BACKFILL_MAX_MESSAGES must be 5")
	}
	pollSeconds, err := optionalPositiveInt("WORKER_POLL_SECONDS", 5)
	if err != nil {
		return Config{}, err
	}
	cfg.WorkerPollInterval = time.Duration(pollSeconds) * time.Second
	return cfg, nil
}

func validateTransactionPoolerURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Host == "" || parsed.User == nil {
		return nil, fmt.Errorf("SUPABASE_DB_URL must be an absolute Postgres connection URL")
	}
	host := strings.ToLower(parsed.Hostname())
	if !strings.HasSuffix(host, ".pooler.supabase.com") {
		return nil, fmt.Errorf("SUPABASE_DB_URL must use the Supabase transaction-pooler host")
	}
	if parsed.Port() != "6543" {
		return nil, fmt.Errorf("SUPABASE_DB_URL must use transaction-pooler port 6543")
	}
	if parsed.Query().Get("sslmode") != "require" {
		return nil, fmt.Errorf("SUPABASE_DB_URL must set sslmode=require")
	}
	return parsed, nil
}

func environmentOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func requiredPositiveInt(name string) (int, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return 0, fmt.Errorf("%s is required", name)
	}
	return parsePositiveInt(name, value)
}

func optionalPositiveInt(name string, fallback int) (int, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	return parsePositiveInt(name, value)
}

func parsePositiveInt(name, value string) (int, error) {
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return parsed, nil
}

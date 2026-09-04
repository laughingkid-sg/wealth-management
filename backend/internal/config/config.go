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

	AlibabaTokenPlanAPIKey        string
	AlibabaBaseURL                *url.URL
	AlibabaModel                  string
	FrontendOrigin                *url.URL
	GmailSyncLabel                string
	InitialBackfillMax            int
	WorkerPollInterval            time.Duration
	OutboundHTTPTimeout           time.Duration
	BulkImportEnabled             bool
	BulkImportRenderTimeout       time.Duration
	BulkImportProviderTimeout     time.Duration
	BulkImportMaxRenderedPage     int
	BulkImportMaxRenderedDocument int
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
	httpTimeoutSeconds, err := optionalPositiveInt("OUTBOUND_HTTP_TIMEOUT_SECONDS", 20)
	if err != nil {
		return Config{}, err
	}
	if httpTimeoutSeconds > 120 {
		return Config{}, fmt.Errorf("OUTBOUND_HTTP_TIMEOUT_SECONDS must be at most 120")
	}
	cfg.OutboundHTTPTimeout = time.Duration(httpTimeoutSeconds) * time.Second
	if cfg.BulkImportEnabled, err = optionalBool("BULK_IMPORT_ENABLED", false); err != nil {
		return Config{}, err
	}
	bulkRenderSeconds, err := optionalPositiveInt("BULK_IMPORT_RENDER_TIMEOUT_SECONDS", 30)
	if err != nil || bulkRenderSeconds > 60 {
		return Config{}, fmt.Errorf("BULK_IMPORT_RENDER_TIMEOUT_SECONDS must be between 1 and 60")
	}
	cfg.BulkImportRenderTimeout = time.Duration(bulkRenderSeconds) * time.Second
	bulkProviderSeconds, err := optionalPositiveInt("BULK_IMPORT_PROVIDER_TIMEOUT_SECONDS", 30)
	if err != nil || bulkProviderSeconds > 30 {
		return Config{}, fmt.Errorf("BULK_IMPORT_PROVIDER_TIMEOUT_SECONDS must be between 1 and 30")
	}
	cfg.BulkImportProviderTimeout = time.Duration(bulkProviderSeconds) * time.Second
	if cfg.BulkImportMaxRenderedPage, err = optionalPositiveInt("BULK_IMPORT_MAX_RENDERED_PAGE_BYTES", 1024*1024); err != nil || cfg.BulkImportMaxRenderedPage > 1024*1024 {
		return Config{}, fmt.Errorf("BULK_IMPORT_MAX_RENDERED_PAGE_BYTES must be between 1 and 1048576")
	}
	if cfg.BulkImportMaxRenderedDocument, err = optionalPositiveInt("BULK_IMPORT_MAX_RENDERED_DOCUMENT_BYTES", 50*1024*1024); err != nil || cfg.BulkImportMaxRenderedDocument > 50*1024*1024 || cfg.BulkImportMaxRenderedDocument < cfg.BulkImportMaxRenderedPage {
		return Config{}, fmt.Errorf("BULK_IMPORT_MAX_RENDERED_DOCUMENT_BYTES must be between the page limit and 52428800")
	}
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
	query := parsed.Query()
	sslModes, hasSSLMode := query["sslmode"]
	if !hasSSLMode {
		query.Set("sslmode", "require")
		parsed.RawQuery = query.Encode()
	} else if len(sslModes) != 1 || sslModes[0] != "require" {
		return nil, fmt.Errorf("SUPABASE_DB_URL sslmode must be require when explicitly set")
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

func optionalBool(name string, fallback bool) (bool, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", name)
	}
	return parsed, nil
}

func parsePositiveInt(name, value string) (int, error) {
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return parsed, nil
}

package config

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestLoadFromEnvAcceptsTransactionPoolerAndDevelopmentToken(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("GOOGLE_TEST_REFRESH_TOKEN", "test-only-token")
	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv() error = %v", err)
	}
	if cfg.InitialBackfillMax != 5 || cfg.GmailSyncLabel != "odin-finance" {
		t.Fatalf("unexpected fixed Gmail configuration: %#v", cfg)
	}
	if cfg.GoogleTestRefreshToken != "test-only-token" {
		t.Fatal("development refresh token was not loaded")
	}
	if cfg.BulkImportProviderTimeout.Seconds() != 30 || cfg.BulkImportRenderTimeout.Seconds() != 30 {
		t.Fatalf("unexpected Bulk Import limits: %#v", cfg)
	}
}

func TestLoadFromEnvRejectsDirectDatabaseHost(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("SUPABASE_DB_URL", "postgresql://postgres:password@db.example.supabase.co:5432/postgres?sslmode=require")
	_, err := LoadFromEnv()
	if err == nil || !strings.Contains(err.Error(), "transaction-pooler") {
		t.Fatalf("LoadFromEnv() error = %v, want transaction-pooler validation", err)
	}
}

func TestLoadFromEnvRejectsTestTokenOutsideDevelopment(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("GOOGLE_OAUTH_REDIRECT_URL", "https://api.example.com/v1/transactions/gmail/oauth/callback")
	t.Setenv("FRONTEND_ORIGIN", "https://app.example.com")
	t.Setenv("GOOGLE_TEST_REFRESH_TOKEN", "test-only-token")
	_, err := LoadFromEnv()
	if err == nil || !strings.Contains(err.Error(), "only in development") {
		t.Fatalf("LoadFromEnv() error = %v, want development-only validation", err)
	}
}

func TestLoadFromEnvRejectsIncorrectInitialBackfill(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("GMAIL_INITIAL_BACKFILL_MAX_MESSAGES", "6")
	_, err := LoadFromEnv()
	if err == nil || !strings.Contains(err.Error(), "must be 5") {
		t.Fatalf("LoadFromEnv() error = %v, want five-message validation", err)
	}
}

func TestLoadFromEnvAddsRequiredSSLModeWhenMissing(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("SUPABASE_DB_URL", "postgresql://postgres.project:password@aws-0-ap-southeast-1.pooler.supabase.com:6543/postgres")
	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.SupabaseDBURL.Query().Get("sslmode"); got != "require" {
		t.Fatalf("sslmode = %q, want require", got)
	}
}

func TestLoadFromEnvRejectsWeakerExplicitSSLMode(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("SUPABASE_DB_URL", "postgresql://postgres.project:password@aws-0-ap-southeast-1.pooler.supabase.com:6543/postgres?sslmode=disable")
	if _, err := LoadFromEnv(); err == nil || !strings.Contains(err.Error(), "sslmode") {
		t.Fatalf("LoadFromEnv() error = %v, want sslmode rejection", err)
	}
}

func setValidEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("SUPABASE_URL", "https://project.supabase.co")
	t.Setenv("SUPABASE_DB_URL", "postgresql://postgres.project:password@aws-0-ap-southeast-1.pooler.supabase.com:6543/postgres?sslmode=require")
	t.Setenv("SUPABASE_SERVICE_ROLE_KEY", "server-only")
	t.Setenv("GOOGLE_OAUTH_CLIENT_ID", "client-id")
	t.Setenv("GOOGLE_OAUTH_CLIENT_SECRET", "client-secret")
	t.Setenv("GOOGLE_OAUTH_REDIRECT_URL", "http://localhost:8080/v1/transactions/gmail/oauth/callback")
	t.Setenv("TRANSACTION_TOKEN_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("ALIBABA_TOKEN_PLAN_API_KEY", "server-only")
	t.Setenv("FRONTEND_ORIGIN", "http://localhost:5173")
	t.Setenv("GMAIL_INITIAL_BACKFILL_MAX_MESSAGES", "5")
	for _, key := range []string{"GOOGLE_TEST_REFRESH_TOKEN", "ALIBABA_TOKEN_PLAN_BASE_URL", "ALIBABA_TOKEN_PLAN_MODEL", "GMAIL_SYNC_LABEL", "WORKER_POLL_SECONDS", "OUTBOUND_HTTP_TIMEOUT_SECONDS", "BULK_IMPORT_ENABLED", "BULK_IMPORT_RENDER_TIMEOUT_SECONDS", "BULK_IMPORT_PROVIDER_TIMEOUT_SECONDS", "BULK_IMPORT_MAX_RENDERED_PAGE_BYTES", "BULK_IMPORT_MAX_RENDERED_DOCUMENT_BYTES"} {
		t.Setenv(key, "")
	}
}

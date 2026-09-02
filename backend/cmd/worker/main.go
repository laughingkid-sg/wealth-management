package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/zhengteck/wealth-builder/backend/internal/config"
	"github.com/zhengteck/wealth-builder/backend/internal/database"
	"github.com/zhengteck/wealth-builder/backend/internal/ingestion"
	"github.com/zhengteck/wealth-builder/backend/internal/jobs"
	"github.com/zhengteck/wealth-builder/backend/internal/providers"
	"github.com/zhengteck/wealth-builder/backend/internal/secret"
	"github.com/zhengteck/wealth-builder/backend/internal/transactionstore"
)

func main() {
	cfg, err := config.LoadFromEnv()
	if err != nil {
		log.Fatalf("invalid configuration: %v", err)
	}
	pool, err := database.OpenTransactionPooler(context.Background(), cfg.SupabaseDBURL.String())
	if err != nil {
		log.Fatalf("open transaction database: %v", err)
	}
	defer pool.Close()
	cipher, err := secret.New(cfg.TokenEncryptionKey)
	if err != nil {
		log.Fatalf("configure token cipher: %v", err)
	}
	oauthClient, err := providers.NewGoogleOAuthClient(http.DefaultClient, cfg.GoogleOAuthClientID, cfg.GoogleOAuthClientSecret)
	if err != nil {
		log.Fatalf("configure Google OAuth client: %v", err)
	}
	gmailClient, err := providers.NewGmailHTTPClient(http.DefaultClient)
	if err != nil {
		log.Fatalf("configure Gmail client: %v", err)
	}

	store := transactionstore.New(pool)
	handler := ingestion.GmailIngestionHandler{
		Repository: store, Gmail: gmailClient, Tokens: oauthClient, Cipher: cipher,
		Label: cfg.GmailSyncLabel, InitialBackfillMax: cfg.InitialBackfillMax,
		DevelopmentRefreshToken: developmentRefreshToken(cfg),
	}
	worker := jobs.Worker{Store: store, WorkerID: workerID(), Handler: handler}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	for {
		processed, err := worker.ProcessOne(ctx)
		if err != nil {
			log.Printf("transaction job failed safely: %v", err)
		}
		if processed {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(cfg.WorkerPollInterval):
		}
	}
}

func developmentRefreshToken(cfg config.Config) string {
	if cfg.Environment != "development" {
		return ""
	}
	return cfg.GoogleTestRefreshToken
}

func workerID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "worker"
	}
	return host + "-" + uuid.NewString()
}

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
	"github.com/zhengteck/wealth-builder/backend/internal/attachmentstorage"
	"github.com/zhengteck/wealth-builder/backend/internal/config"
	"github.com/zhengteck/wealth-builder/backend/internal/database"
	"github.com/zhengteck/wealth-builder/backend/internal/ingestion"
	"github.com/zhengteck/wealth-builder/backend/internal/jobs"
	"github.com/zhengteck/wealth-builder/backend/internal/providers"
	"github.com/zhengteck/wealth-builder/backend/internal/secret"
	"github.com/zhengteck/wealth-builder/backend/internal/transactionstore"
	"github.com/zhengteck/wealth-builder/backend/internal/transactionworker"
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
	providerHTTPClient := &http.Client{Timeout: cfg.OutboundHTTPTimeout}
	storageHTTPClient := &http.Client{Timeout: cfg.OutboundHTTPTimeout}
	oauthClient, err := providers.NewGoogleOAuthClient(providerHTTPClient, cfg.GoogleOAuthClientID, cfg.GoogleOAuthClientSecret)
	if err != nil {
		log.Fatalf("configure Google OAuth client: %v", err)
	}
	gmailClient, err := providers.NewGmailHTTPClient(providerHTTPClient)
	if err != nil {
		log.Fatalf("configure Gmail client: %v", err)
	}
	attachmentClient, err := attachmentstorage.New(storageHTTPClient, cfg.SupabaseURL, cfg.SupabaseServiceRoleKey)
	if err != nil {
		log.Fatalf("configure transaction attachment storage: %v", err)
	}
	qwenClient, err := providers.NewAlibabaQwenClient(providerHTTPClient, cfg.AlibabaBaseURL, cfg.AlibabaTokenPlanAPIKey, cfg.AlibabaModel)
	if err != nil {
		log.Fatalf("configure Alibaba parser: %v", err)
	}

	store := transactionstore.New(pool)
	gmailHandler := ingestion.GmailIngestionHandler{
		Repository: store, Gmail: gmailClient, Tokens: oauthClient, Cipher: cipher, Attachments: attachmentClient,
		Label: cfg.GmailSyncLabel, InitialBackfillMax: cfg.InitialBackfillMax,
		DevelopmentRefreshToken: developmentRefreshToken(cfg),
	}
	processingHandler := transactionworker.Handler{Repository: store, Parser: qwenClient, Attachments: attachmentClient}
	handler := jobs.Router{
		jobs.KindGmailIngest: gmailHandler,
		jobs.KindSourceParse: processingHandler,
		jobs.KindReconcile:   processingHandler,
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

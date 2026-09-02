package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/zhengteck/wealth-builder/backend/internal/attachmentstorage"
	"github.com/zhengteck/wealth-builder/backend/internal/auth"
	"github.com/zhengteck/wealth-builder/backend/internal/config"
	"github.com/zhengteck/wealth-builder/backend/internal/database"
	"github.com/zhengteck/wealth-builder/backend/internal/gmailconnection"
	"github.com/zhengteck/wealth-builder/backend/internal/providers"
	"github.com/zhengteck/wealth-builder/backend/internal/secret"
	"github.com/zhengteck/wealth-builder/backend/internal/transactions"
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

	outboundHTTPClient := &http.Client{Timeout: cfg.OutboundHTTPTimeout}
	verifier := auth.NewSupabaseUserVerifier(cfg.SupabaseURL, cfg.SupabaseServiceRoleKey, outboundHTTPClient)
	store := transactionstore.New(pool)
	attachmentClient, err := attachmentstorage.New(outboundHTTPClient, cfg.SupabaseURL, cfg.SupabaseServiceRoleKey)
	if err != nil {
		log.Fatalf("configure transaction attachment storage: %v", err)
	}
	cipher, err := secret.New(cfg.TokenEncryptionKey)
	if err != nil {
		log.Fatalf("configure token cipher: %v", err)
	}
	connections, err := gmailconnection.New(store, cipher, cfg.GmailSyncLabel)
	if err != nil {
		log.Fatalf("configure Gmail connection persistence: %v", err)
	}
	oauthClient, err := providers.NewGoogleOAuthClient(outboundHTTPClient, cfg.GoogleOAuthClientID, cfg.GoogleOAuthClientSecret)
	if err != nil {
		log.Fatalf("configure Google OAuth client: %v", err)
	}
	oauthFlow, err := gmailconnection.NewOAuthService(store, connections, cipher, oauthClient, cfg.GoogleOAuthClientID, cfg.GoogleOAuthRedirectURL.String(), time.Now)
	if err != nil {
		log.Fatalf("configure Gmail OAuth flow: %v", err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /healthz", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	transactions.NewHandler(store, cfg.Environment == "development" && cfg.GoogleTestRefreshToken != "", oauthFlow, cfg.FrontendOrigin, attachmentClient).Register(mux, verifier)
	server := &http.Server{Addr: cfg.APIAddress, Handler: securityHeaders(mux), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()
	log.Printf("transactions API listening on %s", cfg.APIAddress)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

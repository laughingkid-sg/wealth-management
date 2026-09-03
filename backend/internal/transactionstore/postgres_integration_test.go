package transactionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/zhengteck/wealth-builder/backend/internal/database"
	"github.com/zhengteck/wealth-builder/backend/internal/jobs"
	"github.com/zhengteck/wealth-builder/backend/internal/reconciliation"
)

// TestCreateSyncRunPersistsJSONWithProductionQueryMode guards the transaction
// pooler's simple-protocol encoding boundary. JSON arguments must be passed as
// text; []byte is rendered as a bytea literal and PostgreSQL rejects it as JSON.
func TestCreateSyncRunPersistsJSONWithProductionQueryMode(t *testing.T) {
	databaseURL := os.Getenv("TRANSACTIONSTORE_TEST_DB_URL")
	if databaseURL == "" {
		t.Skip("TRANSACTIONSTORE_TEST_DB_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := database.OpenTransactionPooler(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	userID := uuid.New()
	if _, err = pool.Exec(ctx, `insert into auth.users (id, email) values ($1, $2)`, userID, "create-sync-"+userID.String()+"@example.test"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupContext, `delete from auth.users where id = $1`, userID)
	}()

	store := New(pool)
	if _, err = store.CreateSyncRun(ctx, userID, false); !errors.Is(err, ErrGmailConnectionRequired) {
		t.Fatalf("CreateSyncRun() without connection error = %v", err)
	}
	var runCount int
	if err = pool.QueryRow(ctx, `select count(*) from public.transaction_sync_runs where user_id = $1`, userID).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	if runCount != 0 {
		t.Fatalf("failed CreateSyncRun() persisted %d runs, want 0", runCount)
	}

	run, err := store.CreateSyncRun(ctx, userID, true)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "queued" || run.StartedAt != nil || run.CompletedAt != nil || run.IngestionCompletedAt != nil ||
		run.MessagesFoundCount != 0 || run.SourcesSavedCount != 0 || run.SourcesParsedCount != 0 || run.SourcesFailedCount != 0 {
		t.Fatalf("new sync run has unexpected initial state: %#v", run)
	}
	var connectionID *uuid.UUID
	var payload []byte
	if err = pool.QueryRow(ctx, `
		select run.gmail_connection_id, job.payload
		from public.transaction_sync_runs run
		join private.transaction_jobs job
			on job.user_id = run.user_id and job.sync_run_id = run.id
		where run.user_id = $1 and run.id = $2 and job.job_type = 'gmail_ingestion'`, userID, run.ID).Scan(&connectionID, &payload); err != nil {
		t.Fatal(err)
	}
	if connectionID != nil {
		t.Fatal("development-token sync unexpectedly persisted a Gmail connection")
	}
	var decoded struct {
		SyncRunID string `json:"sync_run_id"`
	}
	if err = json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode queued job payload: %v", err)
	}
	if decoded.SyncRunID != run.ID.String() {
		t.Fatal("queued job payload does not reference its sync run")
	}
	if _, err = store.CreateSyncRun(ctx, userID, true); !errors.Is(err, ErrSyncRunInProgress) {
		t.Fatalf("second CreateSyncRun() error = %v", err)
	}
}

func TestStageSourceDeletionQueuesCleanupPreventsReingestAndRemovesSuccessfulOutbox(t *testing.T) {
	databaseURL := os.Getenv("TRANSACTIONSTORE_TEST_DB_URL")
	if databaseURL == "" {
		t.Skip("TRANSACTIONSTORE_TEST_DB_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := database.OpenTransactionPooler(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	userID, accountID, sourceID, transactionID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	providerMessageID := "permanently-deleted-" + sourceID.String()
	objectPath := userID.String() + "/" + sourceID.String() + "/receipt.pdf"
	if _, err = pool.Exec(ctx, `insert into auth.users (id, email) values ($1, $2)`, userID, "delete-"+userID.String()+"@example.test"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupContext, `delete from auth.users where id = $1`, userID)
	}()
	if _, err = pool.Exec(ctx, `
		insert into public.accounts (id, user_id, side, account_type, name, institution_name)
		values ($1, $2, 'asset', 'bank_account', 'Current', 'Bank')`, accountID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `
		insert into public.transactions (
			id, user_id, account_id, transaction_kind, title,
			original_amount_minor, original_currency, occurred_at, creation_method
		) values ($1, $2, $3, 'debit', 'Automatic receipt', 100, 'SGD', now(), 'automatic_source')`,
		transactionID, userID, accountID); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"attachments": []map[string]any{{
		"object_path": objectPath, "storage_status": "stored", "filename": "receipt.pdf",
	}}})
	if _, err = pool.Exec(ctx, `
		insert into private.data_sources (
			id, user_id, source_type, provider, provider_message_id, received_at, raw_data, parse_status
		) values ($1, $2, 'gmail_email', 'gmail', $3, now(), $4::jsonb, 'parsed')`,
		sourceID, userID, providerMessageID, string(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `
		insert into private.transaction_data_sources
			(user_id, transaction_id, data_source_id, role, matched_by)
		values ($1, $2, $3, 'merchant_receipt', 'automatic')`, userID, transactionID, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `
		insert into private.source_parse_attempts
			(user_id, data_source_id, validation_status)
		values ($1, $2, 'valid')`, userID, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `
		insert into private.transaction_jobs (user_id, data_source_id, job_type)
		values ($1, $2, 'reconciliation')`, userID, sourceID); err != nil {
		t.Fatal(err)
	}

	store := New(pool)
	result, err := store.StageSourceDeletion(ctx, userID, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "cleanup_pending" || !result.CleanupPending {
		t.Fatalf("deletion result = %#v", result)
	}
	for name, query := range map[string]string{
		"source":      `select count(*) from private.data_sources where user_id = $1 and id = $2`,
		"parse audit": `select count(*) from private.source_parse_attempts where user_id = $1 and data_source_id = $2`,
		"source link": `select count(*) from private.transaction_data_sources where user_id = $1 and data_source_id = $2`,
		"transaction": `select count(*) from public.transactions where user_id = $1 and id = $2`,
	} {
		var count int
		identifier := sourceID
		if name == "transaction" {
			identifier = transactionID
		}
		if err = pool.QueryRow(ctx, query, userID, identifier).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s count = %d, want 0", name, count)
		}
	}
	var cleanupJobID uuid.UUID
	var cleanupPayload []byte
	if err = pool.QueryRow(ctx, `
		select id, payload from private.transaction_jobs
		where user_id = $1 and job_type = 'source_attachment_cleanup'`, userID).Scan(&cleanupJobID, &cleanupPayload); err != nil {
		t.Fatal(err)
	}
	var payload jobs.SourceAttachmentCleanupPayload
	if err = json.Unmarshal(cleanupPayload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.SourceID != sourceID.String() || len(payload.ObjectPaths) != 1 || payload.ObjectPaths[0] != objectPath {
		t.Fatalf("cleanup payload = %#v", payload)
	}
	_, _, err = store.StoreIngestedSource(ctx, IngestedSource{
		UserID: userID, SyncRunID: uuid.New(), ProviderMessageID: providerMessageID,
		ReceivedAt: time.Now(), RawData: json.RawMessage(`{}`),
	})
	if !errors.Is(err, ErrSourcePermanentlyDeleted) {
		t.Fatalf("reingest error = %v", err)
	}
	if _, err = pool.Exec(ctx, `
		update private.transaction_jobs
		set status = 'running', attempts = 1, leased_at = now(),
			lease_expires_at = now() + interval '5 minutes', leased_by = 'cleanup-test'
		where id = $1`, cleanupJobID); err != nil {
		t.Fatal(err)
	}
	if err = store.Retry(ctx, cleanupJobID, "cleanup-test", 1, time.Now().Add(time.Second), "storage unavailable"); err != nil {
		t.Fatal(err)
	}
	var retryStatus string
	var retainedPayload []byte
	if err = pool.QueryRow(ctx, `
		select status, payload from private.transaction_jobs where id = $1`, cleanupJobID).Scan(&retryStatus, &retainedPayload); err != nil {
		t.Fatal(err)
	}
	if retryStatus != "queued" || string(retainedPayload) != string(cleanupPayload) {
		t.Fatalf("retry status=%q payload retained=%t", retryStatus, string(retainedPayload) == string(cleanupPayload))
	}
	if _, err = pool.Exec(ctx, `
		update private.transaction_jobs
		set status = 'running', attempts = 2, leased_at = now(),
			lease_expires_at = now() + interval '5 minutes', leased_by = 'cleanup-test'
		where id = $1`, cleanupJobID); err != nil {
		t.Fatal(err)
	}
	if err = store.Complete(ctx, cleanupJobID, "cleanup-test"); err != nil {
		t.Fatal(err)
	}
	var cleanupRows int
	if err = pool.QueryRow(ctx, `select count(*) from private.transaction_jobs where id = $1`, cleanupJobID).Scan(&cleanupRows); err != nil {
		t.Fatal(err)
	}
	if cleanupRows != 0 {
		t.Fatalf("successful cleanup retained %d outbox rows", cleanupRows)
	}
}

func TestStageSourceDeletionConflictsWithActiveGmailIngestion(t *testing.T) {
	databaseURL := os.Getenv("TRANSACTIONSTORE_TEST_DB_URL")
	if databaseURL == "" {
		t.Skip("TRANSACTIONSTORE_TEST_DB_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := database.OpenTransactionPooler(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	userID, sourceID := uuid.New(), uuid.New()
	if _, err = pool.Exec(ctx, `insert into auth.users (id, email) values ($1, $2)`, userID, "delete-conflict-"+userID.String()+"@example.test"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupContext, `delete from auth.users where id = $1`, userID)
	}()
	if _, err = pool.Exec(ctx, `
		insert into private.data_sources
			(id, user_id, source_type, provider, provider_message_id, received_at, raw_data)
		values ($1, $2, 'gmail_email', 'gmail', $3, now(), '{}')`, sourceID, userID, "active-"+sourceID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `
		insert into private.transaction_jobs (user_id, job_type)
		values ($1, 'gmail_ingestion')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err = New(pool).StageSourceDeletion(ctx, userID, sourceID); !errors.Is(err, ErrSourceDeletionIngestionActive) {
		t.Fatalf("StageSourceDeletion() error = %v", err)
	}
	var sourceRows int
	if err = pool.QueryRow(ctx, `select count(*) from private.data_sources where user_id = $1 and id = $2`, userID, sourceID).Scan(&sourceRows); err != nil {
		t.Fatal(err)
	}
	if sourceRows != 1 {
		t.Fatalf("conflicting deletion changed source count to %d", sourceRows)
	}
}

func TestSourceParseDebugCapsAttemptsAndLargeFields(t *testing.T) {
	databaseURL := os.Getenv("TRANSACTIONSTORE_TEST_DB_URL")
	if databaseURL == "" {
		t.Skip("TRANSACTIONSTORE_TEST_DB_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := database.OpenTransactionPooler(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	userID, sourceID := uuid.New(), uuid.New()
	if _, err = pool.Exec(ctx, `insert into auth.users (id, email) values ($1, $2)`, userID, "debug-cap-"+userID.String()+"@example.test"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupContext, `delete from auth.users where id = $1`, userID)
	}()
	if _, err = pool.Exec(ctx, `
		insert into private.data_sources
			(id, user_id, source_type, provider, provider_message_id, received_at, raw_data)
		values ($1, $2, 'gmail_email', 'gmail', $3, now(), '{}')`, sourceID, userID, "debug-"+sourceID.String()); err != nil {
		t.Fatal(err)
	}
	largeJSON, _ := json.Marshal(map[string]string{"payload": strings.Repeat("x", sourceDebugFieldCharacterLimit+200)})
	var newestAttemptID uuid.UUID
	for index := 0; index < sourceDebugAttemptLimit+1; index++ {
		attemptID := uuid.New()
		if index == sourceDebugAttemptLimit {
			newestAttemptID = attemptID
		}
		if _, err = pool.Exec(ctx, `
			insert into private.source_parse_attempts
				(id, user_id, data_source_id, provider_request, validation_status, created_at)
			values ($1, $2, $3, $4::json, 'valid', $5)`, attemptID, userID, sourceID, string(largeJSON), time.Now().Add(time.Duration(index)*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
	}
	debug, err := New(pool).GetSourceParseDebug(ctx, userID, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(debug.Attempts) != sourceDebugAttemptLimit || !debug.HasMore || !debug.Truncated {
		t.Fatalf("debug bounds = attempts %d has_more %t truncated %t", len(debug.Attempts), debug.HasMore, debug.Truncated)
	}
	for _, attempt := range debug.Attempts {
		if attempt.ProviderRequest == nil || len(*attempt.ProviderRequest) > sourceDebugFieldCharacterLimit ||
			!slices.Contains(attempt.TruncatedFields, "provider_request") {
			t.Fatalf("attempt was not safely truncated: %#v", attempt)
		}
	}
	exact, err := New(pool).GetSourceParseAuditField(ctx, userID, sourceID, newestAttemptID, "provider_request")
	if err != nil {
		t.Fatal(err)
	}
	if exact.Value == nil || *exact.Value != string(largeJSON) || exact.MaxBytes != 10485760 {
		t.Fatalf("exact field = value bytes %d max %d", len(valueOrEmpty(exact.Value)), exact.MaxBytes)
	}
	if _, err = New(pool).GetSourceParseAuditField(ctx, userID, sourceID, newestAttemptID, "account_catalog"); !errors.Is(err, ErrSourceDebugFieldUnsupported) {
		t.Fatalf("unsupported exact field error = %v", err)
	}
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func TestSourcePipelinePersistsJSONWithProductionQueryMode(t *testing.T) {
	databaseURL := os.Getenv("TRANSACTIONSTORE_TEST_DB_URL")
	if databaseURL == "" {
		t.Skip("TRANSACTIONSTORE_TEST_DB_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := database.OpenTransactionPooler(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	userID, runID := uuid.New(), uuid.New()
	if _, err = pool.Exec(ctx, `insert into auth.users (id, email) values ($1, $2)`, userID, "source-json-"+userID.String()+"@example.test"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupContext, `delete from auth.users where id = $1`, userID)
	}()
	if _, err = pool.Exec(ctx, `
		insert into public.transaction_sync_runs (id, user_id, status, started_at)
		values ($1, $2, 'running', now())`, runID, userID); err != nil {
		t.Fatal(err)
	}

	store := New(pool)
	sourceID, inserted, err := store.StoreIngestedSource(ctx, IngestedSource{
		UserID: userID, SyncRunID: runID, ProviderMessageID: "simple-protocol-json",
		ReceivedAt: time.Now().UTC(), RawData: json.RawMessage(`{"subject":"Receipt"}`),
	})
	if err != nil || !inserted {
		t.Fatalf("StoreIngestedSource() = inserted %t, err %v", inserted, err)
	}
	if err = store.UpdateIngestedSourceRawData(ctx, userID, sourceID, json.RawMessage(`{"subject":"Receipt","body_truncated":true}`)); err != nil {
		t.Fatal(err)
	}
	if err = store.EnqueueSourceParse(ctx, userID, runID, sourceID); err != nil {
		t.Fatal(err)
	}
	if err = store.SaveParsedSource(ctx, userID, ParsedSourceResult{
		SourceParseAudit: SourceParseAudit{SourceID: sourceID, Model: "test-model", PromptComponents: json.RawMessage(`{}`)},
		SyncRunID:        &runID,
		ParsedResponse:   reconciliation.ParsedResponse{Candidate: reconciliation.Candidate{Confidence: 0.75}},
		ParsedCandidate:  json.RawMessage(`{"candidate":{},"evidence":[]}`), AutoEligible: true,
	}); err != nil {
		t.Fatal(err)
	}

	var bodyTruncated bool
	if err = pool.QueryRow(ctx, `select (raw_data ->> 'body_truncated')::boolean from private.data_sources where id = $1`, sourceID).Scan(&bodyTruncated); err != nil {
		t.Fatal(err)
	}
	if !bodyTruncated {
		t.Fatal("updated source JSON was not persisted")
	}
	var validAttempts, validJobs int
	if err = pool.QueryRow(ctx, `
		select count(*) filter (where jsonb_typeof(request_metadata) = 'object' and jsonb_typeof(parsed_candidate) = 'object')
		from private.source_parse_attempts where user_id = $1 and data_source_id = $2`, userID, sourceID).Scan(&validAttempts); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `
		select count(*) filter (where jsonb_typeof(payload) = 'object')
		from private.transaction_jobs where user_id = $1 and sync_run_id = $2 and data_source_id = $3`, userID, runID, sourceID).Scan(&validJobs); err != nil {
		t.Fatal(err)
	}
	if validAttempts != 1 || validJobs != 2 {
		t.Fatalf("valid JSON rows = attempts %d jobs %d, want 1/2", validAttempts, validJobs)
	}
}

// TestStoreIngestedSourceRetainsSavedProgressAcrossRetry is opt-in because it
// exercises real transaction boundaries. Local verification runs it against
// the disposable Supabase development database; ordinary unit runs skip it.
func TestStoreIngestedSourceRetainsSavedProgressAcrossRetry(t *testing.T) {
	databaseURL := os.Getenv("TRANSACTIONSTORE_TEST_DB_URL")
	if databaseURL == "" {
		t.Skip("TRANSACTIONSTORE_TEST_DB_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := database.OpenTransactionPooler(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	userID, runID := uuid.New(), uuid.New()
	if _, err = pool.Exec(ctx, `insert into auth.users (id, email) values ($1, $2)`, userID, "retry-"+userID.String()+"@example.test"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupContext, `delete from auth.users where id = $1`, userID)
	}()
	if _, err = pool.Exec(ctx, `
		insert into public.transaction_sync_runs (id, user_id, status, started_at)
		values ($1, $2, 'running', now())`, runID, userID); err != nil {
		t.Fatal(err)
	}
	store := New(pool)
	source := IngestedSource{
		UserID: userID, SyncRunID: runID, ProviderMessageID: "retry-message",
		ReceivedAt: time.Now().UTC(), RawData: json.RawMessage(`{"subject":"Receipt"}`),
	}
	if _, inserted, err := store.StoreIngestedSource(ctx, source); err != nil || !inserted {
		t.Fatalf("first StoreIngestedSource() = inserted %t, err %v", inserted, err)
	}
	if _, inserted, err := store.StoreIngestedSource(ctx, source); err != nil || inserted {
		t.Fatalf("retry StoreIngestedSource() = inserted %t, err %v", inserted, err)
	}
	if err = store.CompleteSyncRun(ctx, userID, runID, 1, 0); err != nil {
		t.Fatal(err)
	}
	run, err := store.GetSyncRun(ctx, userID, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.SourcesSavedCount != 1 {
		t.Fatalf("sources saved after retry = %d, want 1", run.SourcesSavedCount)
	}
	if run.IngestionCompletedAt == nil {
		t.Fatal("successful retry did not record ingestion completion")
	}
}

func TestCreateInternalTransferPersistsExactlyTwoLegsAndSharedEvidence(t *testing.T) {
	databaseURL := os.Getenv("TRANSACTIONSTORE_TEST_DB_URL")
	if databaseURL == "" {
		t.Skip("TRANSACTIONSTORE_TEST_DB_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := database.OpenTransactionPooler(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	userID := uuid.New()
	if _, err = pool.Exec(ctx, `insert into auth.users (id, email) values ($1, $2)`, userID, "transfer-"+userID.String()+"@example.test"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupContext, `delete from auth.users where id = $1`, userID)
	}()
	debitAccountID, creditAccountID, alternateAccountID, sourceID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if _, err = pool.Exec(ctx, `
		insert into public.accounts (id, user_id, side, account_type, name, institution_name)
		values
			($1, $3, 'asset', 'bank_account', 'Current', 'Bank'),
			($2, $3, 'asset', 'bank_account', 'Savings', 'Bank'),
			($4, $3, 'asset', 'bank_account', 'Alternate', 'Bank')`,
		debitAccountID, creditAccountID, userID, alternateAccountID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `
		insert into private.data_sources (
			id, user_id, source_type, provider, provider_message_id,
			received_at, raw_data, parse_status
		) values ($1, $2, 'gmail_email', 'gmail', 'transfer-message', now(), '{}', 'dangling')`,
		sourceID, userID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	store := New(pool)
	transfer, err := store.CreateInternalTransfer(ctx, userID, InternalTransferInput{
		Debit: TransferLegInput{
			AccountID: debitAccountID, Title: "Transfer out", OriginalAmountMinor: 5000,
			OriginalCurrency: "SGD", OccurredAt: now, LineItems: json.RawMessage("[]"),
			SourceIDs: []uuid.UUID{sourceID},
		},
		Credit: TransferLegInput{
			AccountID: creditAccountID, Title: "Transfer in", OriginalAmountMinor: 5000,
			OriginalCurrency: "SGD", OccurredAt: now, LineItems: json.RawMessage("[]"),
			SourceIDs: []uuid.UUID{sourceID},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if transfer.ID == uuid.Nil || transfer.Debit.TransactionKind != "debit" || transfer.Credit.TransactionKind != "credit" {
		t.Fatalf("transfer = %#v", transfer)
	}
	var transactionCount, linkCount, sourceLinkCount int
	if err = pool.QueryRow(ctx, `select count(*) from public.transactions where user_id = $1`, userID).Scan(&transactionCount); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `select count(*) from private.transaction_links where user_id = $1`, userID).Scan(&linkCount); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `select count(*) from private.transaction_data_sources where user_id = $1 and detached_at is null`, userID).Scan(&sourceLinkCount); err != nil {
		t.Fatal(err)
	}
	if transactionCount != 2 || linkCount != 1 || sourceLinkCount != 2 {
		t.Fatalf("persisted counts = transactions %d, links %d, source links %d", transactionCount, linkCount, sourceLinkCount)
	}
	updatedDebit, err := store.PatchTransaction(ctx, userID, transfer.Debit.ID, TransactionPatch{AccountID: &alternateAccountID})
	if err != nil {
		t.Fatalf("patch linked debit to distinct account: %v", err)
	}
	if updatedDebit.AccountID != alternateAccountID {
		t.Fatalf("patched debit account = %s, want %s", updatedDebit.AccountID, alternateAccountID)
	}
	_, err = store.PatchTransaction(ctx, userID, transfer.Debit.ID, TransactionPatch{AccountID: &creditAccountID})
	if !errors.Is(err, ErrTransferSameAccount) {
		t.Fatalf("same-account linked patch error = %v", err)
	}
	var persistedDebitAccountID uuid.UUID
	if err = pool.QueryRow(ctx, `select account_id from public.transactions where id = $1`, transfer.Debit.ID).Scan(&persistedDebitAccountID); err != nil {
		t.Fatal(err)
	}
	if persistedDebitAccountID != alternateAccountID {
		t.Fatalf("rejected patch changed debit account to %s", persistedDebitAccountID)
	}
	missingAccountID := uuid.New()
	_, err = store.PatchTransaction(ctx, userID, transfer.Debit.ID, TransactionPatch{AccountID: &missingAccountID})
	if !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("missing patch account error = %v", err)
	}
	missingCategoryID := uuid.New()
	_, err = store.PatchTransaction(ctx, userID, transfer.Debit.ID, TransactionPatch{
		CategoryID: OptionalUUID{Set: true, Value: &missingCategoryID},
	})
	if !errors.Is(err, ErrCategoryNotFound) {
		t.Fatalf("missing patch category error = %v", err)
	}
	_, err = store.CreateInternalTransfer(ctx, userID, InternalTransferInput{
		Debit:  TransferLegInput{AccountID: debitAccountID},
		Credit: TransferLegInput{AccountID: debitAccountID},
	})
	if !errors.Is(err, ErrTransferSameAccount) {
		t.Fatalf("same-account transfer error = %v", err)
	}
	_, err = store.CreateInternalTransfer(ctx, userID, InternalTransferInput{
		Debit: TransferLegInput{
			AccountID: debitAccountID, Title: "Duplicate evidence out", OriginalAmountMinor: 5000,
			OriginalCurrency: "SGD", OccurredAt: now, LineItems: json.RawMessage("[]"), SourceIDs: []uuid.UUID{sourceID},
		},
		Credit: TransferLegInput{
			AccountID: creditAccountID, Title: "Duplicate evidence in", OriginalAmountMinor: 5000,
			OriginalCurrency: "SGD", OccurredAt: now, LineItems: json.RawMessage("[]"), SourceIDs: []uuid.UUID{sourceID},
		},
	})
	if !errors.Is(err, ErrSourceAlreadyLinked) {
		t.Fatalf("prelinked transfer source error = %v", err)
	}
	if err = pool.QueryRow(ctx, `select count(*) from public.transactions where user_id = $1`, userID).Scan(&transactionCount); err != nil {
		t.Fatal(err)
	}
	if transactionCount != 2 {
		t.Fatalf("rejected transfers left %d transactions, want 2", transactionCount)
	}
}

func TestJobLeaseRenewalPreventsReclaimAndStaleOwnerCannotComplete(t *testing.T) {
	databaseURL := os.Getenv("TRANSACTIONSTORE_TEST_DB_URL")
	if databaseURL == "" {
		t.Skip("TRANSACTIONSTORE_TEST_DB_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := database.OpenTransactionPooler(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	userID := uuid.New()
	if _, err = pool.Exec(ctx, `insert into auth.users (id, email) values ($1, $2)`, userID, "lease-"+userID.String()+"@example.test"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupContext, `delete from auth.users where id = $1`, userID)
	}()
	jobID := uuid.New()
	if _, err = pool.Exec(ctx, `
		insert into private.transaction_jobs (id, user_id, job_type, run_after)
		values ($1, $2, 'gmail_ingestion', now() - interval '1 second')`, jobID, userID); err != nil {
		t.Fatal(err)
	}
	store := New(pool)
	claimedAt := time.Now().UTC()
	job, err := store.Claim(ctx, "worker-a", claimedAt)
	if err != nil || job == nil || job.ID != jobID || job.LeaseUntil == nil {
		t.Fatalf("Claim() = %#v, %v", job, err)
	}
	originalExpiry := *job.LeaseUntil
	if err = store.RenewLease(ctx, jobID, "worker-a", originalExpiry.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	var renewedExpiry time.Time
	if err = pool.QueryRow(ctx, `select lease_expires_at from private.transaction_jobs where id = $1`, jobID).Scan(&renewedExpiry); err != nil {
		t.Fatal(err)
	}
	if !renewedExpiry.After(originalExpiry) {
		t.Fatalf("renewed expiry = %s, original = %s", renewedExpiry, originalExpiry)
	}
	if reclaimed, claimErr := store.Claim(ctx, "worker-b", originalExpiry.Add(time.Second)); claimErr != nil || reclaimed != nil {
		t.Fatalf("renewed lease was reclaimed: job=%#v err=%v", reclaimed, claimErr)
	}
	if err = store.Complete(ctx, jobID, "worker-b"); !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatalf("stale Complete() error = %v", err)
	}
	var status, owner string
	if err = pool.QueryRow(ctx, `select status, leased_by from private.transaction_jobs where id = $1`, jobID).Scan(&status, &owner); err != nil {
		t.Fatal(err)
	}
	if status != "running" || owner != "worker-a" {
		t.Fatalf("stale completion mutated job: status=%q owner=%q", status, owner)
	}
}

func TestConcurrentOrdinaryAttachesLeaveExactlyOneActiveLink(t *testing.T) {
	databaseURL := os.Getenv("TRANSACTIONSTORE_TEST_DB_URL")
	if databaseURL == "" {
		t.Skip("TRANSACTIONSTORE_TEST_DB_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := database.OpenTransactionPooler(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	userID, accountID, sourceID := uuid.New(), uuid.New(), uuid.New()
	transactionIDs := []uuid.UUID{uuid.New(), uuid.New()}
	if _, err = pool.Exec(ctx, `insert into auth.users (id, email) values ($1, $2)`, userID, "cardinality-"+userID.String()+"@example.test"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupContext, `delete from auth.users where id = $1`, userID)
	}()
	if _, err = pool.Exec(ctx, `
		insert into public.accounts (id, user_id, side, account_type, name, institution_name)
		values ($1, $2, 'asset', 'bank_account', 'Current', 'Bank')`, accountID, userID); err != nil {
		t.Fatal(err)
	}
	for index, transactionID := range transactionIDs {
		if _, err = pool.Exec(ctx, `
			insert into public.transactions (
				id, user_id, account_id, transaction_kind, title,
				original_amount_minor, original_currency, occurred_at
			) values ($1, $2, $3, 'debit', $4, 100, 'SGD', now())`,
			transactionID, userID, accountID, "Transaction "+string(rune('A'+index))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = pool.Exec(ctx, `
		insert into private.data_sources (
			id, user_id, source_type, provider, provider_message_id, received_at, raw_data, parse_status
		) values ($1, $2, 'gmail_email', 'gmail', 'concurrent-attach', now(), '{}', 'dangling')`, sourceID, userID); err != nil {
		t.Fatal(err)
	}
	store := New(pool)
	type attachResult struct{ err error }
	results := make(chan attachResult, len(transactionIDs))
	start := make(chan struct{})
	for _, transactionID := range transactionIDs {
		go func(targetID uuid.UUID) {
			<-start
			_, attachErr := store.AttachSource(ctx, userID, sourceID, targetID)
			results <- attachResult{err: attachErr}
		}(transactionID)
	}
	close(start)
	successes, conflicts := 0, 0
	for range transactionIDs {
		result := <-results
		switch {
		case result.err == nil:
			successes++
		case errors.Is(result.err, ErrSourceAlreadyLinked):
			conflicts++
		default:
			t.Fatalf("AttachSource() error = %v", result.err)
		}
	}
	var activeLinks int
	if err = pool.QueryRow(ctx, `
		select count(*) from private.transaction_data_sources
		where user_id = $1 and data_source_id = $2 and detached_at is null`, userID, sourceID).Scan(&activeLinks); err != nil {
		t.Fatal(err)
	}
	if successes != 1 || conflicts != 1 || activeLinks != 1 {
		t.Fatalf("successes=%d conflicts=%d active_links=%d", successes, conflicts, activeLinks)
	}
	var activeLinkID, linkedTransactionID uuid.UUID
	if err = pool.QueryRow(ctx, `
		select id, transaction_id from private.transaction_data_sources
		where user_id = $1 and data_source_id = $2 and detached_at is null`, userID, sourceID).Scan(&activeLinkID, &linkedTransactionID); err != nil {
		t.Fatal(err)
	}
	if err = store.UnmatchSourceLink(ctx, userID, activeLinkID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.AttachSource(ctx, userID, sourceID, linkedTransactionID); err != nil {
		t.Fatalf("reattach to same transaction: %v", err)
	}
	var auditLinks int
	if err = pool.QueryRow(ctx, `
		select count(*) from private.transaction_data_sources
		where user_id = $1 and data_source_id = $2 and transaction_id = $3`, userID, sourceID, linkedTransactionID).Scan(&auditLinks); err != nil {
		t.Fatal(err)
	}
	if auditLinks != 2 {
		t.Fatalf("reattachment audit rows = %d, want 2", auditLinks)
	}
}

func TestConcurrentStaleCreateDecisionsProduceOneCanonicalTransaction(t *testing.T) {
	databaseURL := os.Getenv("TRANSACTIONSTORE_TEST_DB_URL")
	if databaseURL == "" {
		t.Skip("TRANSACTIONSTORE_TEST_DB_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := database.OpenTransactionPooler(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	userID, accountID := uuid.New(), uuid.New()
	sourceIDs := []uuid.UUID{uuid.New(), uuid.New()}
	if _, err = pool.Exec(ctx, `insert into auth.users (id, email) values ($1, $2)`, userID, "reconcile-race-"+userID.String()+"@example.test"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupContext, `delete from auth.users where id = $1`, userID)
	}()
	if _, err = pool.Exec(ctx, `
		insert into public.accounts (id, user_id, side, account_type, name, institution_name)
		values ($1, $2, 'asset', 'bank_account', 'Concurrent card', 'Bank')`, accountID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `
		insert into private.account_matching_keys
			(user_id, account_id, key_type, display_value, normalized_value)
		values ($1, $2, 'card_last_four', '**** 2562', '2562')`, userID, accountID); err != nil {
		t.Fatal(err)
	}
	for index, sourceID := range sourceIDs {
		if _, err = pool.Exec(ctx, `
			insert into private.data_sources (
				id, user_id, source_type, provider, provider_message_id, received_at, raw_data, parse_status
			) values ($1, $2, 'gmail_email', 'gmail', $3, now(), '{}', 'parsed')`,
			sourceID, userID, fmt.Sprintf("concurrent-create-%d-%s", index, sourceID)); err != nil {
			t.Fatal(err)
		}
	}

	occurredAt := time.Now().UTC().Truncate(time.Second)
	candidate := reconciliation.Candidate{
		UserID:              userID.String(),
		Kind:                reconciliation.KindDebit,
		Title:               "FairPrice purchase",
		MerchantName:        "FairPrice",
		OriginalAmountMinor: 301,
		OriginalCurrency:    "SGD",
		OccurredAt:          occurredAt,
		References:          []string{},
		AccountEvidence: reconciliation.AccountEvidence{
			CardLastFour:          "2562",
			AdditionalIdentifiers: []string{},
		},
		LineItems:    []reconciliation.LineItem{},
		Confidence:   0.95,
		AutoEligible: true,
	}
	staleDecision := reconciliation.Decision{
		Outcome:   reconciliation.OutcomeCreate,
		AccountID: accountID.String(),
		Reason:    "reliable unmatched candidate",
	}
	store := New(pool)
	results := make(chan error, len(sourceIDs))
	start := make(chan struct{})
	for _, sourceID := range sourceIDs {
		go func(id uuid.UUID) {
			<-start
			results <- store.PersistReconciliation(ctx, userID, ReconciliationResult{
				SourceID: id, Candidate: candidate, Decision: staleDecision,
			})
		}(sourceID)
	}
	close(start)
	for range sourceIDs {
		if persistErr := <-results; persistErr != nil {
			t.Fatalf("PersistReconciliation() error = %v", persistErr)
		}
	}

	var transactionCount, activeLinkCount, linkedSourceCount int
	if err = pool.QueryRow(ctx, `select count(*) from public.transactions where user_id = $1`, userID).Scan(&transactionCount); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `
		select count(*), count(distinct data_source_id)
		from private.transaction_data_sources
		where user_id = $1 and detached_at is null`, userID).Scan(&activeLinkCount, &linkedSourceCount); err != nil {
		t.Fatal(err)
	}
	if transactionCount != 1 || activeLinkCount != 2 || linkedSourceCount != 2 {
		t.Fatalf("transactions=%d active_links=%d linked_sources=%d, want 1/2/2", transactionCount, activeLinkCount, linkedSourceCount)
	}
}

func TestDeferredCardinalityTriggerRejectsConcurrentWriteSkew(t *testing.T) {
	databaseURL := os.Getenv("TRANSACTIONSTORE_TEST_DB_URL")
	if databaseURL == "" {
		t.Skip("TRANSACTIONSTORE_TEST_DB_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := database.OpenTransactionPooler(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	userID, accountID, sourceID := uuid.New(), uuid.New(), uuid.New()
	transactionIDs := []uuid.UUID{uuid.New(), uuid.New()}
	if _, err = pool.Exec(ctx, `insert into auth.users (id, email) values ($1, $2)`, userID, "trigger-"+userID.String()+"@example.test"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupContext, `delete from auth.users where id = $1`, userID)
	}()
	if _, err = pool.Exec(ctx, `
		insert into public.accounts (id, user_id, side, account_type, name, institution_name)
		values ($1, $2, 'asset', 'bank_account', 'Current', 'Bank')`, accountID, userID); err != nil {
		t.Fatal(err)
	}
	for index, transactionID := range transactionIDs {
		if _, err = pool.Exec(ctx, `
			insert into public.transactions (
				id, user_id, account_id, transaction_kind, title,
				original_amount_minor, original_currency, occurred_at
			) values ($1, $2, $3, 'debit', $4, 100, 'SGD', now())`,
			transactionID, userID, accountID, "Transaction "+string(rune('A'+index))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = pool.Exec(ctx, `
		insert into private.data_sources (
			id, user_id, source_type, provider, provider_message_id, received_at, raw_data
		) values ($1, $2, 'gmail_email', 'gmail', 'write-skew', now(), '{}')`, sourceID, userID); err != nil {
		t.Fatal(err)
	}
	txs := make([]pgx.Tx, 0, len(transactionIDs))
	for _, transactionID := range transactionIDs {
		tx, beginErr := pool.Begin(ctx)
		if beginErr != nil {
			t.Fatal(beginErr)
		}
		if _, execErr := tx.Exec(ctx, `
			insert into private.transaction_data_sources (
				user_id, transaction_id, data_source_id, role, matched_by
			) values ($1, $2, $3, 'other', 'user')`, userID, transactionID, sourceID); execErr != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(execErr)
		}
		txs = append(txs, tx)
	}
	commitResults := make(chan error, len(txs))
	start := make(chan struct{})
	for _, tx := range txs {
		go func(transaction pgx.Tx) {
			<-start
			commitResults <- transaction.Commit(ctx)
		}(tx)
	}
	close(start)
	successes, failures := 0, 0
	for range txs {
		if commitErr := <-commitResults; commitErr == nil {
			successes++
		} else {
			failures++
		}
	}
	var activeLinks int
	if err = pool.QueryRow(ctx, `
		select count(*) from private.transaction_data_sources
		where user_id = $1 and data_source_id = $2 and detached_at is null`, userID, sourceID).Scan(&activeLinks); err != nil {
		t.Fatal(err)
	}
	if successes != 1 || failures != 1 || activeLinks != 1 {
		t.Fatalf("concurrent commits = successes %d failures %d active links %d", successes, failures, activeLinks)
	}
}

func TestReconnectResetsGmailCursorAndLastSync(t *testing.T) {
	databaseURL := os.Getenv("TRANSACTIONSTORE_TEST_DB_URL")
	if databaseURL == "" {
		t.Skip("TRANSACTIONSTORE_TEST_DB_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := database.OpenTransactionPooler(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	userID := uuid.New()
	if _, err = pool.Exec(ctx, `insert into auth.users (id, email) values ($1, $2)`, userID, "reconnect-"+userID.String()+"@example.test"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupContext, `delete from auth.users where id = $1`, userID)
	}()
	if _, err = pool.Exec(ctx, `
		insert into private.gmail_connections (
			user_id, encrypted_refresh_token, sync_cursor, last_synced_at, status
		) values ($1, decode('0102', 'hex'), 'old-cursor', now(), 'error')`, userID); err != nil {
		t.Fatal(err)
	}
	store := New(pool)
	if err = store.UpsertGmailConnection(ctx, userID, []byte{3, 4}, json.RawMessage(`{"scope":"gmail.readonly"}`), "odin-finance"); err != nil {
		t.Fatal(err)
	}
	var cursor *string
	var lastSyncedAt *time.Time
	var status string
	if err = pool.QueryRow(ctx, `
		select sync_cursor, last_synced_at, status
		from private.gmail_connections where user_id = $1`, userID).Scan(&cursor, &lastSyncedAt, &status); err != nil {
		t.Fatal(err)
	}
	if cursor != nil || lastSyncedAt != nil || status != "active" {
		t.Fatalf("reconnected state = cursor %#v, last sync %#v, status %q", cursor, lastSyncedAt, status)
	}
}

func TestCreateTransactionFromSourcePreservesCategoryAndRejectsUnavailableAccount(t *testing.T) {
	databaseURL := os.Getenv("TRANSACTIONSTORE_TEST_DB_URL")
	if databaseURL == "" {
		t.Skip("TRANSACTIONSTORE_TEST_DB_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := database.OpenTransactionPooler(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	userID, accountID := uuid.New(), uuid.New()
	if _, err = pool.Exec(ctx, `insert into auth.users (id, email) values ($1, $2)`, userID, "category-"+userID.String()+"@example.test"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupContext, `delete from auth.users where id = $1`, userID)
	}()
	if _, err = pool.Exec(ctx, `
		insert into public.accounts (id, user_id, side, account_type, name, institution_name)
		values ($1, $2, 'asset', 'bank_account', 'Current', 'Bank')`, accountID, userID); err != nil {
		t.Fatal(err)
	}
	var knownCategoryID uuid.UUID
	if err = pool.QueryRow(ctx, `
		select id from public.transaction_categories
		where active and name = 'Coffee Shops'`).Scan(&knownCategoryID); err != nil {
		t.Fatal(err)
	}
	store := New(pool)
	testCases := []struct {
		name           string
		category       string
		want           *uuid.UUID
		messageKey     string
		missingAccount bool
	}{
		{name: "known", category: "Coffee Shops", want: &knownCategoryID, messageKey: "category-known"},
		{name: "unknown", category: "Not A Real Category", messageKey: "category-unknown"},
		{name: "missing-account", category: "Coffee Shops", messageKey: "category-missing-account", missingAccount: true},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			sourceID := uuid.New()
			if _, insertErr := pool.Exec(ctx, `
				insert into private.data_sources (
					id, user_id, source_type, provider, provider_message_id, received_at, raw_data, parse_status
				) values ($1, $2, 'gmail_email', 'gmail', $3, now(), '{}', 'dangling')`, sourceID, userID, testCase.messageKey); insertErr != nil {
				t.Fatal(insertErr)
			}
			candidate := reconciliation.ParsedResponse{
				Candidate: reconciliation.Candidate{
					Kind: reconciliation.KindDebit, Title: "Coffee", MerchantName: "Cafe",
					OriginalAmountMinor: 500, OriginalCurrency: "SGD", OccurredAt: time.Now().UTC(),
					References: []string{}, AccountEvidence: reconciliation.AccountEvidence{AdditionalIdentifiers: []string{}},
					LineItems: []reconciliation.LineItem{}, CategoryLeafName: testCase.category,
				},
				Evidence: []reconciliation.FieldEvidence{
					{Field: "transaction_kind", SourcePath: "text.kind", Confidence: 0.9},
					{Field: "title", SourcePath: "subject", Confidence: 0.9},
					{Field: "merchant_name", SourcePath: "text.merchant", Confidence: 0.9},
					{Field: "original_amount_minor", SourcePath: "text.amount", Confidence: 0.9},
					{Field: "original_currency", SourcePath: "text.currency", Confidence: 0.9},
					{Field: "occurred_at", SourcePath: "received_at", Confidence: 0.9},
					{Field: "category_leaf_name", SourcePath: "text.category", Confidence: 0.9},
				},
			}
			raw, marshalErr := json.Marshal(candidate)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if _, insertErr := pool.Exec(ctx, `
				insert into private.source_parse_attempts (
					user_id, data_source_id, parsed_candidate, validation_status, completed_at
				) values ($1, $2, $3::jsonb, 'valid', now())`, userID, sourceID, string(raw)); insertErr != nil {
				t.Fatal(insertErr)
			}
			selectedAccountID := accountID
			if testCase.missingAccount {
				selectedAccountID = uuid.New()
			}
			transaction, createErr := store.CreateTransactionFromSource(ctx, userID, sourceID, selectedAccountID)
			if testCase.missingAccount {
				if !errors.Is(createErr, ErrAccountNotFound) {
					t.Fatalf("missing account error = %v", createErr)
				}
				return
			}
			if createErr != nil {
				t.Fatal(createErr)
			}
			if testCase.want == nil {
				if transaction.CategoryID != nil {
					t.Fatalf("unknown category resolved to %s", *transaction.CategoryID)
				}
			} else if transaction.CategoryID == nil || *transaction.CategoryID != *testCase.want {
				t.Fatalf("category = %#v, want %s", transaction.CategoryID, *testCase.want)
			}
		})
	}
}

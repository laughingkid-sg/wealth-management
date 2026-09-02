// Package transactione2e contains an explicitly opt-in, non-destructive live
// verification of the complete Gmail-to-Transactions worker pipeline.
package transactione2e

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
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

const (
	liveTransactionsE2EFlag        = "LIVE_TRANSACTIONS_E2E"
	liveTransactionsStorageE2EFlag = "LIVE_TRANSACTIONS_STORAGE_E2E"
	liveSignedURLExpirySeconds     = 5 * 60
)

func classifyLiveRuntimeError(err error) string {
	switch {
	case errors.Is(err, transactionstore.ErrSyncRunInProgress):
		return "another sync run is already active"
	case errors.Is(err, transactionstore.ErrGmailConnectionRequired):
		return "an active Gmail connection is required"
	case errors.Is(err, context.Canceled):
		return "operation was cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "operation timed out"
	}
	var scanError pgx.ScanArgError
	if errors.As(err, &scanError) {
		return fmt.Sprintf("database result scan failed at column %d (%s)", scanError.ColumnIndex, scanError.FieldName)
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		return "database rejected the request (SQLSTATE " + postgresError.Code + ")"
	}
	return "unclassified database/runtime failure"
}

type liveStorageVerificationError struct {
	stage      string
	reason     string
	httpStatus int
	cause      error
}

func (e *liveStorageVerificationError) Error() string { return "live Storage verification failed" }
func (e *liveStorageVerificationError) Unwrap() error { return e.cause }

func classifyLiveStorageVerificationError(err error) string {
	var verificationError *liveStorageVerificationError
	if !errors.As(err, &verificationError) {
		return "stage=unclassified"
	}
	if verificationError.httpStatus != 0 {
		return fmt.Sprintf("stage=%s http_status=%d", verificationError.stage, verificationError.httpStatus)
	}
	if verificationError.reason != "" {
		return fmt.Sprintf("stage=%s reason=%s", verificationError.stage, verificationError.reason)
	}
	return "stage=" + verificationError.stage
}

func signedURLValidationReason(raw string, supabaseURL *url.URL) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "parse_failed"
	}
	if parsed.Scheme != "https" {
		return "scheme_not_https"
	}
	if !strings.EqualFold(parsed.Host, supabaseURL.Host) {
		return "host_mismatch"
	}
	if parsed.RawQuery == "" {
		return "missing_query"
	}
	expectedPathPrefix := strings.TrimRight(supabaseURL.Path, "/") + "/storage/v1/object/sign/" + attachmentstorage.Bucket + "/"
	if !strings.HasPrefix(parsed.Path, expectedPathPrefix) {
		return "path_prefix_invalid"
	}
	return ""
}

func TestClassifyLiveRuntimeErrorDoesNotExposeDatabaseDetails(t *testing.T) {
	secret := "refresh-token-and-user-00000000-0000-0000-0000-000000000001"
	testCases := []struct {
		name string
		err  error
		want string
	}{
		{name: "active run", err: fmt.Errorf("wrapped: %w", transactionstore.ErrSyncRunInProgress), want: "another sync run is already active"},
		{name: "connection required", err: transactionstore.ErrGmailConnectionRequired, want: "an active Gmail connection is required"},
		{name: "scan", err: pgx.ScanArgError{ColumnIndex: 4, FieldName: "ingestion_completed_at", Err: errors.New(secret)}, want: "database result scan failed at column 4 (ingestion_completed_at)"},
		{name: "postgres", err: &pgconn.PgError{Code: "23514", Message: secret, Detail: secret, Hint: secret, Where: secret}, want: "database rejected the request (SQLSTATE 23514)"},
		{name: "other", err: errors.New(secret), want: "unclassified database/runtime failure"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			got := classifyLiveRuntimeError(testCase.err)
			if got != testCase.want {
				t.Fatalf("classifyLiveRuntimeError() = %q, want %q", got, testCase.want)
			}
			if strings.Contains(got, secret) {
				t.Fatal("classification exposed database detail")
			}
		})
	}
}

func TestClassifyLiveStorageVerificationErrorExposesOnlyStageAndStatus(t *testing.T) {
	secret := "signed-url-object-path-and-token"
	err := &liveStorageVerificationError{
		stage: "bounded_get_response", httpStatus: http.StatusForbidden, cause: errors.New(secret),
	}
	got := classifyLiveStorageVerificationError(err)
	if got != "stage=bounded_get_response http_status=403" {
		t.Fatalf("classification = %q", got)
	}
	if strings.Contains(got, secret) {
		t.Fatal("Storage classification exposed sensitive detail")
	}
	err = &liveStorageVerificationError{stage: "signed_url_validation", reason: "missing_query", cause: errors.New(secret)}
	got = classifyLiveStorageVerificationError(err)
	if got != "stage=signed_url_validation reason=missing_query" || strings.Contains(got, secret) {
		t.Fatalf("URL classification = %q", got)
	}
}

type syncRunReader interface {
	GetSyncRun(context.Context, uuid.UUID, uuid.UUID) (transactionstore.SyncRun, error)
}

type jobProcessor interface {
	ProcessOne(context.Context) (bool, error)
}

// drainRun is deliberately dependency-driven so its polling and terminal-state
// behavior can be exercised with local/provider stubs. The live test below
// composes the same function with the real PostgreSQL store and provider clients.
func drainRun(ctx context.Context, reader syncRunReader, processor jobProcessor, userID, runID uuid.UUID) (transactionstore.SyncRun, error) {
	for {
		run, err := reader.GetSyncRun(ctx, userID, runID)
		if err != nil {
			return transactionstore.SyncRun{}, fmt.Errorf("read sync run: %w", err)
		}
		switch run.Status {
		case "completed", "failed", "cancelled":
			return run, nil
		case "queued", "running":
		default:
			return transactionstore.SyncRun{}, errors.New("sync run has an unsupported status")
		}

		processed, err := processor.ProcessOne(ctx)
		if err != nil {
			return transactionstore.SyncRun{}, fmt.Errorf("process scoped transaction job: %w", err)
		}
		if processed {
			continue
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return transactionstore.SyncRun{}, ctx.Err()
		case <-timer.C:
		}
	}
}

type scriptedRunReader struct {
	processor *scriptedProcessor
}

func (r scriptedRunReader) GetSyncRun(context.Context, uuid.UUID, uuid.UUID) (transactionstore.SyncRun, error) {
	if r.processor.calls >= 2 {
		now := time.Now()
		return transactionstore.SyncRun{Status: "completed", CompletedAt: &now}, nil
	}
	return transactionstore.SyncRun{Status: "running"}, nil
}

type scriptedProcessor struct {
	calls int
}

func (p *scriptedProcessor) ProcessOne(context.Context) (bool, error) {
	p.calls++
	return p.calls <= 2, nil
}

func TestDrainRunWorksWithStubbedRuntime(t *testing.T) {
	processor := &scriptedProcessor{}
	run, err := drainRun(context.Background(), scriptedRunReader{processor: processor}, processor, uuid.New(), uuid.New())
	if err != nil {
		t.Fatalf("drainRun() error = %v", err)
	}
	if run.Status != "completed" || processor.calls != 2 {
		t.Fatalf("drainRun() status/calls = %q/%d, want completed/2", run.Status, processor.calls)
	}
}

// scopedJobStore is the live harness's critical safety boundary. Claim is
// constrained to the newly created run and its owner, so this worker cannot
// take unrelated jobs from a shared hosted queue. Lease, completion, and retry
// use the production implementation after a scoped claim establishes ownership.
type scopedJobStore struct {
	pool     *pgxpool.Pool
	delegate *transactionstore.Store
	userID   uuid.UUID
	runID    uuid.UUID
}

var _ jobs.Store = (*scopedJobStore)(nil)

func (s *scopedJobStore) Claim(ctx context.Context, workerID string, now time.Time) (*jobs.Job, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var job jobs.Job
	var payload []byte
	var lease time.Time
	err = tx.QueryRow(ctx, `
		with candidate as (
			select id
			from private.transaction_jobs
			where user_id = $3
				and sync_run_id = $4
				and attempts < max_attempts
				and (
					(status = 'queued' and run_after <= $1)
					or (status = 'running' and lease_expires_at <= $1)
				)
			order by run_after, created_at
			limit 1
			for update skip locked
		)
		update private.transaction_jobs job
		set status = 'running',
			attempts = job.attempts + 1,
			leased_at = $1,
			lease_expires_at = $1::timestamptz + interval '5 minutes',
			leased_by = $2
		from candidate
		where job.id = candidate.id
		returning job.id, job.user_id, job.sync_run_id, job.job_type,
			job.payload, job.attempts, job.run_after, job.lease_expires_at`,
		now, workerID, s.userID, s.runID,
	).Scan(
		&job.ID, &job.UserID, &job.SyncRunID, &job.Kind,
		&payload, &job.Attempts, &job.Available, &lease,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		if err = tx.Commit(ctx); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	job.Payload = payload
	job.LeaseUntil = &lease
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &job, nil
}

func (s *scopedJobStore) RenewLease(ctx context.Context, jobID uuid.UUID, workerID string, now time.Time) error {
	return s.delegate.RenewLease(ctx, jobID, workerID, now)
}

func (s *scopedJobStore) Complete(ctx context.Context, jobID uuid.UUID, workerID string) error {
	return s.delegate.Complete(ctx, jobID, workerID)
}

func (s *scopedJobStore) Retry(ctx context.Context, jobID uuid.UUID, workerID string, attempt int, retryAt time.Time, reason string) error {
	return s.delegate.Retry(ctx, jobID, workerID, attempt, retryAt, reason)
}

func TestScopedJobStoreClaimsOnlyResumedRunWithProductionQueryMode(t *testing.T) {
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
	selectedUser, otherUser := uuid.New(), uuid.New()
	if _, err = pool.Exec(ctx, `
		insert into auth.users (id, email) values
			($1, $3),
			($2, $4)`,
		selectedUser, otherUser,
		"scoped-selected-"+selectedUser.String()+"@example.test",
		"scoped-other-"+otherUser.String()+"@example.test"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupContext, `delete from auth.users where id in ($1, $2)`, selectedUser, otherUser)
	}()

	store := transactionstore.New(pool)
	created, err := store.CreateSyncRun(ctx, selectedUser, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.CreateSyncRun(ctx, otherUser, true); err != nil {
		t.Fatal(err)
	}
	resumed, err := startOrResumeLiveSyncRun(ctx, pool, store, selectedUser, true)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.ID != created.ID {
		t.Fatal("resume selected a different sync run")
	}
	scoped := &scopedJobStore{pool: pool, delegate: store, userID: selectedUser, runID: resumed.ID}
	job, err := scoped.Claim(ctx, "scoped-test-worker", time.Now().UTC())
	if err != nil || job == nil {
		t.Fatalf("scoped Claim() = %#v, %v", job, err)
	}
	if job.UserID != selectedUser || job.SyncRunID == nil || *job.SyncRunID != resumed.ID || job.Kind != jobs.KindGmailIngest {
		t.Fatal("scoped Claim() returned a job outside the selected run")
	}
	var otherStatus string
	var otherAttempts int
	if err = pool.QueryRow(ctx, `
		select job.status, job.attempts
		from private.transaction_jobs job
		where job.user_id = $1 and job.job_type = 'gmail_ingestion'`, otherUser).Scan(&otherStatus, &otherAttempts); err != nil {
		t.Fatal(err)
	}
	if otherStatus != "queued" || otherAttempts != 0 {
		t.Fatal("scoped Claim() mutated another user's job")
	}
}

type runSnapshot struct {
	SourceJobs      int
	TerminalSources int
	FailedSources   int
	ActiveJobs      int
	FailedJobs      int
	GmailJobs       int
	CompletedGmail  int
}

func loadRunSnapshot(ctx context.Context, pool *pgxpool.Pool, userID, runID uuid.UUID) (runSnapshot, error) {
	var snapshot runSnapshot
	err := pool.QueryRow(ctx, `
		select
			count(distinct job.data_source_id) filter (
				where job.job_type = 'source_parsing'
			)::integer,
			count(distinct job.data_source_id) filter (
				where job.job_type = 'source_parsing'
					and source.parse_status in ('parsed', 'review_required', 'dangling', 'failed')
			)::integer,
			count(distinct job.data_source_id) filter (
				where job.job_type = 'source_parsing'
					and source.parse_status = 'failed'
			)::integer,
			count(*) filter (where job.status in ('queued', 'running'))::integer,
			count(*) filter (where job.status = 'failed')::integer,
			count(*) filter (where job.job_type = 'gmail_ingestion')::integer,
			count(*) filter (
				where job.job_type = 'gmail_ingestion' and job.status = 'completed'
			)::integer
		from private.transaction_jobs job
		left join private.data_sources source
			on source.id = job.data_source_id and source.user_id = job.user_id
		where job.user_id = $1 and job.sync_run_id = $2`, userID, runID).Scan(
		&snapshot.SourceJobs, &snapshot.TerminalSources, &snapshot.FailedSources,
		&snapshot.ActiveJobs, &snapshot.FailedJobs, &snapshot.GmailJobs, &snapshot.CompletedGmail,
	)
	return snapshot, err
}

func validateCompletedRun(run transactionstore.SyncRun, snapshot runSnapshot) error {
	if run.Status != "completed" || run.CompletedAt == nil || run.IngestionCompletedAt == nil {
		return errors.New("sync run did not reach a completed terminal state")
	}
	if run.ErrorSummary != nil || run.SourcesFailedCount != 0 || snapshot.FailedSources != 0 || snapshot.FailedJobs != 0 {
		return errors.New("sync run contains a source or job failure")
	}
	if snapshot.ActiveJobs != 0 {
		return errors.New("sync run completed with active jobs")
	}
	if snapshot.GmailJobs != 1 || snapshot.CompletedGmail != 1 {
		return errors.New("sync run does not contain exactly one completed Gmail ingestion job")
	}
	if run.MessagesFoundCount < run.SourcesSavedCount {
		return errors.New("sync run saved more sources than Gmail messages found")
	}
	if snapshot.SourceJobs != snapshot.TerminalSources ||
		snapshot.SourceJobs != run.SourcesParsedCount+run.SourcesFailedCount {
		return errors.New("sync run source terminal-state counters are incoherent")
	}
	if run.SourcesParsedCount != run.SourcesLinkedCount+run.DanglingSourcesCount+run.ReviewRequiredCount {
		return errors.New("sync run reconciliation counters are incoherent")
	}
	if run.TransactionsCreatedCount > run.SourcesLinkedCount {
		return errors.New("sync run created more transactions than linked sources")
	}
	return nil
}

func TestValidateCompletedRun(t *testing.T) {
	now := time.Now()
	run := transactionstore.SyncRun{
		Status: "completed", CompletedAt: &now, IngestionCompletedAt: &now,
		MessagesFoundCount: 2, SourcesSavedCount: 2, SourcesParsedCount: 2,
		TransactionsCreatedCount: 1, SourcesLinkedCount: 1, ReviewRequiredCount: 1,
	}
	snapshot := runSnapshot{SourceJobs: 2, TerminalSources: 2, GmailJobs: 1, CompletedGmail: 1}
	if err := validateCompletedRun(run, snapshot); err != nil {
		t.Fatalf("validateCompletedRun() error = %v", err)
	}
	run.SourcesLinkedCount = 2
	if err := validateCompletedRun(run, snapshot); err == nil {
		t.Fatal("validateCompletedRun() accepted incoherent reconciliation counters")
	}
}

type liveUserCandidateReader interface {
	ActiveGmailUsers(context.Context) ([]uuid.UUID, error)
	ActiveAccountOwners(context.Context) ([]uuid.UUID, error)
}

type postgresLiveUserCandidateReader struct{ pool *pgxpool.Pool }

func (r postgresLiveUserCandidateReader) ActiveGmailUsers(ctx context.Context) ([]uuid.UUID, error) {
	return loadLimitedLiveUsers(ctx, r.pool, `
		select user_id
		from private.gmail_connections
		where provider = 'gmail' and status = 'active'
		order by user_id
		limit 2`)
}

func (r postgresLiveUserCandidateReader) ActiveAccountOwners(ctx context.Context) ([]uuid.UUID, error) {
	return loadLimitedLiveUsers(ctx, r.pool, `
		select distinct user_id
		from public.accounts
		where deleted_at is null
		order by user_id
		limit 2`)
}

func loadLimitedLiveUsers(ctx context.Context, pool *pgxpool.Pool, query string) ([]uuid.UUID, error) {
	rows, err := pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := make([]uuid.UUID, 0, 2)
	for rows.Next() {
		var userID uuid.UUID
		if err = rows.Scan(&userID); err != nil {
			return nil, err
		}
		users = append(users, userID)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return users, nil
}

type liveUserSelection struct {
	userID                uuid.UUID
	allowDevelopmentToken bool
}

func selectLivePipelineUser(
	ctx context.Context,
	reader liveUserCandidateReader,
	environment, developmentRefreshToken string,
) (liveUserSelection, error) {
	activeGmailUsers, err := reader.ActiveGmailUsers(ctx)
	if err != nil {
		return liveUserSelection{}, fmt.Errorf("load active Gmail owners: %w", err)
	}
	switch len(activeGmailUsers) {
	case 1:
		return liveUserSelection{userID: activeGmailUsers[0]}, nil
	case 0:
	default:
		return liveUserSelection{}, fmt.Errorf("expected at most one active Gmail owner, found %d", len(activeGmailUsers))
	}
	if environment != "development" || strings.TrimSpace(developmentRefreshToken) == "" {
		return liveUserSelection{}, errors.New("no active Gmail connection and no eligible development fallback")
	}
	accountOwners, err := reader.ActiveAccountOwners(ctx)
	if err != nil {
		return liveUserSelection{}, fmt.Errorf("load eligible Account owners: %w", err)
	}
	accountOwners = uniqueLiveUsers(accountOwners)
	if len(accountOwners) != 1 {
		return liveUserSelection{}, fmt.Errorf("expected exactly one eligible Account owner, found %d", len(accountOwners))
	}
	return liveUserSelection{userID: accountOwners[0], allowDevelopmentToken: true}, nil
}

// startOrResumeLiveSyncRun permits a rerun after the harness created a run but
// failed before its first claim. The fallback is intentionally narrow: exactly
// one recent, pristine queued run with exactly one untouched Gmail job and the
// expected connection shape. It cannot adopt a partially processed run.
func startOrResumeLiveSyncRun(
	ctx context.Context,
	pool *pgxpool.Pool,
	store *transactionstore.Store,
	userID uuid.UUID,
	allowDevelopmentToken bool,
) (transactionstore.SyncRun, error) {
	run, err := store.CreateSyncRun(ctx, userID, allowDevelopmentToken)
	if err == nil || !errors.Is(err, transactionstore.ErrSyncRunInProgress) {
		return run, err
	}
	rows, err := pool.Query(ctx, `
		select run.id
		from public.transaction_sync_runs run
		where run.user_id = $1
			and run.status = 'queued'
			and run.created_at >= now() - interval '1 hour'
			and run.started_at is null
			and run.completed_at is null
			and run.ingestion_completed_at is null
			and run.messages_found_count = 0
			and run.sources_saved_count = 0
			and run.sources_parsed_count = 0
			and run.sources_failed_count = 0
			and run.transactions_created_count = 0
			and run.sources_linked_count = 0
			and run.dangling_sources_count = 0
			and run.review_required_count = 0
			and run.error_summary is null
			and (
				($2::boolean and run.gmail_connection_id is null)
				or (
					not $2::boolean
					and exists (
						select 1 from private.gmail_connections connection
						where connection.id = run.gmail_connection_id
							and connection.user_id = run.user_id
							and connection.provider = 'gmail'
							and connection.status = 'active'
					)
				)
			)
			and 1 = (
				select count(*) from private.transaction_jobs job
				where job.user_id = run.user_id and job.sync_run_id = run.id
			)
			and 1 = (
				select count(*) from private.transaction_jobs job
				where job.user_id = run.user_id
					and job.sync_run_id = run.id
					and job.data_source_id is null
					and job.job_type = 'gmail_ingestion'
					and job.status = 'queued'
					and job.attempts = 0
					and job.leased_at is null
					and job.lease_expires_at is null
					and job.leased_by is null
					and job.completed_at is null
					and job.last_error is null
					and job.payload = jsonb_build_object('sync_run_id', run.id::text)
			)
		order by run.created_at desc, run.id desc
		limit 2`, userID, allowDevelopmentToken)
	if err != nil {
		return transactionstore.SyncRun{}, err
	}
	defer rows.Close()
	candidates := make([]uuid.UUID, 0, 2)
	for rows.Next() {
		var runID uuid.UUID
		if err = rows.Scan(&runID); err != nil {
			return transactionstore.SyncRun{}, err
		}
		candidates = append(candidates, runID)
	}
	if err = rows.Err(); err != nil {
		return transactionstore.SyncRun{}, err
	}
	if len(candidates) != 1 {
		return transactionstore.SyncRun{}, errors.New("active sync run is not a unique pristine live-harness candidate")
	}
	return store.GetSyncRun(ctx, userID, candidates[0])
}

func uniqueLiveUsers(users []uuid.UUID) []uuid.UUID {
	unique := make([]uuid.UUID, 0, len(users))
	seen := make(map[uuid.UUID]struct{}, len(users))
	for _, userID := range users {
		if _, exists := seen[userID]; exists {
			continue
		}
		seen[userID] = struct{}{}
		unique = append(unique, userID)
	}
	return unique
}

type liveUserCandidateReaderStub struct {
	activeGmailUsers   []uuid.UUID
	activeAccountUsers []uuid.UUID
	activeGmailErr     error
	activeAccountErr   error
	accountCalls       int
}

func (s *liveUserCandidateReaderStub) ActiveGmailUsers(context.Context) ([]uuid.UUID, error) {
	return s.activeGmailUsers, s.activeGmailErr
}

func (s *liveUserCandidateReaderStub) ActiveAccountOwners(context.Context) ([]uuid.UUID, error) {
	s.accountCalls++
	return s.activeAccountUsers, s.activeAccountErr
}

func TestSelectLivePipelineUserFailsClosedAndPreservesConnectionPath(t *testing.T) {
	activeUser, fallbackUser, otherUser := uuid.New(), uuid.New(), uuid.New()
	testCases := []struct {
		name        string
		reader      *liveUserCandidateReaderStub
		environment string
		token       string
		wantUser    uuid.UUID
		wantDev     bool
		wantErr     bool
		wantCalls   int
	}{
		{name: "active connection wins", reader: &liveUserCandidateReaderStub{activeGmailUsers: []uuid.UUID{activeUser}}, environment: "development", token: "secret-token", wantUser: activeUser},
		{name: "active connection needs no fallback", reader: &liveUserCandidateReaderStub{activeGmailUsers: []uuid.UUID{activeUser}}, environment: "production", wantUser: activeUser},
		{name: "multiple active connections", reader: &liveUserCandidateReaderStub{activeGmailUsers: []uuid.UUID{activeUser, otherUser}}, environment: "development", token: "secret-token", wantErr: true},
		{name: "duplicate active connection rows", reader: &liveUserCandidateReaderStub{activeGmailUsers: []uuid.UUID{activeUser, activeUser}}, environment: "development", token: "secret-token", wantErr: true},
		{name: "active lookup failure", reader: &liveUserCandidateReaderStub{activeGmailErr: errors.New("lookup failed")}, environment: "development", token: "secret-token", wantErr: true},
		{name: "fallback requires token", reader: &liveUserCandidateReaderStub{}, environment: "development", wantErr: true},
		{name: "fallback requires development", reader: &liveUserCandidateReaderStub{}, environment: "production", token: "secret-token", wantErr: true},
		{name: "one Account owner fallback", reader: &liveUserCandidateReaderStub{activeAccountUsers: []uuid.UUID{fallbackUser}}, environment: "development", token: "secret-token", wantUser: fallbackUser, wantDev: true, wantCalls: 1},
		{name: "duplicate Accounts for one owner", reader: &liveUserCandidateReaderStub{activeAccountUsers: []uuid.UUID{fallbackUser, fallbackUser}}, environment: "development", token: "secret-token", wantUser: fallbackUser, wantDev: true, wantCalls: 1},
		{name: "no Account owner", reader: &liveUserCandidateReaderStub{}, environment: "development", token: "secret-token", wantErr: true, wantCalls: 1},
		{name: "ambiguous Account owners", reader: &liveUserCandidateReaderStub{activeAccountUsers: []uuid.UUID{fallbackUser, otherUser}}, environment: "development", token: "secret-token", wantErr: true, wantCalls: 1},
		{name: "Account lookup failure", reader: &liveUserCandidateReaderStub{activeAccountErr: errors.New("lookup failed")}, environment: "development", token: "secret-token", wantErr: true, wantCalls: 1},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			selection, err := selectLivePipelineUser(context.Background(), testCase.reader, testCase.environment, testCase.token)
			if (err != nil) != testCase.wantErr {
				t.Fatalf("selectLivePipelineUser() error = %v", err)
			}
			if testCase.reader.accountCalls != testCase.wantCalls {
				t.Fatalf("Account owner lookups = %d, want %d", testCase.reader.accountCalls, testCase.wantCalls)
			}
			if err != nil {
				for _, secret := range []string{activeUser.String(), fallbackUser.String(), otherUser.String(), testCase.token} {
					if secret != "" && strings.Contains(err.Error(), secret) {
						t.Fatal("preflight error exposed an identifier or token")
					}
				}
				return
			}
			if selection.userID != testCase.wantUser || selection.allowDevelopmentToken != testCase.wantDev {
				t.Fatalf("selection = %#v, want user/dev %s/%t", selection, testCase.wantUser, testCase.wantDev)
			}
		})
	}
}

func validateOneSignedAttachment(
	ctx context.Context,
	pool *pgxpool.Pool,
	store *transactionstore.Store,
	storage *attachmentstorage.Client,
	httpClient *http.Client,
	supabaseURL *url.URL,
	userID, runID uuid.UUID,
) error {
	rows, err := pool.Query(ctx, `
		select distinct data_source_id
		from private.transaction_jobs
		where user_id = $1 and sync_run_id = $2 and data_source_id is not null`, userID, runID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var sourceID uuid.UUID
		if err = rows.Scan(&sourceID); err != nil {
			return err
		}
		validated, validateErr := validateSignedAttachmentForSource(ctx, store, storage, httpClient, supabaseURL, userID, sourceID)
		if validateErr != nil {
			return validateErr
		}
		if validated {
			return nil
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	return errors.New("sync run did not produce a stored attachment to verify")
}

func validateSignedAttachmentForSource(
	ctx context.Context,
	store *transactionstore.Store,
	storage *attachmentstorage.Client,
	httpClient *http.Client,
	supabaseURL *url.URL,
	userID, sourceID uuid.UUID,
) (bool, error) {
	attachments, err := store.ListSourceAttachments(ctx, userID, sourceID)
	if err != nil {
		return false, &liveStorageVerificationError{stage: "metadata_lookup", cause: err}
	}
	for _, attachment := range attachments {
		signedURL, signErr := storage.SignURL(ctx, attachmentstorage.ObjectRequest{
			UserID: userID, SourceID: sourceID, ObjectPath: attachment.ObjectPath,
		}, liveSignedURLExpirySeconds)
		if signErr != nil {
			status, _ := attachmentstorage.HTTPStatusCode(signErr)
			return false, &liveStorageVerificationError{stage: "sign_request", httpStatus: status, cause: signErr}
		}
		if reason := signedURLValidationReason(signedURL, supabaseURL); reason != "" {
			return false, &liveStorageVerificationError{
				stage: "signed_url_validation", reason: reason,
				cause: errors.New("signed attachment URL is malformed or outside the configured Supabase host"),
			}
		}
		request, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, signedURL, nil)
		if requestErr != nil {
			return false, &liveStorageVerificationError{stage: "bounded_get_request", cause: requestErr}
		}
		request.Header.Set("Range", "bytes=0-0")
		response, requestErr := httpClient.Do(request)
		if requestErr != nil {
			return false, &liveStorageVerificationError{stage: "bounded_get_transport", cause: requestErr}
		}
		_, readErr := io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		response.Body.Close()
		if readErr != nil {
			return false, &liveStorageVerificationError{stage: "bounded_get_body", cause: readErr}
		}
		if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent {
			return false, &liveStorageVerificationError{
				stage: "bounded_get_response", httpStatus: response.StatusCode,
				cause: errors.New("signed attachment URL did not authorize a bounded download"),
			}
		}
		return true, nil
	}
	return false, nil
}

func validateAttachmentForRun(sourcesSaved int, currentRun, existingOwned func() error) error {
	if sourcesSaved > 0 {
		return currentRun()
	}
	return existingOwned()
}

func validateOneOwnedStoredAttachment(
	ctx context.Context,
	pool *pgxpool.Pool,
	store *transactionstore.Store,
	storage *attachmentstorage.Client,
	httpClient *http.Client,
	supabaseURL *url.URL,
	userID uuid.UUID,
) error {
	var sourceID uuid.UUID
	err := pool.QueryRow(ctx, `
		select source.id
		from private.data_sources source
		where source.user_id = $1
			and source.source_type = 'gmail_email'
			and exists (
				select 1
				from jsonb_array_elements(coalesce(source.raw_data -> 'attachments', '[]'::jsonb)) attachment
				where attachment ->> 'storage_status' = 'stored'
					and nullif(btrim(attachment ->> 'object_path'), '') is not null
			)
		order by source.received_at desc, source.id desc
		limit 1`, userID).Scan(&sourceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("no owned stored attachment is available")
	}
	if err != nil {
		return &liveStorageVerificationError{stage: "metadata_lookup", cause: err}
	}
	validated, err := validateSignedAttachmentForSource(ctx, store, storage, httpClient, supabaseURL, userID, sourceID)
	if err != nil {
		return err
	}
	if !validated {
		return errors.New("owned source had no valid stored attachment metadata")
	}
	return nil
}

func TestValidateAttachmentForRunUsesExistingOnlyForIdempotentRun(t *testing.T) {
	testCases := []struct {
		name              string
		sourcesSaved      int
		wantCurrentCalls  int
		wantExistingCalls int
	}{
		{name: "new sources require current run", sourcesSaved: 1, wantCurrentCalls: 1},
		{name: "duplicate-only run uses existing owned object", sourcesSaved: 0, wantExistingCalls: 1},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			currentCalls, existingCalls := 0, 0
			err := validateAttachmentForRun(testCase.sourcesSaved, func() error {
				currentCalls++
				return nil
			}, func() error {
				existingCalls++
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if currentCalls != testCase.wantCurrentCalls || existingCalls != testCase.wantExistingCalls {
				t.Fatalf("verifier calls = current %d existing %d", currentCalls, existingCalls)
			}
		})
	}
	want := errors.New("verification failed")
	if err := validateAttachmentForRun(1, func() error { return want }, func() error { return nil }); !errors.Is(err, want) {
		t.Fatal("current-run verification error was not preserved")
	}
}

// TestLiveStoredAttachmentSignedDownload is a read-only hosted Storage check.
// It resolves one owned stored attachment internally, signs it for five minutes,
// and downloads only a bounded byte range without logging its IDs or object path.
func TestLiveStoredAttachmentSignedDownload(t *testing.T) {
	if strings.TrimSpace(os.Getenv(liveTransactionsStorageE2EFlag)) != "1" {
		t.Skip(liveTransactionsStorageE2EFlag + "=1 is required for the live Storage test")
	}
	cfg, err := config.LoadFromEnv()
	if err != nil {
		t.Fatal("live Storage configuration validation failed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool, err := database.OpenTransactionPooler(ctx, cfg.SupabaseDBURL.String())
	if err != nil {
		t.Fatal("live Storage database connection failed")
	}
	defer pool.Close()
	selection, err := selectLivePipelineUser(
		ctx,
		postgresLiveUserCandidateReader{pool: pool},
		cfg.Environment,
		cfg.GoogleTestRefreshToken,
	)
	if err != nil {
		t.Fatal("live Storage owner preflight failed")
	}
	httpClient := &http.Client{Timeout: cfg.OutboundHTTPTimeout}
	storage, err := attachmentstorage.New(httpClient, cfg.SupabaseURL, cfg.SupabaseServiceRoleKey)
	if err != nil {
		t.Fatal("live Storage client setup failed")
	}
	err = validateOneOwnedStoredAttachment(
		ctx, pool, transactionstore.New(pool), storage, httpClient, cfg.SupabaseURL, selection.userID,
	)
	if err != nil {
		t.Fatalf("live Storage signed download verification failed: %s", classifyLiveStorageVerificationError(err))
	}
}

// TestLiveTransactionsPipeline deliberately mutates hosted state by starting
// one ordinary, idempotent user sync. It never deletes sources, resets Gmail
// cursors, logs identifiers, or prints provider content/tokens. Source jobs are
// claimed only through scopedJobStore above.
func TestLiveTransactionsPipeline(t *testing.T) {
	if strings.TrimSpace(os.Getenv(liveTransactionsE2EFlag)) != "1" {
		t.Skip(liveTransactionsE2EFlag + "=1 is required for the live Transactions pipeline test")
	}

	cfg, err := config.LoadFromEnv()
	if err != nil {
		t.Fatal("live Transactions configuration validation failed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	pool, err := database.OpenTransactionPooler(ctx, cfg.SupabaseDBURL.String())
	if err != nil {
		t.Fatal("live Transactions database connection failed")
	}
	defer pool.Close()
	selection, err := selectLivePipelineUser(
		ctx,
		postgresLiveUserCandidateReader{pool: pool},
		cfg.Environment,
		cfg.GoogleTestRefreshToken,
	)
	if err != nil {
		t.Fatal("live Transactions Gmail connection preflight failed")
	}
	userID := selection.userID

	providerHTTPClient := &http.Client{Timeout: cfg.OutboundHTTPTimeout}
	storageHTTPClient := &http.Client{Timeout: cfg.OutboundHTTPTimeout}
	oauthClient, err := providers.NewGoogleOAuthClient(providerHTTPClient, cfg.GoogleOAuthClientID, cfg.GoogleOAuthClientSecret)
	if err != nil {
		t.Fatal("live Transactions Google OAuth client setup failed")
	}
	gmailClient, err := providers.NewGmailHTTPClient(providerHTTPClient)
	if err != nil {
		t.Fatal("live Transactions Gmail client setup failed")
	}
	attachmentClient, err := attachmentstorage.New(storageHTTPClient, cfg.SupabaseURL, cfg.SupabaseServiceRoleKey)
	if err != nil {
		t.Fatal("live Transactions attachment Storage client setup failed")
	}
	qwenClient, err := providers.NewAlibabaQwenClient(providerHTTPClient, cfg.AlibabaBaseURL, cfg.AlibabaTokenPlanAPIKey, cfg.AlibabaModel)
	if err != nil {
		t.Fatal("live Transactions parser client setup failed")
	}
	cipher, err := secret.New(cfg.TokenEncryptionKey)
	if err != nil {
		t.Fatal("live Transactions token cipher setup failed")
	}

	store := transactionstore.New(pool)
	run, err := startOrResumeLiveSyncRun(ctx, pool, store, userID, selection.allowDevelopmentToken)
	if err != nil {
		t.Fatalf("live Transactions sync run could not be started: %s", classifyLiveRuntimeError(err))
	}
	scopedStore := &scopedJobStore{pool: pool, delegate: store, userID: userID, runID: run.ID}
	gmailHandler := ingestion.GmailIngestionHandler{
		Repository: store, Gmail: gmailClient, Tokens: oauthClient, Cipher: cipher,
		Attachments: attachmentClient, Label: cfg.GmailSyncLabel,
		InitialBackfillMax: cfg.InitialBackfillMax,
	}
	if selection.allowDevelopmentToken {
		gmailHandler.DevelopmentRefreshToken = cfg.GoogleTestRefreshToken
	}
	processingHandler := transactionworker.Handler{Repository: store, Parser: qwenClient, Attachments: attachmentClient}
	worker := jobs.Worker{
		Store: scopedStore, WorkerID: "transactions-live-e2e-" + uuid.NewString(),
		Handler: jobs.Router{
			jobs.KindGmailIngest: gmailHandler,
			jobs.KindSourceParse: processingHandler,
			jobs.KindReconcile:   processingHandler,
		},
	}

	completed, err := drainRun(ctx, store, worker, userID, run.ID)
	if err != nil {
		t.Fatalf("live Transactions scoped worker did not drain the sync run: %s", classifyLiveRuntimeError(err))
	}
	snapshot, err := loadRunSnapshot(ctx, pool, userID, run.ID)
	if err != nil {
		t.Fatal("live Transactions run verification query failed")
	}
	if err = validateCompletedRun(completed, snapshot); err != nil {
		t.Fatal("live Transactions sync run failed coherence checks")
	}
	err = validateAttachmentForRun(completed.SourcesSavedCount, func() error {
		return validateOneSignedAttachment(ctx, pool, store, attachmentClient, storageHTTPClient, cfg.SupabaseURL, userID, run.ID)
	}, func() error {
		return validateOneOwnedStoredAttachment(ctx, pool, store, attachmentClient, storageHTTPClient, cfg.SupabaseURL, userID)
	})
	if err != nil {
		t.Fatal("live Transactions attachment URL verification failed")
	}
}

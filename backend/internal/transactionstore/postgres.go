// Package transactionstore persists the operational Transactions workflow.
package transactionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zhengteck/wealth-builder/backend/internal/gmailconnection"
	"github.com/zhengteck/wealth-builder/backend/internal/jobs"
)

var (
	ErrGmailConnectionRequired = errors.New("active Gmail connection is required")
	ErrSyncRunInProgress       = errors.New("a Gmail sync is already in progress")
)

type Store struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

type SyncRun struct {
	ID                       uuid.UUID  `json:"id"`
	Status                   string     `json:"status"`
	StartedAt                *time.Time `json:"started_at"`
	CompletedAt              *time.Time `json:"completed_at"`
	IngestionCompletedAt     *time.Time `json:"ingestion_completed_at"`
	MessagesFoundCount       int        `json:"messages_found_count"`
	SourcesSavedCount        int        `json:"sources_saved_count"`
	SourcesParsedCount       int        `json:"sources_parsed_count"`
	SourcesFailedCount       int        `json:"sources_failed_count"`
	TransactionsCreatedCount int        `json:"transactions_created_count"`
	SourcesLinkedCount       int        `json:"sources_linked_count"`
	DanglingSourcesCount     int        `json:"dangling_sources_count"`
	ReviewRequiredCount      int        `json:"review_required_count"`
	ErrorSummary             *string    `json:"error_summary"`
	CreatedAt                time.Time  `json:"created_at"`
}

type SourceSummary struct {
	ID                        uuid.UUID  `json:"id"`
	SourceType                string     `json:"source_type"`
	Provider                  string     `json:"provider"`
	ReceivedAt                time.Time  `json:"received_at"`
	ParseStatus               string     `json:"parse_status"`
	ParseConfidence           *int16     `json:"parse_confidence"`
	Subject                   string     `json:"subject"`
	Sender                    string     `json:"sender"`
	ParseError                *string    `json:"parse_error"`
	ReconciliationReason      *string    `json:"reconciliation_reason"`
	SuggestedTitle            *string    `json:"suggested_title"`
	SuggestedAmountMinor      *int64     `json:"-"`
	SuggestedCurrency         *string    `json:"suggested_currency"`
	SuggestedAccountID        *uuid.UUID `json:"suggested_account_id"`
	SuggestedAccountName      *string    `json:"suggested_account_name"`
	SuggestedTransactionID    *uuid.UUID `json:"suggested_transaction_id"`
	SuggestedCategoryLeafName *string    `json:"suggested_category_leaf_name"`
	CreatedAt                 time.Time  `json:"created_at"`
}

type SanitizedEmail struct {
	ID         uuid.UUID `json:"id"`
	Subject    string    `json:"subject"`
	ReceivedAt time.Time `json:"received_at"`
	HTML       string    `json:"html"`
	Text       string    `json:"text"`
}

type GmailConnection struct {
	ID                    uuid.UUID
	EncryptedRefreshToken []byte
	SelectedLabel         string
	SyncCursor            *string
}

type IngestedSource struct {
	UserID            uuid.UUID
	SyncRunID         uuid.UUID
	ProviderMessageID string
	ProviderThreadID  string
	ReceivedAt        time.Time
	RawData           json.RawMessage
}

func (s *Store) CreateSyncRun(ctx context.Context, userID uuid.UUID, allowDevelopmentToken bool) (SyncRun, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return SyncRun{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = lockTransactionUser(ctx, tx, userID); err != nil {
		return SyncRun{}, err
	}
	var activeRun bool
	if err = tx.QueryRow(ctx, `select exists(select 1 from public.transaction_sync_runs where user_id = $1 and status in ('queued', 'running'))`, userID).Scan(&activeRun); err != nil {
		return SyncRun{}, err
	}
	if activeRun {
		return SyncRun{}, ErrSyncRunInProgress
	}

	var selectedConnectionID uuid.UUID
	err = tx.QueryRow(ctx, `select id from private.gmail_connections where user_id = $1 and provider = 'gmail' and status = 'active'`, userID).Scan(&selectedConnectionID)
	var connectionID *uuid.UUID
	if errors.Is(err, pgx.ErrNoRows) {
		if !allowDevelopmentToken {
			return SyncRun{}, ErrGmailConnectionRequired
		}
	} else if err != nil {
		return SyncRun{}, err
	}
	if err == nil {
		connectionID = &selectedConnectionID
	}

	var run SyncRun
	err = tx.QueryRow(ctx, `
		insert into public.transaction_sync_runs (user_id, gmail_connection_id)
		values ($1, $2)
			returning id, status, started_at, completed_at, ingestion_completed_at, messages_found_count, sources_saved_count,
				sources_parsed_count, sources_failed_count,
				transactions_created_count, sources_linked_count, dangling_sources_count,
				review_required_count, error_summary, created_at`, userID, connectionID).Scan(syncRunFields(&run)...)
	if err != nil {
		var postgresError *pgconn.PgError
		if errors.As(err, &postgresError) && postgresError.Code == "23505" && postgresError.ConstraintName == "transaction_sync_runs_one_active_per_user_idx" {
			return SyncRun{}, ErrSyncRunInProgress
		}
		return SyncRun{}, err
	}
	payload, err := json.Marshal(map[string]string{"sync_run_id": run.ID.String()})
	if err != nil {
		return SyncRun{}, err
	}
	_, err = tx.Exec(ctx, `
		insert into private.transaction_jobs (user_id, sync_run_id, job_type, payload)
		values ($1, $2, 'gmail_ingestion', $3::jsonb)`, userID, run.ID, string(payload))
	if err != nil {
		return SyncRun{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return SyncRun{}, err
	}
	return run, nil
}

func (s *Store) GetSyncRun(ctx context.Context, userID, runID uuid.UUID) (SyncRun, error) {
	var run SyncRun
	err := s.pool.QueryRow(ctx, `
			select id, status, started_at, completed_at, ingestion_completed_at, messages_found_count, sources_saved_count,
				sources_parsed_count, sources_failed_count,
				transactions_created_count, sources_linked_count, dangling_sources_count,
			review_required_count, error_summary, created_at
		from public.transaction_sync_runs where id = $1 and user_id = $2`, runID, userID).Scan(syncRunFields(&run)...)
	return run, err
}

func (s *Store) GetLatestSyncRun(ctx context.Context, userID uuid.UUID) (SyncRun, error) {
	var run SyncRun
	err := s.pool.QueryRow(ctx, `
		select id, status, started_at, completed_at, ingestion_completed_at, messages_found_count, sources_saved_count,
			sources_parsed_count, sources_failed_count, transactions_created_count, sources_linked_count,
			dangling_sources_count, review_required_count, error_summary, created_at
		from public.transaction_sync_runs
		where user_id = $1
		order by created_at desc, id desc
		limit 1`, userID).Scan(syncRunFields(&run)...)
	return run, err
}

func syncRunFields(run *SyncRun) []any {
	return []any{&run.ID, &run.Status, &run.StartedAt, &run.CompletedAt, &run.IngestionCompletedAt,
		&run.MessagesFoundCount, &run.SourcesSavedCount, &run.SourcesParsedCount, &run.SourcesFailedCount,
		&run.TransactionsCreatedCount, &run.SourcesLinkedCount,
		&run.DanglingSourcesCount, &run.ReviewRequiredCount, &run.ErrorSummary, &run.CreatedAt}
}

func (s *Store) ListSources(ctx context.Context, userID uuid.UUID, parseStatus string) ([]SourceSummary, error) {
	query := `
		select id, source_type, provider, received_at, parse_status, parse_confidence,
			coalesce(raw_data ->> 'subject', ''), coalesce(raw_data ->> 'sender', ''), parse_error, created_at
		from private.data_sources where user_id = $1`
	args := []any{userID}
	if parseStatus != "" {
		query += " and parse_status = $2"
		args = append(args, parseStatus)
	}
	query += " order by received_at desc limit 100"
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sources := make([]SourceSummary, 0)
	for rows.Next() {
		var source SourceSummary
		if err := rows.Scan(&source.ID, &source.SourceType, &source.Provider, &source.ReceivedAt, &source.ParseStatus, &source.ParseConfidence, &source.Subject, &source.Sender, &source.ParseError, &source.CreatedAt); err != nil {
			return nil, err
		}
		sources = append(sources, source)
	}
	return sources, rows.Err()
}

func (s *Store) GetSanitizedEmail(ctx context.Context, userID, sourceID uuid.UUID) (SanitizedEmail, error) {
	var email SanitizedEmail
	err := s.pool.QueryRow(ctx, `
		select id, coalesce(raw_data ->> 'subject', ''), received_at,
			coalesce(raw_data ->> 'html_sanitized', ''), coalesce(raw_data ->> 'text', '')
		from private.data_sources
		where id = $1 and user_id = $2 and source_type = 'gmail_email'`, sourceID, userID).Scan(&email.ID, &email.Subject, &email.ReceivedAt, &email.HTML, &email.Text)
	return email, err
}

func (s *Store) GetGmailConnection(ctx context.Context, userID uuid.UUID) (GmailConnection, error) {
	var connection GmailConnection
	err := s.pool.QueryRow(ctx, `
		select id, encrypted_refresh_token, selected_label, sync_cursor
		from private.gmail_connections
		where user_id = $1 and provider = 'gmail' and status = 'active'`, userID).Scan(&connection.ID, &connection.EncryptedRefreshToken, &connection.SelectedLabel, &connection.SyncCursor)
	if errors.Is(err, pgx.ErrNoRows) {
		return GmailConnection{}, ErrGmailConnectionRequired
	}
	return connection, err
}

func (s *Store) UpsertGmailConnection(ctx context.Context, userID uuid.UUID, encryptedToken []byte, metadata json.RawMessage, label string) error {
	_, err := s.pool.Exec(ctx, `
		insert into private.gmail_connections (user_id, encrypted_refresh_token, token_metadata, selected_label, status)
		values ($1, $2, $3::jsonb, $4, 'active')
			on conflict (user_id, provider) do update set
				encrypted_refresh_token = excluded.encrypted_refresh_token,
				token_metadata = excluded.token_metadata,
				selected_label = excluded.selected_label,
				sync_cursor = null,
				last_synced_at = null,
				status = 'active', last_error = null`, userID, encryptedToken, string(metadata), label)
	return err
}

func (s *Store) SaveOAuthState(ctx context.Context, userID uuid.UUID, digest, encryptedVerifier []byte, expiresAt time.Time) error {
	_, err := s.pool.Exec(ctx, `
		insert into private.gmail_oauth_states (user_id, state_digest, encrypted_pkce_verifier, expires_at)
		values ($1, $2, $3, $4)`, userID, digest, encryptedVerifier, expiresAt)
	return err
}

// ConsumeOAuthState uses one update as the replay-safe, expiry-safe state transition.
func (s *Store) ConsumeOAuthState(ctx context.Context, digest []byte, now time.Time) (gmailconnection.OAuthState, error) {
	var state gmailconnection.OAuthState
	err := s.pool.QueryRow(ctx, `
		update private.gmail_oauth_states set consumed_at = $2
		where state_digest = $1 and consumed_at is null and expires_at > $2
		returning user_id, encrypted_pkce_verifier`, digest, now).Scan(&state.UserID, &state.EncryptedVerifier)
	return state, err
}

func (s *Store) StoreIngestedSource(ctx context.Context, source IngestedSource) (uuid.UUID, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = lockTransactionUser(ctx, tx, source.UserID); err != nil {
		return uuid.Nil, false, err
	}
	digest := sourceProviderIdentityDigest("gmail_email", "gmail", source.ProviderMessageID)
	var permanentlyDeleted bool
	if err = tx.QueryRow(ctx, `
		select exists(
			select 1 from private.deleted_provider_messages
			where user_id = $1 and source_type = 'gmail_email' and provider = 'gmail'
				and provider_message_digest = $2
		)`, source.UserID, digest[:]).Scan(&permanentlyDeleted); err != nil {
		return uuid.Nil, false, err
	}
	if permanentlyDeleted {
		return uuid.Nil, false, ErrSourcePermanentlyDeleted
	}
	var id uuid.UUID
	err = tx.QueryRow(ctx, `
			insert into private.data_sources (
				user_id, source_type, provider, provider_message_id, provider_thread_id, received_at, raw_data
		) values ($1, 'gmail_email', 'gmail', $2, nullif($3, ''), $4, $5::jsonb)
		on conflict (user_id, source_type, provider, provider_message_id) where provider_message_id is not null
		do nothing returning id`, source.UserID, source.ProviderMessageID, source.ProviderThreadID, source.ReceivedAt, string(source.RawData)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		if err = tx.Commit(ctx); err != nil {
			return uuid.Nil, false, err
		}
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, err
	}
	command, err := tx.Exec(ctx, `
		update public.transaction_sync_runs
		set sources_saved_count = sources_saved_count + 1
		where id = $1 and user_id = $2 and status in ('queued', 'running')`,
		source.SyncRunID, source.UserID)
	if err != nil {
		return uuid.Nil, false, err
	}
	if command.RowsAffected() != 1 {
		return uuid.Nil, false, errors.New("active transaction sync run not found")
	}
	if err = tx.Commit(ctx); err != nil {
		return uuid.Nil, false, err
	}
	return id, true, nil
}

// EnqueueSourceParse schedules parsing only after source and attachment
// persistence has completed. Locking the source makes concurrent retries
// idempotent without relying on session state.
func (s *Store) EnqueueSourceParse(ctx context.Context, userID, syncRunID, sourceID uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = enqueueSourceParseTx(ctx, tx, userID, &syncRunID, sourceID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func enqueueSourceParseTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, syncRunID *uuid.UUID, sourceID uuid.UUID) error {
	var status string
	if err := tx.QueryRow(ctx, `select parse_status from private.data_sources where id = $1 and user_id = $2 for update`, sourceID, userID).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrSourceNotFound
		}
		return err
	}
	if status != "pending" {
		return nil
	}
	var alreadyQueued bool
	if err := tx.QueryRow(ctx, `
		select exists(
			select 1 from private.transaction_jobs
			where user_id = $1 and data_source_id = $2 and job_type = $3
				and status in ('queued', 'running')
		)`, userID, sourceID, string(jobs.KindSourceParse)).Scan(&alreadyQueued); err != nil {
		return err
	}
	if alreadyQueued {
		return nil
	}
	payload, err := json.Marshal(map[string]string{"data_source_id": sourceID.String()})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		insert into private.transaction_jobs (user_id, sync_run_id, data_source_id, job_type, payload)
		values ($1, $2, $3, $4, $5::jsonb)`, userID, syncRunID, sourceID, string(jobs.KindSourceParse), string(payload))
	return err
}

func (s *Store) StartSyncRun(ctx context.Context, userID, runID uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `
		update public.transaction_sync_runs set status = 'running', started_at = coalesce(started_at, now()), error_summary = null
		where id = $1 and user_id = $2 and status = 'queued'`, runID, userID)
	return err
}

func (s *Store) CompleteSyncRun(ctx context.Context, userID, runID uuid.UUID, messagesFound, sourcesSaved int) error {
	_, err := s.pool.Exec(ctx, `
			update public.transaction_sync_runs set status = 'running', ingestion_completed_at = now(), completed_at = null,
				messages_found_count = $3,
				sources_saved_count = greatest(sources_saved_count, $4),
				error_summary = null
			where id = $1 and user_id = $2 and status in ('queued', 'running')`, runID, userID, messagesFound, sourcesSaved)
	return err
}

func (s *Store) RecordSyncFailure(ctx context.Context, userID, runID uuid.UUID, final bool) error {
	if final {
		_, err := s.pool.Exec(ctx, `update public.transaction_sync_runs set status = 'failed', completed_at = now(), error_summary = 'Gmail ingestion failed; retry the sync run.' where id = $1 and user_id = $2`, runID, userID)
		return err
	}
	_, err := s.pool.Exec(ctx, `update public.transaction_sync_runs set status = 'running', error_summary = 'Gmail ingestion encountered a temporary error and will retry.' where id = $1 and user_id = $2`, runID, userID)
	return err
}

func (s *Store) UpdateConnectionCursor(ctx context.Context, userID uuid.UUID, cursor string) error {
	_, err := s.pool.Exec(ctx, `update private.gmail_connections set sync_cursor = nullif($2, ''), last_synced_at = now(), last_error = null where user_id = $1 and provider = 'gmail'`, userID, cursor)
	return err
}

// Claim implements jobs.Store with a single short, non-blocking PostgreSQL statement.
func (s *Store) Claim(ctx context.Context, workerID string, now time.Time) (*jobs.Job, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = reapExpiredFinalAttempts(ctx, tx, now); err != nil {
		return nil, err
	}
	var job jobs.Job
	var payload []byte
	var lease time.Time
	err = tx.QueryRow(ctx, `
			with candidate as (
			select id from private.transaction_jobs
			where attempts < max_attempts and (
				(status = 'queued' and run_after <= $1)
				or (status = 'running' and lease_expires_at <= $1)
			)
			order by run_after, created_at
			limit 1 for update skip locked
		)
		update private.transaction_jobs job set status = 'running', attempts = job.attempts + 1,
			leased_at = $1, lease_expires_at = $1::timestamptz + interval '5 minutes', leased_by = $2
		from candidate where job.id = candidate.id
		returning job.id, job.user_id, job.sync_run_id, job.job_type, job.payload, job.attempts, job.run_after, job.lease_expires_at`, now, workerID).Scan(&job.ID, &job.UserID, &job.SyncRunID, &job.Kind, &payload, &job.Attempts, &job.Available, &lease)
	if errors.Is(err, pgx.ErrNoRows) {
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return nil, commitErr
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	job.Payload, job.LeaseUntil = payload, &lease
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &job, nil
}

// RenewLease extends an unexpired lease only while the same worker still owns
// the running job. A missing row is deliberately indistinguishable from any
// other ownership loss to prevent a stale worker from finalizing it.
func (s *Store) RenewLease(ctx context.Context, jobID uuid.UUID, workerID string, now time.Time) error {
	command, err := s.pool.Exec(ctx, `
		update private.transaction_jobs
		set lease_expires_at = greatest(lease_expires_at, $3::timestamptz + interval '5 minutes')
		where id = $1 and status = 'running' and leased_by = $2
			and lease_expires_at > $3`, jobID, workerID, now)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return jobs.ErrLeaseLost
	}
	return nil
}

func (s *Store) Complete(ctx context.Context, jobID uuid.UUID, workerID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var userID uuid.UUID
	var syncRunID *uuid.UUID
	var kind jobs.Kind
	err = tx.QueryRow(ctx, `
		select user_id, sync_run_id, job_type
		from private.transaction_jobs
		where id = $1 and status = 'running' and leased_by = $2
			and lease_expires_at > now()
		for update`, jobID, workerID).Scan(&userID, &syncRunID, &kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: job %s is not running for this worker", jobs.ErrLeaseLost, jobID)
	}
	if err != nil {
		return err
	}
	if kind == jobs.KindSourceAttachmentCleanup {
		command, deleteErr := tx.Exec(ctx, `
			delete from private.transaction_jobs
			where id = $1 and status = 'running' and leased_by = $2
				and lease_expires_at > now()`, jobID, workerID)
		if deleteErr != nil {
			return deleteErr
		}
		if command.RowsAffected() != 1 {
			return fmt.Errorf("%w: cleanup job %s is not running for this worker", jobs.ErrLeaseLost, jobID)
		}
		return tx.Commit(ctx)
	}
	err = tx.QueryRow(ctx, `
		update private.transaction_jobs
		set status = 'completed', completed_at = now(), leased_at = null,
			lease_expires_at = null, leased_by = null, last_error = null
		where id = $1 and status = 'running' and leased_by = $2 and lease_expires_at > now()
		returning user_id, sync_run_id`, jobID, workerID).Scan(&userID, &syncRunID)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: job %s is not running for this worker", jobs.ErrLeaseLost, jobID)
	}
	if err != nil {
		return err
	}
	if syncRunID != nil {
		if err = refreshSyncRunProgress(ctx, tx, userID, *syncRunID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) Retry(ctx context.Context, jobID uuid.UUID, workerID string, attempt int, retryAt time.Time, _ string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var userID uuid.UUID
	var syncRunID, sourceID *uuid.UUID
	var kind jobs.Kind
	var status string
	err = tx.QueryRow(ctx, `
			update private.transaction_jobs set
				status = case when $2 >= max_attempts then 'failed' else 'queued' end,
			completed_at = case when $2 >= max_attempts then now() else null end,
			run_after = case when $2 >= max_attempts then run_after else $3 end,
				leased_at = null, lease_expires_at = null, leased_by = null,
				last_error = 'Job failed; retry or inspect the sync run.'
			where id = $1 and status = 'running' and leased_by = $4 and lease_expires_at > now()
			returning user_id, sync_run_id, data_source_id, job_type, status`, jobID, attempt, retryAt, workerID).Scan(&userID, &syncRunID, &sourceID, &kind, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: job %s is not running for this worker", jobs.ErrLeaseLost, jobID)
	}
	if err != nil {
		return err
	}
	if status == "failed" {
		if err = markTerminalJobFailure(ctx, tx, userID, syncRunID, sourceID, kind); err != nil {
			return err
		}
	}
	if syncRunID != nil {
		if err = refreshSyncRunProgress(ctx, tx, userID, *syncRunID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

type expiredJob struct {
	UserID    uuid.UUID
	SyncRunID *uuid.UUID
	SourceID  *uuid.UUID
	Kind      jobs.Kind
}

func reapExpiredFinalAttempts(ctx context.Context, tx pgx.Tx, now time.Time) error {
	rows, err := tx.Query(ctx, `
		update private.transaction_jobs
		set status = 'failed', completed_at = $1, leased_at = null,
			lease_expires_at = null, leased_by = null,
			last_error = 'Worker lease expired on the final attempt.'
		where status = 'running' and lease_expires_at <= $1 and attempts >= max_attempts
		returning user_id, sync_run_id, data_source_id, job_type`, now)
	if err != nil {
		return err
	}
	expired := make([]expiredJob, 0)
	for rows.Next() {
		var job expiredJob
		if err = rows.Scan(&job.UserID, &job.SyncRunID, &job.SourceID, &job.Kind); err != nil {
			rows.Close()
			return err
		}
		expired = append(expired, job)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, job := range expired {
		if err = markTerminalJobFailure(ctx, tx, job.UserID, job.SyncRunID, job.SourceID, job.Kind); err != nil {
			return err
		}
		if job.SyncRunID != nil {
			if err = refreshSyncRunProgress(ctx, tx, job.UserID, *job.SyncRunID); err != nil {
				return err
			}
		}
	}
	return nil
}

func markTerminalJobFailure(ctx context.Context, tx pgx.Tx, userID uuid.UUID, syncRunID, sourceID *uuid.UUID, kind jobs.Kind) error {
	if sourceID != nil {
		message := "Source parsing failed after all retry attempts."
		if kind == jobs.KindReconcile {
			message = "Source reconciliation failed after all retry attempts."
		}
		if _, err := tx.Exec(ctx, `
			update private.data_sources
			set parse_status = 'failed', parse_error = $3
			where id = $1 and user_id = $2`, *sourceID, userID, message); err != nil {
			return err
		}
	}
	if kind == jobs.KindGmailIngest && syncRunID != nil {
		_, err := tx.Exec(ctx, `
			update public.transaction_sync_runs
			set status = 'failed', completed_at = now(),
				error_summary = 'Gmail ingestion failed after all retry attempts.'
			where id = $1 and user_id = $2 and status in ('queued', 'running')`, *syncRunID, userID)
		return err
	}
	return nil
}

func refreshSyncRunProgress(ctx context.Context, tx pgx.Tx, userID, runID uuid.UUID) error {
	var ingestionComplete bool
	var status string
	if err := tx.QueryRow(ctx, `
		select ingestion_completed_at is not null, status
		from public.transaction_sync_runs
		where id = $1 and user_id = $2
		for update`, runID, userID).Scan(&ingestionComplete, &status); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		update public.transaction_sync_runs run
		set sources_parsed_count = progress.parsed_count,
			sources_failed_count = progress.failed_count
		from (
			select
				count(distinct job.data_source_id) filter (
					where source.parse_status in ('parsed', 'review_required', 'dangling')
				)::integer as parsed_count,
				count(distinct job.data_source_id) filter (
					where source.parse_status = 'failed'
				)::integer as failed_count
			from private.transaction_jobs job
			left join private.data_sources source
				on source.id = job.data_source_id and source.user_id = job.user_id
			where job.sync_run_id = $1 and job.user_id = $2
		) progress
		where run.id = $1 and run.user_id = $2`, runID, userID)
	if err != nil {
		return err
	}
	if !ingestionComplete || status == "failed" || status == "cancelled" {
		return nil
	}
	var unfinished bool
	if err = tx.QueryRow(ctx, `
		select exists(
			select 1 from private.transaction_jobs
			where sync_run_id = $1 and user_id = $2 and status in ('queued', 'running')
		)`, runID, userID).Scan(&unfinished); err != nil {
		return err
	}
	if unfinished {
		return nil
	}
	_, err = tx.Exec(ctx, `
		update public.transaction_sync_runs
		set status = 'completed', completed_at = now(),
			error_summary = case
				when sources_failed_count > 0 then sources_failed_count::text || ' source item(s) failed. Retry them from Failed sources.'
				else null
			end
		where id = $1 and user_id = $2 and status in ('queued', 'running')`, runID, userID)
	return err
}

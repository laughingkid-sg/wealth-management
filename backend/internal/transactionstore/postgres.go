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
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zhengteck/wealth-builder/backend/internal/gmailconnection"
	"github.com/zhengteck/wealth-builder/backend/internal/jobs"
)

var ErrGmailConnectionRequired = errors.New("active Gmail connection is required")

type Store struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

type SyncRun struct {
	ID                       uuid.UUID  `json:"id"`
	Status                   string     `json:"status"`
	StartedAt                *time.Time `json:"started_at"`
	CompletedAt              *time.Time `json:"completed_at"`
	MessagesFoundCount       int        `json:"messages_found_count"`
	SourcesSavedCount        int        `json:"sources_saved_count"`
	TransactionsCreatedCount int        `json:"transactions_created_count"`
	SourcesLinkedCount       int        `json:"sources_linked_count"`
	DanglingSourcesCount     int        `json:"dangling_sources_count"`
	ReviewRequiredCount      int        `json:"review_required_count"`
	ErrorSummary             *string    `json:"error_summary"`
	CreatedAt                time.Time  `json:"created_at"`
}

type SourceSummary struct {
	ID              uuid.UUID `json:"id"`
	SourceType      string    `json:"source_type"`
	Provider        string    `json:"provider"`
	ReceivedAt      time.Time `json:"received_at"`
	ParseStatus     string    `json:"parse_status"`
	ParseConfidence *int16    `json:"parse_confidence"`
	Subject         string    `json:"subject"`
	Sender          string    `json:"sender"`
	ParseError      *string   `json:"parse_error"`
	CreatedAt       time.Time `json:"created_at"`
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
		returning id, status, started_at, completed_at, messages_found_count, sources_saved_count,
			transactions_created_count, sources_linked_count, dangling_sources_count,
			review_required_count, error_summary, created_at`, userID, connectionID).Scan(syncRunFields(&run)...)
	if err != nil {
		return SyncRun{}, err
	}
	payload, err := json.Marshal(map[string]string{"sync_run_id": run.ID.String()})
	if err != nil {
		return SyncRun{}, err
	}
	_, err = tx.Exec(ctx, `
		insert into private.transaction_jobs (user_id, sync_run_id, job_type, payload)
		values ($1, $2, 'gmail_ingestion', $3::jsonb)`, userID, run.ID, payload)
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
		select id, status, started_at, completed_at, messages_found_count, sources_saved_count,
			transactions_created_count, sources_linked_count, dangling_sources_count,
			review_required_count, error_summary, created_at
		from public.transaction_sync_runs where id = $1 and user_id = $2`, runID, userID).Scan(syncRunFields(&run)...)
	return run, err
}

func syncRunFields(run *SyncRun) []any {
	return []any{&run.ID, &run.Status, &run.StartedAt, &run.CompletedAt, &run.MessagesFoundCount,
		&run.SourcesSavedCount, &run.TransactionsCreatedCount, &run.SourcesLinkedCount,
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
			status = 'active', last_error = null`, userID, encryptedToken, metadata, label)
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
	var id uuid.UUID
	err = tx.QueryRow(ctx, `
		insert into private.data_sources (
			user_id, source_type, provider, provider_message_id, provider_thread_id, received_at, raw_data
		) values ($1, 'gmail_email', 'gmail', $2, nullif($3, ''), $4, $5::jsonb)
		on conflict (user_id, source_type, provider, provider_message_id) where provider_message_id is not null
		do nothing returning id`, source.UserID, source.ProviderMessageID, source.ProviderThreadID, source.ReceivedAt, source.RawData).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, err
	}
	payload, err := json.Marshal(map[string]string{"data_source_id": id.String()})
	if err != nil {
		return uuid.Nil, false, err
	}
	_, err = tx.Exec(ctx, `
		insert into private.transaction_jobs (user_id, sync_run_id, data_source_id, job_type, payload)
		values ($1, $2, $3, $4, $5::jsonb)`, source.UserID, source.SyncRunID, id, string(jobs.KindSourceParse), payload)
	if err != nil {
		return uuid.Nil, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return uuid.Nil, false, err
	}
	return id, true, nil
}

func (s *Store) StartSyncRun(ctx context.Context, userID, runID uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `
		update public.transaction_sync_runs set status = 'running', started_at = coalesce(started_at, now()), error_summary = null
		where id = $1 and user_id = $2 and status = 'queued'`, runID, userID)
	return err
}

func (s *Store) CompleteSyncRun(ctx context.Context, userID, runID uuid.UUID, messagesFound, sourcesSaved int) error {
	_, err := s.pool.Exec(ctx, `
		update public.transaction_sync_runs set status = 'completed', completed_at = now(),
			messages_found_count = $3, sources_saved_count = $4, error_summary = null
		where id = $1 and user_id = $2`, runID, userID, messagesFound, sourcesSaved)
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
	var job jobs.Job
	var payload []byte
	var lease time.Time
	err := s.pool.QueryRow(ctx, `
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
			leased_at = $1, lease_expires_at = $1 + interval '5 minutes', leased_by = $2
		from candidate where job.id = candidate.id
		returning job.id, job.user_id, job.sync_run_id, job.job_type, job.payload, job.attempts, job.run_after, job.lease_expires_at`, now, workerID).Scan(&job.ID, &job.UserID, &job.SyncRunID, &job.Kind, &payload, &job.Attempts, &job.Available, &lease)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	job.Payload, job.LeaseUntil = payload, &lease
	return &job, nil
}

func (s *Store) Complete(ctx context.Context, jobID uuid.UUID, workerID string) error {
	command, err := s.pool.Exec(ctx, `update private.transaction_jobs set status = 'completed', completed_at = now(), leased_at = null, lease_expires_at = null, leased_by = null, last_error = null where id = $1 and status = 'running' and leased_by = $2 and lease_expires_at > now()`, jobID, workerID)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return fmt.Errorf("job %s is not running", jobID)
	}
	return nil
}

func (s *Store) Retry(ctx context.Context, jobID uuid.UUID, workerID string, attempt int, retryAt time.Time, _ string) error {
	command, err := s.pool.Exec(ctx, `
		update private.transaction_jobs set
			status = case when $2 >= max_attempts then 'failed' else 'queued' end,
			completed_at = case when $2 >= max_attempts then now() else null end,
			run_after = case when $2 >= max_attempts then run_after else $3 end,
			leased_at = null, lease_expires_at = null, leased_by = null,
			last_error = 'Job failed; retry or inspect the sync run.'
		where id = $1 and status = 'running' and leased_by = $4 and lease_expires_at > now()`, jobID, attempt, retryAt, workerID)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return fmt.Errorf("job %s is not running", jobID)
	}
	return nil
}

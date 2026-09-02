package transactionstore

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/zhengteck/wealth-builder/backend/internal/attachmentstorage"
	"github.com/zhengteck/wealth-builder/backend/internal/jobs"
)

const (
	sourceDebugAttemptLimit        = 10
	sourceDebugFieldCharacterLimit = 16 * 1024
	storageCleanupBatchSize        = 1000
	storageCleanupMaxAttempts      = 5
)

var (
	ErrSourceDeletionIngestionActive = errors.New("Gmail ingestion is active for this source owner")
	ErrSourcePermanentlyDeleted      = errors.New("provider message was permanently deleted")
	ErrSourceDebugFieldUnsupported   = errors.New("source debug field is not supported")
	ErrSourceDebugFieldTooLarge      = errors.New("source debug field exceeds its permitted size")
)

type SourceParseDebug struct {
	SourceID  uuid.UUID           `json:"source_id"`
	Attempts  []ParseAttemptDebug `json:"attempts"`
	HasMore   bool                `json:"has_more"`
	Truncated bool                `json:"truncated"`
}

type ParseAttemptDebug struct {
	ID                    uuid.UUID       `json:"id"`
	ParserRuleID          *uuid.UUID      `json:"parser_rule_id"`
	ParserRuleVersion     *int            `json:"parser_rule_version"`
	UserParserRuleID      *uuid.UUID      `json:"user_parser_rule_id"`
	UserParserRuleVersion *int            `json:"user_parser_rule_version"`
	ModelName             *string         `json:"model_name"`
	RequestMetadata       json.RawMessage `json:"request_metadata"`
	ParsedCandidate       json.RawMessage `json:"parsed_candidate"`
	AssembledSystemPrompt *string         `json:"assembled_system_prompt"`
	NormalizedInput       *string         `json:"normalized_input"`
	// These PostgreSQL json values are returned as strings so the browser can
	// display their exact stored spelling without parsing away whitespace, key
	// order, duplicate keys, or integer precision.
	ProviderRequest  *string         `json:"provider_request"`
	ProviderResponse *string         `json:"provider_response"`
	ModelOutput      *string         `json:"model_output"`
	PromptComponents json.RawMessage `json:"prompt_components"`
	ValidationStatus string          `json:"validation_status"`
	ErrorSummary     *string         `json:"error_summary"`
	StartedAt        *time.Time      `json:"started_at"`
	CompletedAt      *time.Time      `json:"completed_at"`
	CreatedAt        time.Time       `json:"created_at"`
	TruncatedFields  []string        `json:"truncated_fields"`
}

type SourceParseAuditField struct {
	SourceID  uuid.UUID `json:"source_id"`
	AttemptID uuid.UUID `json:"attempt_id"`
	Field     string    `json:"field"`
	Value     *string   `json:"value"`
	MaxBytes  int       `json:"max_bytes"`
}

func (s *Store) GetSourceParseDebug(ctx context.Context, userID, sourceID uuid.UUID) (SourceParseDebug, error) {
	var exists bool
	if err := s.pool.QueryRow(ctx, `
		select exists(
			select 1 from private.data_sources where id = $1 and user_id = $2
		)`, sourceID, userID).Scan(&exists); err != nil {
		return SourceParseDebug{}, err
	}
	if !exists {
		return SourceParseDebug{}, ErrSourceNotFound
	}
	rows, err := s.pool.Query(ctx, `
		select id, parser_rule_id, parser_rule_version,
			user_parser_rule_id, user_parser_rule_version, model_name,
			case when char_length(request_metadata::text) > $3 then '{}'::jsonb else request_metadata end,
			case when parsed_candidate is not null and char_length(parsed_candidate::text) > $3 then null else parsed_candidate end,
			left(assembled_system_prompt, $3), left(normalized_input, $3),
			left(provider_request::text, $3), left(provider_response::text, $3),
			left(model_output::text, $3),
			case when char_length(prompt_components::text) > $3 then '{}'::jsonb else prompt_components end,
			validation_status, error_summary, started_at, completed_at, created_at,
			char_length(request_metadata::text) > $3,
			parsed_candidate is not null and char_length(parsed_candidate::text) > $3,
			char_length(coalesce(assembled_system_prompt, '')) > $3,
			char_length(coalesce(normalized_input, '')) > $3,
			char_length(coalesce(provider_request::text, '')) > $3,
			char_length(coalesce(provider_response::text, '')) > $3,
			char_length(coalesce(model_output::text, '')) > $3,
			char_length(prompt_components::text) > $3
		from private.source_parse_attempts
		where data_source_id = $1 and user_id = $2
		order by created_at desc, id desc
		limit $4`, sourceID, userID, sourceDebugFieldCharacterLimit, sourceDebugAttemptLimit+1)
	if err != nil {
		return SourceParseDebug{}, err
	}
	defer rows.Close()
	result := SourceParseDebug{SourceID: sourceID, Attempts: []ParseAttemptDebug{}}
	for rows.Next() {
		if len(result.Attempts) == sourceDebugAttemptLimit {
			result.HasMore = true
			result.Truncated = true
			break
		}
		var attempt ParseAttemptDebug
		var requestMetadataTruncated, parsedCandidateTruncated bool
		var systemPromptTruncated, normalizedInputTruncated bool
		var providerRequestTruncated, providerResponseTruncated, modelOutputTruncated bool
		var promptComponentsTruncated bool
		if err = rows.Scan(
			&attempt.ID, &attempt.ParserRuleID, &attempt.ParserRuleVersion,
			&attempt.UserParserRuleID, &attempt.UserParserRuleVersion,
			&attempt.ModelName, &attempt.RequestMetadata, &attempt.ParsedCandidate,
			&attempt.AssembledSystemPrompt, &attempt.NormalizedInput,
			&attempt.ProviderRequest, &attempt.ProviderResponse,
			&attempt.ModelOutput, &attempt.PromptComponents,
			&attempt.ValidationStatus, &attempt.ErrorSummary,
			&attempt.StartedAt, &attempt.CompletedAt, &attempt.CreatedAt,
			&requestMetadataTruncated, &parsedCandidateTruncated,
			&systemPromptTruncated, &normalizedInputTruncated,
			&providerRequestTruncated, &providerResponseTruncated,
			&modelOutputTruncated, &promptComponentsTruncated,
		); err != nil {
			return SourceParseDebug{}, err
		}
		attempt.TruncatedFields = make([]string, 0)
		for _, field := range []struct {
			name      string
			truncated bool
		}{
			{name: "request_metadata", truncated: requestMetadataTruncated},
			{name: "parsed_candidate", truncated: parsedCandidateTruncated},
			{name: "assembled_system_prompt", truncated: systemPromptTruncated},
			{name: "normalized_input", truncated: normalizedInputTruncated},
			{name: "provider_request", truncated: providerRequestTruncated},
			{name: "provider_response", truncated: providerResponseTruncated},
			{name: "model_output", truncated: modelOutputTruncated},
			{name: "prompt_components", truncated: promptComponentsTruncated},
		} {
			if field.truncated {
				attempt.TruncatedFields = append(attempt.TruncatedFields, field.name)
				result.Truncated = true
			}
		}
		result.Attempts = append(result.Attempts, attempt)
	}
	return result, rows.Err()
}

func (s *Store) GetSourceParseAuditField(
	ctx context.Context,
	userID, sourceID, attemptID uuid.UUID,
	field string,
) (SourceParseAuditField, error) {
	expression, maxBytes, ok := sourceParseAuditFieldSpec(field)
	if !ok {
		return SourceParseAuditField{}, ErrSourceDebugFieldUnsupported
	}
	query := fmt.Sprintf(`
		select case when octet_length(%[1]s) <= $4 then %[1]s else null end,
			%[1]s is null, coalesce(octet_length(%[1]s), 0)
		from private.source_parse_attempts attempt
		where attempt.user_id = $1 and attempt.data_source_id = $2 and attempt.id = $3`, expression)
	var value *string
	var isNull bool
	var byteSize int
	err := s.pool.QueryRow(ctx, query, userID, sourceID, attemptID, maxBytes).Scan(&value, &isNull, &byteSize)
	if errors.Is(err, pgx.ErrNoRows) {
		return SourceParseAuditField{}, ErrSourceNotFound
	}
	if err != nil {
		return SourceParseAuditField{}, err
	}
	if byteSize > maxBytes {
		return SourceParseAuditField{}, ErrSourceDebugFieldTooLarge
	}
	if isNull {
		value = nil
	}
	return SourceParseAuditField{
		SourceID: sourceID, AttemptID: attemptID, Field: field, Value: value, MaxBytes: maxBytes,
	}, nil
}

func sourceParseAuditFieldSpec(field string) (expression string, maxBytes int, ok bool) {
	switch field {
	case "request_metadata":
		return "attempt.request_metadata::text", 65536, true
	case "parsed_candidate":
		return "attempt.parsed_candidate::text", 2097152, true
	case "assembled_system_prompt":
		return "attempt.assembled_system_prompt", 65536, true
	case "normalized_input":
		return "attempt.normalized_input", 262144, true
	case "provider_request":
		return "attempt.provider_request::text", 10485760, true
	case "provider_response":
		return "attempt.provider_response::text", 2097152, true
	case "model_output":
		return "attempt.model_output::text", 2097152, true
	case "prompt_components":
		return "attempt.prompt_components::text", 65536, true
	default:
		return "", 0, false
	}
}

type SourceDeletionResult struct {
	Status         string `json:"status"`
	CleanupPending bool   `json:"cleanup_pending"`
}

// StageSourceDeletion atomically removes a raw source and its database
// dependants while inserting durable Storage-cleanup work. No network call is
// made before this transaction commits.
func (s *Store) StageSourceDeletion(ctx context.Context, userID, sourceID uuid.UUID) (SourceDeletionResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return SourceDeletionResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = lockTransactionUser(ctx, tx, userID); err != nil {
		return SourceDeletionResult{}, err
	}
	var activeGmailIngestion bool
	if err = tx.QueryRow(ctx, `
		select exists(
			select 1 from private.transaction_jobs
			where user_id = $1 and job_type = 'gmail_ingestion'
				and status in ('queued', 'running')
		)`, userID).Scan(&activeGmailIngestion); err != nil {
		return SourceDeletionResult{}, err
	}
	if activeGmailIngestion {
		return SourceDeletionResult{}, ErrSourceDeletionIngestionActive
	}

	var sourceType, provider string
	var providerMessageID *string
	var raw []byte
	err = tx.QueryRow(ctx, `
		select source_type, provider, provider_message_id, raw_data
		from private.data_sources
		where id = $1 and user_id = $2
		for update`, sourceID, userID).Scan(&sourceType, &provider, &providerMessageID, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return SourceDeletionResult{}, ErrSourceNotFound
	}
	if err != nil {
		return SourceDeletionResult{}, err
	}

	objectPaths, err := collectSourceObjectPaths(ctx, tx, userID, sourceID, raw)
	if err != nil {
		return SourceDeletionResult{}, err
	}

	rows, err := tx.Query(ctx, `
		select distinct transaction_id
		from private.transaction_data_sources
		where data_source_id = $1 and user_id = $2
		order by transaction_id`, sourceID, userID)
	if err != nil {
		return SourceDeletionResult{}, err
	}
	affectedTransactions := make([]uuid.UUID, 0)
	for rows.Next() {
		var transactionID uuid.UUID
		if err = rows.Scan(&transactionID); err != nil {
			rows.Close()
			return SourceDeletionResult{}, err
		}
		affectedTransactions = append(affectedTransactions, transactionID)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return SourceDeletionResult{}, err
	}
	rows.Close()

	syncRunIDs, err := sourceJobSyncRunIDs(ctx, tx, userID, sourceID)
	if err != nil {
		return SourceDeletionResult{}, err
	}

	removableTransactions := make([]uuid.UUID, 0, len(affectedTransactions))
	for _, transactionID := range affectedTransactions {
		var removable bool
		err = tx.QueryRow(ctx, `
			select transaction.creation_method = 'automatic_source'
				and transaction.user_modified_at is null
				and not exists (
					select 1 from private.transaction_data_sources evidence
					where evidence.user_id = transaction.user_id
						and evidence.transaction_id = transaction.id
						and evidence.data_source_id <> $3
						and evidence.detached_at is null
				)
				and not exists (
					select 1 from private.transaction_links link
					where link.user_id = transaction.user_id
						and transaction.id in (link.debit_transaction_id, link.credit_transaction_id)
				)
			from public.transactions transaction
			where transaction.id = $1 and transaction.user_id = $2
			for update`, transactionID, userID, sourceID).Scan(&removable)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return SourceDeletionResult{}, err
		}
		if removable {
			removableTransactions = append(removableTransactions, transactionID)
		}
	}

	if providerMessageID != nil && strings.TrimSpace(*providerMessageID) != "" {
		digest := sourceProviderIdentityDigest(sourceType, provider, *providerMessageID)
		if _, err = tx.Exec(ctx, `
			insert into private.deleted_provider_messages
				(user_id, source_type, provider, provider_message_digest)
			values ($1, $2, $3, $4)
			on conflict (user_id, source_type, provider, provider_message_digest) do nothing`,
			userID, sourceType, provider, digest[:]); err != nil {
			return SourceDeletionResult{}, err
		}
	}

	// Mark leased work cancelled before the source cascade removes it. A worker
	// that was between external I/O and persistence then loses its lease and
	// cannot recreate or finalize data for the deleted source.
	if _, err = tx.Exec(ctx, `
		update private.transaction_jobs
		set status = 'cancelled', completed_at = now(), leased_at = null,
			lease_expires_at = null, leased_by = null,
			last_error = 'Source was permanently deleted.'
		where data_source_id = $1 and user_id = $2
			and status in ('queued', 'running')`, sourceID, userID); err != nil {
		return SourceDeletionResult{}, err
	}

	for _, transactionID := range removableTransactions {
		// Suggestions intentionally use a restrictive FK. Clear owner-scoped
		// suggestions before deleting their target transaction.
		if _, err = tx.Exec(ctx, `
			update private.data_sources
			set suggested_transaction_id = null, reconciliation_reason = null
			where user_id = $1 and suggested_transaction_id = $2`, userID, transactionID); err != nil {
			return SourceDeletionResult{}, err
		}
	}

	if _, err = tx.Exec(ctx, `delete from private.data_sources where id = $1 and user_id = $2`, sourceID, userID); err != nil {
		return SourceDeletionResult{}, err
	}

	for _, transactionID := range removableTransactions {
		if _, err = tx.Exec(ctx, `
			delete from public.transactions
			where id = $1 and user_id = $2
				and creation_method = 'automatic_source'
				and user_modified_at is null
				and not exists (
					select 1 from private.transaction_data_sources evidence
					where evidence.user_id = $2
						and evidence.transaction_id = $1
						and evidence.detached_at is null
				)
				and not exists (
					select 1 from private.transaction_links link
					where link.user_id = $2
						and $1 in (link.debit_transaction_id, link.credit_transaction_id)
				)`, transactionID, userID); err != nil {
			return SourceDeletionResult{}, err
		}
	}

	for start := 0; start < len(objectPaths); start += storageCleanupBatchSize {
		end := min(start+storageCleanupBatchSize, len(objectPaths))
		payload, marshalErr := json.Marshal(jobs.SourceAttachmentCleanupPayload{
			SourceID: sourceID.String(), ObjectPaths: objectPaths[start:end],
		})
		if marshalErr != nil {
			return SourceDeletionResult{}, fmt.Errorf("encode source attachment cleanup: %w", marshalErr)
		}
		if _, err = tx.Exec(ctx, `
			insert into private.transaction_jobs
				(user_id, job_type, payload, max_attempts)
			values ($1, $2, $3::jsonb, $4)`, userID, string(jobs.KindSourceAttachmentCleanup), string(payload), storageCleanupMaxAttempts); err != nil {
			return SourceDeletionResult{}, err
		}
	}

	for _, syncRunID := range syncRunIDs {
		if err = refreshSyncRunProgress(ctx, tx, userID, syncRunID); err != nil {
			return SourceDeletionResult{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return SourceDeletionResult{}, err
	}
	if len(objectPaths) > 0 {
		return SourceDeletionResult{Status: "cleanup_pending", CleanupPending: true}, nil
	}
	return SourceDeletionResult{Status: "completed", CleanupPending: false}, nil
}

func lockTransactionUser(ctx context.Context, tx pgx.Tx, userID uuid.UUID) error {
	if _, err := tx.Exec(ctx, `
		insert into private.transaction_user_locks (user_id)
		values ($1) on conflict (user_id) do nothing`, userID); err != nil {
		return err
	}
	var lockedUserID uuid.UUID
	return tx.QueryRow(ctx, `
		select user_id from private.transaction_user_locks
		where user_id = $1 for update`, userID).Scan(&lockedUserID)
}

func collectSourceObjectPaths(ctx context.Context, tx pgx.Tx, userID, sourceID uuid.UUID, raw []byte) ([]string, error) {
	paths := make(map[string]struct{})
	for _, attachment := range sourceAttachmentMetadata(raw) {
		path := strings.TrimSpace(attachment.ObjectPath)
		request := attachmentstorage.ObjectRequest{UserID: userID, SourceID: sourceID, ObjectPath: path}
		if attachment.StorageStatus == "stored" && attachmentstorage.ValidateObjectRequest(request) == nil {
			paths[path] = struct{}{}
		}
	}
	prefix := userID.String() + "/" + sourceID.String() + "/"
	rows, err := tx.Query(ctx, `
		select name from storage.objects
		where bucket_id = $1 and name like $2`, attachmentstorage.Bucket, prefix+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var path string
		if err = rows.Scan(&path); err != nil {
			return nil, err
		}
		request := attachmentstorage.ObjectRequest{UserID: userID, SourceID: sourceID, ObjectPath: path}
		if attachmentstorage.ValidateObjectRequest(request) == nil {
			paths[path] = struct{}{}
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	result := make([]string, 0, len(paths))
	for path := range paths {
		result = append(result, path)
	}
	sort.Strings(result)
	return result, nil
}

func sourceJobSyncRunIDs(ctx context.Context, tx pgx.Tx, userID, sourceID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := tx.Query(ctx, `
		select distinct sync_run_id from private.transaction_jobs
		where user_id = $1 and data_source_id = $2 and sync_run_id is not null
		order by sync_run_id`, userID, sourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]uuid.UUID, 0)
	for rows.Next() {
		var runID uuid.UUID
		if err = rows.Scan(&runID); err != nil {
			return nil, err
		}
		result = append(result, runID)
	}
	return result, rows.Err()
}

func sourceProviderIdentityDigest(sourceType, provider, providerMessageID string) [sha256.Size]byte {
	return sha256.Sum256([]byte(strings.Join([]string{
		"wealth-builder-deleted-provider-message-v1", sourceType, provider, providerMessageID,
	}, "\x00")))
}

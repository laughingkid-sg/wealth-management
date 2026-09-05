package bulkstore

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/zhengteck/wealth-builder/backend/internal/attachmentstorage"
	"github.com/zhengteck/wealth-builder/backend/internal/bulkimport"
	"github.com/zhengteck/wealth-builder/backend/internal/bulkparse"
	"github.com/zhengteck/wealth-builder/backend/internal/bulkprompt"
	"github.com/zhengteck/wealth-builder/backend/internal/bulkworker"
	"github.com/zhengteck/wealth-builder/backend/internal/jobs"
)

const (
	maxChunkAuditRequestMetadataBytes  = 64 * 1024
	maxChunkAuditSystemPromptBytes     = 64 * 1024
	maxChunkAuditNormalizedInputBytes  = 256 * 1024
	maxChunkAuditProviderRequestBytes  = 10 * 1024 * 1024
	maxChunkAuditProviderResponseBytes = 2 * 1024 * 1024
	maxChunkAuditModelOutputBytes      = 2 * 1024 * 1024
	maxChunkAuditPromptComponentsBytes = 64 * 1024
)

type storedPage struct {
	ManifestPath  string    `json:"manifest_path"`
	ObjectPath    string    `json:"object_path"`
	Filename      string    `json:"filename"`
	MIMEType      string    `json:"mime_type"`
	SourceScopeID uuid.UUID `json:"source_scope_id"`
}

type storedManifest struct {
	Pages []storedPage `json:"pages"`
}

const claimBulkChunkSQL = `update private.bulk_import_chunks c set status='parsing',started_at=coalesce(c.started_at,now()),completed_at=null,error_summary=null from private.bulk_import_documents d join public.bulk_import_batches b on b.id=d.batch_id and b.user_id=d.user_id where c.id=$1 and c.document_id=$2 and c.batch_id=$3 and c.user_id=$4 and c.attempt_generation=$5 and d.id=c.document_id and d.attempt_generation=c.attempt_generation and b.status not in ('cancelling','cancelled') returning c.chunk_index,c.page_manifest,b.document_type_snapshot,b.parsing_prompt_snapshot`

func (s *Store) IsCancelled(ctx context.Context, work bulkworker.Work) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var batchStatus, documentStatus string
	var generation int
	err = tx.QueryRow(ctx, `select b.status,d.status,d.attempt_generation from public.bulk_import_batches b join private.bulk_import_documents d on d.batch_id=b.id and d.user_id=b.user_id where b.id=$1 and d.id=$2 and b.user_id=$3 for update of b,d`, work.BatchID, work.DocumentID, work.UserID).Scan(&batchStatus, &documentStatus, &generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	cancelled := batchStatus == "cancelling" || batchStatus == "cancelled" || documentStatus == "cancelled" || generation != work.Generation
	if (batchStatus == "cancelling" || batchStatus == "cancelled") && documentStatus != "completed" && documentStatus != "completed_with_errors" && documentStatus != "failed" && documentStatus != "cancelled" {
		if _, err = tx.Exec(ctx, `update private.bulk_import_documents set status='cancelled',completed_at=coalesce(completed_at,now()) where id=$1 and user_id=$2`, work.DocumentID, work.UserID); err != nil {
			return false, err
		}
		if err = recomputeBulkProgress(ctx, tx, work.UserID, work.BatchID); err != nil {
			return false, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	return cancelled, nil
}

func (s *Store) LoadOriginals(ctx context.Context, work bulkworker.Work) ([]bulkworker.OriginalFile, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var sourceScopeID uuid.UUID
	var status string
	err = tx.QueryRow(ctx, `select source_scope_id,status from private.bulk_import_documents where id=$1 and batch_id=$2 and user_id=$3 and attempt_generation=$4 for update`, work.DocumentID, work.BatchID, work.UserID, work.Generation).Scan(&sourceScopeID, &status)
	if err != nil {
		return nil, err
	}
	if status == "cancelled" {
		return nil, bulkimport.ErrConflict
	}
	if status != "queued" && status != "preparing" {
		return nil, bulkworker.ErrWorkAlreadyApplied
	}
	if _, err = tx.Exec(ctx, `update private.bulk_import_documents set status='preparing',started_at=coalesce(started_at,now()),completed_at=null,error_summary=null where id=$1 and user_id=$2`, work.DocumentID, work.UserID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `select id,storage_object_path,display_filename,declared_mime_type,declared_byte_size,encode(declared_sha256,'hex') from private.bulk_import_files where document_id=$1 and user_id=$2 and status in ('uploaded','verified') order by sort_order,id`, work.DocumentID, work.UserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	files := make([]bulkworker.OriginalFile, 0)
	for rows.Next() {
		var item bulkworker.OriginalFile
		if err = rows.Scan(&item.FileID, &item.ObjectPath, &item.Filename, &item.MIMEType, &item.ByteSize, &item.SHA256); err != nil {
			return nil, err
		}
		item.SourceScopeID, err = attachmentstorage.ScopeIDFromObjectPath(work.UserID, item.ObjectPath)
		if err != nil {
			return nil, fmt.Errorf("validate bulk original storage scope: %w", err)
		}
		files = append(files, item)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if len(files) == 0 {
		return nil, errors.New("bulk document has no uploaded files")
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return files, nil
}

func (s *Store) RecordPrepareFailure(ctx context.Context, work bulkworker.Work, class string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var status string
	if err = tx.QueryRow(ctx, `select status from private.bulk_import_documents where id=$1 and batch_id=$2 and user_id=$3 and attempt_generation=$4 for update`, work.DocumentID, work.BatchID, work.UserID, work.Generation).Scan(&status); err != nil {
		return err
	}
	if status == "cancelled" || status == "failed" {
		return tx.Commit(ctx)
	}
	rows, err := tx.Query(ctx, `select storage_object_path from private.bulk_import_files where document_id=$1 and user_id=$2 and status in ('uploaded','verified') order by sort_order,id`, work.DocumentID, work.UserID)
	if err != nil {
		return err
	}
	grouped := make(map[uuid.UUID][]string)
	for rows.Next() {
		var path string
		if err = rows.Scan(&path); err != nil {
			rows.Close()
			return err
		}
		scopeID, scopeErr := attachmentstorage.ScopeIDFromObjectPath(work.UserID, path)
		if scopeErr != nil {
			rows.Close()
			return scopeErr
		}
		grouped[scopeID] = append(grouped[scopeID], path)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if _, err = tx.Exec(ctx, `update private.bulk_import_files set status='cleanup_pending',error_summary=$3 where document_id=$1 and user_id=$2 and status in ('uploaded','verified')`, work.DocumentID, work.UserID, class); err != nil {
		return err
	}
	scopes := make([]uuid.UUID, 0, len(grouped))
	for scopeID := range grouped {
		scopes = append(scopes, scopeID)
	}
	sort.Slice(scopes, func(i, j int) bool { return scopes[i].String() < scopes[j].String() })
	for _, scopeID := range scopes {
		paths := grouped[scopeID]
		sort.Strings(paths)
		for start := 0; start < len(paths); start += 1000 {
			end := min(start+1000, len(paths))
			payload, marshalErr := json.Marshal(jobs.SourceAttachmentCleanupPayload{SourceID: scopeID.String(), ObjectPaths: paths[start:end]})
			if marshalErr != nil {
				return marshalErr
			}
			if _, err = tx.Exec(ctx, `insert into private.transaction_jobs(user_id,job_type,payload,max_attempts) values($1,$2,$3::jsonb,5)`, work.UserID, string(jobs.KindSourceAttachmentCleanup), string(payload)); err != nil {
				return err
			}
		}
	}
	if _, err = tx.Exec(ctx, `update private.transaction_jobs set status='cancelled',completed_at=now() where bulk_import_document_id=$1 and user_id=$2 and status='queued'`, work.DocumentID, work.UserID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `update private.bulk_import_documents set status='failed',failed_count=failed_count+1,error_summary=$3,completed_at=now() where id=$1 and user_id=$2`, work.DocumentID, work.UserID, class); err != nil {
		return err
	}
	if err = recomputeBulkProgress(ctx, tx, work.UserID, work.BatchID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) RecordPrepared(ctx context.Context, work bulkworker.Work, prepared bulkworker.PreparedDocument) error {
	if len(prepared.Pages) == 0 || len(prepared.Pages) > bulkimport.MaxPages {
		return bulkimport.ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var sourceID uuid.UUID
	var status string
	err = tx.QueryRow(ctx, `select data_source_id,status from private.bulk_import_documents where id=$1 and batch_id=$2 and user_id=$3 and attempt_generation=$4 for update`, work.DocumentID, work.BatchID, work.UserID, work.Generation).Scan(&sourceID, &status)
	if err != nil {
		return err
	}
	if status == "cancelled" {
		return tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx, `delete from private.bulk_import_chunks where document_id=$1 and user_id=$2 and attempt_generation=$3`, work.DocumentID, work.UserID, work.Generation); err != nil {
		return err
	}
	for start, chunkIndex := 0, 0; start < len(prepared.Pages); start, chunkIndex = start+5, chunkIndex+1 {
		end := min(start+5, len(prepared.Pages))
		manifest := storedManifest{Pages: make([]storedPage, 0, end-start)}
		for _, page := range prepared.Pages[start:end] {
			manifest.Pages = append(manifest.Pages, storedPage{ManifestPath: page.ManifestPath, ObjectPath: page.ObjectPath, Filename: page.Filename, MIMEType: page.MIMEType, SourceScopeID: page.SourceScopeID})
		}
		encoded, marshalErr := json.Marshal(manifest)
		if marshalErr != nil {
			return marshalErr
		}
		var chunkID uuid.UUID
		err = tx.QueryRow(ctx, `insert into private.bulk_import_chunks(user_id,batch_id,document_id,attempt_generation,chunk_index,page_manifest,page_count) values($1,$2,$3,$4,$5,$6::jsonb,$7) returning id`, work.UserID, work.BatchID, work.DocumentID, work.Generation, chunkIndex, string(encoded), end-start).Scan(&chunkID)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `insert into private.transaction_jobs(user_id,data_source_id,job_type,bulk_import_batch_id,bulk_import_document_id,bulk_import_chunk_id,attempt_generation,payload) values($1,$2,$3,$4,$5,$6,$7,'{}') on conflict do nothing`, work.UserID, sourceID, string(jobs.KindBulkDocumentChunkParse), work.BatchID, work.DocumentID, chunkID, work.Generation); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `update private.bulk_import_files set status='verified',verified_mime_type=declared_mime_type,verified_byte_size=declared_byte_size,verified_sha256=declared_sha256,finalized_at=coalesce(finalized_at,now()),error_summary=null where document_id=$1 and user_id=$2`, work.DocumentID, work.UserID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `update private.bulk_import_documents set status='parsing',page_count=$3,error_summary=null where id=$1 and user_id=$2`, work.DocumentID, work.UserID, len(prepared.Pages)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) LoadChunk(ctx context.Context, work bulkworker.Work) (bulkworker.ChunkInput, error) {
	var input bulkworker.ChunkInput
	var raw []byte
	err := s.pool.QueryRow(ctx, claimBulkChunkSQL, work.ChunkID, work.DocumentID, work.BatchID, work.UserID, work.Generation).Scan(&input.ChunkIndex, &raw, &input.DocumentType, &input.TemplatePrompt)
	if err != nil {
		return input, err
	}
	var manifest storedManifest
	if err = json.Unmarshal(raw, &manifest); err != nil || len(manifest.Pages) == 0 || len(manifest.Pages) > 5 {
		return input, errors.New("bulk chunk manifest is invalid")
	}
	input.Pages = make([]bulkworker.PreparedPage, 0, len(manifest.Pages))
	for _, page := range manifest.Pages {
		page.SourceScopeID, err = attachmentstorage.ScopeIDFromObjectPath(work.UserID, page.ObjectPath)
		if err != nil {
			return input, fmt.Errorf("validate prepared page storage scope: %w", err)
		}
		input.PageManifest = append(input.PageManifest, page.ManifestPath)
		input.Pages = append(input.Pages, bulkworker.PreparedPage{ManifestPath: page.ManifestPath, ObjectPath: page.ObjectPath, SourceScopeID: page.SourceScopeID, Filename: page.Filename, MIMEType: page.MIMEType})
	}
	accounts, err := s.batchAccounts(ctx, work.UserID, work.BatchID)
	if err != nil {
		return input, err
	}
	for _, account := range accounts {
		input.Accounts = append(input.Accounts, bulkprompt.AccountDescriptor{AccountRef: account.AccountRef, Name: account.Name, Institution: account.InstitutionName, AccountType: account.AccountType})
	}
	return input, nil
}

func (s *Store) RecordChunkResult(ctx context.Context, work bulkworker.Work, result bulkworker.ChunkResult) error {
	encoded, err := json.Marshal(result.Decoded)
	if err != nil {
		return err
	}
	metadata, _ := json.Marshal(map[string]any{"batch_id": work.BatchID, "document_id": work.DocumentID, "generation": work.Generation, "chunk_id": work.ChunkID, "chunk_index": result.Prompt.UserMessage})
	components, _ := json.Marshal(result.Prompt)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var sourceID uuid.UUID
	if err = tx.QueryRow(ctx, `select data_source_id from private.bulk_import_documents where id=$1 and batch_id=$2 and user_id=$3 and attempt_generation=$4 and status not in ('cancelled','failed') for update`, work.DocumentID, work.BatchID, work.UserID, work.Generation).Scan(&sourceID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return tx.Commit(ctx)
		}
		return err
	}
	var ordinal int
	if err = tx.QueryRow(ctx, `select coalesce(max(attempt_ordinal),0)+1 from private.source_parse_attempts where bulk_import_chunk_id=$1 and user_id=$2`, work.ChunkID, work.UserID).Scan(&ordinal); err != nil {
		return err
	}
	var attemptID uuid.UUID
	err = tx.QueryRow(ctx, `insert into private.source_parse_attempts(user_id,data_source_id,model_name,request_metadata,parsed_candidate,assembled_system_prompt,normalized_input,provider_request,provider_response,model_output,prompt_components,validation_status,started_at,completed_at,bulk_import_chunk_id,attempt_ordinal) values($1,$2,nullif($3,''),$4::jsonb,$5::jsonb,$6,$7,case when $8='null' then null else $8::json end,case when $9='null' then null else $9::json end,case when $10='null' then null else $10::json end,$11::jsonb,'valid',now(),now(),$12,$13) returning id`, work.UserID, sourceID, result.ModelName, string(metadata), string(encoded), result.Prompt.SystemPrompt, string(result.Prompt.UserMessage), nullableRaw(result.ProviderRequest), nullableRaw(result.ProviderResponse), nullableRaw(result.RawModelOutput), string(components), work.ChunkID, ordinal).Scan(&attemptID)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `update private.bulk_import_chunks set status='valid',valid_candidate_count=$3,invalid_candidate_count=0,completed_at=now(),error_summary=null where id=$1 and user_id=$2`, work.ChunkID, work.UserID, len(result.Decoded.Transactions)); err != nil {
		return err
	}
	if err = enqueueAggregateWhenReady(ctx, tx, work, sourceID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type chunkFailureAudit struct {
	ModelName        string
	RequestMetadata  []byte
	SystemPrompt     string
	NormalizedInput  string
	ProviderRequest  []byte
	ProviderResponse []byte
	ModelOutput      []byte
	PromptComponents []byte
}

func buildChunkFailureAudit(work bulkworker.Work, failure bulkworker.ChunkFailure) (chunkFailureAudit, error) {
	metadata, err := json.Marshal(map[string]any{
		"batch_id": work.BatchID, "document_id": work.DocumentID,
		"generation": work.Generation, "chunk_id": work.ChunkID,
	})
	if err != nil {
		return chunkFailureAudit{}, err
	}
	components := []byte(`{}`)
	if failure.Prompt.SystemPrompt != "" || len(failure.Prompt.UserMessage) != 0 || failure.Prompt.PlatformVersion != 0 || failure.Prompt.SchemaVersion != 0 || len(failure.Prompt.VisualPlaceholders) != 0 {
		components, err = json.Marshal(failure.Prompt)
		if err != nil {
			return chunkFailureAudit{}, err
		}
	}
	audit := chunkFailureAudit{
		ModelName: failure.ModelName, RequestMetadata: metadata,
		SystemPrompt: failure.Prompt.SystemPrompt, NormalizedInput: string(failure.Prompt.UserMessage),
		ProviderRequest:  auditJSONObject(failure.ProviderRequest, "raw_provider_request"),
		ProviderResponse: auditJSONObject(failure.ProviderResponse, "raw_provider_response"),
		ModelOutput:      auditJSONObject(failure.ModelOutput, "raw_model_output"),
		PromptComponents: components,
	}
	if err = validateChunkFailureAudit(audit); err != nil {
		return chunkFailureAudit{}, err
	}
	return audit, nil
}

func validateChunkFailureAudit(audit chunkFailureAudit) error {
	limits := []struct {
		name       string
		value      []byte
		max        int
		jsonObject bool
	}{
		{name: "request metadata", value: audit.RequestMetadata, max: maxChunkAuditRequestMetadataBytes, jsonObject: true},
		{name: "provider request", value: audit.ProviderRequest, max: maxChunkAuditProviderRequestBytes, jsonObject: true},
		{name: "provider response", value: audit.ProviderResponse, max: maxChunkAuditProviderResponseBytes, jsonObject: true},
		{name: "model output", value: audit.ModelOutput, max: maxChunkAuditModelOutputBytes, jsonObject: true},
		{name: "prompt components", value: audit.PromptComponents, max: maxChunkAuditPromptComponentsBytes, jsonObject: true},
	}
	for _, field := range limits {
		if len(field.value) > field.max {
			return fmt.Errorf("bulk parse audit %s exceeds byte limit", field.name)
		}
		if field.jsonObject && len(field.value) != 0 {
			var object map[string]json.RawMessage
			if err := json.Unmarshal(field.value, &object); err != nil || object == nil {
				return fmt.Errorf("bulk parse audit %s must be a JSON object", field.name)
			}
		}
	}
	if len(audit.SystemPrompt) > maxChunkAuditSystemPromptBytes {
		return errors.New("bulk parse audit system prompt exceeds byte limit")
	}
	if len(audit.NormalizedInput) > maxChunkAuditNormalizedInputBytes {
		return errors.New("bulk parse audit normalized input exceeds byte limit")
	}
	return nil
}

func auditJSONObject(raw json.RawMessage, fallbackField string) []byte {
	if len(raw) == 0 {
		return nil
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) == nil && object != nil {
		return append([]byte(nil), raw...)
	}
	encoded, _ := json.Marshal(map[string]string{fallbackField: string(raw)})
	return encoded
}

func (s *Store) RecordChunkFailure(ctx context.Context, work bulkworker.Work, failure bulkworker.ChunkFailure) error {
	audit, err := buildChunkFailureAudit(work, failure)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var sourceID uuid.UUID
	if err = tx.QueryRow(ctx, `select data_source_id from private.bulk_import_documents where id=$1 and batch_id=$2 and user_id=$3 and attempt_generation=$4 for update`, work.DocumentID, work.BatchID, work.UserID, work.Generation).Scan(&sourceID); err != nil {
		return err
	}
	var ordinal int
	if err = tx.QueryRow(ctx, `select coalesce(max(attempt_ordinal),0)+1 from private.source_parse_attempts where bulk_import_chunk_id=$1 and user_id=$2`, work.ChunkID, work.UserID).Scan(&ordinal); err != nil {
		return err
	}
	summary := chunkFailureSummary(failure)
	if _, err = tx.Exec(ctx, `insert into private.source_parse_attempts(user_id,data_source_id,model_name,request_metadata,assembled_system_prompt,normalized_input,provider_request,provider_response,model_output,prompt_components,validation_status,error_summary,started_at,completed_at,bulk_import_chunk_id,attempt_ordinal) values($1,$2,nullif($3,''),$4::jsonb,nullif($5,''),nullif($6,''),case when $7='null' then null else $7::json end,case when $8='null' then null else $8::json end,case when $9='null' then null else $9::json end,$10::jsonb,$11,$12,now(),now(),$13,$14)`, work.UserID, sourceID, audit.ModelName, string(audit.RequestMetadata), audit.SystemPrompt, audit.NormalizedInput, nullableRaw(audit.ProviderRequest), nullableRaw(audit.ProviderResponse), nullableRaw(audit.ModelOutput), string(audit.PromptComponents), map[bool]string{true: "invalid", false: "failed"}[failure.Terminal], summary, work.ChunkID, ordinal); err != nil {
		return err
	}
	status := "queued"
	if failure.Terminal {
		status = "failed"
	}
	if _, err = tx.Exec(ctx, `update private.bulk_import_chunks set status=$3,error_summary=$4,completed_at=case when $3='failed' then now() else null end,started_at=case when $3='queued' then null else started_at end where id=$1 and user_id=$2`, work.ChunkID, work.UserID, status, summary); err != nil {
		return err
	}
	if failure.Terminal {
		if err = enqueueAggregateWhenReady(ctx, tx, work, sourceID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func enqueueAggregateWhenReady(ctx context.Context, tx pgx.Tx, work bulkworker.Work, sourceID uuid.UUID) error {
	var remaining int
	if err := tx.QueryRow(ctx, `select count(*) from private.bulk_import_chunks where document_id=$1 and user_id=$2 and attempt_generation=$3 and status in ('queued','parsing')`, work.DocumentID, work.UserID, work.Generation).Scan(&remaining); err != nil || remaining != 0 {
		return err
	}
	_, err := tx.Exec(ctx, `insert into private.transaction_jobs(user_id,data_source_id,job_type,bulk_import_batch_id,bulk_import_document_id,attempt_generation,payload) values($1,$2,$3,$4,$5,$6,'{}') on conflict do nothing`, work.UserID, sourceID, string(jobs.KindBulkDocumentAggregate), work.BatchID, work.DocumentID, work.Generation)
	return err
}

type candidateRecord struct {
	AttemptID uuid.UUID
	Candidate bulkparse.Candidate
	Evidence  []bulkparse.Evidence
}

func (s *Store) AggregateDocument(ctx context.Context, work bulkworker.Work) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var sourceID uuid.UUID
	var documentType bulkimport.DocumentType
	err = tx.QueryRow(ctx, `select d.data_source_id,b.document_type_snapshot from private.bulk_import_documents d join public.bulk_import_batches b on b.id=d.batch_id and b.user_id=d.user_id where d.id=$1 and d.batch_id=$2 and d.user_id=$3 and d.attempt_generation=$4 and d.status not in ('cancelled','failed') for update of d`, work.DocumentID, work.BatchID, work.UserID, work.Generation).Scan(&sourceID, &documentType)
	if errors.Is(err, pgx.ErrNoRows) {
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `update private.bulk_import_documents set status='aggregating' where id=$1 and user_id=$2`, work.DocumentID, work.UserID); err != nil {
		return err
	}
	accountRefs, err := batchAccountRefs(ctx, tx, work.UserID, work.BatchID)
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `select distinct on (c.chunk_index) a.id,a.parsed_candidate from private.bulk_import_chunks c join private.source_parse_attempts a on a.bulk_import_chunk_id=c.id and a.user_id=c.user_id where c.document_id=$1 and c.user_id=$2 and c.attempt_generation=$3 and c.status in ('valid','partially_valid') and a.validation_status='valid' order by c.chunk_index,a.attempt_ordinal desc`, work.DocumentID, work.UserID, work.Generation)
	if err != nil {
		return err
	}
	records := make([]candidateRecord, 0)
	var aggregateSummary *bulkparse.BillSummary
	summaryConflicts := make(map[string]bool)
	for rows.Next() {
		var attemptID uuid.UUID
		var raw []byte
		if err = rows.Scan(&attemptID, &raw); err != nil {
			rows.Close()
			return err
		}
		parserType := bulkparse.GenericDocument
		if documentType == bulkimport.DocumentCreditCardBill {
			parserType = bulkparse.CreditCardBill
		}
		decoded, decodeErr := bulkparse.Decode(raw, parserType, accountRefs)
		if decodeErr != nil {
			rows.Close()
			return decodeErr
		}
		if decoded.DocumentSummary != nil {
			aggregateSummary = mergeBillSummary(aggregateSummary, decoded.DocumentSummary, summaryConflicts)
		}
		for _, item := range decoded.Transactions {
			records = append(records, candidateRecord{AttemptID: attemptID, Candidate: item.Candidate, Evidence: item.Evidence})
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	var summary json.RawMessage
	if aggregateSummary != nil {
		summary, err = json.Marshal(aggregateSummary)
		if err != nil {
			return err
		}
	}
	accountMap, err := batchAccountMap(ctx, tx, work.UserID, work.BatchID)
	if err != nil {
		return err
	}
	accountIDs, candidates := make([]uuid.UUID, len(records)), make([]bulkparse.Candidate, len(records))
	for index, record := range records {
		accountIDs[index], candidates[index] = accountMap[record.Candidate.AccountRef], record.Candidate
		if accountIDs[index] == uuid.Nil {
			return errors.New("bulk candidate account reference is unresolved")
		}
	}
	deduped, err := bulkparse.DedupeV1(accountIDs, candidates)
	if err != nil {
		return err
	}
	lineIndexes := documentGlobalLineIndexes(deduped)
	inserted := make([]uuid.UUID, len(records))
	for index, record := range records {
		if documentType == bulkimport.DocumentCreditCardBill {
			record.Candidate.LineIndex = lineIndexes[index]
		}
		candidateJSON, marshalErr := storedCandidateJSON(record.Candidate, record.Evidence, documentType)
		if marshalErr != nil {
			return marshalErr
		}
		fingerprint, _ := hex.DecodeString(deduped[index].Fingerprint)
		status := bulkimport.CandidatePending
		var duplicateOf *uuid.UUID
		if deduped[index].DuplicateOf != nil {
			status = bulkimport.CandidateDuplicate
			duplicate := inserted[*deduped[index].DuplicateOf]
			duplicateOf = &duplicate
		}
		err = tx.QueryRow(ctx, `insert into private.source_candidates(user_id,batch_id,document_id,data_source_id,source_parse_attempt_id,attempt_generation,output_ordinal,fingerprint,parsed_candidate,account_id,status,duplicate_of_candidate_id,reconciliation_reason) values($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb,$10,$11,$12,$13) on conflict(document_id,attempt_generation,output_ordinal) do update set updated_at=now() returning id`, work.UserID, work.BatchID, work.DocumentID, sourceID, record.AttemptID, work.Generation, index, fingerprint, string(candidateJSON), accountIDs[index], status, duplicateOf, map[bool]string{true: "exact candidate fingerprint repeated in document", false: ""}[duplicateOf != nil]).Scan(&inserted[index])
		if err != nil {
			return err
		}
		if duplicateOf == nil {
			if _, err = tx.Exec(ctx, `insert into private.transaction_jobs(user_id,data_source_id,job_type,bulk_import_batch_id,bulk_import_document_id,bulk_import_candidate_id,attempt_generation,payload) values($1,$2,$3,$4,$5,$6,$7,'{}') on conflict do nothing`, work.UserID, sourceID, string(jobs.KindBulkCandidateReconcile), work.BatchID, work.DocumentID, inserted[index], work.Generation); err != nil {
				return err
			}
		}
	}
	if _, err = tx.Exec(ctx, `update private.bulk_import_documents set status='reconciling',document_summary=$3::jsonb,candidate_count=$4,duplicate_count=$5,failed_count=(select count(*)::int from private.bulk_import_chunks where document_id=$1 and user_id=$2 and attempt_generation=$6 and status='failed') where id=$1 and user_id=$2`, work.DocumentID, work.UserID, nullableJSONBytes(summary), len(records), countDuplicates(deduped), work.Generation); err != nil {
		return err
	}
	if len(records) == countDuplicates(deduped) {
		if _, err = tx.Exec(ctx, `insert into private.transaction_jobs(user_id,data_source_id,job_type,bulk_import_batch_id,bulk_import_document_id,attempt_generation,payload) values($1,$2,$3,$4,$5,$6,'{}') on conflict do nothing`, work.UserID, sourceID, string(jobs.KindBulkDocumentPostProcess), work.BatchID, work.DocumentID, work.Generation); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func documentGlobalLineIndexes(candidates []bulkparse.Deduped) []int {
	result := make([]int, len(candidates))
	next := 0
	for index, candidate := range candidates {
		if candidate.DuplicateOf != nil && *candidate.DuplicateOf >= 0 && *candidate.DuplicateOf < index {
			result[index] = result[*candidate.DuplicateOf]
			continue
		}
		next++
		result[index] = next
	}
	return result
}

func (s *Store) ReconcileCandidate(ctx context.Context, work bulkworker.Work) error {
	if s.actions == nil {
		return errors.New("bulk candidate reconciler is not configured")
	}
	return s.actions.ReconcileBulkCandidate(ctx, work.UserID, work.CandidateID, work.Generation)
}

func (s *Store) LoadPostProcessInput(ctx context.Context, work bulkworker.Work) (bulkimport.PostProcessInput, error) {
	input := bulkimport.PostProcessInput{UserID: work.UserID, BatchID: work.BatchID, DocumentID: work.DocumentID, AttemptGeneration: work.Generation}
	var raw []byte
	err := s.pool.QueryRow(ctx, `select b.document_type_snapshot,coalesce(d.document_summary,'{}'::jsonb) from private.bulk_import_documents d join public.bulk_import_batches b on b.id=d.batch_id and b.user_id=d.user_id where d.id=$1 and d.batch_id=$2 and d.user_id=$3 and d.attempt_generation=$4 and d.status='reconciling'`, work.DocumentID, work.BatchID, work.UserID, work.Generation).Scan(&input.DocumentType, &raw)
	if err != nil {
		return input, err
	}
	input.DocumentSummary = append(json.RawMessage(nil), raw...)
	rows, err := s.pool.Query(ctx, `select id from private.source_candidates where document_id=$1 and user_id=$2 and attempt_generation=$3 order by output_ordinal`, work.DocumentID, work.UserID, work.Generation)
	if err != nil {
		return input, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		if err = rows.Scan(&id); err != nil {
			return input, err
		}
		input.CandidateIDs = append(input.CandidateIDs, id)
	}
	return input, rows.Err()
}

func (s *Store) RecordGenericPostProcess(ctx context.Context, input bulkimport.PostProcessInput) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var candidateFailures, chunkFailures int
	if err = tx.QueryRow(ctx, `select (select count(*)::int from private.source_candidates where document_id=$1 and user_id=$2 and attempt_generation=$3 and status='failed'),(select count(*)::int from private.bulk_import_chunks where document_id=$1 and user_id=$2 and attempt_generation=$3 and status='failed')`, input.DocumentID, input.UserID, input.AttemptGeneration).Scan(&candidateFailures, &chunkFailures); err != nil {
		return err
	}
	failed, status := documentFailureOutcome(candidateFailures, chunkFailures)
	if _, err = tx.Exec(ctx, `update private.bulk_import_documents set status=$4,failed_count=$6,completed_at=now(),error_summary=null where id=$1 and batch_id=$2 and user_id=$3 and attempt_generation=$5 and status='reconciling'`, input.DocumentID, input.BatchID, input.UserID, status, input.AttemptGeneration, failed); err != nil {
		return err
	}
	if err = recomputeBulkProgress(ctx, tx, input.UserID, input.BatchID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func documentFailureOutcome(candidateFailures, chunkFailures int) (int, bulkimport.DocumentStatus) {
	failed := candidateFailures + chunkFailures
	if failed > 0 {
		return failed, bulkimport.DocumentCompletedWithErrors
	}
	return 0, bulkimport.DocumentCompleted
}

func recomputeBulkProgress(ctx context.Context, tx pgx.Tx, userID, batchID uuid.UUID) error {
	_, err := tx.Exec(ctx, `update public.bulk_import_batches b set file_count=p.files,document_count=p.documents,page_count=p.pages,parsed_candidate_count=p.candidates,created_count=p.created,attached_count=p.attached,review_count=p.review,failed_count=p.failed,duplicate_count=p.duplicates,status=case when b.status='cancelling' and p.active=0 then 'cancelled' when p.active=0 and p.failed>0 then 'completed_with_errors' when p.active=0 then 'completed' when b.status='queued' then 'running' else b.status end,started_at=case when b.status='queued' then coalesce(b.started_at,now()) else b.started_at end,completed_at=case when p.active=0 then coalesce(b.completed_at,now()) else null end from (select (select count(*) from private.bulk_import_files f where f.batch_id=$2 and f.user_id=$1)::int files,count(*)::int documents,coalesce(sum(d.page_count),0)::int pages,coalesce(sum(d.candidate_count),0)::int candidates,coalesce(sum(d.created_count),0)::int created,coalesce(sum(d.attached_count),0)::int attached,coalesce(sum(d.review_count),0)::int review,coalesce(sum(d.failed_count),0)::int failed,coalesce(sum(d.duplicate_count),0)::int duplicates,count(*) filter(where d.status in ('queued','preparing','parsing','aggregating','reconciling'))::int active from private.bulk_import_documents d where d.batch_id=$2 and d.user_id=$1) p where b.id=$2 and b.user_id=$1`, userID, batchID)
	return err
}

func batchAccountMap(ctx context.Context, tx pgx.Tx, userID, batchID uuid.UUID) (map[string]uuid.UUID, error) {
	rows, err := tx.Query(ctx, `select account_ref,account_id from private.bulk_import_batch_accounts where batch_id=$1 and user_id=$2 order by sort_order`, batchID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]uuid.UUID{}
	for rows.Next() {
		var ref string
		var id uuid.UUID
		if err = rows.Scan(&ref, &id); err != nil {
			return nil, err
		}
		result[ref] = id
	}
	return result, rows.Err()
}

func batchAccountRefs(ctx context.Context, tx pgx.Tx, userID, batchID uuid.UUID) ([]string, error) {
	mapping, err := batchAccountMap(ctx, tx, userID, batchID)
	if err != nil {
		return nil, err
	}
	refs := make([]string, 0, len(mapping))
	for index := 1; index <= len(mapping); index++ {
		refs = append(refs, fmt.Sprintf("account_%d", index))
	}
	return refs, nil
}

func storedCandidateJSON(candidate bulkparse.Candidate, evidence []bulkparse.Evidence, documentType bulkimport.DocumentType) ([]byte, error) {
	raw, err := json.Marshal(candidate)
	if err != nil {
		return nil, err
	}
	var object map[string]any
	if err = json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}
	confidence := 1.0
	if len(evidence) == 0 {
		confidence = 0
	}
	for _, item := range evidence {
		if item.Confidence < confidence {
			confidence = item.Confidence
		}
	}
	object["confidence"] = confidence
	object["auto_eligible"] = confidence >= 0.75 && !candidate.PossibleInternalTransfer
	object["evidence"] = evidence
	if documentType != bulkimport.DocumentCreditCardBill {
		return json.Marshal(object)
	}
	object["bill_line_index"], object["bill_line_kind"] = object["line_index"], object["line_kind"]
	delete(object, "line_index")
	delete(object, "line_kind")
	return json.Marshal(object)
}

func countDuplicates(values []bulkparse.Deduped) int {
	count := 0
	for _, value := range values {
		if value.DuplicateOf != nil {
			count++
		}
	}
	return count
}

func mergeBillSummary(current, next *bulkparse.BillSummary, conflicts map[string]bool) *bulkparse.BillSummary {
	if next == nil {
		return current
	}
	if current == nil {
		copy := *next
		copy.Evidence = append([]bulkparse.Evidence(nil), next.Evidence...)
		return &copy
	}
	mergeSummaryValue("card_account_ref", &current.CardAccountRef, next.CardAccountRef, conflicts)
	mergeSummaryValue("period_start", &current.PeriodStart, next.PeriodStart, conflicts)
	mergeSummaryValue("period_end", &current.PeriodEnd, next.PeriodEnd, conflicts)
	mergeSummaryValue("statement_date", &current.StatementDate, next.StatementDate, conflicts)
	mergeSummaryValue("due_date", &current.DueDate, next.DueDate, conflicts)
	mergeSummaryValue("settlement_currency", &current.SettlementCurrency, next.SettlementCurrency, conflicts)
	mergeSummaryValue("amount_due_minor", &current.AmountDueMinor, next.AmountDueMinor, conflicts)
	mergeSummaryValue("minimum_payment_minor", &current.MinimumPaymentMinor, next.MinimumPaymentMinor, conflicts)
	mergeSummaryValue("previous_balance_minor", &current.PreviousBalanceMinor, next.PreviousBalanceMinor, conflicts)
	// Exact per-chunk evidence remains immutable in parse attempts. The merged
	// summary only carries citations for fields that survived conflict folding.
	current.Evidence = append(current.Evidence, next.Evidence...)
	return current
}

func mergeSummaryValue[T comparable](key string, current **T, next *T, conflicts map[string]bool) {
	if conflicts[key] {
		*current = nil
		return
	}
	if *current == nil {
		if next != nil {
			copy := *next
			*current = &copy
		}
		return
	}
	if next != nil && **current != *next {
		conflicts[key] = true
		*current = nil
	}
}

func nullableRaw(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "null"
	}
	return string(raw)
}

func nullableJSONBytes(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return string(raw)
}

func boundedError(value string) string {
	if len(value) > 2000 {
		return value[:2000]
	}
	return value
}

func chunkFailureSummary(failure bulkworker.ChunkFailure) string {
	if failure.Detail == "" {
		return boundedError(failure.Class)
	}
	return boundedError(failure.Class + ": " + failure.Detail)
}

var _ bulkworker.Repository = (*Store)(nil)

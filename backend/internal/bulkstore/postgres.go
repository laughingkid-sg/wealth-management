// Package bulkstore persists Bulk Import state in PostgreSQL. Calls made through
// CandidateActions and DeletionCoordinator own their complete short database
// transactions; Store never wraps external work in a transaction.
package bulkstore

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zhengteck/wealth-builder/backend/internal/attachmentstorage"
	"github.com/zhengteck/wealth-builder/backend/internal/bulkimport"
)

type CandidateActions interface {
	ResolveBulkCandidate(context.Context, uuid.UUID, uuid.UUID, bulkimport.CandidateResolution) (bulkimport.Candidate, error)
	ReconcileBulkCandidate(context.Context, uuid.UUID, uuid.UUID, int) error
}

type DeletionCoordinator interface {
	DeleteBulkDocument(context.Context, uuid.UUID, uuid.UUID) error
	DeleteBulkBatch(context.Context, uuid.UUID, uuid.UUID) error
}

type Store struct {
	pool     *pgxpool.Pool
	actions  CandidateActions
	deletion DeletionCoordinator
}

func New(pool *pgxpool.Pool, actions CandidateActions, deletion DeletionCoordinator) *Store {
	return &Store{pool: pool, actions: actions, deletion: deletion}
}

func (s *Store) ListTemplates(ctx context.Context, userID uuid.UUID, includeArchived bool) ([]bulkimport.Template, error) {
	rows, err := s.pool.Query(ctx, `select id, title, document_type, parsing_prompt, version, archived_at, created_at, updated_at from private.bulk_import_templates where user_id=$1 and ($2 or archived_at is null) order by archived_at nulls first, updated_at desc, id desc`, userID, includeArchived)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []bulkimport.Template{}
	for rows.Next() {
		var item bulkimport.Template
		if err = rows.Scan(&item.ID, &item.Title, &item.DocumentType, &item.ParsingPrompt, &item.Version, &item.ArchivedAt, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		item.UserID = userID
		item.Accounts, err = s.templateAccounts(ctx, userID, item.ID)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) CreateTemplate(ctx context.Context, userID uuid.UUID, input bulkimport.TemplateInput) (bulkimport.Template, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return bulkimport.Template{}, err
	}
	defer tx.Rollback(ctx)
	if err = validateAccounts(ctx, tx, userID, input.AccountIDs, input.DocumentType); err != nil {
		return bulkimport.Template{}, err
	}
	var result bulkimport.Template
	err = tx.QueryRow(ctx, `insert into private.bulk_import_templates(user_id,title,document_type,parsing_prompt) values($1,$2,$3,$4) returning id,title,document_type,parsing_prompt,version,archived_at,created_at,updated_at`, userID, input.Title, input.DocumentType, input.ParsingPrompt).Scan(&result.ID, &result.Title, &result.DocumentType, &result.ParsingPrompt, &result.Version, &result.ArchivedAt, &result.CreatedAt, &result.UpdatedAt)
	if err != nil {
		return bulkimport.Template{}, mapError(err)
	}
	for index, accountID := range input.AccountIDs {
		if _, err = tx.Exec(ctx, `insert into private.bulk_import_template_accounts(user_id,template_id,account_id,sort_order) values($1,$2,$3,$4)`, userID, result.ID, accountID, index); err != nil {
			return bulkimport.Template{}, mapError(err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return bulkimport.Template{}, mapError(err)
	}
	result.UserID, result.Accounts = userID, accountSelections(input.AccountIDs)
	return result, nil
}

func (s *Store) UpdateTemplate(ctx context.Context, userID, templateID uuid.UUID, input bulkimport.TemplateInput) (bulkimport.Template, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return bulkimport.Template{}, err
	}
	defer tx.Rollback(ctx)
	if err = validateAccounts(ctx, tx, userID, input.AccountIDs, input.DocumentType); err != nil {
		return bulkimport.Template{}, err
	}
	var result bulkimport.Template
	err = tx.QueryRow(ctx, `update private.bulk_import_templates set title=$3,document_type=$4,parsing_prompt=$5,version=version+1 where id=$1 and user_id=$2 and version=$6 returning id,title,document_type,parsing_prompt,version,archived_at,created_at,updated_at`, templateID, userID, input.Title, input.DocumentType, input.ParsingPrompt, *input.ExpectedVersion).Scan(&result.ID, &result.Title, &result.DocumentType, &result.ParsingPrompt, &result.Version, &result.ArchivedAt, &result.CreatedAt, &result.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return bulkimport.Template{}, bulkimport.ErrVersionConflict
	}
	if err != nil {
		return bulkimport.Template{}, mapError(err)
	}
	if _, err = tx.Exec(ctx, `delete from private.bulk_import_template_accounts where template_id=$1 and user_id=$2`, templateID, userID); err != nil {
		return bulkimport.Template{}, err
	}
	for index, accountID := range input.AccountIDs {
		if _, err = tx.Exec(ctx, `insert into private.bulk_import_template_accounts(user_id,template_id,account_id,sort_order) values($1,$2,$3,$4)`, userID, templateID, accountID, index); err != nil {
			return bulkimport.Template{}, mapError(err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return bulkimport.Template{}, mapError(err)
	}
	result.UserID, result.Accounts = userID, accountSelections(input.AccountIDs)
	return result, nil
}

func (s *Store) SetTemplateArchived(ctx context.Context, userID, templateID uuid.UUID, archived bool) (bulkimport.Template, error) {
	expression := "now()"
	if !archived {
		expression = "null"
	}
	query := `update private.bulk_import_templates set archived_at=` + expression + `,version=version+1 where id=$1 and user_id=$2 and (archived_at is null)=$3 returning id,title,document_type,parsing_prompt,version,archived_at,created_at,updated_at`
	var result bulkimport.Template
	err := s.pool.QueryRow(ctx, query, templateID, userID, archived).Scan(&result.ID, &result.Title, &result.DocumentType, &result.ParsingPrompt, &result.Version, &result.ArchivedAt, &result.CreatedAt, &result.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return bulkimport.Template{}, bulkimport.ErrConflict
	}
	if err != nil {
		return bulkimport.Template{}, mapError(err)
	}
	result.UserID = userID
	result.Accounts, err = s.templateAccounts(ctx, userID, templateID)
	return result, err
}

func (s *Store) CreateBatch(ctx context.Context, userID, templateID uuid.UUID, override []uuid.UUID) (bulkimport.Batch, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return bulkimport.Batch{}, err
	}
	defer tx.Rollback(ctx)
	var result bulkimport.Batch
	err = tx.QueryRow(ctx, `select title,document_type,parsing_prompt,version from private.bulk_import_templates where id=$1 and user_id=$2 and archived_at is null for share`, templateID, userID).Scan(&result.TitleSnapshot, &result.DocumentTypeSnapshot, &result.ParsingPromptSnapshot, &result.TemplateVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return bulkimport.Batch{}, bulkimport.ErrNotFound
	}
	if err != nil {
		return bulkimport.Batch{}, err
	}
	accountIDs := override
	if len(accountIDs) == 0 {
		rows, qerr := tx.Query(ctx, `select account_id from private.bulk_import_template_accounts where template_id=$1 and user_id=$2 order by sort_order`, templateID, userID)
		if qerr != nil {
			return bulkimport.Batch{}, qerr
		}
		for rows.Next() {
			var id uuid.UUID
			if qerr = rows.Scan(&id); qerr != nil {
				rows.Close()
				return bulkimport.Batch{}, qerr
			}
			accountIDs = append(accountIDs, id)
		}
		qerr = rows.Err()
		rows.Close()
		if qerr != nil {
			return bulkimport.Batch{}, qerr
		}
	}
	if err = validateAccounts(ctx, tx, userID, accountIDs, result.DocumentTypeSnapshot); err != nil {
		return bulkimport.Batch{}, err
	}
	result.ID = uuid.New()
	result.UserID = userID
	result.TemplateID = &templateID
	err = tx.QueryRow(ctx, `insert into public.bulk_import_batches(id,user_id,template_id,template_version,title_snapshot,document_type_snapshot,parsing_prompt_snapshot) values($1,$2,$3,$4,$5,$6,$7) returning status,created_at,updated_at`, result.ID, userID, templateID, result.TemplateVersion, result.TitleSnapshot, result.DocumentTypeSnapshot, result.ParsingPromptSnapshot).Scan(&result.Status, &result.CreatedAt, &result.UpdatedAt)
	if err != nil {
		return bulkimport.Batch{}, mapError(err)
	}
	for index, id := range accountIDs {
		var selected bulkimport.AccountSelection
		err = tx.QueryRow(ctx, `select id,name,institution_name,account_type from public.accounts where id=$1 and user_id=$2 and deleted_at is null`, id, userID).Scan(&selected.AccountID, &selected.Name, &selected.InstitutionName, &selected.AccountType)
		if err != nil {
			return bulkimport.Batch{}, bulkimport.ErrInvalid
		}
		selected.SortOrder = index
		selected.AccountRef = fmt.Sprintf("account_%d", index+1)
		if _, err = tx.Exec(ctx, `insert into private.bulk_import_batch_accounts(user_id,batch_id,account_id,account_ref,sort_order,account_name,institution_name,account_type) values($1,$2,$3,$4,$5,$6,$7,$8)`, userID, result.ID, id, selected.AccountRef, index, selected.Name, selected.InstitutionName, selected.AccountType); err != nil {
			return bulkimport.Batch{}, mapError(err)
		}
		result.Accounts = append(result.Accounts, selected)
	}
	if err = tx.Commit(ctx); err != nil {
		return bulkimport.Batch{}, mapError(err)
	}
	return result, nil
}

func (s *Store) ListBatches(ctx context.Context, userID uuid.UUID, cursor *bulkimport.BatchCursor, limit int) (bulkimport.BatchPage, error) {
	when, id := time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC), uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff")
	if cursor != nil {
		when, id = cursor.CreatedAt, cursor.ID
	}
	rows, err := s.pool.Query(ctx, `select id,template_id,template_version,title_snapshot,document_type_snapshot,parsing_prompt_snapshot,status,file_count,document_count,page_count,parsed_candidate_count,created_count,attached_count,review_count,failed_count,duplicate_count,cancel_requested_at,error_summary,started_at,completed_at,created_at,updated_at from public.bulk_import_batches where user_id=$1 and (created_at,id)<($2,$3) order by created_at desc,id desc limit $4`, userID, when, id, limit+1)
	if err != nil {
		return bulkimport.BatchPage{}, err
	}
	defer rows.Close()
	page := bulkimport.BatchPage{Items: []bulkimport.Batch{}}
	for rows.Next() {
		var b bulkimport.Batch
		if err = scanBatch(rows, &b); err != nil {
			return bulkimport.BatchPage{}, err
		}
		b.UserID = userID
		if len(page.Items) == limit {
			last := page.Items[len(page.Items)-1]
			page.NextCursor = &bulkimport.BatchCursor{CreatedAt: last.CreatedAt, ID: last.ID}
			break
		}
		page.Items = append(page.Items, b)
	}
	return page, rows.Err()
}

func (s *Store) GetBatch(ctx context.Context, userID, batchID uuid.UUID) (bulkimport.Batch, error) {
	var b bulkimport.Batch
	err := s.pool.QueryRow(ctx, `select id,template_id,template_version,title_snapshot,document_type_snapshot,parsing_prompt_snapshot,status,file_count,document_count,page_count,parsed_candidate_count,created_count,attached_count,review_count,failed_count,duplicate_count,cancel_requested_at,error_summary,started_at,completed_at,created_at,updated_at from public.bulk_import_batches where id=$1 and user_id=$2`, batchID, userID).Scan(batchFields(&b)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return b, bulkimport.ErrNotFound
	}
	if err != nil {
		return b, err
	}
	b.UserID = userID
	b.Accounts, err = s.batchAccounts(ctx, userID, batchID)
	if err != nil {
		return b, err
	}
	b.Documents, err = s.documents(ctx, userID, batchID)
	return b, err
}

func (s *Store) ReserveFile(ctx context.Context, userID, batchID uuid.UUID, input bulkimport.ReservationInput, expires time.Time) (bulkimport.ReservedFile, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return bulkimport.ReservedFile{}, err
	}
	defer tx.Rollback(ctx)
	var status string
	var fileCount int
	var totalBytes int64
	err = tx.QueryRow(ctx, `select status,file_count,coalesce((select sum(declared_byte_size) from private.bulk_import_files where batch_id=$1 and user_id=$2),0) from public.bulk_import_batches where id=$1 and user_id=$2 for update`, batchID, userID).Scan(&status, &fileCount, &totalBytes)
	if errors.Is(err, pgx.ErrNoRows) {
		return bulkimport.ReservedFile{}, bulkimport.ErrNotFound
	}
	if err != nil {
		return bulkimport.ReservedFile{}, err
	}
	if status != "draft" || fileCount >= bulkimport.MaxFilesPerBatch || totalBytes+input.ByteSize > bulkimport.MaxBatchBytes {
		return bulkimport.ReservedFile{}, bulkimport.ErrConflict
	}
	checksum, err := hex.DecodeString(input.SHA256)
	if err != nil {
		return bulkimport.ReservedFile{}, bulkimport.ErrInvalid
	}
	if !input.IntentionalDuplicate {
		var exists bool
		if err = tx.QueryRow(ctx, `select exists(select 1 from private.bulk_import_files where user_id=$1 and (declared_sha256=$2 or verified_sha256=$2))`, userID, checksum).Scan(&exists); err != nil {
			return bulkimport.ReservedFile{}, err
		}
		if exists {
			return bulkimport.ReservedFile{}, bulkimport.ErrDuplicateFile
		}
	}
	documentID, scopeID, fileID := uuid.New(), uuid.New(), uuid.New()
	extension := extensionForMIME(input.MIMEType)
	path := userID.String() + "/" + scopeID.String() + "/" + fileID.String() + "." + extension
	if _, err = tx.Exec(ctx, `insert into private.bulk_import_documents(id,user_id,batch_id,source_scope_id,sort_order,status) values($1,$2,$3,$4,$5,'draft')`, documentID, userID, batchID, scopeID, fileCount); err != nil {
		return bulkimport.ReservedFile{}, mapError(err)
	}
	var file bulkimport.File
	err = tx.QueryRow(ctx, `insert into private.bulk_import_files(id,user_id,batch_id,document_id,sort_order,display_filename,declared_mime_type,declared_byte_size,declared_sha256,storage_object_path,reservation_expires_at) values($1,$2,$3,$4,0,$5,$6,$7,$8,$9,$10) returning id,document_id,sort_order,display_filename,declared_mime_type,declared_byte_size,encode(declared_sha256,'hex'),status,reservation_expires_at,finalized_at`, fileID, userID, batchID, documentID, input.DisplayFilename, input.MIMEType, input.ByteSize, checksum, path, expires).Scan(&file.ID, &file.DocumentID, &file.SortOrder, &file.DisplayFilename, &file.DeclaredMIME, &file.DeclaredBytes, &file.DeclaredSHA256, &file.Status, &file.ReservationExpiresAt, &file.FinalizedAt)
	if err != nil {
		return bulkimport.ReservedFile{}, mapError(err)
	}
	_, err = tx.Exec(ctx, `update public.bulk_import_batches set file_count=file_count+1,document_count=document_count+1 where id=$1 and user_id=$2`, batchID, userID)
	if err != nil {
		return bulkimport.ReservedFile{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return bulkimport.ReservedFile{}, mapError(err)
	}
	return bulkimport.ReservedFile{File: file, SourceScopeID: scopeID, ObjectPath: path}, nil
}

func (s *Store) MarkReservationFailed(ctx context.Context, userID, fileID uuid.UUID, message string) error {
	_, err := s.pool.Exec(ctx, `update private.bulk_import_files set status='failed',error_summary=left($3,2000) where id=$1 and user_id=$2 and status='reserved'`, fileID, userID, message)
	return err
}
func (s *Store) FinalizeFile(ctx context.Context, userID, fileID uuid.UUID, metadata bulkimport.ObjectMetadata) (bulkimport.File, error) {
	var f bulkimport.File
	err := s.pool.QueryRow(ctx, `update private.bulk_import_files set status='uploaded',finalized_at=now(),error_summary=null where id=$1 and user_id=$2 and status in ('reserved','uploaded') and reservation_expires_at>now() returning id,document_id,sort_order,display_filename,declared_mime_type,declared_byte_size,encode(declared_sha256,'hex'),status,reservation_expires_at,finalized_at`, fileID, userID).Scan(&f.ID, &f.DocumentID, &f.SortOrder, &f.DisplayFilename, &f.DeclaredMIME, &f.DeclaredBytes, &f.DeclaredSHA256, &f.Status, &f.ReservationExpiresAt, &f.FinalizedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return f, bulkimport.ErrConflict
	}
	return f, err
}

func (s *Store) ReplaceDocumentLayout(ctx context.Context, userID, batchID uuid.UUID, layout []bulkimport.DocumentLayout) (bulkimport.Batch, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return bulkimport.Batch{}, err
	}
	defer tx.Rollback(ctx)
	var status string
	if err = tx.QueryRow(ctx, `select status from public.bulk_import_batches where id=$1 and user_id=$2 for update`, batchID, userID).Scan(&status); errors.Is(err, pgx.ErrNoRows) {
		return bulkimport.Batch{}, bulkimport.ErrNotFound
	}
	if err != nil {
		return bulkimport.Batch{}, err
	}
	if status != "draft" {
		return bulkimport.Batch{}, bulkimport.ErrConflict
	}
	var count int
	if err = tx.QueryRow(ctx, `select count(*) from private.bulk_import_files where batch_id=$1 and user_id=$2`, batchID, userID).Scan(&count); err != nil {
		return bulkimport.Batch{}, err
	}
	listed := 0
	for _, doc := range layout {
		listed += len(doc.FileIDs)
	}
	if listed != count {
		return bulkimport.Batch{}, bulkimport.ErrInvalid
	}
	var documentTemporaryBase, fileTemporaryBase int
	if err = tx.QueryRow(ctx, `select coalesce(max(sort_order),-1)+$3 from private.bulk_import_documents where batch_id=$1 and user_id=$2`, batchID, userID, len(layout)+1).Scan(&documentTemporaryBase); err != nil {
		return bulkimport.Batch{}, err
	}
	if err = tx.QueryRow(ctx, `select coalesce(max(sort_order),-1)+$3 from private.bulk_import_files where batch_id=$1 and user_id=$2`, batchID, userID, count+1).Scan(&fileTemporaryBase); err != nil {
		return bulkimport.Batch{}, err
	}
	for _, doc := range layout {
		if len(doc.FileIDs) > 1 {
			var pdfs int
			filePlaceholders, fileArguments := scalarUUIDList(2, []any{userID}, doc.FileIDs)
			if err = tx.QueryRow(ctx, `select count(*) from private.bulk_import_files where user_id=$1 and id in (`+filePlaceholders+`) and declared_mime_type='application/pdf'`, fileArguments...).Scan(&pdfs); err != nil {
				return bulkimport.Batch{}, err
			}
			if pdfs > 0 {
				return bulkimport.Batch{}, bulkimport.ErrInvalid
			}
		}
	}
	temporaryFileIndex := 0
	for docIndex, doc := range layout {
		temporaryOrder, orderErr := temporarySortOrder(documentTemporaryBase, docIndex)
		if orderErr != nil {
			return bulkimport.Batch{}, bulkimport.ErrInvalid
		}
		command, execErr := tx.Exec(ctx, `update private.bulk_import_documents set sort_order=$4,display_label=nullif(btrim($5),'') where id=$1 and user_id=$2 and batch_id=$3`, doc.DocumentID, userID, batchID, temporaryOrder, doc.Label)
		if execErr != nil || command.RowsAffected() != 1 {
			return bulkimport.Batch{}, bulkimport.ErrInvalid
		}
		for _, fileID := range doc.FileIDs {
			temporaryOrder, orderErr = temporarySortOrder(fileTemporaryBase, temporaryFileIndex)
			if orderErr != nil {
				return bulkimport.Batch{}, bulkimport.ErrInvalid
			}
			command, execErr = tx.Exec(ctx, `update private.bulk_import_files set document_id=$4,sort_order=$5 where id=$1 and user_id=$2 and batch_id=$3`, fileID, userID, batchID, doc.DocumentID, temporaryOrder)
			if execErr != nil || command.RowsAffected() != 1 {
				return bulkimport.Batch{}, bulkimport.ErrInvalid
			}
			temporaryFileIndex++
		}
	}
	if _, err = tx.Exec(ctx, `delete from private.bulk_import_documents d where d.batch_id=$1 and d.user_id=$2 and not exists(select 1 from private.bulk_import_files f where f.document_id=d.id)`, batchID, userID); err != nil {
		return bulkimport.Batch{}, err
	}
	for docIndex, doc := range layout {
		if _, err = tx.Exec(ctx, `update private.bulk_import_documents set sort_order=$3 where id=$1 and user_id=$2`, doc.DocumentID, userID, docIndex); err != nil {
			return bulkimport.Batch{}, err
		}
		for fileIndex, fileID := range doc.FileIDs {
			if _, err = tx.Exec(ctx, `update private.bulk_import_files set sort_order=$3 where id=$1 and user_id=$2`, fileID, userID, fileIndex); err != nil {
				return bulkimport.Batch{}, err
			}
		}
	}
	if _, err = tx.Exec(ctx, `update public.bulk_import_batches set document_count=$3 where id=$1 and user_id=$2`, batchID, userID, len(layout)); err != nil {
		return bulkimport.Batch{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return bulkimport.Batch{}, mapError(err)
	}
	return s.GetBatch(ctx, userID, batchID)
}

func temporarySortOrder(base, index int) (int, error) {
	if base < 0 || index < 0 || base > math.MaxInt32-index {
		return 0, bulkimport.ErrInvalid
	}
	return base + index, nil
}

func (s *Store) SubmitBatch(ctx context.Context, userID, batchID uuid.UUID) (bulkimport.Batch, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return bulkimport.Batch{}, err
	}
	defer tx.Rollback(ctx)
	var status string
	var documentType bulkimport.DocumentType
	if err = tx.QueryRow(ctx, `select status,document_type_snapshot from public.bulk_import_batches where id=$1 and user_id=$2 for update`, batchID, userID).Scan(&status, &documentType); errors.Is(err, pgx.ErrNoRows) {
		return bulkimport.Batch{}, bulkimport.ErrNotFound
	}
	if err != nil {
		return bulkimport.Batch{}, err
	}
	if status != "draft" {
		if err = tx.Commit(ctx); err != nil {
			return bulkimport.Batch{}, err
		}
		return s.GetBatch(ctx, userID, batchID)
	}
	var unready, count int
	if err = tx.QueryRow(ctx, `select count(*) filter(where status<>'uploaded'),count(*) from private.bulk_import_files where batch_id=$1 and user_id=$2`, batchID, userID).Scan(&unready, &count); err != nil {
		return bulkimport.Batch{}, err
	}
	if count == 0 || unready > 0 {
		return bulkimport.Batch{}, bulkimport.ErrConflict
	}
	rows, err := tx.Query(ctx, `select id,source_scope_id,sort_order,coalesce(display_label,'') from private.bulk_import_documents where batch_id=$1 and user_id=$2 order by sort_order for update`, batchID, userID)
	if err != nil {
		return bulkimport.Batch{}, err
	}
	type doc struct {
		id, scope uuid.UUID
		order     int
		label     string
	}
	docs := []doc{}
	for rows.Next() {
		var d doc
		if err = rows.Scan(&d.id, &d.scope, &d.order, &d.label); err != nil {
			rows.Close()
			return bulkimport.Batch{}, err
		}
		docs = append(docs, d)
	}
	rows.Close()
	for _, d := range docs {
		var raw string
		if err = tx.QueryRow(ctx, `select jsonb_build_object('batch_id',$1,'document_id',$2,'document_type',$3,'attachments',coalesce(jsonb_agg(jsonb_build_object('filename',display_filename,'mime_type',declared_mime_type,'object_path',storage_object_path,'storage_status','stored','parse_eligible',true) order by sort_order),'[]'::jsonb))::text from private.bulk_import_files where document_id=$2 and user_id=$4`, batchID, d.id, documentType, userID).Scan(&raw); err != nil {
			return bulkimport.Batch{}, err
		}
		if _, err = tx.Exec(ctx, `insert into private.data_sources(id,user_id,source_type,provider,received_at,raw_data,parse_status) values($1,$2,'bulk_upload_document','user_upload',now(),$3::jsonb,'pending')`, d.scope, userID, raw); err != nil {
			return bulkimport.Batch{}, mapError(err)
		}
		if _, err = tx.Exec(ctx, `update private.bulk_import_documents set data_source_id=source_scope_id,status='queued' where id=$1 and user_id=$2`, d.id, userID); err != nil {
			return bulkimport.Batch{}, err
		}
		if _, err = tx.Exec(ctx, `insert into private.transaction_jobs(user_id,data_source_id,job_type,bulk_import_batch_id,bulk_import_document_id,attempt_generation,payload) values($1,$2,'bulk_document_prepare',$3,$4,1,'{}') on conflict do nothing`, userID, d.scope, batchID, d.id); err != nil {
			return bulkimport.Batch{}, mapError(err)
		}
	}
	if _, err = tx.Exec(ctx, `update public.bulk_import_batches set status='queued' where id=$1 and user_id=$2`, batchID, userID); err != nil {
		return bulkimport.Batch{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return bulkimport.Batch{}, mapError(err)
	}
	return s.GetBatch(ctx, userID, batchID)
}

func (s *Store) CancelBatch(ctx context.Context, userID, batchID uuid.UUID) (bulkimport.Batch, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return bulkimport.Batch{}, err
	}
	defer tx.Rollback(ctx)
	var status string
	if err = tx.QueryRow(ctx, `select status from public.bulk_import_batches where id=$1 and user_id=$2 for update`, batchID, userID).Scan(&status); errors.Is(err, pgx.ErrNoRows) {
		return bulkimport.Batch{}, bulkimport.ErrNotFound
	}
	if err != nil {
		return bulkimport.Batch{}, err
	}
	active := false
	switch status {
	case "queued", "running":
		if err = tx.QueryRow(ctx, `select exists(select 1 from private.transaction_jobs where bulk_import_batch_id=$1 and user_id=$2 and status='running') or exists(select 1 from private.bulk_import_documents where batch_id=$1 and user_id=$2 and status in ('preparing','parsing','aggregating','reconciling'))`, batchID, userID).Scan(&active); err != nil {
			return bulkimport.Batch{}, err
		}
	}
	targetStatus, targetErr := cancellationTarget(status, active)
	if targetErr != nil {
		return bulkimport.Batch{}, targetErr
	}
	if targetStatus == "cancelled" {
		_, err = tx.Exec(ctx, `update public.bulk_import_batches set status='cancelled',cancel_requested_at=coalesce(cancel_requested_at,now()),completed_at=coalesce(completed_at,now()) where id=$1 and user_id=$2`, batchID, userID)
	} else if targetStatus == "cancelling" {
		_, err = tx.Exec(ctx, `update public.bulk_import_batches set status='cancelling',cancel_requested_at=coalesce(cancel_requested_at,now()),completed_at=null where id=$1 and user_id=$2`, batchID, userID)
	}
	if err != nil {
		return bulkimport.Batch{}, err
	}
	if _, err = tx.Exec(ctx, `update private.transaction_jobs set status='cancelled',completed_at=now() where bulk_import_batch_id=$1 and user_id=$2 and status='queued'`, batchID, userID); err != nil {
		return bulkimport.Batch{}, err
	}
	if targetStatus == "cancelled" {
		if _, err = tx.Exec(ctx, `update private.bulk_import_documents set status='cancelled',completed_at=coalesce(completed_at,now()) where batch_id=$1 and user_id=$2 and status not in ('completed','completed_with_errors','failed','cancelled')`, batchID, userID); err != nil {
			return bulkimport.Batch{}, err
		}
	} else if targetStatus == "cancelling" {
		if _, err = tx.Exec(ctx, `update private.bulk_import_documents set status='cancelled',completed_at=coalesce(completed_at,now()) where batch_id=$1 and user_id=$2 and status='queued'`, batchID, userID); err != nil {
			return bulkimport.Batch{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return bulkimport.Batch{}, err
	}
	return s.GetBatch(ctx, userID, batchID)
}

func cancellationTarget(status string, active bool) (string, error) {
	switch status {
	case "draft":
		return "cancelled", nil
	case "queued", "running":
		if active {
			return "cancelling", nil
		}
		return "cancelled", nil
	case "cancelling", "cancelled":
		return status, nil
	default:
		return "", bulkimport.ErrConflict
	}
}

func (s *Store) RetryDocument(ctx context.Context, userID, documentID uuid.UUID) (bulkimport.Document, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return bulkimport.Document{}, err
	}
	defer tx.Rollback(ctx)
	var doc bulkimport.Document
	var documentType bulkimport.DocumentType
	err = tx.QueryRow(ctx, `select d.id,d.batch_id,d.source_scope_id,d.data_source_id,d.sort_order,coalesce(d.display_label,''),d.status,d.attempt_generation,d.page_count,b.document_type_snapshot from private.bulk_import_documents d join public.bulk_import_batches b on b.id=d.batch_id and b.user_id=d.user_id where d.id=$1 and d.user_id=$2 for update of d`, documentID, userID).Scan(&doc.ID, &doc.BatchID, &doc.SourceScopeID, &doc.DataSourceID, &doc.SortOrder, &doc.DisplayLabel, &doc.Status, &doc.AttemptGeneration, &doc.PageCount, &documentType)
	if errors.Is(err, pgx.ErrNoRows) {
		return doc, bulkimport.ErrNotFound
	}
	if err != nil {
		return doc, err
	}
	if doc.Status != bulkimport.DocumentFailed && doc.Status != bulkimport.DocumentCompletedWithErrors {
		return doc, bulkimport.ErrConflict
	}
	if documentType == bulkimport.DocumentCreditCardBill {
		var retained bool
		if err = tx.QueryRow(ctx, `select exists(select 1 from private.credit_card_statements where bulk_document_id=$1)`, documentID).Scan(&retained); err != nil {
			return doc, err
		}
		if retained {
			return doc, bulkimport.ErrConflict
		}
	}
	doc.AttemptGeneration++
	if _, err = tx.Exec(ctx, `update private.bulk_import_documents set status='queued',attempt_generation=$3,error_summary=null,started_at=null,completed_at=null where id=$1 and user_id=$2`, documentID, userID, doc.AttemptGeneration); err != nil {
		return doc, err
	}
	if doc.DataSourceID == nil {
		return doc, bulkimport.ErrConflict
	}
	if _, err = tx.Exec(ctx, `insert into private.transaction_jobs(user_id,data_source_id,job_type,bulk_import_batch_id,bulk_import_document_id,attempt_generation,payload) values($1,$2,'bulk_document_prepare',$3,$4,$5,'{}') on conflict do nothing`, userID, *doc.DataSourceID, doc.BatchID, documentID, doc.AttemptGeneration); err != nil {
		return doc, mapError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return doc, err
	}
	doc.Status = bulkimport.DocumentQueued
	return doc, nil
}

func (s *Store) DeleteDocument(ctx context.Context, userID, documentID uuid.UUID) error {
	if s.deletion == nil {
		return errors.New("bulk deletion coordinator is not configured")
	}
	return s.deletion.DeleteBulkDocument(ctx, userID, documentID)
}
func (s *Store) DeleteBatch(ctx context.Context, userID, batchID uuid.UUID) error {
	if s.deletion == nil {
		return errors.New("bulk deletion coordinator is not configured")
	}
	return s.deletion.DeleteBulkBatch(ctx, userID, batchID)
}
func (s *Store) ResolveCandidate(ctx context.Context, userID, candidateID uuid.UUID, resolution bulkimport.CandidateResolution) (bulkimport.Candidate, error) {
	if s.actions == nil {
		return bulkimport.Candidate{}, errors.New("bulk candidate actions are not configured")
	}
	var kind bulkimport.DocumentType
	err := s.pool.QueryRow(ctx, `select b.document_type_snapshot from private.bulk_import_candidates c join public.bulk_import_batches b on b.id=c.batch_id and b.user_id=c.user_id where c.id=$1 and c.user_id=$2`, candidateID, userID).Scan(&kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return bulkimport.Candidate{}, bulkimport.ErrNotFound
	}
	if err != nil {
		return bulkimport.Candidate{}, err
	}
	if kind == bulkimport.DocumentCreditCardBill {
		return bulkimport.Candidate{}, bulkimport.ErrReadOnlyCandidate
	}
	return s.actions.ResolveBulkCandidate(ctx, userID, candidateID, resolution)
}

func (s *Store) ListCandidates(ctx context.Context, userID, batchID uuid.UUID) ([]bulkimport.Candidate, error) {
	rows, err := s.pool.Query(ctx, `select id,batch_id,document_id,attempt_generation,output_ordinal,encode(fingerprint,'hex'),parsed_candidate,account_id,status,transaction_id,duplicate_of_candidate_id,reconciliation_reason from private.bulk_import_candidates where batch_id=$1 and user_id=$2 order by document_id,attempt_generation desc,output_ordinal`, batchID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []bulkimport.Candidate{}
	for rows.Next() {
		var c bulkimport.Candidate
		if err = rows.Scan(&c.ID, &c.BatchID, &c.DocumentID, &c.AttemptGeneration, &c.Ordinal, &c.Fingerprint, &c.ParsedCandidate, &c.AccountID, &c.Status, &c.TransactionID, &c.DuplicateOfID, &c.Reason); err != nil {
			return nil, err
		}
		items = append(items, c)
	}
	return items, rows.Err()
}
func (s *Store) LoadPromptPreview(ctx context.Context, userID, templateID uuid.UUID, override []uuid.UUID) (bulkimport.Template, []bulkimport.AccountSelection, error) {
	items, err := s.ListTemplates(ctx, userID, true)
	if err != nil {
		return bulkimport.Template{}, nil, err
	}
	var template bulkimport.Template
	found := false
	for _, item := range items {
		if item.ID == templateID {
			template = item
			found = true
			break
		}
	}
	if !found {
		return template, nil, bulkimport.ErrNotFound
	}
	if len(override) == 0 {
		return template, template.Accounts, nil
	}
	accounts := make([]bulkimport.AccountSelection, 0, len(override))
	for index, id := range override {
		var a bulkimport.AccountSelection
		err = s.pool.QueryRow(ctx, `select id,name,institution_name,account_type from public.accounts where id=$1 and user_id=$2 and deleted_at is null`, id, userID).Scan(&a.AccountID, &a.Name, &a.InstitutionName, &a.AccountType)
		if err != nil {
			return template, nil, bulkimport.ErrInvalid
		}
		a.AccountRef = fmt.Sprintf("account_%d", index+1)
		a.SortOrder = index
		accounts = append(accounts, a)
	}
	return template, accounts, nil
}
func (s *Store) ListDebugAttempts(ctx context.Context, userID, sourceID uuid.UUID) ([]bulkimport.DebugAttempt, error) {
	const previewLimit = 16 * 1024
	rows, err := s.pool.Query(ctx, `
		select a.id,c.id,c.chunk_index,c.attempt_generation,a.model_name,a.validation_status,
			case when char_length(a.request_metadata::text)>$3 then '{}'::jsonb else a.request_metadata end,
			case when a.parsed_candidate is not null and char_length(a.parsed_candidate::text)>$3 then null else a.parsed_candidate end,
			left(a.assembled_system_prompt,$3),left(a.normalized_input,$3),left(a.provider_request::text,$3),
			left(a.provider_response::text,$3),left(a.model_output::text,$3),
			case when char_length(a.prompt_components::text)>$3 then '{}'::jsonb else a.prompt_components end,
			a.error_summary,a.started_at,a.completed_at,a.created_at,
			char_length(a.request_metadata::text)>$3,
			a.parsed_candidate is not null and char_length(a.parsed_candidate::text)>$3,
			char_length(coalesce(a.assembled_system_prompt,''))>$3,char_length(coalesce(a.normalized_input,''))>$3,
			char_length(coalesce(a.provider_request::text,''))>$3,char_length(coalesce(a.provider_response::text,''))>$3,
			char_length(coalesce(a.model_output::text,''))>$3,char_length(a.prompt_components::text)>$3
		from private.source_parse_attempts a
		join private.bulk_import_chunks c on c.id=a.bulk_import_chunk_id and c.user_id=a.user_id
		where a.user_id=$1 and a.data_source_id=$2
		order by a.created_at desc,a.id desc limit 100`, userID, sourceID, previewLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []bulkimport.DebugAttempt{}
	for rows.Next() {
		var item bulkimport.DebugAttempt
		truncated := make([]bool, 8)
		if err = rows.Scan(&item.ID, &item.ChunkID, &item.ChunkIndex, &item.Generation, &item.ModelName, &item.Status,
			&item.Metadata, &item.ParsedCandidate, &item.AssembledSystemPrompt, &item.NormalizedInput,
			&item.ProviderRequest, &item.ProviderResponse, &item.ModelOutput, &item.PromptComponents,
			&item.ErrorSummary, &item.StartedAt, &item.CompletedAt, &item.CreatedAt,
			&truncated[0], &truncated[1], &truncated[2], &truncated[3], &truncated[4], &truncated[5], &truncated[6], &truncated[7]); err != nil {
			return nil, err
		}
		fields := []string{"request_metadata", "parsed_candidate", "assembled_system_prompt", "normalized_input", "provider_request", "provider_response", "model_output", "prompt_components"}
		item.TruncatedFields = []string{}
		for index, value := range truncated {
			if value {
				item.TruncatedFields = append(item.TruncatedFields, fields[index])
			}
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) GetDebugAttemptField(ctx context.Context, userID, sourceID, attemptID uuid.UUID, field string) (bulkimport.DebugField, error) {
	expression, maxBytes, ok := bulkDebugFieldSpec(field)
	if !ok {
		return bulkimport.DebugField{}, bulkimport.ErrInvalid
	}
	query := fmt.Sprintf(`select case when octet_length(%[1]s)<=$4 then %[1]s else null end,%[1]s is null,coalesce(octet_length(%[1]s),0) from private.source_parse_attempts a join private.bulk_import_chunks c on c.id=a.bulk_import_chunk_id and c.user_id=a.user_id where a.user_id=$1 and a.data_source_id=$2 and a.id=$3`, expression)
	var value *string
	var isNull bool
	var byteSize int
	err := s.pool.QueryRow(ctx, query, userID, sourceID, attemptID, maxBytes).Scan(&value, &isNull, &byteSize)
	if errors.Is(err, pgx.ErrNoRows) {
		return bulkimport.DebugField{}, bulkimport.ErrNotFound
	}
	if err != nil {
		return bulkimport.DebugField{}, err
	}
	if byteSize > maxBytes {
		return bulkimport.DebugField{}, bulkimport.ErrInvalid
	}
	if isNull {
		value = nil
	}
	return bulkimport.DebugField{SourceID: sourceID, AttemptID: attemptID, Field: field, Value: value, MaxBytes: maxBytes}, nil
}

func bulkDebugFieldSpec(field string) (string, int, bool) {
	switch field {
	case "request_metadata":
		return "a.request_metadata::text", 65536, true
	case "parsed_candidate":
		return "a.parsed_candidate::text", 2097152, true
	case "assembled_system_prompt":
		return "a.assembled_system_prompt", 65536, true
	case "normalized_input":
		return "a.normalized_input", 262144, true
	case "provider_request":
		return "a.provider_request::text", 10485760, true
	case "provider_response":
		return "a.provider_response::text", 2097152, true
	case "model_output":
		return "a.model_output::text", 2097152, true
	case "prompt_components":
		return "a.prompt_components::text", 65536, true
	default:
		return "", 0, false
	}
}

func (s *Store) LoadDocumentEvidence(ctx context.Context, userID, documentID uuid.UUID) ([]bulkimport.EvidenceObject, error) {
	rows, err := s.pool.Query(ctx, `select f.id,d.source_scope_id,f.display_filename,coalesce(f.verified_mime_type,f.declared_mime_type),coalesce(f.verified_byte_size,f.declared_byte_size),encode(coalesce(f.verified_sha256,f.declared_sha256),'hex'),f.storage_object_path from private.bulk_import_documents d join private.bulk_import_files f on f.document_id=d.id and f.user_id=d.user_id where d.id=$1 and d.user_id=$2 and f.status in ('uploaded','verified') and f.finalized_at is not null order by f.sort_order,f.id`, documentID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]bulkimport.EvidenceObject, 0)
	for rows.Next() {
		var item bulkimport.EvidenceObject
		if err = rows.Scan(&item.ID, &item.SourceScopeID, &item.Filename, &item.MIMEType, &item.ByteSize, &item.SHA256, &item.ObjectPath); err != nil {
			return nil, err
		}
		item.SourceScopeID, err = attachmentstorage.ScopeIDFromObjectPath(userID, item.ObjectPath)
		if err != nil {
			return nil, fmt.Errorf("validate bulk evidence storage scope: %w", err)
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(items) == 0 {
		var exists bool
		if err = s.pool.QueryRow(ctx, `select exists(select 1 from private.bulk_import_documents where id=$1 and user_id=$2)`, documentID, userID).Scan(&exists); err != nil {
			return nil, err
		}
		if !exists {
			return nil, bulkimport.ErrNotFound
		}
	}
	return items, nil
}

func validateAccounts(ctx context.Context, tx pgx.Tx, userID uuid.UUID, ids []uuid.UUID, documentType bulkimport.DocumentType) error {
	if len(ids) == 0 || (documentType == bulkimport.DocumentCreditCardBill && len(ids) != 1) {
		return bulkimport.ErrInvalid
	}
	accountPlaceholders, accountArguments := scalarUUIDList(2, []any{userID}, ids)
	rows, err := tx.Query(ctx, `select id,account_type from public.accounts where user_id=$1 and id in (`+accountPlaceholders+`) and deleted_at is null for share`, accountArguments...)
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id uuid.UUID
		var kind string
		if err = rows.Scan(&id, &kind); err != nil {
			return err
		}
		count++
		if documentType == bulkimport.DocumentCreditCardBill && kind != "credit_card" {
			return bulkimport.ErrInvalid
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if count != len(ids) {
		return bulkimport.ErrInvalid
	}
	return nil
}

// scalarUUIDList avoids passing a UUID slice as one parameter. The database
// connection uses pgx simple protocol for Supavisor transaction pooling, where
// []uuid.UUID has no unambiguous text encoding unless PostgreSQL has already
// supplied a parameter OID. Individual UUID parameters remain type-safe and
// work in both simple and extended query modes.
func scalarUUIDList(firstParameter int, leading []any, ids []uuid.UUID) (string, []any) {
	placeholders := make([]string, len(ids))
	arguments := make([]any, 0, len(leading)+len(ids))
	arguments = append(arguments, leading...)
	for index, id := range ids {
		placeholders[index] = fmt.Sprintf("$%d", firstParameter+index)
		arguments = append(arguments, id)
	}
	return strings.Join(placeholders, ","), arguments
}

func (s *Store) templateAccounts(ctx context.Context, userID, templateID uuid.UUID) ([]bulkimport.AccountSelection, error) {
	rows, err := s.pool.Query(ctx, `select x.account_id,x.sort_order,a.name,a.institution_name,a.account_type from private.bulk_import_template_accounts x join public.accounts a on a.id=x.account_id and a.user_id=x.user_id where x.template_id=$1 and x.user_id=$2 order by x.sort_order`, templateID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []bulkimport.AccountSelection{}
	for rows.Next() {
		var a bulkimport.AccountSelection
		if err = rows.Scan(&a.AccountID, &a.SortOrder, &a.Name, &a.InstitutionName, &a.AccountType); err != nil {
			return nil, err
		}
		a.AccountRef = fmt.Sprintf("account_%d", a.SortOrder+1)
		items = append(items, a)
	}
	return items, rows.Err()
}
func (s *Store) batchAccounts(ctx context.Context, userID, batchID uuid.UUID) ([]bulkimport.AccountSelection, error) {
	rows, err := s.pool.Query(ctx, `select account_id,account_ref,sort_order,account_name,institution_name,account_type from private.bulk_import_batch_accounts where batch_id=$1 and user_id=$2 order by sort_order`, batchID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []bulkimport.AccountSelection{}
	for rows.Next() {
		var a bulkimport.AccountSelection
		if err = rows.Scan(&a.AccountID, &a.AccountRef, &a.SortOrder, &a.Name, &a.InstitutionName, &a.AccountType); err != nil {
			return nil, err
		}
		items = append(items, a)
	}
	return items, rows.Err()
}
func (s *Store) documents(ctx context.Context, userID, batchID uuid.UUID) ([]bulkimport.Document, error) {
	rows, err := s.pool.Query(ctx, `select id,batch_id,source_scope_id,data_source_id,sort_order,coalesce(display_label,''),status,attempt_generation,page_count,candidate_count,created_count,attached_count,review_count,failed_count,duplicate_count,document_summary from private.bulk_import_documents where batch_id=$1 and user_id=$2 order by sort_order`, batchID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []bulkimport.Document{}
	for rows.Next() {
		var d bulkimport.Document
		if err = rows.Scan(&d.ID, &d.BatchID, &d.SourceScopeID, &d.DataSourceID, &d.SortOrder, &d.DisplayLabel, &d.Status, &d.AttemptGeneration, &d.PageCount, &d.CandidateCount, &d.CreatedCount, &d.AttachedCount, &d.ReviewCount, &d.FailedCount, &d.DuplicateCount, &d.DocumentSummary); err != nil {
			return nil, err
		}
		fileRows, qerr := s.pool.Query(ctx, `select id,document_id,sort_order,display_filename,declared_mime_type,declared_byte_size,encode(declared_sha256,'hex'),status,reservation_expires_at,finalized_at from private.bulk_import_files where document_id=$1 and user_id=$2 order by sort_order`, d.ID, userID)
		if qerr != nil {
			return nil, qerr
		}
		for fileRows.Next() {
			var f bulkimport.File
			if qerr = fileRows.Scan(&f.ID, &f.DocumentID, &f.SortOrder, &f.DisplayFilename, &f.DeclaredMIME, &f.DeclaredBytes, &f.DeclaredSHA256, &f.Status, &f.ReservationExpiresAt, &f.FinalizedAt); qerr != nil {
				fileRows.Close()
				return nil, qerr
			}
			d.Files = append(d.Files, f)
		}
		qerr = fileRows.Err()
		fileRows.Close()
		if qerr != nil {
			return nil, qerr
		}
		items = append(items, d)
	}
	return items, rows.Err()
}
func batchFields(b *bulkimport.Batch) []any {
	return []any{&b.ID, &b.TemplateID, &b.TemplateVersion, &b.TitleSnapshot, &b.DocumentTypeSnapshot, &b.ParsingPromptSnapshot, &b.Status, &b.Counters.Files, &b.Counters.Documents, &b.Counters.Pages, &b.Counters.ParsedCandidates, &b.Counters.Created, &b.Counters.Attached, &b.Counters.Review, &b.Counters.Failed, &b.Counters.Duplicates, &b.CancelRequestedAt, &b.ErrorSummary, &b.StartedAt, &b.CompletedAt, &b.CreatedAt, &b.UpdatedAt}
}

type scanner interface{ Scan(...any) error }

func scanBatch(row scanner, b *bulkimport.Batch) error { return row.Scan(batchFields(b)...) }
func accountSelections(ids []uuid.UUID) []bulkimport.AccountSelection {
	items := make([]bulkimport.AccountSelection, len(ids))
	for i, id := range ids {
		items[i] = bulkimport.AccountSelection{AccountID: id, SortOrder: i}
	}
	return items
}
func extensionForMIME(value string) string {
	switch value {
	case "application/pdf":
		return "pdf"
	case "image/bmp":
		return "bmp"
	case "image/jpeg":
		return "jpg"
	case "image/png":
		return "png"
	case "image/tiff":
		return "tiff"
	case "image/webp":
		return "webp"
	case "image/heic":
		return "heic"
	}
	return "bin"
}
func mapError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505", "23514", "23503":
			return fmt.Errorf("%w: database constraint", bulkimport.ErrConflict)
		}
	}
	return err
}

var _ bulkimport.Store = (*Store)(nil)
var _ = json.Valid
var _ = strings.TrimSpace

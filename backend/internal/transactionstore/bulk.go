package transactionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/zhengteck/wealth-builder/backend/internal/bulkimport"
	"github.com/zhengteck/wealth-builder/backend/internal/bulkparse"
	"github.com/zhengteck/wealth-builder/backend/internal/jobs"
)

type persistedBulkCandidate struct {
	bulkparse.Candidate
	BillLineIndex int                  `json:"bill_line_index"`
	BillLineKind  string               `json:"bill_line_kind"`
	Confidence    float64              `json:"confidence"`
	AutoEligible  bool                 `json:"auto_eligible"`
	Evidence      []bulkparse.Evidence `json:"evidence"`
}

func (s *Store) ReconcileBulkCandidate(ctx context.Context, userID, candidateID uuid.UUID, generation int) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockTransactionUser(ctx, tx, userID); err != nil {
		return err
	}
	row, parsed, err := loadBulkCandidateForUpdate(ctx, tx, userID, candidateID, generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	if row.Status != bulkimport.CandidatePending {
		return tx.Commit(ctx)
	}
	if parsed.PossibleInternalTransfer {
		return finishBulkCandidate(ctx, tx, row, bulkimport.CandidateReview, nil, "possible internal transfer requires review")
	}
	if row.DocumentType == bulkimport.DocumentCreditCardBill {
		lineKind := parsed.creditCardLineKind()
		if !validCreditCardCandidateDirection(lineKind, parsed.TransactionKind) {
			return finishBulkCandidate(ctx, tx, row, bulkimport.CandidateReview, nil, "Credit Card line direction requires review")
		}
	}
	transactionIDs, conflict, err := matchBulkTransactions(ctx, tx, userID, row.AccountID, parsed)
	if err != nil {
		return err
	}
	if conflict || len(transactionIDs) > 1 {
		return finishBulkCandidate(ctx, tx, row, bulkimport.CandidateReview, nil, "multiple or conflicting transaction matches")
	}
	if row.DocumentType == bulkimport.DocumentCreditCardBill {
		if len(transactionIDs) == 1 && parsed.creditCardLineKind() == "payment" {
			qualifying, qualifyErr := isQualifyingCreditCardPayment(ctx, tx, userID, row.AccountID, transactionIDs[0])
			if qualifyErr != nil {
				return qualifyErr
			}
			if !qualifying {
				return finishBulkCandidate(ctx, tx, row, bulkimport.CandidateReview, nil, "payment requires an existing Bank-to-Card transfer credit leg")
			}
		}
		if len(transactionIDs) == 1 {
			if err = attachBulkEvidence(ctx, tx, row, transactionIDs[0], "automatic"); err != nil {
				return err
			}
			return finishBulkCandidate(ctx, tx, row, bulkimport.CandidateAttached, &transactionIDs[0], "unique policy-compliant Credit Card transaction match")
		}
		return finishBulkCandidate(ctx, tx, row, bulkimport.CandidateReview, nil, "unmatched Credit Card statement line requires review")
	}
	if len(transactionIDs) == 1 {
		if err = attachBulkEvidence(ctx, tx, row, transactionIDs[0], "automatic"); err != nil {
			return err
		}
		return finishBulkCandidate(ctx, tx, row, bulkimport.CandidateAttached, &transactionIDs[0], "unique account, amount, direction, currency, and calendar-day match")
	}
	if !parsed.AutoEligible || parsed.Confidence < 0.75 {
		return finishBulkCandidate(ctx, tx, row, bulkimport.CandidateReview, nil, "candidate requires user confirmation")
	}
	transactionID, err := insertBulkTransaction(ctx, tx, row, parsed, row.AccountID, nil, "automatic_source")
	if err != nil {
		return err
	}
	if err = attachBulkEvidence(ctx, tx, row, transactionID, "automatic"); err != nil {
		return err
	}
	return finishBulkCandidate(ctx, tx, row, bulkimport.CandidateCreated, &transactionID, "reliable unmatched candidate")
}

func (candidate persistedBulkCandidate) creditCardLineKind() string {
	if candidate.BillLineKind != "" {
		return candidate.BillLineKind
	}
	return candidate.LineKind
}

func validCreditCardCandidateDirection(lineKind, transactionKind string) bool {
	switch lineKind {
	case "activity", "fee", "interest":
		return transactionKind == "debit"
	case "refund", "payment":
		return transactionKind == "credit"
	default:
		return false
	}
}

func isQualifyingCreditCardPayment(ctx context.Context, tx pgx.Tx, userID, cardAccountID, transactionID uuid.UUID) (bool, error) {
	var qualifying bool
	err := tx.QueryRow(ctx, `
		select exists(
			select 1
			from public.transactions credit
			join public.accounts card on card.id=credit.account_id and card.user_id=credit.user_id
			join private.transaction_links link on link.user_id=credit.user_id and link.credit_transaction_id=credit.id and link.link_type='internal_transfer'
			join public.transactions debit on debit.id=link.debit_transaction_id and debit.user_id=link.user_id
			join public.accounts bank on bank.id=debit.account_id and bank.user_id=debit.user_id
			where credit.id=$1 and credit.user_id=$2 and credit.account_id=$3
				and card.account_type='credit_card' and card.deleted_at is null
				and bank.account_type='bank_account' and bank.deleted_at is null
				and credit.transaction_kind='credit' and debit.transaction_kind='debit'
				and credit.original_currency=debit.original_currency
				and credit.original_amount_minor=debit.original_amount_minor
		)`, transactionID, userID, cardAccountID).Scan(&qualifying)
	return qualifying, err
}

func (s *Store) ResolveBulkCandidate(ctx context.Context, userID, candidateID uuid.UUID, resolution bulkimport.CandidateResolution) (bulkimport.Candidate, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return bulkimport.Candidate{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockTransactionUser(ctx, tx, userID); err != nil {
		return bulkimport.Candidate{}, err
	}
	row, parsed, err := loadBulkCandidateForUpdate(ctx, tx, userID, candidateID, resolution.ExpectedGeneration)
	if errors.Is(err, pgx.ErrNoRows) {
		return bulkimport.Candidate{}, bulkimport.ErrNotFound
	}
	if err != nil {
		return bulkimport.Candidate{}, err
	}
	if row.DocumentType == bulkimport.DocumentCreditCardBill {
		return bulkimport.Candidate{}, bulkimport.ErrReadOnlyCandidate
	}
	if row.Status != bulkimport.CandidateReview && row.Status != bulkimport.CandidatePending {
		return bulkimport.Candidate{}, bulkimport.ErrConflict
	}
	switch resolution.Action {
	case bulkimport.CandidateSetAccount:
		if resolution.AccountID == nil || validateBulkSelectedAccount(ctx, tx, row.BatchID, userID, *resolution.AccountID) != nil {
			return bulkimport.Candidate{}, bulkimport.ErrInvalid
		}
		row.AccountID = *resolution.AccountID
		if _, err = tx.Exec(ctx, `update private.source_candidates set account_id=$3,status='pending_reconciliation',reconciliation_reason=null where id=$1 and user_id=$2`, row.ID, userID, row.AccountID); err != nil {
			return bulkimport.Candidate{}, err
		}
		if err = enqueueBulkCandidateJob(ctx, tx, row); err != nil {
			return bulkimport.Candidate{}, err
		}
	case bulkimport.CandidateAttach:
		if resolution.TransactionID == nil {
			return bulkimport.Candidate{}, bulkimport.ErrInvalid
		}
		if err = attachBulkEvidence(ctx, tx, row, *resolution.TransactionID, "user"); err != nil {
			return bulkimport.Candidate{}, err
		}
		if err = updateBulkCandidateOutcome(ctx, tx, row, bulkimport.CandidateAttached, resolution.TransactionID, "user attached candidate"); err != nil {
			return bulkimport.Candidate{}, err
		}
	case bulkimport.CandidateCreate:
		accountID := row.AccountID
		if resolution.AccountID != nil {
			accountID = *resolution.AccountID
		}
		if err = validateBulkSelectedAccount(ctx, tx, row.BatchID, userID, accountID); err != nil {
			return bulkimport.Candidate{}, bulkimport.ErrInvalid
		}
		transactionID, createErr := insertBulkTransaction(ctx, tx, row, parsed, accountID, resolution.CategoryID, "user_source")
		if createErr != nil {
			return bulkimport.Candidate{}, createErr
		}
		if err = attachBulkEvidence(ctx, tx, row, transactionID, "user"); err != nil {
			return bulkimport.Candidate{}, err
		}
		if err = updateBulkCandidateOutcome(ctx, tx, row, bulkimport.CandidateCreated, &transactionID, "user created transaction"); err != nil {
			return bulkimport.Candidate{}, err
		}
	case bulkimport.CandidateInternalTransfer:
		if resolution.DebitAccountID == nil || resolution.CreditAccountID == nil || *resolution.DebitAccountID == *resolution.CreditAccountID {
			return bulkimport.Candidate{}, bulkimport.ErrInvalid
		}
		if err = validateActiveAccounts(ctx, tx, userID, *resolution.DebitAccountID, *resolution.CreditAccountID); err != nil {
			return bulkimport.Candidate{}, err
		}
		debitID, createErr := insertBulkTransactionWithKind(ctx, tx, row, parsed, *resolution.DebitAccountID, resolution.CategoryID, "user_source", "debit")
		if createErr != nil {
			return bulkimport.Candidate{}, createErr
		}
		creditID, createErr := insertBulkTransactionWithKind(ctx, tx, row, parsed, *resolution.CreditAccountID, resolution.CategoryID, "user_source", "credit")
		if createErr != nil {
			return bulkimport.Candidate{}, createErr
		}
		if _, err = tx.Exec(ctx, `insert into private.transaction_links(user_id,link_type,debit_transaction_id,credit_transaction_id) values($1,'internal_transfer',$2,$3)`, userID, debitID, creditID); err != nil {
			return bulkimport.Candidate{}, err
		}
		if err = attachBulkEvidence(ctx, tx, row, debitID, "user"); err != nil {
			return bulkimport.Candidate{}, err
		}
		if err = attachBulkEvidence(ctx, tx, row, creditID, "user"); err != nil {
			return bulkimport.Candidate{}, err
		}
		if err = updateBulkCandidateOutcome(ctx, tx, row, bulkimport.CandidateCreated, &debitID, "user created internal transfer"); err != nil {
			return bulkimport.Candidate{}, err
		}
	default:
		return bulkimport.Candidate{}, bulkimport.ErrInvalid
	}
	if err = enqueuePostProcessIfReady(ctx, tx, row); err != nil {
		return bulkimport.Candidate{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return bulkimport.Candidate{}, err
	}
	return s.loadBulkCandidate(ctx, userID, candidateID)
}

type bulkCandidateRow struct {
	ID, BatchID, DocumentID, SourceID, AccountID uuid.UUID
	Generation                                   int
	Status                                       bulkimport.CandidateStatus
	DocumentType                                 bulkimport.DocumentType
	Raw                                          json.RawMessage
}

func loadBulkCandidateForUpdate(ctx context.Context, tx pgx.Tx, userID, candidateID uuid.UUID, generation int) (bulkCandidateRow, persistedBulkCandidate, error) {
	var row bulkCandidateRow
	err := tx.QueryRow(ctx, `select c.id,c.batch_id,c.document_id,c.data_source_id,c.account_id,c.attempt_generation,c.status,b.document_type_snapshot,c.parsed_candidate from private.source_candidates c join public.bulk_import_batches b on b.id=c.batch_id and b.user_id=c.user_id where c.id=$1 and c.user_id=$2 and c.attempt_generation=$3 for update of c`, candidateID, userID, generation).Scan(&row.ID, &row.BatchID, &row.DocumentID, &row.SourceID, &row.AccountID, &row.Generation, &row.Status, &row.DocumentType, &row.Raw)
	if err != nil {
		return row, persistedBulkCandidate{}, err
	}
	var parsed persistedBulkCandidate
	if err = json.Unmarshal(row.Raw, &parsed); err != nil {
		return row, parsed, err
	}
	if parsed.OccurredOn == "" {
		return row, parsed, errors.New("bulk candidate date is missing")
	}
	return row, parsed, nil
}

func matchBulkTransactions(ctx context.Context, tx pgx.Tx, userID, accountID uuid.UUID, parsed persistedBulkCandidate) ([]uuid.UUID, bool, error) {
	rows, err := tx.Query(ctx, `select id,line_items from public.transactions where user_id=$1 and account_id=$2 and transaction_kind=$3 and original_amount_minor=$4 and (original_currency=$5 or original_currency='') and (occurred_at at time zone 'UTC')::date=$6::date order by id limit 3`, userID, accountID, parsed.TransactionKind, parsed.OriginalAmountMinor, parsed.OriginalCurrency, parsed.OccurredOn)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	ids := make([]uuid.UUID, 0, 3)
	conflict := false
	parsedLines, _ := json.Marshal(parsed.LineItems)
	for rows.Next() {
		var id uuid.UUID
		var lineItems []byte
		if err = rows.Scan(&id, &lineItems); err != nil {
			return nil, false, err
		}
		if len(parsed.LineItems) > 0 && string(lineItems) != "[]" && !jsonEqual(parsedLines, lineItems) {
			conflict = true
		}
		ids = append(ids, id)
	}
	return ids, conflict, rows.Err()
}

func insertBulkTransaction(ctx context.Context, tx pgx.Tx, row bulkCandidateRow, parsed persistedBulkCandidate, accountID uuid.UUID, categoryID *uuid.UUID, creationMethod string) (uuid.UUID, error) {
	return insertBulkTransactionWithKind(ctx, tx, row, parsed, accountID, categoryID, creationMethod, parsed.TransactionKind)
}

func insertBulkTransactionWithKind(ctx context.Context, tx pgx.Tx, row bulkCandidateRow, parsed persistedBulkCandidate, accountID uuid.UUID, categoryID *uuid.UUID, creationMethod, kind string) (uuid.UUID, error) {
	if categoryID == nil {
		var err error
		categoryID, err = (&Store{}).resolveCategoryLeaf(ctx, tx, parsed.CategoryLeafName)
		if err != nil {
			return uuid.Nil, err
		}
	} else if err := validatePatchedCategory(ctx, tx, *categoryID); err != nil {
		return uuid.Nil, err
	}
	lineItems, _ := json.Marshal(parsed.LineItems)
	details, _ := json.Marshal(map[string]any{"references": parsed.References, "account_evidence": parsed.AccountEvidence})
	occurred, err := time.Parse("2006-01-02", parsed.OccurredOn)
	if err != nil {
		return uuid.Nil, err
	}
	occurred = occurred.UTC().Add(12 * time.Hour)
	var id uuid.UUID
	err = tx.QueryRow(ctx, `insert into public.transactions(user_id,account_id,transaction_kind,title,merchant_name,original_amount_minor,original_currency,sgd_amount_minor,occurred_at,time_precision,category_id,line_items,details,review_status,match_confidence,creation_method) select $1,a.id,$3,$4,nullif($5,''),$6,$7,$8,$9,'date',$10,$11::jsonb,$12::jsonb,'confirmed',$13,$14 from public.accounts a where a.id=$2 and a.user_id=$1 and a.deleted_at is null returning id`, rowSourceUser(ctx, tx, row), accountID, kind, parsed.Title, parsed.MerchantName, parsed.OriginalAmountMinor, parsed.OriginalCurrency, parsed.SGDAmountMinor, occurred, categoryID, string(lineItems), string(details), int(parsed.Confidence*100), creationMethod).Scan(&id)
	return id, err
}

func rowSourceUser(ctx context.Context, tx pgx.Tx, row bulkCandidateRow) uuid.UUID {
	var userID uuid.UUID
	_ = tx.QueryRow(ctx, `select user_id from private.source_candidates where id=$1`, row.ID).Scan(&userID)
	return userID
}

func attachBulkEvidence(ctx context.Context, tx pgx.Tx, row bulkCandidateRow, transactionID uuid.UUID, matchedBy string) error {
	userID := rowSourceUser(ctx, tx, row)
	command, err := tx.Exec(ctx, `insert into private.transaction_data_sources(user_id,transaction_id,data_source_id,bulk_import_candidate_id,role,match_confidence,matched_by) select $1,t.id,$2,$3,'other',null,$4 from public.transactions t where t.id=$5 and t.user_id=$1 on conflict(transaction_id,data_source_id) where detached_at is null do nothing`, userID, row.SourceID, row.ID, matchedBy, transactionID)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		var exists bool
		if err = tx.QueryRow(ctx, `select exists(select 1 from private.transaction_data_sources where user_id=$1 and transaction_id=$2 and data_source_id=$3 and bulk_import_candidate_id=$4 and detached_at is null)`, userID, transactionID, row.SourceID, row.ID).Scan(&exists); err != nil || !exists {
			return bulkimport.ErrConflict
		}
	}
	return nil
}

func finishBulkCandidate(ctx context.Context, tx pgx.Tx, row bulkCandidateRow, status bulkimport.CandidateStatus, transactionID *uuid.UUID, reason string) error {
	if err := updateBulkCandidateOutcome(ctx, tx, row, status, transactionID, reason); err != nil {
		return err
	}
	if err := enqueuePostProcessIfReady(ctx, tx, row); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func updateBulkCandidateOutcome(ctx context.Context, tx pgx.Tx, row bulkCandidateRow, status bulkimport.CandidateStatus, transactionID *uuid.UUID, reason string) error {
	userID := rowSourceUser(ctx, tx, row)
	_, err := tx.Exec(ctx, `update private.source_candidates set status=$3,transaction_id=$4,reconciliation_reason=$5,error_summary=null where id=$1 and user_id=$2`, row.ID, userID, status, transactionID, boundedBulkReason(reason))
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `update private.bulk_import_documents d set created_count=p.created,attached_count=p.attached,review_count=p.review,failed_count=p.failed+(select count(*)::int from private.bulk_import_chunks where document_id=$1 and user_id=$2 and attempt_generation=$3 and status='failed'),duplicate_count=p.duplicates from (select count(*) filter(where status='created')::int created,count(*) filter(where status='attached')::int attached,count(*) filter(where status='review_required')::int review,count(*) filter(where status='failed')::int failed,count(*) filter(where status='duplicate')::int duplicates from private.source_candidates where document_id=$1 and user_id=$2 and attempt_generation=$3) p where d.id=$1 and d.user_id=$2`, row.DocumentID, userID, row.Generation)
	return err
}

func enqueuePostProcessIfReady(ctx context.Context, tx pgx.Tx, row bulkCandidateRow) error {
	userID := rowSourceUser(ctx, tx, row)
	var remaining int
	if err := tx.QueryRow(ctx, `select count(*) from private.source_candidates where document_id=$1 and user_id=$2 and attempt_generation=$3 and status='pending_reconciliation'`, row.DocumentID, userID, row.Generation).Scan(&remaining); err != nil || remaining != 0 {
		return err
	}
	_, err := tx.Exec(ctx, `insert into private.transaction_jobs(user_id,data_source_id,job_type,bulk_import_batch_id,bulk_import_document_id,attempt_generation,payload) values($1,$2,$3,$4,$5,$6,'{}') on conflict do nothing`, userID, row.SourceID, string(jobs.KindBulkDocumentPostProcess), row.BatchID, row.DocumentID, row.Generation)
	return err
}

func enqueueBulkCandidateJob(ctx context.Context, tx pgx.Tx, row bulkCandidateRow) error {
	userID := rowSourceUser(ctx, tx, row)
	_, err := tx.Exec(ctx, `insert into private.transaction_jobs(user_id,data_source_id,job_type,bulk_import_batch_id,bulk_import_document_id,bulk_import_candidate_id,attempt_generation,payload) values($1,$2,$3,$4,$5,$6,$7,'{}') on conflict do nothing`, userID, row.SourceID, string(jobs.KindBulkCandidateReconcile), row.BatchID, row.DocumentID, row.ID, row.Generation)
	return err
}

func validateBulkSelectedAccount(ctx context.Context, tx pgx.Tx, batchID, userID, accountID uuid.UUID) error {
	var id uuid.UUID
	return tx.QueryRow(ctx, `select a.id from private.bulk_import_batch_accounts b join public.accounts a on a.id=b.account_id and a.user_id=b.user_id where b.batch_id=$1 and b.user_id=$2 and b.account_id=$3 and a.deleted_at is null`, batchID, userID, accountID).Scan(&id)
}

func validateActiveAccounts(ctx context.Context, tx pgx.Tx, userID uuid.UUID, accountIDs ...uuid.UUID) error {
	for _, id := range accountIDs {
		var found uuid.UUID
		if err := tx.QueryRow(ctx, `select id from public.accounts where id=$1 and user_id=$2 and deleted_at is null for share`, id, userID).Scan(&found); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) loadBulkCandidate(ctx context.Context, userID, candidateID uuid.UUID) (bulkimport.Candidate, error) {
	var item bulkimport.Candidate
	var fingerprint []byte
	err := s.pool.QueryRow(ctx, `select id,batch_id,document_id,attempt_generation,output_ordinal,fingerprint,parsed_candidate,account_id,status,transaction_id,duplicate_of_candidate_id,reconciliation_reason from private.source_candidates where id=$1 and user_id=$2`, candidateID, userID).Scan(&item.ID, &item.BatchID, &item.DocumentID, &item.AttemptGeneration, &item.Ordinal, &fingerprint, &item.ParsedCandidate, &item.AccountID, &item.Status, &item.TransactionID, &item.DuplicateOfID, &item.Reason)
	item.Fingerprint = fmt.Sprintf("%x", fingerprint)
	return item, err
}

func (s *Store) DeleteBulkDocument(ctx context.Context, userID, documentID uuid.UUID) error {
	var sourceID *uuid.UUID
	var status string
	err := s.pool.QueryRow(ctx, `select data_source_id,status from private.bulk_import_documents where id=$1 and user_id=$2`, documentID, userID).Scan(&sourceID, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return bulkimport.ErrNotFound
	}
	if err != nil {
		return err
	}
	if sourceID != nil {
		if status != "completed" && status != "completed_with_errors" && status != "failed" && status != "cancelled" {
			return bulkimport.ErrConflict
		}
		var retainedBill bool
		if err = s.pool.QueryRow(ctx, `select exists(select 1 from private.credit_card_statements where bulk_document_id=$1 and user_id=$2)`, documentID, userID).Scan(&retainedBill); err != nil {
			return err
		}
		if retainedBill {
			return bulkimport.ErrConflict
		}
		_, err = s.StageSourceDeletion(ctx, userID, *sourceID)
		return err
	}
	if status != "draft" && status != "cancelled" {
		return bulkimport.ErrConflict
	}
	return s.deleteDraftBulkDocument(ctx, userID, documentID)
}

func (s *Store) deleteDraftBulkDocument(ctx context.Context, userID, documentID uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var scopeID uuid.UUID
	rows, err := tx.Query(ctx, `select d.source_scope_id,f.storage_object_path from private.bulk_import_documents d left join private.bulk_import_files f on f.document_id=d.id and f.user_id=d.user_id where d.id=$1 and d.user_id=$2 and d.data_source_id is null and d.status in ('draft','cancelled')`, documentID, userID)
	if err != nil {
		return err
	}
	paths := make(scopedObjectPaths)
	for rows.Next() {
		var path *string
		if err = rows.Scan(&scopeID, &path); err != nil {
			rows.Close()
			return err
		}
		if path != nil {
			paths.addOwned(userID, *path)
		}
	}
	rows.Close()
	if scopeID == uuid.Nil {
		return bulkimport.ErrNotFound
	}
	if _, err = tx.Exec(ctx, `delete from private.bulk_import_documents where id=$1 and user_id=$2`, documentID, userID); err != nil {
		return err
	}
	if err = enqueueAttachmentCleanupJobs(ctx, tx, userID, paths); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) DeleteBulkBatch(ctx context.Context, userID, batchID uuid.UUID) error {
	var status string
	if err := s.pool.QueryRow(ctx, `select status from public.bulk_import_batches where id=$1 and user_id=$2`, batchID, userID).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return bulkimport.ErrNotFound
		}
		return err
	}
	if status != "draft" && status != "cancelled" {
		return bulkimport.ErrConflict
	}
	rows, err := s.pool.Query(ctx, `select id from private.bulk_import_documents where batch_id=$1 and user_id=$2 order by sort_order`, batchID, userID)
	if err != nil {
		return err
	}
	ids := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		if err = s.DeleteBulkDocument(ctx, userID, id); err != nil && !errors.Is(err, bulkimport.ErrNotFound) {
			return err
		}
	}
	_, err = s.pool.Exec(ctx, `delete from public.bulk_import_batches where id=$1 and user_id=$2 and status in ('draft','cancelled')`, batchID, userID)
	return err
}

func jsonEqual(left, right []byte) bool {
	var l, r any
	return json.Unmarshal(left, &l) == nil && json.Unmarshal(right, &r) == nil && reflect.DeepEqual(l, r)
}

func boundedBulkReason(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 2000 {
		value = value[:2000]
	}
	return value
}

var _ interface {
	ResolveBulkCandidate(context.Context, uuid.UUID, uuid.UUID, bulkimport.CandidateResolution) (bulkimport.Candidate, error)
	ReconcileBulkCandidate(context.Context, uuid.UUID, uuid.UUID, int) error
	DeleteBulkDocument(context.Context, uuid.UUID, uuid.UUID) error
	DeleteBulkBatch(context.Context, uuid.UUID, uuid.UUID) error
} = (*Store)(nil)

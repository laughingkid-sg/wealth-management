package transactionstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var (
	ErrAccountNotFound     = errors.New("account not found")
	ErrCategoryNotFound    = errors.New("transaction category not found")
	ErrTransferConflict    = errors.New("transaction transfer could not be created")
	ErrTransferSameAccount = errors.New("internal transfer accounts must be different")
)

type GmailConnectionStatus struct {
	Connected     bool       `json:"connected"`
	Status        string     `json:"status"`
	Email         *string    `json:"email"`
	SelectedLabel string     `json:"selected_label"`
	LastSyncedAt  *time.Time `json:"last_synced_at"`
	LastError     *string    `json:"last_error"`
}

func (s *Store) GetGmailConnectionStatus(ctx context.Context, userID uuid.UUID) (GmailConnectionStatus, error) {
	var result GmailConnectionStatus
	err := s.pool.QueryRow(ctx, `
		select status,
			coalesce(nullif(token_metadata ->> 'email', ''), nullif(token_metadata ->> 'email_address', '')),
			selected_label, last_synced_at, last_error
		from private.gmail_connections
		where user_id = $1 and provider = 'gmail'`, userID).Scan(
		&result.Status, &result.Email, &result.SelectedLabel, &result.LastSyncedAt, &result.LastError,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return GmailConnectionStatus{Status: "disconnected"}, nil
	}
	if err != nil {
		return GmailConnectionStatus{}, err
	}
	result.Connected = result.Status == "active"
	return result, nil
}

type SourcePageCursor struct {
	ReceivedAt time.Time
	ID         uuid.UUID
}

type SourcePage struct {
	Items   []SourceSummary
	HasMore bool
}

func (s *Store) ListSourcesPage(ctx context.Context, userID uuid.UUID, parseStatus string, cursor *SourcePageCursor, limit int) (SourcePage, error) {
	if limit < 1 || limit > 100 {
		return SourcePage{}, errors.New("source page limit must be between 1 and 100")
	}
	var beforeTime *time.Time
	var beforeID *uuid.UUID
	if cursor != nil {
		beforeTime, beforeID = &cursor.ReceivedAt, &cursor.ID
	}
	rows, err := s.pool.Query(ctx, `
		select source.id, source.source_type, source.provider, source.received_at,
			source.parse_status, source.parse_confidence,
			coalesce(source.raw_data ->> 'subject', ''),
			coalesce(source.raw_data ->> 'sender', ''),
			source.parse_error, source.reconciliation_reason,
			source.suggested_account_id, account.name,
			source.suggested_transaction_id, attempt.parsed_candidate,
			source.created_at
		from private.data_sources source
		left join public.accounts account
			on account.id = source.suggested_account_id
			and account.user_id = source.user_id
		left join lateral (
			select parsed_candidate
			from private.source_parse_attempts
			where user_id = source.user_id
				and data_source_id = source.id
				and validation_status = 'valid'
			order by created_at desc, id desc
			limit 1
		) attempt on true
		where source.user_id = $1 and source.parse_status = $2
			and ($3::timestamptz is null or (source.received_at, source.id) < ($3, $4))
		order by source.received_at desc, source.id desc
		limit $5`, userID, parseStatus, beforeTime, beforeID, limit+1)
	if err != nil {
		return SourcePage{}, err
	}
	defer rows.Close()
	items := make([]SourceSummary, 0, limit+1)
	for rows.Next() {
		var source SourceSummary
		var candidate []byte
		if err = rows.Scan(
			&source.ID, &source.SourceType, &source.Provider, &source.ReceivedAt,
			&source.ParseStatus, &source.ParseConfidence, &source.Subject, &source.Sender,
			&source.ParseError, &source.ReconciliationReason, &source.SuggestedAccountID,
			&source.SuggestedAccountName, &source.SuggestedTransactionID, &candidate,
			&source.CreatedAt,
		); err != nil {
			return SourcePage{}, err
		}
		applySourceSuggestion(&source, candidate)
		items = append(items, source)
	}
	if err = rows.Err(); err != nil {
		return SourcePage{}, err
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return SourcePage{Items: items, HasMore: hasMore}, nil
}

func applySourceSuggestion(source *SourceSummary, raw []byte) {
	if source == nil || len(raw) == 0 {
		return
	}
	var parsed struct {
		Candidate struct {
			Title            string          `json:"title"`
			Amount           json.RawMessage `json:"original_amount_minor"`
			Currency         string          `json:"original_currency"`
			CategoryLeafName string          `json:"category_leaf_name"`
		} `json:"candidate"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&parsed); err != nil {
		// Parser responses contain additional strictly validated fields. Decode a
		// narrow projection on the fallback path without trusting their shape here.
		var envelope map[string]json.RawMessage
		if json.Unmarshal(raw, &envelope) != nil {
			return
		}
		var candidate map[string]json.RawMessage
		if json.Unmarshal(envelope["candidate"], &candidate) != nil {
			return
		}
		_ = json.Unmarshal(candidate["title"], &parsed.Candidate.Title)
		parsed.Candidate.Amount = candidate["original_amount_minor"]
		_ = json.Unmarshal(candidate["original_currency"], &parsed.Candidate.Currency)
		_ = json.Unmarshal(candidate["category_leaf_name"], &parsed.Candidate.CategoryLeafName)
	}
	if value := strings.TrimSpace(parsed.Candidate.Title); value != "" {
		source.SuggestedTitle = &value
	}
	if amount, ok := decodeJSONInt64(parsed.Candidate.Amount); ok && amount > 0 {
		source.SuggestedAmountMinor = &amount
	}
	if value := strings.TrimSpace(parsed.Candidate.Currency); value != "" {
		source.SuggestedCurrency = &value
	}
	if value := strings.TrimSpace(parsed.Candidate.CategoryLeafName); value != "" {
		source.SuggestedCategoryLeafName = &value
	}
}

func decodeJSONInt64(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return 0, false
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return 0, false
	}
	switch typed := value.(type) {
	case json.Number:
		parsed, err := typed.Int64()
		return parsed, err == nil
	case string:
		var parsed json.Number = json.Number(typed)
		result, err := parsed.Int64()
		return result, err == nil
	default:
		return 0, false
	}
}

func (s *Store) RetrySourceParse(ctx context.Context, userID, sourceID uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var status string
	if err = tx.QueryRow(ctx, `
		select parse_status from private.data_sources
		where id = $1 and user_id = $2
		for update`, sourceID, userID).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrSourceNotFound
		}
		return err
	}
	var active bool
	if err = tx.QueryRow(ctx, `
		select exists(
			select 1 from private.transaction_jobs
			where user_id = $1 and data_source_id = $2 and job_type = 'source_parsing'
				and status in ('queued', 'running')
		)`, userID, sourceID).Scan(&active); err != nil {
		return err
	}
	if active {
		return tx.Commit(ctx)
	}
	if status != "failed" {
		return ErrSourceNotActionable
	}
	if _, err = tx.Exec(ctx, `
		update private.data_sources
		set parse_status = 'pending', parse_error = null,
			reconciliation_reason = null, suggested_transaction_id = null
		where id = $1 and user_id = $2`, sourceID, userID); err != nil {
		return err
	}
	if err = enqueueSourceParseTx(ctx, tx, userID, nil, sourceID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type TransactionPageCursor struct {
	OccurredAt time.Time
	ID         uuid.UUID
}

type TransactionListFilter struct {
	Kind         string
	ReviewStatus string
	AccountID    *uuid.UUID
	Search       string
	Cursor       *TransactionPageCursor
	Limit        int
}

type TransferLinkProjection struct {
	ID                       uuid.UUID `json:"id"`
	LinkType                 string    `json:"link_type"`
	Role                     string    `json:"role"`
	CounterpartTransactionID uuid.UUID `json:"counterpart_transaction_id"`
	CounterpartAccountID     uuid.UUID `json:"counterpart_account_id"`
	CounterpartTitle         string    `json:"counterpart_title"`
	CounterpartAccountName   *string   `json:"counterpart_account_name"`
}

type TransactionListRecord struct {
	Transaction
	Details            json.RawMessage
	AccountName        string
	CategoryName       *string
	CategoryParentName *string
	SourceCount        int
	TransferLink       *TransferLinkProjection
}

type TransactionPage struct {
	Items   []TransactionListRecord
	HasMore bool
}

func (s *Store) ListTransactionsPage(ctx context.Context, userID uuid.UUID, filter TransactionListFilter) (TransactionPage, error) {
	if filter.Limit < 1 || filter.Limit > 100 {
		return TransactionPage{}, errors.New("transaction page limit must be between 1 and 100")
	}
	var beforeTime *time.Time
	var beforeID *uuid.UUID
	if filter.Cursor != nil {
		beforeTime, beforeID = &filter.Cursor.OccurredAt, &filter.Cursor.ID
	}
	rows, err := s.pool.Query(ctx, `
		select transaction.id, transaction.account_id, transaction.transaction_kind,
			transaction.title, transaction.merchant_name, transaction.original_amount_minor,
			transaction.original_currency, transaction.sgd_amount_minor, transaction.occurred_at,
			transaction.category_id, transaction.line_items, transaction.review_status,
			transaction.match_confidence, transaction.created_at, transaction.updated_at,
			transaction.details, account.name, category.name, category.parent_name, source_count.count,
			transfer.id, transfer.link_type,
			case when transfer.debit_transaction_id = transaction.id then 'debit' else 'credit' end,
			counterpart.id, counterpart.account_id, counterpart.title, counterpart_account.name
		from public.transactions transaction
		join public.accounts account
			on account.id = transaction.account_id and account.user_id = transaction.user_id
		left join public.transaction_categories category on category.id = transaction.category_id
		left join lateral (
			select count(*)::integer as count
			from private.transaction_data_sources source_link
			where source_link.user_id = transaction.user_id
				and source_link.transaction_id = transaction.id
				and source_link.detached_at is null
		) source_count on true
		left join private.transaction_links transfer
			on transfer.user_id = transaction.user_id
			and (transfer.debit_transaction_id = transaction.id or transfer.credit_transaction_id = transaction.id)
		left join public.transactions counterpart
			on counterpart.user_id = transaction.user_id
			and counterpart.id = case
				when transfer.debit_transaction_id = transaction.id then transfer.credit_transaction_id
				else transfer.debit_transaction_id
			end
		left join public.accounts counterpart_account
			on counterpart_account.user_id = counterpart.user_id
			and counterpart_account.id = counterpart.account_id
		where transaction.user_id = $1
			and ($2 = '' or transaction.transaction_kind = $2)
			and ($3 = '' or transaction.review_status = $3)
			and ($4::uuid is null or transaction.account_id = $4)
			and ($5 = '' or transaction.title ilike '%' || $5 || '%'
				or coalesce(transaction.merchant_name, '') ilike '%' || $5 || '%')
			and ($6::timestamptz is null or (transaction.occurred_at, transaction.id) < ($6, $7))
		order by transaction.occurred_at desc, transaction.id desc
		limit $8`, userID, filter.Kind, filter.ReviewStatus, filter.AccountID, filter.Search,
		beforeTime, beforeID, filter.Limit+1)
	if err != nil {
		return TransactionPage{}, err
	}
	defer rows.Close()
	items := make([]TransactionListRecord, 0, filter.Limit+1)
	for rows.Next() {
		var item TransactionListRecord
		var transferID, counterpartID, counterpartAccountID *uuid.UUID
		var linkType, role, counterpartTitle, counterpartAccountName *string
		if err = rows.Scan(
			&item.ID, &item.AccountID, &item.TransactionKind, &item.Title,
			&item.MerchantName, &item.OriginalAmountMinor, &item.OriginalCurrency,
			&item.SGDAmountMinor, &item.OccurredAt, &item.CategoryID, &item.LineItems,
			&item.ReviewStatus, &item.MatchConfidence, &item.CreatedAt, &item.UpdatedAt,
			&item.Details, &item.AccountName, &item.CategoryName, &item.CategoryParentName, &item.SourceCount,
			&transferID, &linkType, &role, &counterpartID, &counterpartAccountID,
			&counterpartTitle, &counterpartAccountName,
		); err != nil {
			return TransactionPage{}, err
		}
		if transferID != nil && linkType != nil && role != nil && counterpartID != nil && counterpartAccountID != nil && counterpartTitle != nil {
			item.TransferLink = &TransferLinkProjection{
				ID: *transferID, LinkType: *linkType, Role: *role,
				CounterpartTransactionID: *counterpartID,
				CounterpartAccountID:     *counterpartAccountID,
				CounterpartTitle:         *counterpartTitle,
				CounterpartAccountName:   counterpartAccountName,
			}
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return TransactionPage{}, err
	}
	hasMore := len(items) > filter.Limit
	if hasMore {
		items = items[:filter.Limit]
	}
	return TransactionPage{Items: items, HasMore: hasMore}, nil
}

type AttachmentRecord struct {
	ID            string
	Filename      string
	MIMEType      string
	ByteSize      int64
	SHA256        string
	ParseEligible bool
	ParseStatus   string
	ObjectPath    string
}

func (s *Store) ListSourceAttachments(ctx context.Context, userID, sourceID uuid.UUID) ([]AttachmentRecord, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx, `
		select coalesce(raw_data -> 'attachments', '[]'::jsonb)
		from private.data_sources
		where id = $1 and user_id = $2 and source_type = 'gmail_email'`, sourceID, userID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSourceNotFound
	}
	if err != nil {
		return nil, err
	}
	var stored []struct {
		ID            string `json:"provider_attachment_id"`
		Filename      string `json:"filename"`
		MIMEType      string `json:"mime_type"`
		ByteSize      int64  `json:"byte_size"`
		SHA256        string `json:"sha256"`
		ParseEligible bool   `json:"parse_eligible"`
		ParseStatus   string `json:"parse_status"`
		StorageStatus string `json:"storage_status"`
		ObjectPath    string `json:"object_path"`
	}
	if len(raw) == 0 || string(raw) == "null" {
		return []AttachmentRecord{}, nil
	}
	if err = json.Unmarshal(raw, &stored); err != nil {
		return nil, errors.New("stored attachment metadata is invalid")
	}
	result := make([]AttachmentRecord, 0, len(stored))
	for _, item := range stored {
		if item.StorageStatus != "stored" || item.ObjectPath == "" {
			continue
		}
		parseStatus := item.ParseStatus
		if parseStatus == "" {
			parseStatus = "not_parsed"
		}
		attachmentID := attachmentRecordID(item.ID, item.SHA256, item.ObjectPath)
		result = append(result, AttachmentRecord{
			ID: attachmentID, Filename: item.Filename, MIMEType: item.MIMEType,
			ByteSize: item.ByteSize, SHA256: item.SHA256,
			ParseEligible: item.ParseEligible, ParseStatus: parseStatus,
			ObjectPath: item.ObjectPath,
		})
	}
	return result, nil
}

func attachmentRecordID(providerID, checksum, objectPath string) string {
	if value := strings.TrimSpace(providerID); value != "" {
		return value
	}
	if value := strings.TrimSpace(checksum); value != "" {
		return value
	}
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(objectPath)).String()
}

type TransferLegInput struct {
	AccountID           uuid.UUID
	Title               string
	MerchantName        *string
	OriginalAmountMinor int64
	OriginalCurrency    string
	SGDAmountMinor      *int64
	OccurredAt          time.Time
	CategoryID          *uuid.UUID
	LineItems           json.RawMessage
	SourceIDs           []uuid.UUID
}

type InternalTransferInput struct {
	Debit  TransferLegInput
	Credit TransferLegInput
}

type InternalTransfer struct {
	ID        uuid.UUID
	LinkType  string
	Debit     Transaction
	Credit    Transaction
	CreatedAt time.Time
}

func (s *Store) CreateInternalTransfer(ctx context.Context, userID uuid.UUID, input InternalTransferInput) (InternalTransfer, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return InternalTransfer{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = validateInternalTransferReferences(ctx, tx, userID, input); err != nil {
		return InternalTransfer{}, err
	}
	result := InternalTransfer{LinkType: "internal_transfer"}
	if result.Debit, err = insertTransferLeg(ctx, tx, userID, "debit", input.Debit); err != nil {
		return InternalTransfer{}, err
	}
	if result.Credit, err = insertTransferLeg(ctx, tx, userID, "credit", input.Credit); err != nil {
		return InternalTransfer{}, err
	}
	err = tx.QueryRow(ctx, `
		insert into private.transaction_links (
			user_id, link_type, debit_transaction_id, credit_transaction_id
		) values ($1, 'internal_transfer', $2, $3)
		returning id, created_at`, userID, result.Debit.ID, result.Credit.ID).Scan(&result.ID, &result.CreatedAt)
	if err != nil {
		return InternalTransfer{}, fmt.Errorf("create internal transfer link: %w", err)
	}
	if err = attachTransferSources(ctx, tx, userID, result.Debit.ID, input.Debit.SourceIDs); err != nil {
		return InternalTransfer{}, err
	}
	if err = attachTransferSources(ctx, tx, userID, result.Credit.ID, input.Credit.SourceIDs); err != nil {
		return InternalTransfer{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return InternalTransfer{}, err
	}
	return result, nil
}

func validateInternalTransferReferences(ctx context.Context, tx pgx.Tx, userID uuid.UUID, input InternalTransferInput) error {
	if input.Debit.AccountID == input.Credit.AccountID {
		return ErrTransferSameAccount
	}

	// Stable UUID ordering avoids deadlocks between concurrent transfer and
	// source-attachment transactions.
	for _, accountID := range sortedUUIDs([]uuid.UUID{input.Debit.AccountID, input.Credit.AccountID}) {
		var lockedID uuid.UUID
		err := tx.QueryRow(ctx, `
			select id from public.accounts
			where id = $1 and user_id = $2 and deleted_at is null
			for share`, accountID, userID).Scan(&lockedID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrAccountNotFound
		}
		if err != nil {
			return err
		}
	}
	for _, categoryID := range sortedOptionalUUIDs(input.Debit.CategoryID, input.Credit.CategoryID) {
		var lockedID uuid.UUID
		err := tx.QueryRow(ctx, `
			select id from public.transaction_categories
			where id = $1 and active
			for share`, categoryID).Scan(&lockedID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrCategoryNotFound
		}
		if err != nil {
			return err
		}
	}
	sourceIDs := append(append([]uuid.UUID{}, input.Debit.SourceIDs...), input.Credit.SourceIDs...)
	for _, sourceID := range sortedUUIDs(sourceIDs) {
		var status string
		err := tx.QueryRow(ctx, `
			select parse_status from private.data_sources
			where id = $1 and user_id = $2
			for update`, sourceID, userID).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrSourceNotFound
		}
		if err != nil {
			return err
		}
		if status != "parsed" && status != "review_required" && status != "dangling" {
			return ErrSourceNotActionable
		}
		linked, err := sourceHasActiveLink(ctx, tx, userID, sourceID)
		if err != nil {
			return err
		}
		if linked {
			return ErrSourceAlreadyLinked
		}
	}
	return nil
}

func insertTransferLeg(ctx context.Context, tx pgx.Tx, userID uuid.UUID, kind string, leg TransferLegInput) (Transaction, error) {
	var transaction Transaction
	err := tx.QueryRow(ctx, `
		insert into public.transactions (
			user_id, account_id, transaction_kind, title, merchant_name,
			original_amount_minor, original_currency, sgd_amount_minor, occurred_at,
			category_id, line_items, details, review_status
		) values (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::jsonb, '{}'::jsonb, 'confirmed'
		)
		returning id, account_id, transaction_kind, title, merchant_name,
			original_amount_minor, original_currency, sgd_amount_minor, occurred_at,
			category_id, line_items, review_status, match_confidence, created_at, updated_at`,
		userID, leg.AccountID, kind, leg.Title, leg.MerchantName,
		leg.OriginalAmountMinor, leg.OriginalCurrency, leg.SGDAmountMinor, leg.OccurredAt,
		leg.CategoryID, string(leg.LineItems),
	).Scan(transactionFields(&transaction)...)
	return transaction, err
}

func attachTransferSources(ctx context.Context, tx pgx.Tx, userID, transactionID uuid.UUID, sourceIDs []uuid.UUID) error {
	for _, sourceID := range dedupeUUIDs(sourceIDs) {
		if _, err := tx.Exec(ctx, `
			insert into private.transaction_data_sources (
				user_id, transaction_id, data_source_id, role, matched_by
			) values ($1, $2, $3, 'other', 'user')`, userID, transactionID, sourceID); err != nil {
			return fmt.Errorf("attach transfer source: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			update private.data_sources
			set parse_status = 'parsed', suggested_account_id = null,
				suggested_transaction_id = null, reconciliation_reason = null,
				parse_error = null
			where id = $1 and user_id = $2`, sourceID, userID); err != nil {
			return err
		}
	}
	return nil
}

func dedupeUUIDs(values []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{}, len(values))
	result := make([]uuid.UUID, 0, len(values))
	for _, value := range values {
		if value == uuid.Nil {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func sortedUUIDs(values []uuid.UUID) []uuid.UUID {
	result := dedupeUUIDs(values)
	slices.SortFunc(result, func(left, right uuid.UUID) int {
		return bytes.Compare(left[:], right[:])
	})
	return result
}

func sortedOptionalUUIDs(values ...*uuid.UUID) []uuid.UUID {
	result := make([]uuid.UUID, 0, len(values))
	for _, value := range values {
		if value != nil {
			result = append(result, *value)
		}
	}
	return sortedUUIDs(result)
}

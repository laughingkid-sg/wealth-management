package transactionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/zhengteck/wealth-builder/backend/internal/jobs"
	"github.com/zhengteck/wealth-builder/backend/internal/parserrules"
	"github.com/zhengteck/wealth-builder/backend/internal/reconciliation"
	"golang.org/x/net/html"
)

// SourceParseInput contains only normalized provider content safe to send to
// the configured parser. Raw MIME and attachment bytes stay private.
type SourceParseInput struct {
	ID                         uuid.UUID
	Subject                    string
	Sender                     string
	Content                    string
	ReceivedAt                 time.Time
	ParseStatus                string
	NormalizedContent          string
	Rules                      []parserrules.Rule
	UserRules                  []parserrules.UserRule
	DefaultInstructions        string
	DefaultInstructionsVersion int
	Attachments                []SourceAttachment
}

// SourceAttachment is metadata only. Attachment bytes remain in private
// Storage and are downloaded by the worker only when parse eligible.
type SourceAttachment struct {
	Filename, MIMEType, ObjectPath, StorageStatus string
	ParseEligible                                 bool
}

type ReconciliationInput struct {
	SourceID     uuid.UUID
	Candidate    reconciliation.Candidate
	Accounts     []reconciliation.AccountIdentity
	Transactions []reconciliation.Transaction
}

type ParsedSourceResult struct {
	SourceParseAudit
	SyncRunID       *uuid.UUID
	ParsedResponse  reconciliation.ParsedResponse
	ParsedCandidate json.RawMessage
	AutoEligible    bool
}

type AttachmentUsage struct{ ObjectPath, Filename, MIMEType string }

type PromptComponent struct {
	ID      string `json:"id,omitempty"`
	Name    string `json:"name,omitempty"`
	Version int    `json:"version,omitempty"`
	Content string `json:"content"`
}

type PromptComponents struct {
	Platform       PromptComponent  `json:"platform"`
	GlobalRule     *PromptComponent `json:"global_rule"`
	UserDefault    *PromptComponent `json:"user_default"`
	UserSourceRule *PromptComponent `json:"user_source_rule"`
}

// SourceParseAudit is populated before the provider call and enriched with the
// exact request/response/model JSON as each boundary succeeds.
type SourceParseAudit struct {
	SourceID              uuid.UUID
	Model                 string
	AssembledSystemPrompt string
	NormalizedInput       string
	ProviderRequest       json.RawMessage
	ProviderResponse      json.RawMessage
	ModelOutput           json.RawMessage
	PromptComponents      json.RawMessage
	RuleID                string
	RuleVersion           int
	UserRuleID            string
	UserRuleVersion       int
	AttachmentUsage       []AttachmentUsage
}

type ReconciliationResult struct {
	SourceID  uuid.UUID
	SyncRunID *uuid.UUID
	Candidate reconciliation.Candidate
	Decision  reconciliation.Decision
}

func (s *Store) LoadSourceParseInput(ctx context.Context, userID, sourceID uuid.UUID) (SourceParseInput, error) {
	var input SourceParseInput
	var subject, sender, text, sanitizedHTML string
	var rawData []byte
	err := s.pool.QueryRow(ctx, `
		select id, coalesce(raw_data ->> 'subject', ''), coalesce(raw_data ->> 'sender', ''),
			coalesce(raw_data ->> 'text', ''), coalesce(raw_data ->> 'html_sanitized', ''), raw_data, received_at, parse_status
		from private.data_sources
		where id = $1 and user_id = $2 and source_type = 'gmail_email' and provider = 'gmail'`, sourceID, userID).Scan(&input.ID, &subject, &sender, &text, &sanitizedHTML, &rawData, &input.ReceivedAt, &input.ParseStatus)
	if err != nil {
		return SourceParseInput{}, err
	}
	input.Subject, input.Sender = subject, sender
	if strings.TrimSpace(text) == "" {
		text = normalizedSanitizedHTMLText(sanitizedHTML)
	}
	input.Content = text
	input.NormalizedContent = normalizedEmailContent(subject, sender, text, input.ReceivedAt)
	if input.NormalizedContent == "" {
		return SourceParseInput{}, errors.New("source has no normalized email content")
	}
	rules, err := s.loadActiveGmailParserRules(ctx)
	if err != nil {
		return SourceParseInput{}, err
	}
	input.Rules = rules
	if err = s.pool.QueryRow(ctx, `
		select default_instructions, version
		from private.user_parser_settings
		where user_id = $1`, userID).Scan(&input.DefaultInstructions, &input.DefaultInstructionsVersion); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return SourceParseInput{}, err
	}
	userRules, err := s.loadActiveUserGmailParserRules(ctx, userID)
	if err != nil {
		return SourceParseInput{}, err
	}
	input.UserRules = userRules
	input.Attachments = sourceAttachmentMetadata(rawData)
	return input, nil
}

func normalizedSanitizedHTMLText(value string) string {
	node, err := html.Parse(strings.NewReader(value))
	if err != nil {
		return ""
	}
	var builder strings.Builder
	var visit func(*html.Node)
	visit = func(current *html.Node) {
		if current.Type == html.TextNode {
			builder.WriteString(current.Data)
			builder.WriteByte(' ')
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(node)
	return strings.Join(strings.Fields(builder.String()), " ")
}

func sourceAttachmentMetadata(raw []byte) []SourceAttachment {
	var source struct {
		Attachments []struct {
			Filename      string `json:"filename"`
			MIMEType      string `json:"mime_type"`
			ObjectPath    string `json:"object_path"`
			StorageStatus string `json:"storage_status"`
			ParseEligible bool   `json:"parse_eligible"`
		} `json:"attachments"`
	}
	if json.Unmarshal(raw, &source) != nil {
		return nil
	}
	attachments := make([]SourceAttachment, 0, len(source.Attachments))
	for _, value := range source.Attachments {
		attachments = append(attachments, SourceAttachment{Filename: value.Filename, MIMEType: value.MIMEType, ObjectPath: value.ObjectPath, StorageStatus: value.StorageStatus, ParseEligible: value.ParseEligible})
	}
	return attachments
}

func (s *Store) loadActiveGmailParserRules(ctx context.Context) ([]parserrules.Rule, error) {
	rows, err := s.pool.Query(ctx, `
		select id, name, version, priority, coalesce(sender_matcher, ''), coalesce(content_matcher, ''),
			coalesce(prompt_fragment, ''), extraction_config
		from private.source_parser_rules
		where provider = 'gmail' and active = true
		order by priority desc, id asc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rules := make([]parserrules.Rule, 0)
	for rows.Next() {
		var rule parserrules.Rule
		if err := rows.Scan(&rule.ID, &rule.Name, &rule.Version, &rule.Priority, &rule.SenderMatcher, &rule.ContentMatcher, &rule.PromptFragment, &rule.ExtractionConfig); err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	return rules, rows.Err()
}

func (s *Store) loadActiveUserGmailParserRules(ctx context.Context, userID uuid.UUID) ([]parserrules.UserRule, error) {
	rows, err := s.pool.Query(ctx, `
		select id, name, version, priority, sender_match_type, sender_match_value,
			coalesce(subject_matcher, ''), coalesce(content_matcher, ''), prompt_fragment
		from private.user_source_parser_rules
		where user_id = $1 and provider = 'gmail' and active = true
		order by priority desc, id asc`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rules := make([]parserrules.UserRule, 0)
	for rows.Next() {
		var rule parserrules.UserRule
		if err := rows.Scan(
			&rule.ID, &rule.Name, &rule.Version, &rule.Priority,
			&rule.SenderMatchType, &rule.SenderMatchValue,
			&rule.SubjectMatcher, &rule.ContentMatcher, &rule.PromptFragment,
		); err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	return rules, rows.Err()
}

func normalizedEmailContent(subject, sender, text string, receivedAt time.Time) string {
	parts := make([]string, 0, 4)
	if value := strings.TrimSpace(subject); value != "" {
		parts = append(parts, "subject: "+value)
	}
	if value := strings.TrimSpace(sender); value != "" {
		parts = append(parts, "sender: "+value)
	}
	if value := strings.TrimSpace(text); value != "" {
		parts = append(parts, "text: "+value)
	}
	if !receivedAt.IsZero() {
		parts = append(parts, "received_at: "+receivedAt.UTC().Format(time.RFC3339))
	}
	return strings.Join(parts, "\n")
}

// SaveParsedSource records a validated parser result and queues reconciliation
// atomically. The parser call itself must have completed before this method.
func (s *Store) SaveParsedSource(ctx context.Context, userID uuid.UUID, result ParsedSourceResult) error {
	if result.SourceID == uuid.Nil || !validJSONObject(result.ParsedCandidate) {
		return errors.New("valid source ID and parser response are required")
	}
	confidence := confidencePercent(result.ParsedResponse.Candidate.Confidence)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var status string
	if err = tx.QueryRow(ctx, `select parse_status from private.data_sources where id = $1 and user_id = $2 for update`, result.SourceID, userID).Scan(&status); err != nil {
		return err
	}
	if status == "review_required" || status == "dangling" {
		return tx.Commit(ctx)
	}
	metadata, err := json.Marshal(map[string]any{
		"provider": "alibaba_openai_compatible", "thinking": false,
		"response_format": "json_object", "parser_rule_id": result.RuleID,
		"parser_rule_version": result.RuleVersion, "user_parser_rule_id": result.UserRuleID,
		"user_parser_rule_version": result.UserRuleVersion,
		"attachment_usage":         result.AttachmentUsage, "auto_eligible": result.AutoEligible,
	})
	if err != nil {
		return err
	}
	ruleID, err := parseOptionalRuleProvenance(result.RuleID, result.RuleVersion)
	if err != nil {
		return err
	}
	userRuleID, err := parseOptionalRuleProvenance(result.UserRuleID, result.UserRuleVersion)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		insert into private.source_parse_attempts (
			user_id, data_source_id, parser_rule_id, parser_rule_version,
			user_parser_rule_id, user_parser_rule_version, model_name,
			request_metadata, parsed_candidate, assembled_system_prompt,
			normalized_input, provider_request, provider_response, model_output,
			prompt_components, validation_status, started_at, completed_at
		) values (
			$1, $2, $3, nullif($4, 0), $5, nullif($6, 0), $7,
			$8::jsonb, $9::jsonb, $10, $11,
			case when $12 = 'null' then null else $12::json end,
			case when $13 = 'null' then null else $13::json end,
			case when $14 = 'null' then null else $14::json end, $15::jsonb,
			'valid', now(), now()
		)`, userID, result.SourceID, ruleID, result.RuleVersion,
		userRuleID, result.UserRuleVersion, result.Model, string(metadata),
		string(result.ParsedCandidate), result.AssembledSystemPrompt,
		result.NormalizedInput, nullableJSON(result.ProviderRequest),
		nullableJSON(result.ProviderResponse), nullableJSON(result.ModelOutput),
		promptComponentsJSON(result.PromptComponents))
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		update private.data_sources set parse_status = 'parsed', parse_confidence = $3,
			parse_error = null, suggested_account_id = null, parser_rule_id = $4, parser_rule_version = nullif($5, 0)
		where id = $1 and user_id = $2`, result.SourceID, userID, confidence, ruleID, result.RuleVersion)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]string{"data_source_id": result.SourceID.String()})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		insert into private.transaction_jobs (user_id, sync_run_id, data_source_id, job_type, payload)
		select $1, $2, $3, $4, $5::jsonb
		where not exists (
			select 1 from private.transaction_jobs
			where user_id = $1 and data_source_id = $3 and job_type = $4 and status in ('queued', 'running')
		)`, userID, result.SyncRunID, result.SourceID, string(jobs.KindReconcile), string(payload))
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) RecordInvalidSourceParse(ctx context.Context, userID uuid.UUID, audit SourceParseAudit, cause error) error {
	return s.recordSourceParseError(ctx, userID, audit, "invalid", cause)
}

func (s *Store) RecordFailedSourceParse(ctx context.Context, userID uuid.UUID, audit SourceParseAudit, cause error) error {
	return s.recordSourceParseError(ctx, userID, audit, "failed", cause)
}

func (s *Store) recordSourceParseError(ctx context.Context, userID uuid.UUID, audit SourceParseAudit, validationStatus string, cause error) error {
	if audit.SourceID == uuid.Nil || cause == nil {
		return errors.New("source ID and parse error are required")
	}
	ruleID, err := parseOptionalRuleProvenance(audit.RuleID, audit.RuleVersion)
	if err != nil {
		return err
	}
	userRuleID, err := parseOptionalRuleProvenance(audit.UserRuleID, audit.UserRuleVersion)
	if err != nil {
		return err
	}
	errorSummary := boundedError(cause)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	metadata, err := json.Marshal(map[string]any{
		"provider": "alibaba_openai_compatible", "thinking": false,
		"response_format": "json_object", "parser_rule_id": audit.RuleID,
		"parser_rule_version": audit.RuleVersion, "user_parser_rule_id": audit.UserRuleID,
		"user_parser_rule_version": audit.UserRuleVersion,
		"attachment_usage":         audit.AttachmentUsage,
	})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		insert into private.source_parse_attempts (
			user_id, data_source_id, parser_rule_id, parser_rule_version,
			user_parser_rule_id, user_parser_rule_version, model_name,
			request_metadata, parsed_candidate, assembled_system_prompt,
			normalized_input, provider_request, provider_response, model_output,
			prompt_components, validation_status, error_summary, started_at, completed_at
		) values (
			$1, $2, $3, nullif($4, 0), $5, nullif($6, 0), $7,
			$8::jsonb, null, $9, $10,
			case when $11 = 'null' then null else $11::json end,
			case when $12 = 'null' then null else $12::json end,
			case when $13 = 'null' then null else $13::json end, $14::jsonb,
			$15, $16, now(), now()
		)`, userID, audit.SourceID, ruleID, audit.RuleVersion,
		userRuleID, audit.UserRuleVersion, audit.Model, string(metadata),
		audit.AssembledSystemPrompt, audit.NormalizedInput,
		nullableJSON(audit.ProviderRequest), nullableJSON(audit.ProviderResponse),
		nullableJSON(audit.ModelOutput), promptComponentsJSON(audit.PromptComponents),
		validationStatus, errorSummary)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		update private.data_sources set parse_status = 'failed', parse_confidence = null, parse_error = $3
		where id = $1 and user_id = $2`, audit.SourceID, userID, errorSummary)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func nullableJSON(raw json.RawMessage) string {
	if !json.Valid(raw) {
		return "null"
	}
	return string(raw)
}

func promptComponentsJSON(raw json.RawMessage) string {
	if !validJSONObject(raw) {
		return "{}"
	}
	return string(raw)
}

func validJSONObject(raw json.RawMessage) bool {
	var object map[string]json.RawMessage
	return json.Unmarshal(raw, &object) == nil && object != nil
}

func parseOptionalRuleProvenance(id string, version int) (*uuid.UUID, error) {
	if id == "" && version == 0 {
		return nil, nil
	}
	parsed, err := uuid.Parse(id)
	if err != nil || version < 1 {
		return nil, errors.New("invalid parser rule provenance")
	}
	return &parsed, nil
}

func (s *Store) LoadReconciliationInput(ctx context.Context, userID, sourceID uuid.UUID) (ReconciliationInput, error) {
	var raw, metadata []byte
	err := s.pool.QueryRow(ctx, `
		select attempt.parsed_candidate, attempt.request_metadata
		from private.data_sources source
		join lateral (
			select parsed_candidate, request_metadata from private.source_parse_attempts
			where user_id = source.user_id and data_source_id = source.id and validation_status = 'valid'
			order by created_at desc, id desc limit 1
		) attempt on true
		where source.id = $1 and source.user_id = $2 and source.parse_status = 'parsed'`, sourceID, userID).Scan(&raw, &metadata)
	if err != nil {
		return ReconciliationInput{}, err
	}
	parsed, err := decodePersistedCandidate(raw, userID)
	if err != nil {
		return ReconciliationInput{}, fmt.Errorf("decode persisted parse result: %w", err)
	}
	var metadataValue struct {
		AutoEligible bool `json:"auto_eligible"`
	}
	if json.Unmarshal(metadata, &metadataValue) != nil {
		return ReconciliationInput{}, errors.New("decode parser request metadata")
	}
	parsed.Candidate.AutoEligible = metadataValue.AutoEligible
	accounts, err := s.loadOwnedAccountIdentities(ctx, userID)
	if err != nil {
		return ReconciliationInput{}, err
	}
	transactions, err := s.loadOwnedTransactions(ctx, userID, parsed.Candidate.OccurredAt)
	if err != nil {
		return ReconciliationInput{}, err
	}
	return ReconciliationInput{SourceID: sourceID, Candidate: parsed.Candidate, Accounts: accounts, Transactions: transactions}, nil
}

func (s *Store) loadOwnedAccountIdentities(ctx context.Context, userID uuid.UUID) ([]reconciliation.AccountIdentity, error) {
	return loadOwnedAccountIdentities(ctx, s.pool, userID)
}

type reconciliationQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func loadOwnedAccountIdentities(ctx context.Context, querier reconciliationQuerier, userID uuid.UUID) ([]reconciliation.AccountIdentity, error) {
	rows, err := querier.Query(ctx, `
		select account.id, matching_key.key_type, matching_key.normalized_value
		from public.accounts account
		join private.account_matching_keys matching_key
			on matching_key.account_id = account.id
			and matching_key.user_id = account.user_id
			and matching_key.active = true
			and matching_key.retired_at is null
		where account.user_id = $1 and account.deleted_at is null
		order by account.id, matching_key.key_type, matching_key.normalized_value`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	accountsByID := make(map[string]*reconciliation.AccountIdentity)
	order := make([]string, 0)
	for rows.Next() {
		var accountID, keyType, normalizedValue string
		if err := rows.Scan(&accountID, &keyType, &normalizedValue); err != nil {
			return nil, err
		}
		account := accountsByID[accountID]
		if account == nil {
			account = &reconciliation.AccountIdentity{ID: accountID, UserID: userID.String()}
			accountsByID[accountID] = account
			order = append(order, accountID)
		}
		account.MatchingKeys = append(account.MatchingKeys, reconciliation.AccountMatchingKey{
			KeyType: keyType, NormalizedValue: normalizedValue,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	accounts := make([]reconciliation.AccountIdentity, 0, len(order))
	for _, accountID := range order {
		accounts = append(accounts, *accountsByID[accountID])
	}
	return accounts, nil
}

func (s *Store) loadOwnedTransactions(ctx context.Context, userID uuid.UUID, occurredAt time.Time) ([]reconciliation.Transaction, error) {
	return loadOwnedTransactions(ctx, s.pool, userID, occurredAt)
}

func loadOwnedTransactions(ctx context.Context, querier reconciliationQuerier, userID uuid.UUID, occurredAt time.Time) ([]reconciliation.Transaction, error) {
	rows, err := querier.Query(ctx, `
		select id, account_id, transaction_kind, coalesce(merchant_name, ''), original_amount_minor,
			original_currency, occurred_at, coalesce(details -> 'references', '[]'::jsonb)
		from public.transactions
		where user_id = $1 and occurred_at between $2::timestamptz - interval '24 hours' and $2::timestamptz + interval '24 hours'`, userID, occurredAt)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	transactions := make([]reconciliation.Transaction, 0)
	for rows.Next() {
		var transaction reconciliation.Transaction
		var references []byte
		if err := rows.Scan(&transaction.ID, &transaction.AccountID, &transaction.Kind, &transaction.MerchantName,
			&transaction.OriginalAmountMinor, &transaction.OriginalCurrency, &transaction.OccurredAt, &references); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(references, &transaction.References); err != nil {
			return nil, fmt.Errorf("decode transaction references: %w", err)
		}
		transaction.UserID = userID.String()
		transactions = append(transactions, transaction)
	}
	return transactions, rows.Err()
}

// PersistReconciliation applies one domain decision after all external work
// and matching reads have completed. Automatic creates are serialized per
// owner and reconciled again inside the write transaction before insertion.
// Every outcome also locks the source, verifies ownership again, and updates
// visible sync-run counters atomically.
func (s *Store) PersistReconciliation(ctx context.Context, userID uuid.UUID, result ReconciliationResult) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	serializedCreate := result.Decision.Outcome == reconciliation.OutcomeCreate
	if serializedCreate {
		// Every automatic create for one owner takes the same transaction-scoped
		// row lock. A worker that waited here will observe the winner's committed
		// transaction when it repeats reconciliation below.
		if err = lockTransactionUser(ctx, tx, userID); err != nil {
			return err
		}
	}
	var status string
	if err = tx.QueryRow(ctx, `select parse_status from private.data_sources where id = $1 and user_id = $2 for update`, result.SourceID, userID).Scan(&status); err != nil {
		return err
	}
	if status != "parsed" {
		return tx.Commit(ctx)
	}
	var linked bool
	if err = tx.QueryRow(ctx, `select exists(select 1 from private.transaction_data_sources where user_id = $1 and data_source_id = $2 and detached_at is null)`, userID, result.SourceID).Scan(&linked); err != nil {
		return err
	}
	if linked {
		return tx.Commit(ctx)
	}
	if serializedCreate {
		accounts, loadErr := loadOwnedAccountIdentities(ctx, tx, userID)
		if loadErr != nil {
			return loadErr
		}
		transactions, loadErr := loadOwnedTransactions(ctx, tx, userID, result.Candidate.OccurredAt)
		if loadErr != nil {
			return loadErr
		}
		decision, reconcileErr := reconciliation.Reconcile(result.Candidate, accounts, transactions)
		if reconcileErr != nil {
			return fmt.Errorf("repeat serialized reconciliation: %w", reconcileErr)
		}
		result.Decision = decision
	}
	created, attached, dangling, review := 0, 0, 0, 0
	confidence := confidencePercent(result.Candidate.Confidence)
	score := int16(min(100, result.Decision.Score.Total()))
	suggestedAccount := nullableUUID(result.Decision.AccountID)
	suggestedTransaction := nullableUUID(result.Decision.TransactionID)
	switch result.Decision.Outcome {
	case reconciliation.OutcomeAttach:
		transactionID, err := uuid.Parse(result.Decision.TransactionID)
		if err != nil {
			return errors.New("reconciliation selected an invalid transaction")
		}
		command, err := tx.Exec(ctx, `
			insert into private.transaction_data_sources (user_id, transaction_id, data_source_id, role, match_confidence, matched_by)
			select $1, transaction.id, $2, 'other', $3, 'automatic'
			from public.transactions transaction where transaction.id = $4 and transaction.user_id = $1`, userID, result.SourceID, score, transactionID)
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return errors.New("reconciliation selected a transaction not owned by the source user")
		}
		attached = 1
	case reconciliation.OutcomeCreate:
		accountID, err := uuid.Parse(result.Decision.AccountID)
		if err != nil {
			return errors.New("reconciliation selected an invalid account")
		}
		lineItems, err := json.Marshal(result.Candidate.LineItems)
		if err != nil {
			return err
		}
		details, err := json.Marshal(map[string]any{"references": result.Candidate.References, "account_evidence": result.Candidate.AccountEvidence})
		if err != nil {
			return err
		}
		categoryID, err := s.resolveCategoryLeaf(ctx, tx, result.Candidate.CategoryLeafName)
		if err != nil {
			return err
		}
		var transactionID uuid.UUID
		err = tx.QueryRow(ctx, `
			insert into public.transactions (user_id, account_id, transaction_kind, title, merchant_name,
				original_amount_minor, original_currency, sgd_amount_minor, occurred_at, category_id, line_items, details,
				review_status, match_confidence, creation_method)
			values ($1, $2, $3, $4, nullif($5, ''), $6, $7, $8, $9, $10, $11::jsonb, $12::jsonb, 'confirmed', $13, 'automatic_source')
			returning id`, userID, accountID, string(result.Candidate.Kind), result.Candidate.Title,
			result.Candidate.MerchantName, result.Candidate.OriginalAmountMinor, result.Candidate.OriginalCurrency,
			result.Candidate.SGDAmountMinor, result.Candidate.OccurredAt, categoryID, string(lineItems), string(details), confidence).Scan(&transactionID)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `insert into private.transaction_data_sources (user_id, transaction_id, data_source_id, role, match_confidence, matched_by) values ($1, $2, $3, 'other', $4, 'automatic')`, userID, transactionID, result.SourceID, confidence)
		if err != nil {
			return err
		}
		created, attached = 1, 1
	case reconciliation.OutcomeReview:
		_, err = tx.Exec(ctx, `update private.data_sources set parse_status = 'review_required', suggested_account_id = $3, suggested_transaction_id = $4, reconciliation_reason = $5 where id = $1 and user_id = $2`, result.SourceID, userID, suggestedAccount, suggestedTransaction, result.Decision.Reason)
		if err != nil {
			return err
		}
		review = 1
	case reconciliation.OutcomeDangling:
		_, err = tx.Exec(ctx, `update private.data_sources set parse_status = 'dangling', suggested_account_id = null, suggested_transaction_id = null, reconciliation_reason = $3 where id = $1 and user_id = $2`, result.SourceID, userID, result.Decision.Reason)
		if err != nil {
			return err
		}
		dangling = 1
	default:
		return fmt.Errorf("unsupported reconciliation outcome %q", result.Decision.Outcome)
	}
	if result.Decision.Outcome == reconciliation.OutcomeAttach || result.Decision.Outcome == reconciliation.OutcomeCreate {
		_, err = tx.Exec(ctx, `update private.data_sources set suggested_account_id = $3, suggested_transaction_id = null, reconciliation_reason = null, parse_error = null where id = $1 and user_id = $2`, result.SourceID, userID, suggestedAccount)
		if err != nil {
			return err
		}
	}
	if result.SyncRunID != nil {
		_, err = tx.Exec(ctx, `
			update public.transaction_sync_runs set transactions_created_count = transactions_created_count + $3,
				sources_linked_count = sources_linked_count + $4, dangling_sources_count = dangling_sources_count + $5,
				review_required_count = review_required_count + $6
			where id = $1 and user_id = $2`, *result.SyncRunID, userID, created, attached, dangling, review)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) resolveCategoryLeaf(ctx context.Context, tx interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, name string) (*uuid.UUID, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `select id from public.transaction_categories where active = true and name = $1 limit 2`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]uuid.UUID, 0, 2)
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return uniqueCategoryLeaf(ids), nil
}

func uniqueCategoryLeaf(ids []uuid.UUID) *uuid.UUID {
	if len(ids) != 1 || ids[0] == uuid.Nil {
		return nil
	}
	result := ids[0]
	return &result
}

func confidencePercent(value float64) int16 {
	return int16(math.Round(math.Max(0, math.Min(1, value)) * 100))
}

func nullableUUID(value string) *uuid.UUID {
	parsed, err := uuid.Parse(value)
	if err != nil {
		return nil
	}
	return &parsed
}

func boundedError(err error) string {
	value := strings.TrimSpace(err.Error())
	if len(value) > 2000 {
		return value[:2000]
	}
	return value
}

func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}

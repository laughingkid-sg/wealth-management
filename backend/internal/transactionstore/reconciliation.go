package transactionstore

import (
	"context"
	"crypto/sha256"
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
	CandidateID  uuid.UUID
	Candidate    reconciliation.Candidate
	Accounts     []reconciliation.AccountIdentity
	Transactions []reconciliation.Transaction
}

// ParsedSourceResult records one model call that produced zero or more valid
// candidates (one email may yield several transactions). InvalidCount is the
// number of candidates dropped by validation (D2: drop invalid, keep valid);
// it is recorded for the audit trail.
type ParsedSourceResult struct {
	SourceParseAudit
	SyncRunID    *uuid.UUID
	Candidates   []reconciliation.ParsedResponse
	InvalidCount int
}

// persistedSourceCandidate is the JSON stored in
// source_candidates.parsed_candidate: the parsed response plus the
// server-derived confidence and auto-eligibility that are json:"-" on the
// Candidate and must survive the round trip to reconciliation.
type persistedSourceCandidate struct {
	reconciliation.ParsedResponse
	Confidence   float64 `json:"confidence"`
	AutoEligible bool    `json:"auto_eligible"`
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
	// PreProcess records the pre-process outcome: "" (inert / no script),
	// "<key>:v<n>" (applied), or "fallback:<reason>" (script failed, original
	// content used).
	PreProcess string
	// PostProcess records the post-process outcome using the same convention.
	PostProcess string
}

type ReconciliationResult struct {
	SourceID    uuid.UUID
	CandidateID uuid.UUID
	SyncRunID   *uuid.UUID
	Candidate   reconciliation.Candidate
	Decision    reconciliation.Decision
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

// SaveParsedSource records one model call's valid candidates and queues a
// reconciliation job per candidate, atomically. One email may yield several
// transactions: each becomes a source_candidates row (origin gmail_email) under
// a single audit attempt. An empty candidate list is a valid no-transaction
// result: the source is marked parsed with no candidates and no jobs.
func (s *Store) SaveParsedSource(ctx context.Context, userID uuid.UUID, result ParsedSourceResult) error {
	if result.SourceID == uuid.Nil {
		return errors.New("valid source ID is required")
	}
	persisted := make([]persistedSourceCandidate, 0, len(result.Candidates))
	for _, response := range result.Candidates {
		persisted = append(persisted, persistedSourceCandidate{
			ParsedResponse: response,
			Confidence:     response.Candidate.Confidence,
			AutoEligible:   response.Candidate.AutoEligible,
		})
	}
	batchJSON, err := json.Marshal(map[string]any{"transactions": persisted, "invalid_count": result.InvalidCount})
	if err != nil {
		return err
	}
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
		"attachment_usage":         result.AttachmentUsage,
		"pre_process":              result.PreProcess, "post_process": result.PostProcess,
		"candidate_count": len(persisted), "invalid_count": result.InvalidCount,
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
	var attemptID uuid.UUID
	if err = tx.QueryRow(ctx, `
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
		) returning id`, userID, result.SourceID, ruleID, result.RuleVersion,
		userRuleID, result.UserRuleVersion, result.Model, string(metadata),
		string(batchJSON), result.AssembledSystemPrompt,
		result.NormalizedInput, nullableJSON(result.ProviderRequest),
		nullableJSON(result.ProviderResponse), nullableJSON(result.ModelOutput),
		promptComponentsJSON(result.PromptComponents)).Scan(&attemptID); err != nil {
		return err
	}
	for index, candidate := range persisted {
		canonical, marshalErr := json.Marshal(candidate)
		if marshalErr != nil {
			return marshalErr
		}
		digest := sha256.Sum256(canonical)
		var candidateID uuid.UUID
		if err = tx.QueryRow(ctx, `
			insert into private.source_candidates (
				user_id, data_source_id, source_parse_attempt_id, origin,
				attempt_generation, output_ordinal, fingerprint, parsed_candidate, status
			) values ($1, $2, $3, 'gmail_email', 1, $4, $5, $6::jsonb, 'pending_reconciliation')
			returning id`, userID, result.SourceID, attemptID, index, digest[:], string(canonical)).Scan(&candidateID); err != nil {
			return err
		}
		payload, payloadErr := json.Marshal(map[string]string{
			"data_source_id": result.SourceID.String(), "source_candidate_id": candidateID.String(),
		})
		if payloadErr != nil {
			return payloadErr
		}
		if _, err = tx.Exec(ctx, `
			insert into private.transaction_jobs (user_id, sync_run_id, data_source_id, job_type, payload)
			values ($1, $2, $3, $4, $5::jsonb)`,
			userID, result.SyncRunID, result.SourceID, string(jobs.KindReconcile), string(payload)); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `
		update private.data_sources set parse_status = 'parsed', parse_confidence = null,
			parse_error = null, suggested_account_id = null, parser_rule_id = $3, parser_rule_version = nullif($4, 0)
		where id = $1 and user_id = $2`, result.SourceID, userID, ruleID, result.RuleVersion); err != nil {
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
		"pre_process":              audit.PreProcess,
		"post_process":             audit.PostProcess,
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

func (s *Store) LoadReconciliationInput(ctx context.Context, userID, sourceID, candidateID uuid.UUID) (ReconciliationInput, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx, `
		select parsed_candidate from private.source_candidates
		where id = $1 and user_id = $2 and data_source_id = $3 and status = 'pending_reconciliation'`,
		candidateID, userID, sourceID).Scan(&raw)
	if err != nil {
		return ReconciliationInput{}, err
	}
	parsed, err := decodePersistedSourceCandidate(raw, userID)
	if err != nil {
		return ReconciliationInput{}, fmt.Errorf("decode persisted parse result: %w", err)
	}
	accounts, err := s.loadOwnedAccountIdentities(ctx, userID)
	if err != nil {
		return ReconciliationInput{}, err
	}
	// Exclude transactions already linked to this source so one email's later
	// candidate cannot auto-attach to a transaction an earlier candidate created.
	transactions, err := loadOwnedTransactionsExcludingSource(ctx, s.pool, userID, parsed.Candidate.OccurredAt, sourceID)
	if err != nil {
		return ReconciliationInput{}, err
	}
	return ReconciliationInput{SourceID: sourceID, CandidateID: candidateID, Candidate: parsed.Candidate, Accounts: accounts, Transactions: transactions}, nil
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

// loadOwnedTransactionsExcludingSource loads candidate match transactions in the
// ±24h window, optionally excluding transactions already linked to excludeSource
// (pass uuid.Nil to exclude none). The exclusion prevents one email's later
// candidate from auto-attaching to a transaction an earlier candidate created.
func loadOwnedTransactionsExcludingSource(ctx context.Context, querier reconciliationQuerier, userID uuid.UUID, occurredAt time.Time, excludeSource uuid.UUID) ([]reconciliation.Transaction, error) {
	rows, err := querier.Query(ctx, `
		select id, account_id, transaction_kind, coalesce(merchant_name, ''), original_amount_minor,
			original_currency, occurred_at, coalesce(details -> 'references', '[]'::jsonb)
		from public.transactions
		where user_id = $1 and occurred_at between $2::timestamptz - interval '24 hours' and $2::timestamptz + interval '24 hours'
			and id not in (
				select transaction_id from private.transaction_data_sources
				where user_id = $1 and data_source_id = $3 and detached_at is null and transaction_id is not null
			)`, userID, occurredAt, excludeSource)
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
	if result.CandidateID == uuid.Nil {
		return errors.New("reconciliation requires a candidate ID")
	}
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
	// Guard on the candidate's own status, not the source rollup: sibling
	// candidates from the same email reconcile independently.
	var candidateStatus string
	if err = tx.QueryRow(ctx, `select status from private.source_candidates where id = $1 and user_id = $2 and data_source_id = $3 for update`, result.CandidateID, userID, result.SourceID).Scan(&candidateStatus); err != nil {
		return err
	}
	if candidateStatus != "pending_reconciliation" {
		return tx.Commit(ctx)
	}
	if serializedCreate {
		accounts, loadErr := loadOwnedAccountIdentities(ctx, tx, userID)
		if loadErr != nil {
			return loadErr
		}
		transactions, loadErr := loadOwnedTransactionsExcludingSource(ctx, tx, userID, result.Candidate.OccurredAt, result.SourceID)
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
		transactionID, perr := uuid.Parse(result.Decision.TransactionID)
		if perr != nil {
			return errors.New("reconciliation selected an invalid transaction")
		}
		command, cerr := tx.Exec(ctx, `
			insert into private.transaction_data_sources (user_id, transaction_id, data_source_id, bulk_import_candidate_id, role, match_confidence, matched_by)
			select $1, transaction.id, $2, $5, 'other', $3, 'automatic'
			from public.transactions transaction where transaction.id = $4 and transaction.user_id = $1`, userID, result.SourceID, score, transactionID, result.CandidateID)
		if cerr != nil {
			return cerr
		}
		if command.RowsAffected() != 1 {
			return errors.New("reconciliation selected a transaction not owned by the source user")
		}
		if _, err = tx.Exec(ctx, `update private.source_candidates set status = 'attached', transaction_id = $3, account_id = coalesce($4, account_id), match_confidence = $5, suggested_account_id = null, suggested_transaction_id = null, reconciliation_reason = null where id = $1 and user_id = $2`, result.CandidateID, userID, transactionID, suggestedAccount, score); err != nil {
			return err
		}
		attached = 1
	case reconciliation.OutcomeCreate:
		accountID, perr := uuid.Parse(result.Decision.AccountID)
		if perr != nil {
			return errors.New("reconciliation selected an invalid account")
		}
		lineItems, merr := json.Marshal(result.Candidate.LineItems)
		if merr != nil {
			return merr
		}
		details, merr := json.Marshal(map[string]any{"references": result.Candidate.References, "account_evidence": result.Candidate.AccountEvidence})
		if merr != nil {
			return merr
		}
		categoryID, cerr := s.resolveCategoryLeaf(ctx, tx, result.Candidate.CategoryLeafName)
		if cerr != nil {
			return cerr
		}
		var transactionID uuid.UUID
		if err = tx.QueryRow(ctx, `
			insert into public.transactions (user_id, account_id, transaction_kind, title, merchant_name,
				original_amount_minor, original_currency, sgd_amount_minor, occurred_at, category_id, line_items, details,
				review_status, match_confidence, creation_method)
			values ($1, $2, $3, $4, nullif($5, ''), $6, $7, $8, $9, $10, $11::jsonb, $12::jsonb, 'confirmed', $13, 'automatic_source')
			returning id`, userID, accountID, string(result.Candidate.Kind), result.Candidate.Title,
			result.Candidate.MerchantName, result.Candidate.OriginalAmountMinor, result.Candidate.OriginalCurrency,
			result.Candidate.SGDAmountMinor, result.Candidate.OccurredAt, categoryID, string(lineItems), string(details), confidence).Scan(&transactionID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `insert into private.transaction_data_sources (user_id, transaction_id, data_source_id, bulk_import_candidate_id, role, match_confidence, matched_by) values ($1, $2, $3, $4, 'other', $5, 'automatic')`, userID, transactionID, result.SourceID, result.CandidateID, confidence); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `update private.source_candidates set status = 'created', transaction_id = $3, account_id = $4, match_confidence = $5, suggested_account_id = null, suggested_transaction_id = null, reconciliation_reason = null where id = $1 and user_id = $2`, result.CandidateID, userID, transactionID, accountID, confidence); err != nil {
			return err
		}
		created, attached = 1, 1
	case reconciliation.OutcomeReview:
		if _, err = tx.Exec(ctx, `update private.source_candidates set status = 'review_required', suggested_account_id = $3, suggested_transaction_id = $4, reconciliation_reason = $5, match_confidence = $6 where id = $1 and user_id = $2`, result.CandidateID, userID, suggestedAccount, suggestedTransaction, result.Decision.Reason, score); err != nil {
			return err
		}
		review = 1
	case reconciliation.OutcomeDangling:
		if _, err = tx.Exec(ctx, `update private.source_candidates set status = 'dangling', suggested_account_id = null, suggested_transaction_id = null, reconciliation_reason = $3 where id = $1 and user_id = $2`, result.CandidateID, userID, result.Decision.Reason); err != nil {
			return err
		}
		dangling = 1
	default:
		return fmt.Errorf("unsupported reconciliation outcome %q", result.Decision.Outcome)
	}
	// Recompute the source rollup from all its candidates.
	if err = recomputeSourceParseRollup(ctx, tx, userID, result.SourceID); err != nil {
		return err
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

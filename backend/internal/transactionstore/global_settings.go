package transactionstore

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var (
	ErrGlobalSourceRuleNotFound = errors.New("global source parser rule not found")
	ErrGlobalSourceRuleConflict = errors.New("global source parser rule version conflict")
)

// GlobalSourceParserRule is the editable global rule projection. Its
// extraction configuration remains visible for diagnostics but is never part
// of browser mutation input.
type GlobalSourceParserRule struct {
	ID               uuid.UUID       `json:"id"`
	Name             string          `json:"name"`
	Provider         string          `json:"provider"`
	SenderMatcher    *string         `json:"sender_matcher"`
	ContentMatcher   *string         `json:"content_matcher"`
	PromptFragment   string          `json:"prompt_fragment"`
	ExtractionConfig json.RawMessage `json:"extraction_config"`
	Version          int             `json:"version"`
	Priority         int             `json:"priority"`
	Active           bool            `json:"active"`
	UpdatedByUserID  *uuid.UUID      `json:"updated_by_user_id"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

type GlobalSourceParserRuleInput struct {
	Name            string
	Provider        string
	SenderMatcher   *string
	ContentMatcher  *string
	PromptFragment  string
	Priority        int
	Active          bool
	ExpectedVersion int
}

type PromptPreviewSource struct {
	ID          uuid.UUID `json:"id"`
	Subject     string    `json:"subject"`
	Sender      string    `json:"sender"`
	ReceivedAt  time.Time `json:"received_at"`
	ParseStatus string    `json:"parse_status"`
}

func (s *Store) ListGlobalSourceParserRules(ctx context.Context) ([]GlobalSourceParserRule, error) {
	rows, err := s.pool.Query(ctx, `
		select id, name, provider, sender_matcher, content_matcher,
			coalesce(prompt_fragment, ''), extraction_config, version,
			priority, active, updated_by_user_id, created_at, updated_at
		from private.source_parser_rules
		order by priority desc, name asc, id asc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rules := make([]GlobalSourceParserRule, 0)
	for rows.Next() {
		var rule GlobalSourceParserRule
		if err = rows.Scan(globalSourceParserRuleFields(&rule)...); err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	return rules, rows.Err()
}

func (s *Store) GetGlobalSourceParserRule(ctx context.Context, ruleID uuid.UUID) (GlobalSourceParserRule, error) {
	var rule GlobalSourceParserRule
	err := s.pool.QueryRow(ctx, `
		select id, name, provider, sender_matcher, content_matcher,
			coalesce(prompt_fragment, ''), extraction_config, version,
			priority, active, updated_by_user_id, created_at, updated_at
		from private.source_parser_rules
		where id = $1`, ruleID).Scan(globalSourceParserRuleFields(&rule)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return GlobalSourceParserRule{}, ErrGlobalSourceRuleNotFound
	}
	return rule, err
}

func (s *Store) CreateGlobalSourceParserRule(ctx context.Context, userID uuid.UUID, input GlobalSourceParserRuleInput) (GlobalSourceParserRule, error) {
	var rule GlobalSourceParserRule
	err := s.pool.QueryRow(ctx, `
		insert into private.source_parser_rules (
			name, provider, sender_matcher, content_matcher, prompt_fragment,
			extraction_config, priority, active, updated_by_user_id
		) values ($1, $2, $3, $4, $5, '{}'::jsonb, $6, $7, $8)
		returning id, name, provider, sender_matcher, content_matcher,
			coalesce(prompt_fragment, ''), extraction_config, version,
			priority, active, updated_by_user_id, created_at, updated_at`,
		input.Name, input.Provider, input.SenderMatcher, input.ContentMatcher,
		input.PromptFragment, input.Priority, input.Active, userID,
	).Scan(globalSourceParserRuleFields(&rule)...)
	return rule, err
}

// UpdateGlobalSourceParserRule uses optimistic concurrency and deliberately
// omits extraction_config so browser edits cannot replace deterministic logic.
func (s *Store) UpdateGlobalSourceParserRule(ctx context.Context, userID, ruleID uuid.UUID, input GlobalSourceParserRuleInput) (GlobalSourceParserRule, error) {
	var rule GlobalSourceParserRule
	err := s.pool.QueryRow(ctx, `
		update private.source_parser_rules
		set name = $3, provider = $4, sender_matcher = $5,
			content_matcher = $6, prompt_fragment = $7, priority = $8,
			active = $9, updated_by_user_id = $10, version = version + 1
		where id = $1 and version = $2
		returning id, name, provider, sender_matcher, content_matcher,
			coalesce(prompt_fragment, ''), extraction_config, version,
			priority, active, updated_by_user_id, created_at, updated_at`,
		ruleID, input.ExpectedVersion, input.Name, input.Provider,
		input.SenderMatcher, input.ContentMatcher, input.PromptFragment,
		input.Priority, input.Active, userID,
	).Scan(globalSourceParserRuleFields(&rule)...)
	if err == nil {
		return rule, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return GlobalSourceParserRule{}, err
	}
	var exists bool
	if existsErr := s.pool.QueryRow(ctx, `select exists(select 1 from private.source_parser_rules where id = $1)`, ruleID).Scan(&exists); existsErr != nil {
		return GlobalSourceParserRule{}, existsErr
	}
	if exists {
		return GlobalSourceParserRule{}, ErrGlobalSourceRuleConflict
	}
	return GlobalSourceParserRule{}, ErrGlobalSourceRuleNotFound
}

func (s *Store) GetDefaultParserInstructions(ctx context.Context, userID uuid.UUID) (DefaultParserInstructions, error) {
	var value DefaultParserInstructions
	err := s.pool.QueryRow(ctx, `
		select default_instructions, version
		from private.user_parser_settings
		where user_id = $1`, userID).Scan(&value.DefaultInstructions, &value.DefaultInstructionsVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return DefaultParserInstructions{}, nil
	}
	return value, err
}

func (s *Store) GetUserSourceParserRule(ctx context.Context, userID, ruleID uuid.UUID) (UserSourceParserRule, error) {
	var rule UserSourceParserRule
	err := s.pool.QueryRow(ctx, `
		select id, name, provider, sender_match_type, sender_match_value,
			subject_matcher, content_matcher, prompt_fragment, priority, active,
			version, created_at, updated_at
		from private.user_source_parser_rules
		where id = $1 and user_id = $2`, ruleID, userID).Scan(userSourceParserRuleFields(&rule)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return UserSourceParserRule{}, ErrUserSourceRuleNotFound
	}
	return rule, err
}

func (s *Store) ListPromptPreviewSources(ctx context.Context, userID uuid.UUID, limit int) ([]PromptPreviewSource, error) {
	if limit < 1 || limit > 100 {
		return nil, errors.New("prompt preview source limit must be between 1 and 100")
	}
	rows, err := s.pool.Query(ctx, `
		select id, coalesce(raw_data ->> 'subject', ''),
			coalesce(raw_data ->> 'sender', ''), received_at, parse_status
		from private.data_sources
		where user_id = $1 and source_type = 'gmail_email' and provider = 'gmail'
		order by received_at desc, id desc
		limit $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sources := make([]PromptPreviewSource, 0)
	for rows.Next() {
		var source PromptPreviewSource
		if err = rows.Scan(&source.ID, &source.Subject, &source.Sender, &source.ReceivedAt, &source.ParseStatus); err != nil {
			return nil, err
		}
		sources = append(sources, source)
	}
	return sources, rows.Err()
}

func globalSourceParserRuleFields(rule *GlobalSourceParserRule) []any {
	return []any{
		&rule.ID, &rule.Name, &rule.Provider, &rule.SenderMatcher,
		&rule.ContentMatcher, &rule.PromptFragment, &rule.ExtractionConfig,
		&rule.Version, &rule.Priority, &rule.Active, &rule.UpdatedByUserID,
		&rule.CreatedAt, &rule.UpdatedAt,
	}
}

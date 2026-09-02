package transactionstore

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/zhengteck/wealth-builder/backend/internal/reconciliation"
)

var (
	ErrUserSourceRuleNotFound = errors.New("user source parser rule not found")
	ErrMatchingKeyNotFound    = errors.New("account matching key not found")
	ErrMatchingKeyConflict    = errors.New("account matching key already exists")
)

// TransactionSettings is the complete owner-scoped configuration projection
// used by the independent Transactions settings page.
type TransactionSettings struct {
	DefaultInstructions        string                 `json:"default_instructions"`
	DefaultInstructionsVersion int                    `json:"default_instructions_version"`
	SourceRules                []UserSourceParserRule `json:"source_rules"`
	MatchingKeys               []AccountMatchingKey   `json:"matching_keys"`
}

type DefaultParserInstructions struct {
	DefaultInstructions        string `json:"default_instructions"`
	DefaultInstructionsVersion int    `json:"default_instructions_version"`
}

type UserSourceParserRule struct {
	ID               uuid.UUID `json:"id"`
	Name             string    `json:"name"`
	Provider         string    `json:"provider"`
	SenderMatchType  string    `json:"sender_match_type"`
	SenderMatchValue string    `json:"sender_match_value"`
	SubjectMatcher   *string   `json:"subject_matcher"`
	ContentMatcher   *string   `json:"content_matcher"`
	PromptFragment   string    `json:"prompt_fragment"`
	Priority         int       `json:"priority"`
	Active           bool      `json:"active"`
	Version          int       `json:"version"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type UserSourceParserRuleInput struct {
	Name             string
	Provider         string
	SenderMatchType  string
	SenderMatchValue string
	SubjectMatcher   *string
	ContentMatcher   *string
	PromptFragment   string
	Priority         int
	Active           bool
}

type AccountMatchingKey struct {
	ID              uuid.UUID  `json:"id"`
	AccountID       uuid.UUID  `json:"account_id"`
	AccountName     string     `json:"account_name"`
	KeyType         string     `json:"key_type"`
	DisplayValue    string     `json:"display_value"`
	NormalizedValue string     `json:"normalized_value"`
	Active          bool       `json:"active"`
	RetiredAt       *time.Time `json:"retired_at"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

type AccountMatchingKeyInput struct {
	AccountID    uuid.UUID
	KeyType      string
	DisplayValue string
}

func (s *Store) GetTransactionSettings(ctx context.Context, userID uuid.UUID) (TransactionSettings, error) {
	settings := TransactionSettings{SourceRules: []UserSourceParserRule{}, MatchingKeys: []AccountMatchingKey{}}
	err := s.pool.QueryRow(ctx, `
		select default_instructions, version
		from private.user_parser_settings
		where user_id = $1`, userID).Scan(&settings.DefaultInstructions, &settings.DefaultInstructionsVersion)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return TransactionSettings{}, err
	}

	rows, err := s.pool.Query(ctx, `
		select id, name, provider, sender_match_type, sender_match_value,
			subject_matcher, content_matcher, prompt_fragment, priority, active,
			version, created_at, updated_at
		from private.user_source_parser_rules
		where user_id = $1
		order by priority desc, name asc, id asc`, userID)
	if err != nil {
		return TransactionSettings{}, err
	}
	for rows.Next() {
		var rule UserSourceParserRule
		if err = rows.Scan(
			&rule.ID, &rule.Name, &rule.Provider, &rule.SenderMatchType,
			&rule.SenderMatchValue, &rule.SubjectMatcher, &rule.ContentMatcher,
			&rule.PromptFragment, &rule.Priority, &rule.Active, &rule.Version,
			&rule.CreatedAt, &rule.UpdatedAt,
		); err != nil {
			rows.Close()
			return TransactionSettings{}, err
		}
		settings.SourceRules = append(settings.SourceRules, rule)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return TransactionSettings{}, err
	}
	rows.Close()

	rows, err = s.pool.Query(ctx, `
		select matching_key.id, matching_key.account_id, account.name,
			matching_key.key_type, matching_key.display_value,
			matching_key.normalized_value, matching_key.active,
			matching_key.retired_at, matching_key.created_at, matching_key.updated_at
		from private.account_matching_keys matching_key
		join public.accounts account
			on account.id = matching_key.account_id
			and account.user_id = matching_key.user_id
		where matching_key.user_id = $1
		order by account.name asc, matching_key.key_type asc,
			matching_key.normalized_value asc, matching_key.id asc`, userID)
	if err != nil {
		return TransactionSettings{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var key AccountMatchingKey
		if err = rows.Scan(
			&key.ID, &key.AccountID, &key.AccountName, &key.KeyType,
			&key.DisplayValue, &key.NormalizedValue, &key.Active,
			&key.RetiredAt, &key.CreatedAt, &key.UpdatedAt,
		); err != nil {
			return TransactionSettings{}, err
		}
		settings.MatchingKeys = append(settings.MatchingKeys, key)
	}
	return settings, rows.Err()
}

func (s *Store) PutDefaultParserInstructions(ctx context.Context, userID uuid.UUID, instructions string) (DefaultParserInstructions, error) {
	instructions = strings.TrimSpace(instructions)
	var saved DefaultParserInstructions
	err := s.pool.QueryRow(ctx, `
		insert into private.user_parser_settings (user_id, default_instructions)
		values ($1, $2)
		on conflict (user_id) do update
		set default_instructions = excluded.default_instructions,
			version = private.user_parser_settings.version + 1
		returning default_instructions, version`, userID, instructions).Scan(
		&saved.DefaultInstructions, &saved.DefaultInstructionsVersion,
	)
	return saved, err
}

func (s *Store) CreateUserSourceParserRule(ctx context.Context, userID uuid.UUID, input UserSourceParserRuleInput) (UserSourceParserRule, error) {
	var rule UserSourceParserRule
	err := s.pool.QueryRow(ctx, `
		insert into private.user_source_parser_rules (
			user_id, name, provider, sender_match_type, sender_match_value,
			subject_matcher, content_matcher, prompt_fragment, priority, active
		) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		returning id, name, provider, sender_match_type, sender_match_value,
			subject_matcher, content_matcher, prompt_fragment, priority, active,
			version, created_at, updated_at`,
		userID, input.Name, input.Provider, input.SenderMatchType,
		input.SenderMatchValue, input.SubjectMatcher, input.ContentMatcher,
		input.PromptFragment, input.Priority, input.Active,
	).Scan(userSourceParserRuleFields(&rule)...)
	return rule, err
}

func (s *Store) UpdateUserSourceParserRule(ctx context.Context, userID, ruleID uuid.UUID, input UserSourceParserRuleInput) (UserSourceParserRule, error) {
	var rule UserSourceParserRule
	err := s.pool.QueryRow(ctx, `
		update private.user_source_parser_rules
		set name = $3, provider = $4, sender_match_type = $5,
			sender_match_value = $6, subject_matcher = $7, content_matcher = $8,
			prompt_fragment = $9, priority = $10, active = $11,
			version = version + 1
		where id = $1 and user_id = $2
		returning id, name, provider, sender_match_type, sender_match_value,
			subject_matcher, content_matcher, prompt_fragment, priority, active,
			version, created_at, updated_at`,
		ruleID, userID, input.Name, input.Provider, input.SenderMatchType,
		input.SenderMatchValue, input.SubjectMatcher, input.ContentMatcher,
		input.PromptFragment, input.Priority, input.Active,
	).Scan(userSourceParserRuleFields(&rule)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return UserSourceParserRule{}, ErrUserSourceRuleNotFound
	}
	return rule, err
}

// RetireUserSourceParserRule preserves rule provenance for parse-attempt audit.
func (s *Store) RetireUserSourceParserRule(ctx context.Context, userID, ruleID uuid.UUID) error {
	command, err := s.pool.Exec(ctx, `
		update private.user_source_parser_rules
		set active = false, version = version + 1
		where id = $1 and user_id = $2`, ruleID, userID)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return ErrUserSourceRuleNotFound
	}
	return nil
}

func (s *Store) CreateAccountMatchingKey(ctx context.Context, userID uuid.UUID, input AccountMatchingKeyInput) (AccountMatchingKey, error) {
	display := strings.TrimSpace(input.DisplayValue)
	normalized, err := reconciliation.NormalizeAccountMatchingKey(input.KeyType, display)
	if err != nil {
		return AccountMatchingKey{}, err
	}
	var key AccountMatchingKey
	err = s.pool.QueryRow(ctx, `
		insert into private.account_matching_keys as matching_key (
			user_id, account_id, key_type, display_value, normalized_value
		)
		select $1, account.id, $3, $4, $5
		from public.accounts account
		where account.id = $2 and account.user_id = $1 and account.deleted_at is null
		returning matching_key.id, matching_key.account_id,
			(select account.name from public.accounts account
				where account.id = matching_key.account_id and account.user_id = matching_key.user_id),
			matching_key.key_type, matching_key.display_value,
			matching_key.normalized_value, matching_key.active,
			matching_key.retired_at, matching_key.created_at, matching_key.updated_at`,
		userID, input.AccountID, input.KeyType, display, normalized,
	).Scan(accountMatchingKeyFields(&key)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return AccountMatchingKey{}, ErrAccountNotFound
	}
	if isUniqueViolation(err) {
		return AccountMatchingKey{}, ErrMatchingKeyConflict
	}
	return key, err
}

// SetAccountMatchingKeyActive is the only mutation supported after creation;
// account, type and value identity remain immutable for permanent provenance.
func (s *Store) SetAccountMatchingKeyActive(ctx context.Context, userID, keyID uuid.UUID, active bool) (AccountMatchingKey, error) {
	var key AccountMatchingKey
	err := s.pool.QueryRow(ctx, `
		update private.account_matching_keys matching_key
		set active = $3, retired_at = case when $3 then null else now() end
		where matching_key.id = $1 and matching_key.user_id = $2
			and (not $3 or exists (
				select 1 from public.accounts account
				where account.id = matching_key.account_id
					and account.user_id = matching_key.user_id
					and account.deleted_at is null
			))
		returning matching_key.id, matching_key.account_id,
			(select name from public.accounts where id = matching_key.account_id and user_id = matching_key.user_id),
			matching_key.key_type, matching_key.display_value,
			matching_key.normalized_value, matching_key.active,
			matching_key.retired_at, matching_key.created_at, matching_key.updated_at`,
		keyID, userID, active,
	).Scan(accountMatchingKeyFields(&key)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return AccountMatchingKey{}, ErrMatchingKeyNotFound
	}
	if isUniqueViolation(err) {
		return AccountMatchingKey{}, ErrMatchingKeyConflict
	}
	return key, err
}

func userSourceParserRuleFields(rule *UserSourceParserRule) []any {
	return []any{
		&rule.ID, &rule.Name, &rule.Provider, &rule.SenderMatchType,
		&rule.SenderMatchValue, &rule.SubjectMatcher, &rule.ContentMatcher,
		&rule.PromptFragment, &rule.Priority, &rule.Active, &rule.Version,
		&rule.CreatedAt, &rule.UpdatedAt,
	}
}

func accountMatchingKeyFields(key *AccountMatchingKey) []any {
	return []any{
		&key.ID, &key.AccountID, &key.AccountName, &key.KeyType,
		&key.DisplayValue, &key.NormalizedValue, &key.Active,
		&key.RetiredAt, &key.CreatedAt, &key.UpdatedAt,
	}
}

func isUniqueViolation(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == "23505"
}

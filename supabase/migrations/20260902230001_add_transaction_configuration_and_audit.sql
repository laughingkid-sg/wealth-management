-- Add server-only transaction matching and parser configuration together with
-- complete model-call audit provenance. All configuration remains in the
-- non-exposed private schema and is reachable only by trusted backend roles.

alter table private.source_parser_rules
  add column prompt_fragment text not null default '',
  add constraint source_parser_rules_prompt_fragment_length_check
    check (char_length(prompt_fragment) <= 4000);

-- PostgreSQL's locale-sensitive whitespace classes do not consistently cover
-- every code point handled by Go's unicode.IsSpace. Use the explicit Unicode
-- set so legacy backfill and database checks share the confirmed semantics.
create or replace function private.normalize_account_matching_value(
  matching_key_type text,
  matching_value text
)
returns text
language sql
immutable
strict
parallel safe
set search_path = ''
as $function$
  select case matching_key_type
    when 'card_last_four' then translate(
      matching_value,
      concat(
        chr(9), chr(10), chr(11), chr(12), chr(13), ' ', chr(133), chr(160),
        chr(5760), chr(8192), chr(8193), chr(8194), chr(8195), chr(8196),
        chr(8197), chr(8198), chr(8199), chr(8200), chr(8201), chr(8202),
        chr(8232), chr(8233), chr(8239), chr(8287), chr(12288), '*•-xX'
      ),
      ''
    )
    when 'bank_account_suffix' then translate(
      lower(matching_value),
      concat(
        chr(9), chr(10), chr(11), chr(12), chr(13), ' ', chr(133), chr(160),
        chr(5760), chr(8192), chr(8193), chr(8194), chr(8195), chr(8196),
        chr(8197), chr(8198), chr(8199), chr(8200), chr(8201), chr(8202),
        chr(8232), chr(8233), chr(8239), chr(8287), chr(12288), '*•-'
      ),
      ''
    )
  end;
$function$;

revoke execute on function private.normalize_account_matching_value(text, text)
  from public, anon, authenticated;

create table private.account_matching_keys (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references auth.users(id) on delete cascade,
  account_id uuid not null,
  key_type text not null,
  display_value text not null,
  normalized_value text not null,
  active boolean not null default true,
  retired_at timestamptz,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  constraint account_matching_keys_user_account_fkey
    foreign key (user_id, account_id)
    references public.accounts (user_id, id)
    on delete cascade,
  constraint account_matching_keys_key_type_check
    check (key_type in ('card_last_four', 'bank_account_suffix')),
  constraint account_matching_keys_display_value_check
    check (char_length(btrim(display_value)) between 1 and 100),
  constraint account_matching_keys_normalized_value_check check (
    (key_type = 'card_last_four' and normalized_value ~ '^[0-9]{4}$')
    or
    (
      key_type = 'bank_account_suffix'
      and char_length(normalized_value) between 1 and 100
      and normalized_value = private.normalize_account_matching_value(
        'bank_account_suffix',
        normalized_value
      )
    )
  ),
  constraint account_matching_keys_active_retired_at_check
    check (active = (retired_at is null)),
  constraint account_matching_keys_retired_at_chronology_check
    check (retired_at is null or retired_at >= created_at),
  constraint account_matching_keys_user_type_normalized_key
    unique (user_id, key_type, normalized_value)
);

comment on table private.account_matching_keys is
  'Immutable Account matching identities. Corrections retire a key and insert a new one; a retired row may be reactivated.';

create table private.user_parser_settings (
  user_id uuid primary key references auth.users(id) on delete cascade,
  default_instructions text not null default '',
  version integer not null default 1,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  constraint user_parser_settings_default_instructions_length_check
    check (char_length(default_instructions) <= 4000),
  constraint user_parser_settings_version_check check (version > 0)
);

create table private.user_source_parser_rules (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references auth.users(id) on delete cascade,
  name text not null,
  provider text not null default 'gmail',
  sender_match_type text not null,
  sender_match_value text not null,
  subject_matcher text,
  content_matcher text,
  prompt_fragment text not null default '',
  priority integer not null default 0,
  active boolean not null default true,
  version integer not null default 1,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  constraint user_source_parser_rules_name_check
    check (char_length(btrim(name)) between 1 and 100),
  constraint user_source_parser_rules_provider_check check (provider = 'gmail'),
  constraint user_source_parser_rules_sender_match_type_check
    check (sender_match_type in ('exact', 'domain', 'regex')),
  constraint user_source_parser_rules_sender_match_value_check
    check (char_length(btrim(sender_match_value)) between 1 and 500),
  constraint user_source_parser_rules_subject_matcher_check
    check (subject_matcher is null or char_length(btrim(subject_matcher)) between 1 and 1000),
  constraint user_source_parser_rules_content_matcher_check
    check (content_matcher is null or char_length(btrim(content_matcher)) between 1 and 1000),
  constraint user_source_parser_rules_prompt_fragment_length_check
    check (char_length(prompt_fragment) <= 4000),
  constraint user_source_parser_rules_version_check check (version > 0),
  constraint user_source_parser_rules_user_id_id_key unique (user_id, id)
);

comment on table private.user_parser_settings is
  'One server-only parser instruction record per authenticated user.';

comment on table private.user_source_parser_rules is
  'User-owned Gmail sender/content selection and prompt fragments, with regular expressions validated as RE2 by the Go API.';

-- Keep matching-key identity immutable. Lifecycle fields may toggle together
-- so an accidental retirement can be corrected without defeating permanent
-- uniqueness or assigning the same identifier to another Account.
create or replace function private.protect_account_matching_key_identity()
returns trigger
language plpgsql
set search_path = ''
as $function$
begin
  if old.id is distinct from new.id
    or old.user_id is distinct from new.user_id
    or old.account_id is distinct from new.account_id
    or old.key_type is distinct from new.key_type
    or old.display_value is distinct from new.display_value
    or old.normalized_value is distinct from new.normalized_value
    or old.created_at is distinct from new.created_at then
    raise exception using
      errcode = '23514',
      message = 'account matching key identity and value fields are immutable';
  end if;

  return new;
end;
$function$;

revoke execute on function private.protect_account_matching_key_identity()
  from public, anon, authenticated;

create trigger account_matching_keys_protect_identity
before update on private.account_matching_keys
for each row execute function private.protect_account_matching_key_identity();

create trigger account_matching_keys_set_updated_at
before update on private.account_matching_keys
for each row execute function public.set_updated_at();

create trigger user_parser_settings_set_updated_at
before update on private.user_parser_settings
for each row execute function public.set_updated_at();

create trigger user_source_parser_rules_set_updated_at
before update on private.user_source_parser_rules
for each row execute function public.set_updated_at();

create index account_matching_keys_user_account_id_idx
  on private.account_matching_keys (user_id, account_id);

create index user_source_parser_rules_user_active_priority_idx
  on private.user_source_parser_rules (user_id, provider, priority desc, id)
  where active;

alter table private.account_matching_keys enable row level security;
alter table private.user_parser_settings enable row level security;
alter table private.user_source_parser_rules enable row level security;

revoke all privileges on table private.account_matching_keys from public, anon, authenticated;
revoke all privileges on table private.user_parser_settings from public, anon, authenticated;
revoke all privileges on table private.user_source_parser_rules from public, anon, authenticated;

-- Translate only recognized legacy metadata names. Metadata remains untouched.
-- Keeping the idempotent backfill private and ungranted lets pgTAP verify the
-- upgrade path without making it a browser-accessible write API.
create or replace function private.backfill_account_matching_keys_from_metadata()
returns integer
language plpgsql
set search_path = ''
as $function$
declare
  candidate record;
  existing_account_id uuid;
  inserted_count integer := 0;
begin
  for candidate in
    with metadata_values as (
      select
        account.user_id,
        account.id as account_id,
        metadata.key as metadata_key,
        case
          when jsonb_typeof(metadata.value) in ('string', 'number')
            then btrim(metadata.value #>> '{}')
        end as display_value,
        lower(regexp_replace(metadata.key, '[^[:alnum:]]', '', 'g')) as metadata_key_normalized
      from public.accounts account
      cross join lateral jsonb_each(account.metadata) metadata
    ), typed_values as (
      select
        user_id,
        account_id,
        metadata_key,
        display_value,
        case
          when metadata_key_normalized in (
            'last4digit',
            'last4digits',
            'lastfourdigit',
            'lastfourdigits',
            'cardlast4',
            'cardlastfour',
            'cardlast4digit',
            'cardlastfourdigits',
            'cardending',
            'cardnumberlast4'
          ) then 'card_last_four'
          when metadata_key_normalized in (
            'bankaccountsuffix',
            'bankaccountlast4',
            'bankaccountlastfour',
            'accountsuffix',
            'accountlast4',
            'accountlastfour',
            'maskedbankreference'
          ) then 'bank_account_suffix'
        end as key_type
      from metadata_values
    ), normalized_values as (
      select
        user_id,
        account_id,
        metadata_key,
        key_type,
        display_value,
        -- Remove only whitespace and the confirmed masking characters. Other
        -- punctuation remains part of the matching identity.
        private.normalize_account_matching_value(key_type, display_value) as normalized_value
      from typed_values
      where key_type is not null
        and display_value is not null
        and char_length(display_value) between 1 and 100
    )
    select distinct on (user_id, account_id, key_type, normalized_value)
      user_id,
      account_id,
      key_type,
      display_value,
      normalized_value
    from normalized_values
    where (key_type = 'card_last_four' and normalized_value ~ '^[0-9]{4}$')
      or (key_type = 'bank_account_suffix' and char_length(normalized_value) between 1 and 100)
    order by user_id, account_id, key_type, normalized_value, metadata_key
  loop
    existing_account_id := null;

    select matching_key.account_id
    into existing_account_id
    from private.account_matching_keys matching_key
    where matching_key.user_id = candidate.user_id
      and matching_key.key_type = candidate.key_type
      and matching_key.normalized_value = candidate.normalized_value;

    if found then
      if existing_account_id is distinct from candidate.account_id then
        raise exception using
          errcode = '23505',
          message = 'legacy Account metadata contains a matching key assigned to multiple Accounts';
      end if;
      continue;
    end if;

    insert into private.account_matching_keys (
      user_id,
      account_id,
      key_type,
      display_value,
      normalized_value
    ) values (
      candidate.user_id,
      candidate.account_id,
      candidate.key_type,
      candidate.display_value,
      candidate.normalized_value
    );

    inserted_count := inserted_count + 1;
  end loop;

  return inserted_count;
end;
$function$;

revoke execute on function private.backfill_account_matching_keys_from_metadata()
  from public, anon, authenticated;

select private.backfill_account_matching_keys_from_metadata();

-- Preserve the original OCBC row for historical parser provenance. The active
-- v2 keeps the same deterministic constants, recognizes common masked-card
-- forms, and contributes source-specific guidance without inventing facts.
update private.source_parser_rules
set active = false
where provider = 'gmail'
  and sender_matcher = '(?i)@(?:[a-z0-9-]+\.)*ocbc\.com(?:\.sg)?(?:>|$)'
  and content_matcher = '(?is)\b(?:charged|purchase|spent|debited)\b.*\bSGD\b'
  and version = 1
  and active;

insert into private.source_parser_rules (
  provider,
  sender_matcher,
  content_matcher,
  extraction_config,
  prompt_fragment,
  version,
  priority,
  active
)
select
  'gmail',
  '(?i)@(?:[a-z0-9-]+\.)*ocbc\.com(?:\.sg)?(?:>|$)',
  '(?is)\b(?:charged|purchase|spent|debited)\b.*\bSGD\b',
  jsonb_build_object(
    'constants', jsonb_build_object(
      'transaction_kind', 'debit',
      'original_currency', 'SGD'
    ),
    'extractors', jsonb_build_object(
      'card_last_four', jsonb_build_object(
        'pattern', '(?is)(?:card|ending|ends\s+in|mastercard|master\s*card|visa|amex).{0,64}?(?:\(\s*)?(?:\*{2,}|x{2,}|•{2,})?\s*([0-9]{4})(?:$|[^0-9])',
        'group', 1
      )
    )
  ),
  'For OCBC messages, use only explicit account or card suffix, amount and currency, timestamp, and transaction direction. Leave absent facts unset.',
  2,
  100,
  true
where not exists (
  select 1
  from private.source_parser_rules rule
  where rule.provider = 'gmail'
    and rule.sender_matcher = '(?i)@(?:[a-z0-9-]+\.)*ocbc\.com(?:\.sg)?(?:>|$)'
    and rule.content_matcher = '(?is)\b(?:charged|purchase|spent|debited)\b.*\bSGD\b'
    and rule.version = 2
);

-- This low-priority global rule supplies only deterministic Account evidence.
-- Its RE2-compatible dot-all pattern tolerates HTML-to-text whitespace and
-- newlines, including FairPrice text such as `Mastercard (**** 2562)`.
insert into private.source_parser_rules (
  provider,
  sender_matcher,
  content_matcher,
  extraction_config,
  prompt_fragment,
  version,
  priority,
  active
)
select
  'gmail',
  null,
  '(?is)(?:^|[^A-Za-z0-9_])(?:mastercard|master\s*card|visa|amex|american\s+express|card)(?:$|[^A-Za-z0-9_]).{0,255}?(?:\(\s*)?(?:\*{2,}|x{2,}|•{2,})\s*([0-9]{4})(?:$|[^0-9])',
  jsonb_build_object(
    'extractors', jsonb_build_object(
      'card_last_four', jsonb_build_object(
        'pattern', '(?is)(?:^|[^A-Za-z0-9_])(?:mastercard|master\s*card|visa|amex|american\s+express|card)(?:$|[^A-Za-z0-9_]).{0,255}?(?:\(\s*)?(?:\*{2,}|x{2,}|•{2,})\s*([0-9]{4})(?:$|[^0-9])',
        'group', 1
      )
    )
  ),
  'Treat a masked card ending as Account evidence only. Do not infer issuer, owner, amount, or transaction direction from the mask.',
  1,
  50,
  true
where not exists (
  select 1
  from private.source_parser_rules rule
  where rule.provider = 'gmail'
    and rule.content_matcher = '(?is)(?:^|[^A-Za-z0-9_])(?:mastercard|master\s*card|visa|amex|american\s+express|card)(?:$|[^A-Za-z0-9_]).{0,255}?(?:\(\s*)?(?:\*{2,}|x{2,}|•{2,})\s*([0-9]{4})(?:$|[^0-9])'
    and rule.version = 1
);

alter table private.source_parse_attempts
  add column assembled_system_prompt text,
  add column normalized_input text,
  add column provider_request json,
  add column provider_response json,
  add column model_output json,
  add column prompt_components jsonb not null default '{}'::jsonb,
  add column user_parser_rule_id uuid,
  add column user_parser_rule_version integer,
  add constraint source_parse_attempts_assembled_system_prompt_length_check
    check (assembled_system_prompt is null or octet_length(assembled_system_prompt) <= 65536),
  add constraint source_parse_attempts_normalized_input_length_check
    check (normalized_input is null or octet_length(normalized_input) <= 262144),
  add constraint source_parse_attempts_provider_request_object_check
    check (provider_request is null or json_typeof(provider_request) = 'object'),
  add constraint source_parse_attempts_provider_request_length_check
    check (provider_request is null or octet_length(provider_request::text) <= 10485760),
  add constraint source_parse_attempts_provider_response_object_check
    check (provider_response is null or json_typeof(provider_response) = 'object'),
  add constraint source_parse_attempts_provider_response_length_check
    check (provider_response is null or octet_length(provider_response::text) <= 2097152),
  add constraint source_parse_attempts_model_output_object_check
    check (model_output is null or json_typeof(model_output) = 'object'),
  add constraint source_parse_attempts_model_output_length_check
    check (model_output is null or octet_length(model_output::text) <= 2097152),
  add constraint source_parse_attempts_prompt_components_object_check
    check (jsonb_typeof(prompt_components) = 'object'),
  add constraint source_parse_attempts_prompt_components_length_check
    check (octet_length(prompt_components::text) <= 65536),
  add constraint source_parse_attempts_global_rule_provenance_check
    check ((parser_rule_id is null) = (parser_rule_version is null)),
  add constraint source_parse_attempts_user_rule_version_check
    check (user_parser_rule_version is null or user_parser_rule_version > 0),
  add constraint source_parse_attempts_user_rule_provenance_check
    check ((user_parser_rule_id is null) = (user_parser_rule_version is null)),
  add constraint source_parse_attempts_user_parser_rule_fkey
    foreign key (user_id, user_parser_rule_id)
    references private.user_source_parser_rules (user_id, id)
    on delete restrict;

create index source_parse_attempts_user_parser_rule_id_idx
  on private.source_parse_attempts (user_id, user_parser_rule_id)
  where user_parser_rule_id is not null;

-- Raw-source deletion is an explicit server workflow. All dependent work and
-- audit/evidence rows disappear with it; deleting only a canonical transaction
-- removes its evidence links while retaining the raw source.
alter table private.source_parse_attempts
  drop constraint source_parse_attempts_user_data_source_fkey,
  add constraint source_parse_attempts_user_data_source_fkey
    foreign key (user_id, data_source_id)
    references private.data_sources (user_id, id)
    on delete cascade;

alter table private.transaction_jobs
  drop constraint transaction_jobs_user_data_source_fkey,
  add constraint transaction_jobs_user_data_source_fkey
    foreign key (user_id, data_source_id)
    references private.data_sources (user_id, id)
    on delete cascade;

alter table private.transaction_data_sources
  drop constraint transaction_data_sources_user_data_source_fkey,
  drop constraint transaction_data_sources_user_transaction_fkey,
  add constraint transaction_data_sources_user_data_source_fkey
    foreign key (user_id, data_source_id)
    references private.data_sources (user_id, id)
    on delete cascade,
  add constraint transaction_data_sources_user_transaction_fkey
    foreign key (user_id, transaction_id)
    references public.transactions (user_id, id)
    on delete cascade;

alter table public.transactions
  add column creation_method text not null default 'manual',
  add column user_modified_at timestamptz;

-- Preserve the safest available provenance for rows created before this
-- migration. Transfer membership takes precedence over source evidence. The
-- backfill is not a user edit, so preserve the prior updated_at values.
alter table public.transactions disable trigger transactions_set_updated_at;

update public.transactions transaction_row
set creation_method = case
  when exists (
    select 1
    from private.transaction_links transfer
    where transfer.user_id = transaction_row.user_id
      and transaction_row.id in (transfer.debit_transaction_id, transfer.credit_transaction_id)
  ) then 'internal_transfer'
  when exists (
    select 1
    from private.transaction_data_sources evidence
    where evidence.user_id = transaction_row.user_id
      and evidence.transaction_id = transaction_row.id
      and evidence.matched_by = 'automatic'
  ) then 'automatic_source'
  when exists (
    select 1
    from private.transaction_data_sources evidence
    where evidence.user_id = transaction_row.user_id
      and evidence.transaction_id = transaction_row.id
  ) then 'user_source'
  else 'manual'
end;

alter table public.transactions enable trigger transactions_set_updated_at;

alter table public.transactions
  add constraint transactions_creation_method_check
    check (creation_method in ('automatic_source', 'user_source', 'manual', 'internal_transfer')),
  add constraint transactions_user_modified_at_check
    check (user_modified_at is null or user_modified_at >= created_at);

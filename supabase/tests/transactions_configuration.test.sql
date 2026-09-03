begin;

create extension if not exists pgtap with schema extensions;
select plan(64);

select has_table(
  'private', 'account_matching_keys',
  'Account matching keys are stored outside the exposed schema'
);
select has_table(
  'private', 'user_parser_settings',
  'per-user parser defaults are stored outside the exposed schema'
);
select has_table(
  'private', 'user_source_parser_rules',
  'per-user source rules are stored outside the exposed schema'
);
select has_column(
  'private', 'source_parser_rules', 'prompt_fragment',
  'global parser rules can contribute a bounded prompt fragment'
);
select has_column(
  'private', 'user_parser_settings', 'version',
  'per-user parser defaults carry explicit prompt version provenance'
);
select has_column(
  'private', 'source_parse_attempts', 'assembled_system_prompt',
  'parse attempts retain their assembled system prompt'
);
select has_column(
  'private', 'source_parse_attempts', 'normalized_input',
  'parse attempts retain normalized model input'
);
select has_column(
  'private', 'source_parse_attempts', 'provider_request',
  'parse attempts retain exact provider request JSON'
);
select has_column(
  'private', 'source_parse_attempts', 'provider_response',
  'parse attempts retain exact provider response JSON'
);
select has_column(
  'private', 'source_parse_attempts', 'model_output',
  'parse attempts retain exact model output JSON'
);
select has_column(
  'private', 'source_parse_attempts', 'prompt_components',
  'parse attempts retain prompt component and version metadata'
);
select has_column(
  'private', 'source_parse_attempts', 'user_parser_rule_id',
  'parse attempts retain optional user-rule identity provenance'
);
select has_column(
  'private', 'source_parse_attempts', 'user_parser_rule_version',
  'parse attempts retain optional user-rule version provenance'
);

select is(
  (select count(*)
    from information_schema.columns
    where table_schema = 'private'
      and table_name = 'source_parse_attempts'
      and column_name in ('model_output', 'provider_request', 'provider_response')
      and data_type = 'json'),
  3::bigint,
  'raw provider and model JSON uses the lexical-preserving json type'
);

-- Use transaction-local fixtures so editable hosted rule rows cannot change
-- parser behavior asserted by this suite.
insert into private.source_parser_rules (
  id,
  name,
  provider,
  sender_matcher,
  content_matcher,
  extraction_config,
  prompt_fragment,
  version,
  priority,
  active
) values
  (
    'a1010101-0101-4101-8101-010101010101',
    'Test masked card evidence',
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
    'Treat a masked card ending as Account evidence only.',
    1,
    50,
    true
  ),
  (
    'a2020202-0202-4202-8202-020202020202',
    'Test OCBC legacy rule',
    'gmail',
    '(?i)@(?:[a-z0-9-]+\.)*ocbc\.com(?:\.sg)?(?:>|$)',
    '(?is)\b(?:charged|purchase|spent|debited)\b.*\bSGD\b',
    jsonb_build_object(
      'constants', jsonb_build_object(
        'transaction_kind', 'debit',
        'original_currency', 'SGD'
      )
    ),
    'Legacy OCBC fixture guidance.',
    1,
    100,
    false
  ),
  (
    'a3030303-0303-4303-8303-030303030303',
    'Test OCBC current rule',
    'gmail',
    '(?i)@(?:[a-z0-9-]+\.)*ocbc\.com(?:\.sg)?(?:>|$)',
    '(?is)\b(?:charged|purchase|spent|debited)\b.*\bSGD\b',
    jsonb_build_object(
      'constants', jsonb_build_object(
        'transaction_kind', 'debit',
        'original_currency', 'SGD'
      )
    ),
    'Current OCBC fixture guidance.',
    2,
    100,
    true
  );

select ok(
  exists (
    select 1
    from private.source_parser_rules rule
    where rule.id = 'a1010101-0101-4101-8101-010101010101'
      and rule.provider = 'gmail'
      and rule.active
      and (regexp_match(
        E'subject: Your FairPrice Group app receipt\ntext: Mastercard\n   (**** 2562)',
        rule.extraction_config #>> '{extractors,card_last_four,pattern}'
      ))[1] = '2562'
      and rule.prompt_fragment <> ''
  ),
  'the broad RE2-compatible masked-card rule captures FairPrice card ending 2562 across newlines'
);

select ok(
  exists (
    select 1
    from private.source_parser_rules
    where id = 'a2020202-0202-4202-8202-020202020202'
      and provider = 'gmail'
      and not active
      and sender_matcher = '(?i)@(?:[a-z0-9-]+\.)*ocbc\.com(?:\.sg)?(?:>|$)'
      and version = 1
  )
  and exists (
    select 1
    from private.source_parser_rules
    where id = 'a3030303-0303-4303-8303-030303030303'
      and provider = 'gmail'
      and active
      and sender_matcher = '(?i)@(?:[a-z0-9-]+\.)*ocbc\.com(?:\.sg)?(?:>|$)'
      and version = 2
      and prompt_fragment <> ''
      and extraction_config @> '{"constants":{"transaction_kind":"debit","original_currency":"SGD"}}'::jsonb
  )
  and (
    select count(*)
    from private.source_parser_rules
    where id in (
        'a2020202-0202-4202-8202-020202020202',
        'a3030303-0303-4303-8303-030303030303'
      )
      and provider = 'gmail'
      and active
      and priority = 100
      and sender_matcher = '(?i)@(?:[a-z0-9-]+\.)*ocbc\.com(?:\.sg)?(?:>|$)'
  ) = 1,
  'the OCBC fixtures preserve v1 provenance while exactly one prompt-bearing v2 rule is active'
);

select results_eq(
  $$select count(*)
    from pg_class relation
    join pg_namespace namespace on namespace.oid = relation.relnamespace
    where namespace.nspname = 'private'
      and relation.relname in ('account_matching_keys', 'user_parser_settings', 'user_source_parser_rules')
      and relation.relrowsecurity$$,
  array[3::bigint],
  'all new private configuration tables have RLS enabled as defense in depth'
);

select results_eq(
  $$select count(*)
    from (values
      ('private.account_matching_keys'),
      ('private.user_parser_settings'),
      ('private.user_source_parser_rules')
    ) as relation(name)
    where has_table_privilege('authenticated', relation.name, 'SELECT')
      or has_table_privilege('authenticated', relation.name, 'INSERT')
      or has_table_privilege('authenticated', relation.name, 'UPDATE')
      or has_table_privilege('authenticated', relation.name, 'DELETE')$$,
  array[0::bigint],
  'authenticated browser roles have no privileges on private configuration tables'
);

select ok(
  not has_function_privilege(
    'authenticated',
    'private.backfill_account_matching_keys_from_metadata()',
    'EXECUTE'
  ),
  'the migration-only legacy metadata backfill is not browser-executable'
);

insert into auth.users (id, email) values
  ('11111111-1111-1111-1111-111111111111', 'configuration-owner@example.com'),
  ('22222222-2222-2222-2222-222222222222', 'configuration-other@example.com');

insert into public.accounts (
  id, user_id, side, account_type, name, institution_name, metadata
) values
  (
    'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
    '11111111-1111-1111-1111-111111111111',
    'liability', 'credit_card', 'Legacy card', 'FairPrice Bank',
    '{"Last 4 Digit":"**** 2562","untouched":{"nested":true}}'
  ),
  (
    'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb',
    '11111111-1111-1111-1111-111111111111',
    'asset', 'bank_account', 'Legacy bank', 'Example Bank',
    '{"Bank Account Suffix":" AB / C:-• 9 "}'
  ),
  (
    'cccccccc-cccc-cccc-cccc-cccccccccccc',
    '11111111-1111-1111-1111-111111111111',
    'asset', 'bank_account', 'Spare account', 'Example Bank', '{}'
  ),
  (
    'dddddddd-dddd-dddd-dddd-dddddddddddd',
    '22222222-2222-2222-2222-222222222222',
    'asset', 'bank_account', 'Other owner account', 'Example Bank', '{}'
  ),
  (
    'eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee',
    '11111111-1111-1111-1111-111111111111',
    'liability', 'credit_card', 'Invalid legacy card', 'Example Bank',
    '{"Last 4 Digit":"12345"}'
  );

select is(
  private.backfill_account_matching_keys_from_metadata(),
  2,
  'recognized legacy card and bank metadata are backfilled once'
);

select results_eq(
  $$select display_value, normalized_value
    from private.account_matching_keys
    where account_id = 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa'$$,
  $$values ('**** 2562'::text, '2562'::text)$$,
  'Last 4 Digit metadata becomes a card_last_four matching key'
);

select results_eq(
  $$select key_type, normalized_value
    from private.account_matching_keys
    where account_id = 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb'$$,
  $$values ('bank_account_suffix'::text, 'ab/c:9'::text)$$,
  'bank suffix normalization lowercases and removes only whitespace and masking separators'
);

select results_eq(
  $$select metadata
    from public.accounts
    where id = 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa'$$,
  array['{"Last 4 Digit":"**** 2562","untouched":{"nested":true}}'::jsonb],
  'legacy Account metadata remains unchanged by backfill'
);

select is(
  private.backfill_account_matching_keys_from_metadata(),
  0,
  'the legacy metadata backfill is idempotent'
);

select is_empty(
  $$select 1
    from private.account_matching_keys
    where account_id = 'eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee'$$,
  'legacy card values that normalize to more than four digits are rejected from backfill'
);

select throws_ok(
  $$insert into private.account_matching_keys (
      user_id, account_id, key_type, display_value, normalized_value
    ) values (
      '11111111-1111-1111-1111-111111111111',
      'cccccccc-cccc-cccc-cccc-cccccccccccc',
      'card_last_four', '12345', '12345'
    )$$,
  '23514',
  null,
  'card matching keys must normalize to exactly four ASCII digits'
);

select lives_ok(
  $$insert into private.account_matching_keys (
      user_id, account_id, key_type, display_value, normalized_value
    ) values (
      '11111111-1111-1111-1111-111111111111',
      'cccccccc-cccc-cccc-cccc-cccccccccccc',
      'bank_account_suffix', 'ABC / 42', 'abc/42'
    )$$,
  'bank suffix matching keys retain non-masking punctuation'
);

select throws_ok(
  $$insert into private.account_matching_keys (
      user_id, account_id, key_type, display_value, normalized_value
    ) values (
      '11111111-1111-1111-1111-111111111111',
      'cccccccc-cccc-cccc-cccc-cccccccccccc',
      'bank_account_suffix', 'ABC42', 'ABC42'
    )$$,
  '23514',
  null,
  'bank suffix normalized values must be lowercase'
);

select throws_ok(
  $$insert into private.account_matching_keys (
      user_id, account_id, key_type, display_value, normalized_value
    ) values (
      '11111111-1111-1111-1111-111111111111',
      'cccccccc-cccc-cccc-cccc-cccccccccccc',
      'bank_account_suffix', 'ab-42', 'ab-42'
    )$$,
  '23514',
  null,
  'bank suffix normalized values cannot retain confirmed masking separators'
);

select throws_ok(
  $$insert into private.account_matching_keys (
      user_id, account_id, key_type, display_value, normalized_value
    ) values (
      '11111111-1111-1111-1111-111111111111',
      'dddddddd-dddd-dddd-dddd-dddddddddddd',
      'card_last_four', '9999', '9999'
    )$$,
  '23503',
  null,
  'matching-key ownership must agree with Account ownership'
);

select lives_ok(
  $$update private.account_matching_keys
    set active = false, retired_at = now()
    where user_id = '11111111-1111-1111-1111-111111111111'
      and key_type = 'card_last_four'
      and normalized_value = '2562'$$,
  'an active matching key can be retired'
);

select throws_ok(
  $$insert into private.account_matching_keys (
      user_id, account_id, key_type, display_value, normalized_value
    ) values (
      '11111111-1111-1111-1111-111111111111',
      'cccccccc-cccc-cccc-cccc-cccccccccccc',
      'card_last_four', '2562', '2562'
    )$$,
  '23505',
  null,
  'permanent uniqueness prevents reassigning even a retired matching identity'
);

select lives_ok(
  $$update private.account_matching_keys
    set active = true, retired_at = null
    where user_id = '11111111-1111-1111-1111-111111111111'
      and key_type = 'card_last_four'
      and normalized_value = '2562'$$,
  'a retired matching key can be reactivated on the same row and Account'
);

select throws_ok(
  $$update private.account_matching_keys
    set display_value = '9999', normalized_value = '9999'
    where user_id = '11111111-1111-1111-1111-111111111111'
      and key_type = 'card_last_four'
      and normalized_value = '2562'$$,
  '23514',
  'account matching key identity and value fields are immutable',
  'matching-key value changes must use retire plus insert'
);

select throws_ok(
  $$insert into private.account_matching_keys (
      user_id, account_id, key_type, display_value, normalized_value, active
    ) values (
      '11111111-1111-1111-1111-111111111111',
      'cccccccc-cccc-cccc-cccc-cccccccccccc',
      'card_last_four', '1111', '1111', false
    )$$,
  '23514',
  null,
  'inactive matching keys require a retirement timestamp'
);

insert into public.accounts (
  id, user_id, side, account_type, name, institution_name
) values (
  'abababab-abab-abab-abab-abababababab',
  '11111111-1111-1111-1111-111111111111',
  'liability', 'credit_card', 'Temporary card', 'Example Bank'
);

insert into private.account_matching_keys (
  user_id, account_id, key_type, display_value, normalized_value
) values (
  '11111111-1111-1111-1111-111111111111',
  'abababab-abab-abab-abab-abababababab',
  'card_last_four', '7777', '7777'
);

delete from public.accounts where id = 'abababab-abab-abab-abab-abababababab';

select is_empty(
  $$select 1 from private.account_matching_keys
    where account_id = 'abababab-abab-abab-abab-abababababab'$$,
  'hard Account deletion cascades to its matching keys'
);

select lives_ok(
  $$insert into private.user_parser_settings (user_id, default_instructions)
    values ('11111111-1111-1111-1111-111111111111', 'Prefer the receipt total.')$$,
  'one bounded parser-default row can be stored for a user'
);

select throws_ok(
  $$insert into private.user_parser_settings (user_id)
    values ('11111111-1111-1111-1111-111111111111')$$,
  '23505',
  null,
  'parser settings allow only one row per user'
);

select throws_ok(
  $$update private.user_parser_settings
    set default_instructions = repeat('x', 4001)
    where user_id = '11111111-1111-1111-1111-111111111111'$$,
  '23514',
  null,
  'default parser instructions are limited to 4000 characters'
);

select throws_ok(
  $$update private.user_parser_settings
    set version = 0
    where user_id = '11111111-1111-1111-1111-111111111111'$$,
  '23514',
  null,
  'default parser instruction versions must remain positive'
);

select lives_ok(
  $$insert into private.user_source_parser_rules (
      id, user_id, name, sender_match_type, sender_match_value,
      subject_matcher, content_matcher, prompt_fragment, priority
    ) values (
      '31313131-3131-3131-3131-313131313131',
      '11111111-1111-1111-1111-111111111111',
      'FairPrice receipts', 'domain', 'fairprice.com.sg',
      '(?i)fairprice group app receipt', '(?is)mastercard.*\\*{4}',
      'Prefer the final charged total.', 200
    )$$,
  'a valid owned Gmail parser rule can be stored'
);

select throws_ok(
  $$insert into private.user_source_parser_rules (
      user_id, name, sender_match_type, sender_match_value
    ) values (
      '11111111-1111-1111-1111-111111111111',
      'Bad sender type', 'contains', 'fairprice'
    )$$,
  '23514',
  null,
  'sender match type is restricted to exact, domain, or regex'
);

select throws_ok(
  $$insert into private.user_source_parser_rules (
      user_id, name, sender_match_type, sender_match_value, prompt_fragment
    ) values (
      '11111111-1111-1111-1111-111111111111',
      'Oversized prompt', 'exact', 'sender@example.com', repeat('x', 4001)
    )$$,
  '23514',
  null,
  'user-rule prompt fragments are limited to 4000 characters'
);

select throws_ok(
  $$insert into private.user_source_parser_rules (
      user_id, name, sender_match_type, sender_match_value, subject_matcher
    ) values (
      '11111111-1111-1111-1111-111111111111',
      'Blank subject regex', 'exact', 'sender@example.com', '   '
    )$$,
  '23514',
  null,
  'present RE2 matchers cannot be blank'
);

insert into private.user_source_parser_rules (
  id, user_id, name, sender_match_type, sender_match_value
) values (
  '32323232-3232-3232-3232-323232323232',
  '22222222-2222-2222-2222-222222222222',
  'Other owner rule', 'exact', 'other@example.com'
);

insert into private.data_sources (
  id, user_id, source_type, provider, provider_message_id, received_at, raw_data
) values (
  '41414141-4141-4141-4141-414141414141',
  '11111111-1111-1111-1111-111111111111',
  'gmail_email', 'gmail', 'configuration-message-1', now(), '{}'
);

insert into public.transactions (
  id, user_id, account_id, transaction_kind, title,
  original_amount_minor, original_currency, occurred_at, creation_method
) values (
  '51515151-5151-5151-5151-515151515151',
  '11111111-1111-1111-1111-111111111111',
  'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
  'debit', 'FairPrice', 2562, 'SGD', now(), 'automatic_source'
);

insert into private.transaction_data_sources (
  user_id, transaction_id, data_source_id, role, matched_by
) values (
  '11111111-1111-1111-1111-111111111111',
  '51515151-5151-5151-5151-515151515151',
  '41414141-4141-4141-4141-414141414141',
  'merchant_receipt', 'automatic'
);

insert into private.transaction_jobs (
  user_id, data_source_id, job_type
) values (
  '11111111-1111-1111-1111-111111111111',
  '41414141-4141-4141-4141-414141414141',
  'source_parsing'
);

select lives_ok(
  $$insert into private.source_parse_attempts (
      id, user_id, data_source_id, parser_rule_id, parser_rule_version,
      model_name, request_metadata, parsed_candidate, validation_status,
      assembled_system_prompt, normalized_input,
      provider_request, provider_response, model_output, prompt_components,
      user_parser_rule_id, user_parser_rule_version
    ) values (
      '61616161-6161-6161-6161-616161616161',
      '11111111-1111-1111-1111-111111111111',
      '41414141-4141-4141-4141-414141414141',
      'a1010101-0101-4101-8101-010101010101',
      1,
      'qwen3.8-flash', '{"provider":"alibaba"}', '{}', 'valid',
      'Base prompt plus user configuration',
      E'subject: receipt\ntext: Mastercard (**** 2562)',
      '{ "model": "qwen3.8-flash", "messages": [] }'::json,
      '{ "choices": [] }'::json,
      '{ "candidate": {}, "evidence": [] }'::json,
      '{"base_prompt_version":1,"global_rule_version":1,"user_rule_version":1}',
      '31313131-3131-3131-3131-313131313131', 1
    )$$,
  'a complete exact parser audit record can be stored'
);

select is(
  (select provider_request::text
    from private.source_parse_attempts
    where id = '61616161-6161-6161-6161-616161616161'),
  '{ "model": "qwen3.8-flash", "messages": [] }',
  'raw json preserves the exact provider request representation'
);

select throws_ok(
  $$insert into private.source_parse_attempts (
      user_id, data_source_id, provider_request
    ) values (
      '11111111-1111-1111-1111-111111111111',
      '41414141-4141-4141-4141-414141414141', '[]'
    )$$,
  '23514',
  null,
  'provider request audit JSON must be an object'
);

select throws_ok(
  $$insert into private.source_parse_attempts (
      user_id, data_source_id, prompt_components
    ) values (
      '11111111-1111-1111-1111-111111111111',
      '41414141-4141-4141-4141-414141414141', '[]'
    )$$,
  '23514',
  null,
  'prompt component metadata must be a JSON object'
);

select throws_ok(
  $$insert into private.source_parse_attempts (
      user_id, data_source_id, user_parser_rule_version
    ) values (
      '11111111-1111-1111-1111-111111111111',
      '41414141-4141-4141-4141-414141414141', 1
    )$$,
  '23514',
  null,
  'user parser rule ID and version provenance must appear together'
);

select throws_ok(
  $$insert into private.source_parse_attempts (
      user_id, data_source_id, user_parser_rule_id, user_parser_rule_version
    ) values (
      '11111111-1111-1111-1111-111111111111',
      '41414141-4141-4141-4141-414141414141',
      '32323232-3232-3232-3232-323232323232', 1
    )$$,
  '23503',
  null,
  'parse-attempt user-rule provenance cannot cross owners'
);

select throws_ok(
  $$insert into private.source_parse_attempts (
      user_id, data_source_id, normalized_input
    ) values (
      '11111111-1111-1111-1111-111111111111',
      '41414141-4141-4141-4141-414141414141', repeat('x', 262145)
    )$$,
  '23514',
  null,
  'normalized parser input is bounded to 256 KiB'
);

select throws_ok(
  $$insert into public.transactions (
      user_id, account_id, transaction_kind, title,
      original_amount_minor, original_currency, occurred_at, creation_method
    ) values (
      '11111111-1111-1111-1111-111111111111',
      'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
      'debit', 'Bad provenance', 1, 'SGD', now(), 'import'
    )$$,
  '23514',
  null,
  'canonical transaction creation method uses the four supported provenance values'
);

select throws_ok(
  $$update public.transactions
    set user_modified_at = created_at - interval '1 second'
    where id = '51515151-5151-5151-5151-515151515151'$$,
  '23514',
  null,
  'user modification time cannot predate transaction creation'
);

insert into public.transactions (
  id, user_id, account_id, transaction_kind, title,
  original_amount_minor, original_currency, occurred_at
) values (
  '71717171-7171-7171-7171-717171717171',
  '11111111-1111-1111-1111-111111111111',
  'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb',
  'credit', 'Manual default', 100, 'SGD', now()
);

select results_eq(
  $$select creation_method
    from public.transactions
    where id = '71717171-7171-7171-7171-717171717171'$$,
  array['manual'::text],
  'new canonical transactions default conservatively to manual provenance'
);

select lives_ok(
  $$update public.transactions
    set user_modified_at = now()
    where id = '51515151-5151-5151-5151-515151515151'$$,
  'valid user modification provenance can be recorded'
);

select lives_ok(
  $$delete from private.data_sources
    where id = '41414141-4141-4141-4141-414141414141'$$,
  'a raw source can be explicitly deleted with all dependents'
);

select results_eq(
  $$select
      (select count(*) from private.source_parse_attempts
        where data_source_id = '41414141-4141-4141-4141-414141414141'),
      (select count(*) from private.transaction_jobs
        where data_source_id = '41414141-4141-4141-4141-414141414141'),
      (select count(*) from private.transaction_data_sources
        where data_source_id = '41414141-4141-4141-4141-414141414141')$$,
  $$values (0::bigint, 0::bigint, 0::bigint)$$,
  'raw-source deletion leaves no parse attempts, jobs, or evidence links'
);

insert into private.data_sources (
  id, user_id, source_type, provider, provider_message_id, received_at, raw_data
) values (
  '81818181-8181-8181-8181-818181818181',
  '11111111-1111-1111-1111-111111111111',
  'gmail_email', 'gmail', 'configuration-message-2', now(), '{}'
);

insert into private.transaction_data_sources (
  user_id, transaction_id, data_source_id, role, matched_by
) values (
  '11111111-1111-1111-1111-111111111111',
  '71717171-7171-7171-7171-717171717171',
  '81818181-8181-8181-8181-818181818181',
  'other', 'user'
);

select lives_ok(
  $$delete from public.transactions
    where id = '71717171-7171-7171-7171-717171717171'$$,
  'an ordinary canonical transaction can be deleted without first deleting its evidence link'
);

select results_eq(
  $$select
      (select count(*) from private.transaction_data_sources
        where transaction_id = '71717171-7171-7171-7171-717171717171'),
      (select count(*) from private.data_sources
        where id = '81818181-8181-8181-8181-818181818181')$$,
  $$values (0::bigint, 1::bigint)$$,
  'transaction deletion removes only the evidence link and retains the raw source'
);

select results_eq(
  $$select count(*)
    from pg_constraint
    where conname in (
      'source_parse_attempts_user_data_source_fkey',
      'transaction_jobs_user_data_source_fkey',
      'transaction_data_sources_user_data_source_fkey',
      'transaction_data_sources_user_transaction_fkey'
    )
      and confdeltype = 'c'$$,
  array[4::bigint],
  'all four raw-source/evidence foreign keys use ON DELETE CASCADE'
);

select results_eq(
  $$select count(*)
    from pg_indexes
    where schemaname = 'private'
      and indexname in (
        'account_matching_keys_user_account_id_idx',
        'user_source_parser_rules_user_active_priority_idx',
        'source_parse_attempts_user_parser_rule_id_idx'
      )$$,
  array[3::bigint],
  'matching, active-rule, and audit-provenance indexes exist'
);

set local role authenticated;
set local request.jwt.claim.sub = '11111111-1111-1111-1111-111111111111';

select throws_ok(
  $$select * from private.account_matching_keys$$,
  '42501',
  null,
  'authenticated users cannot access matching keys directly'
);

select throws_ok(
  $$select * from private.user_parser_settings$$,
  '42501',
  null,
  'authenticated users cannot access parser defaults directly'
);

select throws_ok(
  $$select * from private.user_source_parser_rules$$,
  '42501',
  null,
  'authenticated users cannot access user parser rules directly'
);

select * from finish();
rollback;

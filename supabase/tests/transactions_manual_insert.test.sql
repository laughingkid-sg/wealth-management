begin;

create extension if not exists pgtap with schema extensions;
select plan(39);

insert into auth.users (id, email) values
  ('11111111-1111-1111-1111-111111111111', 'manual-owner@example.com'),
  ('22222222-2222-2222-2222-222222222222', 'manual-other@example.com');

insert into public.accounts (
  id, user_id, side, account_type, name, institution_name, deleted_at
) values
  (
    'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
    '11111111-1111-1111-1111-111111111111',
    'asset', 'bank_account', 'Active account', 'DBS', null
  ),
  (
    'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb',
    '11111111-1111-1111-1111-111111111111',
    'asset', 'bank_account', 'Deleted account', 'DBS', now()
  ),
  (
    'cccccccc-cccc-cccc-cccc-cccccccccccc',
    '22222222-2222-2222-2222-222222222222',
    'asset', 'bank_account', 'Other owner account', 'UOB', null
  );

insert into public.transaction_categories (
  id, parent_name, name, emoji, sort_order, active
) values
  (
    'dddddddd-dddd-dddd-dddd-dddddddddddd',
    'Test', 'Active manual category', 'A', 1000, true
  ),
  (
    'eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee',
    'Test', 'Inactive manual category', 'I', 1010, false
  ),
  (
    'ffffffff-ffff-ffff-ffff-ffffffffffff',
    'Test', 'Deleted manual category', 'D', 1020, true
  );

delete from public.transaction_categories
where id = 'ffffffff-ffff-ffff-ffff-ffffffffffff';

select is(
  (
    select jsonb_agg(
      column_name::text
      order by column_name::text collate "C"
    )
    from information_schema.column_privileges
    where table_schema = 'public'
      and table_name = 'transactions'
      and grantee = 'authenticated'
      and privilege_type = 'INSERT'
  ),
  '[
    "account_id",
    "category_id",
    "details",
    "line_items",
    "merchant_name",
    "occurred_at",
    "original_amount_minor",
    "original_currency",
    "review_status",
    "sgd_amount_minor",
    "title",
    "transaction_kind",
    "user_id"
  ]'::jsonb,
  'authenticated receives only the manual-create INSERT columns'
);

set local role authenticated;
set local request.jwt.claim.sub = '11111111-1111-1111-1111-111111111111';

select results_eq(
  $$insert into public.transactions (
      user_id,
      account_id,
      transaction_kind,
      title,
      merchant_name,
      original_amount_minor,
      original_currency,
      sgd_amount_minor,
      occurred_at,
      category_id,
      line_items,
      details,
      review_status
    ) values (
      '11111111-1111-1111-1111-111111111111',
      'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
      'debit',
      'Browser manual transaction',
      'Coffee shop',
      450,
      'SGD',
      450,
      now(),
      'dddddddd-dddd-dddd-dddd-dddddddddddd',
      '[{
        "schema_version": 1,
        "description": "Coffee",
        "quantity": 1,
        "unit_price_minor": 450,
        "line_total_minor": "450",
        "tax_minor": null,
        "discount_minor": 0,
        "currency": "SGD",
        "details": {}
      }]'::jsonb,
      '{"user_notes":"Entered from the browser"}'::jsonb,
      'confirmed'
    )
    returning creation_method, review_status,
      match_confidence is null, user_modified_at is null$$,
  $$values ('manual'::text, 'confirmed'::text, true, true)$$,
  'an owner can insert a confirmed manual transaction with safe v1 JSON'
);

select throws_ok(
  $$insert into public.transactions (
      user_id, account_id, transaction_kind, title,
      original_amount_minor, original_currency, occurred_at, review_status
    ) values (
      '22222222-2222-2222-2222-222222222222',
      'cccccccc-cccc-cccc-cccc-cccccccccccc',
      'debit', 'Other owner row', 1, 'SGD', now(), 'confirmed'
    )$$,
  '42501',
  null,
  'an authenticated user cannot insert another owner''s transaction'
);

select throws_ok(
  $$insert into public.transactions (
      user_id, account_id, transaction_kind, title,
      original_amount_minor, original_currency, occurred_at, review_status
    ) values (
      '11111111-1111-1111-1111-111111111111',
      'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
      'debit', 'Pending browser row', 1, 'SGD', now(), 'pending'
    )$$,
  '42501',
  null,
  'browser-created manual transactions must be explicitly confirmed'
);

select throws_ok(
  $$insert into public.transactions (
      user_id, account_id, transaction_kind, title,
      original_amount_minor, original_currency, occurred_at, review_status
    ) values (
      '11111111-1111-1111-1111-111111111111',
      'cccccccc-cccc-cccc-cccc-cccccccccccc',
      'debit', 'Cross-owner account', 1, 'SGD', now(), 'confirmed'
    )$$,
  '23514',
  'transaction account must be active and owned by the transaction user',
  'a manual transaction cannot use a cross-owner account'
);

select throws_ok(
  $$insert into public.transactions (
      user_id, account_id, transaction_kind, title,
      original_amount_minor, original_currency, occurred_at, review_status
    ) values (
      '11111111-1111-1111-1111-111111111111',
      'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb',
      'debit', 'Deleted account', 1, 'SGD', now(), 'confirmed'
    )$$,
  '23514',
  'transaction account must be active and owned by the transaction user',
  'a manual transaction cannot use a deleted account'
);

select throws_ok(
  $$insert into public.transactions (
      user_id, account_id, transaction_kind, title,
      original_amount_minor, original_currency, occurred_at,
      category_id, review_status
    ) values (
      '11111111-1111-1111-1111-111111111111',
      'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
      'debit', 'Inactive category', 1, 'SGD', now(),
      'eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee', 'confirmed'
    )$$,
  '23514',
  'transaction category must be active',
  'a manual transaction cannot use an inactive category'
);

select throws_ok(
  $$insert into public.transactions (
      user_id, account_id, transaction_kind, title,
      original_amount_minor, original_currency, occurred_at,
      category_id, review_status
    ) values (
      '11111111-1111-1111-1111-111111111111',
      'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
      'debit', 'Deleted category', 1, 'SGD', now(),
      'ffffffff-ffff-ffff-ffff-ffffffffffff', 'confirmed'
    )$$,
  '23514',
  'transaction category must be active',
  'a manual transaction cannot use a deleted category'
);

select throws_ok(
  $$insert into public.transactions (
      user_id, account_id, transaction_kind, title,
      original_amount_minor, original_currency, occurred_at,
      line_items, review_status
    ) values (
      '11111111-1111-1111-1111-111111111111',
      'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
      'debit', 'Non-array lines', 1, 'SGD', now(), '{}', 'confirmed'
    )$$,
  '23514',
  null,
  'line items must be an array'
);

select throws_ok(
  $$insert into public.transactions (
      user_id, account_id, transaction_kind, title,
      original_amount_minor, original_currency, occurred_at,
      line_items, review_status
    ) values (
      '11111111-1111-1111-1111-111111111111',
      'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
      'debit', 'Too many lines', 1, 'SGD', now(),
      (
        select jsonb_agg(jsonb_build_object(
          'schema_version', 1,
          'description', 'Item',
          'quantity', 1,
          'currency', 'SGD',
          'details', jsonb_build_object()
        ))
        from generate_series(1, 101)
      ),
      'confirmed'
    )$$,
  '23514',
  null,
  'line items are capped at one hundred entries'
);

select throws_ok(
  $$insert into public.transactions (
      user_id, account_id, transaction_kind, title,
      original_amount_minor, original_currency, occurred_at,
      line_items, review_status
    ) values (
      '11111111-1111-1111-1111-111111111111',
      'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
      'debit', 'Oversized line metadata', 1, 'SGD', now(),
      jsonb_build_array(jsonb_build_object(
        'schema_version', 1,
        'description', 'Item',
        'quantity', 1,
        'currency', 'SGD',
        'details', jsonb_build_object('blob', repeat('x', 262144))
      )),
      'confirmed'
    )$$,
  '23514',
  null,
  'serialized line items are capped at 256 KiB'
);

select throws_ok(
  $$insert into public.transactions (
      user_id, account_id, transaction_kind, title,
      original_amount_minor, original_currency, occurred_at,
      line_items, review_status
    ) values (
      '11111111-1111-1111-1111-111111111111',
      'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
      'debit', 'Scalar line', 1, 'SGD', now(), '[1]', 'confirmed'
    )$$,
  '23514',
  null,
  'each line item must be an object'
);

select throws_ok(
  $$insert into public.transactions (
      user_id, account_id, transaction_kind, title,
      original_amount_minor, original_currency, occurred_at,
      line_items, review_status
    ) values (
      '11111111-1111-1111-1111-111111111111',
      'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
      'debit', 'Unknown line key', 1, 'SGD', now(),
      '[{"schema_version":1,"description":"Item","quantity":1,"currency":"SGD","details":{},"unexpected":true}]',
      'confirmed'
    )$$,
  '23514',
  null,
  'line items reject unknown keys'
);

select throws_ok(
  $$insert into public.transactions (
      user_id, account_id, transaction_kind, title,
      original_amount_minor, original_currency, occurred_at,
      line_items, review_status
    ) values (
      '11111111-1111-1111-1111-111111111111',
      'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
      'debit', 'Wrong line version', 1, 'SGD', now(),
      '[{"schema_version":2,"description":"Item","quantity":1,"currency":"SGD","details":{}}]',
      'confirmed'
    )$$,
  '23514',
  null,
  'line items require schema version one'
);

select throws_ok(
  $$insert into public.transactions (
      user_id, account_id, transaction_kind, title,
      original_amount_minor, original_currency, occurred_at,
      line_items, review_status
    ) values (
      '11111111-1111-1111-1111-111111111111',
      'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
      'debit', 'Blank line description', 1, 'SGD', now(),
      '[{"schema_version":1,"description":"   ","quantity":1,"currency":"SGD","details":{}}]',
      'confirmed'
    )$$,
  '23514',
  null,
  'line-item descriptions must be nonblank'
);

select throws_ok(
  $$insert into public.transactions (
      user_id, account_id, transaction_kind, title,
      original_amount_minor, original_currency, occurred_at,
      line_items, review_status
    ) values (
      '11111111-1111-1111-1111-111111111111',
      'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
      'debit', 'Whitespace line description', 1, 'SGD', now(),
      jsonb_build_array(jsonb_build_object(
        'schema_version', 1,
        'description', E'\t\n\r',
        'quantity', 1,
        'currency', 'SGD',
        'details', jsonb_build_object()
      )),
      'confirmed'
    )$$,
  '23514',
  null,
  'line-item descriptions containing only non-space whitespace are rejected'
);

select throws_ok(
  $$insert into public.transactions (
      user_id, account_id, transaction_kind, title,
      original_amount_minor, original_currency, occurred_at,
      line_items, review_status
    ) values (
      '11111111-1111-1111-1111-111111111111',
      'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
      'debit', 'Long line description', 1, 'SGD', now(),
      jsonb_build_array(jsonb_build_object(
        'schema_version', 1,
        'description', repeat('x', 251),
        'quantity', 1,
        'currency', 'SGD',
        'details', jsonb_build_object()
      )),
      'confirmed'
    )$$,
  '23514',
  null,
  'line-item descriptions are capped at 250 characters'
);

select throws_ok(
  $$insert into public.transactions (
      user_id, account_id, transaction_kind, title,
      original_amount_minor, original_currency, occurred_at,
      line_items, review_status
    ) values (
      '11111111-1111-1111-1111-111111111111',
      'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
      'debit', 'Zero quantity', 1, 'SGD', now(),
      '[{"schema_version":1,"description":"Item","quantity":0,"currency":"SGD","details":{}}]',
      'confirmed'
    )$$,
  '23514',
  null,
  'line-item quantity must be positive'
);

select throws_ok(
  $$insert into public.transactions (
      user_id, account_id, transaction_kind, title,
      original_amount_minor, original_currency, occurred_at,
      line_items, review_status
    ) values (
      '11111111-1111-1111-1111-111111111111',
      'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
      'debit', 'Fractional quantity', 1, 'SGD', now(),
      '[{"schema_version":1,"description":"Item","quantity":1.5,"currency":"SGD","details":{}}]',
      'confirmed'
    )$$,
  '23514',
  null,
  'line-item quantity must be an integer'
);

select throws_ok(
  $$insert into public.transactions (
      user_id, account_id, transaction_kind, title,
      original_amount_minor, original_currency, occurred_at,
      line_items, review_status
    ) values (
      '11111111-1111-1111-1111-111111111111',
      'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
      'debit', 'Bad line currency', 1, 'SGD', now(),
      '[{"schema_version":1,"description":"Item","quantity":1,"currency":"sgd","details":{}}]',
      'confirmed'
    )$$,
  '23514',
  null,
  'line-item currency must be three uppercase letters'
);

select throws_ok(
  $$insert into public.transactions (
      user_id, account_id, transaction_kind, title,
      original_amount_minor, original_currency, occurred_at,
      line_items, review_status
    ) values (
      '11111111-1111-1111-1111-111111111111',
      'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
      'debit', 'Negative line amount', 1, 'SGD', now(),
      '[{"schema_version":1,"description":"Item","quantity":1,"unit_price_minor":-1,"currency":"SGD","details":{}}]',
      'confirmed'
    )$$,
  '23514',
  null,
  'line-item amounts cannot be negative'
);

select throws_ok(
  $$insert into public.transactions (
      user_id, account_id, transaction_kind, title,
      original_amount_minor, original_currency, occurred_at,
      line_items, review_status
    ) values (
      '11111111-1111-1111-1111-111111111111',
      'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
      'debit', 'Fractional line amount', 1, 'SGD', now(),
      '[{"schema_version":1,"description":"Item","quantity":1,"unit_price_minor":1.5,"currency":"SGD","details":{}}]',
      'confirmed'
    )$$,
  '23514',
  null,
  'line-item amounts must be integers'
);

select throws_ok(
  $$insert into public.transactions (
      user_id, account_id, transaction_kind, title,
      original_amount_minor, original_currency, occurred_at,
      line_items, review_status
    ) values (
      '11111111-1111-1111-1111-111111111111',
      'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
      'debit', 'Oversized line amount', 1, 'SGD', now(),
      '[{"schema_version":1,"description":"Item","quantity":1,"unit_price_minor":"9223372036854775808","currency":"SGD","details":{}}]',
      'confirmed'
    )$$,
  '23514',
  null,
  'line-item amounts must fit in a signed bigint'
);

select throws_ok(
  $$insert into public.transactions (
      user_id, account_id, transaction_kind, title,
      original_amount_minor, original_currency, occurred_at,
      line_items, review_status
    ) values (
      '11111111-1111-1111-1111-111111111111',
      'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
      'debit', 'Bad line details', 1, 'SGD', now(),
      '[{"schema_version":1,"description":"Item","quantity":1,"currency":"SGD","details":[]}]',
      'confirmed'
    )$$,
  '23514',
  null,
  'line-item details must be an object'
);

-- The browser policy additionally restricts top-level details to user_notes.
-- Exercise the general server-write constraints as the migration owner so the
-- RLS allowlist cannot mask their failures.
reset role;

select is_empty(
  $$select 1
    from private.transaction_data_sources evidence
    join public.transactions transaction_row
      on transaction_row.user_id = evidence.user_id
      and transaction_row.id = evidence.transaction_id
    where transaction_row.user_id = '11111111-1111-1111-1111-111111111111'
      and transaction_row.title = 'Browser manual transaction'$$,
  'a direct browser-created manual transaction has no source evidence link'
);

select throws_ok(
  $$insert into public.transactions (
      user_id, account_id, transaction_kind, title,
      original_amount_minor, original_currency, occurred_at,
      details, review_status
    ) values (
      '11111111-1111-1111-1111-111111111111',
      'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
      'debit', 'Bad transaction details', 1, 'SGD', now(), '[]', 'confirmed'
    )$$,
  '23514',
  null,
  'transaction details must be an object'
);

select throws_ok(
  $$insert into public.transactions (
      user_id, account_id, transaction_kind, title,
      original_amount_minor, original_currency, occurred_at,
      details, review_status
    ) values (
      '11111111-1111-1111-1111-111111111111',
      'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
      'debit', 'Oversized transaction details', 1, 'SGD', now(),
      jsonb_build_object('blob', repeat('x', 16385)), 'confirmed'
    )$$,
  '23514',
  null,
  'transaction details are capped at 16 KiB'
);

set local role authenticated;
set local request.jwt.claim.sub = '11111111-1111-1111-1111-111111111111';

select throws_ok(
  $$insert into public.transactions (
      user_id, account_id, transaction_kind, title,
      original_amount_minor, original_currency, occurred_at,
      details, review_status
    ) values (
      '11111111-1111-1111-1111-111111111111',
      'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
      'debit', 'Non-string note', 1, 'SGD', now(),
      '{"user_notes":42}', 'confirmed'
    )$$,
  '23514',
  null,
  'transaction user notes must be a string'
);

select throws_ok(
  $$insert into public.transactions (
      user_id, account_id, transaction_kind, title,
      original_amount_minor, original_currency, occurred_at,
      details, review_status
    ) values (
      '11111111-1111-1111-1111-111111111111',
      'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
      'debit', 'Long note', 1, 'SGD', now(),
      jsonb_build_object('user_notes', repeat('x', 4001)), 'confirmed'
    )$$,
  '23514',
  null,
  'transaction user notes are capped at 4000 characters'
);

select throws_ok(
  $$insert into public.transactions (
      user_id, account_id, transaction_kind, title,
      original_amount_minor, original_currency, occurred_at,
      details, review_status
    ) values (
      '11111111-1111-1111-1111-111111111111',
      'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
      'debit', 'Forged source provenance', 1, 'SGD', now(),
      '{"user_notes":"note","references":["forged"],"account_evidence":{"card_last_four":"4242"}}',
      'confirmed'
    )$$,
  '42501',
  null,
  'manual browser inserts cannot forge server-owned transaction details'
);

select throws_ok(
  $$insert into public.transactions (
      user_id, account_id, transaction_kind, title,
      original_amount_minor, original_currency, occurred_at,
      review_status, creation_method
    ) values (
      '11111111-1111-1111-1111-111111111111',
      'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
      'debit', 'Supplied provenance', 1, 'SGD', now(),
      'confirmed', 'manual'
    )$$,
  '42501',
  null,
  'browser clients cannot supply creation provenance'
);

select throws_ok(
  $$insert into public.transactions (
      id, user_id, account_id, transaction_kind, title,
      original_amount_minor, original_currency, occurred_at, review_status
    ) values (
      gen_random_uuid(),
      '11111111-1111-1111-1111-111111111111',
      'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
      'debit', 'Supplied identifier', 1, 'SGD', now(), 'confirmed'
    )$$,
  '42501',
  null,
  'browser clients cannot supply canonical transaction identifiers'
);

select throws_ok(
  $$insert into public.transactions (
      user_id, account_id, transaction_kind, title,
      original_amount_minor, original_currency, occurred_at,
      review_status, updated_at
    ) values (
      '11111111-1111-1111-1111-111111111111',
      'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
      'debit', 'Supplied timestamp', 1, 'SGD', now(), 'confirmed', now()
    )$$,
  '42501',
  null,
  'browser clients cannot supply canonical timestamps'
);

select throws_ok(
  $$update public.transactions
    set title = 'Browser update'
    where title = 'Browser manual transaction'$$,
  '42501',
  null,
  'authenticated browser users still cannot update transactions'
);

select throws_ok(
  $$delete from public.transactions
    where title = 'Browser manual transaction'$$,
  '42501',
  null,
  'authenticated browser users still cannot delete transactions'
);

set local role anon;
set local request.jwt.claim.sub = '';

select throws_ok(
  $$insert into public.transactions (
      user_id, account_id, transaction_kind, title,
      original_amount_minor, original_currency, occurred_at, review_status
    ) values (
      '11111111-1111-1111-1111-111111111111',
      'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
      'debit', 'Anonymous transaction', 1, 'SGD', now(), 'confirmed'
    )$$,
  '42501',
  null,
  'anonymous clients cannot insert transactions'
);

reset role;
grant insert (creation_method, match_confidence, user_modified_at)
  on table public.transactions to authenticated;
set local role authenticated;
set local request.jwt.claim.sub = '11111111-1111-1111-1111-111111111111';

select throws_ok(
  $$insert into public.transactions (
      user_id, account_id, transaction_kind, title,
      original_amount_minor, original_currency, occurred_at,
      review_status, creation_method
    ) values (
      '11111111-1111-1111-1111-111111111111',
      'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
      'debit', 'Automatic provenance', 1, 'SGD', now(),
      'confirmed', 'automatic_source'
    )$$,
  '42501',
  null,
  'RLS rejects non-manual provenance even if column privileges broaden'
);

select throws_ok(
  $$insert into public.transactions (
      user_id, account_id, transaction_kind, title,
      original_amount_minor, original_currency, occurred_at,
      review_status, match_confidence
    ) values (
      '11111111-1111-1111-1111-111111111111',
      'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
      'debit', 'Supplied confidence', 1, 'SGD', now(), 'confirmed', 100
    )$$,
  '42501',
  null,
  'RLS rejects browser-supplied match confidence'
);

select throws_ok(
  $$insert into public.transactions (
      user_id, account_id, transaction_kind, title,
      original_amount_minor, original_currency, occurred_at,
      review_status, user_modified_at
    ) values (
      '11111111-1111-1111-1111-111111111111',
      'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
      'debit', 'Supplied modification time', 1, 'SGD', now(),
      'confirmed', now()
    )$$,
  '42501',
  null,
  'RLS rejects browser-supplied modification provenance'
);

reset role;
revoke insert (creation_method, match_confidence, user_modified_at)
  on table public.transactions from authenticated;

select * from finish();
rollback;

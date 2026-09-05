begin;

create extension if not exists pgtap with schema extensions;
select plan(6);

insert into auth.users (id, email) values
  ('11111111-1111-1111-1111-111111111111', 'account-owner@example.com');

select has_column('public', 'accounts', 'tags', 'Accounts expose a visible tags array');

select col_not_null('public', 'accounts', 'tags', 'tags is never null');

select results_eq(
  $$insert into public.accounts (user_id, side, account_type, name, institution_name)
    values ('11111111-1111-1111-1111-111111111111', 'asset', 'bank_account', 'Everyday', 'DBS')
    returning tags$$,
  $$values ('{}'::text[])$$,
  'tags default to an empty array'
);

select lives_ok(
  $$insert into public.accounts (user_id, side, account_type, name, institution_name, tags)
    values ('11111111-1111-1111-1111-111111111111', 'asset', 'bank_account', 'Tagged', 'OCBC',
            array['savings', 'emergency-fund'])$$,
  'valid tags are accepted'
);

select throws_ok(
  $$insert into public.accounts (user_id, side, account_type, name, institution_name, tags)
    values ('11111111-1111-1111-1111-111111111111', 'asset', 'bank_account', 'Blank tag', 'OCBC',
            array['ok', '   '])$$,
  '23514',
  null,
  'blank or whitespace-only tags are rejected'
);

select throws_ok(
  $$insert into public.accounts (user_id, side, account_type, name, institution_name, tags)
    values ('11111111-1111-1111-1111-111111111111', 'asset', 'bank_account', 'Too many', 'OCBC',
            array(select 'tag-' || generate_series(1, 21)))$$,
  '23514',
  null,
  'more than 20 tags are rejected'
);

select * from finish();
rollback;

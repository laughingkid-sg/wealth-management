begin;

create extension if not exists pgtap with schema extensions;
select plan(16);

select has_column(
  'private', 'source_parser_rules', 'name',
  'global source rules have a human-readable name'
);

select has_column(
  'private', 'source_parser_rules', 'updated_by_user_id',
  'global source rules retain their most recent editor'
);

select ok(
  exists (
    select 1
    from information_schema.columns
    where table_schema = 'private'
      and table_name = 'source_parser_rules'
      and column_name = 'updated_by_user_id'
      and data_type = 'uuid'
      and is_nullable = 'YES'
  ),
  'rule editor attribution is an optional UUID'
);

select ok(
  exists (
    select 1
    from information_schema.columns
    where table_schema = 'private'
      and table_name = 'source_parser_rules'
      and column_name = 'name'
      and data_type = 'text'
      and is_nullable = 'NO'
  ),
  'rule names are required text values'
);

select results_eq(
  $$select count(*)
    from pg_constraint
    where conrelid = 'private.source_parser_rules'::regclass
      and conname = 'source_parser_rules_name_check'
      and contype = 'c'$$,
  array[1::bigint],
  'rule names have a dedicated length constraint'
);

select results_eq(
  $$select count(*)
    from private.source_parser_rules
    where name is null
      or char_length(btrim(name)) not between 1 and 100$$,
  array[0::bigint],
  'every existing global rule has a valid name'
);

select ok(
  exists (
    select 1
    from pg_trigger
    where tgrelid = 'private.source_parser_rules'::regclass
      and tgname = 'source_parser_rules_set_updated_at'
      and not tgisinternal
  ),
  'global source rule edits retain automatic updated_at auditing'
);

select ok(
  exists (
    select 1
    from pg_constraint constraint_record
    join pg_class referenced_relation
      on referenced_relation.oid = constraint_record.confrelid
    join pg_namespace referenced_namespace
      on referenced_namespace.oid = referenced_relation.relnamespace
    where constraint_record.conrelid = 'private.source_parser_rules'::regclass
      and constraint_record.conname = 'source_parser_rules_updated_by_user_id_fkey'
      and constraint_record.contype = 'f'
      and referenced_namespace.nspname = 'auth'
      and referenced_relation.relname = 'users'
      and constraint_record.confdeltype = 'n'
  ),
  'rule editor attribution references auth users and clears on user deletion'
);

select has_index(
  'private', 'source_parser_rules', 'source_parser_rules_updated_by_user_id_idx',
  'rule editor foreign-key lookups are indexed'
);

select results_eq(
  $$select relrowsecurity
    from pg_class
    where oid = 'private.source_parser_rules'::regclass$$,
  array[true],
  'global source rules retain RLS as defense in depth'
);

select results_eq(
  $$select count(*)
    from (values ('anon'), ('authenticated')) as role_name(name)
    where has_table_privilege(role_name.name, 'private.source_parser_rules', 'SELECT')
      or has_table_privilege(role_name.name, 'private.source_parser_rules', 'INSERT')
      or has_table_privilege(role_name.name, 'private.source_parser_rules', 'UPDATE')
      or has_table_privilege(role_name.name, 'private.source_parser_rules', 'DELETE')$$,
  array[0::bigint],
  'browser roles retain no direct privileges on global source rules'
);

insert into auth.users (id, email)
values ('91919191-9191-9191-9191-919191919191', 'global-rule-editor@example.com');

select lives_ok(
  $$insert into private.source_parser_rules (
      id, name, provider, updated_by_user_id
    ) values (
      '93939393-9393-4393-8393-939393939393',
      'FairPrice receipts', 'gmail',
      '91919191-9191-9191-9191-919191919191'
    )$$,
  'a named global rule can attribute a valid authenticated editor'
);

select throws_ok(
  $$insert into private.source_parser_rules (name, provider)
    values ('   ', 'gmail')$$,
  '23514',
  null,
  'blank global rule names are rejected'
);

select throws_ok(
  $$insert into private.source_parser_rules (name, provider)
    values (repeat('x', 101), 'gmail')$$,
  '23514',
  null,
  'global rule names longer than 100 characters are rejected'
);

select throws_ok(
  $$insert into private.source_parser_rules (
      name, provider, updated_by_user_id
    ) values (
      'Unknown editor', 'gmail',
      '92929292-9292-9292-9292-929292929292'
    )$$,
  '23503',
  null,
  'global rules cannot attribute a nonexistent auth user'
);

delete from auth.users
where id = '91919191-9191-9191-9191-919191919191';

select results_eq(
  $$select count(*)
    from private.source_parser_rules
    where id = '93939393-9393-4393-8393-939393939393'
      and updated_by_user_id is null$$,
  array[1::bigint],
  'deleting an editor preserves the rule and clears its attribution'
);

select * from finish();
rollback;

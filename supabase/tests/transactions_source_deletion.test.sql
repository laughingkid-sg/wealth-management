begin;

create extension if not exists pgtap with schema extensions;
select plan(28);

select has_table(
  'private', 'transaction_user_locks',
  'transaction operations have a private per-user coordination row'
);

select has_table(
  'private', 'deleted_provider_messages',
  'deleted provider messages have minimal one-way tombstones'
);

select ok(
  (select count(*) = 2
    from information_schema.columns
    where table_schema = 'private' and table_name = 'transaction_user_locks')
  and
  (select count(*) = 2
    from information_schema.columns
    where table_schema = 'private' and table_name = 'transaction_user_locks'
      and is_nullable = 'NO'
      and (
        (ordinal_position = 1 and column_name = 'user_id' and udt_name = 'uuid')
        or (ordinal_position = 2 and column_name = 'created_at' and udt_name = 'timestamptz')
      )),
  'coordination rows contain only their owner and creation time'
);

select ok(
  (select count(*) = 5
    from information_schema.columns
    where table_schema = 'private' and table_name = 'deleted_provider_messages')
  and
  (select count(*) = 5
    from information_schema.columns
    where table_schema = 'private' and table_name = 'deleted_provider_messages'
      and is_nullable = 'NO'
      and (
        (ordinal_position = 1 and column_name = 'user_id' and udt_name = 'uuid')
        or (ordinal_position = 2 and column_name = 'source_type' and udt_name = 'text')
        or (ordinal_position = 3 and column_name = 'provider' and udt_name = 'text')
        or (ordinal_position = 4 and column_name = 'provider_message_digest' and udt_name = 'bytea')
        or (ordinal_position = 5 and column_name = 'deleted_at' and udt_name = 'timestamptz')
      )),
  'tombstones retain only owner, provider identity dimensions, digest, and deletion time'
);

select results_eq(
  $$select count(*) from pg_class
    where oid in (
      'private.transaction_user_locks'::regclass,
      'private.deleted_provider_messages'::regclass
    ) and relrowsecurity$$,
  array[2::bigint],
  'both private deletion tables enable RLS as defense in depth'
);

select results_eq(
  $$select count(*)
    from (values ('anon'), ('authenticated')) as role_name(name)
    cross join (values
      ('private.transaction_user_locks'),
      ('private.deleted_provider_messages')
    ) as relation(name)
    where has_table_privilege(role_name.name, relation.name, 'SELECT')
      or has_table_privilege(role_name.name, relation.name, 'INSERT')
      or has_table_privilege(role_name.name, relation.name, 'UPDATE')
      or has_table_privilege(role_name.name, relation.name, 'DELETE')$$,
  array[0::bigint],
  'browser roles have no privileges on deletion coordination or tombstones'
);

select results_eq(
  $$select count(*)
    from pg_constraint
    where conrelid in (
        'private.transaction_user_locks'::regclass,
        'private.deleted_provider_messages'::regclass
      )
      and confrelid = 'auth.users'::regclass
      and contype = 'f'
      and confdeltype = 'c'$$,
  array[2::bigint],
  'both private deletion tables are owner-scoped and cascade with their auth user'
);

select ok(
  exists (
    select 1
    from pg_constraint
    where conrelid = 'private.transaction_user_locks'::regclass
      and contype = 'p'
      and conkey = array[
        (select attnum from pg_attribute
          where attrelid = 'private.transaction_user_locks'::regclass
            and attname = 'user_id')
      ]::smallint[]
  ),
  'one coordination row exists per user'
);

select ok(
  exists (
    select 1
    from pg_constraint
    where conrelid = 'private.deleted_provider_messages'::regclass
      and contype = 'p'
      and conkey = array[
        (select attnum from pg_attribute
          where attrelid = 'private.deleted_provider_messages'::regclass
            and attname = 'user_id'),
        (select attnum from pg_attribute
          where attrelid = 'private.deleted_provider_messages'::regclass
            and attname = 'source_type'),
        (select attnum from pg_attribute
          where attrelid = 'private.deleted_provider_messages'::regclass
            and attname = 'provider'),
        (select attnum from pg_attribute
          where attrelid = 'private.deleted_provider_messages'::regclass
            and attname = 'provider_message_digest')
      ]::smallint[]
  ),
  'tombstone uniqueness is scoped to owner and complete provider identity'
);

select results_eq(
  $$select count(*) from pg_constraint
    where conrelid = 'private.deleted_provider_messages'::regclass
      and conname in (
        'deleted_provider_messages_source_type_check',
        'deleted_provider_messages_provider_check',
        'deleted_provider_messages_digest_check'
      ) and contype = 'c'$$,
  array[3::bigint],
  'all tombstone shape constraints are installed'
);

select results_eq(
  $$select count(*) from pg_constraint
    where conrelid = 'private.source_parse_attempts'::regclass
      and conname in (
        'source_parse_attempts_request_metadata_length_check',
        'source_parse_attempts_parsed_candidate_length_check'
      ) and contype = 'c'$$,
  array[2::bigint],
  'the remaining exact audit fields have database-enforced size ceilings'
);

select results_eq(
  $$select count(*) from pg_constraint
    where conrelid = 'private.transaction_jobs'::regclass
      and conname = 'transaction_jobs_source_cleanup_scope_check'
      and contype = 'c'$$,
  array[1::bigint],
  'cleanup outbox rows have a dedicated scope and retry constraint'
);

select ok(
  exists (
    select 1 from pg_constraint
    where conrelid = 'private.transaction_jobs'::regclass
      and conname = 'transaction_jobs_job_type_check'
      and pg_get_constraintdef(oid) like '%source_attachment_cleanup%'
  ),
  'source attachment cleanup is an allowed durable job kind'
);

select has_index(
  'private', 'transaction_jobs', 'transaction_jobs_attachment_cleanup_recovery_idx',
  'terminal cleanup failures have a narrow recovery index'
);

insert into auth.users (id, email) values
  ('91919191-9191-9191-9191-919191919191', 'source-delete@example.test'),
  ('92929292-9292-9292-9292-929292929292', 'source-delete-other@example.test');

insert into private.transaction_user_locks (user_id) values
  ('91919191-9191-9191-9191-919191919191'),
  ('92929292-9292-9292-9292-929292929292');

insert into private.data_sources (
  id, user_id, source_type, provider, provider_message_id, received_at, raw_data
) values (
  '93939393-9393-9393-9393-939393939393',
  '91919191-9191-9191-9191-919191919191',
  'gmail_email', 'gmail', 'source-delete-pgtap', now(), '{}'
);

select lives_ok(
  $$insert into private.transaction_jobs (user_id, job_type, payload, max_attempts)
    values (
      '91919191-9191-9191-9191-919191919191',
      'source_attachment_cleanup',
      '{"source_id":"93939393-9393-9393-9393-939393939393","object_paths":["91919191-9191-9191-9191-919191919191/93939393-9393-9393-9393-939393939393/receipt.pdf"]}',
      5
    )$$,
  'a source attachment cleanup job can be inserted without a live source FK'
);

select results_eq(
  $$select count(*) from private.transaction_jobs
    where user_id = '91919191-9191-9191-9191-919191919191'
      and job_type = 'source_attachment_cleanup'
      and data_source_id is null
      and sync_run_id is null
      and max_attempts = 5$$,
  array[1::bigint],
  'cleanup outbox ownership and retry bound are persisted without source references'
);

select throws_ok(
  $$insert into private.transaction_jobs (user_id, job_type, max_attempts)
    values (
      '91919191-9191-9191-9191-919191919191',
      'source_attachment_cleanup',
      6
    )$$,
  '23514',
  null,
  'cleanup outbox attempts are capped at five'
);

select throws_ok(
  $$insert into private.transaction_jobs (user_id, data_source_id, job_type, max_attempts)
    values (
      '91919191-9191-9191-9191-919191919191',
      '93939393-9393-9393-9393-939393939393',
      'source_attachment_cleanup',
      5
    )$$,
  '23514',
  null,
  'cleanup outbox rows cannot retain a source foreign key'
);

select lives_ok(
  $$insert into private.deleted_provider_messages
      (user_id, source_type, provider, provider_message_digest)
    values (
      '91919191-9191-9191-9191-919191919191',
      'gmail_email',
      'gmail',
      decode(repeat('ab', 32), 'hex')
    )$$,
  'a 32-byte deleted-provider digest can be retained without raw evidence'
);

select throws_ok(
  $$insert into private.deleted_provider_messages
      (user_id, source_type, provider, provider_message_digest)
    values (
      '91919191-9191-9191-9191-919191919191',
      'gmail_email',
      'gmail',
      decode('ab', 'hex')
    )$$,
  '23514',
  null,
  'deleted-provider tombstones reject non-SHA-256 digests'
);

select throws_ok(
  $$insert into private.deleted_provider_messages
      (user_id, source_type, provider, provider_message_digest)
    values (
      '91919191-9191-9191-9191-919191919191',
      'gmail_email',
      'gmail',
      decode(repeat('ab', 32), 'hex')
    )$$,
  '23505',
  null,
  'the same provider message cannot be tombstoned twice for one user'
);

select lives_ok(
  $$insert into private.deleted_provider_messages
      (user_id, source_type, provider, provider_message_digest)
    values (
      '92929292-9292-9292-9292-929292929292',
      'gmail_email',
      'gmail',
      decode(repeat('ab', 32), 'hex')
    )$$,
  'the same provider digest remains independent between users'
);

select lives_ok(
  $$insert into private.source_parse_attempts
      (user_id, data_source_id, request_metadata, parsed_candidate)
    values (
      '91919191-9191-9191-9191-919191919191',
      '93939393-9393-9393-9393-939393939393',
      '{"request":"bounded"}',
      '{"candidate":"bounded"}'
    )$$,
  'bounded exact audit values remain valid'
);

select throws_ok(
  $$insert into private.source_parse_attempts
      (user_id, data_source_id, request_metadata)
    values (
      '91919191-9191-9191-9191-919191919191',
      '93939393-9393-9393-9393-939393939393',
      jsonb_build_object('payload', repeat('r', 65536))
    )$$,
  '23514',
  null,
  'request metadata cannot exceed the exact-field response ceiling'
);

select throws_ok(
  $$insert into private.source_parse_attempts
      (user_id, data_source_id, parsed_candidate)
    values (
      '91919191-9191-9191-9191-919191919191',
      '93939393-9393-9393-9393-939393939393',
      jsonb_build_object('payload', repeat('c', 2097152))
    )$$,
  '23514',
  null,
  'parsed candidates cannot exceed the exact-field response ceiling'
);

delete from auth.users where id = '92929292-9292-9292-9292-929292929292';

select results_eq(
  $$select count(*) from (
      select user_id from private.transaction_user_locks
      where user_id = '92929292-9292-9292-9292-929292929292'
      union all
      select user_id from private.deleted_provider_messages
      where user_id = '92929292-9292-9292-9292-929292929292'
    ) deleted_owner_rows$$,
  array[0::bigint],
  'deleting an auth user cascades their coordination row and tombstones'
);

set local role authenticated;
set local request.jwt.claim.sub = '91919191-9191-9191-9191-919191919191';

select throws_ok(
  $$select * from private.transaction_user_locks$$,
  '42501',
  null,
  'authenticated users cannot access transaction coordination locks directly'
);

select throws_ok(
  $$select * from private.deleted_provider_messages$$,
  '42501',
  null,
  'authenticated users cannot access deleted-provider tombstones directly'
);

select * from finish();
rollback;

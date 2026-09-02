begin;

create extension if not exists pgtap with schema extensions;
select plan(9);

select has_column(
  'private', 'transaction_jobs', 'cleanup_failure_count',
  'cleanup jobs retain a cumulative failure count for monitoring'
);

select ok(
  exists (
    select 1
    from information_schema.columns
    where table_schema = 'private'
      and table_name = 'transaction_jobs'
      and column_name = 'cleanup_failure_count'
      and udt_name = 'int8'
      and is_nullable = 'NO'
      and column_default is not null
  ),
  'cleanup failure count is a non-null bigint with a default'
);

select results_eq(
  $$select count(*) from pg_constraint
    where conrelid = 'private.transaction_jobs'::regclass
      and conname = 'transaction_jobs_cleanup_failure_count_check'
      and contype = 'c'$$,
  array[1::bigint],
  'cleanup failure counts have a dedicated shape constraint'
);

select has_index(
  'private', 'transaction_jobs', 'transaction_jobs_attachment_cleanup_recovery_idx',
  'retrying cleanup work has a focused monitoring index'
);

insert into auth.users (id, email)
values ('94949494-9494-9494-9494-949494949494', 'cleanup-recovery@example.test');

select lives_ok(
  $$insert into private.transaction_jobs (id, user_id, job_type, payload)
    values (
      '95959595-9595-9595-9595-959595959595',
      '94949494-9494-9494-9494-949494949494',
      'source_attachment_cleanup',
      '{"source_id":"96969696-9696-9696-9696-969696969696","object_paths":["94949494-9494-9494-9494-949494949494/96969696-9696-9696-9696-969696969696/receipt.pdf"]}'
    )$$,
  'new cleanup work defaults to a valid zero failure count'
);

select results_eq(
  $$select cleanup_failure_count
    from private.transaction_jobs
    where id = '95959595-9595-9595-9595-959595959595'$$,
  array[0::bigint],
  'new cleanup work starts with zero recorded failures'
);

select lives_ok(
  $$update private.transaction_jobs
    set cleanup_failure_count = 7
    where id = '95959595-9595-9595-9595-959595959595'$$,
  'cleanup work may retain failures across retry bursts'
);

select throws_ok(
  $$update private.transaction_jobs
    set cleanup_failure_count = -1
    where id = '95959595-9595-9595-9595-959595959595'$$,
  '23514',
  null,
  'cleanup failure counts cannot be negative'
);

select throws_ok(
  $$insert into private.transaction_jobs (
      user_id, job_type, cleanup_failure_count
    ) values (
      '94949494-9494-9494-9494-949494949494',
      'gmail_ingestion',
      1
    )$$,
  '23514',
  null,
  'non-cleanup jobs cannot carry cleanup failure state'
);

select * from finish();
rollback;

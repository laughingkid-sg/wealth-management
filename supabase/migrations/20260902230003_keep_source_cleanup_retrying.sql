-- Storage cleanup is deletion-critical work. Preserve a cumulative, queryable
-- failure count and recycle each five-attempt burst instead of abandoning the
-- exact object-path payload in a terminal state.
alter table private.transaction_jobs
  add column cleanup_failure_count bigint not null default 0,
  add constraint transaction_jobs_cleanup_failure_count_check
    check (
      cleanup_failure_count >= 0
      and (
        job_type = 'source_attachment_cleanup'
        or cleanup_failure_count = 0
      )
    );

comment on column private.transaction_jobs.cleanup_failure_count is
  'Cumulative failed cleanup attempts. Cleanup jobs remain durable and retry until Storage deletion succeeds.';

-- Recover cleanup rows that reached the old terminal limit before this
-- migration. Their exact source/path payload remains intact for the worker.
update private.transaction_jobs
set status = 'queued',
    attempts = 0,
    cleanup_failure_count = greatest(attempts::bigint, 1),
    run_after = greatest(run_after, now() + interval '15 minutes'),
    completed_at = null,
    leased_at = null,
    lease_expires_at = null,
    leased_by = null,
    last_error = 'Attachment cleanup remains pending and will retry automatically.'
where job_type = 'source_attachment_cleanup'
  and status = 'failed';

drop index if exists private.transaction_jobs_attachment_cleanup_recovery_idx;

create index transaction_jobs_attachment_cleanup_recovery_idx
  on private.transaction_jobs (cleanup_failure_count desc, updated_at)
  where job_type = 'source_attachment_cleanup'
    and cleanup_failure_count > 0;

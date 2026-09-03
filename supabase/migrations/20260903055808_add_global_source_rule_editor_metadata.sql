-- Global source rules are edited through the trusted Go API. Names make the
-- rules understandable in the settings UI, while editor attribution and the
-- existing version/updated_at fields provide optimistic-concurrency history.

alter table private.source_parser_rules
  add column name text,
  add column updated_by_user_id uuid
    references auth.users(id)
    on delete set null;

-- Preserve stable, human-readable names for the three first-party rules that
-- predate this column. The UUID fallback keeps any development-only rows valid
-- without guessing what they represent.
update private.source_parser_rules
set name = case
  when provider = 'gmail'
    and sender_matcher = '(?i)@(?:[a-z0-9-]+\.)*ocbc\.com(?:\.sg)?(?:>|$)'
    and content_matcher = '(?is)\b(?:charged|purchase|spent|debited)\b.*\bSGD\b'
    and version = 1
    then 'OCBC card purchase (legacy v1)'
  when provider = 'gmail'
    and sender_matcher = '(?i)@(?:[a-z0-9-]+\.)*ocbc\.com(?:\.sg)?(?:>|$)'
    and content_matcher = '(?is)\b(?:charged|purchase|spent|debited)\b.*\bSGD\b'
    and version = 2
    then 'OCBC card purchase'
  when provider = 'gmail'
    and sender_matcher is null
    and content_matcher = '(?is)(?:^|[^A-Za-z0-9_])(?:mastercard|master\s*card|visa|amex|american\s+express|card)(?:$|[^A-Za-z0-9_]).{0,255}?(?:\(\s*)?(?:\*{2,}|x{2,}|•{2,})\s*([0-9]{4})(?:$|[^0-9])'
    and version = 1
    then 'Masked card evidence'
  else concat('Source rule ', left(id::text, 8))
end;

alter table private.source_parser_rules
  alter column name set not null,
  add constraint source_parser_rules_name_check
    check (char_length(btrim(name)) between 1 and 100);

create index source_parser_rules_updated_by_user_id_idx
  on private.source_parser_rules (updated_by_user_id);

comment on column private.source_parser_rules.name is
  'Human-readable global rule name shown in transaction settings.';

comment on column private.source_parser_rules.updated_by_user_id is
  'Most recent authenticated editor; cleared if that auth user is deleted.';

-- Reassert the original private-table security posture after extending it.
alter table private.source_parser_rules enable row level security;
revoke all privileges on table private.source_parser_rules from public, anon, authenticated;

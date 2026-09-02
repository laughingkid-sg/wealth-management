-- Align the already-deployed table with the least-privilege grants in the
-- initial migration. This leaves the Data API available only for its intended
-- authenticated SELECT, INSERT and UPDATE operations.
revoke all privileges on table public.accounts from public, anon, authenticated;
grant select, insert, update on table public.accounts to authenticated;

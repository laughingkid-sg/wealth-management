-- The browser directory surface writes to accounts via column-scoped grants
-- (see 20260904043721). Extend those grants to cover the new tags column so
-- authenticated users can set tags on insert and update.
grant insert (tags) on table public.accounts to authenticated;
grant update (tags) on table public.accounts to authenticated;

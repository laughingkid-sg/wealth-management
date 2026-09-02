# Wealth Builder product overview

## Product direction

Wealth Builder is a private personal wealth-management SPA built progressively, page by page. Each feature must have an explicit user goal, bounded scope, and a self-contained documentation folder before it grows beyond an agreed slice.

## Platform decisions

- Frontend: React, TypeScript, shadcn/ui, and the existing styling setup.
- Data and authentication: hosted Supabase.
- Simple browser-accessible CRUD: Supabase Data REST API protected by RLS.
- All other backend requests: Go HTTP API, including business logic, integrations, cross-resource operations, and privileged work.
- Initial authentication: one private email/password user. Public registration and OAuth are out of scope unless explicitly requested.

## Documentation rule

Feature requirements and technical details live under `docs/features/<feature>/`. This overview records only cross-feature decisions and must not become a duplicate feature specification.

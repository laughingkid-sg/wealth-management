# Wealth Builder — Agent Guide

## Product and scope

We are building a personal wealth-management platform as a single-page application (SPA). Build the product page by page: implement only the page, behaviour, and supporting API/data work currently requested. Do not begin deployment, infrastructure-as-code, or cloud-VM work unless explicitly asked.

When a requirement changes, update the corresponding product and technical documentation in the same change. Keep feature documentation isolated so future work can read only the relevant feature folder plus any necessary cross-cutting product documentation.

## Agreed stack

- Frontend: React + TypeScript, shadcn/ui, and the project's existing styling setup.
- Backend: Go HTTP API.
- Data and authentication: hosted Supabase.
- Authentication (initial release): one private user using email and password. Do not implement public self-registration or OAuth unless requested.

Use the Supabase Data REST API with RLS for simple browser-accessible CRUD. Route every other backend request through the Go HTTP API, including sensitive business logic, cross-resource workflows, integrations, aggregations, and privileged operations.

Keep the frontend, API, and database concerns separate. Browser code may use Supabase only for the user session and data explicitly intended for browser access; sensitive business logic belongs in the Go API.

## Frontend conventions

- Use TypeScript strictly; avoid `any` and model external/API data explicitly.
- Prefer small, focused components and accessible semantic HTML.
- Use the shadcn/ui MCP, when available, and shadcn/ui components before creating a bespoke equivalent. Keep generated shadcn components local and customize them only when the design requires it.
- Design responsive layouts and include useful loading, empty, and error states for data-backed screens.
- Do not introduce a global state library unless a concrete page requirement warrants it.

## Go API conventions

- Keep handlers thin: validate input, call application/domain logic, and return consistent JSON responses.
- Pass `context.Context` through all I/O boundaries.
- Validate untrusted input at the API boundary; never expose database errors or secrets in responses.
- Keep configuration in environment variables. Provide safe example values in `.env.example`, never real credentials.

## Supabase rules

- Use the available Supabase skill and MCP/documentation before implementing Supabase features; verify current API/CLI behaviour rather than relying on memory.
- Never expose the Supabase `service_role`/secret key in frontend code or any public environment variable. The frontend may use only the publishable (or legacy anon) key.
- Enable RLS on every browser-accessible table and write policies that restrict rows to the authenticated user. Policies must enforce ownership, not merely `TO authenticated`.
- Treat `user_metadata` as user-controlled; do not use it for authorization.
- Make schema changes through the repository's Supabase migration workflow. Review generated migrations and RLS policies before applying them.
- Keep schema, RLS, and auth changes minimal and tested. Load the Supabase Postgres best-practices skill before changing database objects.

## Security and repository hygiene

- Never commit secrets, Supabase tokens, private keys, or populated `.env` files.
- Avoid unrelated refactors and preserve existing user changes.
- Run the narrowest relevant checks after changes (typecheck, lint, tests, or targeted API tests).

## Documentation structure

- `docs/README.md` is the documentation index and describes how to find the right layer.
- `docs/product/` contains cross-feature product decisions. Read it only when a change depends on product-wide context.
- `docs/features/<feature>/README.md` contains that feature's requirements and scope.
- `docs/features/<feature>/technical.md` contains that feature's implementation, data, and verification details.
- Update only the affected feature documents and any genuinely affected product-level document; do not require unrelated feature documents to be read or changed.

## Git workflow

- Work on a dedicated branch for each change. Use the `codex/` prefix unless a different branch name is requested.
- Use Conventional Commit messages such as `feat:`, `fix:`, `docs:`, `test:`, `refactor:`, and `chore:`.
- Make small, focused commits after each completed logical update.
- Open a merge request for each branch and merge it only after review.
- Include `Co-authored-by: Codex <codex@openai.com>` in commits created by Codex.

### Accounts initial delivery exception

The completed Accounts feature may be captured as one initial commit because its implementation predates this workflow. Apply the small-commit workflow to all subsequent changes.

## Collaboration

- Before a page is built, clarify its user goal, primary actions, data it needs, and success/empty/error states if they are not already evident.
- When parallel work is explicitly requested, divide it into independent, non-overlapping tasks and avoid concurrent edits to the same files.

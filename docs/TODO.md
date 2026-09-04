# Wealth Builder TODO

This is the single implementation backlog for the Wealth Builder project. The scrum master maintains it by turning requested work into clear, actionable items and keeping their status current.

## Current backlog

### Transactions — delete a transaction

- **Status:** `todo`
- **Priority:** `next`
- **Context:** Transactions should support removing an existing transaction that the user no longer wants to keep.
- **Acceptance criteria:**
  - The user can initiate deletion for an existing transaction.
  - The UI clearly identifies the transaction being deleted and asks for confirmation before removal.
  - A confirmed deletion removes the transaction from the data store and refreshes the relevant transaction list or detail view.
  - Loading, success, and error states are communicated accessibly; failed deletion leaves the transaction intact and explains that it was not removed.
  - Deletion is restricted to transactions owned by the authenticated user.

### Transactions — transaction title

- **Status:** `todo`
- **Priority:** `next`
- **Context:** Add a clear title to each transaction so users can identify transactions quickly.
- **Acceptance criteria:**
  - A transaction has a title field that can be entered or edited by the user.
  - The title is displayed consistently in transaction lists, detail views, and relevant summaries.
  - Title input is validated and handles empty or overly long values clearly.
  - Title changes persist without altering unrelated transaction data.
  - Title data respects authenticated-user ownership and is covered by focused tests.

### Tengo engine — dynamic handling support

- **Status:** `todo`
- **Priority:** `later`
- **Context:** Add Tengo engine support for dynamically handling transaction-related processing as rules or inputs change.
- **Acceptance criteria:**
  - The Tengo engine integration has a defined interface for dynamic handling.
  - Runtime inputs or rules can be handled without hard-coded changes for every supported case.
  - Unsupported or invalid inputs produce a safe, actionable error and do not corrupt transaction data.
  - Dynamic handling is observable through focused tests and documented behaviour.

### Email — clean content with Tengo before sending

- **Status:** `todo`
- **Priority:** `next`
- **Context:** Run outgoing email content through Tengo before sending so messages are cleaned and normalized consistently.
- **Acceptance criteria:**
  - Email content is passed through Tengo before the send operation.
  - Cleaning preserves the intended meaning and required recipients, subject, and message content.
  - The cleaned result is the exact payload used for sending and is previewable or inspectable before delivery where applicable.
  - If cleaning fails or returns invalid content, the email is not sent and the user receives a clear error.
  - Cleaning behaviour is covered by focused tests and does not expose sensitive email data in logs.

## Backlog format

Each TODO should include:

- **Status:** `todo`, `in progress`, `blocked`, or `done`
- **Priority:** `now`, `next`, or `later`
- **Context:** the feature, user need, or implementation area
- **Acceptance criteria:** the observable result that marks the item complete

## Working agreements

- Keep items small enough to implement and verify independently.
- Record dependencies or blockers explicitly.
- Update this file whenever an implementation request changes scope or is completed.
- Keep completed items for traceability unless they are superseded by a newer decision.

# Accounts — Product Requirements

## Summary

Accounts is the first Wealth Builder feature. It is an authenticated, user-owned account directory: users create and maintain records for the financial accounts they have.

This release deliberately stores **no financial value**. An account is only an identifier and its descriptive metadata. There are no balances, asset codes, positions, opening balances, transactions, valuations, net-worth totals, charts, or market-data calls.

## User goal

> I can sign in and keep a clean, private list of my bank, brokerage, crypto, RSU, and liability accounts so the product has a trustworthy account catalogue before financial data is added.

## In scope

- Email/password sign-in with Supabase Auth; no public sign-up or OAuth.
- A protected `/accounts` page listing only the signed-in user's accounts.
- Create, view, edit, soft-delete, restore, search, filter, and sort account records.
- Fixed account types:
  - Assets: Bank Account, Brokerage, Crypto Wallet, Crypto Exchange, RSU.
  - Liabilities: Credit Card, Personal Loan.
- Required institution/platform and user-owned custom metadata.
- Supabase Data API access from the frontend, with RLS enforcing ownership.

## Explicitly out of scope

- Amounts, balances, positions, currencies, asset codes, BTC/ETH/RSU quantities, or SGD conversion.
- Opening-balance records, transactions, imports, transfers, adjustments, or transaction history.
- Net-worth totals, asset/liability totals, percentages, performance, or charts.
- Bank, brokerage, exchange, wallet, or market-data integrations.
- A Go API for ordinary account CRUD.

## Account record

| Field | Rules |
| --- | --- |
| Side | Required: Asset or Liability. |
| Account type | Required. The available fixed types are limited by the chosen side. |
| Account name | Required, 1–100 characters. |
| Institution/platform | Required free text, 1–100 characters; for example, DBS, Interactive Brokers, MetaMask, an employer, or “Self-custody”. |
| Account identification | Optional free-text reference, maximum 100 characters. Never store credentials, full account/card numbers, private keys, or seed phrases. |
| Notes | Optional plain text, maximum 500 characters. |
| Metadata | Optional user-defined key/value data, rendered as safe text only. |

## Experience

### Sign in

- Unauthenticated visitors see an email/password sign-in page.
- A valid session opens `/accounts`; missing or expired sessions return the visitor to sign-in.
- The provisioned user can sign out. No registration form is shown.

### Accounts list

- Page heading: **Accounts**.
- Primary action: **Add account**.
- List or cards grouped by Asset and Liability; rows show account name, type, and institution/platform only.
- Search account name and institution/platform.
- Filter by side, account type, institution/platform, and deleted status.
- Sort by account name, ascending or descending.
- Expand an account row to view its safe custom metadata.
- Empty state prompts the user to add their first account.
- Soft-deleted accounts are hidden by default, can be restored through a filter, and are never permanently deleted in this release.

The reference screenshots inform the clean card layout, simple toolbar, and orange primary action. Their financial summaries, charts, period controls, and refresh action are not part of this release.

## States and errors

- Loading: skeleton account rows.
- Empty: explanation and Add account action.
- No results: retain filters and offer Clear filters.
- Validation: inline accessible errors without discarding input.
- Request failure: concise error and Retry.
- Authorisation/session failure: clear local session and route to sign-in.

## Acceptance criteria

1. An unauthenticated visitor cannot view the account page or any account data.
2. A provisioned user can sign in, refresh, and sign out with Supabase Auth.
3. The user can create and edit each permitted account type, with a required compatible side and institution/platform.
4. The user can save custom metadata on their own account record.
5. The user can search, filter, sort, soft-delete, and restore their own accounts.
6. A manually crafted REST request cannot read or mutate another user's account row or metadata.
7. No values, balances, asset codes, transactions, totals, charts, or market-data requests are created by this feature.
8. Forms, menus, dialogs, and errors are keyboard accessible and the page is responsive.

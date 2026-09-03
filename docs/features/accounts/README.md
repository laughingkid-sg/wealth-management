# Accounts requirements

## User goal

> I can sign in and keep a clean, private list of my bank, brokerage, crypto, RSU, and liability accounts before the product starts tracking financial values.

## Scope

Accounts is a user-owned directory only. It stores descriptive identity and metadata, not financial information.

### Included

- Email/password sign-in with Supabase Auth; no public sign-up or OAuth.
- Create, edit, search, filter, alphabetically sort, soft-delete, and restore accounts.
- Click anywhere on an account header row—its icon, text, or whitespace—to expand or collapse its safe custom metadata. Header action buttons remain independent, and clicks inside expanded details do not toggle the row.
- Add and Edit use a popup that closes with Escape except while a save is in progress.
- Required account name and institution/platform.
- Optional account identification, notes, and user-defined key/value metadata.
- Fixed account types:
  - Assets: Bank Account, Brokerage, Digital Wallet, Crypto Wallet, Crypto Exchange, RSU.
  - Liabilities: Credit Card, Personal Loan.
- A responsive Accounts page with a sidebar, top bar, loading, empty, no-results, validation, and request-error states.

### Not included

- Balances, quantities, positions, currencies, asset codes, or SGD conversion.
- Opening balances, transactions, imports, transfers, adjustments, or transaction history.
- Net worth, performance, charts, market data, or financial-provider integrations.
- A Go API for ordinary Accounts CRUD.

## Account fields

| Field | Rule |
| --- | --- |
| Side | Required: Asset or Liability. |
| Account type | Required and limited by side. |
| Account name | Required, 1–100 characters. |
| Institution/platform | Required, 1–100 characters. |
| Account identification | Optional safe free-text reference, maximum 100 characters. |
| Notes | Optional plain text, maximum 500 characters. |
| Metadata | Optional safe user-defined key/value data. |

Never store credentials, full account or card numbers, private keys, or seed phrases.

## Acceptance criteria

1. An unauthenticated visitor cannot access account data.
2. A provisioned user can sign in, sign out, and manage only their own account rows.
3. The user can create and edit every permitted type with a compatible side and required institution/platform; the Add/Edit popup closes with Escape unless it is saving.
4. The user can expand or collapse an account from any non-action area of its header row, while header actions remain independent and expanded details do not toggle the row.
5. The user can search, filter, alphabetically sort, soft-delete, and restore their own accounts.
6. No value, balance, position, transaction, market-data, total, or chart capability is introduced.

See [technical implementation](technical.md) for implementation and security details.

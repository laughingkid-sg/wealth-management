# Account Balances and Credit Card requirements

## Delivery status

**Delivered.** Account opening balances and revision history, transaction spending treatments, and the Credit Card bill workspace are connected to the authenticated Go API and hosted database. Credit Card bills are produced by the shared Bulk Import worker and retain its source evidence and audit trail.

This feature introduces a financial baseline for Accounts, a narrowly scoped transaction-calculation treatment, and a Credit Card bill workspace. The Credit Card workspace remains the sole bill-review surface; Bulk Import owns upload, parsing, and source evidence.

The feature integrates with [Bulk Insert](../bulk-insert/README.md) for statement-document upload and extraction. It does not duplicate Bulk Insert's file, batch, model, source, audit, or cleanup workflow.

## User goal

> I can set an accurate opening balance for each Account, understand which transactions affect my Account balance versus my spending, review and reconcile a monthly Credit Card bill processed through Bulk Import, and pay that bill from a Bank Account without counting the same spending twice.

## Product decisions

### Financial baseline, not a fabricated zero

Every Account has an opening-balance state:

- **Unconfigured** means no amount has been recorded. It is displayed as **Not set**, never as zero.
- **Configured** means the user explicitly saved one or more currency amounts and an as-of date/time.
- An explicit amount of zero, such as `SGD 0.00`, is a valid configured balance and is materially different from unconfigured.

Existing Accounts are backfilled as unconfigured. The product must never invent a zero balance for an Account created before this feature.

### Multi-currency opening balances

An Account may have one opening amount for each currency. The product stores integer minor units and does not calculate exchange rates or synthesize SGD values.

For example, a configured Account may hold a baseline of SGD 1,250.00 and USD 42.00. The UI displays each currency independently. It does not sum different currencies into one total without an explicitly supplied conversion feature.

### Negative balances

Negative opening balances are permitted only for a `bank_account`, representing an overdraft. All other asset Account types require zero or positive opening balances. Credit cards and personal loans store a non-negative amount **owed**; a card credit or loan overpayment is out of scope for this release.

### Corrections

The user can correct a configured opening balance. A correction replaces the current baseline after clear confirmation and records the change reason, prior value, new value, editor, and time in immutable history. It does not create a Transaction.

The account's current balance calculation starts after the currently effective baseline's as-of timestamp. A correction therefore changes the baseline; it is not retroactive transaction editing.

V1 does not let a configured Account return to **Unconfigured**: the user corrects it to explicit zero amounts instead. A first save and every correction must contain at least one currency; clearing the final currency is rejected. This preserves an unambiguous audit trail and calculation cutoff.

## Balance and spending are different calculations

The feature deliberately separates three concepts:

| Concept | Meaning | Effect of a Bank → Credit Card payoff |
| --- | --- | --- |
| Account balance | The displayed balance of an individual Account. | Included: the Bank decreases and the Credit Card amount owed decreases. |
| Net worth | Assets less liabilities across Accounts. | Neutral: both Account legs offset each other. |
| Spending | Consumption/activity reporting, calculated from transaction totals or line items. | Excluded: paying a card bill is not new spending. |

The product must not call a spending exclusion an “exclude from balance” setting. Excluding an internal transfer from individual Account balances would leave both the cash Account and card debt incorrect.

### Calculation treatment

Each canonical Transaction has one effective spending treatment:

- **Transaction total:** use the canonical transaction amount once.
- **Line items:** use the transaction's complete, reconciled line items instead of its header total.
- **Exclude from spending:** include no amount in spending.

The treatment never changes the raw transaction, evidence, Account ownership, or Account-balance effect.

Bank → Credit Card payoff transfers always receive the system-owned **Exclude from spending** treatment. Users cannot remove that protection. Other user-selected treatments require a reason and remain auditable.

Line items can become the spending basis only when every counted item has an amount in the transaction's original currency and the item totals exactly equal the transaction's original amount. Otherwise the product uses the canonical transaction total and tells the user why line items cannot yet be used.

## Account-balance calculation

For a configured Account, current balance is calculated from its effective opening baseline plus confirmed transactions strictly after the baseline as-of time.

| Account side | Displayed balance formula per currency |
| --- | --- |
| Asset | opening amount + credits − debits |
| Liability | opening amount owed + debits − credits |

For net-worth reporting, an Asset contributes its displayed amount and a Liability contributes the negative of its amount owed. An overdrafted Bank Account therefore has a negative Asset balance and reduces net worth.

Transactions before, or exactly at, the baseline as-of time are assumed to be represented by the baseline and are not added again. A transaction with no original amount/currency cannot enter this calculation. An unconfigured Account has no calculated balance; the UI must show an explanation rather than a number.

## Credit Card

**Credit Card** is a dedicated item in the main Transactions navigation. Its landing page groups bills by active Credit Card Account, expands every Account group by default, and lists each Account's bills newest first. Selecting a bill opens the Account-scoped bill detail and reconciliation workflow. The same bill detail may also be opened from its Credit Card Account or an exact Bulk Import result.

A bill is created automatically by Bulk Import's Credit Card bill processor; the Credit Card workspace does not upload or parse files. A bill is not a new top-level transaction type and is not a canonical Transaction.

A bill contains:

- billing-period start and end dates;
- statement date and payment due date;
- one issuer settlement currency and amount due;
- its uploaded Bulk Insert source document;
- imported activity, payment, and summary lines; and
- reconciliation links to canonical transactions.

Every projected bill line preserves its explicit source order and type. `line_index` is one-based within the bill, while `line_kind` distinguishes activity, refund, fee, interest, payment, and summary rows. When evidence supplies only a calendar date, the stored timestamp uses noon UTC as an internal placeholder and records date precision; matching compares the calendar day and the UI must not present noon as a source-provided time.

V1 supports one settlement currency and one **full** payoff transfer for a bill. A bill with an amount due in more than one settlement currency remains in Review until multi-currency settlement is explicitly designed. Foreign-currency purchases may still be represented by linked canonical transactions; their issuer conversion does not create a second expense.

### Bill lines and transaction links

A bill line is reconciliation data, not an additional amount used by balance, net-worth, or spending calculations.

For each imported activity line, the processor:

1. links exactly one uniquely matching canonical Transaction on that Credit Card Account;
2. creates and links exactly one safe missing Credit Card Transaction from the existing Bulk candidate and evidence; or
3. leaves ambiguous, conflicting, or incomplete lines unresolved for user Review.

An imported **payment** line is different: it may only link to an existing Bank → Credit Card internal transfer. The Credit Card workflow never creates a standalone Credit Card credit for a payment, because that would reduce card debt without reducing the paying Bank Account. A payment that cannot be linked remains in review or is explicitly marked ignored with a reason.

Once generated, the bill and its projected document lines are the source of truth for reconciliation. From each bill line, the workflow may link an eligible existing canonical Transaction, safely create and link a missing Transaction from that line's pinned Bulk candidate and evidence, or leave the line unresolved for Review. A canonical Transaction may belong to at most one bill line and a bill line may belong to at most one canonical Transaction.

Summary values such as previous balance, statement total, minimum payment, and amount due remain header/summary information. They must never create a missing Transaction or be added to a calculation.

### Payment detection and payoff

After transaction reconciliation, the processor checks whether the bill was already paid. The only supported payment path is **Bank Account → Credit Card** through the existing atomic internal-transfer model.

A bill is marked **Paid** automatically only when exactly one existing internal transfer has:

- the bill's Credit Card Account as its incoming Account;
- the exact amount due and settlement currency; and
- a transaction time between the statement date and due date, inclusive.

If no such transfer exists, the bill is marked **Unpaid**. Multiple or conflicting matches require Review and never mark the bill Paid automatically.

If the system finds credible debits from active Bank Accounts in that window but no corresponding Credit Card credit, the bill retains every suggestion. The user must explicitly select one when several are plausible and then confirm it. Confirmation keeps the existing Bank debit, creates the missing Credit Card credit, and links the two as one internal transfer. It then marks the bill Paid. The product never guesses between ambiguous candidates, uses an archived Bank Account, or creates a second Bank debit.

From an Unpaid bill, **Pay in full** remains available when no existing payment should be linked. It asks the user to select an active owned Bank Account and creates exactly one internal transfer:

- Bank leg: debit for the statement amount due.
- Credit Card leg: credit for the same amount and currency.
- Transfer link: references the statement as its payoff.

The payoff amount must equal the statement's remaining amount due and currency. A user cannot pay from cash, an external bank, another card, or a non-Bank Account in v1. Partial payments, split payments, overpayments, and one payment applied to multiple statements are explicitly out of scope.

The bill becomes **Paid** only after a detected or newly created atomic transfer is durably linked. If a creation or completion action fails, no partial Card leg or payoff link is retained. The two transfer legs update Account balances, but their system-owned spending treatment excludes them from spending totals.

## Bulk Insert integration contract

Bulk Insert owns user-uploaded files, batches, documents, model extraction, source/audit records, candidate reconciliation, retries, and Storage cleanup. Account Balances owns only the Credit Card bill record, bill-line projection/review, payment detection, and payoff rules.

There is no separate handoff button or upload flow:

1. The user uploads the file in Bulk Import using a **Credit Card bill** template and selects the relevant active Credit Card Account.
2. Bulk Import runs its normal upload, parsing, candidate, deduplication, and reconciliation pipeline once.
3. Its server-owned Credit Card bill processor validates the extracted bill summary and invokes the Account Balances domain after candidate reconciliation.
4. Account Balances automatically creates one idempotent bill linked to that exact Bulk document generation, projects its lines from the Bulk candidates, and performs payment detection.
5. Complete, unambiguous results become Paid or Unpaid. Missing headers, candidate conflicts, omitted or unprojectable candidates, contradictory Account evidence, or payment ambiguity remain visible in Review. The bill retains an unresolved-candidate count so this state cannot disappear merely because no safe line row was created.

Discarding a Review-stage bill does not rerun Bulk Insert, call the model, duplicate its files, or create a second raw source. It removes only the bill and its projection rows; it leaves the Bulk document and every canonical Transaction intact. An Unpaid, Paid, or Void bill is retained as an audit record. Removing the underlying raw Bulk evidence is blocked while any retained bill relies on it; the user must discard an eligible Review-stage bill before using Bulk Import's normal source-deletion workflow.

## User experience

### Account detail: Balance

Every Account detail view gains a **Balance** section.

The Account directory exposes this section through an icon action beside the Account row's edit, archive, and expand controls. Its tooltip and accessible name describe **balance and history**; it is not labelled as a live bank balance.

- Unconfigured Accounts show **Opening balance not set** and an action to set it.
- The form collects an as-of date/time and one or more currency/major-unit amounts.
- The form adds/removes currencies, validates exact minor-unit conversion, and clearly labels a permitted negative Bank balance as an overdraft.
- Liability Accounts label values as **amount owed**.
- A configured baseline displays its values, as-of time, correction history, and **Correct opening balance** action.
- The current calculated balance is shown only when a baseline is configured. Its calculation explains which post-baseline confirmed transactions are included.

### Credit Card workspace

The main sidebar exposes a **Credit Card** workspace under Transactions. Every active Credit Card Account is shown as an expanded group by default, including an empty state when that Account has no bills.

- Empty state explains that bills are created by processing a Credit Card bill in Bulk Import and links to that page; it contains no uploader.
- The list shows period, statement date, due date, amount due, status, and linked payoff when present.
- A bill detail view shows the original evidence link, summary, imported activity lines, unresolved lines, and payment match.
- A linked line opens its Transaction detail. Transaction detail shows a link back to its bill and line.
- An Unpaid bill offers review of any Bank-debit suggestion and **Pay in full**; Paid/Void bills do not.
- All destructive operations and financial corrections require confirmation.
- Credit Card is the only bill-review surface; Bulk Import may deep-link to the exact generated bill but does not duplicate reconciliation controls.

### Calculation treatment controls

Transaction detail exposes the effective spending treatment and its source:

- System-owned Credit Card payoff exclusions are read-only and explain why they are excluded.
- A user can choose Transaction total, Line items, or Exclude from spending only where validation permits it.
- A change shows its effect on spending only; Account balance and source evidence remain unchanged.

## Loading, empty, error, and success states

All new views must include accessible:

- initial loading, refresh, and retry states;
- no-baseline, no-bill, and no-bulk-document empty states;
- validation for invalid date, currency, amount, negative amount, ineligible line item, inactive Account, duplicate bill, and wrong payment Account/currency/amount;
- clear success confirmation for initial balance save, correction, automatic bill creation, reconciliation action, payment completion, and payoff;
- owner-safe errors for unavailable Bulk evidence, source deletion conflict, stale baseline correction, unresolved bill line, duplicate link, concurrent payoff, or failed internal transfer; and
- responsive, keyboard-accessible tabs, tables, dialogs, transactions links, and status changes.

## Out of scope

- Automatically treating existing Accounts as having a zero opening balance.
- Exchange-rate conversion, multi-currency statement settlement, or aggregation across currencies.
- Negative balances for non-Bank asset Accounts, Credit Card credit balances, or loan overpayments.
- Editing a past Transaction to correct an opening baseline.
- A statement amount, previous balance, minimum payment, or due amount as a new canonical Transaction.
- Partial, split, external, cash, or non-Bank Credit Card payments.
- Automatic statement closure, billing-cycle scheduling, interest calculation, fees, minimum-payment calculation, or delinquency management.
- Automatically attaching ambiguous bill lines or completing a Bank-debit-only payment suggestion without confirmation.
- New upload, Storage, parser, model, job, or source-deletion infrastructure outside the Bulk Insert integration contract.
- Public sharing, multi-user collaboration, public registration, or OAuth sign-in.

## Acceptance criteria

| Acceptance criterion | Status |
| --- | --- |
| Every existing Account starts unconfigured rather than with an invented numeric balance; a user can explicitly save zero. | Implemented |
| A user can maintain exact multi-currency opening balances and corrections without creating Transactions. | Implemented |
| Only Bank Accounts allow negative opening amounts, and only as an explicit overdraft. | Implemented |
| Account balances include post-baseline confirmed transfer legs, while Card payoff transfers remain excluded from spending. | Implemented |
| A complete line-item set replaces—not supplements—the transaction header in spending calculations. | Implemented |
| A Credit Card bill is uploaded only through Bulk Import; its processor automatically creates one bill without a second upload, parse, candidate, or source workflow. | Implemented |
| Credit Card is directly discoverable from the main sidebar and groups every active Card's bills, expanded by default and newest first. | Implemented |
| Once generated, a bill is the reconciliation source of truth: every canonical Transaction link or safe creation starts from one of its projected document lines. | Implemented |
| A bill line and a canonical Credit Card Transaction can link only one-to-one, with two-way navigation. | Implemented |
| A safe missing Card transaction can be created exactly once from an evidence-backed Bulk candidate; ambiguous lines remain in Review. | Implemented |
| A unique exact existing Bank-to-Card transfer marks the bill Paid; no match marks it Unpaid; ambiguous matches require explicit user selection in Review. | Implemented |
| A confirmed Bank-debit-only suggestion creates only the missing Card credit leg and links both transactions as one internal transfer. | Implemented |
| A user can pay an Unpaid bill in full only through an owned active Bank → Credit Card internal transfer of the exact due amount/currency. | Implemented |
| The bill, its summary values, and its payoff link never add duplicate spending or Account-balance effects. | Implemented |
| All financial records, Bulk evidence, bill links, corrections, and calculation treatments remain owner-scoped and auditable. | Implemented |

See the [technical implementation](technical.md) for the database contract, security model, API surface, Bulk Import processor integration, migration history, and verification record.

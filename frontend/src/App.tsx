import { lazy, Suspense, type FormEvent, useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { Session } from "@supabase/supabase-js";
import {
  ArchiveRestore,
  ArrowLeftRight,
  Bell,
  ChartColumn,
  ChevronDown,
  CircleAlert,
  CircleHelp,
  Eye,
  EyeOff,
  Landmark,
  LayoutDashboard,
  LogOut,
  PanelLeft,
  Pencil,
  Plus,
  Search,
  Settings,
  SlidersHorizontal,
  Sparkles,
  Trash2,
  WalletCards,
  X,
} from "lucide-react";
import {
  accountTypeLabel,
  accountTypesForSide,
  isJsonObject,
  type Account,
  type AccountSide,
  type AccountType,
  type DeletedFilter,
  type SortOption,
} from "./features/accounts/model";
import {
  buildMetadata,
  metadataEntries,
  validateAccountDraft,
  type AccountDraft,
  type MetadataEntry,
} from "./features/accounts/validation";
import { isSupabaseConfigured, supabase } from "./lib/supabase";
import "./App.css";

const TransactionsPage = lazy(() =>
  import("./features/transactions/TransactionsPage").then(({ TransactionsPage: Page }) => ({
    default: Page,
  })),
);

type WorkspacePage = "accounts" | "transactions";

const newDraft = (): AccountDraft => ({
  side: "asset",
  account_type: "bank_account",
  name: "",
  institution_name: "",
  account_identifier: "",
  notes: "",
  sort_order: 0,
  metadataEntries: [],
});

function LoginPage({ onSignedIn }: { onSignedIn: (session: Session) => void }) {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [showPassword, setShowPassword] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [isSubmitting, setIsSubmitting] = useState(false);
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError(null);
    setIsSubmitting(true);
    const { data, error: authError } = await supabase.auth.signInWithPassword({
      email,
      password,
    });
    setIsSubmitting(false);
    if (authError) setError(authError.message);
    else if (data.session) onSignedIn(data.session);
  }
  return (
    <main className="auth-shell">
      <section className="auth-card" aria-labelledby="sign-in-title">
        <p className="eyebrow">WEALTH BUILDER</p>
        <h1 id="sign-in-title">Welcome back</h1>
        <p className="muted">
          Sign in to manage your private finances.
        </p>
        <form onSubmit={submit} className="form-stack">
          <label>
            Email
            <input
              autoComplete="email"
              type="email"
              value={email}
              onChange={(event) => setEmail(event.target.value)}
              required
            />
          </label>
          <label>
            Password
            <span className="password-field">
              <input
                autoComplete="current-password"
                type={showPassword ? "text" : "password"}
                value={password}
                onChange={(event) => setPassword(event.target.value)}
                required
              />
              <button
                className="password-visibility"
                type="button"
                onClick={() => setShowPassword((visible) => !visible)}
                aria-label={showPassword ? "Hide password" : "Show password"}
                aria-pressed={showPassword}
              >
                {showPassword ? (
                  <EyeOff size={18} aria-hidden="true" />
                ) : (
                  <Eye size={18} aria-hidden="true" />
                )}
              </button>
            </span>
          </label>
          {error && (
            <p className="form-error" role="alert">
              {error}
            </p>
          )}
          <button
            className="button button-primary"
            disabled={isSubmitting}
            type="submit"
          >
            {isSubmitting ? "Signing in…" : "Sign in"}
          </button>
        </form>
        <p className="helper-text">
          Accounts are provisioned by the product owner. Registration is not
          available here.
        </p>
      </section>
    </main>
  );
}

function AccountForm({
  account,
  close,
  saved,
}: {
  account: Account | null;
  close: () => void;
  saved: () => Promise<void>;
}) {
  const [draft, setDraft] = useState<AccountDraft>(() =>
    account
      ? {
          side: account.side,
          account_type: account.account_type,
          name: account.name,
          institution_name: account.institution_name,
          account_identifier: account.account_identifier ?? "",
          notes: account.notes ?? "",
          sort_order: account.sort_order,
          metadataEntries: metadataEntries(account.metadata),
        }
      : newDraft(),
  );
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [requestError, setRequestError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const changeMetadata = (
    index: number,
    field: keyof MetadataEntry,
    value: string,
  ) =>
    setDraft((current) => ({
      ...current,
      metadataEntries: current.metadataEntries.map((entry, itemIndex) =>
        itemIndex === index ? { ...entry, [field]: value } : entry,
      ),
    }));
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const fieldErrors = validateAccountDraft(draft);
    setErrors(fieldErrors);
    if (Object.keys(fieldErrors).length) return;
    setSaving(true);
    setRequestError(null);
    const values = {
      side: draft.side,
      account_type: draft.account_type,
      name: draft.name.trim(),
      institution_name: draft.institution_name.trim(),
      account_identifier: draft.account_identifier.trim() || null,
      notes: draft.notes.trim() || null,
      metadata: buildMetadata(draft.metadataEntries),
      sort_order: draft.sort_order,
    };
    const response = account
      ? await supabase.from("accounts").update(values).eq("id", account.id)
      : await supabase.from("accounts").insert({
          ...values,
          user_id: (await supabase.auth.getUser()).data.user?.id ?? "",
        });
    setSaving(false);
    if (response.error) {
      setRequestError(response.error.message);
      return;
    }
    await saved();
    close();
  }
  return (
    <div className="modal-backdrop" role="presentation" onMouseDown={close}>
      <section
        className="modal"
        aria-modal="true"
        aria-labelledby="account-form-title"
        role="dialog"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <div className="modal-header">
          <div>
            <p className="eyebrow">ACCOUNT DIRECTORY</p>
            <h2 id="account-form-title">
              {account ? "Edit account" : "Add account"}
            </h2>
          </div>
          <button
            className="icon-button"
            onClick={close}
            aria-label="Close account form"
          >
            <X size={20} />
          </button>
        </div>
        <form className="account-form" onSubmit={submit}>
          <div className="form-grid">
            <label>
              Side
              <select
                value={draft.side}
                onChange={(event) =>
                  setDraft((current) => {
                    const side = event.target.value as AccountSide;
                    return {
                      ...current,
                      side,
                      account_type: accountTypesForSide(side)[0].value,
                    };
                  })
                }
              >
                <option value="asset">Asset</option>
                <option value="liability">Liability</option>
              </select>
            </label>
            <label>
              Account type
              <select
                value={draft.account_type}
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    account_type: event.target.value as AccountType,
                  }))
                }
              >
                {accountTypesForSide(draft.side).map((item) => (
                  <option key={item.value} value={item.value}>
                    {item.label}
                  </option>
                ))}
              </select>
              {errors.account_type && (
                <span className="field-error">{errors.account_type}</span>
              )}
            </label>
          </div>
          <label>
            Account name
            <input
              value={draft.name}
              onChange={(event) =>
                setDraft((current) => ({
                  ...current,
                  name: event.target.value,
                }))
              }
              maxLength={100}
              aria-invalid={Boolean(errors.name)}
              required
            />
            {errors.name && <span className="field-error">{errors.name}</span>}
          </label>
          <label>
            Institution or platform
            <input
              value={draft.institution_name}
              onChange={(event) =>
                setDraft((current) => ({
                  ...current,
                  institution_name: event.target.value,
                }))
              }
              maxLength={100}
              aria-invalid={Boolean(errors.institution_name)}
              required
              placeholder="e.g. DBS, Interactive Brokers, MetaMask"
            />
            {errors.institution_name && (
              <span className="field-error">{errors.institution_name}</span>
            )}
          </label>
          <label>
            Account identification <span className="optional">Optional</span>
            <input
              value={draft.account_identifier}
              onChange={(event) =>
                setDraft((current) => ({
                  ...current,
                  account_identifier: event.target.value,
                }))
              }
              maxLength={100}
              placeholder="A safe reference"
            />
          </label>
          <label>
            Notes <span className="optional">Optional</span>
            <textarea
              value={draft.notes}
              onChange={(event) =>
                setDraft((current) => ({
                  ...current,
                  notes: event.target.value,
                }))
              }
              maxLength={500}
              rows={3}
            />
          </label>
          <fieldset className="metadata-editor">
            <legend>
              Custom metadata <span className="optional">Optional</span>
            </legend>
            <p>
              Store safe descriptive details as text. Never add passwords, card
              numbers, private keys, or seed phrases.
            </p>
            {draft.metadataEntries.map((entry, index) => (
              <div className="metadata-row" key={index}>
                <input
                  aria-label={`Metadata key ${index + 1}`}
                  placeholder="Field"
                  value={entry.key}
                  onChange={(event) =>
                    changeMetadata(index, "key", event.target.value)
                  }
                />
                <input
                  aria-label={`Metadata value ${index + 1}`}
                  placeholder="Value"
                  value={entry.value}
                  onChange={(event) =>
                    changeMetadata(index, "value", event.target.value)
                  }
                />
                <button
                  type="button"
                  className="icon-button"
                  onClick={() =>
                    setDraft((current) => ({
                      ...current,
                      metadataEntries: current.metadataEntries.filter(
                        (_, itemIndex) => itemIndex !== index,
                      ),
                    }))
                  }
                  aria-label={`Remove metadata row ${index + 1}`}
                >
                  <X size={18} />
                </button>
              </div>
            ))}
            {errors.metadata && (
              <span className="field-error">{errors.metadata}</span>
            )}
            <button
              type="button"
              className="text-button"
              onClick={() =>
                setDraft((current) => ({
                  ...current,
                  metadataEntries: [
                    ...current.metadataEntries,
                    { key: "", value: "" },
                  ],
                }))
              }
            >
              + Add metadata field
            </button>
          </fieldset>
          {requestError && (
            <p className="form-error" role="alert">
              {requestError}
            </p>
          )}
          <div className="modal-actions">
            <button
              type="button"
              className="button button-secondary"
              onClick={close}
            >
              Cancel
            </button>
            <button
              type="submit"
              className="button button-primary"
              disabled={saving}
            >
              {saving ? "Saving…" : account ? "Save changes" : "Add account"}
            </button>
          </div>
        </form>
      </section>
    </div>
  );
}

const navigation = [
  { label: "Dashboard", icon: LayoutDashboard },
  { label: "Accounts", icon: Landmark, page: "accounts" as const },
  { label: "Transactions", icon: ArrowLeftRight, page: "transactions" as const },
  { label: "Investments", icon: ChartColumn },
  { label: "Goals", icon: WalletCards },
];

function SideNav({
  email,
  signOut,
  activePage,
  onNavigate,
  mobileOpen,
  onMobileClose,
}: {
  email: string | undefined;
  signOut: () => Promise<void>;
  activePage: WorkspacePage;
  onNavigate: (page: WorkspacePage) => void;
  mobileOpen: boolean;
  onMobileClose: () => void;
}) {
  return (
    <aside
      aria-label="Workspace navigation"
      className={`side-nav${mobileOpen ? " mobile-open" : ""}`}
      id="workspace-navigation"
    >
      <div className="brand">
        <span className="brand-mark">W</span>
        <span>Wealth Builder</span>
        <button
          aria-label="Close navigation"
          className="icon-button mobile-nav-close"
          onClick={onMobileClose}
          type="button"
        >
          <X aria-hidden="true" size={18} />
        </button>
      </div>
      <nav aria-label="Primary navigation">
        {navigation.map(({ label, icon: Icon, page }) => {
          const active = page === activePage;
          return (
          <button
            aria-label={label}
            key={label}
            type="button"
            className={`nav-item${active ? " active" : ""}`}
            aria-current={active ? "page" : undefined}
            disabled={!page}
            onClick={page ? () => {
              onNavigate(page);
              onMobileClose();
            } : undefined}
            title={page ? label : `${label} (coming soon)`}
          >
            <Icon aria-hidden="true" size={19} />
            <span>{label}</span>
            {!page && <small>Soon</small>}
          </button>
          );
        })}
      </nav>
      <div className="nav-bottom">
        <button aria-label="AI assistant (coming soon)" className="nav-item" type="button" disabled>
          <Sparkles aria-hidden="true" size={19} />
          <span>AI assistant</span>
          <small>Soon</small>
        </button>
        <button aria-label="Help and support (coming soon)" className="nav-item" type="button" disabled>
          <CircleHelp aria-hidden="true" size={19} />
          <span>Help & support</span>
        </button>
        <div className="nav-user">
          <span>{email?.slice(0, 1).toUpperCase() ?? "U"}</span>
          <div>
            <strong>{email?.split("@")[0] ?? "Your account"}</strong>
            <button type="button" onClick={() => void signOut()}>
              <LogOut size={15} /> Sign out
            </button>
          </div>
        </div>
      </div>
    </aside>
  );
}

function AccountsPage({
  session,
  signOut,
  activePage,
  onNavigate,
}: {
  session: Session;
  signOut: () => Promise<void>;
  activePage: WorkspacePage;
  onNavigate: (page: WorkspacePage) => void;
}) {
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [search, setSearch] = useState("");
  const [side, setSide] = useState<"all" | AccountSide>("all");
  const [type, setType] = useState<"all" | AccountType>("all");
  const [institution, setInstitution] = useState("");
  const [deletedFilter, setDeletedFilter] = useState<DeletedFilter>("active");
  const [sort, setSort] = useState<SortOption>("name_asc");
  const [editing, setEditing] = useState<Account | null | undefined>(undefined);
  const [expandedAccountIds, setExpandedAccountIds] = useState<string[]>([]);
  const [mobileNavOpen, setMobileNavOpen] = useState(false);
  const mobileNavToggleRef = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (!mobileNavOpen) return;
    const mobileNavToggle = mobileNavToggleRef.current;
    const appMain = document.querySelector<HTMLElement>(".app-main");
    const wasInert = appMain?.inert ?? false;
    if (appMain) appMain.inert = true;
    const frame = window.requestAnimationFrame(() => {
      document.querySelector<HTMLElement>("#workspace-navigation .mobile-nav-close")?.focus();
    });
    const handleNavigationKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        setMobileNavOpen(false);
        return;
      }
      if (event.key !== "Tab") return;
      const navigation = document.getElementById("workspace-navigation");
      const focusable = navigation
        ? [...navigation.querySelectorAll<HTMLElement>("button:not([disabled]), a[href]")]
        : [];
      if (focusable.length === 0) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };
    document.addEventListener("keydown", handleNavigationKey);
    return () => {
      window.cancelAnimationFrame(frame);
      document.removeEventListener("keydown", handleNavigationKey);
      if (appMain) appMain.inert = wasInert;
      mobileNavToggle?.focus();
    };
  }, [mobileNavOpen]);
  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    let query = supabase.from("accounts").select("*");
    if (deletedFilter === "active") query = query.is("deleted_at", null);
    if (deletedFilter === "deleted")
      query = query.not("deleted_at", "is", null);
    const { data, error: loadError } = await query
      .order("sort_order")
      .order("created_at");
    if (loadError) setError(loadError.message);
    else setAccounts(data);
    setLoading(false);
  }, [deletedFilter]);
  useEffect(() => {
    const timer = window.setTimeout(() => void load(), 0);
    return () => window.clearTimeout(timer);
  }, [load]);
  const institutions = useMemo(
    () =>
      [...new Set(accounts.map((account) => account.institution_name))].sort(
        (a, b) => a.localeCompare(b),
      ),
    [accounts],
  );
  const visible = useMemo(
    () =>
      accounts
        .filter((account) => {
          const query = search.trim().toLowerCase();
          return (
            (!query ||
              account.name.toLowerCase().includes(query) ||
              account.institution_name.toLowerCase().includes(query)) &&
            (side === "all" || account.side === side) &&
            (type === "all" || account.account_type === type) &&
            (!institution || account.institution_name === institution)
          );
        })
        .sort((a, b) =>
          sort === "name_asc"
            ? a.name.localeCompare(b.name)
            : sort === "name_desc"
              ? b.name.localeCompare(a.name)
              : a.sort_order - b.sort_order ||
                a.created_at.localeCompare(b.created_at),
        ),
    [accounts, institution, search, side, sort, type],
  );
  const assets = visible.filter((account) => account.side === "asset");
  const liabilities = visible.filter((account) => account.side === "liability");
  const hasFilters = Boolean(
    search ||
      side !== "all" ||
      type !== "all" ||
      institution ||
      sort !== "name_asc",
  );
  const clearFilters = () => {
    setSearch("");
    setSide("all");
    setType("all");
    setInstitution("");
    setSort("name_asc");
  };
  async function archive(account: Account, isDeleted: boolean) {
    const { error: updateError } = await supabase
      .from("accounts")
      .update({ deleted_at: isDeleted ? new Date().toISOString() : null })
      .eq("id", account.id);
    if (updateError) setError(updateError.message);
    else await load();
  }
  const toggleExpanded = (accountId: string) =>
    setExpandedAccountIds((ids) =>
      ids.includes(accountId)
        ? ids.filter((id) => id !== accountId)
        : [...ids, accountId],
    );
  const group = (title: string, entries: Account[]) =>
    entries.length ? (
      <section className="account-section" aria-labelledby={`${title}-heading`}>
        <div className="section-heading">
          <div>
            <p className="eyebrow">{title.toUpperCase()}</p>
            <h2 id={`${title}-heading`}>{title}</h2>
          </div>
          <span>
            {entries.length} {entries.length === 1 ? "account" : "accounts"}
          </span>
        </div>
        <div className="account-list">
          {entries.map((account) => {
            const isExpanded = expandedAccountIds.includes(account.id);
            const metadata = isJsonObject(account.metadata)
              ? Object.entries(account.metadata)
              : [];
            return (
              <article
                className={`account-row${isExpanded ? " expanded" : ""}`}
                key={account.id}
              >
                <div className="account-row-main">
                  <div className={`account-mark ${account.side}`}>
                    {account.name.slice(0, 1).toUpperCase()}
                  </div>
                  <div className="account-identity">
                    <h3>{account.name}</h3>
                    <p>
                      {accountTypeLabel(account.account_type)} ·{" "}
                      {account.institution_name}
                    </p>
                    {account.deleted_at && (
                      <span className="archived-label">Archived</span>
                    )}
                  </div>
                  <div className="row-actions">
                    <button
                      className="icon-button"
                      onClick={() => setEditing(account)}
                      aria-label={`Edit ${account.name}`}
                    >
                      <Pencil size={18} />
                    </button>
                    {account.deleted_at ? (
                      <button
                        className="icon-button"
                        onClick={() => void archive(account, false)}
                        aria-label={`Restore ${account.name}`}
                      >
                        <ArchiveRestore size={18} />
                      </button>
                    ) : (
                      <button
                        className="icon-button danger-button"
                        onClick={() => void archive(account, true)}
                        aria-label={`Archive ${account.name}`}
                      >
                        <Trash2 size={18} />
                      </button>
                    )}
                    <button
                      className="icon-button account-expand"
                      type="button"
                      onClick={() => toggleExpanded(account.id)}
                      aria-label={`${isExpanded ? "Hide" : "Show"} details for ${account.name}`}
                      aria-controls={`account-details-${account.id}`}
                      aria-expanded={isExpanded}
                    >
                      <ChevronDown size={18} />
                    </button>
                  </div>
                </div>
                {isExpanded && (
                  <div
                    className="account-details"
                    id={`account-details-${account.id}`}
                  >
                    {metadata.length ? (
                      metadata.map(([key, value]) => (
                        <div className="metadata-pair" key={key}>
                          <dt>{key}</dt>
                          <dd>
                            {typeof value === "string"
                              ? value
                              : JSON.stringify(value)}
                          </dd>
                        </div>
                      ))
                    ) : (
                      <p className="metadata-empty">
                        No custom metadata has been added.
                      </p>
                    )}
                  </div>
                )}
              </article>
            );
          })}
        </div>
      </section>
    ) : null;
  return (
    <div className="app-layout">
      <SideNav
        activePage={activePage}
        email={session.user.email}
        mobileOpen={mobileNavOpen}
        onNavigate={onNavigate}
        onMobileClose={() => setMobileNavOpen(false)}
        signOut={signOut}
      />
      {mobileNavOpen && (
        <button
          aria-label="Close navigation"
          className="mobile-nav-backdrop"
          onClick={() => setMobileNavOpen(false)}
          type="button"
        />
      )}
      <div className="app-main">
        <header className="top-bar">
          <div className="top-bar-title">
            <button
              aria-controls="workspace-navigation"
              aria-expanded={mobileNavOpen}
              aria-label={mobileNavOpen ? "Close navigation" : "Open navigation"}
              className="top-icon"
              onClick={() => setMobileNavOpen((open) => !open)}
              ref={mobileNavToggleRef}
              type="button"
            >
              <PanelLeft size={20} />
            </button>
            <span>Personal finance</span>
          </div>
          <div className="top-bar-actions">
            <button
              className="top-icon"
              type="button"
              aria-label="Notifications"
            >
              <Bell size={19} />
            </button>
            <button className="top-icon" type="button" aria-label="Settings">
              <Settings size={19} />
            </button>
            <span className="top-avatar">
              {session.user.email?.slice(0, 1).toUpperCase() ?? "U"}
            </span>
          </div>
        </header>
        <main className="app-shell">
          {activePage === "transactions" ? (
            <Suspense
              fallback={(
                <section aria-busy="true" aria-label="Loading transactions" className="transaction-panel" role="status">
                  <span className="sr-only">Loading transactions…</span>
                  <div className="skeleton-row" />
                  <div className="skeleton-row" />
                  <div className="skeleton-row" />
                </section>
              )}
            >
              <TransactionsPage session={session} />
            </Suspense>
          ) : (
            <>
          <header className="page-header">
            <div>
              <p className="eyebrow">ACCOUNT DIRECTORY</p>
              <h1>Accounts</h1>
              <p className="muted">
                Your private account directory. No balances or financial data
                are stored here.
              </p>
            </div>
            <button
              className="button button-primary"
              onClick={() => setEditing(null)}
            >
              <Plus size={18} /> Add account
            </button>
          </header>
          <section className="toolbar" aria-label="Account controls">
            <label className="search-field">
              <Search size={18} />
              <span className="sr-only">Search accounts</span>
              <input
                value={search}
                onChange={(event) => setSearch(event.target.value)}
                placeholder="Search name or institution"
              />
            </label>
            <label className="select-field">
              <SlidersHorizontal size={17} />
              <span className="sr-only">Side</span>
              <select
                value={side}
                onChange={(event) => {
                  setSide(event.target.value as "all" | AccountSide);
                  setType("all");
                }}
              >
                <option value="all">All sides</option>
                <option value="asset">Assets</option>
                <option value="liability">Liabilities</option>
              </select>
            </label>
            <label className="select-field">
              <span className="sr-only">Account type</span>
              <select
                value={type}
                onChange={(event) =>
                  setType(event.target.value as "all" | AccountType)
                }
              >
                <option value="all">All types</option>
                {(side === "all"
                  ? [
                      ...accountTypesForSide("asset"),
                      ...accountTypesForSide("liability"),
                    ]
                  : accountTypesForSide(side)
                ).map((item) => (
                  <option key={item.value} value={item.value}>
                    {item.label}
                  </option>
                ))}
              </select>
              <ChevronDown size={17} />
            </label>
            <label className="select-field">
              <span className="sr-only">Institution</span>
              <select
                value={institution}
                onChange={(event) => setInstitution(event.target.value)}
              >
                <option value="">All institutions</option>
                {institutions.map((item) => (
                  <option key={item} value={item}>
                    {item}
                  </option>
                ))}
              </select>
              <ChevronDown size={17} />
            </label>
            <label className="select-field">
              <span className="sr-only">Status</span>
              <select
                value={deletedFilter}
                onChange={(event) =>
                  setDeletedFilter(event.target.value as DeletedFilter)
                }
              >
                <option value="active">Active</option>
                <option value="deleted">Archived</option>
                <option value="all">All status</option>
              </select>
              <ChevronDown size={17} />
            </label>
            <label className="select-field">
              <span className="sr-only">Sort accounts</span>
              <select
                value={sort}
                onChange={(event) => setSort(event.target.value as SortOption)}
              >
                <option value="name_asc">Name A–Z</option>
                <option value="name_desc">Name Z–A</option>
              </select>
              <ChevronDown size={17} />
            </label>
          </section>
          {hasFilters && (
            <button
              className="text-button clear-filters"
              onClick={clearFilters}
            >
              Clear filters
            </button>
          )}
          {error && (
            <section className="notice notice-error" role="alert">
              <CircleAlert size={20} />
              <div>
                <strong>Couldn’t load accounts.</strong>
                <p>{error}</p>
              </div>
              <button
                className="button button-secondary"
                onClick={() => void load()}
              >
                Retry
              </button>
            </section>
          )}
          {loading ? (
            <section className="account-section">
              <div className="skeleton-row" />
              <div className="skeleton-row" />
              <div className="skeleton-row" />
            </section>
          ) : visible.length === 0 ? (
            <section className="empty-state">
              <h2>
                {accounts.length === 0
                  ? "Create your first account"
                  : "No accounts match these filters"}
              </h2>
              <p>
                {accounts.length === 0
                  ? "Start with an institution, account type, and a name. Financial balances stay out of this feature."
                  : "Adjust your filters or clear them to see more accounts."}
              </p>
              {accounts.length === 0 ? (
                <button
                  className="button button-primary"
                  onClick={() => setEditing(null)}
                >
                  <Plus size={18} /> Add account
                </button>
              ) : (
                <button
                  className="button button-secondary"
                  onClick={clearFilters}
                >
                  Clear filters
                </button>
              )}
            </section>
          ) : (
            <div className="account-groups">
              {group("Assets", assets)}
              {group("Liabilities", liabilities)}
            </div>
          )}
          {editing !== undefined && (
            <AccountForm
              account={editing}
              close={() => setEditing(undefined)}
              saved={load}
            />
          )}
            </>
          )}
        </main>
        <footer className="app-footer">
          © {new Date().getFullYear()} Wealth Builder. All rights reserved.
        </footer>
      </div>
    </div>
  );
}

function workspacePageFromLocation(): WorkspacePage {
  const parameters = new URL(window.location.href).searchParams;
  if (parameters.get("gmail")) return "transactions";
  return parameters.get("page") === "transactions" ? "transactions" : "accounts";
}

export default function App() {
  const [session, setSession] = useState<Session | null>(null);
  const [loading, setLoading] = useState(true);
  const [activePage, setActivePage] = useState<WorkspacePage>(workspacePageFromLocation);
  useEffect(() => {
    void supabase.auth.getSession().then(({ data }) => {
      setSession(data.session);
      setLoading(false);
    });
    const { data: listener } = supabase.auth.onAuthStateChange(
      (_event, nextSession) => setSession(nextSession),
    );
    return () => listener.subscription.unsubscribe();
  }, []);
  useEffect(() => {
    const onPopState = () => setActivePage(workspacePageFromLocation());
    window.addEventListener("popstate", onPopState);
    return () => window.removeEventListener("popstate", onPopState);
  }, []);
  const navigate = (page: WorkspacePage) => {
    setActivePage(page);
    const url = new URL(window.location.href);
    url.searchParams.set("page", page);
    url.searchParams.delete("gmail");
    window.history.pushState({}, "", url);
  };
  if (!isSupabaseConfigured)
    return (
      <main className="auth-shell">
        <section className="auth-card">
          <p className="eyebrow">SETUP REQUIRED</p>
          <h1>Connect Supabase</h1>
          <p className="muted">
            Copy <code>.env.example</code> to <code>.env.local</code>, then
            provide the project URL and publishable key. No secret or service
            role key belongs in this frontend.
          </p>
        </section>
      </main>
    );
  if (loading)
    return (
      <main className="auth-shell">
        <p className="muted">Loading your secure session…</p>
      </main>
    );
  return session ? (
    <AccountsPage
      activePage={activePage}
      onNavigate={navigate}
      session={session}
      signOut={() => supabase.auth.signOut().then(() => undefined)}
    />
  ) : (
    <LoginPage onSignedIn={setSession} />
  );
}

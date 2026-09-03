import { useCallback, useEffect, useMemo, useState, type FormEvent } from "react";
import type { Session } from "@supabase/supabase-js";
import {
  CircleAlert,
  KeyRound,
  Pencil,
  Plus,
  RefreshCw,
  RotateCcw,
  Save,
  SlidersHorizontal,
  Trash2,
} from "lucide-react";
import {
  createAccountMatchingKey,
  createSourceParserRule,
  getTransactionSettings,
  listOwnedAccounts,
  putDefaultParserInstructions,
  retireSourceParserRule,
  setAccountMatchingKeyActive,
  updateSourceParserRule,
} from "./api";
import type {
  AccountMatchingKey,
  MatchingKeyType,
  OwnedAccountOption,
  SenderMatchType,
  SourceParserRule,
  SourceParserRuleInput,
  TransactionSettings,
} from "./model";

const maximumInstructionsLength = 4000;

interface RuleDraft {
  name: string;
  senderMatchType: SenderMatchType;
  senderMatchValue: string;
  subjectMatcher: string;
  contentMatcher: string;
  promptFragment: string;
  priority: string;
  active: boolean;
}

const emptyRuleDraft = (): RuleDraft => ({
  name: "",
  senderMatchType: "exact",
  senderMatchValue: "",
  subjectMatcher: "",
  contentMatcher: "",
  promptFragment: "",
  priority: "100",
  active: true,
});

function ruleDraft(rule: SourceParserRule): RuleDraft {
  return {
    name: rule.name,
    senderMatchType: rule.sender_match_type,
    senderMatchValue: rule.sender_match_value,
    subjectMatcher: rule.subject_matcher ?? "",
    contentMatcher: rule.content_matcher ?? "",
    promptFragment: rule.prompt_fragment,
    priority: String(rule.priority),
    active: rule.active,
  };
}

function nullableTrimmed(value: string): string | null {
  const result = value.trim();
  return result === "" ? null : result;
}

function matchingKeyLabel(type: MatchingKeyType): string {
  return type === "card_last_four" ? "Card last four" : "Bank account suffix";
}

function senderMatchLabel(type: SenderMatchType): string {
  if (type === "exact") return "Exact sender";
  if (type === "domain") return "Sender domain";
  return "Advanced RE2";
}

function replaceByID<T extends { id: string }>(items: T[], replacement: T): T[] {
  return items.map((item) => (item.id === replacement.id ? replacement : item));
}

export function TransactionSettingsPage({ session }: { session: Session }) {
  const [settings, setSettings] = useState<TransactionSettings | null>(null);
  const [accounts, setAccounts] = useState<OwnedAccountOption[]>([]);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [success, setSuccess] = useState<string | null>(null);
  const [defaultInstructions, setDefaultInstructions] = useState("");
  const [instructionsSaving, setInstructionsSaving] = useState(false);
  const [instructionsError, setInstructionsError] = useState<string | null>(null);
  const [keyAccountID, setKeyAccountID] = useState("");
  const [keyType, setKeyType] = useState<MatchingKeyType>("card_last_four");
  const [keyValue, setKeyValue] = useState("");
  const [keySaving, setKeySaving] = useState(false);
  const [keyChangingID, setKeyChangingID] = useState<string | null>(null);
  const [keyError, setKeyError] = useState<string | null>(null);
  const [editingRuleID, setEditingRuleID] = useState<string | null>(null);
  const [ruleDraftValue, setRuleDraftValue] = useState<RuleDraft>(emptyRuleDraft);
  const [ruleSaving, setRuleSaving] = useState(false);
  const [ruleChangingID, setRuleChangingID] = useState<string | null>(null);
  const [ruleError, setRuleError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setLoadError(null);
    try {
      const [nextSettings, nextAccounts] = await Promise.all([
        getTransactionSettings(session),
        listOwnedAccounts(session),
      ]);
      setSettings(nextSettings);
      setAccounts(nextAccounts);
      setDefaultInstructions(nextSettings.default_instructions);
      setKeyAccountID((current) =>
        nextAccounts.some(({ id }) => id === current) ? current : (nextAccounts[0]?.id ?? ""),
      );
    } catch (error: unknown) {
      setLoadError(error instanceof Error ? error.message : "Couldn’t load transaction settings.");
    } finally {
      setLoading(false);
    }
  }, [session]);

  useEffect(() => {
    const controller = new AbortController();
    const timer = window.setTimeout(() => {
      setLoading(true);
      setLoadError(null);
      void Promise.all([
        getTransactionSettings(session, controller.signal),
        listOwnedAccounts(session, controller.signal),
      ])
        .then(([nextSettings, nextAccounts]) => {
          setSettings(nextSettings);
          setAccounts(nextAccounts);
          setDefaultInstructions(nextSettings.default_instructions);
          setKeyAccountID(nextAccounts[0]?.id ?? "");
        })
        .catch((error: unknown) => {
          if (!controller.signal.aborted) {
            setLoadError(
              error instanceof Error ? error.message : "Couldn’t load transaction settings.",
            );
          }
        })
        .finally(() => {
          if (!controller.signal.aborted) setLoading(false);
        });
    }, 0);
    return () => {
      window.clearTimeout(timer);
      controller.abort();
    };
  }, [session]);

  const keysByAccount = useMemo(() => {
    const groups = new Map<string, { name: string; keys: AccountMatchingKey[] }>();
    for (const key of settings?.matching_keys ?? []) {
      const group = groups.get(key.account_id);
      groups.set(key.account_id, {
        name: key.account_name,
        keys: [...(group?.keys ?? []), key],
      });
    }
    return [...groups.entries()].sort(([, left], [, right]) => left.name.localeCompare(right.name));
  }, [settings?.matching_keys]);

  function announce(message: string) {
    setSuccess(message);
    window.setTimeout(() => setSuccess((current) => (current === message ? null : current)), 5000);
  }

  async function saveDefaultInstructions(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setInstructionsError(null);
    if (defaultInstructions.length > maximumInstructionsLength) {
      setInstructionsError(`Instructions cannot exceed ${maximumInstructionsLength} characters.`);
      return;
    }
    setInstructionsSaving(true);
    try {
      const saved = await putDefaultParserInstructions(session, defaultInstructions);
      setDefaultInstructions(saved.default_instructions);
      setSettings((current) => current ? { ...current, ...saved } : current);
      announce("Default parsing instructions saved.");
    } catch (error: unknown) {
      setInstructionsError(
        error instanceof Error ? error.message : "Couldn’t save default parsing instructions.",
      );
    } finally {
      setInstructionsSaving(false);
    }
  }

  async function addMatchingKey(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setKeyError(null);
    const value = keyValue.trim();
    if (!accounts.some(({ id }) => id === keyAccountID)) {
      setKeyError("Choose an active account.");
      return;
    }
    if (keyType === "card_last_four" && !/^\d{4}$/.test(value)) {
      setKeyError("A card matching key must contain exactly four digits.");
      return;
    }
    if (keyType === "bank_account_suffix" && value === "") {
      setKeyError("Enter only the bank account suffix shown by the source.");
      return;
    }
    setKeySaving(true);
    try {
      const created = await createAccountMatchingKey(session, {
        account_id: keyAccountID,
        key_type: keyType,
        display_value: value,
      });
      setSettings((current) => current ? {
        ...current,
        matching_keys: [...current.matching_keys, created],
      } : current);
      setKeyValue("");
      announce(`Matching key added to ${created.account_name}.`);
    } catch (error: unknown) {
      setKeyError(error instanceof Error ? error.message : "Couldn’t add this matching key.");
    } finally {
      setKeySaving(false);
    }
  }

  async function changeMatchingKey(key: AccountMatchingKey) {
    setKeyChangingID(key.id);
    setKeyError(null);
    try {
      const updated = await setAccountMatchingKeyActive(session, key.id, !key.active);
      setSettings((current) => current ? {
        ...current,
        matching_keys: replaceByID(current.matching_keys, updated),
      } : current);
      announce(`${matchingKeyLabel(key.key_type)} ${key.active ? "retired" : "reactivated"}.`);
    } catch (error: unknown) {
      setKeyError(error instanceof Error ? error.message : "Couldn’t update this matching key.");
    } finally {
      setKeyChangingID(null);
    }
  }

  function beginRuleEdit(rule: SourceParserRule) {
    setEditingRuleID(rule.id);
    setRuleDraftValue(ruleDraft(rule));
    setRuleError(null);
    document.getElementById("source-rule-form")?.scrollIntoView({ behavior: "smooth" });
  }

  function cancelRuleEdit() {
    setEditingRuleID(null);
    setRuleDraftValue(emptyRuleDraft());
    setRuleError(null);
  }

  function validatedRuleInput(): SourceParserRuleInput | null {
    const priority = Number(ruleDraftValue.priority);
    if (!Number.isSafeInteger(priority) || priority < -2147483648 || priority > 2147483647) {
      setRuleError("Priority must be a whole number in the supported 32-bit range.");
      return null;
    }
    if (!ruleDraftValue.name.trim()) {
      setRuleError("Enter a rule name.");
      return null;
    }
    if (!ruleDraftValue.senderMatchValue.trim()) {
      setRuleError("Enter a sender value or RE2 pattern.");
      return null;
    }
    if (ruleDraftValue.promptFragment.length > maximumInstructionsLength) {
      setRuleError(`Rule instructions cannot exceed ${maximumInstructionsLength} characters.`);
      return null;
    }
    return {
      name: ruleDraftValue.name.trim(),
      provider: "gmail",
      sender_match_type: ruleDraftValue.senderMatchType,
      sender_match_value: ruleDraftValue.senderMatchValue.trim(),
      subject_matcher: nullableTrimmed(ruleDraftValue.subjectMatcher),
      content_matcher: nullableTrimmed(ruleDraftValue.contentMatcher),
      prompt_fragment: ruleDraftValue.promptFragment.trim(),
      priority,
      active: ruleDraftValue.active,
    };
  }

  async function saveRule(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setRuleError(null);
    const input = validatedRuleInput();
    if (!input) return;
    setRuleSaving(true);
    try {
      const saved = editingRuleID
        ? await updateSourceParserRule(session, editingRuleID, input)
        : await createSourceParserRule(session, input);
      setSettings((current) => current ? {
        ...current,
        source_rules: editingRuleID
          ? replaceByID(current.source_rules, saved)
          : [...current.source_rules, saved],
      } : current);
      announce(editingRuleID ? "Source rule updated." : "Source rule created.");
      cancelRuleEdit();
    } catch (error: unknown) {
      setRuleError(error instanceof Error ? error.message : "Couldn’t save this source rule.");
    } finally {
      setRuleSaving(false);
    }
  }

  async function changeRule(rule: SourceParserRule, active: boolean, retire = false) {
    setRuleChangingID(rule.id);
    setRuleError(null);
    try {
      if (retire) {
        await retireSourceParserRule(session, rule.id);
        setSettings((current) => current ? {
          ...current,
          source_rules: current.source_rules.map((item) =>
            item.id === rule.id
              ? { ...item, active: false, version: item.version + 1, updated_at: new Date().toISOString() }
              : item,
          ),
        } : current);
        announce("Source rule retired. Its historical parser audit remains available.");
      } else {
        const updated = await updateSourceParserRule(session, rule.id, {
          name: rule.name,
          provider: "gmail",
          sender_match_type: rule.sender_match_type,
          sender_match_value: rule.sender_match_value,
          subject_matcher: rule.subject_matcher,
          content_matcher: rule.content_matcher,
          prompt_fragment: rule.prompt_fragment,
          priority: rule.priority,
          active,
        });
        setSettings((current) => current ? {
          ...current,
          source_rules: replaceByID(current.source_rules, updated),
        } : current);
        announce(`Source rule ${active ? "reactivated" : "disabled"}.`);
      }
    } catch (error: unknown) {
      setRuleError(error instanceof Error ? error.message : "Couldn’t update this source rule.");
    } finally {
      setRuleChangingID(null);
    }
  }

  if (loading) {
    return (
      <section aria-busy="true" aria-label="Loading transaction settings" className="settings-loading" role="status">
        <span className="sr-only">Loading transaction settings…</span>
        <div className="skeleton-row" />
        <div className="skeleton-row" />
        <div className="skeleton-row" />
      </section>
    );
  }

  if (loadError || !settings) {
    return (
      <section className="empty-state transaction-empty" role="alert">
        <CircleAlert aria-hidden="true" size={30} />
        <h1>Couldn’t load transaction settings</h1>
        <p>{loadError ?? "The transaction settings response was unavailable."}</p>
        <button className="button button-secondary" onClick={() => void load()} type="button">
          <RefreshCw aria-hidden="true" size={17} /> Retry
        </button>
      </section>
    );
  }

  return (
    <div className="transaction-settings-page">
      <header className="page-header">
        <div>
          <p className="eyebrow">TRANSACTION CONFIGURATION</p>
          <h1>Settings</h1>
          <p className="muted">
            Control how private evidence is parsed and connected to your Accounts.
          </p>
        </div>
        <SlidersHorizontal aria-hidden="true" className="settings-page-icon" size={34} />
      </header>

      <div aria-live="polite" aria-atomic="true">
        {success && <p className="notice notice-success settings-success" role="status">{success}</p>}
      </div>

      <form className="settings-card" onSubmit={(event) => void saveDefaultInstructions(event)}>
        <div className="settings-card-heading">
          <div>
            <p className="eyebrow">DEFAULT PARSING</p>
            <h2>Instructions for every source</h2>
            <p>
              Appended after immutable platform safeguards and global guidance, then followed by any
              matching sender-specific rule. Personal instructions cannot replace the response
              schema, security controls, or source-only evidence requirements.
            </p>
          </div>
        </div>
        <label htmlFor="default-parsing-instructions">
          Default instructions
          <textarea
            id="default-parsing-instructions"
            maxLength={maximumInstructionsLength}
            onChange={(event) => setDefaultInstructions(event.target.value)}
            rows={8}
            value={defaultInstructions}
          />
        </label>
        <div className="settings-form-footer">
          <small>
            {defaultInstructions.length.toLocaleString()} / {maximumInstructionsLength.toLocaleString()} characters · {settings.default_instructions_version === 0 ? "Not customized yet" : `Saved version ${settings.default_instructions_version}`}
          </small>
          <button className="button button-primary" disabled={instructionsSaving} type="submit">
            <Save aria-hidden="true" size={17} />
            {instructionsSaving ? "Saving…" : "Save instructions"}
          </button>
        </div>
        {instructionsError && <p className="form-error" role="alert">{instructionsError}</p>}
      </form>

      <section aria-labelledby="matching-keys-title" className="settings-card">
        <div className="settings-card-heading">
          <div>
            <p className="eyebrow">ACCOUNT MATCHING</p>
            <h2 id="matching-keys-title">Matching keys</h2>
            <p>Map source-safe identifiers to one of your active Accounts.</p>
          </div>
          <KeyRound aria-hidden="true" size={22} />
        </div>

        {accounts.length === 0 ? (
          <div className="settings-empty" role="status">
            <strong>No active Accounts are available</strong>
            <p>Add or restore an Account before creating a matching key.</p>
          </div>
        ) : (
          <form className="matching-key-form" onSubmit={(event) => void addMatchingKey(event)}>
            <label>
              Account
              <select onChange={(event) => setKeyAccountID(event.target.value)} value={keyAccountID}>
                {accounts.map((account) => (
                  <option key={account.id} value={account.id}>
                    {account.name}{account.institution_name ? ` · ${account.institution_name}` : ""}
                  </option>
                ))}
              </select>
            </label>
            <label>
              Key type
              <select onChange={(event) => {
                setKeyType(event.target.value as MatchingKeyType);
                setKeyValue("");
                setKeyError(null);
              }} value={keyType}>
                <option value="card_last_four">Card last four</option>
                <option value="bank_account_suffix">Bank account suffix</option>
              </select>
            </label>
            <label>
              {keyType === "card_last_four" ? "Four digits" : "Account suffix"}
              <input
                autoComplete="off"
                inputMode={keyType === "card_last_four" ? "numeric" : "text"}
                maxLength={keyType === "card_last_four" ? 4 : 100}
                onChange={(event) => setKeyValue(event.target.value)}
                placeholder={keyType === "card_last_four" ? "2562" : "1818"}
                value={keyValue}
              />
              {keyType === "bank_account_suffix" && (
                <small>
                  Enter only the suffix itself, such as 1818. Include punctuation only when it is
                  part of the actual suffix; do not enter prose such as “Account ending.”
                </small>
              )}
            </label>
            <button className="button button-primary" disabled={keySaving} type="submit">
              <Plus aria-hidden="true" size={17} /> {keySaving ? "Adding…" : "Add key"}
            </button>
          </form>
        )}
        <p className="settings-caution">
          A key’s Account, type, and value are permanent for audit history. Values must be unique
          within each key type across your Accounts; retire or reactivate a key instead of replacing it.
        </p>
        {keyError && <p className="form-error" role="alert">{keyError}</p>}

        {settings.matching_keys.length === 0 ? (
          <div className="settings-empty" role="status">
            <strong>No matching keys yet</strong>
            <p>Sources without matching Account evidence stay safely unattached.</p>
          </div>
        ) : (
          <div className="matching-key-groups">
            {keysByAccount.map(([accountID, group]) => (
              <section aria-label={`${group.name} matching keys`} className="matching-key-group" key={accountID}>
                <h3>{group.name}</h3>
                <ul className="settings-list">
                  {group.keys.map((key) => (
                    <li className={!key.active ? "inactive" : undefined} key={key.id}>
                      <div>
                        <strong>{key.display_value}</strong>
                        <p>{matchingKeyLabel(key.key_type)} · {key.active ? "Active" : "Retired"}</p>
                      </div>
                      <button
                        className="button button-secondary button-compact"
                        disabled={keyChangingID === key.id}
                        onClick={() => void changeMatchingKey(key)}
                        type="button"
                      >
                        <RotateCcw aria-hidden="true" size={15} />
                        {keyChangingID === key.id ? "Saving…" : key.active ? "Retire" : "Reactivate"}
                      </button>
                    </li>
                  ))}
                </ul>
              </section>
            ))}
          </div>
        )}
      </section>

      <section aria-labelledby="source-rules-title" className="settings-card source-rules-card">
        <div className="settings-card-heading">
          <div>
            <p className="eyebrow">SENDER-SPECIFIC PARSING</p>
            <h2 id="source-rules-title">Source rules</h2>
            <p>Apply extra instructions only when a Gmail sender and optional content match.</p>
          </div>
        </div>

        <form className="source-rule-form" id="source-rule-form" onSubmit={(event) => void saveRule(event)}>
          <div className="settings-form-grid">
            <label>
              Rule name
              <input maxLength={100} onChange={(event) => setRuleDraftValue((draft) => ({ ...draft, name: event.target.value }))} placeholder="FairPrice receipts" required value={ruleDraftValue.name} />
            </label>
            <label>
              Sender matching
              <select onChange={(event) => setRuleDraftValue((draft) => ({ ...draft, senderMatchType: event.target.value as SenderMatchType }))} value={ruleDraftValue.senderMatchType}>
                <option value="exact">Exact email address</option>
                <option value="domain">Email domain</option>
                <option value="regex">Advanced pattern (RE2)</option>
              </select>
            </label>
            <label className="settings-grid-wide">
              {ruleDraftValue.senderMatchType === "regex" ? "Sender RE2 pattern" : ruleDraftValue.senderMatchType === "domain" ? "Sender domain" : "Sender email address"}
              <input
                autoCapitalize="none"
                autoComplete="off"
                maxLength={500}
                onChange={(event) => setRuleDraftValue((draft) => ({ ...draft, senderMatchValue: event.target.value }))}
                placeholder={ruleDraftValue.senderMatchType === "regex" ? String.raw`(?i)^receipts?@.*\.fairprice\.com$` : ruleDraftValue.senderMatchType === "domain" ? "fairprice.com" : "receipts@fairprice.com"}
                required
                value={ruleDraftValue.senderMatchValue}
              />
              <small>{ruleDraftValue.senderMatchType === "regex" ? "Validated by the server using RE2 syntax." : `Matches ${senderMatchLabel(ruleDraftValue.senderMatchType).toLowerCase()}.`}</small>
            </label>
            <label>
              Subject pattern <span className="optional">(optional RE2)</span>
              <input maxLength={1000} onChange={(event) => setRuleDraftValue((draft) => ({ ...draft, subjectMatcher: event.target.value }))} placeholder="receipt|invoice" value={ruleDraftValue.subjectMatcher} />
            </label>
            <label>
              Content pattern <span className="optional">(optional RE2)</span>
              <input maxLength={1000} onChange={(event) => setRuleDraftValue((draft) => ({ ...draft, contentMatcher: event.target.value }))} placeholder="Paid By" value={ruleDraftValue.contentMatcher} />
            </label>
            <label className="settings-grid-wide">
              Parsing instructions <span className="optional">(optional)</span>
              <textarea maxLength={maximumInstructionsLength} onChange={(event) => setRuleDraftValue((draft) => ({ ...draft, promptFragment: event.target.value }))} placeholder="Describe the sender-specific fields and how to interpret them." rows={6} value={ruleDraftValue.promptFragment} />
              <small>{ruleDraftValue.promptFragment.length.toLocaleString()} / {maximumInstructionsLength.toLocaleString()} characters</small>
            </label>
            <label>
              Priority
              <input max="2147483647" min="-2147483648" onChange={(event) => setRuleDraftValue((draft) => ({ ...draft, priority: event.target.value }))} required step="1" type="number" value={ruleDraftValue.priority} />
            </label>
            <label className="checkbox-label settings-rule-enabled">
              <input checked={ruleDraftValue.active} onChange={(event) => setRuleDraftValue((draft) => ({ ...draft, active: event.target.checked }))} type="checkbox" />
              Enabled
            </label>
          </div>
          {ruleError && <p className="form-error" role="alert">{ruleError}</p>}
          <div className="settings-form-footer">
            <small>Higher priorities are evaluated first. Rule versions are retained in parser audit history.</small>
            <div className="settings-form-actions">
              {editingRuleID && <button className="button button-secondary" onClick={cancelRuleEdit} type="button">Cancel edit</button>}
              <button className="button button-primary" disabled={ruleSaving} type="submit">
                {editingRuleID ? <Save aria-hidden="true" size={17} /> : <Plus aria-hidden="true" size={17} />}
                {ruleSaving ? "Saving…" : editingRuleID ? "Save rule" : "Add rule"}
              </button>
            </div>
          </div>
        </form>

        {settings.source_rules.length === 0 ? (
          <div className="settings-empty" role="status">
            <strong>No sender-specific rules</strong>
            <p>System rules and safeguards still apply, followed by your default instructions.</p>
          </div>
        ) : (
          <ul className="settings-list source-rule-list">
            {settings.source_rules.map((rule) => (
              <li className={!rule.active ? "inactive" : undefined} key={rule.id}>
                <div>
                  <span className="settings-list-heading">
                    <strong>{rule.name}</strong>
                    <span className={`status-pill ${rule.active ? "transfer" : "review"}`}>{rule.active ? "Enabled" : "Retired"}</span>
                  </span>
                  <p>{senderMatchLabel(rule.sender_match_type)}: {rule.sender_match_value} · Priority {rule.priority} · Version {rule.version}</p>
                  {(rule.subject_matcher || rule.content_matcher) && (
                    <p>{rule.subject_matcher ? `Subject: ${rule.subject_matcher}` : "Any subject"} · {rule.content_matcher ? `Content: ${rule.content_matcher}` : "Any content"}</p>
                  )}
                </div>
                <div className="settings-list-actions">
                  <button aria-label={`Edit ${rule.name}`} className="icon-button" disabled={ruleChangingID === rule.id} onClick={() => beginRuleEdit(rule)} type="button"><Pencil aria-hidden="true" size={16} /></button>
                  {rule.active ? (
                    <button aria-label={`Retire ${rule.name}`} className="icon-button danger-button" disabled={ruleChangingID === rule.id} onClick={() => void changeRule(rule, false, true)} type="button"><Trash2 aria-hidden="true" size={16} /></button>
                  ) : (
                    <button aria-label={`Reactivate ${rule.name}`} className="icon-button" disabled={ruleChangingID === rule.id} onClick={() => void changeRule(rule, true)} type="button"><RotateCcw aria-hidden="true" size={16} /></button>
                  )}
                </div>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  );
}

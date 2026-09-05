import { useCallback, useEffect, useMemo, useState, type FormEvent } from "react";
import type { Session } from "@supabase/supabase-js";
import { CircleAlert, Globe2, Pencil, Plus, RefreshCw, Save } from "lucide-react";
import {
  createGlobalSourceParserRule,
  getGlobalTransactionSettings,
  TransactionApiError,
  updateGlobalSourceParserRule,
} from "./api";
import {
  emptyGlobalRuleDraft,
  isCatchAllGlobalRuleDraft,
  type GlobalRuleDraft,
} from "./globalRuleModel";
import type {
  GlobalSourceParserRule,
  GlobalSourceParserRuleInput,
} from "./model";

const maximumNameLength = 100;
const maximumSenderMatcherLength = 500;
const maximumContentMatcherLength = 1000;
const maximumPromptLength = 4000;

function draftForRule(rule: GlobalSourceParserRule): GlobalRuleDraft {
  return {
    name: rule.name,
    senderMatcher: rule.sender_matcher ?? "",
    contentMatcher: rule.content_matcher ?? "",
    promptFragment: rule.prompt_fragment,
    priority: String(rule.priority),
    active: rule.active,
  };
}

function nullableTrimmed(value: string): string | null {
  const trimmed = value.trim();
  return trimmed === "" ? null : trimmed;
}

function ruleOrder(left: GlobalSourceParserRule, right: GlobalSourceParserRule): number {
  return right.priority - left.priority || left.name.localeCompare(right.name);
}

function formattedDate(value: string): string {
  return new Intl.DateTimeFormat(undefined, {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(new Date(value));
}

function editorLabel(userID: string | null): string {
  if (!userID) return "System";
  return `User ${userID.slice(0, 8)}…`;
}

export function GlobalTransactionSettingsPage({ session }: { session: Session }) {
  const [rules, setRules] = useState<GlobalSourceParserRule[]>([]);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [editingRule, setEditingRule] = useState<GlobalSourceParserRule | null>(null);
  const [formOpen, setFormOpen] = useState(false);
  const [draft, setDraft] = useState<GlobalRuleDraft>(emptyGlobalRuleDraft);
  const [saving, setSaving] = useState(false);
  const [reloadingConflict, setReloadingConflict] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [hasConflict, setHasConflict] = useState(false);
  const [success, setSuccess] = useState<string | null>(null);

  const load = useCallback(async (signal?: AbortSignal): Promise<boolean> => {
    setLoading(true);
    setLoadError(null);
    try {
      const settings = await getGlobalTransactionSettings(session, signal);
      setRules([...settings.rules].sort(ruleOrder));
      return true;
    } catch (error: unknown) {
      if (!signal?.aborted) {
        setLoadError(
          error instanceof Error ? error.message : "Couldn’t load global source rules.",
        );
      }
      return false;
    } finally {
      if (!signal?.aborted) setLoading(false);
    }
  }, [session]);

  useEffect(() => {
    const controller = new AbortController();
    const timer = window.setTimeout(() => void load(controller.signal), 0);
    return () => {
      window.clearTimeout(timer);
      controller.abort();
    };
  }, [load]);

  const activeCount = useMemo(() => rules.filter((rule) => rule.active).length, [rules]);
  const formBusy = saving || reloadingConflict || loading;
  const isCatchAll = isCatchAllGlobalRuleDraft(draft);

  function announce(message: string) {
    setSuccess(message);
    window.setTimeout(
      () => setSuccess((current) => (current === message ? null : current)),
      5000,
    );
  }

  function beginCreate() {
    setEditingRule(null);
    setDraft(emptyGlobalRuleDraft());
    setSaveError(null);
    setHasConflict(false);
    setFormOpen(true);
    window.requestAnimationFrame(() => document.getElementById("global-rule-name")?.focus());
  }

  function beginEdit(rule: GlobalSourceParserRule) {
    setEditingRule(rule);
    setDraft(draftForRule(rule));
    setSaveError(null);
    setHasConflict(false);
    setFormOpen(true);
    window.requestAnimationFrame(() => {
      document.getElementById("global-source-rule-form")?.scrollIntoView({ behavior: "smooth" });
      document.getElementById("global-rule-name")?.focus();
    });
  }

  function closeForm() {
    setFormOpen(false);
    setEditingRule(null);
    setDraft(emptyGlobalRuleDraft());
    setSaveError(null);
    setHasConflict(false);
  }

  function validateDraft(): GlobalSourceParserRuleInput | null {
    const name = draft.name.trim();
    const priority = Number(draft.priority);
    if (!name) {
      setSaveError("Enter a rule name.");
      return null;
    }
    if (name.length > maximumNameLength) {
      setSaveError(`Rule names cannot exceed ${maximumNameLength} characters.`);
      return null;
    }
    if (draft.senderMatcher.length > maximumSenderMatcherLength) {
      setSaveError(`Sender matchers cannot exceed ${maximumSenderMatcherLength} characters.`);
      return null;
    }
    if (draft.contentMatcher.length > maximumContentMatcherLength) {
      setSaveError(`Content matchers cannot exceed ${maximumContentMatcherLength} characters.`);
      return null;
    }
    if (draft.promptFragment.length > maximumPromptLength) {
      setSaveError(`Prompt fragments cannot exceed ${maximumPromptLength} characters.`);
      return null;
    }
    if (!Number.isSafeInteger(priority) || priority < -2147483648 || priority > 2147483647) {
      setSaveError("Priority must be a whole number in the supported 32-bit range.");
      return null;
    }
    return {
      name,
      provider: "gmail",
      sender_matcher: nullableTrimmed(draft.senderMatcher),
      content_matcher: nullableTrimmed(draft.contentMatcher),
      prompt_fragment: draft.promptFragment.trim(),
      priority,
      active: draft.active,
    };
  }

  async function saveRule(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (formBusy) return;
    setSaveError(null);
    setHasConflict(false);
    const input = validateDraft();
    if (!input) return;
    setSaving(true);
    try {
      const saved = editingRule
        ? await updateGlobalSourceParserRule(session, editingRule.id, editingRule.version, input)
        : await createGlobalSourceParserRule(session, input);
      setRules((current) => {
        const withoutSaved = current.filter((rule) => rule.id !== saved.id);
        return [...withoutSaved, saved].sort(ruleOrder);
      });
      announce(editingRule ? "Global source rule updated." : "Global source rule created.");
      closeForm();
    } catch (error: unknown) {
      if (error instanceof TransactionApiError && error.status === 409) {
        setHasConflict(true);
        setSaveError(
          "Someone changed this rule after you opened it. Reload the latest version before editing again.",
        );
      } else {
        setSaveError(error instanceof Error ? error.message : "Couldn’t save this global rule.");
      }
    } finally {
      setSaving(false);
    }
  }

  async function reloadAfterConflict() {
    if (formBusy) return;
    setReloadingConflict(true);
    const reloaded = await load();
    setReloadingConflict(false);
    if (reloaded) {
      closeForm();
      return;
    }
    setSaveError("Couldn’t reload the latest rule. Your draft is still open; try again.");
  }

  if (loading && rules.length === 0) {
    return (
      <section
        aria-busy="true"
        aria-label="Loading global transaction settings"
        className="settings-loading"
        role="status"
      >
        <span className="sr-only">Loading global transaction settings…</span>
        <div className="skeleton-row" />
        <div className="skeleton-row" />
        <div className="skeleton-row" />
      </section>
    );
  }

  if (loadError && rules.length === 0) {
    return (
      <section className="empty-state transaction-empty" role="alert">
        <CircleAlert aria-hidden="true" size={30} />
        <h1>Couldn’t load global source rules</h1>
        <p>{loadError}</p>
        <button
          className="button button-secondary"
          disabled={loading}
          onClick={() => void load()}
          type="button"
        >
          <RefreshCw aria-hidden="true" size={17} /> Retry
        </button>
      </section>
    );
  }

  return (
    <div className="transaction-settings-page global-settings-page">
      <header className="page-header">
        <div>
          <p className="eyebrow">PLATFORM-WIDE TRANSACTION CONFIGURATION</p>
          <h1>Global Settings</h1>
          <p className="muted">
            Define Gmail source rules shared by every user. Platform safeguards remain immutable.
          </p>
        </div>
        <button
          className="button button-primary"
          disabled={formBusy || loading}
          onClick={beginCreate}
          type="button"
        >
          <Plus aria-hidden="true" size={17} /> Add global rule
        </button>
      </header>

      <div aria-atomic="true" aria-live="polite">
        {success && <p className="notice notice-success settings-success" role="status">{success}</p>}
      </div>

      <section className="settings-caution" role="note">
        <strong>Development access:</strong> every signed-in user can currently change these global
        rules. A saved change affects future and manually retried parses for all users; it does not
        reparse existing sources automatically.
      </section>

      {loadError && (
        <section className="notice notice-error" role="alert">
          <CircleAlert aria-hidden="true" size={20} />
          <div><strong>Couldn’t refresh all rules.</strong><p>{loadError}</p></div>
          <button
            className="button button-secondary"
            disabled={loading || formBusy}
            onClick={() => void load()}
            type="button"
          >
            Retry
          </button>
        </section>
      )}

      {formOpen && (
        <form
          className="settings-card source-rule-form"
          id="global-source-rule-form"
          onSubmit={(event) => void saveRule(event)}
        >
          <div className="settings-card-heading">
            <div>
              <p className="eyebrow">{editingRule ? "EDIT GLOBAL RULE" : "NEW GLOBAL RULE"}</p>
              <h2>{editingRule ? editingRule.name : "Configure a source rule"}</h2>
              <p>Matchers use RE2 syntax. Leave either matcher empty when that field should not restrict the rule.</p>
            </div>
            {editingRule && <span className="status-pill transfer">Version {editingRule.version}</span>}
          </div>
          <fieldset className="global-rule-fields" disabled={formBusy}>
          <div className="settings-form-grid">
            <label htmlFor="global-rule-name">
              Rule name
              <input
                id="global-rule-name"
                maxLength={maximumNameLength}
                onChange={(event) => setDraft((current) => ({ ...current, name: event.target.value }))}
                placeholder="OCBC card purchase"
                required
                value={draft.name}
              />
            </label>
            <label>
              Provider
              <input aria-describedby="global-provider-help" readOnly value="Gmail" />
              <small id="global-provider-help">Global source rules currently support Gmail only.</small>
            </label>
            <label className="settings-grid-wide">
              Sender matcher <span className="optional">(optional RE2)</span>
              <input
                autoCapitalize="none"
                autoComplete="off"
                maxLength={maximumSenderMatcherLength}
                onChange={(event) => setDraft((current) => ({ ...current, senderMatcher: event.target.value }))}
                placeholder={String.raw`(?i)@ocbc\.com$`}
                value={draft.senderMatcher}
              />
              <small>{draft.senderMatcher.length.toLocaleString()} / {maximumSenderMatcherLength.toLocaleString()} characters</small>
            </label>
            <label className="settings-grid-wide">
              Content matcher <span className="optional">(optional RE2)</span>
              <input
                maxLength={maximumContentMatcherLength}
                onChange={(event) => setDraft((current) => ({ ...current, contentMatcher: event.target.value }))}
                placeholder="card purchase|transaction alert"
                value={draft.contentMatcher}
              />
              <small>{draft.contentMatcher.length.toLocaleString()} / {maximumContentMatcherLength.toLocaleString()} characters</small>
            </label>
            <label className="settings-grid-wide">
              Prompt fragment <span className="optional">(optional)</span>
              <textarea
                maxLength={maximumPromptLength}
                onChange={(event) => setDraft((current) => ({ ...current, promptFragment: event.target.value }))}
                placeholder="Explain sender-specific fields and how the parser should interpret them."
                rows={7}
                value={draft.promptFragment}
              />
              <small>{draft.promptFragment.length.toLocaleString()} / {maximumPromptLength.toLocaleString()} characters</small>
            </label>
            <label>
              Priority
              <input
                max="2147483647"
                min="-2147483648"
                onChange={(event) => setDraft((current) => ({ ...current, priority: event.target.value }))}
                required
                step="1"
                type="number"
                value={draft.priority}
              />
            </label>
            <label className="checkbox-label settings-rule-enabled">
              <input
                checked={draft.active}
                onChange={(event) => setDraft((current) => ({ ...current, active: event.target.checked }))}
                type="checkbox"
              />
              Enabled
            </label>
          </div>
          {isCatchAll && (
            <div
              className={`settings-caution catch-all-rule-warning${draft.active ? " enabled" : ""}`}
              role={draft.active ? "alert" : "note"}
            >
              <strong>{draft.active ? "Catch-all rule enabled." : "This is a catch-all rule."}</strong>{" "}
              With both matchers blank, it can match every Gmail source. {draft.active
                ? "Its priority may cause it to win over narrower rules. Enable it only intentionally."
                : "It will remain harmless while disabled; add a matcher before enabling it unless this broad scope is intentional."}
            </div>
          )}
          </fieldset>
          {saveError && (
            <div className="global-rule-save-error" role="alert">
              <p className="form-error">{saveError}</p>
              {hasConflict && (
                <button
                  className="button button-secondary"
                  disabled={formBusy}
                  onClick={() => void reloadAfterConflict()}
                  type="button"
                >
                  <RefreshCw aria-hidden="true" size={16} />
                  {reloadingConflict ? "Reloading…" : "Reload latest version"}
                </button>
              )}
            </div>
          )}
          <div className="settings-form-footer">
            <small>Higher priorities are evaluated first. Disable a rule instead of deleting its history.</small>
            <div className="settings-form-actions">
              <button className="button button-secondary" disabled={formBusy} onClick={closeForm} type="button">Cancel</button>
              <button className="button button-primary" disabled={formBusy} type="submit">
                <Save aria-hidden="true" size={17} /> {saving ? "Saving…" : "Save global rule"}
              </button>
            </div>
          </div>
        </form>
      )}

      <section aria-labelledby="global-rules-title" className="settings-card">
        <div className="settings-card-heading">
          <div>
            <p className="eyebrow">GLOBAL SOURCE RULES</p>
            <h2 id="global-rules-title">{activeCount} active of {rules.length}</h2>
            <p>Extraction configuration is controlled by the backend and is visible here for inspection only.</p>
          </div>
          <Globe2 aria-hidden="true" size={22} />
        </div>

        {rules.length === 0 ? (
          <div className="settings-empty" role="status">
            <strong>No global rules configured</strong>
            <p>The immutable platform prompt still applies. Add a rule for provider-specific guidance.</p>
          </div>
        ) : (
          <ul className="settings-list global-rule-list">
            {rules.map((rule) => (
              <li className={!rule.active ? "inactive" : undefined} key={rule.id}>
                <div className="global-rule-summary">
                  <span className="settings-list-heading">
                    <strong>{rule.name}</strong>
                    <span className={`status-pill ${rule.active ? "transfer" : "review"}`}>
                      {rule.active ? "Enabled" : "Disabled"}
                    </span>
                  </span>
                  <p>Gmail · Priority {rule.priority} · Version {rule.version}</p>
                  <p>Sender: {rule.sender_matcher ?? "Any sender"}</p>
                  <p>Content: {rule.content_matcher ?? "Any content"}</p>
                  {rule.prompt_fragment && <p className="global-rule-prompt">{rule.prompt_fragment}</p>}
                  <p>Last updated {formattedDate(rule.updated_at)} by {editorLabel(rule.updated_by_user_id)}</p>
                </div>
                <button
                  aria-label={`Edit ${rule.name}`}
                  className="icon-button"
                  disabled={formBusy || loading}
                  onClick={() => beginEdit(rule)}
                  type="button"
                >
                  <Pencil aria-hidden="true" size={16} />
                </button>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  );
}

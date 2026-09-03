import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import type { Session } from "@supabase/supabase-js";
import { Braces, CircleAlert, FileSearch, Mail, RefreshCw, ShieldCheck } from "lucide-react";
import {
  buildPromptPreview,
  getGlobalTransactionSettings,
  getTransactionSettings,
  listPromptPreviewSources,
} from "./api";
import {
  automaticPromptPreviewInput,
  formatPromptPreviewRequest,
  manualPromptPreviewInput,
} from "./promptPreviewModel";
import type {
  GlobalSourceParserRule,
  PromptPreviewMode,
  PromptPreviewResult,
  PromptPreviewSource,
  SourceParserRule,
  TransactionSettings,
} from "./model";

function formattedDate(value: string): string {
  return new Intl.DateTimeFormat(undefined, {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(new Date(value));
}

function emailLabel(source: PromptPreviewSource): string {
  const subject = source.subject?.trim() || "No subject";
  const sender = source.sender?.trim() || "Unknown sender";
  return `${subject} — ${sender} — ${formattedDate(source.received_at)} — ${source.parse_status.replaceAll("_", " ")}`;
}

function ruleLabel(rule: GlobalSourceParserRule | SourceParserRule): string {
  return `${rule.name}${rule.active ? "" : " (inactive)"} · priority ${rule.priority} · v${rule.version}`;
}

export function PromptPreviewPage({ session }: { session: Session }) {
  const [globalRules, setGlobalRules] = useState<GlobalSourceParserRule[]>([]);
  const [settings, setSettings] = useState<TransactionSettings | null>(null);
  const [sources, setSources] = useState<PromptPreviewSource[]>([]);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [mode, setMode] = useState<PromptPreviewMode>("automatic");
  const [globalRuleID, setGlobalRuleID] = useState("");
  const [includeUserDefault, setIncludeUserDefault] = useState(true);
  const [userRuleID, setUserRuleID] = useState("");
  const [sourceID, setSourceID] = useState("");
  const [preview, setPreview] = useState<PromptPreviewResult | null>(null);
  const [previewing, setPreviewing] = useState(false);
  const [previewError, setPreviewError] = useState<string | null>(null);
  const [previewStatus, setPreviewStatus] = useState("");
  const previewController = useRef<AbortController | null>(null);
  const previewGeneration = useRef(0);

  const load = useCallback(async (signal?: AbortSignal) => {
    setLoading(true);
    setLoadError(null);
    try {
      const [globalSettings, personalSettings, recentSources] = await Promise.all([
        getGlobalTransactionSettings(session, signal),
        getTransactionSettings(session, signal),
        listPromptPreviewSources(session, signal),
      ]);
      setGlobalRules(globalSettings.rules);
      setSettings(personalSettings);
      setSources(recentSources);
      setSourceID((current) =>
        recentSources.some((source) => source.id === current)
          ? current
          : (recentSources[0]?.id ?? ""),
      );
      setGlobalRuleID((current) =>
        globalSettings.rules.some((rule) => rule.id === current) ? current : "",
      );
      setUserRuleID((current) =>
        personalSettings.source_rules.some((rule) => rule.id === current) ? current : "",
      );
    } catch (error: unknown) {
      if (!signal?.aborted) {
        setLoadError(error instanceof Error ? error.message : "Couldn’t load prompt preview options.");
      }
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

  useEffect(() => () => {
    previewGeneration.current += 1;
    previewController.current?.abort();
  }, []);

  const selectedSource = useMemo(
    () => sources.find((source) => source.id === sourceID) ?? null,
    [sourceID, sources],
  );

  function invalidatePreview() {
    previewGeneration.current += 1;
    previewController.current?.abort();
    previewController.current = null;
    setPreviewing(false);
    setPreview(null);
    setPreviewError(null);
    setPreviewStatus("");
  }

  function changeMode(nextMode: PromptPreviewMode) {
    invalidatePreview();
    setMode(nextMode);
  }

  async function createPreview(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (previewing) return;
    setPreviewError(null);
    setPreviewStatus("");
    if (mode === "automatic" && !sourceID) {
      setPreviewError("Choose a past email to run the production rule matcher.");
      return;
    }
    previewController.current?.abort();
    const controller = new AbortController();
    const generation = previewGeneration.current + 1;
    previewGeneration.current = generation;
    previewController.current = controller;
    setPreview(null);
    setPreviewing(true);
    try {
      const result = await buildPromptPreview(
        session,
        mode === "automatic"
          ? automaticPromptPreviewInput(sourceID)
          : manualPromptPreviewInput(globalRuleID, includeUserDefault, userRuleID),
        controller.signal,
      );
      if (controller.signal.aborted || generation !== previewGeneration.current) return;
      setPreview(result);
      setPreviewStatus("Prompt preview built.");
    } catch (error: unknown) {
      if (controller.signal.aborted || generation !== previewGeneration.current) return;
      setPreview(null);
      setPreviewError(error instanceof Error ? error.message : "Couldn’t build this prompt preview.");
    } finally {
      if (generation === previewGeneration.current) {
        previewController.current = null;
        setPreviewing(false);
      }
    }
  }

  if (loading) {
    return (
      <section
        aria-busy="true"
        aria-label="Loading prompt preview"
        className="settings-loading"
        role="status"
      >
        <span className="sr-only">Loading prompt preview…</span>
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
        <h1>Couldn’t load prompt preview</h1>
        <p>{loadError ?? "The prompt configuration was unavailable."}</p>
        <button className="button button-secondary" onClick={() => void load()} type="button">
          <RefreshCw aria-hidden="true" size={17} /> Retry
        </button>
      </section>
    );
  }

  return (
    <div className="transaction-settings-page prompt-preview-page">
      <header className="page-header">
        <div>
          <p className="eyebrow">TRANSACTION PARSER INSPECTION</p>
          <h1>Prompt Preview</h1>
          <p className="muted">
            Inspect how immutable platform guidance and configurable rules assemble before Qwen sees a source.
          </p>
        </div>
        <FileSearch aria-hidden="true" className="settings-page-icon" size={34} />
      </header>

      <section className="prompt-preview-safety" role="note">
        <ShieldCheck aria-hidden="true" size={20} />
        <div>
          <strong>Safe preview only</strong>
          <p>
            This page does not call Qwen, queue a job, create a parse audit, or write transaction data.
            Email and eligible attachment content are replaced by explicit placeholders in the request template.
          </p>
        </div>
      </section>

      {previewStatus && (
        <p aria-atomic="true" className="prompt-preview-live-status" role="status">
          {previewStatus}
        </p>
      )}

      <form className="settings-card" onSubmit={(event) => void createPreview(event)}>
        <fieldset className="prompt-mode-picker">
          <legend>Preview mode</legend>
          <label className={mode === "automatic" ? "selected" : undefined}>
            <input
              checked={mode === "automatic"}
              disabled={previewing}
              name="prompt-preview-mode"
              onChange={() => changeMode("automatic")}
              type="radio"
            />
            <span><strong>Automatic</strong><small>Use a past owned email and the production matcher.</small></span>
          </label>
          <label className={mode === "manual" ? "selected" : undefined}>
            <input
              checked={mode === "manual"}
              disabled={previewing}
              name="prompt-preview-mode"
              onChange={() => changeMode("manual")}
              type="radio"
            />
            <span><strong>Manual</strong><small>Choose the configurable prompt parts yourself.</small></span>
          </label>
        </fieldset>

        {mode === "automatic" ? (
          <div className="prompt-preview-controls">
            <label htmlFor="prompt-preview-email">
              Past email
              <select
                disabled={previewing || sources.length === 0}
                id="prompt-preview-email"
                onChange={(event) => {
                  invalidatePreview();
                  setSourceID(event.target.value);
                }}
                required
                value={sourceID}
              >
                {sources.length === 0 ? (
                  <option value="">No stored Gmail emails available</option>
                ) : sources.map((source) => (
                  <option key={source.id} value={source.id}>{emailLabel(source)}</option>
                ))}
              </select>
            </label>
            {selectedSource && (
              <div className="selected-preview-source">
                <Mail aria-hidden="true" size={19} />
                <div>
                  <strong>{selectedSource.subject ?? "No subject"}</strong>
                  <p>{selectedSource.sender ?? "Unknown sender"} · {formattedDate(selectedSource.received_at)} · {selectedSource.parse_status.replaceAll("_", " ")}</p>
                </div>
              </div>
            )}
            {sources.length === 0 && (
              <div className="settings-empty" role="status">
                <strong>No past Gmail email is stored</strong>
                <p>Sync Gmail first, or use Manual mode to inspect a chosen rule combination.</p>
              </div>
            )}
          </div>
        ) : (
          <div className="settings-form-grid prompt-preview-controls">
            <label>
              Global source rule <span className="optional">(optional)</span>
              <select disabled={previewing} onChange={(event) => {
                invalidatePreview();
                setGlobalRuleID(event.target.value);
              }} value={globalRuleID}>
                <option value="">None</option>
                {globalRules.map((rule) => (
                  <option key={rule.id} value={rule.id}>{ruleLabel(rule)}</option>
                ))}
              </select>
            </label>
            <label>
              Personal source rule <span className="optional">(optional)</span>
              <select disabled={previewing} onChange={(event) => {
                invalidatePreview();
                setUserRuleID(event.target.value);
              }} value={userRuleID}>
                <option value="">None</option>
                {settings.source_rules.map((rule) => (
                  <option key={rule.id} value={rule.id}>{ruleLabel(rule)}</option>
                ))}
              </select>
            </label>
            <label className="checkbox-label prompt-default-toggle settings-grid-wide">
              <input
                checked={includeUserDefault}
                disabled={previewing}
                onChange={(event) => {
                  invalidatePreview();
                  setIncludeUserDefault(event.target.checked);
                }}
                type="checkbox"
              />
              <span>
                Include my default instructions
                <small>
                  {settings.default_instructions_version === 0
                    ? "No customized default is currently saved."
                    : `Saved version ${settings.default_instructions_version}.`}
                </small>
              </span>
            </label>
          </div>
        )}

        {previewError && <p className="form-error" role="alert">{previewError}</p>}
        <div className="settings-form-footer prompt-preview-submit">
          <small>
            {mode === "automatic"
              ? "The server reads the selected source privately to choose rules, then omits its content from the response."
              : "Inactive rules can be selected here for inspection; saving or activation is handled elsewhere."}
          </small>
          <button
            className="button button-primary"
            disabled={previewing || (mode === "automatic" && sources.length === 0)}
            type="submit"
          >
            <FileSearch aria-hidden="true" size={17} /> {previewing ? "Building preview…" : "Build preview"}
          </button>
        </div>
      </form>

      {preview && (
        <section className="prompt-preview-results">
          <article className="settings-card prompt-output-card">
            <div className="settings-card-heading">
              <div>
                <p className="eyebrow">EXACT SYSTEM MESSAGE</p>
                <h2>Assembled system prompt</h2>
                <p>This is the complete system message assembled by the production prompt builder.</p>
              </div>
              <span className="status-pill transfer">{preview.mode}</span>
            </div>
            <pre className="prompt-output">{preview.assembled_system_prompt}</pre>
          </article>

          <article className="settings-card prompt-output-card">
            <div className="settings-card-heading">
              <div>
                <p className="eyebrow">QWEN REQUEST TEMPLATE</p>
                <h2>Provider request structure</h2>
                <p>Dynamic email and eligible attachment content appears only as placeholders.</p>
              </div>
              <Braces aria-hidden="true" size={22} />
            </div>
            <pre className="prompt-output">{formatPromptPreviewRequest(preview.provider_request)}</pre>
          </article>

          <article className="settings-card prompt-output-card">
            <div className="settings-card-heading">
              <div>
                <p className="eyebrow">ASSEMBLY DETAILS</p>
                <h2>Prompt components</h2>
                <p>Use these details to see which configurable parts were included.</p>
              </div>
            </div>
            <pre className="prompt-output prompt-output-compact">{JSON.stringify(preview.prompt_components, null, 2)}</pre>
            {preview.selection && (
              <details className="prompt-selection-details">
                <summary>Rule selection and matching reason</summary>
                <pre className="prompt-output prompt-output-compact">{JSON.stringify(preview.selection, null, 2)}</pre>
              </details>
            )}
            {preview.selected_source && (
              <p className="prompt-selected-source-result">
                Selected source: <strong>{preview.selected_source.subject ?? "No subject"}</strong> from {preview.selected_source.sender ?? "Unknown sender"}.
              </p>
            )}
          </article>
        </section>
      )}
    </div>
  );
}

import { useCallback, useEffect, useMemo, useState } from "react";
import type { Session } from "@supabase/supabase-js";
import { CircleAlert, FileCode2, Play, Plus, RefreshCw, Check } from "lucide-react";
import {
  activateScriptVersion,
  createScriptVersion,
  dryRunScript,
  getScriptVersion,
  listScriptVersions,
  listScripts,
  TransactionApiError,
  type ScriptDryRunResult,
} from "./api";
import { formatDateTime, type ScriptSummary, type ScriptVersion } from "./model";

const defaultKeys = ["email_pre_process", "transaction_post_process"] as const;

const starterSource: Record<string, string> = {
  email_pre_process: `// Clean the email before the LLM. Return the cleaned normalized_content.\noutput := {normalized_content: input.normalized_content}`,
  transaction_post_process: `// Transform one candidate. Return the full candidate object.\noutput := input`,
};

function mergeKeys(summaries: ScriptSummary[]): ScriptSummary[] {
  const byKey = new Map<string, ScriptSummary>();
  for (const key of defaultKeys) {
    byKey.set(key, { script_key: key, active_version: 0, version_count: 0 });
  }
  for (const summary of summaries) byKey.set(summary.script_key, summary);
  return Array.from(byKey.values());
}

export function ScriptsManagementPage({ session }: { session: Session }) {
  const [summaries, setSummaries] = useState<ScriptSummary[]>(mergeKeys([]));
  const [selectedKey, setSelectedKey] = useState<string>(defaultKeys[0]);
  const [versions, setVersions] = useState<ScriptVersion[]>([]);
  const [source, setSource] = useState<string>("");
  const [notes, setNotes] = useState<string>("");
  const [dryRunInput, setDryRunInput] = useState<string>("{}");
  const [dryRunResult, setDryRunResult] = useState<ScriptDryRunResult | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const reportError = useCallback((cause: unknown) => {
    if (cause instanceof TransactionApiError) setError(cause.message);
    else if (cause instanceof Error) setError(cause.message);
    else setError("Something went wrong.");
  }, []);

  const refreshSummaries = useCallback(async () => {
    try {
      setSummaries(mergeKeys(await listScripts(session)));
    } catch (cause) {
      reportError(cause);
    }
  }, [session, reportError]);

  const loadVersions = useCallback(
    async (key: string) => {
      setLoading(true);
      setError(null);
      try {
        const loaded = await listScriptVersions(session, key);
        setVersions(loaded);
        const active = loaded.find((version) => version.is_active);
        if (active) {
          const full = await getScriptVersion(session, key, active.version);
          setSource(full.source);
        } else {
          setSource(starterSource[key] ?? "");
        }
      } catch (cause) {
        reportError(cause);
      } finally {
        setLoading(false);
      }
    },
    [session, reportError],
  );

  useEffect(() => {
    void refreshSummaries();
  }, [refreshSummaries]);

  useEffect(() => {
    void loadVersions(selectedKey);
  }, [selectedKey, loadVersions]);

  const activeVersion = useMemo(
    () => versions.find((version) => version.is_active)?.version ?? 0,
    [versions],
  );

  async function handleCreate() {
    if (source.trim() === "") {
      setError("Script source is required.");
      return;
    }
    setBusy(true);
    setError(null);
    setNotice(null);
    try {
      const created = await createScriptVersion(session, selectedKey, source, notes);
      setNotice(`Created version ${created.version}. Activate it to make it live.`);
      setNotes("");
      await loadVersions(selectedKey);
      await refreshSummaries();
    } catch (cause) {
      reportError(cause);
    } finally {
      setBusy(false);
    }
  }

  async function handleActivate(version: number) {
    setBusy(true);
    setError(null);
    setNotice(null);
    try {
      await activateScriptVersion(session, selectedKey, version);
      setNotice(`Version ${version} is now active.`);
      await loadVersions(selectedKey);
      await refreshSummaries();
    } catch (cause) {
      reportError(cause);
    } finally {
      setBusy(false);
    }
  }

  async function handleDryRun() {
    setBusy(true);
    setError(null);
    try {
      setDryRunResult(await dryRunScript(session, source, dryRunInput));
    } catch (cause) {
      reportError(cause);
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="scripts-page" aria-labelledby="scripts-title">
      <header className="page-header">
        <div>
          <h1 id="scripts-title">
            <FileCode2 aria-hidden size={20} /> Parser scripts
          </h1>
          <p>Operator Tengo scripts that clean the email before the LLM and transform each candidate after it. Changes take effect once activated.</p>
        </div>
        <button type="button" className="secondary-button" onClick={() => void refreshSummaries()}>
          <RefreshCw aria-hidden size={16} /> Refresh
        </button>
      </header>

      {error && (
        <p className="form-error" role="alert">
          <CircleAlert aria-hidden size={16} /> {error}
        </p>
      )}
      {notice && <p className="form-notice" role="status">{notice}</p>}

      <div className="scripts-layout">
        <nav aria-label="Script keys" className="scripts-keys">
          {summaries.map((summary) => (
            <button
              key={summary.script_key}
              type="button"
              className={`nav-item${summary.script_key === selectedKey ? " active" : ""}`}
              onClick={() => setSelectedKey(summary.script_key)}
            >
              <span className="mono">{summary.script_key}</span>
              <span className="badge">{summary.active_version > 0 ? `v${summary.active_version} active` : "none active"}</span>
            </button>
          ))}
        </nav>

        <div className="scripts-editor">
          <label htmlFor="script-source">Source (active version loaded; save creates a new version)</label>
          <textarea
            id="script-source"
            className="mono"
            rows={14}
            value={source}
            onChange={(event) => setSource(event.target.value)}
            disabled={loading || busy}
            spellCheck={false}
          />
          <label htmlFor="script-notes">Notes (optional)</label>
          <input
            id="script-notes"
            type="text"
            value={notes}
            onChange={(event) => setNotes(event.target.value)}
            disabled={busy}
          />
          <div className="scripts-actions">
            <button type="button" className="primary-button" onClick={() => void handleCreate()} disabled={busy || loading}>
              <Plus aria-hidden size={16} /> Save as new version
            </button>
          </div>

          <h2>Versions</h2>
          {loading ? (
            <p>Loading versions…</p>
          ) : versions.length === 0 ? (
            <p className="empty-state">No versions yet. Save the source above to create version 1, then activate it.</p>
          ) : (
            <ul className="scripts-versions">
              {versions.map((version) => (
                <li key={version.version}>
                  <span>v{version.version}</span>
                  <span className="muted">{formatDateTime(version.created_at)}</span>
                  {version.notes && <span className="muted">{version.notes}</span>}
                  {version.is_active ? (
                    <span className="badge success"><Check aria-hidden size={14} /> active</span>
                  ) : (
                    <button type="button" className="secondary-button" onClick={() => void handleActivate(version.version)} disabled={busy}>
                      Activate
                    </button>
                  )}
                </li>
              ))}
            </ul>
          )}
          {activeVersion === 0 && versions.length > 0 && (
            <p className="muted">No version is active — this stage is currently inert.</p>
          )}
        </div>

        <div className="scripts-dryrun">
          <h2>Dry run</h2>
          <p className="muted">Run the source above against sample input without saving.</p>
          <label htmlFor="dryrun-input">Input JSON</label>
          <textarea
            id="dryrun-input"
            className="mono"
            rows={6}
            value={dryRunInput}
            onChange={(event) => setDryRunInput(event.target.value)}
            disabled={busy}
            spellCheck={false}
          />
          <button type="button" className="secondary-button" onClick={() => void handleDryRun()} disabled={busy}>
            <Play aria-hidden size={16} /> Run
          </button>
          {dryRunResult && (
            <pre className={`dryrun-output${dryRunResult.ok ? "" : " error"}`}>
              {dryRunResult.ok ? dryRunResult.output : dryRunResult.error}
            </pre>
          )}
        </div>
      </div>
    </section>
  );
}

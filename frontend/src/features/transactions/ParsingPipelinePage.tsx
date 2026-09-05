import { useEffect, useState } from "react";
import type { Session } from "@supabase/supabase-js";
import {
  SlidersHorizontal,
  CircleAlert,
  Cpu,
  Eye,
  FileCode2,
  Globe2,
  Lock,
  Paperclip,
  ShieldCheck,
  User,
  Wand2,
  GitMerge,
} from "lucide-react";
import { buildPromptPreview, listScripts } from "./api";
import { manualPromptPreviewInput } from "./promptPreviewModel";
import type { ScriptSummary } from "./model";
import type { WorkspacePage } from "../../workspace";

const preKey = "email_pre_process";
const postKey = "transaction_post_process";

function activeLabel(summaries: ScriptSummary[], key: string): string {
  const summary = summaries.find((item) => item.script_key === key);
  if (!summary || summary.active_version === 0) return "none active";
  return `v${summary.active_version} active`;
}

export function ParsingPipelinePage({
  session,
  onNavigate,
}: {
  session: Session;
  onNavigate: (page: WorkspacePage) => void;
}) {
  const [scripts, setScripts] = useState<ScriptSummary[]>([]);
  const [assembled, setAssembled] = useState<string>("");
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    Promise.all([
      listScripts(session, controller.signal),
      buildPromptPreview(session, manualPromptPreviewInput("", true, ""), controller.signal),
    ])
      .then(([loadedScripts, preview]) => {
        if (controller.signal.aborted) return;
        setScripts(loadedScripts);
        setAssembled(preview.assembled_system_prompt);
      })
      .catch((cause: unknown) => {
        if (controller.signal.aborted) return;
        setError(cause instanceof Error ? cause.message : "Couldn’t load the pipeline.");
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [session]);

  return (
    <section className="pipeline-page" aria-labelledby="pipeline-title">
      <header className="page-header">
        <div>
          <h1 id="pipeline-title">Parsing pipeline</h1>
          <p>The whole email → transaction flow in order. Blue stages are editable; grey stages are fixed server behaviour shown for context.</p>
        </div>
      </header>

      {error && (
        <p className="form-error" role="alert">
          <CircleAlert aria-hidden size={16} /> {error}
        </p>
      )}

      <ol className="pipeline-stages">
        <li className="pipeline-stage editable">
          <div className="pipeline-stage-head">
            <Wand2 aria-hidden size={18} />
            <span className="pipeline-stage-title">1 · Pre-processing</span>
            <span className="muted">clean the email before the LLM</span>
            <button type="button" className="secondary-button" onClick={() => onNavigate("transaction-scripts")}>Manage</button>
          </div>
          <p className="muted mono">{preKey} · {loading ? "…" : activeLabel(scripts, preKey)}</p>
        </li>

        <li className="pipeline-stage editable">
          <div className="pipeline-stage-head">
            <Globe2 aria-hidden size={18} />
            <span className="pipeline-stage-title">2 · Global prompt</span>
            <span className="muted">platform + global rule fragment</span>
            <button type="button" className="secondary-button" onClick={() => onNavigate("transaction-global-settings")}>Edit</button>
          </div>
          <p className="muted"><Lock aria-hidden size={13} /> Platform prompt is immutable; global rule guidance is editable.</p>
        </li>

        <li className="pipeline-stage editable">
          <div className="pipeline-stage-head">
            <User aria-hidden size={18} />
            <span className="pipeline-stage-title">3 · User prompt</span>
            <span className="muted">your default + rule fragment</span>
            <button type="button" className="secondary-button" onClick={() => onNavigate("transaction-settings")}>Edit</button>
          </div>
        </li>

        <li className="pipeline-stage readonly">
          <div className="pipeline-stage-head">
            <Paperclip aria-hidden size={18} />
            <span className="pipeline-stage-title">Attachments</span>
            <span className="muted">eligible receipt/invoice images sent with the text</span>
            <span className="badge"><Lock aria-hidden size={12} /> read-only</span>
          </div>
        </li>

        <li className="pipeline-stage readonly">
          <div className="pipeline-stage-head">
            <Cpu aria-hidden size={18} />
            <span className="pipeline-stage-title">4 · LLM</span>
            <span className="muted mono">qwen3.8-flash · returns transactions[]</span>
          </div>
        </li>

        <li className="pipeline-stage editable">
          <div className="pipeline-stage-head">
            <SlidersHorizontal aria-hidden size={18} />
            <span className="pipeline-stage-title">5 · Post-processing</span>
            <span className="muted">fix each candidate after the LLM</span>
            <button type="button" className="secondary-button" onClick={() => onNavigate("transaction-scripts")}>Manage</button>
          </div>
          <p className="muted mono">{postKey} · {loading ? "…" : activeLabel(scripts, postKey)}</p>
        </li>

        <li className="pipeline-stage readonly">
          <div className="pipeline-stage-head">
            <ShieldCheck aria-hidden size={18} />
            <span className="pipeline-stage-title">Server checks</span>
            <span className="muted">owner · sanitize account evidence · auto-eligible · validate</span>
            <span className="badge"><Lock aria-hidden size={12} /> always enforced</span>
          </div>
        </li>

        <li className="pipeline-stage readonly">
          <div className="pipeline-stage-head">
            <GitMerge aria-hidden size={18} />
            <span className="pipeline-stage-title">Reconciliation</span>
            <span className="muted">resolve account → dedup → create / attach / review / dangling</span>
            <span className="badge"><Lock aria-hidden size={12} /> per candidate</span>
          </div>
        </li>
      </ol>

      <section className="pipeline-preview" aria-labelledby="pipeline-preview-title">
        <div className="pipeline-stage-head">
          <Eye aria-hidden size={18} />
          <h2 id="pipeline-preview-title">Assembled preview</h2>
          <button type="button" className="secondary-button" onClick={() => onNavigate("transaction-prompt-preview")}>Open full preview</button>
        </div>
        {loading ? (
          <p className="muted">Loading assembled prompt…</p>
        ) : (
          <pre className="prompt-output">{assembled}</pre>
        )}
      </section>

      <p className="muted"><FileCode2 aria-hidden size={13} /> Per-sender rule selection of scripts is configured on each parser rule; this overview shows the global defaults and the assembled prompt.</p>
    </section>
  );
}

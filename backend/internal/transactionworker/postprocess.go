package transactionworker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/zhengteck/wealth-builder/backend/internal/reconciliation"
	"github.com/zhengteck/wealth-builder/backend/internal/scriptstore"
)

// defaultPostProcessScriptKey is the global script consulted after the LLM (and
// after any deterministic rule) when a matched rule names no post-process script.
const defaultPostProcessScriptKey = "transaction_post_process"

// postprocessCandidate runs the active post-process script over one parsed
// candidate's mutable subset (the JSON-serialisable candidate fields; the
// server-owned UserID/Confidence/AutoEligible are json:"-" and never exposed to
// or writable by the script). It returns the possibly-mutated response and an
// audit note. The stage is inert when no engine/resolver is configured or no
// active script is seeded, and falls back to the unmodified candidate on any
// error or invalid output. The caller re-runs the full validation tail after
// this, so a script cannot break a server invariant.
//
// First-cut contract: a script may change values of already-populated fields; a
// candidate the script leaves invalid (or that introduces an uncited decisive
// field) is rejected by the downstream validation, not persisted.
func (h Handler) postprocessCandidate(ctx context.Context, parsed reconciliation.ParsedResponse) (reconciliation.ParsedResponse, string) {
	if h.Engine == nil || h.Scripts == nil {
		return parsed, ""
	}
	script, err := h.Scripts.LoadActiveScript(ctx, defaultPostProcessScriptKey)
	if errors.Is(err, scriptstore.ErrNoActiveScript) {
		return parsed, ""
	}
	if err != nil {
		return parsed, "fallback:load_error"
	}
	before, err := json.Marshal(parsed.Candidate)
	if err != nil {
		return parsed, "fallback:marshal_error"
	}
	out, err := h.Engine.Run(ctx, script.Source, before)
	if err != nil {
		return parsed, "fallback:run_error"
	}
	decoder := json.NewDecoder(bytes.NewReader(out))
	decoder.DisallowUnknownFields()
	var mutated reconciliation.Candidate
	if decoder.Decode(&mutated) != nil {
		return parsed, "fallback:invalid_output"
	}
	// Server-owned fields are json:"-", so they are absent from the script's
	// output and must be carried over from the pre-script candidate.
	mutated.UserID = parsed.Candidate.UserID
	mutated.Confidence = parsed.Candidate.Confidence
	mutated.AutoEligible = parsed.Candidate.AutoEligible
	parsed.Candidate = mutated
	return parsed, fmt.Sprintf("%s:v%d", script.Key, script.Version)
}

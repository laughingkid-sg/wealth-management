// Package scriptengine is a standalone, domain-agnostic sandbox for running
// operator-authored Tengo scripts with a strict JSON in / JSON out contract.
//
// It embeds the Tengo interpreter (github.com/d5/tengo/v2) in-process so any
// service in this module (api, worker) can share one Engine. The engine knows
// nothing about the finance domain: a caller passes a script source and a JSON
// input document, and receives the JSON value the script assigned to its
// top-level output variable.
//
// Script contract:
//
//   - The script reads its input from the global variable input (a map).
//   - The script MUST declare a top-level variable named output with :=, set to
//     any JSON-serialisable value (map, array, string, number, bool). A script
//     that never declares a top-level output fails with ErrNoOutput.
//
// Example:
//
//	text := import("text")
//	output := {upper: text.to_upper(input.name)}
//
// Safety: the engine is meant to run source loaded from the database, so every
// run is sandboxed — a wall-clock timeout, an allocation cap, a constant-object
// cap, and a source-size cap. The Tengo os module (filesystem, environment,
// process execution) is never exposed; only the curated modules in Options are
// importable.
//
// An Engine is stateless after construction (its options and module map are
// read-only) and is safe for concurrent use: each Run compiles and executes an
// independent script.
package scriptengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/d5/tengo/v2"
	"github.com/d5/tengo/v2/stdlib"
)

// Errors returned by Run. Callers can match them with errors.Is.
var (
	// ErrSourceTooLarge is returned when the script source exceeds
	// Options.MaxSourceBytes.
	ErrSourceTooLarge = errors.New("scriptengine: source exceeds maximum size")
	// ErrNoOutput is returned when the script completes without declaring a
	// top-level output value.
	ErrNoOutput = errors.New("scriptengine: script did not set an output value")
)

// inputGlobal is the variable name the script reads its JSON input from.
// outputGlobal is the top-level variable the script must declare.
const (
	inputGlobal  = "input"
	outputGlobal = "output"
)

// Options configures the sandbox. Use DefaultOptions and override selectively;
// New backfills any non-positive limit with its default so a zero value is safe.
type Options struct {
	// Timeout bounds a single Run's wall-clock execution. A script that loops
	// past it is aborted.
	Timeout time.Duration
	// MaxAllocs caps the number of objects the VM may allocate in one run,
	// guarding against runaway memory growth.
	MaxAllocs int64
	// MaxConstObjects caps the number of constant objects created at compile
	// time. Zero leaves Tengo's own default in place.
	MaxConstObjects int
	// MaxSourceBytes rejects scripts whose source is larger than this before
	// compiling. Zero disables the check.
	MaxSourceBytes int
	// Modules is the allowlist of Tengo standard-library modules a script may
	// import (for example "text", "math", "times", "json"). The "os" and
	// "exec" modules are always stripped, so filesystem, environment and
	// process access can never be granted. A nil or empty list means no module
	// may be imported.
	Modules []string
}

// DefaultOptions returns conservative limits and a pure, side-effect-free module
// allowlist suitable for transforming a JSON document.
//
// Note on determinism: the "times" module exposes times.now(), which is
// nondeterministic. Scripts whose output feeds a replayable pipeline (such as
// transaction reconciliation) should avoid it and take any needed clock from the
// input document instead.
func DefaultOptions() Options {
	return Options{
		Timeout:         250 * time.Millisecond,
		MaxAllocs:       1_000_000,
		MaxConstObjects: 500,
		MaxSourceBytes:  64 * 1024,
		Modules:         []string{"math", "text", "times", "fmt", "json", "enum", "base64", "hex"},
	}
}

// Engine runs sandboxed scripts. Construct it once with New and share it.
type Engine struct {
	opts    Options
	modules *tengo.ModuleMap
}

// New builds an Engine from opts, filling in defaults for any unset limit and
// stripping forbidden modules from the allowlist.
func New(opts Options) *Engine {
	defaults := DefaultOptions()
	if opts.Timeout <= 0 {
		opts.Timeout = defaults.Timeout
	}
	if opts.MaxAllocs <= 0 {
		opts.MaxAllocs = defaults.MaxAllocs
	}
	if opts.MaxSourceBytes <= 0 {
		opts.MaxSourceBytes = defaults.MaxSourceBytes
	}
	return &Engine{opts: opts, modules: stdlib.GetModuleMap(allowedModules(opts.Modules)...)}
}

// allowedModules removes modules that would breach the sandbox regardless of
// what a caller requested.
func allowedModules(names []string) []string {
	out := make([]string, 0, len(names))
	for _, name := range names {
		switch name {
		case "os", "exec":
			// Filesystem, environment and process access are never permitted.
			continue
		default:
			out = append(out, name)
		}
	}
	return out
}

// Run compiles and executes source against the JSON document input, returning
// the JSON value the script assigned to its top-level output variable.
//
// The input document is exposed to the script as the global variable named
// input; an empty or nil input becomes an empty object. Integer JSON numbers are
// preserved as 64-bit integers (not float64), so minor-unit money values
// round-trip without loss.
//
// Run never trusts the script: the returned JSON should still be strictly
// decoded and validated by the caller before use.
func (e *Engine) Run(ctx context.Context, source string, input json.RawMessage) (json.RawMessage, error) {
	if e.opts.MaxSourceBytes > 0 && len(source) > e.opts.MaxSourceBytes {
		return nil, ErrSourceTooLarge
	}

	inputObj, err := decodeInput(input)
	if err != nil {
		return nil, err
	}

	script := tengo.NewScript([]byte(source))
	script.SetImports(e.modules)
	script.SetMaxAllocs(e.opts.MaxAllocs)
	if e.opts.MaxConstObjects > 0 {
		script.SetMaxConstObjects(e.opts.MaxConstObjects)
	}
	if err := script.Add(inputGlobal, inputObj); err != nil {
		return nil, fmt.Errorf("scriptengine: add input: %w", err)
	}

	runCtx, cancel := context.WithTimeout(ctx, e.opts.Timeout)
	defer cancel()

	compiled, err := script.RunContext(runCtx)
	if err != nil {
		return nil, fmt.Errorf("scriptengine: run: %w", err)
	}

	outObj := compiled.Get(outputGlobal).Object()
	if outObj == nil || outObj == tengo.UndefinedValue {
		return nil, ErrNoOutput
	}

	goValue, err := tengoToGo(outObj, 0)
	if err != nil {
		return nil, fmt.Errorf("scriptengine: encode output: %w", err)
	}
	raw, err := json.Marshal(goValue)
	if err != nil {
		return nil, fmt.Errorf("scriptengine: marshal output: %w", err)
	}
	return raw, nil
}

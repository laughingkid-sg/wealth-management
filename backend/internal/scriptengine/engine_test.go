package scriptengine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// newTestEngine returns an engine with generous-but-bounded defaults, overriding
// only what a test needs.
func newTestEngine(t *testing.T, mutate func(*Options)) *Engine {
	t.Helper()
	opts := DefaultOptions()
	if mutate != nil {
		mutate(&opts)
	}
	return New(opts)
}

func run(t *testing.T, engine *Engine, source string, input string) (json.RawMessage, error) {
	t.Helper()
	return engine.Run(context.Background(), source, json.RawMessage(input))
}

func TestRunEchoesInput(t *testing.T) {
	engine := newTestEngine(t, nil)
	out, err := run(t, engine, `output := input`, `{"a":1,"b":"two","c":[1,2,3]}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !jsonEqual(t, out, `{"a":1,"b":"two","c":[1,2,3]}`) {
		t.Fatalf("unexpected output: %s", out)
	}
}

func TestRunTransformsFields(t *testing.T) {
	engine := newTestEngine(t, nil)
	source := `
text := import("text")
output := {
	merchant: text.to_upper(input.merchant),
	doubled: input.amount_minor * 2
}`
	out, err := run(t, engine, source, `{"merchant":"cafe","amount_minor":150}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !jsonEqual(t, out, `{"merchant":"CAFE","doubled":300}`) {
		t.Fatalf("unexpected output: %s", out)
	}
}

// TestRunPreservesInt64 proves that integer money values above 2^53 survive the
// JSON -> Tengo -> JSON round-trip exactly, i.e. they are never demoted to
// float64.
func TestRunPreservesInt64(t *testing.T) {
	engine := newTestEngine(t, nil)
	const big = "9007199254740993" // 2^53 + 1, not representable as float64
	out, err := run(t, engine, `output := {amount_minor: input.amount_minor}`, `{"amount_minor":`+big+`}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	number := decodeNumberField(t, out, "amount_minor")
	if number != big {
		t.Fatalf("int64 not preserved: got %s want %s", number, big)
	}
}

func TestRunPreservesFloat(t *testing.T) {
	engine := newTestEngine(t, nil)
	out, err := run(t, engine, `output := {rate: input.rate}`, `{"rate":1.5}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := decodeNumberField(t, out, "rate"); got != "1.5" {
		t.Fatalf("float not preserved: got %s", got)
	}
}

func TestRunEmptyInputBecomesEmptyObject(t *testing.T) {
	engine := newTestEngine(t, nil)
	source := `output := {count: len(input)}`
	for _, in := range []string{"", "   ", "{}"} {
		out, err := run(t, engine, source, in)
		if err != nil {
			t.Fatalf("run(%q): %v", in, err)
		}
		if !jsonEqual(t, out, `{"count":0}`) {
			t.Fatalf("run(%q): unexpected output %s", in, out)
		}
	}
}

func TestRunNoOutputIsError(t *testing.T) {
	engine := newTestEngine(t, nil)
	if _, err := run(t, engine, `x := input.a + 1`, `{"a":1}`); !errors.Is(err, ErrNoOutput) {
		t.Fatalf("want ErrNoOutput, got %v", err)
	}
}

func TestRunUndefinedOutputIsError(t *testing.T) {
	engine := newTestEngine(t, nil)
	if _, err := run(t, engine, `output := undefined`, `{}`); !errors.Is(err, ErrNoOutput) {
		t.Fatalf("want ErrNoOutput, got %v", err)
	}
}

func TestRunSourceTooLarge(t *testing.T) {
	engine := newTestEngine(t, func(o *Options) { o.MaxSourceBytes = 32 })
	source := "output := {padding: \"" + strings.Repeat("x", 200) + "\"}"
	if _, err := run(t, engine, source, `{}`); !errors.Is(err, ErrSourceTooLarge) {
		t.Fatalf("want ErrSourceTooLarge, got %v", err)
	}
}

func TestRunTimeoutAbortsInfiniteLoop(t *testing.T) {
	engine := newTestEngine(t, func(o *Options) { o.Timeout = 50 * time.Millisecond })
	start := time.Now()
	_, err := run(t, engine, `for { }`, `{}`)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("timeout did not abort promptly: %s", elapsed)
	}
}

func TestRunMaxAllocsAbortsRunaway(t *testing.T) {
	engine := newTestEngine(t, func(o *Options) { o.MaxAllocs = 1000 })
	source := `
acc := []
for i := 0; i < 1000000; i++ {
	acc = append(acc, i)
}
output := {n: len(acc)}`
	if _, err := run(t, engine, source, `{}`); err == nil {
		t.Fatal("expected allocation-cap error, got nil")
	}
}

func TestRunOSModuleNeverAvailable(t *testing.T) {
	// Even if a caller explicitly allow-lists "os", it must be stripped.
	engine := newTestEngine(t, func(o *Options) { o.Modules = append(o.Modules, "os") })
	if _, err := run(t, engine, `os := import("os"); output := {}`, `{}`); err == nil {
		t.Fatal("expected import(\"os\") to fail, got nil")
	}
}

func TestRunAllowedModuleImport(t *testing.T) {
	engine := newTestEngine(t, nil)
	out, err := run(t, engine, `math := import("math"); output := {root: math.sqrt(input.n)}`, `{"n":16}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := decodeNumberField(t, out, "root"); got != "4" {
		t.Fatalf("unexpected math result: %s", got)
	}
}

func TestRunInvalidInputJSON(t *testing.T) {
	engine := newTestEngine(t, nil)
	if _, err := run(t, engine, `output := input`, `{not json`); err == nil {
		t.Fatal("expected decode error, got nil")
	}
}

func TestRunCompileErrorSurfaces(t *testing.T) {
	engine := newTestEngine(t, nil)
	if _, err := run(t, engine, `output := (`, `{}`); err == nil {
		t.Fatal("expected compile error, got nil")
	}
}

func TestRunArrayOutput(t *testing.T) {
	engine := newTestEngine(t, nil)
	out, err := run(t, engine, `output := [input.a, input.b]`, `{"a":1,"b":2}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !jsonEqual(t, out, `[1,2]`) {
		t.Fatalf("unexpected output: %s", out)
	}
}

func TestRunNestedTooDeepIsError(t *testing.T) {
	engine := newTestEngine(t, nil)
	// Build a map nested deeper than maxOutputDepth.
	var b strings.Builder
	b.WriteString("output := ")
	for i := 0; i <= maxOutputDepth+1; i++ {
		b.WriteString("{n:")
	}
	b.WriteString("0")
	for i := 0; i <= maxOutputDepth+1; i++ {
		b.WriteString("}")
	}
	if _, err := run(t, engine, b.String(), `{}`); err == nil {
		t.Fatal("expected depth-limit error, got nil")
	}
}

func TestEngineConcurrentUse(t *testing.T) {
	engine := newTestEngine(t, nil)
	const workers = 16
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func() {
			_, err := run(t, engine, `output := {v: input.v * 2}`, `{"v":21}`)
			errs <- err
		}()
	}
	for i := 0; i < workers; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent run: %v", err)
		}
	}
}

// --- helpers ---

func jsonEqual(t *testing.T, got json.RawMessage, want string) bool {
	t.Helper()
	var a, b any
	if err := json.Unmarshal(got, &a); err != nil {
		t.Fatalf("unmarshal got %s: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &b); err != nil {
		t.Fatalf("unmarshal want %s: %v", want, err)
	}
	ga, _ := json.Marshal(a)
	gb, _ := json.Marshal(b)
	return bytes.Equal(ga, gb)
}

// decodeNumberField returns the raw numeric token for a top-level object field,
// so integer-vs-float representation can be asserted exactly.
func decodeNumberField(t *testing.T, raw json.RawMessage, field string) string {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	fields := map[string]json.Number{}
	if err := decoder.Decode(&fields); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	number, ok := fields[field]
	if !ok {
		t.Fatalf("field %q not present in %s", field, raw)
	}
	return number.String()
}

// SPDX-License-Identifier: Elastic-2.0

package ollamareq

import (
	"errors"
	"strings"
	"testing"
)

func opts(body map[string]any) map[string]any {
	o, _ := body["options"].(map[string]any)
	return o
}

// THE BUG: corral never set num_ctx, so every local model ran at ollama's
// default (~4096) regardless of what it was trained for. A challenger seat died
// with "request (15802 tokens) exceeds the available context size" on
// deepseek-r1:14b — a model trained for 131072. The measurement was skipped for
// want of a parameter nobody sent.
func TestDecorateSetsNumCtx(t *testing.T) {
	body := map[string]any{"model": "deepseek-r1:14b", "options": map[string]any{"temperature": 0.2}}
	Decorate(body, "deepseek-r1:14b")
	got, ok := opts(body)["num_ctx"]
	if !ok {
		t.Fatal("num_ctx not set — a normal-sized file will blow ollama's tiny default")
	}
	if n, _ := got.(int); n < 16384 {
		t.Errorf("num_ctx = %v, want at least 16384 — the prompt that failed in production was 15802 tokens", got)
	}
}

// It must not clobber options the caller already set.
func TestDecoratePreservesExistingOptions(t *testing.T) {
	body := map[string]any{"model": "x", "options": map[string]any{"temperature": 0.2}}
	Decorate(body, "x")
	if opts(body)["temperature"] != 0.2 {
		t.Errorf("temperature lost: %v", opts(body))
	}
}

// An explicit caller-set num_ctx wins — the operator knows their VRAM.
func TestDecorateRespectsAnExplicitNumCtx(t *testing.T) {
	body := map[string]any{"model": "x", "options": map[string]any{"num_ctx": 999}}
	Decorate(body, "x")
	if opts(body)["num_ctx"] != 999 {
		t.Errorf("num_ctx = %v, want the caller's 999 untouched", opts(body)["num_ctx"])
	}
}

// Missing options map must be created, not panic.
func TestDecorateCreatesOptionsWhenAbsent(t *testing.T) {
	body := map[string]any{"model": "x"}
	Decorate(body, "x")
	if opts(body)["num_ctx"] == nil {
		t.Error("num_ctx not set when the caller supplied no options map")
	}
}

// The thinking-mode suppression rides along, so the two call sites carry ONE
// call rather than two concerns duplicated four ways.
func TestDecorateSuppressesThinkingForReasoningModels(t *testing.T) {
	body := map[string]any{"model": "qwen3.5:9b"}
	Decorate(body, "qwen3.5:9b")
	if v, ok := body["think"]; !ok || v != false {
		t.Errorf("think = %v (present=%v), want false for a Qwen 3+ reasoning model", v, ok)
	}
}

func TestDecorateLeavesThinkUnsetForNonReasoningModels(t *testing.T) {
	body := map[string]any{"model": "qwen2.5-coder:14b"}
	Decorate(body, "qwen2.5-coder:14b")
	if _, ok := body["think"]; ok {
		t.Error("think must be OMITTED for a model with no reasoning pass — it is an ollama-specific field")
	}
}

// The default is a VRAM budget, not a preference. Measured on a 16.37GB card
// with a 9.5GB model: 16384 -> 12.0GB resident, 32768 -> 15.1GB (92% of the
// card). At 32768 two writer seats cannot both stay resident, each load evicts
// the other and re-reads ~9GB from disk, and a real audit died on the 180s HTTP
// timeout doing exactly that.
func TestDefaultNumCtxLeavesVRAMHeadroom(t *testing.T) {
	if DefaultNumCtx > 16384 {
		t.Errorf("DefaultNumCtx = %d: measured at 15.1GB of a 16.37GB card at 32768, which starves seat swapping", DefaultNumCtx)
	}
	// It must still clear the 15802-token prompt that originally failed.
	if DefaultNumCtx < 15802 {
		t.Errorf("DefaultNumCtx = %d does not clear the 15802-token prompt that motivated setting num_ctx at all", DefaultNumCtx)
	}
}

// The raw Ollama error names a token count and a limit but not the KNOB, and
// the limit is corral's own num_ctx rather than the model's trained maximum —
// so the obvious reading ("this model is too small") is the wrong one.
func TestContextOverflowHintNamesTheKnob(t *testing.T) {
	err := errors.New(`400 Bad Request: request (15802 tokens) exceeds the available context size (4096)`)
	hint := ContextOverflowHint(err)
	if hint == "" {
		t.Fatal("no hint produced for a context-size rejection")
	}
	for _, want := range []string{NumCtxEnv, "num_ctx"} {
		if !strings.Contains(hint, want) {
			t.Errorf("hint does not mention %q: %s", want, hint)
		}
	}
	if !strings.Contains(strings.ToLower(hint), "not the model") {
		t.Errorf("hint must correct the obvious wrong conclusion (that the MODEL is too small): %s", hint)
	}
}

func TestContextOverflowHintIgnoresOtherErrors(t *testing.T) {
	if h := ContextOverflowHint(errors.New("connection refused")); h != "" {
		t.Errorf("hint = %q for an unrelated error, want empty so callers can wrap unconditionally", h)
	}
	if h := ContextOverflowHint(nil); h != "" {
		t.Errorf("hint = %q for nil", h)
	}
}

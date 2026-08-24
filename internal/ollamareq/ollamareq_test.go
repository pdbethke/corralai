// SPDX-License-Identifier: Elastic-2.0

package ollamareq

import (
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

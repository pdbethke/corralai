// SPDX-License-Identifier: Elastic-2.0

// Package ollamareq decorates an Ollama /api/chat request body with the
// options every corral caller needs, in ONE place.
//
// There are two callers (internal/agentbackend's ollamaBackend and
// internal/llm's askOllama). Each concern added here would otherwise be
// duplicated at both, and this repo has already paid for that: one tick loop
// learned to recognize a terminal error while the other did not, purely
// because the judgment lived in two places.
package ollamareq

import (
	"os"
	"strconv"

	"github.com/pdbethke/corralai/internal/thinkmode"
)

// DefaultNumCtx is the context window corral asks Ollama for.
//
// WHY IT MUST BE SET AT ALL. Ollama does NOT use a model's trained context by
// default — it uses its own small default (~4096) unless the request says
// otherwise. corral never said otherwise, so every local model ran at that
// default no matter what it could handle. A challenger seat died with
// "request (15802 tokens) exceeds the available context size" on a model
// trained for 131072: the measurement was lost for want of a parameter.
//
// WHY NOT THE MODEL'S TRAINED MAXIMUM. num_ctx sizes the KV cache, which is
// VRAM. Asking a 14B model for its full 131072 would exhaust a 16GB card and
// push the model to CPU offload, which is far worse than a small window — a
// measured 53x throughput collapse on this hardware. 32768 clears the prompts
// corral actually sends (the failing one was 15802 tokens) with headroom, at a
// KV cost a consumer card absorbs.
//
// Override with CORRALAI_OLLAMA_NUM_CTX when the operator knows their VRAM.
const DefaultNumCtx = 32768

// NumCtxEnv is the override variable.
const NumCtxEnv = "CORRALAI_OLLAMA_NUM_CTX"

// numCtx resolves the window: the env override when it parses to a positive
// integer, else DefaultNumCtx. A malformed value falls back rather than
// failing the run — a typo in an env var must not kill an audit.
func numCtx() int {
	if v := os.Getenv(NumCtxEnv); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return DefaultNumCtx
}

// Decorate adds corral's standard options to an Ollama chat body in place.
//
// It never overwrites a value the caller set explicitly: an operator who
// passes their own num_ctx knows their VRAM better than this package does.
func Decorate(body map[string]any, model string) {
	if body == nil {
		return
	}

	opts, _ := body["options"].(map[string]any)
	if opts == nil {
		opts = map[string]any{}
		body["options"] = opts
	}
	if _, set := opts["num_ctx"]; !set {
		opts["num_ctx"] = numCtx()
	}

	// Qwen 3+ and DeepSeek-R1 reason into a separate `thinking` field and hand
	// back EMPTY content when the budget runs out mid-reasoning — HTTP 200, no
	// error. `think` is Ollama-specific, so it is set only for the families
	// actually probed (see thinkmode).
	if _, set := body["think"]; !set && thinkmode.Suppress(model) {
		body["think"] = false
	}
}

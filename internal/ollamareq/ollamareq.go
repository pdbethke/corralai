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
	"fmt"
	"os"
	"strconv"
	"strings"

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
// WHY NOT MUCH LARGER. num_ctx sizes the KV cache, which is VRAM. Measured on
// deepseek-r1:14b (a 9.5GB model) on a 16.37GB card:
//
//	num_ctx   resident VRAM
//	  4096      9.5 GB
//	 16384     12.0 GB
//	 32768     15.1 GB   <- 92% of the card
//
// At 32768 the model plus a ~1.2GB desktop fills the card. That is survivable
// for ONE model and ruinous for a run with two writer seats: each load evicts
// the other and re-reads ~9GB from disk, and a real audit died on the 180s HTTP
// timeout doing exactly that. 16384 leaves ~4GB of headroom, keeps seat
// swapping affordable, and still clears the 15802-token prompt that originally
// failed.
//
// THE HEADROOM IS THIN, DELIBERATELY SO. 16384 exceeds that prompt by only ~600
// tokens, and prompt size scales with the audited file — a larger file WILL
// need more. That is what CORRALAI_OLLAMA_NUM_CTX is for, and why exceeding the
// window must produce an error that names it (see ContextOverflowHint).
// Raising it is correct on a card with the VRAM to spare; the default cannot
// assume that card.
const DefaultNumCtx = 16384

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

// ContextOverflowHint turns Ollama's context-size rejection into something an
// operator can act on. The raw error names a token count and a limit but not
// the knob, and the knob is not discoverable: the limit is Ollama's own default
// (or corral's DefaultNumCtx), never the model's trained maximum, so "use a
// bigger model" is the wrong conclusion and the obvious one.
//
// Returns "" for any other error, so callers can wrap unconditionally.
func ContextOverflowHint(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "context size") && !strings.Contains(msg, "context length") {
		return ""
	}
	return fmt.Sprintf("the prompt exceeded the context window corral asked Ollama for (num_ctx=%d). "+
		"This is NOT the model's limit — Ollama ignores a model's trained context unless the request sets num_ctx, "+
		"and corral's default is sized to leave VRAM headroom for seat swapping on a consumer card. "+
		"Raise it with %s=<tokens> if your GPU has the VRAM (the KV cache grows with it), "+
		"or audit a smaller file", numCtx(), NumCtxEnv)
}

// WrapErr appends the context-overflow hint when it applies, and returns err
// unchanged otherwise. Both Ollama call sites wrap through this so the guidance
// cannot exist in one and be missing from the other — the failure mode this
// package was created to end.
func WrapErr(err error) error {
	hint := ContextOverflowHint(err)
	if hint == "" {
		return err
	}
	return fmt.Errorf("%w — %s", err, hint)
}

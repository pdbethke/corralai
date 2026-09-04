// SPDX-License-Identifier: Elastic-2.0

// Package agentbackend holds the tool-calling LLM backends that drive a
// corral worker: ollama, any OpenAI-compatible endpoint (OpenAI itself,
// Gemini, OpenRouter, local vLLM/LM Studio/llama.cpp), and Anthropic's native
// Messages API. It used to live unexported inside cmd/corral-agent/backend.go
// (a `main` package), which meant only corral-agent could construct one. It
// is importable now so `corral certify --local` can build the exact same
// backends (including the decorrelation default of two Claude models off one
// ANTHROPIC_API_KEY: FromEnv().(ModelSwitcher).WithModel("claude-sonnet-5")
// / .WithModel("claude-haiku-4-5")) without shelling out to corral-agent.
//
// corral-agent itself is unchanged behaviorally: it now calls FromEnv() and
// AsChatter() here instead of the old local newBackend()/backendChatter.
package agentbackend

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pdbethke/corralai/internal/agentworker"
	"github.com/pdbethke/corralai/internal/creds"
	"github.com/pdbethke/corralai/internal/ollamareq"
)

// ErrModelUnreachable is returned by a Backend when the model endpoint responds
// with HTTP 404 or is connection-refused — the model name is wrong, the endpoint
// was pulled, or the server isn't running. Callers use errors.Is to detect
// this and release the claim so the reaper can reassign to a healthy agent.
var ErrModelUnreachable = errors.New("model unreachable")

// Backend is the LLM a worker drives itself with. corral-agent (and
// corral certify --local) is model-agnostic by design: the coordination loop
// is identical regardless of what's behind this interface — that's the whole
// "any model, any agent" point. Ollama is just the zero-cost local default;
// it is NOT hard-wired in.
//
// Select with MODEL_BACKEND:
//   - ollama   (default)  — local, free. OLLAMA_URL.
//   - openai             — ANY OpenAI-compatible endpoint via OPENAI_BASE_URL +
//     OPENAI_API_KEY. That covers a lot on purpose:
//   - Gemini:     OPENAI_BASE_URL=https://generativelanguage.googleapis.com/v1beta/openai
//   - OpenRouter: OPENAI_BASE_URL=https://openrouter.ai/api/v1  (→ Claude, Gemini, anything)
//   - OpenAI:     (default base)
//   - local:      vLLM / LM Studio / llama.cpp servers
type Backend interface {
	Chat(messages []Message, tools []any) (Message, error)
}

// Message is one turn in a tool-calling chat exchange, and ToolCall is one
// function-call request inside a Message. These mirror the wire shape every
// concrete Backend below actually speaks (Ollama's /api/chat and the
// OpenAI-compatible /v1/chat/completions both nest a tool call under
// "function": {"name","arguments"}) — internal/agentworker.Message/ToolCall
// use a flatter shape suited to RunRole's needs, so this package keeps its
// own type and AsChatter converts at the boundary rather than reusing
// agentworker's (that would silently change the JSON this package sends to
// real model endpoints).
type Message struct {
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// Usage is what the provider reported for the call that PRODUCED this
	// message; it is meaningless on a message being SENT. `json:"-"` keeps it
	// out of the request body entirely — a stray "usage" field on an outbound
	// message is the kind of thing a strict endpoint rejects.
	Usage Usage `json:"-"`
}

// ToolCall is one function-call request inside a Message: the tool name and
// its raw (not-yet-decoded) argument object.
type ToolCall struct {
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

// chatter adapts a Backend to agentworker.Chatter so a caller can hand its
// already-configured backend (ollama/openai/anthropic, with per-task
// WithModel already applied) straight to agentworker.RunRole, without
// changing the Backend interface or any of its implementations. It only
// translates message/tool-call shapes (Message/ToolCall <-> agentworker.
// Message/ToolCall) — no behavior of its own.
type chatter struct{ b Backend }

// AsChatter adapts b to agentworker.Chatter. Use it wherever a Backend needs
// to be handed to agentworker.RunRole (corral-agent's queue loop and the
// future --local orchestrator both do this).
func AsChatter(b Backend) agentworker.Chatter { return chatter{b} }

func (c chatter) Chat(messages []agentworker.Message, tools []any) (agentworker.Message, error) {
	out, _, err := chatConverting(c.b, messages, tools)
	return out, err
}

// chatConverting is the whole of chatter.Chat plus the reply's reported token
// usage, which the agentworker.Message shape has no room for. Shared with
// meteredChatter (see usage.go) so the message/tool-call translation exists in
// exactly one place: two copies of this conversion would drift, and the drift
// would show up as tool calls silently losing their arguments.
func chatConverting(b Backend, messages []agentworker.Message, tools []any) (agentworker.Message, Usage, error) {
	oms := make([]Message, len(messages))
	for i, m := range messages {
		oms[i] = Message{Role: m.Role, Content: m.Content}
		if len(m.ToolCalls) > 0 {
			tcs := make([]ToolCall, len(m.ToolCalls))
			for j, tc := range m.ToolCalls {
				tcs[j].Function.Name = tc.Name
				tcs[j].Function.Arguments = tc.Arguments
			}
			oms[i].ToolCalls = tcs
		}
	}
	m, err := b.Chat(oms, tools)
	if err != nil {
		// m may still carry usage for a call the provider billed before
		// failing, so it is returned rather than dropped with the error.
		return agentworker.Message{}, m.Usage, err
	}
	out := agentworker.Message{Role: m.Role, Content: m.Content}
	if len(m.ToolCalls) > 0 {
		out.ToolCalls = make([]agentworker.ToolCall, len(m.ToolCalls))
		for j, tc := range m.ToolCalls {
			out.ToolCalls[j] = agentworker.ToolCall{Name: tc.Function.Name, Arguments: tc.Function.Arguments}
		}
	}
	return out, m.Usage, nil
}

// ModelSwitcher is an optional capability: backends that can serve more than
// one model implement it so a caller can honor a task's gate-earned Model
// assignment for the duration of one task, without changing the Backend
// interface (and so without touching every test double that only implements
// Chat). WithModel returns a new Backend value configured for the given
// model — it does not mutate the receiver, so the worker's default backend
// is unaffected once the task is done.
//
// A backend that does NOT implement this (a genuinely single-model harness,
// or a test double) cannot be told to serve a different model at all; the
// caller keeps running its own model and records the mismatch instead of
// silently pretending it ran the assigned one.
type ModelSwitcher interface {
	Model() string
	WithModel(model string) Backend
}

// FromEnv constructs the Backend selected by MODEL_BACKEND, reading the
// vendor-specific env vars exactly as corral-agent always has. It is the
// exported form of what used to be corral-agent's unexported newBackend().
func FromEnv() Backend {
	model := env("AGENT_MODEL", "qwen2.5-coder:7b")
	switch env("MODEL_BACKEND", "ollama") {
	case "gemini":
		// Gemini speaks the OpenAI wire format but is NOT OpenAI: it has its
		// own endpoint and its own key. It used to share the arm below, so
		// MODEL_BACKEND=gemini read the OpenAI key and defaulted to
		// api.openai.com — pointing "the gemini backend" at the wrong vendor,
		// unauthenticated. ForModel had the correct routing all along, so the
		// two disagreed about what "gemini" means depending on which door you
		// came in: a cross-vendor CRITIC worked while aiming a WHOLE run at
		// Gemini silently did not.
		return &openaiBackend{base: geminiBase(true), key: geminiKey(), model: model}
	case "openai", "openrouter":
		// Same per-model wire choice as ForModel — a Codex model named through
		// MODEL_BACKEND=openai must reach /responses, not /chat/completions.
		// OpenRouter fronts many vendors on the chat-completions shape, so a
		// "-codex" name there is left alone unless the operator points
		// OPENAI_BASE_URL at OpenAI itself.
		if env("MODEL_BACKEND", "") == "openai" {
			return newOpenAIBackend(agentSecret("OPENAI_API_KEY"), model)
		}
		// MODEL_BACKEND=openrouter: the docs said "one OpenRouter key
		// covers gemini + gpt + claude" and named OPENROUTER_API_KEY in
		// three places — and nothing read it. The arm read OPENAI_API_KEY
		// and defaulted the base to api.openai.com, so the documented setup
		// sent an unauthenticated request to OpenAI. OpenRouter's own
		// variable and endpoint come first; the OpenAI ones stay as the
		// fallback for operators who configured it that way.
		return &openaiBackend{
			base:  env("OPENROUTER_BASE_URL", env("OPENAI_BASE_URL", "https://openrouter.ai/api/v1")),
			key:   firstSecret("OPENROUTER_API_KEY", "OPENAI_API_KEY"),
			model: model,
		}
	case "anthropic", "claude":
		return &anthropicBackend{
			base:  env("ANTHROPIC_BASE_URL", "https://api.anthropic.com"),
			key:   agentSecret("ANTHROPIC_API_KEY"),
			model: model, // e.g. claude-sonnet-4-6 / claude-haiku-4-5-20251001 / claude-opus-4-8
		}
	default: // ollama
		return &ollamaBackend{url: env("OLLAMA_URL", "http://127.0.0.1:11434"), model: model}
	}
}

// ForModelOrLocal is ForModel with a LOCAL fallback: a model name that matches
// no cloud vendor prefix is an Ollama model, served by the local daemon.
//
// ForModel alone is the cross-vendor CLOUD path and refuses such a name. That
// is right for a cross-vendor critic, and wrong for any seat an operator may
// legitimately fill with a local model — which is every seat corral has. A
// caller that wants "whatever backend serves this model" wants this function;
// a caller that specifically requires a cloud vendor still wants ForModel.
func ForModelOrLocal(model string) (Backend, error) {
	if VendorOf(model) == "" {
		return NewOllamaBackend(env("OLLAMA_URL", "http://127.0.0.1:11434"), model), nil
	}
	return ForModel(model)
}

// NewOllamaBackend builds an ollama Backend directly (bypassing MODEL_BACKEND
// selection) — used by tests, and available to any caller that wants to talk
// to a specific Ollama endpoint/model without going through env vars.
func NewOllamaBackend(url, model string) Backend {
	return &ollamaBackend{url: url, model: model}
}

// geminiBase and geminiKey are the SINGLE resolution of Gemini's endpoint and
// credential, shared by FromEnv (MODEL_BACKEND=gemini, every role on Gemini)
// and ForModel (a cross-vendor gemini-* role on another base backend). They
// were duplicated in only one of the two, which is exactly how those paths
// came to disagree — factoring them means a future change cannot fix one door
// and leave the other pointing at api.openai.com.
//
// The credential falls back through GEMINI_API_KEY, then GOOGLE_API_KEY, then
// OPENAI_API_KEY. That last hop is deliberate BACK-COMPAT, not sloppiness: the
// OpenAI variable was the only thing that made MODEL_BACKEND=gemini reach any
// endpoint at all before this, so an operator already configured that way must
// keep working.
//
// The OPENAI_BASE_URL hop applies to the PINNED door only (pinned true:
// MODEL_BACKEND=gemini, where that variable was the operator's way of
// naming Gemini's endpoint before CORRALAI_GEMINI_BASE_URL existed). On the
// cross-vendor door it does not: an unpinned run with OPENAI_BASE_URL set
// for its gpt seats used to send its gemini-* seat — carrying the GOOGLE
// key — to whatever the gpt gateway does with that name.
func geminiBase(pinned bool) string {
	if base := os.Getenv("CORRALAI_GEMINI_BASE_URL"); base != "" {
		return base
	}
	if base := os.Getenv("OPENAI_BASE_URL"); pinned && base != "" {
		return base
	}
	return "https://generativelanguage.googleapis.com/v1beta/openai"
}

// firstSecret resolves the first non-empty credential among names.
func firstSecret(names ...string) string {
	for _, n := range names {
		if v := agentSecret(n); v != "" {
			return v
		}
	}
	return ""
}

func geminiKey() string {
	if key := agentSecret("GEMINI_API_KEY"); key != "" {
		return key
	}
	if key := agentSecret("GOOGLE_API_KEY"); key != "" {
		return key
	}
	return agentSecret("OPENAI_API_KEY")
}

// ForModel infers the cloud vendor from model's name prefix and builds the
// matching backend, reading THAT vendor's base URL + key from env/keystore —
// independent of MODEL_BACKEND/FromEnv. It exists for exactly one case: a
// `corral certify --local` role (the decorrelated test-critic, by design)
// that needs to run on a DIFFERENT vendor than the run's base backend, e.g. a
// Gemini critic grading a Claude writer/mutant-generator. It fails closed —
// a clear, actionable error naming the missing env var — rather than falling
// back to any other backend, because a silent fallback here would silently
// collapse the cross-vendor decorrelation the caller asked for.
//
// ForModel is NOT a general-purpose backend constructor: it only recognizes
// hosted-cloud model name prefixes. Local/ollama models (and any explicit
// MODEL_BACKEND setup) are unaffected — they keep going through FromEnv().
func ForModel(model string) (Backend, error) {
	switch {
	case hasAnyPrefix(model, "claude-", "opus-", "sonnet-", "haiku-", "fable-"):
		key := agentSecret("ANTHROPIC_API_KEY")
		if key == "" {
			return nil, fmt.Errorf("agentbackend: ForModel: model %q needs an Anthropic key — set ANTHROPIC_API_KEY", model)
		}
		return &anthropicBackend{
			base:  env("ANTHROPIC_BASE_URL", "https://api.anthropic.com"),
			key:   key,
			model: model,
		}, nil
	case hasAnyPrefix(model, "gemini-"):
		key := geminiKey()
		if key == "" {
			return nil, fmt.Errorf("agentbackend: ForModel: model %q needs a Google key — set GEMINI_API_KEY (or GOOGLE_API_KEY)", model)
		}
		return &openaiBackend{base: geminiBase(false), key: key, model: model}, nil
	case hasAnyPrefix(model, "gpt-", "o1-", "o3-"):
		key := agentSecret("OPENAI_API_KEY")
		if key == "" {
			return nil, fmt.Errorf("agentbackend: ForModel: model %q needs an OpenAI key — set OPENAI_API_KEY", model)
		}
		// Not every OpenAI model speaks the same wire format: the Codex models
		// are served on the Responses API only. newOpenAIBackend picks by model
		// rather than by vendor, so naming one no longer routes to an endpoint
		// that cannot serve it.
		return newOpenAIBackend(key, model), nil
	default:
		return nil, fmt.Errorf("agentbackend: ForModel: cannot infer a cloud vendor for model %q (this path is for cross-vendor cloud critics; local/ollama models use the base backend)", model)
	}
}

// VendorOf reports which cloud vendor ForModel would route model to
// ("anthropic"/"google"/"openai"), or "" if none matches (e.g. a local/ollama
// model name) — used by callers (certify --local's localChatterFor) that need
// to know a critic model's vendor differs from the base backend's BEFORE
// calling ForModel (e.g. to decide whether cross-vendor routing even
// applies), without duplicating the prefix table.
func VendorOf(model string) string {
	switch {
	case hasAnyPrefix(model, "claude-", "opus-", "sonnet-", "haiku-", "fable-"):
		return "anthropic"
	case hasAnyPrefix(model, "gemini-"):
		return "google"
	case hasAnyPrefix(model, "gpt-", "o1-", "o3-"):
		return "openai"
	default:
		return ""
	}
}

func hasAnyPrefix(s string, prefixes ...string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

// credsStoreOnce/credsStore are the memoized creds.Store used by agentSecret.
// It is opened once, lazily, on first secret resolution — not at package
// init — so tests that set CORRAL_CREDS_DIR/CREDENTIALS_DIRECTORY before the
// first call see a store scoped to their temp dir.
var (
	credsStoreOnce sync.Once
	credsStore     *creds.Store

	// scrubbedSecrets holds the resolved value of any secret that
	// ScrubSecretEnv has already unset from the environment. Once a name
	// lands here, agentSecret answers from this cache instead of
	// re-querying the store — after scrubbing, the env tier of the chain is
	// gone by design, so a fresh store.Get would silently lose an
	// env-sourced value (or fall through to a stale keyring/age entry).
	// Only the names ScrubSecretEnv scrubs ever populate this.
	scrubbedSecrets sync.Map // name string -> value string
)

// Secret resolves a named secret (provider API key, brain bearer token)
// through the creds keystore chain (env → OS keyring → age file). Env always
// wins inside the chain. Degrade-never-block: any resolve error (or unset
// name) returns "" rather than aborting startup — callers already handle ""
// as "no key configured."
func Secret(name string) string { return agentSecret(name) }

func agentSecret(name string) string {
	if v, ok := scrubbedSecrets.Load(name); ok {
		return v.(string)
	}
	credsStoreOnce.Do(func() {
		st, err := creds.Open()
		if err != nil {
			fmt.Fprintf(os.Stderr, "agentbackend: open creds store: %v\n", err)
			return
		}
		credsStore = st
	})
	if credsStore == nil {
		return ""
	}
	v, ok, err := credsStore.Get(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agentbackend: resolve %s from creds store: %v\n", name, err)
		return ""
	}
	if !ok {
		return ""
	}
	return v
}

// ScrubSecretEnv resolves-and-caches, then unsets, the sensitive env vars
// resolved via Secret — the full creds.CanonicalNames set (OPENAI_API_KEY,
// GEMINI_API_KEY, ANTHROPIC_API_KEY, OPENROUTER_API_KEY,
// CORRALAI_BRAIN_KEY) — once, at startup. It must run AFTER anything that
// needs the env-sourced value has had a chance to resolve it (FromEnv() runs
// first and captures provider keys into the Backend struct directly), but
// callers of Secret("CORRALAI_BRAIN_KEY") happen later, on demand, during the
// run loop — so the resolved brain token is cached here and served from
// cache post-scrub. This keeps provider keys and the brain bearer token out
// of the environment of any child process the caller spawns via jailed exec.
func ScrubSecretEnv() { scrubSecretEnv() }

func scrubSecretEnv() {
	for _, name := range creds.CanonicalNames {
		v := agentSecret(name) // resolve (env still present) before scrubbing
		scrubbedSecrets.Store(name, v)
		os.Unsetenv(name)
	}
}

// llmHTTPTimeout is the per-request timeout for a model call, overridable via
// AGENT_LLM_TIMEOUT_SECONDS.
//
// The old comment here said 180s "is fine for hosted/frontier models". That is
// FALSIFIED: a hosted challenger seat was sent a 103130-token prompt (a
// 328-line file plus its 10 survivors) and exceeded 180s "while awaiting
// headers", losing the measurement. Prompt size scales with the audited file
// and with how much the dev suite missed, so the worst case is a big file whose
// tests are bad — exactly the case an audit exists for.
//
// 300s covered that case — and was then falsified in turn, by the OTHER end of
// the hardware range. Auditing spf13/afero with a 9B model on one 16GB consumer
// GPU, 8 of 16 files died on `context deadline exceeded` at 300s. Nothing was
// hanging: re-running the same commit, same models and same build with
// AGENT_LLM_TIMEOUT_SECONDS=900 graded 12 of 16 and raised that panel's
// proven-missed count from 36 to 53.
//
// The pattern in both falsifications is the same. A local model's prefill on
// consumer hardware, and a hosted model's on a large prompt, are both slow in a
// way this default was not sized for — and the failure it produces names the
// TRANSPORT ("context deadline exceeded", surfaced as executor-error) and never
// the cause, so an operator has no reason to suspect a knob.
//
// 600s is chosen to cover the measured local case with margin rather than to be
// generous: a request that genuinely hangs still fails, just not one that was
// only slow. Raise AGENT_LLM_TIMEOUT_SECONDS further for a big model on a slow
// card; the 900s used above is a reasonable ceiling for a 16GB GPU.
func llmHTTPTimeout() time.Duration {
	if v := strings.TrimSpace(os.Getenv("AGENT_LLM_TIMEOUT_SECONDS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 600 * time.Second
}

var httpc = &http.Client{Timeout: llmHTTPTimeout()}

func postJSON(url string, hdr map[string]string, body, out any) error {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", url, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := httpc.Do(req)
	if err != nil {
		// Connection-refused / no-route means the backend process or its host is
		// unreachable, not merely that this request failed transiently. Classify so
		// the task loop can release the claim instead of spinning.
		if strings.Contains(err.Error(), "connection refused") ||
			strings.Contains(err.Error(), "no such host") {
			return fmt.Errorf("%w: %s", ErrModelUnreachable, err.Error())
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(resp.Body)
		e := fmt.Errorf("%s: %s", resp.Status, oneline(string(msg)))
		if resp.StatusCode == http.StatusNotFound {
			// 404 from any backend means the model name is wrong or the endpoint
			// was pulled. Wrap so callers use errors.Is(err, ErrModelUnreachable).
			return fmt.Errorf("%w: %w", ErrModelUnreachable, e)
		}
		return e
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// oneline collapses a possibly-multiline error/response body into a single
// line for compact logging, truncated to ~110 chars — verbatim copy of
// cmd/corral-agent's helper (same package boundary reasoning as Message
// above: this package must not depend on cmd/corral-agent).
func oneline(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 110 {
		s = s[:110] + "…"
	}
	return s
}

// ---- Ollama (/api/chat) ----

type ollamaBackend struct{ url, model string }

func (b *ollamaBackend) Model() string { return b.model }
func (b *ollamaBackend) WithModel(model string) Backend {
	c := *b
	c.model = model
	return &c
}

func (b *ollamaBackend) Chat(messages []Message, tools []any) (Message, error) {
	var out struct {
		Message Message `json:"message"`
	}
	body := map[string]any{
		"model": b.model, "messages": messages, "tools": tools, "stream": false,
		"options": map[string]any{"temperature": 0.2},
	}
	// One decorator owns corral's Ollama request options (num_ctx, and the
	// think-suppression that keeps a reasoning model from returning empty
	// content). Both concerns used to be per-call-site, and this repo has
	// already paid for duplicated judgment at these two loops.
	ollamareq.Decorate(body, b.model)
	err := postJSON(b.url+"/api/chat", nil, body, &out)
	// A context-size rejection names a limit that is corral's own num_ctx, not
	// the model's trained maximum, so the obvious reading is the wrong one.
	return out.Message, ollamareq.WrapErr(err)
}

// ---- OpenAI-compatible (/v1/chat/completions) — also Gemini, OpenRouter, local ----

type openaiBackend struct{ base, key, model string }

func (b *openaiBackend) Model() string { return b.model }
func (b *openaiBackend) WithModel(model string) Backend {
	c := *b
	c.model = model
	return &c
}

func (b *openaiBackend) Chat(messages []Message, tools []any) (Message, error) {
	var out struct {
		Choices []struct {
			Message struct {
				Content   string     `json:"content"`
				ToolCalls []ToolCall `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		// Reported by OpenAI, Gemini's OpenAI-compatible endpoint and
		// OpenRouter alike. Absent from a provider that does not report it,
		// which reads as zero — never as a guess.
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			// PromptTokensDetails.CachedTokens is the cached half of
			// prompt_tokens — reported by OpenAI, by Gemini's
			// OpenAI-compatible endpoint (its implicit cache needs nothing
			// sent to be used; it only has to be READ back) and by
			// OpenRouter. A POINTER so a provider that does not report it
			// stays NULL rather than claiming a measured zero.
			PromptTokensDetails *struct {
				CachedTokens *int64 `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	hdr := map[string]string{}
	if b.key != "" {
		hdr["Authorization"] = "Bearer " + b.key
	}
	if err := postJSON(b.base+"/chat/completions", hdr, map[string]any{
		"model": b.model, "messages": messages, "tools": tools, "temperature": 0.2,
	}, &out); err != nil {
		return Message{}, err
	}
	usage := Usage{InputTokens: out.Usage.PromptTokens, OutputTokens: out.Usage.CompletionTokens}
	if d := out.Usage.PromptTokensDetails; d != nil && d.CachedTokens != nil {
		usage.CachedInputTokens = d.CachedTokens
	}
	if len(out.Choices) == 0 {
		// No choice returned, but the call still happened and may have been
		// billed — carry the usage rather than reporting a free call.
		return Message{Usage: usage}, nil
	}
	m := out.Choices[0].Message
	return Message{Role: "assistant", Content: m.Content, ToolCalls: m.ToolCalls, Usage: usage}, nil
}

// ---- Anthropic (Claude, Messages API with native tool use) ----
//
// Select with MODEL_BACKEND=anthropic, ANTHROPIC_API_KEY=sk-ant-…, and an
// AGENT_MODEL like claude-sonnet-4-6. Claude's native tool_use is reliable, which
// is what makes a real mission converge (clean tool calls, fewer fumbles).

type anthropicBackend struct{ base, key, model string }

func (b *anthropicBackend) Model() string { return b.model }
func (b *anthropicBackend) WithModel(model string) Backend {
	c := *b
	c.model = model
	return &c
}

func (b *anthropicBackend) Chat(messages []Message, tools []any) (Message, error) {
	// Anthropic takes `system` as a top-level field; everything else must be a
	// user/assistant turn (it has no "tool" role — tool results arrive as the
	// loop's "user" messages). Merge consecutive same-role turns and never send
	// empty content (the API rejects it).
	var sys strings.Builder
	var msgs []map[string]any
	for _, m := range messages {
		if m.Role == "system" {
			if sys.Len() > 0 {
				sys.WriteString("\n\n")
			}
			sys.WriteString(m.Content)
			continue
		}
		role := "user"
		if m.Role == "assistant" {
			role = "assistant"
		}
		content := m.Content
		if strings.TrimSpace(content) == "" {
			content = "."
		}
		if n := len(msgs); n > 0 && msgs[n-1]["role"] == role {
			msgs[n-1]["content"] = msgs[n-1]["content"].(string) + "\n\n" + content
		} else {
			msgs = append(msgs, map[string]any{"role": role, "content": content})
		}
	}
	// Convert the OpenAI-style function tools to Anthropic's {name, description,
	// input_schema} shape (input_schema IS the function's JSON-schema parameters).
	var atools []map[string]any
	for _, t := range tools {
		tm, _ := t.(map[string]any)
		fn, _ := tm["function"].(map[string]any)
		if fn == nil {
			continue
		}
		atools = append(atools, map[string]any{
			"name": fn["name"], "description": fn["description"], "input_schema": fn["parameters"],
		})
	}
	// No `temperature`: newer Anthropic models (Claude Sonnet 5+) REJECT it with
	// a 400 ("temperature is deprecated for this model"), and older models are
	// fine with the API default. Sending it broke every cross-vendor run whose
	// writer/mutant-generator was a current Claude.
	body := map[string]any{
		"model": b.model, "max_tokens": 4096, "messages": msgs,
	}
	if sys.Len() > 0 {
		// The system prompt as a CACHEABLE content block, not a bare string.
		//
		// Anthropic caches nothing unless a request asks it to, and the
		// writer seat now makes one call PER SURVIVOR against one file. Those
		// calls share a long, byte-identical prefix — the writer system
		// prompt, the goal, the whole TARGET FILE, its signature surface and
		// the harness exemplar (advpool.renderTestWriterPrefix) — which
		// reaches this backend as the request's SYSTEM half, because
		// queue.TaskSpec.System carries it and agentworker.RunRoleWithSystem
		// sends it as its own turn. That routing is load-bearing: folded into
		// the user message (as advpool's joinPrompt did before the fan-out)
		// there would be no system field on the request at all and this block
		// would have nothing to attach to. Marking it ephemeral is what turns
		// N calls over one file into one full-price prompt and N-1 cache
		// reads. The saving is then RECORDED, never assumed, from the
		// response's own cache_read_input_tokens.
		//
		// Applied to every Anthropic call, not only the writer's: the block
		// is a request for reuse, the provider decides whether there is
		// anything to reuse, and a prefix that never repeats simply reads
		// back as no cached tokens. Gemini needs no equivalent — its implicit
		// cache matches a repeated prefix with nothing sent — and the
		// OpenAI-compatible wire has no per-block control to send either, so
		// this is the one backend with a request-side change to make.
		body["system"] = []map[string]any{{
			"type":          "text",
			"text":          sys.String(),
			"cache_control": map[string]any{"type": "ephemeral"},
		}}
	}
	if len(atools) > 0 {
		body["tools"] = atools
	}

	var out struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
			// CacheReadInputTokens is the half of this prompt Anthropic
			// served from the cache the cache_control block above asks for.
			// A POINTER: a response that omits it has reported nothing, not
			// a miss — see Usage.CachedInputTokens.
			CacheReadInputTokens *int64 `json:"cache_read_input_tokens"`
			// The WRITE half: tokens Anthropic put INTO the cache on this
			// call, billed at 1.25x. Also a pointer — a response that
			// reports a read and no write has not reported a zero write.
			CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	hdr := map[string]string{"x-api-key": b.key, "anthropic-version": "2023-06-01"}
	if err := postJSON(b.base+"/v1/messages", hdr, body, &out); err != nil {
		return Message{}, err
	}
	// NORMALISED, not copied. Anthropic's three input counters are DISJOINT:
	// `input_tokens` is the UNCACHED remainder of the prompt, and the two
	// cache counters are the rest of that same prompt. Every other wire
	// corral speaks reports a prompt total that already contains its cached
	// half (OpenAI's prompt_tokens, which is also what Gemini's
	// OpenAI-compatible endpoint sends), so copying the remainder into
	// Usage.InputTokens would give one column two different meanings
	// depending on which vendor sat the seat — a per-survivor Anthropic
	// writer would look CHEAPER in tokens than a batched one purely because
	// its prompt cached well, which is the opposite of the truth.
	//
	// So InputTokens is the whole prompt, every provider, and the two cache
	// counters remain the breakdown OF it — which is what
	// Usage.CachedInputTokens' doc says and what cost_line.go's `(N cached)`
	// renders against.
	in := out.Usage.InputTokens
	if out.Usage.CacheReadInputTokens != nil {
		in += int(*out.Usage.CacheReadInputTokens)
	}
	if out.Usage.CacheCreationInputTokens != nil {
		in += int(*out.Usage.CacheCreationInputTokens)
	}
	res := Message{Role: "assistant", Usage: Usage{
		InputTokens:           in,
		OutputTokens:          out.Usage.OutputTokens,
		CachedInputTokens:     out.Usage.CacheReadInputTokens,
		CacheWriteInputTokens: out.Usage.CacheCreationInputTokens,
	}}
	for _, c := range out.Content {
		switch c.Type {
		case "text":
			res.Content += c.Text
		case "tool_use":
			var tc ToolCall
			tc.Function.Name = c.Name
			tc.Function.Arguments = c.Input // a JSON object — extractCall unmarshals it directly
			res.ToolCalls = append(res.ToolCalls, tc)
		}
	}
	return res, nil
}

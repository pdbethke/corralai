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
	m, err := c.b.Chat(oms, tools)
	if err != nil {
		return agentworker.Message{}, err
	}
	out := agentworker.Message{Role: m.Role, Content: m.Content}
	if len(m.ToolCalls) > 0 {
		out.ToolCalls = make([]agentworker.ToolCall, len(m.ToolCalls))
		for j, tc := range m.ToolCalls {
			out.ToolCalls[j] = agentworker.ToolCall{Name: tc.Function.Name, Arguments: tc.Function.Arguments}
		}
	}
	return out, nil
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
	case "openai", "gemini", "openrouter":
		return &openaiBackend{
			base:  env("OPENAI_BASE_URL", "https://api.openai.com/v1"),
			key:   agentSecret("OPENAI_API_KEY"),
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

// NewOllamaBackend builds an ollama Backend directly (bypassing MODEL_BACKEND
// selection) — used by tests, and available to any caller that wants to talk
// to a specific Ollama endpoint/model without going through env vars.
func NewOllamaBackend(url, model string) Backend {
	return &ollamaBackend{url: url, model: model}
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

// llmHTTPTimeout is the per-request timeout for a model call. The default 180s
// is fine for hosted/frontier models but too tight for a large local model
// generating a big structured artifact (e.g. many full-file mutants of a real
// source file), so it's overridable via AGENT_LLM_TIMEOUT_SECONDS.
func llmHTTPTimeout() time.Duration {
	if v := strings.TrimSpace(os.Getenv("AGENT_LLM_TIMEOUT_SECONDS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 180 * time.Second
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
	err := postJSON(b.url+"/api/chat", nil, map[string]any{
		"model": b.model, "messages": messages, "tools": tools, "stream": false,
		"options": map[string]any{"temperature": 0.2},
	}, &out)
	return out.Message, err
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
	if len(out.Choices) == 0 {
		return Message{}, nil
	}
	m := out.Choices[0].Message
	return Message{Role: "assistant", Content: m.Content, ToolCalls: m.ToolCalls}, nil
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
		body["system"] = sys.String()
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
	}
	hdr := map[string]string{"x-api-key": b.key, "anthropic-version": "2023-06-01"}
	if err := postJSON(b.base+"/v1/messages", hdr, body, &out); err != nil {
		return Message{}, err
	}
	res := Message{Role: "assistant"}
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

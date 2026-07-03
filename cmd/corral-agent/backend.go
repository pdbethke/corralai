// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// ErrModelUnreachable is returned by a Backend when the model endpoint responds
// with HTTP 404 or is connection-refused — the model name is wrong, the endpoint
// was pulled, or the server isn't running. The task loop uses errors.Is to detect
// this and release the claim so the reaper can reassign to a healthy agent.
var ErrModelUnreachable = errors.New("model unreachable")

// Backend is the LLM the agent drives itself with. corral-agent is model-agnostic
// by design: the coordination loop is identical regardless of what's behind this
// interface — that's the whole "any model, any agent" point. Ollama is just the
// zero-cost local default; it is NOT hard-wired in.
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
	Chat(messages []omsg, tools []any) (omsg, error)
}

func newBackend() Backend {
	model := env("AGENT_MODEL", "qwen2.5-coder:7b")
	switch env("MODEL_BACKEND", "ollama") {
	case "openai", "gemini", "openrouter":
		return &openaiBackend{
			base:  env("OPENAI_BASE_URL", "https://api.openai.com/v1"),
			key:   os.Getenv("OPENAI_API_KEY"),
			model: model,
		}
	case "anthropic", "claude":
		return &anthropicBackend{
			base:  env("ANTHROPIC_BASE_URL", "https://api.anthropic.com"),
			key:   os.Getenv("ANTHROPIC_API_KEY"),
			model: model, // e.g. claude-sonnet-4-6 / claude-haiku-4-5-20251001 / claude-opus-4-8
		}
	default: // ollama
		return &ollamaBackend{url: env("OLLAMA_URL", "http://127.0.0.1:11434"), model: model}
	}
}

var httpc = &http.Client{Timeout: 180 * time.Second}

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

// ---- Ollama (/api/chat) ----

type ollamaBackend struct{ url, model string }

func (b *ollamaBackend) Chat(messages []omsg, tools []any) (omsg, error) {
	var out struct {
		Message omsg `json:"message"`
	}
	err := postJSON(b.url+"/api/chat", nil, map[string]any{
		"model": b.model, "messages": messages, "tools": tools, "stream": false,
		"options": map[string]any{"temperature": 0.2},
	}, &out)
	return out.Message, err
}

// ---- OpenAI-compatible (/v1/chat/completions) — also Gemini, OpenRouter, local ----

type openaiBackend struct{ base, key, model string }

func (b *openaiBackend) Chat(messages []omsg, tools []any) (omsg, error) {
	var out struct {
		Choices []struct {
			Message struct {
				Content   string     `json:"content"`
				ToolCalls []otoolcal `json:"tool_calls"`
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
		return omsg{}, err
	}
	if len(out.Choices) == 0 {
		return omsg{}, nil
	}
	m := out.Choices[0].Message
	return omsg{Role: "assistant", Content: m.Content, ToolCalls: m.ToolCalls}, nil
}

// ---- Anthropic (Claude, Messages API with native tool use) ----
//
// Select with MODEL_BACKEND=anthropic, ANTHROPIC_API_KEY=sk-ant-…, and an
// AGENT_MODEL like claude-sonnet-4-6. Claude's native tool_use is reliable, which
// is what makes a real mission converge (clean tool calls, fewer fumbles).

type anthropicBackend struct{ base, key, model string }

func (b *anthropicBackend) Chat(messages []omsg, tools []any) (omsg, error) {
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
	body := map[string]any{
		"model": b.model, "max_tokens": 4096, "messages": msgs, "temperature": 0.2,
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
		return omsg{}, err
	}
	res := omsg{Role: "assistant"}
	for _, c := range out.Content {
		switch c.Type {
		case "text":
			res.Content += c.Text
		case "tool_use":
			var tc otoolcal
			tc.Function.Name = c.Name
			tc.Function.Arguments = c.Input // a JSON object — extractCall unmarshals it directly
			res.ToolCalls = append(res.ToolCalls, tc)
		}
	}
	return res, nil
}

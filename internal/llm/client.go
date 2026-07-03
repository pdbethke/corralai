// SPDX-License-Identifier: Elastic-2.0

// Package llm is a minimal, text-only chat client the BRAIN uses to narrate on
// behalf of a bee (the read-only "ask an agent" debrief). It deliberately offers
// no tool-calling and no streaming: the narrator only ever turns a system prompt
// + a question into a paragraph of text, grounded in a bee's recorded trail. The
// agent's own richer, tool-using backend lives in cmd/corral-agent; this is the
// small surface the brain needs, selected from the same MODEL_BACKEND env so the
// narrator speaks with the same model the swarm runs on.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Client is a single-shot text chat client. Construct with FromEnv.
type Client struct {
	backend       string
	openAIBase    string
	openAIKey     string
	anthropicBase string
	anthropicKey  string
	ollamaURL     string
	model         string
	hc            *http.Client
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// FromEnv builds a Client from the same model environment the agents use, so the
// narrator runs on whatever backend the swarm is configured for.
func FromEnv() *Client {
	return &Client{
		backend:       env("MODEL_BACKEND", "ollama"),
		openAIBase:    env("OPENAI_BASE_URL", "https://api.openai.com/v1"),
		openAIKey:     os.Getenv("OPENAI_API_KEY"),
		anthropicBase: env("ANTHROPIC_BASE_URL", "https://api.anthropic.com"),
		anthropicKey:  os.Getenv("ANTHROPIC_API_KEY"),
		ollamaURL:     env("OLLAMA_URL", "http://127.0.0.1:11434"),
		model:         env("AGENT_MODEL", "qwen2.5-coder:7b"),
		hc:            &http.Client{Timeout: 60 * time.Second},
	}
}

// Available reports whether the configured backend can actually be called (an API
// key is present, or it's local Ollama). The narrator UI is hidden when false.
func (c *Client) Available() bool {
	switch c.backend {
	case "openai", "gemini", "openrouter":
		return c.openAIKey != ""
	case "anthropic", "claude":
		return c.anthropicKey != ""
	default: // ollama is keyless/local
		return true
	}
}

// Ask returns the model's answer to user, steered by system. Single-shot, no tools.
func (c *Client) Ask(ctx context.Context, system, user string) (string, error) {
	switch c.backend {
	case "anthropic", "claude":
		return c.askAnthropic(ctx, system, user)
	case "openai", "gemini", "openrouter":
		return c.askOpenAI(ctx, system, user)
	default:
		return c.askOllama(ctx, system, user)
	}
}

func (c *Client) post(ctx context.Context, url string, hdr map[string]string, body, out any) error {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) askOpenAI(ctx context.Context, system, user string) (string, error) {
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	hdr := map[string]string{}
	if c.openAIKey != "" {
		hdr["Authorization"] = "Bearer " + c.openAIKey
	}
	err := c.post(ctx, c.openAIBase+"/chat/completions", hdr, map[string]any{
		"model": c.model, "temperature": 0.2,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
	}, &out)
	if err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", nil
	}
	return strings.TrimSpace(out.Choices[0].Message.Content), nil
}

func (c *Client) askAnthropic(ctx context.Context, system, user string) (string, error) {
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	hdr := map[string]string{"x-api-key": c.anthropicKey, "anthropic-version": "2023-06-01"}
	err := c.post(ctx, c.anthropicBase+"/v1/messages", hdr, map[string]any{
		"model": c.model, "max_tokens": 1024, "temperature": 0.2, "system": system,
		"messages": []map[string]any{{"role": "user", "content": user}},
	}, &out)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	for _, p := range out.Content {
		if p.Type == "text" {
			sb.WriteString(p.Text)
		}
	}
	return strings.TrimSpace(sb.String()), nil
}

func (c *Client) askOllama(ctx context.Context, system, user string) (string, error) {
	var out struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	err := c.post(ctx, c.ollamaURL+"/api/chat", nil, map[string]any{
		"model": c.model, "stream": false, "options": map[string]any{"temperature": 0.2},
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
	}, &out)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out.Message.Content), nil
}

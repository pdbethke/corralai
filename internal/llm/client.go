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
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/ollamareq"
)

// Client is a single-shot text chat client. Construct with FromEnv.
type Client struct {
	backend  string
	endpoint agentbackend.Endpoint
	model    string
	// unavailable says why the client cannot be called; "" when it can.
	unavailable string
	hc          *http.Client
}

// FromEnv builds a Client from the same model environment the agents use, so the
// narrator runs on whatever backend the swarm is configured for — resolved by
// agentbackend.EndpointFor, the rules corral-agent's FromEnv applies.
//
// There are no defaults. This used to mean ollama at 127.0.0.1:11434 running
// qwen2.5-coder:7b when nothing was set, so the daemon's staffing planner and
// narrator sent prompts to a backend and model nobody chose. An unset or
// unknown backend, or an unset AGENT_MODEL, now leaves the client unavailable
// with the reason (Unavailable), and every caller already checks Available.
func FromEnv() *Client {
	c := &Client{
		backend: strings.TrimSpace(os.Getenv("MODEL_BACKEND")),
		model:   strings.TrimSpace(os.Getenv("AGENT_MODEL")),
		hc:      &http.Client{Timeout: 60 * time.Second},
	}
	ep, err := agentbackend.EndpointFor(c.backend)
	switch {
	case err != nil:
		c.unavailable = err.Error()
	case c.model == "":
		c.unavailable = fmt.Sprintf("llm: AGENT_MODEL is not set — name the model MODEL_BACKEND=%s should serve (there is no default model)", c.backend)
	case ep.Wire == "anthropic" && ep.Key == "":
		c.unavailable = "llm: MODEL_BACKEND=" + c.backend + " needs ANTHROPIC_API_KEY"
	case c.backend == "openai" && ep.Key == "" && strings.TrimSpace(os.Getenv("OPENAI_BASE_URL")) == "":
		// A keyless OpenAI-compatible server is fine when you named it; with
		// neither a key nor a base URL there is nothing to call.
		c.unavailable = "llm: MODEL_BACKEND=openai needs OPENAI_API_KEY, or OPENAI_BASE_URL for a keyless OpenAI-compatible server"
	}
	c.endpoint = ep
	return c
}

// Available reports whether the configured backend can actually be called. The
// narrator UI (and the staffing planner) is off when false.
func (c *Client) Available() bool { return c.unavailable == "" }

// Unavailable is why Available is false, naming what to set; "" when available.
func (c *Client) Unavailable() string { return c.unavailable }

// Ask returns the model's answer to user, steered by system. Single-shot, no tools.
func (c *Client) Ask(ctx context.Context, system, user string) (string, error) {
	if c.unavailable != "" {
		return "", errors.New(c.unavailable)
	}
	switch c.endpoint.Wire {
	case "anthropic":
		return c.askAnthropic(ctx, system, user)
	case "openai-chat":
		return c.askOpenAI(ctx, system, user)
	case "ollama":
		return c.askOllama(ctx, system, user)
	default:
		return "", fmt.Errorf("llm: no wire for MODEL_BACKEND=%q", c.backend)
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
	if c.endpoint.Key != "" {
		hdr["Authorization"] = "Bearer " + c.endpoint.Key
	}
	err := c.post(ctx, strings.TrimSuffix(c.endpoint.Base, "/")+"/chat/completions", hdr, map[string]any{
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
	hdr := map[string]string{"x-api-key": c.endpoint.Key, "anthropic-version": "2023-06-01"}
	err := c.post(ctx, strings.TrimSuffix(c.endpoint.Base, "/")+"/v1/messages", hdr, map[string]any{
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
	body := map[string]any{
		"model": c.model, "stream": false, "options": map[string]any{"temperature": 0.2},
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
	}
	// See ollamareq.Decorate: num_ctx plus think-suppression, in one place.
	ollamareq.Decorate(body, c.model)
	err := c.post(ctx, strings.TrimSuffix(c.endpoint.Base, "/")+"/api/chat", nil, body, &out)
	if err != nil {
		return "", ollamareq.WrapErr(err)
	}
	return strings.TrimSpace(out.Message.Content), nil
}

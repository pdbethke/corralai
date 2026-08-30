// SPDX-License-Identifier: Elastic-2.0

package agentbackend

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestAnthropicReportsCachedInputTokens: the per-survivor writer sends the SAME
// prefix (the file, the signatures, the harness) on every one of a file's
// writer calls, so a caching provider bills most of them at the cached rate.
// The saving is invisible unless the response's own cached-token count is
// recorded — a bare input_tokens total cannot distinguish a cheap cached
// prompt from an expensive fresh one.
func TestAnthropicReportsCachedInputTokens(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"ok"}],
		  "usage":{"input_tokens":40,"output_tokens":7,"cache_read_input_tokens":1200}}`)
	}))
	defer srv.Close()

	b := &anthropicBackend{base: srv.URL, key: "k", model: "claude-test"}
	m, err := b.Chat([]Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if m.Usage.CachedInputTokens == nil {
		t.Fatal("CachedInputTokens is nil — the provider reported 1200 cached tokens and corral dropped them at the JSON boundary")
	}
	if got := *m.Usage.CachedInputTokens; got != 1200 {
		t.Errorf("CachedInputTokens = %d, want 1200", got)
	}
}

// TestAnthropicSilentAboutCacheStaysNil is the NULL-not-zero half. A response
// with no cached-token field has not reported a cache MISS — it has reported
// nothing, and a stored 0 would be read by any later query as a measured zero.
func TestAnthropicSilentAboutCacheStaysNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":40,"output_tokens":7}}`)
	}))
	defer srv.Close()

	b := &anthropicBackend{base: srv.URL, key: "k", model: "claude-test"}
	m, err := b.Chat([]Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if m.Usage.CachedInputTokens != nil {
		t.Errorf("CachedInputTokens = %d, want nil — nothing was reported", *m.Usage.CachedInputTokens)
	}
}

// TestOpenAICompatReportsCachedInputTokens covers the other wire shape corral
// actually speaks — OpenAI, Gemini's OpenAI-compatible endpoint and OpenRouter
// all report the cached half under prompt_tokens_details.
func TestOpenAICompatReportsCachedInputTokens(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}],
		  "usage":{"prompt_tokens":900,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":800}}}`)
	}))
	defer srv.Close()

	b := &openaiBackend{base: srv.URL, model: "gemini-test"}
	m, err := b.Chat([]Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if m.Usage.CachedInputTokens == nil || *m.Usage.CachedInputTokens != 800 {
		t.Fatalf("CachedInputTokens = %v, want 800", m.Usage.CachedInputTokens)
	}
}

// TestUsageMeterAccumulatesCachedTokensOnlyWhenReported: a meter over four
// calls, one of which reported a cached count, must report that one count —
// not nil (which would lose a real measurement) and not a total that silently
// counted the three silent calls as zero-cache measurements.
func TestUsageMeterAccumulatesCachedTokensOnlyWhenReported(t *testing.T) {
	var m UsageMeter
	n := int64(1200)
	m.Add(Usage{InputTokens: 10, OutputTokens: 1})
	m.Add(Usage{InputTokens: 10, OutputTokens: 1, CachedInputTokens: &n})
	m.Add(Usage{InputTokens: 10, OutputTokens: 1})

	snap := m.Snapshot()
	if snap.CachedInputTokens == nil {
		t.Fatal("the meter dropped the one call that reported cached tokens")
	}
	if *snap.CachedInputTokens != 1200 {
		t.Errorf("CachedInputTokens = %d, want 1200", *snap.CachedInputTokens)
	}

	var silent UsageMeter
	silent.Add(Usage{InputTokens: 10, OutputTokens: 1})
	if got := silent.Snapshot().CachedInputTokens; got != nil {
		t.Errorf("a meter no call reported cache for = %d, want nil", *got)
	}
}

// TestAnthropicMarksTheSystemBlockCacheable proves the request corral actually
// sends asks for the saving, rather than merely being able to record one. The
// system prompt is the byte-identical prefix every one of a file's writer calls
// shares; without cache_control Anthropic caches nothing at all.
func TestAnthropicMarksTheSystemBlockCacheable(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer srv.Close()

	b := &anthropicBackend{base: srv.URL, key: "k", model: "claude-test"}
	if _, err := b.Chat([]Message{{Role: "system", Content: "the shared prefix"}, {Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	blocks, ok := body["system"].([]any)
	if !ok || len(blocks) != 1 {
		t.Fatalf("system = %#v, want a one-block content array", body["system"])
	}
	block, _ := blocks[0].(map[string]any)
	if block["type"] != "text" || block["text"] != "the shared prefix" {
		t.Fatalf("system block = %#v, want the system text verbatim", block)
	}
	cc, _ := block["cache_control"].(map[string]any)
	if cc["type"] != "ephemeral" {
		t.Errorf("cache_control = %#v, want {\"type\":\"ephemeral\"}", block["cache_control"])
	}
}

// TestAnthropicSendsNoSystemBlockWhenThereIsNoSystemPrompt: the cacheable-block
// shape must not invent an empty system turn, which the API rejects.
func TestAnthropicSendsNoSystemBlockWhenThereIsNoSystemPrompt(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"ok"}],"usage":{}}`)
	}))
	defer srv.Close()

	b := &anthropicBackend{base: srv.URL, key: "k", model: "claude-test"}
	if _, err := b.Chat([]Message{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if _, present := body["system"]; present {
		t.Errorf("system = %#v, want the field absent", body["system"])
	}
}

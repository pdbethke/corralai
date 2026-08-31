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

// TestAnthropicReportsCacheWriteTokens: a cache WRITE is billed at 1.25x a
// normal input token, so the first call of a fan-out costs MORE than an
// uncached one and only the calls after it save. A ledger that recorded only
// the reads would report the saving and hide its price.
func TestAnthropicReportsCacheWriteTokens(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"ok"}],
		  "usage":{"input_tokens":40,"output_tokens":7,"cache_creation_input_tokens":38000}}`)
	}))
	defer srv.Close()

	b := &anthropicBackend{base: srv.URL, key: "k", model: "claude-test"}
	m, err := b.Chat([]Message{{Role: "system", Content: "prefix"}, {Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if m.Usage.CacheWriteInputTokens == nil || *m.Usage.CacheWriteInputTokens != 38000 {
		t.Fatalf("CacheWriteInputTokens = %v, want 38000", m.Usage.CacheWriteInputTokens)
	}
	// Reads and writes are separate measurements: the write above reported no
	// read, and inventing a 0 would claim this call read nothing from cache.
	if m.Usage.CachedInputTokens != nil {
		t.Errorf("CachedInputTokens = %d, want nil — this response reported only a write", *m.Usage.CachedInputTokens)
	}
}

// TestUsageMeterAccumulatesCacheWritesSeparately: two nullable counters, two
// independent "was this ever measured" answers.
func TestUsageMeterAccumulatesCacheWritesSeparately(t *testing.T) {
	var m UsageMeter
	w := int64(38000)
	r := int64(1200)
	m.Add(Usage{InputTokens: 10, CacheWriteInputTokens: &w})
	m.Add(Usage{InputTokens: 10, CachedInputTokens: &r})
	snap := m.Snapshot()
	if snap.CacheWriteInputTokens == nil || *snap.CacheWriteInputTokens != 38000 {
		t.Errorf("CacheWriteInputTokens = %v, want 38000", snap.CacheWriteInputTokens)
	}
	if snap.CachedInputTokens == nil || *snap.CachedInputTokens != 1200 {
		t.Errorf("CachedInputTokens = %v, want 1200", snap.CachedInputTokens)
	}

	var none UsageMeter
	none.Add(Usage{InputTokens: 10})
	if s := none.Snapshot(); s.CacheWriteInputTokens != nil {
		t.Errorf("CacheWriteInputTokens = %d, want nil", *s.CacheWriteInputTokens)
	}
}

// TestAnthropicInputTokensIncludeTheCacheCounters is the units fix.
//
// Anthropic's three input counters are DISJOINT: `input_tokens` is the
// uncached remainder, and cache_read_input_tokens / cache_creation_input_tokens
// are the rest of the same prompt. Every other wire corral speaks reports a
// prompt total that ALREADY contains its cached half (OpenAI's prompt_tokens),
// so storing Anthropic's remainder under the same column name would give
// scan_model_calls.input_tokens two different meanings depending on the seat's
// vendor — and would make `(N cached)` read as N of a total that excludes it.
//
// So the backend normalises: InputTokens is the WHOLE prompt, everywhere, and
// the two cache counters stay the breakdown of it.
func TestAnthropicInputTokensIncludeTheCacheCounters(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"ok"}],
		  "usage":{"input_tokens":40,"output_tokens":7,
		           "cache_read_input_tokens":1200,"cache_creation_input_tokens":300}}`)
	}))
	defer srv.Close()

	b := &anthropicBackend{base: srv.URL, key: "k", model: "claude-test"}
	m, err := b.Chat([]Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got := m.Usage.InputTokens; got != 1540 {
		t.Errorf("InputTokens = %d, want 1540 (40 uncached + 1200 read + 300 written) — Anthropic's three input counters are disjoint, so the raw input_tokens is only the remainder", got)
	}
	if m.Usage.CachedInputTokens == nil || *m.Usage.CachedInputTokens != 1200 {
		t.Errorf("CachedInputTokens = %v, want the reported 1200 unchanged — it is the breakdown of InputTokens, not a second total", m.Usage.CachedInputTokens)
	}
	if m.Usage.CacheWriteInputTokens == nil || *m.Usage.CacheWriteInputTokens != 300 {
		t.Errorf("CacheWriteInputTokens = %v, want the reported 300 unchanged", m.Usage.CacheWriteInputTokens)
	}
}

// TestAnthropicInputTokensUnchangedWithoutCacheCounters: a response that
// reports no cache activity must not have its total moved. Normalising must
// add what was reported and nothing else.
func TestAnthropicInputTokensUnchangedWithoutCacheCounters(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":40,"output_tokens":7}}`)
	}))
	defer srv.Close()

	b := &anthropicBackend{base: srv.URL, key: "k", model: "claude-test"}
	m, err := b.Chat([]Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got := m.Usage.InputTokens; got != 40 {
		t.Errorf("InputTokens = %d, want the reported 40", got)
	}
}

// TestOpenAICompatInputTokensAreNotDoubleCounted is the other half of the
// units rule: prompt_tokens ALREADY includes cached_tokens on this wire, so
// the normalisation that Anthropic needs must NOT be applied here — 900 with
// 800 of it cached is a 900-token prompt, never a 1700-token one.
func TestOpenAICompatInputTokensAreNotDoubleCounted(t *testing.T) {
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
	if got := m.Usage.InputTokens; got != 900 {
		t.Errorf("InputTokens = %d, want the reported prompt_tokens 900 — it already contains the 800 cached", got)
	}
}

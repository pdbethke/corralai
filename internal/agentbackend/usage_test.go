// SPDX-License-Identifier: Elastic-2.0

package agentbackend

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// Every provider returns its token counts on every response, and corral used to
// drop them at the JSON boundary: the anonymous response structs simply did not
// declare a `usage` field, so encoding/json discarded it silently. That made the
// one number the audit cost model is built on — cost is O(mutants x suite
// runtime), and the model half of it is tokens — unmeasurable, and left "what
// did this audit cost" a question nobody could answer from the record.
func TestAnthropicBackendSurfacesUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"hi"}],
			"usage":{"input_tokens":1234,"output_tokens":567}}`))
	}))
	defer srv.Close()

	b := &anthropicBackend{base: srv.URL, key: "k", model: "claude-test"}
	m, err := b.Chat([]Message{{Role: "user", Content: "x"}}, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if m.Content != "hi" {
		t.Fatalf("content = %q, want %q", m.Content, "hi")
	}
	if m.Usage.InputTokens != 1234 || m.Usage.OutputTokens != 567 {
		t.Fatalf("usage = %+v, want {1234 567} — the provider reported it and it must not be dropped", m.Usage)
	}
}

// The OpenAI-compatible shape is how corral reaches Gemini and OpenRouter as
// well as OpenAI, so its field names (prompt_/completion_) matter just as much
// as Anthropic's.
func TestOpenAIBackendSurfacesUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hi"}}],
			"usage":{"prompt_tokens":88,"completion_tokens":22}}`))
	}))
	defer srv.Close()

	b := &openaiBackend{base: srv.URL, key: "k", model: "gemini-test"}
	m, err := b.Chat([]Message{{Role: "user", Content: "x"}}, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if m.Usage.InputTokens != 88 || m.Usage.OutputTokens != 22 {
		t.Fatalf("usage = %+v, want {88 22}", m.Usage)
	}
}

// A provider that reports no usage must read as zero, never as a guess. An
// invented count is worse than an absent one: it would be indistinguishable
// from a measurement in the ledger.
func TestUsageAbsentReadsAsZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hi"}}]}`))
	}))
	defer srv.Close()

	b := &openaiBackend{base: srv.URL, key: "k", model: "m"}
	m, err := b.Chat([]Message{{Role: "user", Content: "x"}}, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if m.Usage.InputTokens != 0 || m.Usage.OutputTokens != 0 {
		t.Fatalf("usage = %+v, want zero when the provider reported none", m.Usage)
	}
}

// The meter is read from one goroutine after up to 8 seats have written to it
// concurrently (certify --local auto-sizes the swarm to the host's cores), so
// it has to be safe under -race, not merely correct single-threaded.
func TestUsageMeterAccumulatesConcurrently(t *testing.T) {
	var m UsageMeter
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				m.Add(Usage{InputTokens: 3, OutputTokens: 2})
			}
		}()
	}
	wg.Wait()

	in, out, calls := m.Totals()
	if in != 8*100*3 || out != 8*100*2 || calls != 800 {
		t.Fatalf("totals = (%d in, %d out, %d calls), want (2400, 1600, 800)", in, out, calls)
	}
}

// A metered chatter must record what the backend reported without changing what
// the caller receives — the meter is an observer, never part of the result.
func TestMeteredChatterRecordsWithoutAlteringTheReply(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"answer"}}],
			"usage":{"prompt_tokens":10,"completion_tokens":5}}`))
	}))
	defer srv.Close()

	var meter UsageMeter
	c := AsChatterMetered(&openaiBackend{base: srv.URL, key: "k", model: "m"}, &meter)
	reply, err := c.Chat(nil, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if reply.Content != "answer" {
		t.Fatalf("content = %q, want %q", reply.Content, "answer")
	}
	in, out, calls := meter.Totals()
	if in != 10 || out != 5 || calls != 1 {
		t.Fatalf("meter = (%d, %d, %d), want (10, 5, 1)", in, out, calls)
	}
}

// A nil meter must be usable: AsChatter (the unmetered constructor) is still the
// common path, and a caller that does not care about tokens should not have to
// invent a meter to avoid a panic.
func TestMeteredChatterTolueratesNilMeter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1}}`))
	}))
	defer srv.Close()

	c := AsChatterMetered(&openaiBackend{base: srv.URL, key: "k", model: "m"}, nil)
	if _, err := c.Chat(nil, nil); err != nil {
		t.Fatalf("a nil meter must be a no-op, got: %v", err)
	}
}

// TestMeterRecordsWallAndModel pins the fix for a meter that could say WHAT a
// role spent but not WHICH model it spent it on or HOW LONG the provider took
// — both needed once a meter is scoped to one role instead of a whole run.
func TestMeterRecordsWallAndModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Millisecond)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hi"}}],
			"usage":{"prompt_tokens":10,"completion_tokens":5}}`))
	}))
	defer srv.Close()

	meter := &UsageMeter{Model: "test-model"}
	c := AsChatterMetered(&openaiBackend{base: srv.URL, key: "k", model: "test-model"}, meter)
	if _, err := c.Chat(nil, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	snap := meter.Snapshot()
	if snap.Model != "test-model" {
		t.Errorf("Snapshot().Model = %q, want %q", snap.Model, "test-model")
	}
	if snap.Calls != 1 {
		t.Errorf("Snapshot().Calls = %d, want 1", snap.Calls)
	}
	if snap.Wall < 5*time.Millisecond {
		t.Errorf("Snapshot().Wall = %v, want >= 5ms — the call was timed around a 5ms sleep", snap.Wall)
	}
}

// fakeUsageBackend answers every Chat with a fixed reply and usage, counting
// how often the "provider" was reached.
type fakeUsageBackend struct {
	reply  string
	usage  Usage
	onChat func()
}

func (f *fakeUsageBackend) Chat(_ []Message, _ []any) (Message, error) {
	if f.onChat != nil {
		f.onChat()
	}
	return Message{Role: "assistant", Content: f.reply, Usage: f.usage}, nil
}

// The cap refuses BEFORE dialling and charges AFTER: a call past the cap
// costs nothing and the meter never sees it; the refusal says so with the
// spend at that moment.
func TestTokenBudgetRefusesBeforeTheProviderIsDialled(t *testing.T) {
	calls := 0
	inner := &fakeUsageBackend{reply: "ok", usage: Usage{InputTokens: 600, OutputTokens: 100}, onChat: func() { calls++ }}
	meter := &UsageMeter{}
	budget := NewTokenBudget(1000)
	c := AsChatterBudgeted(inner, meter, budget)
	if _, err := c.Chat(nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Chat(nil, nil); err != nil { // 700 spent, still under 1000
		t.Fatal(err)
	}
	if _, err := c.Chat(nil, nil); !errors.Is(err, ErrTokenBudgetExhausted) { // 1400 spent
		t.Fatalf("third call: err = %v, want ErrTokenBudgetExhausted", err)
	}
	if calls != 2 {
		t.Fatalf("provider dialled %d times, want 2 — the refused call must never reach it", calls)
	}
	hit, cap, spent, at := budget.Exhausted()
	if !hit || cap != 1000 || spent != 1400 || at != 2 {
		t.Errorf("Exhausted = %v %d %d %d, want hit at 1400 of 1000 after 2 calls", hit, cap, spent, at)
	}
	if _, _, n := meter.Totals(); n != 2 {
		t.Errorf("meter recorded %d calls, want 2", n)
	}
	// nil is no cap.
	if _, err := AsChatterBudgeted(inner, meter, nil).Chat(nil, nil); err != nil {
		t.Errorf("a nil budget must not refuse: %v", err)
	}
	if NewTokenBudget(0) != nil {
		t.Error("a cap of 0 is 'no cap', which is nil")
	}
}

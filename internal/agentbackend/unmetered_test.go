// SPDX-License-Identifier: Elastic-2.0

package agentbackend

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/agentworker"
	"github.com/pdbethke/corralai/internal/seatrecord"
)

// A meter fed metered and unmetered calls counts every call, counts the
// unmetered ones separately, and adds no tokens for them — an unmetered call
// that somehow carried numbers must not have them summed as a measurement.
func TestUsageMeterCountsUnmeteredCallsAndAddsNoTokensForThem(t *testing.T) {
	var m UsageMeter
	cached := int64(4)
	m.Add(Usage{InputTokens: 10, OutputTokens: 5, CachedInputTokens: &cached})
	m.Add(Usage{Unmetered: true})
	m.Add(Usage{Unmetered: true, InputTokens: 99, OutputTokens: 99, CachedInputTokens: &cached})
	s := m.Snapshot()
	if s.Calls != 3 || s.UnmeteredCalls != 2 {
		t.Fatalf("calls = %d, unmetered = %d; want 3 and 2", s.Calls, s.UnmeteredCalls)
	}
	if s.InputTokens != 10 || s.OutputTokens != 5 || s.CachedInputTokens == nil || *s.CachedInputTokens != 4 {
		t.Fatalf("tokens = %d/%d cached %v; want 10/5 cached 4 — the unmetered call's numbers must not be summed", s.InputTokens, s.OutputTokens, s.CachedInputTokens)
	}
}

// A meter that saw only metered calls reports zero unmetered calls, and a nil
// meter reports nothing.
func TestUsageMeterWithNoUnmeteredCalls(t *testing.T) {
	var m UsageMeter
	m.Add(Usage{InputTokens: 1})
	if got := m.Snapshot().UnmeteredCalls; got != 0 {
		t.Fatalf("unmetered = %d, want 0", got)
	}
	var nilMeter *UsageMeter
	nilMeter.Add(Usage{Unmetered: true})
	if got := nilMeter.Snapshot().UnmeteredCalls; got != 0 {
		t.Fatalf("a nil meter reported %d unmetered calls", got)
	}
}

// operatorAgent defines CORRALAI_AGENT_FAKE as a script that prints stdout,
// on the named wire ("" leaves the wire undeclared, which is text).
func operatorAgent(t *testing.T, wire, stdout string) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "fake.sh")
	writeExec(t, script, "#!/bin/sh\ncat >/dev/null\nprintf '%s' '"+stdout+"'\n")
	t.Setenv("CORRALAI_AGENT_FAKE", script)
	t.Setenv("CORRALAI_AGENT_FAKE_WIRE", wire)
}

// A text-wire reply reports no usage, so it must say so on the reply — and
// a metered chatter must record it as an unmetered call, not a free one.
func TestATextWireReplyIsUnmeteredAndTheMeterSaysSo(t *testing.T) {
	operatorAgent(t, "", "plain reply")
	b, err := ForSeat("fake", SeatOptions{})
	if err != nil {
		t.Fatal(err)
	}
	m, err := b.Chat([]Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Usage.Unmetered {
		t.Fatal("a text-wire reply must carry Usage.Unmetered")
	}
	var meter UsageMeter
	if _, err := AsChatterMetered(b, &meter).Chat([]agentworker.Message{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatal(err)
	}
	if s := meter.Snapshot(); s.Calls != 1 || s.UnmeteredCalls != 1 {
		t.Fatalf("meter = %+v, want 1 call, 1 unmetered", s)
	}
}

// A metered wire's reply is not unmetered.
func TestAClaudeJSONWireReplyIsMetered(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("testdata", "agentwire", "claude-json-ok.json"))
	if err != nil {
		t.Fatal(err)
	}
	operatorAgent(t, "claude-json", string(doc))
	b, err := ForSeat("fake", SeatOptions{})
	if err != nil {
		t.Fatal(err)
	}
	m, err := b.Chat([]Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.Usage.Unmetered || m.Usage.InputTokens == 0 {
		t.Fatalf("a claude-json reply must be metered with real tokens, got %+v", m.Usage)
	}
}

// Metered asks the backend, and for an agent seat the WIRE answers: the same
// agent name is metered on a usage-reporting wire and not on text. Every API
// backend is metered.
func TestMeteredIsTheWiresAnswer(t *testing.T) {
	operatorAgent(t, "", "x")
	text, err := ForSeat("fake", SeatOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if Metered(text) {
		t.Error("a text-wire agent seat reported Metered")
	}
	t.Setenv("CORRALAI_AGENT_FAKE_WIRE", "codex-jsonl")
	codex, err := ForSeat("fake", SeatOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !Metered(codex) {
		t.Error("a codex-jsonl agent seat reported unmetered")
	}
	t.Setenv("CORRALAI_AGENT_FAKE_WIRE", "claude-json")
	claude, err := ForSeat("fake", SeatOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !Metered(claude) {
		t.Error("a claude-json agent seat reported unmetered")
	}
	if !Metered(NewOllamaBackend("http://127.0.0.1:1", "m")) {
		t.Error("an API backend reported unmetered")
	}
	if !Metered(&openaiBackend{base: "http://x", model: "m"}) {
		t.Error("an API backend reported unmetered")
	}
}

// SeatTool is the ONE helper both verbs call for the record: the agent's
// version for an agent seat, "" for an API seat.
func TestSeatToolNamesTheAgentAndNothingForAnAPISeat(t *testing.T) {
	script := filepath.Join(t.TempDir(), "fake.sh")
	writeExec(t, script, "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'fake-cli 1.2.3'; exit 0; fi\ncat\n")
	t.Setenv("CORRALAI_AGENT_FAKE", script+" {model:--model}")
	if got := SeatTool("fake:some-model"); got == "" || !strings.Contains(got, "fake-cli 1.2.3") {
		t.Errorf("SeatTool(agent) = %q, want the agent's version", got)
	}
	if got := SeatTool("claude-sonnet-5"); got != "" {
		t.Errorf("SeatTool(API model) = %q, want empty", got)
	}
}

// The recorded rule needs no environment: the record's tool says an agent
// sat, and only a pin names its model.
func TestRecordedSeatNamesAModelIsEnvFree(t *testing.T) {
	for _, c := range []struct {
		spec, tool string
		want       bool
	}{
		{"codex", "codex-cli 0.160.0", false},
		{"codex:gpt-6-astra", "codex-cli 0.160.0", true},
		{"my-agent", "my-agent 0.1 [my-agent run]", false},
		{"gpt-6", "", true},
		{"qwen2.5-coder:14b", "", true},
		{"", "", false},
		{"claude-code", "", false}, // an older record with no tool: a built-in name is still an agent
	} {
		if got := RecordedSeatNamesAModel(c.spec, c.tool); got != c.want {
			t.Errorf("RecordedSeatNamesAModel(%q, %q) = %v, want %v", c.spec, c.tool, got, c.want)
		}
	}
}

// The recorded rule lives in seatrecord so a browser-safe reader (the ledger's
// Summary, compiled to WASM) applies it without importing the backends. Its
// copy of the built-in names must be exactly this package's: a built-in added
// here and not there would read as an API model in every pre-tool record.
func TestSeatrecordKnowsEveryBuiltinAgent(t *testing.T) {
	got := map[string]bool{}
	for _, n := range seatrecord.BuiltinAgentNames() {
		got[n] = true
	}
	for name := range builtinAgents {
		if !got[name] {
			t.Errorf("built-in agent %q is missing from seatrecord.BuiltinAgentNames", name)
		}
		delete(got, name)
	}
	for name := range got {
		t.Errorf("seatrecord.BuiltinAgentNames has %q, which is not a built-in agent here", name)
	}
}

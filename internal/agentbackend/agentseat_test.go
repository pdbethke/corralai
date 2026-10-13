// SPDX-License-Identifier: Elastic-2.0

package agentbackend

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeExec(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil { // #nosec G306 -- a test fixture executable, owner-only
		t.Fatal(err)
	}
}

func modelOf(b Backend) string {
	if m, ok := b.(interface{ Model() string }); ok {
		return m.Model()
	}
	return ""
}

func TestForSeatRunsAnOperatorAgentAndReadsItsOutFile(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake.sh")
	writeExec(t, script, "#!/bin/sh\nout=\"$1\"; printf 'REPLY:%s' \"$(cat)\" > \"$out\"\n")
	t.Setenv("CORRALAI_AGENT_FAKE", script+" {out}")
	b, err := ForSeat("fake", SeatOptions{})
	if err != nil {
		t.Fatal(err)
	}
	m, err := b.Chat([]Message{{Role: "user", Content: "hello"}}, nil)
	if err != nil || m.Content != "REPLY:hello" {
		t.Fatalf("got %q, %v", m.Content, err)
	}
	if _, ok := b.(ModelSwitcher); ok {
		t.Error("an agent seat is one tool+model; WithModel would silently change what sat in the seat")
	}
}

func TestForSeatDefersToForModelForAnAPIName(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "x")
	b, err := ForSeat("gemini-3.8-flash", SeatOptions{})
	if err != nil || modelOf(b) != "gemini-3.8-flash" {
		t.Fatalf("got %v, %v", b, err)
	}
}

// A cloud model with its key missing must still FAIL: certify's up-front
// credential check relies on it, and a silent fall back to a local model would
// collapse the cross-vendor decorrelation the operator asked for.
func TestForSeatCloudNameWithoutItsKeyFailsRatherThanFallingBackLocal(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	if b, err := ForSeat("gemini-3.8-flash", SeatOptions{}); err == nil {
		t.Fatalf("want a missing-key error, got backend %v", b)
	}
}

// An unprefixed name is a local model, as review's seat always served it.
func TestForSeatServesAnUnprefixedNameFromTheLocalDaemon(t *testing.T) {
	b, err := ForSeat("llama-local-7b", SeatOptions{})
	if err != nil || modelOf(b) != "llama-local-7b" {
		t.Fatalf("got %v, %v", b, err)
	}
}

func TestForSeatEmptySpecIsTheSameErrorAsForModel(t *testing.T) {
	_, want := ForModel("")
	_, got := ForSeat("", SeatOptions{})
	if want == nil || got == nil || got.Error() != want.Error() {
		t.Fatalf("ForSeat(\"\") = %v, want %v", got, want)
	}
}

func TestAPinThatTheDefinitionCannotCarryIsRefusedByName(t *testing.T) {
	t.Setenv("CORRALAI_AGENT_FAKE", "/bin/cat")
	if _, err := ForSeat("fake:some-model", SeatOptions{}); err == nil || !strings.Contains(err.Error(), "takes no model") {
		t.Fatalf("want a refusal naming the pin, got %v", err)
	}
}

func TestACertifySeatRunsInAFreshEmptyDirectory(t *testing.T) {
	t.Setenv("CORRALAI_AGENT_FAKE", "/bin/sh -c 'ls -A; pwd'")
	b, err := ForSeat("fake", SeatOptions{})
	if err != nil {
		t.Fatal(err)
	}
	m, err := b.Chat([]Message{{Role: "user", Content: ""}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(m.Content), "\n")
	if len(lines) != 1 { // only pwd's line: the directory was empty
		t.Fatalf("seat dir was not empty: %q", m.Content)
	}
	if _, err := os.Stat(lines[0]); !os.IsNotExist(err) {
		t.Fatalf("seat dir %s not removed after the call", lines[0])
	}
}

func TestAGivenDirIsUsedAndKept(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CORRALAI_AGENT_FAKE", "/bin/sh -c pwd")
	b, err := ForSeat("fake", SeatOptions{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	m, err := b.Chat([]Message{{Role: "user", Content: ""}}, nil)
	if err != nil || strings.TrimSpace(m.Content) != dir {
		t.Fatalf("got %q, %v; want %s", m.Content, err, dir)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("a caller's dir must not be removed: %v", err)
	}
}

func TestExpandAgentArgv(t *testing.T) {
	got, err := expandAgentArgv("a", []string{"x", "{model:--m}", "{dir}", "o={out}"}, "mod", "/d", "/o", nil)
	if err != nil || strings.Join(got, "|") != "x|--m|mod|/d|o=/o" {
		t.Fatalf("got %q, %v", got, err)
	}
	got, err = expandAgentArgv("a", []string{"x", "{model:--m}"}, "", "/d", "/o", nil)
	if err != nil || strings.Join(got, "|") != "x" {
		t.Fatalf("unpinned flag form must vanish: %q, %v", got, err)
	}
	if _, err := expandAgentArgv("a", []string{"x", "{model}"}, "", "/d", "/o", nil); err == nil || !strings.Contains(err.Error(), "takes a model") {
		t.Fatalf("a bare {model} with no pin must be refused: %v", err)
	}
	if _, err := expandAgentArgv("a", []string{"x"}, "m", "/d", "/o", nil); err == nil || !strings.Contains(err.Error(), "takes no model") {
		t.Fatalf("a pin with no carrier must be refused: %v", err)
	}
}

func TestAgentVersionOfAShellWrappedDefinitionNamesTheDefinitionNotTheShell(t *testing.T) {
	t.Setenv("CORRALAI_AGENT_WRAPPED", "sh -c 'echo hi'")
	got := AgentVersion("wrapped")
	if got != "[sh -c 'echo hi']" {
		t.Fatalf("got %q; want the definition alone, never the shell's version", got)
	}
	if AgentVersion("no-such-agent-defined") != "" {
		t.Error("an undefined agent has no version")
	}
}

func TestAgentSeatNamesNoAPIModel(t *testing.T) {
	if _, _, ok := AgentSeat("gemini-3.6-flash"); ok {
		t.Error("a provider model name is not an agentic seat")
	}
	if !strings.Contains(strings.Join(DefinedAgents(), ","), "codex") {
		t.Error("the built-ins are defined agents")
	}
}

func TestSplitWordsIsPOSIXEnough(t *testing.T) {
	for in, want := range map[string][]string{
		`a b  c`:                 {"a", "b", "c"},
		`a "b c" 'd e' f\ g`:     {"a", "b c", "d e", "f g"},
		`x --flag="q v" 'it''s'`: {"x", "--flag=q v", "its"},
		``:                       nil,
	} {
		got, err := splitWords(in)
		if err != nil || strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("%q → %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{`a "b`, `a 'b`, `a \`} {
		if _, err := splitWords(bad); err == nil {
			t.Errorf("%q must not split", bad)
		}
	}
}

// TestSeatNamesAModel pins the distinction the record depends on: whether a
// seat spec identifies a MODEL or merely the TOOL that ran one.
//
// This is the question a per-model comparison asks, and it is deliberately
// not the question SeatModelOf answers. SeatModelOf resolves an unpinned
// agentic seat to the AGENT, which is right for decorrelation (two unpinned
// claude-code seats really are correlated) and wrong as a model: the CLI
// chose its own and never said which. A row like that is a could-not-measure
// and must not read as a measurement — 24 of this repository's first 50
// recorded reviews are exactly that shape, unrecoverably.
func TestSeatNamesAModel(t *testing.T) {
	for _, c := range []struct {
		spec string
		want bool
		why  string
	}{
		{"claude-code", false, "agentic seat, unpinned — the CLI picks the model"},
		{"codex", false, "agentic seat, unpinned"},
		{"antigravity", false, "agentic seat, unpinned"},
		{"codex:gpt-6-astra", true, "agentic seat pinned to a model"},
		{"claude-code:claude-fable-5-1", true, "agentic seat pinned to a model"},
		{"antigravity:gemini-3.1-pro-high", true, "agentic seat pinned to a model"},
		{"gemini-3.6-flash", true, "API seat — the spec IS the model name"},
		{"claude-sonnet-5", true, "API seat"},
		{"", false, "no seat at all is not a resolved model"},
		{"   ", false, "whitespace is not a resolved model"},
	} {
		if got := SeatNamesAModel(c.spec); got != c.want {
			t.Errorf("SeatNamesAModel(%q) = %v, want %v (%s)", c.spec, got, c.want, c.why)
		}
	}
}

// TestSeatNamesAModelDisagreesWithSeatModelOf is the point of the new
// function stated as an assertion: for an unpinned agentic seat the two
// answer differently, and a change that collapsed them would silently
// restore the defect this field exists to prevent.
func TestSeatNamesAModelDisagreesWithSeatModelOf(t *testing.T) {
	const spec = "claude-code"
	if got := SeatModelOf(spec); got != spec {
		t.Fatalf("SeatModelOf(%q) = %q, want the agent name — the decorrelation rule depends on it", spec, got)
	}
	if SeatNamesAModel(spec) {
		t.Errorf("SeatNamesAModel(%q) = true, but %q is a CLI, not a model: "+
			"recording it as resolved is how a tool name becomes a measurement", spec, spec)
	}
}

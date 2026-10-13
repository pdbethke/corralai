// SPDX-License-Identifier: Elastic-2.0

package agentbackend

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// A seat whose CLI is not installed is refused when it is RESOLVED — the one
// place every verb goes through — naming the seat and the binary, and marked
// as the seat's environment rather than anything a model did. It used to be
// checked only by doctor, so certify ran its jail first and then filed the
// missing writer as "writer-failed".
func TestForSeatRefusesAnAgentWhoseBinaryIsMissing(t *testing.T) {
	t.Setenv("CORRALAI_AGENT_GHOST", "/nonexistent/corral-ghost-agent {model:--model}")
	_, err := ForSeat("ghost", SeatOptions{})
	if err == nil || !errors.Is(err, ErrSeatEnvironment) || !strings.Contains(err.Error(), "ghost") || !strings.Contains(err.Error(), "corral-ghost-agent") {
		t.Fatalf("want an ErrSeatEnvironment naming seat and binary, got %v", err)
	}
	// A built-in is checked by its real binary, a shell-wrapped one included.
	t.Setenv("PATH", t.TempDir())
	for name, bin := range map[string]string{"codex": "codex", "claude-code": "claude", "antigravity": "agy"} {
		_, err := ForSeat(name, SeatOptions{})
		if err == nil || !errors.Is(err, ErrSeatEnvironment) || !strings.Contains(err.Error(), `"`+bin+`"`) {
			t.Errorf("%s: want a refusal naming binary %q, got %v", name, bin, err)
		}
	}
}

// An operator definition that wraps its CLI in a shell cannot be checked, and
// that is not a refusal: the seat proceeds, as doctor says "not checked".
func TestForSeatProceedsWhenTheBinaryCannotBeChecked(t *testing.T) {
	t.Setenv("CORRALAI_AGENT_WRAPPED", `sh -c 'corral-no-such-inner-cli "$@"' sh`)
	if _, err := ForSeat("wrapped", SeatOptions{}); err != nil {
		t.Fatalf("a shell-wrapped definition must resolve: %v", err)
	}
}

// One comparison for "is this the same seat", used by certify and review
// alike. They used to differ on case (== against EqualFold), so the same pair
// was refused at one door and let through at the other.
func TestSameSeatIgnoresCaseAndAgentNamesAreNormalised(t *testing.T) {
	t.Setenv("CORRALAI_AGENT_MINE", "/bin/cat {model:--model}")
	if name, _, ok := AgentSeat("MINE"); !ok || name != "mine" {
		t.Fatalf("AgentSeat(MINE) = %q, %v; want the normalised name mine", name, ok)
	}
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"MINE", "mine", true},
		{"mine:Model-X", "mine:model-x", true},
		{"Gemini-3.6-flash", "gemini-3.6-flash", true},
		{"codex", "codex:gpt-6-astra", false},
		{"codex:gpt-6-astra", "gpt-6-astra", true},
		{"claude-code", "codex", false},
	} {
		if got := SameSeat(c.a, c.b); got != c.want {
			t.Errorf("SameSeat(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// A pin is a model name. One that starts with `-` would reach the CLI as a
// flag of its own through {model:--model}, so it is refused by name.
func TestAPinThatLooksLikeAFlagIsRefused(t *testing.T) {
	t.Setenv("CORRALAI_AGENT_FAKE", "/bin/cat {model:--model}")
	_, err := ForSeat("fake:--dangerously-skip-permissions", SeatOptions{})
	if err == nil || !strings.Contains(err.Error(), "--dangerously-skip-permissions") || !strings.Contains(err.Error(), "flag") {
		t.Fatalf("want a refusal naming the flag-like pin, got %v", err)
	}
}

// The per-call timeout must end the whole agent, not only the process corral
// started: a shell-wrapped agent's grandchild holds stdout open, and Wait
// would otherwise block until it exits on its own.
func TestTheTimeoutEndsAShellWrappedAgentsGrandchild(t *testing.T) {
	t.Setenv("CORRALAI_AGENT_SLOW", `sh -c 'sleep 30 & wait'`)
	b, err := ForSeat("slow", SeatOptions{Timeout: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = b.Chat([]Message{{Role: "user", Content: "x"}}, nil)
	if err == nil {
		t.Fatal("a call past its timeout must fail")
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("the call took %s: the timeout did not end the agent's grandchild", d)
	}
}

// --version is probed for the record; an agent that hangs on it must not hang
// the run.
func TestAgentVersionIsBounded(t *testing.T) {
	old := agentVersionTimeout
	agentVersionTimeout = 300 * time.Millisecond
	t.Cleanup(func() { agentVersionTimeout = old })
	path := filepath.Join(t.TempDir(), "hangs")
	writeExec(t, path, "#!/bin/sh\nsleep 30\n")
	t.Setenv("CORRALAI_AGENT_HANGS", path)
	start := time.Now()
	_ = AgentVersion("hangs")
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("AgentVersion took %s on a hanging --version", d)
	}
}

// antigravity needs --dangerously-skip-permissions only to use tools: headless
// agy denies every tool it cannot prompt for. A certify seat is granted none,
// so it runs without the flag; review grants read-only tools and keeps it.
func TestAntigravitySkipsPermissionsOnlyWhenToolsAreGranted(t *testing.T) {
	bin := t.TempDir()
	writeExec(t, filepath.Join(bin, "agy"), "#!/bin/sh\necho \"ARGV $*\"\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, c := range []struct {
		tools []string
		want  bool
	}{{nil, false}, {[]string{"Read", "Grep", "Glob"}, true}} {
		b, err := ForSeat("antigravity", SeatOptions{Tools: c.tools})
		if err != nil {
			t.Fatal(err)
		}
		m, err := b.Chat([]Message{{Role: "user", Content: "brief"}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(m.Content, "--dangerously-skip-permissions"); got != c.want {
			t.Errorf("tools %v: skip flag present = %v, want %v (argv %q)", c.tools, got, c.want, m.Content)
		}
		if !strings.Contains(m.Content, "-p=brief") {
			t.Errorf("the brief must still reach agy as -p: %q", m.Content)
		}
	}
}

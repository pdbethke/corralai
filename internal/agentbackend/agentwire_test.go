// SPDX-License-Identifier: Elastic-2.0

package agentbackend

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/agentworker"
)

// The fixtures are real CLI output; testdata/agentwire/README.md records the
// commands, the versions and what was scrubbed.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "agentwire", name)) // #nosec G304 -- a test fixture
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func fixturePath(t *testing.T, name string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("testdata", "agentwire", name))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func i64(p *int64) string {
	if p == nil {
		return "nil"
	}
	b, _ := json.Marshal(*p)
	return string(b)
}

func wantUsage(t *testing.T, got Usage, in, out int, cached, written string) {
	t.Helper()
	if got.InputTokens != in || got.OutputTokens != out || i64(got.CachedInputTokens) != cached || i64(got.CacheWriteInputTokens) != written {
		t.Fatalf("usage = in %d out %d cached %s written %s; want in %d out %d cached %s written %s",
			got.InputTokens, got.OutputTokens, i64(got.CachedInputTokens), i64(got.CacheWriteInputTokens), in, out, cached, written)
	}
}

func wire(t *testing.T, name string) agentWire {
	t.Helper()
	w, ok := agentWires[name]
	if !ok {
		t.Fatalf("no wire %q registered", name)
	}
	return w
}

// claude's three input counters are DISJOINT, so the wire normalises them
// exactly as the Anthropic API backend does: InputTokens is the whole prompt.
func TestWireClaudeJSONReadsTheResultAndNormalisesUsage(t *testing.T) {
	content, u, metered, err := wire(t, "claude-json").parse(fixture(t, "claude-json-ok.json"), nil)
	if err != nil || content != "ok" || !metered {
		t.Fatalf("got %q metered=%v err=%v", content, metered, err)
	}
	wantUsage(t, u, 2+618+0, 4, "0", "618")
}

func TestWireClaudeJSONReturnsTheStructuredOutputForASchema(t *testing.T) {
	content, u, metered, err := wire(t, "claude-json").parse(fixture(t, "claude-json-schema.json"), nil)
	if err != nil || !metered {
		t.Fatalf("metered=%v err=%v", metered, err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(content), &got); err != nil || got["answer"] != "ok" {
		t.Fatalf("content %q is not the structured value: %v", content, err)
	}
	wantUsage(t, u, 2+1122+0, 53, "0", "1122")
}

// Review Focus 3: an is_error document carries its error in `.result`. That
// text is the CLI's complaint, never the model's answer.
func TestWireClaudeJSONErrorDocumentIsASeatEnvironmentFailureNeverContent(t *testing.T) {
	content, _, _, err := wire(t, "claude-json").parse(fixture(t, "claude-json-error.json"), nil)
	if !errors.Is(err, ErrSeatEnvironment) {
		t.Fatalf("want ErrSeatEnvironment, got %v", err)
	}
	if content != "" {
		t.Fatalf("the error text came back as content: %q", content)
	}
	if !strings.Contains(err.Error(), "no-such-model") {
		t.Errorf("the error should carry the CLI's own words: %v", err)
	}
}

// A terminal_reason other than "completed" is a failure even when is_error
// is false — the two fields the captured error document set, either suffices.
func TestWireClaudeJSONAnUncompletedTerminalReasonIsASeatEnvironmentFailure(t *testing.T) {
	doc := strings.Replace(string(fixture(t, "claude-json-error.json")), `"is_error":true`, `"is_error":false`, 1)
	if _, _, _, err := wire(t, "claude-json").parse([]byte(doc), nil); !errors.Is(err, ErrSeatEnvironment) {
		t.Fatalf("want ErrSeatEnvironment, got %v", err)
	}
}

func TestWireClaudeJSONThatIsNotJSONIsAPlainError(t *testing.T) {
	for _, out := range []string{"hello", `{"opinion":"a review, not a result document"}`} {
		_, _, _, err := wire(t, "claude-json").parse([]byte(out), nil)
		if err == nil || errors.Is(err, ErrSeatEnvironment) {
			t.Fatalf("%s: want a plain parse error, got %v", out, err)
		}
	}
}

func TestWireCodexReadsTheOutFileAndTheTurnUsage(t *testing.T) {
	content, u, metered, err := wire(t, "codex-jsonl").parse(fixture(t, "codex-ok.jsonl"), []byte("ok"))
	if err != nil || content != "ok" || !metered {
		t.Fatalf("got %q metered=%v err=%v", content, metered, err)
	}
	// codex's input_tokens already contains its cached half.
	wantUsage(t, u, 14728, 5, "12288", "0")
}

func TestWireCodexFallsBackToTheAgentMessageWhenTheOutFileIsEmpty(t *testing.T) {
	content, _, _, err := wire(t, "codex-jsonl").parse(fixture(t, "codex-ok.jsonl"), nil)
	if err != nil || content != "ok" {
		t.Fatalf("got %q, %v", content, err)
	}
}

func TestWireCodexSchemaReplyIsTheOutFile(t *testing.T) {
	content, u, _, err := wire(t, "codex-jsonl").parse(fixture(t, "codex-schema.jsonl"), []byte(`{"answer":"ok"}`))
	if err != nil || content != `{"answer":"ok"}` {
		t.Fatalf("got %q, %v", content, err)
	}
	wantUsage(t, u, 14277, 15, "0", "0")
}

// The captured error stream opens with an item-level `error` that is only a
// warning; the failure is the top-level error / turn.failed pair.
func TestWireCodexErrorEventIsASeatEnvironmentFailure(t *testing.T) {
	content, _, _, err := wire(t, "codex-jsonl").parse(fixture(t, "codex-error.jsonl"), nil)
	if !errors.Is(err, ErrSeatEnvironment) || content != "" {
		t.Fatalf("want ErrSeatEnvironment and no content, got %q, %v", content, err)
	}
	if !strings.Contains(err.Error(), "not supported") {
		t.Errorf("the error should carry the CLI's own words: %v", err)
	}
}

func TestWireCodexItemErrorAloneIsAWarningNotAFailure(t *testing.T) {
	lines := strings.Split(strings.TrimSpace(string(fixture(t, "codex-error.jsonl"))), "\n")
	stream := lines[1] + "\n" + string(fixture(t, "codex-ok.jsonl"))
	content, _, _, err := wire(t, "codex-jsonl").parse([]byte(stream), []byte("ok"))
	if err != nil || content != "ok" {
		t.Fatalf("an item-level warning failed the call: %q, %v", content, err)
	}
}

func TestWireTextIsUnmetered(t *testing.T) {
	w := wire(t, "text")
	content, _, metered, err := w.parse([]byte("from stdout"), nil)
	if err != nil || content != "from stdout" || metered {
		t.Fatalf("got %q metered=%v err=%v", content, metered, err)
	}
	if content, _, _, _ = w.parse([]byte("from stdout"), []byte("from out")); content != "from out" {
		t.Fatalf("the {out} file must win over stdout: %q", content)
	}
}

func TestWireArgs(t *testing.T) {
	f := &agentworker.ResponseFormat{Name: "x", Schema: map[string]any{"type": "object", "required": []any{"answer"}}}
	compact := `{"required":["answer"],"type":"object"}`

	if a, err := wire(t, "claude-json").args(f, t.TempDir()); err != nil || strings.Join(a, "|") != "--json-schema|"+compact {
		t.Fatalf("claude-json args = %q, %v", a, err)
	}
	tmp := t.TempDir()
	a, err := wire(t, "codex-jsonl").args(f, tmp)
	if err != nil || len(a) != 2 || a[0] != "--output-schema" || filepath.Dir(a[1]) != tmp {
		t.Fatalf("codex-jsonl args = %q, %v", a, err)
	}
	if b, _ := os.ReadFile(a[1]); string(b) != compact { // #nosec G304 -- the test's own temp file
		t.Fatalf("schema file holds %q", b)
	}
	for _, name := range []string{"claude-json", "codex-jsonl", "text"} {
		if a, err := wire(t, name).args(nil, t.TempDir()); err != nil || len(a) != 0 {
			t.Errorf("%s with no format must add nothing: %q, %v", name, a, err)
		}
	}
	if a, _ := wire(t, "text").args(f, t.TempDir()); len(a) != 0 {
		t.Errorf("a text wire has no schema flag; the schema rides in the prompt: %q", a)
	}
}

// Every built-in names a registered wire and a maker: the table is read by
// name, so a typo would otherwise surface only when that seat first runs.
func TestEveryBuiltinNamesARegisteredWire(t *testing.T) {
	for name, b := range builtinAgents {
		if _, ok := agentWires[b.wire]; !ok {
			t.Errorf("%s: wire %q is not registered", name, b.wire)
		}
		if b.maker == "" {
			t.Errorf("%s: no maker", name)
		}
	}
}

// The built-in certify seats are DEFINED cold: none of the operator's settings,
// MCP servers, slash commands or tools. The flag spelling is pinned against
// the measured CLI (testdata/agentwire/README.md).
func TestBuiltinSeatDefinitionsCarryTheColdFlags(t *testing.T) {
	for name, want := range map[string][]string{
		"claude-code": {"--output-format json", `--setting-sources ""`, "--strict-mcp-config", "--disable-slash-commands", "--no-session-persistence", "--system-prompt", "{tools:--tools}"},
		"codex":       {"--json", "--ignore-user-config", "--ignore-rules", "--sandbox read-only", "--ephemeral"},
	} {
		def := builtinAgents[name].def
		for _, w := range want {
			if !strings.Contains(def, w) {
				t.Errorf("%s definition lacks %q: %s", name, w, def)
			}
		}
	}
}

func TestToolsPlaceholderExpands(t *testing.T) {
	argv := []string{"x", "{tools:--tools}"}
	got, err := expandAgentArgv("a", argv, "", "/d", "/o", nil)
	if err != nil || len(got) != 3 || got[1] != "--tools" || got[2] != "" {
		t.Fatalf("no tools must be FLAG \"\": %q, %v", got, err)
	}
	got, err = expandAgentArgv("a", argv, "", "/d", "/o", []string{"Read", "Grep", "Glob"})
	if err != nil || strings.Join(got, "|") != "x|--tools|Read,Grep,Glob" {
		t.Fatalf("got %q, %v", got, err)
	}
}

// fakeSeat defines an operator agent running script with the given wire.
func fakeSeat(t *testing.T, script, wireName string) Backend {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake.sh")
	writeExec(t, path, "#!/bin/sh\n"+script)
	t.Setenv("CORRALAI_AGENT_FAKE", path+" {out}")
	if wireName != "" {
		t.Setenv("CORRALAI_AGENT_FAKE_WIRE", wireName)
	}
	b, err := ForSeat("fake", SeatOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Review Focus 3, through the whole backend: the CLI exited 0 and printed an
// is_error document. That is never a reply.
func TestChatClaudeErrorDocumentWithExitZeroIsSeatEnvironment(t *testing.T) {
	b := fakeSeat(t, "cat >/dev/null; cat '"+fixturePath(t, "claude-json-error.json")+"'\n", "claude-json")
	m, err := b.Chat([]Message{{Role: "user", Content: "hi"}}, nil)
	if !errors.Is(err, ErrSeatEnvironment) || m.Content != "" {
		t.Fatalf("want ErrSeatEnvironment and no content, got %q, %v", m.Content, err)
	}
	if !strings.Contains(err.Error(), "fake") {
		t.Errorf("the error must name the seat: %v", err)
	}
}

func TestChatCodexErrorStreamWithExitZeroIsSeatEnvironment(t *testing.T) {
	b := fakeSeat(t, "cat >/dev/null; cat '"+fixturePath(t, "codex-error.jsonl")+"'\n", "codex-jsonl")
	if _, err := b.Chat([]Message{{Role: "user", Content: "hi"}}, nil); !errors.Is(err, ErrSeatEnvironment) {
		t.Fatalf("want ErrSeatEnvironment, got %v", err)
	}
}

func TestChatNonZeroExitWithAnErrorRecordIsSeatEnvironmentWithStderr(t *testing.T) {
	b := fakeSeat(t, "cat >/dev/null; cat '"+fixturePath(t, "claude-json-error.json")+"'; echo 'unrecognized_model' >&2; exit 1\n", "claude-json")
	_, err := b.Chat([]Message{{Role: "user", Content: "hi"}}, nil)
	if !errors.Is(err, ErrSeatEnvironment) || !strings.Contains(err.Error(), "unrecognized_model") {
		t.Fatalf("want ErrSeatEnvironment carrying the stderr tail, got %v", err)
	}
}

// A non-zero exit with nothing the wire recognises is a plain seat error: an
// error, never content, and not claimed as an environment failure.
func TestChatNonZeroExitWithoutAnErrorRecordIsAPlainSeatError(t *testing.T) {
	b := fakeSeat(t, "cat >/dev/null; echo 'some reply'; echo boom >&2; exit 3\n", "text")
	m, err := b.Chat([]Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil || errors.Is(err, ErrSeatEnvironment) || m.Content != "" {
		t.Fatalf("want a plain error, got %q, %v", m.Content, err)
	}
	if !strings.Contains(err.Error(), "fake") || !strings.Contains(err.Error(), "boom") {
		t.Errorf("the error must name the seat and carry stderr: %v", err)
	}
}

// Review Focus 4: an empty reply from a text wire is an error naming the
// seat, not an empty answer handed on as if the model had said nothing.
func TestChatTextWireEmptyReplyIsAnErrorNamingTheSeat(t *testing.T) {
	b := fakeSeat(t, "cat >/dev/null\n", "")
	_, err := b.Chat([]Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "fake") {
		t.Fatalf("want an error naming the seat, got %v", err)
	}
}

func TestChatClaudeJSONFillsUsage(t *testing.T) {
	b := fakeSeat(t, "cat >/dev/null; cat '"+fixturePath(t, "claude-json-ok.json")+"'\n", "claude-json")
	m, err := b.Chat([]Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil || m.Content != "ok" {
		t.Fatalf("got %q, %v", m.Content, err)
	}
	wantUsage(t, m.Usage, 620, 4, "0", "618")
}

// The format element is found by splitFormat, exactly as the API backends
// find it, and the codex schema flag goes before the trailing `-`.
func TestChatPlacesTheSchemaFlagBeforeTheTrailingDash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fake.sh")
	writeExec(t, path, "#!/bin/sh\ncat >/dev/null\nprintf '%s' \"$*\" > \"$2\"\ncat '"+fixturePath(t, "codex-ok.jsonl")+"'\n")
	t.Setenv("CORRALAI_AGENT_FAKE", path+" -o {out} -")
	t.Setenv("CORRALAI_AGENT_FAKE_WIRE", "codex-jsonl")
	b, err := ForSeat("fake", SeatOptions{})
	if err != nil {
		t.Fatal(err)
	}
	f := agentworker.ResponseFormat{Name: "x", Schema: map[string]any{"type": "object"}}
	m, err := b.Chat([]Message{{Role: "user", Content: "hi"}}, []any{map[string]any{"type": "function"}, f})
	if err != nil {
		t.Fatal(err)
	}
	words := strings.Fields(m.Content) // the fake wrote its argv to {out}
	n := len(words)
	if n < 4 || words[n-1] != "-" || words[n-3] != "--output-schema" {
		t.Fatalf("argv %q: want --output-schema FILE before the trailing -", m.Content)
	}
	wantUsage(t, m.Usage, 14728, 5, "12288", "0")
}

func TestChatMissingBinaryIsSeatEnvironment(t *testing.T) {
	t.Setenv("CORRALAI_AGENT_FAKE", "/nonexistent/corral-no-such-agent {out}")
	b, err := ForSeat("fake", SeatOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Chat([]Message{{Role: "user", Content: "hi"}}, nil); !errors.Is(err, ErrSeatEnvironment) {
		t.Fatalf("want ErrSeatEnvironment, got %v", err)
	}
}

func TestAnUnknownWireIsRefusedByName(t *testing.T) {
	t.Setenv("CORRALAI_AGENT_FAKE", "/bin/cat")
	t.Setenv("CORRALAI_AGENT_FAKE_WIRE", "smoke-signals")
	if _, err := ForSeat("fake", SeatOptions{}); err == nil || !strings.Contains(err.Error(), "smoke-signals") || !strings.Contains(err.Error(), "CORRALAI_AGENT_FAKE_WIRE") {
		t.Fatalf("want a refusal naming the wire and its variable, got %v", err)
	}
}

// CORRALAI_AGENT_<NAME>_WIRE declares a wire; it must not also define an
// agent called <name>-wire whose command line is the wire's name.
func TestAWireVariableIsNotAnAgentDefinition(t *testing.T) {
	t.Setenv("CORRALAI_AGENT_FAKE", "/bin/cat")
	t.Setenv("CORRALAI_AGENT_FAKE_WIRE", "text")
	if _, _, ok := AgentSeat("fake-wire"); ok {
		t.Error("fake-wire resolved as an agent")
	}
	for _, n := range DefinedAgents() {
		if n == "fake-wire" {
			t.Error("DefinedAgents lists the wire variable as an agent")
		}
	}
}

// An operator who redefines a built-in's command line gets the text wire
// unless they also declare one: their command may not print the built-in's
// JSON, and reading it as if it did would turn its reply into a parse error.
func TestAnOperatorRedefinitionOfABuiltinDefaultsToText(t *testing.T) {
	t.Setenv("CORRALAI_AGENT_CODEX", "/bin/cat")
	b, err := ForSeat("codex", SeatOptions{})
	if err != nil {
		t.Fatal(err)
	}
	m, err := b.Chat([]Message{{Role: "user", Content: "plain"}}, nil)
	if err != nil || m.Content != "plain" {
		t.Fatalf("got %q, %v", m.Content, err)
	}
}

// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/sandbox"
)

// fakeRunner is a stub commandRunner returning canned stdout/err and
// recording the command it was asked to run.
type fakeRunner struct {
	out string
	err error
	got []string
}

func (f *fakeRunner) Enumerate(_ context.Context, _ map[string]string, cmd []string) (string, error) {
	f.got = cmd
	return f.out, f.err
}

// stubPlugin implements lang.Plugin ONLY — deliberately not
// lang.CoverageReporter — to exercise the "language has no instrumentation"
// state.
type stubPlugin struct{ name string }

func (p stubPlugin) Name() string                                { return p.name }
func (stubPlugin) Detect(string) bool                            { return false }
func (stubPlugin) Scaffold() map[string]string                   { return nil }
func (stubPlugin) TestCmd() []string                             { return nil }
func (stubPlugin) CompileCheck(string, string) []string          { return nil }
func (stubPlugin) TestPaths(string) []lang.TestCandidate         { return nil }
func (stubPlugin) Preflight([]string) error                      { return nil }
func (stubPlugin) PromptLang() string                            { return "" }
func (stubPlugin) TestWriterSystem() string                      { return "" }
func (stubPlugin) MutantSystem() string                          { return "" }
func (stubPlugin) SingleTestCmd(string, string) ([]string, bool) { return nil, false }
func (stubPlugin) ListTestsCmd(string) ([]string, bool)          { return nil, false }
func (stubPlugin) ParseTestList(string) []string                 { return nil }

var _ lang.Plugin = stubPlugin{}

// covPlugin embeds stubPlugin (so it satisfies lang.Plugin) and additionally
// implements lang.CoverageReporter with test-controlled behaviour.
type covPlugin struct {
	stubPlugin
	cmd     []string
	cmdOK   bool
	parseFn func(stdout, modulePath string) (map[string]bool, error)
}

func (p covPlugin) CoverageCmd(testCmd []string) ([]string, bool) { return p.cmd, p.cmdOK }
func (p covPlugin) ParseCoverage(stdout, modulePath string) (map[string]bool, error) {
	return p.parseFn(stdout, modulePath)
}

var _ lang.CoverageReporter = covPlugin{}

func TestPreflight_HappyPath(t *testing.T) {
	runner := &fakeRunner{out: "mode: set\nrepo/a.go:1.1,2.2 1 1\n"}
	p := covPlugin{
		stubPlugin: stubPlugin{name: "go"},
		cmd:        []string{"sh", "-c", "go test -coverprofile=/dev/stdout"},
		cmdOK:      true,
		parseFn: func(stdout, modulePath string) (map[string]bool, error) {
			return map[string]bool{"a.go": true}, nil
		},
	}

	got := Preflight(context.Background(), runner, nil, p, []string{"go", "test", "./..."}, "repo")

	if !got.Ran {
		t.Fatalf("Ran = false, want true; Note=%q", got.Note)
	}
	if got.Executed == nil || !got.Executed["a.go"] {
		t.Fatalf("Executed = %v, want {a.go: true}", got.Executed)
	}
	if len(runner.got) == 0 {
		t.Fatal("runner was never invoked")
	}
}

func TestPreflight_NoReporter(t *testing.T) {
	p := stubPlugin{name: "ruby"}
	runner := &fakeRunner{out: "should not be read"}

	got := Preflight(context.Background(), runner, nil, p, []string{"rspec"}, "repo")

	if got.Ran {
		t.Fatal("Ran = true, want false: plugin does not implement CoverageReporter")
	}
	if got.Executed != nil {
		t.Fatalf("Executed = %v, want nil (not empty map)", got.Executed)
	}
	if !strings.Contains(got.Note, "ruby") {
		t.Fatalf("Note = %q, want it to name the language %q", got.Note, "ruby")
	}
	if runner.got != nil {
		t.Fatal("runner was invoked; a language with no instrumentation must never run a command")
	}
}

func TestPreflight_CoverageCmdUnsupported(t *testing.T) {
	// A plugin that DOES implement CoverageReporter but declines this
	// particular test command (CoverageCmd ok=false) must fail the same
	// way as "no reporter at all" — never fabricate a run.
	p := covPlugin{
		stubPlugin: stubPlugin{name: "python"},
		cmdOK:      false,
		parseFn: func(string, string) (map[string]bool, error) {
			t.Fatal("ParseCoverage must not be called when CoverageCmd declines")
			return nil, nil
		},
	}
	runner := &fakeRunner{}

	got := Preflight(context.Background(), runner, nil, p, []string{"pytest", "--weird-flag"}, "repo")

	if got.Ran {
		t.Fatal("Ran = true, want false: CoverageCmd declined this test command")
	}
	if got.Executed != nil {
		t.Fatalf("Executed = %v, want nil", got.Executed)
	}
	if !strings.Contains(got.Note, "python") {
		t.Fatalf("Note = %q, want it to name the language", got.Note)
	}
	if runner.got != nil {
		t.Fatal("runner was invoked despite CoverageCmd declining")
	}
}

func TestPreflight_CommandFailed(t *testing.T) {
	p := covPlugin{
		stubPlugin: stubPlugin{name: "go"},
		cmd:        []string{"sh", "-c", "go test -coverprofile=/dev/stdout"},
		cmdOK:      true,
		parseFn: func(string, string) (map[string]bool, error) {
			t.Fatal("ParseCoverage must not be called when the run itself errored")
			return nil, nil
		},
	}
	runner := &fakeRunner{err: errors.New("boom: sandbox timeout")}

	got := Preflight(context.Background(), runner, nil, p, []string{"go", "test", "./..."}, "repo")

	if got.Ran {
		t.Fatal("Ran = true, want false: the run itself errored")
	}
	if got.Executed != nil {
		t.Fatalf("Executed = %v, want nil", got.Executed)
	}
	if !strings.Contains(got.Note, "boom: sandbox timeout") {
		t.Fatalf("Note = %q, want it to carry the underlying error", got.Note)
	}
}

// TestPreflight_TruncatedOutputIsItsOwnFailure pins the 64 KiB fix: a
// runner's output carrying sandbox.TruncationMarker (the way sandbox.Run
// reports a head-truncated combined stdout+stderr) must be reported as its
// own distinct failure BEFORE ParseCoverage ever runs on it — never
// forwarded to ParseCoverage, where a truncation that happens to land on a
// clean boundary could parse as a valid-looking but incomplete report.
func TestPreflight_TruncatedOutputIsItsOwnFailure(t *testing.T) {
	p := covPlugin{
		stubPlugin: stubPlugin{name: "python"},
		cmd:        []string{"sh", "-c", "coverage json -o -"},
		cmdOK:      true,
		parseFn: func(string, string) (map[string]bool, error) {
			t.Fatal("ParseCoverage must not be called on truncated output")
			return nil, nil
		},
	}
	runner := &fakeRunner{out: `{"meta": {"format": 3}, "files": {` + "\n" + sandbox.TruncationMarker}

	got := Preflight(context.Background(), runner, nil, p, []string{"pytest", "-q"}, "repo")

	if got.Ran {
		t.Fatal("Ran = true, want false: the output was truncated")
	}
	if got.Executed != nil {
		t.Fatalf("Executed = %v, want nil", got.Executed)
	}
	if !strings.Contains(got.Note, "truncated") {
		t.Fatalf("Note = %q, want it to say the output was truncated", got.Note)
	}
}

// TestPreflight_MarkerTextMidStreamIsNotMistakenForTruncation pins the F2
// fix: the truncation marker is checked as a SUFFIX, not a substring. stdout
// carries the wrapped suite's own output ahead of the payload (pytest's
// dot-progress, a test's own printed/asserted strings); a project whose
// tests happen to print or assert on the literal marker text, anywhere
// OTHER than the very end, must not be misdiagnosed as truncated forever.
func TestPreflight_MarkerTextMidStreamIsNotMistakenForTruncation(t *testing.T) {
	parsed := false
	p := covPlugin{
		stubPlugin: stubPlugin{name: "go"},
		cmd:        []string{"sh", "-c", "go test -coverprofile=/dev/stdout"},
		cmdOK:      true,
		parseFn: func(string, string) (map[string]bool, error) {
			parsed = true
			return map[string]bool{"a.go": true}, nil
		},
	}
	// The marker text appears mid-stream (e.g. a test asserting on it) but
	// the ACTUAL end of stdout is a clean, untruncated coverage profile.
	runner := &fakeRunner{out: "some test printed " + sandbox.TruncationMarker + " as part of its own output\n" +
		"mode: set\nrepo/a.go:1.1,2.2 1 1\n"}

	got := Preflight(context.Background(), runner, nil, p, []string{"go", "test", "./..."}, "repo")

	if !got.Ran {
		t.Fatalf("Ran = false, want true: the marker text mid-stream must not be mistaken for real truncation; Note=%q", got.Note)
	}
	if !parsed {
		t.Fatal("ParseCoverage was never called — the mid-stream marker text short-circuited it")
	}
}

func TestPreflight_Unparseable(t *testing.T) {
	p := covPlugin{
		stubPlugin: stubPlugin{name: "go"},
		cmd:        []string{"sh", "-c", "go test -coverprofile=/dev/stdout"},
		cmdOK:      true,
		parseFn: func(string, string) (map[string]bool, error) {
			return nil, errors.New("lang: coverage report has no \"mode:\" header (got 3 bytes)")
		},
	}
	runner := &fakeRunner{out: "???"}

	got := Preflight(context.Background(), runner, nil, p, []string{"go", "test", "./..."}, "repo")

	if got.Ran {
		t.Fatal("Ran = true, want false: the report was unparseable")
	}
	if got.Executed != nil {
		t.Fatalf("Executed = %v, want nil", got.Executed)
	}
	if !strings.Contains(got.Note, "mode") {
		t.Fatalf("Note = %q, want it to carry the parse error", got.Note)
	}
}

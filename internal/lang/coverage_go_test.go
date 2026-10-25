// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestGoParseCoverageExecutedFiles(t *testing.T) {
	// go test -coverprofile emits "mode:" then one line per block:
	//   <import-path>/<file>:<startLine>.<col>,<endLine>.<col> <numStmt> <count>
	const out = `mode: set
github.com/x/proj/pkg/a.go:3.10,5.2 1 1
github.com/x/proj/pkg/b.go:7.1,9.4 2 0
github.com/x/proj/c.go:1.1,2.2 1 3
`
	p := goPlugin{}
	got, err := p.ParseCoverage(out, "github.com/x/proj")
	if err != nil {
		t.Fatalf("ParseCoverage: %v", err)
	}
	// count>0 means executed (true); b.go has count 0 so it is MEASURED but
	// not executed (false) — present in the map either way, per the
	// tri-state contract (present-true / present-false / absent).
	want := map[string]bool{"pkg/a.go": true, "pkg/b.go": false, "c.go": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("executed set = %v, want %v", got, want)
	}
}

// TestGoParseCoverageMeasuredNeverExecutedStaysFalseAcrossBlocks pins the
// tri-state merge rule for a file with MULTIPLE blocks: once any block
// reports count > 0, a later count-0 block for the same file must not
// overwrite it back to false, and a file whose every block is count == 0
// stays false rather than being dropped from the map.
func TestGoParseCoverageMeasuredNeverExecutedStaysFalseAcrossBlocks(t *testing.T) {
	const out = `mode: set
github.com/x/proj/a.go:1.1,2.2 1 1
github.com/x/proj/a.go:3.1,4.2 1 0
github.com/x/proj/b.go:1.1,2.2 1 0
github.com/x/proj/b.go:3.1,4.2 1 0
`
	p := goPlugin{}
	got, err := p.ParseCoverage(out, "github.com/x/proj")
	if err != nil {
		t.Fatalf("ParseCoverage: %v", err)
	}
	want := map[string]bool{"a.go": true, "b.go": false}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("executed set = %v, want %v", got, want)
	}
}

// TestGoParseCoverageUnparseableIsError pins the design point that matters
// most here: a report the parser cannot make sense of must come back as an
// ERROR, never a silently-empty map. A later caller turns "no coverage data"
// into a repo-wide finding — if ParseCoverage swallowed garbage into an empty
// map, that finding would be fabricated from noise, not evidence.
func TestGoParseCoverageUnparseableIsError(t *testing.T) {
	p := goPlugin{}

	cases := map[string]string{
		"empty input":                "",
		"no mode header at all":      "just some random text\nthat is not a coverage profile\n",
		"garbled block after mode":   "mode: set\nthis line is not a coverage block\n",
		"non-numeric count":          "mode: set\ngithub.com/x/proj/a.go:3.10,5.2 1 notanumber\n",
		"block missing a colon path": "mode: set\nnocolonhere 1 1\n",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := p.ParseCoverage(in, "github.com/x/proj")
			if err == nil {
				t.Fatalf("ParseCoverage(%q) = %v, nil error; want an error", in, got)
			}
			if got != nil {
				t.Fatalf("ParseCoverage(%q) returned non-nil map %v alongside an error", in, got)
			}
		})
	}
}

// TestGoCoverageCmdIncludesCoverpkgAll pins the task-5 sweep fix: without
// -coverpkg=./..., `go test ./...` instruments each test binary for ONLY
// the package it directly tests, so a package with no _test.go files of
// its own always reports synthetic all-zero coverage — even when its code
// is genuinely executed via a shared interface from another package's
// tests (verified on gin: codec/json/json.go, called every run through
// json.API.Marshal from the root package's own errors_test.go, was
// reported "measured, never executed" without this flag). -coverpkg=./...
// is what makes cross-package execution visible to the tri-state map.
func TestGoCoverageCmdIncludesCoverpkgAll(t *testing.T) {
	p := goPlugin{}
	cmd, ok := p.CoverageCmd([]string{"go", "test", "./..."})
	if !ok {
		t.Fatalf("CoverageCmd ok=false")
	}
	if len(cmd) != 3 || cmd[0] != "sh" || cmd[1] != "-c" {
		t.Fatalf("CoverageCmd = %v, want [sh -c <script>]", cmd)
	}
	script := cmd[2]
	if !strings.Contains(script, "-coverpkg=./...") {
		t.Fatalf("CoverageCmd script = %q, missing -coverpkg=./... (without it, cross-package execution is invisible and untested packages are falsely reported as unexecuted)", script)
	}
	// -coverpkg=./... makes the RAW profile carry one block set per (test
	// binary × imported covered package) — measured up to 253 MB on a
	// large real repo (grpc-go). The script must reduce it before it ever
	// reaches stdout, or the fix for the false-unexecuted-finding bug
	// converts "works, with a false positive" into "never completes" for
	// any repo above a few dozen packages.
	if !strings.Contains(script, "awk '") {
		t.Fatalf("CoverageCmd script = %q, missing the awk profile reduction — -coverpkg=./... without it produces a profile that scales ~quadratically with package count", script)
	}
}

// TestGoCoverageReduceScriptCollapsesDuplicateBlocksPreservingTriState
// actually RUNS goCoverageReduceScript (not a string-containment check) —
// the same shell reduction CoverageCmd wires after -coverpkg's raw profile
// — against a synthetic raw profile shaped exactly like what -coverpkg=./...
// produces on a real repo: the SAME file appearing multiple times (once per
// test binary that imports it), with different per-occurrence counts. It
// must collapse to exactly one line per file, and the tri-state answer
// (executed / measured-and-never-executed) must be identical to what
// ParseCoverage would have computed from the raw, unreduced profile — the
// reduction is a size optimization, not a semantic change.
func TestGoCoverageReduceScriptCollapsesDuplicateBlocksPreservingTriState(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh on PATH")
	}
	if _, err := exec.LookPath("awk"); err != nil {
		t.Skip("no awk on PATH")
	}
	dir := t.TempDir()
	profile := filepath.Join(dir, "profile.out")
	// a.go: one test binary saw it unexecuted (0), another saw it executed
	// (1) — the real shape -coverpkg=./... produces when a file is
	// imported by two different tested packages. b.go: every occurrence,
	// across every test binary, is 0 — genuinely measured and never
	// executed. c.go: one occurrence, executed with a real statement count.
	raw := "mode: set\n" +
		"github.com/x/a.go:1.1,2.2 1 0\n" +
		"github.com/x/a.go:3.1,4.2 1 0\n" +
		"github.com/x/a.go:1.1,2.2 1 1\n" +
		"github.com/x/b.go:1.1,2.2 1 0\n" +
		"github.com/x/b.go:5.1,6.2 1 0\n" +
		"github.com/x/c.go:1.1,2.2 1 5\n"
	if err := os.WriteFile(profile, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}

	// goCoverageReduceScript reads from "$f" — set it exactly the way
	// CoverageCmd's own `f=$(mktemp)` does, just pointed at the fixture
	// instead of a fresh mktemp, since this test cares about the reduction
	// alone, not the surrounding test-command invocation.
	script := `f=` + shellQuote(profile) + `; ` + goCoverageReduceScript
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "sh", "-c", script).Output()
	if err != nil {
		t.Fatalf("running goCoverageReduceScript: %v", err)
	}

	// Exactly one line per file: 1 mode line + 3 file lines, however many
	// raw block lines fed in (6 here; -coverpkg=./... on a real repo can
	// feed in orders of magnitude more per file without growing this).
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("reduced output has %d lines, want 4 (mode + 3 files): %q", len(lines), out)
	}

	p := goPlugin{}
	got, perr := p.ParseCoverage(string(out), "github.com/x")
	if perr != nil {
		t.Fatalf("ParseCoverage(reduced output): %v (reduced output was %q)", perr, out)
	}
	want := map[string]bool{"a.go": true, "b.go": false, "c.go": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseCoverage(reduced output) = %v, want %v; reduced output was %q", got, want, out)
	}
}

// TestGoCoverageReduceScriptPassesMalformedLinesThroughUnchanged pins the
// fail-closed guard the review round required explicitly stay in Go: the
// shell reduction must never itself decide a malformed line is fine to drop
// or coerce — it passes anything that doesn't match the exact 3-field
// "<path>:<range> <numStmt> <count>" shape straight through, byte for byte,
// so ParseCoverage's own validation (never relocated) still sees the exact
// original bytes and still errors on it, identically to before this
// reduction existed.
func TestGoCoverageReduceScriptPassesMalformedLinesThroughUnchanged(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh on PATH")
	}
	if _, err := exec.LookPath("awk"); err != nil {
		t.Skip("no awk on PATH")
	}
	cases := map[string]string{
		"garbled line, wrong field count": "mode: set\nthis line is not a coverage block\n",
		"non-numeric count":               "mode: set\ngithub.com/x/a.go:1.1,2.2 1 notanumber\n",
		"no colon in the path field":      "mode: set\nnocolonhere 1 1\n",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			profile := filepath.Join(dir, "profile.out")
			if err := os.WriteFile(profile, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			script := `f=` + shellQuote(profile) + `; ` + goCoverageReduceScript
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, "sh", "-c", script).Output()
			if err != nil {
				t.Fatalf("running goCoverageReduceScript: %v", err)
			}
			p := goPlugin{}
			if _, perr := p.ParseCoverage(string(out), ""); perr == nil {
				t.Fatalf("ParseCoverage(reduced output) = nil error, want an error — the malformed line must survive the reduction unchanged, not be silently normalized away; reduced output was %q", out)
			}
		})
	}
}

// TestGoCoverageCmdStillReportsWhenTestsFail is the regression for the
// review's Minor 4. goPlugin.CoverageCmd joined the test invocation to the
// reduction with `&&`, so an ordinary RED suite — `go test`'s exit 1 — meant
// awk never ran and no profile ever reached stdout, even though `go test
// -coverprofile` had already written a perfectly usable one. The pre-flight
// then blamed the report ("no \"mode:\" header"), and silently no-opped on
// precisely the weak-suite repos corral exists to audit.
//
// pyPlugin.CoverageCmd has handled this deliberately since it was written
// (`rc=$?; case $rc in 0|1)`); this pins the same treatment for Go.
func TestGoCoverageCmdStillReportsWhenTestsFail(t *testing.T) {
	for _, bin := range []string{"sh", "awk", "go"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("no %s on PATH", bin)
		}
	}
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/redsuite\n\ngo 1.22\n")
	write("used.go", "package redsuite\n\nfunc Used() int { return 1 }\n")
	write("unused.go", "package redsuite\n\nfunc Unused() int { return 2 }\n")
	// One ordinary failing test — a red suite, not a broken toolchain.
	write("used_test.go", "package redsuite\n\nimport \"testing\"\n\n"+
		"func TestUsed(t *testing.T) {\n\tif Used() != 99 {\n\t\tt.Fatal(\"deliberately red\")\n\t}\n}\n")

	cmd, ok := goPlugin{}.CoverageCmd([]string{"go", "test", "./..."})
	if !ok {
		t.Fatal("CoverageCmd declined `go test ./...`")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	// #nosec G204 -- argv is this package's own CoverageCmd output.
	c := exec.CommandContext(ctx, cmd[0], cmd[1:]...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GOFLAGS=-mod=mod")
	out, _ := c.Output() // a red suite exits non-zero; that is the point

	got, err := goPlugin{}.ParseCoverage(string(out), "example.com/redsuite")
	if err != nil {
		t.Fatalf("ParseCoverage after a RED suite: %v (a failing test must not discard the profile go test already wrote); output was %q", err, out)
	}
	if v, measured := got["used.go"]; !measured || !v {
		t.Errorf("used.go = (%v, measured=%v), want executed: the failing test still called Used()", v, measured)
	}
	if v, measured := got["unused.go"]; !measured || v {
		t.Errorf("unused.go = (%v, measured=%v), want measured-and-never-executed", v, measured)
	}
}

// TestGoCoverageCmdClaimsOnlyInstrumentableCommands: goPlugin.CoverageCmd
// appends `-coverpkg=./... -coverprofile=…` to the command it is handed, so
// it may claim only a command that passes those flags to `go test`.
// Measured 2026-10-11 (see the plan's evidence table): `go test …` and
// `gotestsum -- …` write a profile; `make test`, bare `gotestsum` and
// `gotestsum --format x` exit 2/3 with none. Claiming those made every mixed
// Go repository's pre-flight decline as ambiguous.
func TestGoCoverageCmdClaimsOnlyInstrumentableCommands(t *testing.T) {
	accept := [][]string{
		{"go", "test", "./..."},
		{"go", "test", "-v", "./..."},
		{"/usr/local/go/bin/go", "test", "./..."},
		{"go.exe", "test", "./..."},
		{"go", "-C", "sub", "test", "./..."},
		{"gotestsum", "--", "./..."},
		{"gotestsum", "--format", "testname", "--", "-v", "./..."},
	}
	refuse := [][]string{
		{},
		{"make", "test"},
		{"gotestsum"},
		{"gotestsum", "--format", "testname"},
		{"go", "vet", "./..."},
		{"go", "build", "./..."},
		{"go"},
		{"sh", "-c", "go test ./..."},
		// Everything after -args (or --args) goes to the test binary, so
		// the appended -coverprofile never reaches go test. Measured
		// 2026-10-11, go1.26.9: both spellings exit 1 with no profile.
		{"go", "test", "./...", "-args", "-v"},
		{"go", "test", "./...", "--args", "-v"},
		// gotestsum forwards everything after -- to go test, so the same
		// rule follows; not run (gotestsum was not installed).
		{"gotestsum", "--", "./...", "-args", "-v"},
		{"python3", "-m", "pytest", "-q"},
		{"node", "--test"},
	}
	for _, c := range accept {
		if _, ok := (goPlugin{}).CoverageCmd(c); !ok {
			t.Errorf("CoverageCmd(%q) refused; it passes the coverage flags to go test", c)
		}
	}
	for _, c := range refuse {
		if _, ok := (goPlugin{}).CoverageCmd(c); ok {
			t.Errorf("CoverageCmd(%q) accepted; the appended coverage flags cannot reach go test", c)
		}
	}
}

// THREE BLOCKS, ONE FILE: statements are summed per file, and a block counts
// as executed by its hit, not by how many statements it holds.
func TestGoCoverageCountsSumsBlocksPerFile(t *testing.T) {
	raw := "mode: set\n" +
		"github.com/x/a.go:1.1,2.2 2 1\n" +
		"github.com/x/a.go:3.1,4.2 3 1\n" +
		"github.com/x/a.go:5.1,6.2 4 0\n"
	got, err := goPlugin{}.CoverageCounts(raw, "github.com/x")
	if err != nil {
		t.Fatal(err)
	}
	if want := (LineCounts{Executed: 5, Executable: 9}); got["a.go"] != want {
		t.Errorf("a.go = %+v, want %+v", got["a.go"], want)
	}
}

// -coverpkg REPEATS A BLOCK PER TEST BINARY: it is one block, counted once,
// and it ran if any binary ran it.
func TestGoCoverageCountsDeduplicatesBlocksRepeatedPerTestBinary(t *testing.T) {
	raw := "mode: set\n" +
		"x/a.go:1.1,2.2 2 0\n" +
		"x/a.go:3.1,4.2 3 0\n" +
		"x/a.go:1.1,2.2 2 7\n" +
		"x/a.go:1.1,2.2 2 0\n" +
		"x/b.go:1.1,2.2 1 0\n" +
		"x/b.go:1.1,2.2 1 0\n"
	got, err := goPlugin{}.CoverageCounts(raw, "")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]LineCounts{"x/a.go": {Executed: 2, Executable: 5}, "x/b.go": {Executed: 0, Executable: 1}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestGoCoverageCountsUnparseableIsError(t *testing.T) {
	for _, raw := range []string{"", "x/a.go:1.1,2.2 1 1\n", "mode: set\nx/a.go:1.1,2.2 one 1\n", "mode: set\nx/a.go:1.1,2.2 1\n"} {
		if got, err := (goPlugin{}).CoverageCounts(raw, ""); err == nil {
			t.Errorf("CoverageCounts(%q) = %v, want an error", raw, got)
		}
	}
}

// A REAL PROFILE: captured by running corral's own internal/pairing tests with
// -coverpkg. Dedupe is a function the package tests call, so it has executed
// statements, and no file can execute more statements than it holds.
func TestGoCoverageCountsFromRealProfile(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "coverage", "go-pairing-profile.txt"))
	if err != nil {
		t.Fatal(err)
	}
	const mod = "github.com/pdbethke/corralai"
	counts, err := goPlugin{}.CoverageCounts(string(raw), mod)
	if err != nil {
		t.Fatal(err)
	}
	dedupe := counts["internal/pairing/dedupe.go"]
	if dedupe.Executed == 0 || dedupe.Executable < dedupe.Executed {
		t.Errorf("dedupe.go = %+v, want executed > 0 and executable >= executed", dedupe)
	}
	for p, c := range counts {
		if c.Executed > c.Executable {
			t.Errorf("%s: executed %d exceeds executable %d", p, c.Executed, c.Executable)
		}
	}
	executed, err := goPlugin{}.ParseCoverage(string(raw), mod)
	if err != nil {
		t.Fatal(err)
	}
	for p, c := range counts {
		if executed[p] != (c.Executed > 0) {
			t.Errorf("%s: ParseCoverage %v disagrees with %+v", p, executed[p], c)
		}
	}
}

// THE REDUCER KEEPS THE COUNTS: run for real over a profile with repeated
// blocks and a zero-statement block that ran, it must give the same counts
// as the raw profile does, and must not turn that file's executed bit off.
func TestGoCoverageReduceScriptKeepsCountsAndTheExecutedBit(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh on PATH")
	}
	if _, err := exec.LookPath("awk"); err != nil {
		t.Skip("no awk on PATH")
	}
	raw := "mode: set\n" +
		"x/a.go:1.1,2.2 2 0\n" +
		"x/a.go:3.1,4.2 3 1\n" +
		"x/a.go:1.1,2.2 2 1\n" +
		"x/empty.go:1.1,1.5 0 1\n" + // an empty function body that ran
		"x/dead.go:1.1,2.2 4 0\n"
	profile := filepath.Join(t.TempDir(), "profile.out")
	if err := os.WriteFile(profile, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "sh", "-c", `f=`+shellQuote(profile)+`; `+goCoverageReduceScript).Output()
	if err != nil {
		t.Fatal(err)
	}
	p := goPlugin{}
	fromRaw, err := p.CoverageCounts(raw, "x")
	if err != nil {
		t.Fatal(err)
	}
	fromReduced, err := p.CoverageCounts(string(out), "x")
	if err != nil {
		t.Fatalf("%v; reduced was %q", err, out)
	}
	if !reflect.DeepEqual(fromRaw, fromReduced) {
		t.Errorf("reduced counts %v differ from raw counts %v; reduced was %q", fromReduced, fromRaw, out)
	}
	if want := (LineCounts{Executed: 5, Executable: 5}); fromReduced["a.go"] != want {
		t.Errorf("a.go = %+v, want %+v", fromReduced["a.go"], want)
	}
	for _, in := range []string{raw, string(out)} {
		executed, err := p.ParseCoverage(in, "x")
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]bool{"a.go": true, "empty.go": true, "dead.go": false}
		if !reflect.DeepEqual(executed, want) {
			t.Errorf("ParseCoverage = %v, want %v (input %q)", executed, want, in)
		}
	}
}

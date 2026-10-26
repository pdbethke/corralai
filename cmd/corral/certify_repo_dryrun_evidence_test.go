// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/reposcan"
)

// dryRunDefaultSuffix is the candidacy line's ending on a dry run that ran
// no evidence pass, verbatim: the plan says where evidence pairing comes
// from instead of printing zeros a reader could take for a measurement.
const dryRunDefaultSuffix = " — evidence pairing runs on a real run or with --dry-run --evidence"

// refuseModelEndpoints fails the test if certify --repo builds a goal
// deriver, and clears every provider credential and backend override, so a
// seat resolved anywhere on the path would be refused rather than reached.
func refuseModelEndpoints(t *testing.T) {
	t.Helper()
	noKeys(t)
	prev := certifyRepoDeriver
	certifyRepoDeriver = func(model, endpoint string) (reposcan.Deriver, error) {
		t.Errorf("a model endpoint was constructed (deriver %q at %q) on a path that must call no model", model, endpoint)
		return prev(model, endpoint)
	}
	t.Cleanup(func() { certifyRepoDeriver = prev })
}

// fakeSelectionPass stands in for Python's one instrumented run and counts
// its calls; run, when set, is what the call does.
type fakeSelectionPass struct {
	mu    sync.Mutex
	calls int
	run   func(ctx context.Context) reposcan.SelectionEvidence
}

func (f *fakeSelectionPass) install(t *testing.T) {
	t.Helper()
	prev := collectSelectionEvidence
	collectSelectionEvidence = func(ctx context.Context, _ coverageRunner, _ map[string]string, _ lang.Plugin, _ []string, _ []string) reposcan.SelectionEvidence {
		f.mu.Lock()
		f.calls++
		f.mu.Unlock()
		if f.run != nil {
			return f.run(ctx)
		}
		return reposcan.SelectionEvidence{Note: "fake: no selection evidence"}
	}
	t.Cleanup(func() { collectSelectionEvidence = prev })
}

func (f *fakeSelectionPass) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// The free planning run, with --evidence: no seat flag, no key, no goals —
// the unit pass still runs (here a fake standing in for the jail's runs)
// and the file only a unit's execution pairs is in the plan, as a real run
// would audit it.
func TestDryRunEvidenceRunsThePassWithNoModel(t *testing.T) {
	refuseModelEndpoints(t)
	root := unitEvidenceTSFixture(t)
	fake := &fakeUnitPass{ev: reposcan.UnitEvidence{Ran: true, Units: []reposcan.UnitResult{
		unitRun("tests/composition/service.test.ts", map[string]lang.LineCounts{"src/app/Service.ts": {Executed: 1, Executable: 1}}),
	}}}
	fake.install(t)

	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{
		"--repo", root, "--dry-run", "--evidence", "--substrate", substrateWorkspace, "--no-selection-cache",
		"--", "vitest", "run",
	}, &out, &errb)
	s := out.String()
	if code != 0 {
		t.Fatalf("exit %d, want 0\nstdout:\n%s\nstderr:\n%s", code, s, errb.String())
	}
	if n := len(fake.called()); n != 1 {
		t.Fatalf("unit pass called %d time(s), want once:\n%s", n, s)
	}
	if c := fake.called()[0]; c.workers != 1 {
		t.Errorf("unit pass ran %d at a time; want the workspace substrate's own worker count, 1, as a real run resolves it", c.workers)
	}
	for _, want := range []string{
		"src/app/Service.ts paired by evidence: 1 covering test unit(s), authored test lands beside tests/composition/service.test.ts",
		"evidence-paired 1 · name-paired 0",
		"would audit 1 file(s), in rank order:",
		"src/app/Service.ts (typescript) — graded by the tests that cover it; a written test lands beside tests/composition/service.test.ts",
		"units: 1 run, 0 failed",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, dryRunDefaultSuffix) || strings.Contains(s, "unknown without a run") {
		t.Errorf("an evidence dry run measured pairing, so it must not say evidence was not run:\n%s", s)
	}
}

// --evidence changes only the dry run: a real run always collects evidence,
// so the flag there is refused before anything is resolved or spent.
func TestEvidenceWithoutDryRunIsAUsageError(t *testing.T) {
	refuseModelEndpoints(t)
	root := unitEvidenceTSFixture(t)
	units := &fakeUnitPass{}
	units.install(t)

	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{
		"--repo", root, "--evidence", "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off",
		"--goals", writeGoals(t, root, `{}`), "--substrate", substrateWorkspace,
		"--", "vitest", "run",
	}, &out, &errb)
	if code != 2 {
		t.Fatalf("exit %d, want 2\nstderr:\n%s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "--evidence only changes --dry-run; a real run always collects evidence") {
		t.Errorf("stderr must say why, verbatim:\n%s", errb.String())
	}
	if n := len(units.called()); n != 0 {
		t.Errorf("the unit pass ran %d time(s) on a refused invocation", n)
	}
}

// A dry run without --evidence stays what it was: no pass of either kind
// runs, and the candidacy line ends by saying where evidence pairing does.
func TestDryRunWithoutEvidenceRunsNothing(t *testing.T) {
	refuseModelEndpoints(t)
	for _, tc := range []struct {
		name string
		args func(t *testing.T) []string
	}{
		{"typescript", func(t *testing.T) []string {
			return []string{"--repo", unitEvidenceTSFixture(t), "--dry-run", "--", "vitest", "run"}
		}},
		{"python", func(t *testing.T) []string {
			root := t.TempDir()
			preflightUnpairedPythonFixture(t, root)
			return []string{"--repo", root, "--dry-run"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			units := &fakeUnitPass{}
			units.install(t)
			sel := &fakeSelectionPass{}
			sel.install(t)

			var out, errb bytes.Buffer
			code := runCertifyRepo(tc.args(t), &out, &errb)
			s := out.String()
			if code != 0 {
				t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, s, errb.String())
			}
			if n := len(units.called()); n != 0 {
				t.Errorf("a plain dry run ran the unit pass %d time(s)", n)
			}
			if n := sel.count(); n != 0 {
				t.Errorf("a plain dry run ran the selection pass %d time(s)", n)
			}
			for _, banned := range []string{"selection:", "units:"} {
				if strings.Contains(s, banned) {
					t.Errorf("a plain dry run printed a %q line, so an evidence pass was set up:\n%s", banned, s)
				}
			}
			var summary string
			for _, line := range strings.Split(s, "\n") {
				if strings.Contains(line, "evidence-paired ") && strings.Contains(line, " · name-paired ") {
					summary = line
				}
			}
			if !strings.HasSuffix(summary, dryRunDefaultSuffix) {
				t.Errorf("candidacy line = %q; want it to end with %q", summary, dryRunDefaultSuffix)
			}
		})
	}
}

// Python's selection pass runs under --dry-run --evidence exactly as a real
// run runs it, and widens candidacy by what it measured. Executed for real:
// mypkg/core.py is paired to tests/test_smoke.py by execution alone.
func TestDryRunEvidenceRunsPythonsSelectionPass(t *testing.T) {
	skipWithoutPythonCoverage(t)
	refuseModelEndpoints(t)
	root := t.TempDir()
	preflightUnpairedPythonFixture(t, root)

	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{
		"--repo", root, "--dry-run", "--evidence", "--substrate", substrateWorkspace, "--no-selection-cache",
	}, &out, &errb)
	s := out.String()
	if code != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, s, errb.String())
	}
	for _, want := range []string{
		"selection: running the suite once with per-test coverage instrumentation…",
		"1 candidate(s)",
		"mypkg/core.py paired by evidence",
		"would audit 1 file(s)",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q:\n%s", want, s)
		}
	}
}

// interruptSelf sends this process the operator's Ctrl-C and waits for ctx
// to be cancelled by it; it reports whether it was.
func interruptSelf(t *testing.T, ctx context.Context) bool {
	t.Helper()
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGINT); err != nil {
		t.Errorf("cannot signal self: %v", err)
		return false
	}
	select {
	case <-ctx.Done():
		return true
	case <-time.After(10 * time.Second):
		return false
	}
}

// Ctrl-C during the unit pass reaches it: the pass's context is the run's
// signal-cancelled one, its unstarted units say "not run (interrupted)", and
// the run stops there, recording nothing — a real run and a dry run alike.
func TestAnInterruptStopsTheUnitPass(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"dry run", []string{"--dry-run", "--evidence"}},
		{"real run", []string{"--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(agentbackend.ResetInterrupted)
			t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
			root := unitEvidenceTSFixture(t)
			cancelled := false
			prev := collectUnitEvidence
			collectUnitEvidence = func(ctx context.Context, _ coverageRunner, _ map[string]string, _ lang.Plugin, _ lang.Invocation, _ string, _ int, _ func(int, int)) reposcan.UnitEvidence {
				cancelled = interruptSelf(t, ctx)
				return reposcan.UnitEvidence{Ran: true, Units: []reposcan.UnitResult{
					{Unit: "tests/composition/service.test.ts", Err: "not run (interrupted)"},
				}}
			}
			t.Cleanup(func() { collectUnitEvidence = prev })

			var out, errb bytes.Buffer
			args := append([]string{"--repo", root, "--goals", writeGoals(t, root, `{}`), "--substrate", substrateWorkspace, "--no-ledger"}, tc.args...)
			code := runCertifyRepo(append(args, "--", "vitest", "run"), &out, &errb)
			if !cancelled {
				t.Fatal("the unit pass's context was not cancelled by the operator's interrupt")
			}
			if code != exitInterrupted {
				t.Errorf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s", code, exitInterrupted, out.String(), errb.String())
			}
			if !strings.Contains(out.String(), "tests/composition/service.test.ts: not run (interrupted)") {
				t.Errorf("the interrupted unit must be reported:\n%s", out.String())
			}
			if !strings.Contains(errb.String(), "interrupted — nothing recorded") {
				t.Errorf("stderr must say nothing was recorded:\n%s", errb.String())
			}
		})
	}
}

// The same for Python's selection pass.
func TestAnInterruptStopsTheSelectionPass(t *testing.T) {
	t.Cleanup(agentbackend.ResetInterrupted)
	noKeys(t)
	root := t.TempDir()
	preflightUnpairedPythonFixture(t, root)
	cancelled := false
	sel := &fakeSelectionPass{run: func(ctx context.Context) reposcan.SelectionEvidence {
		cancelled = interruptSelf(t, ctx)
		return reposcan.SelectionEvidence{Note: "fake: interrupted"}
	}}
	sel.install(t)

	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{
		"--repo", root, "--dry-run", "--evidence", "--substrate", substrateWorkspace, "--no-selection-cache",
		"--", "python3", "-m", "pytest", "-q",
	}, &out, &errb)
	if !cancelled {
		t.Fatalf("the selection pass's context was not cancelled by the operator's interrupt\nstdout:\n%s\nstderr:\n%s", out.String(), errb.String())
	}
	if code != exitInterrupted {
		t.Errorf("exit %d, want %d\nstderr:\n%s", code, exitInterrupted, errb.String())
	}
}

// The unit pass announces itself before it starts — it can run up to
// selectionTimeout — naming how many units will run and the bound.
func TestUnitPassAnnouncesItselfBeforeItRuns(t *testing.T) {
	refuseModelEndpoints(t)
	root := unitEvidenceTSFixture(t)
	var out, errb bytes.Buffer
	prev := collectUnitEvidence
	var atStart string
	collectUnitEvidence = func(_ context.Context, _ coverageRunner, _ map[string]string, _ lang.Plugin, _ lang.Invocation, _ string, _ int, announce func(int, int)) reposcan.UnitEvidence {
		announce(2, 3)
		atStart = out.String()
		return reposcan.UnitEvidence{Ran: true, Units: []reposcan.UnitResult{
			unitRun("tests/composition/service.test.ts", map[string]lang.LineCounts{"src/app/Service.ts": {Executed: 1, Executable: 1}}),
		}}
	}
	t.Cleanup(func() { collectUnitEvidence = prev })

	code := runCertifyRepo([]string{
		"--repo", root, "--dry-run", "--evidence", "--substrate", substrateWorkspace, "--no-selection-cache",
		"--", "vitest", "run",
	}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d\nstderr:\n%s", code, errb.String())
	}
	want := "  units: running 2 of 3 listed test unit(s) alone under coverage, 1 at a time, within " + selectionTimeout.String() + " for the whole pass…\n"
	if !strings.HasSuffix(atStart, want) {
		t.Errorf("before the first unit ran, stdout ended:\n%q\nwant it to end with:\n%q", lastLine(atStart), want)
	}
}

func lastLine(s string) string {
	s = strings.TrimSuffix(s, "\n")
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		return s[i+1:]
	}
	return s
}

// A settled pass from an --evidence dry run is cached as a real run's would
// be, so the run it previews (here a second dry run) reuses it on the same
// tree rather than paying for the pass twice.
func TestDryRunEvidenceCachesASettledPass(t *testing.T) {
	refuseModelEndpoints(t)
	root := unitEvidenceTSFixture(t)
	gitRun := gitCmd(t, root)
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")
	fake := &fakeUnitPass{ev: reposcan.UnitEvidence{Ran: true, Units: []reposcan.UnitResult{
		unitRun("tests/composition/service.test.ts", map[string]lang.LineCounts{"src/app/Service.ts": {Executed: 1, Executable: 1}}),
	}}}
	fake.install(t)
	cacheDB := filepath.Join(t.TempDir(), "cache.duckdb")

	run := func() string {
		var out, errb bytes.Buffer
		code := runCertifyRepo([]string{
			"--repo", root, "--dry-run", "--evidence", "--substrate", substrateWorkspace, "--cache-db", cacheDB,
			"--", "vitest", "run",
		}, &out, &errb)
		if code != 0 {
			t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out.String(), errb.String())
		}
		return out.String()
	}
	run()
	second := run()
	if n := len(fake.called()); n != 1 {
		t.Errorf("unit pass ran %d time(s) over an unchanged tree, want once", n)
	}
	if !strings.Contains(second, "units: 1 run, 0 failed (reused — tree unchanged since an earlier scan (cached evidence))") {
		t.Errorf("the second dry run must reuse the first's pass:\n%s", second)
	}
	if !strings.Contains(second, "src/app/Service.ts paired by evidence") {
		t.Errorf("the reused pass must pair as the fresh one did:\n%s", second)
	}
}

// The scan line carries a COUNT of each unit's uncounted files and the
// --json inventory carries the list (ruling R22): REA's 25 units each put
// ~390 paths on the scan line, 1.4 MB of output. A failed unit's reason
// keeps its output tail, indented under its unit so it reads as part of it.
func TestUnitPassPrintsUncountedAsACountAndListsThemInJSON(t *testing.T) {
	refuseModelEndpoints(t)
	root := unitEvidenceTSFixture(t)
	mustWrite(t, filepath.Join(root, "tests", "broken.test.ts"), "test('broken', () => {});\n")
	fake := &fakeUnitPass{ev: reposcan.UnitEvidence{Ran: true, Units: []reposcan.UnitResult{
		{Unit: "tests/composition/service.test.ts", Counts: map[string]lang.LineCounts{"src/app/Service.ts": {Executed: 1, Executable: 1}},
			Uncounted: []string{"src/gen/a.ts: transformed code", "src/gen/b.ts: transformed code"}},
		{Unit: "tests/broken.test.ts", Err: "run failed: exit 1: AssertionError: no\nat broken.test.ts:1", Uncounted: []string{"src/gen/c.ts: transformed code"}},
	}}}
	fake.install(t)
	args := []string{"--repo", root, "--dry-run", "--evidence", "--substrate", substrateWorkspace, "--no-selection-cache"}

	var out, errb bytes.Buffer
	if code := runCertifyRepo(append(slices.Clone(args), "--", "vitest", "run"), &out, &errb); code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out.String(), errb.String())
	}
	s := out.String()
	for _, want := range []string{
		"    tests/composition/service.test.ts: 2 file(s) measured and not counted (listed in --dry-run --json)\n",
		"    tests/broken.test.ts: run failed: exit 1: AssertionError: no\n      at broken.test.ts:1\n",
		"    tests/broken.test.ts: 1 file(s) measured and not counted (listed in --dry-run --json)\n",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "src/gen/a.ts") {
		t.Errorf("an uncounted list reached the scan line:\n%s", s)
	}

	out.Reset()
	errb.Reset()
	if code := runCertifyRepo(append(slices.Clone(args), "--json", "--", "vitest", "run"), &out, &errb); code != 0 {
		t.Fatalf("--json exit %d\n%s\n%s", code, out.String(), errb.String())
	}
	var inv struct {
		Units []struct {
			Unit      string   `json:"unit"`
			Err       string   `json:"error"`
			Uncounted []string `json:"uncounted"`
		} `json:"units"`
	}
	if err := json.Unmarshal(out.Bytes(), &inv); err != nil {
		t.Fatalf("inventory is not JSON: %v\n%s", err, out.String())
	}
	if len(inv.Units) != 2 || inv.Units[1].Unit != "tests/broken.test.ts" || inv.Units[1].Err == "" || inv.Units[0].Err != "" ||
		!slices.Equal(inv.Units[0].Uncounted, []string{"src/gen/a.ts: transformed code", "src/gen/b.ts: transformed code"}) {
		t.Errorf("units in the inventory = %+v", inv.Units)
	}
}

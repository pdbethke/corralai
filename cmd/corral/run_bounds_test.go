// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"flag"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/reposcan"
)

// The two operator-settable bounds a large repository meets first: the jail
// seed limit (--seed-limit) and the evidence pass bound (--evidence-timeout).
// Without either flag the bound is today's, exactly; with one, the bound is
// the operator's; and when a bound stops a run, what is printed names the
// flag that moves it.

// bigText is n KiB of valid UTF-8, for a fixture that has to cross a MiB
// boundary without a 64 MiB tree.
func bigText(kib int) string { return strings.Repeat("x", kib<<10) }

// seedOverOneMiB adds 1.2 MiB of text to root in two files, each under the
// 1 MiB per-file cut so both are seeded.
func seedOverOneMiB(t *testing.T, root string) {
	t.Helper()
	mustWrite(t, filepath.Join(root, "data", "a.txt"), bigText(600))
	mustWrite(t, filepath.Join(root, "data", "b.txt"), bigText(600))
}

func TestLoadRepoFilesSeedLimitIsTheCallersBound(t *testing.T) {
	root := t.TempDir()
	seedOverOneMiB(t, root)

	_, _, err := loadRepoFiles(root, loadOpts{BackendName: "bwrap", SeedLimit: 1 << 20})
	if err == nil {
		t.Fatal("1.2 MiB of text was seeded under a 1 MiB limit")
	}
	for _, want := range []string{"1.2 MiB", "1 MiB", "--seed-limit"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must name %q (the measured total, the limit and the flag): %v", want, err)
		}
	}

	files, _, err := loadRepoFiles(root, loadOpts{BackendName: "bwrap", SeedLimit: 2 << 20})
	if err != nil {
		t.Fatalf("a 2 MiB limit refused 1.2 MiB of text: %v", err)
	}
	if len(files) != 2 {
		t.Errorf("seeded %d files, want 2", len(files))
	}
}

// No limit is not "unlimited" and not a number loadRepoFiles picks: a caller
// that reads files names the bound.
func TestLoadRepoFilesRefusesToSeedWithNoLimit(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "a.txt"), "a\n")
	if _, _, err := loadRepoFiles(root, loadOpts{BackendName: "bwrap"}); err == nil {
		t.Fatal("loadRepoFiles seeded a tree with no limit named")
	}
	// Collecting binds reads no file, so it needs no limit.
	if _, _, err := loadRepoFiles(root, loadOpts{BackendName: "bwrap", NoFiles: true}); err != nil {
		t.Fatalf("a bind-only walk refused for want of a seed limit: %v", err)
	}
}

func TestResolveSeedLimit(t *testing.T) {
	parse := func(args ...string) (int64, error) {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		v := fs.Int("seed-limit", 0, "")
		if err := fs.Parse(args); err != nil {
			t.Fatal(err)
		}
		return resolveSeedLimit(fs, *v)
	}
	if got, err := parse(); err != nil || got != 64<<20 {
		t.Errorf("no flag: %d, %v; want today's 64 MiB", got, err)
	}
	if got, err := parse("--seed-limit", "200"); err != nil || got != 200<<20 {
		t.Errorf("--seed-limit 200: %d, %v; want 200 MiB", got, err)
	}
	for _, bad := range []string{"0", "-1"} {
		if _, err := parse("--seed-limit", bad); err == nil || !strings.Contains(err.Error(), "--seed-limit") {
			t.Errorf("--seed-limit %s: err = %v, want a refusal naming the flag", bad, err)
		}
	}
}

func TestResolveEvidenceTimeout(t *testing.T) {
	parse := func(args ...string) (time.Duration, error) {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		v := fs.Duration("evidence-timeout", 0, "")
		if err := fs.Parse(args); err != nil {
			t.Fatal(err)
		}
		return resolveEvidenceTimeout(fs, *v)
	}
	if got, err := parse(); err != nil || got != 15*time.Minute {
		t.Errorf("no flag: %s, %v; want today's 15m", got, err)
	}
	if got, err := parse("--evidence-timeout", "2h"); err != nil || got != 2*time.Hour {
		t.Errorf("--evidence-timeout 2h: %s, %v", got, err)
	}
	for _, bad := range []string{"0s", "-1m"} {
		if _, err := parse("--evidence-timeout", bad); err == nil || !strings.Contains(err.Error(), "--evidence-timeout") {
			t.Errorf("--evidence-timeout %s: err = %v, want a refusal naming the flag", bad, err)
		}
	}
}

// requireSandbox skips a test whose subject is the jail seed when this host
// has no sandbox to build one for.
func requireSandbox(t *testing.T) {
	t.Helper()
	if _, err := resolveLocalJail(""); err != nil {
		t.Skipf("no sandbox backend on this host: %v", err)
	}
}

// The flag the operator types reaches the seed of the jail `corral tests map`
// builds: under it the refusal names the flag, over it the pass runs.
func TestTestsMapSeedLimitFlagBoundsTheJailSeed(t *testing.T) {
	requireSandbox(t)
	refuseModelEndpoints(t)
	root, fake := testsMapFixture(t)
	seedOverOneMiB(t, root)
	run := func(limit string) (int, string) {
		var out, errb bytes.Buffer
		code := runTests([]string{"map", "--repo-dir", root, "--substrate", substrateJail, "--workers", "1", "--seed-limit", limit, "--no-selection-cache", "--", "vitest", "run"}, &out, &errb)
		return code, out.String() + "\n[stderr]\n" + errb.String()
	}
	code, s := run("1")
	if code == 0 || !strings.Contains(s, "--seed-limit") || !strings.Contains(s, "1.2 MiB") {
		t.Fatalf("--seed-limit 1 over 1.2 MiB: exit %d, want a refusal naming the flag and the total:\n%s", code, s)
	}
	if n := len(fake.called()); n != 0 {
		t.Fatalf("the unit pass ran %d time(s) on a seed over its limit", n)
	}
	if code, s := run("2"); code != 0 {
		t.Fatalf("--seed-limit 2 over 1.2 MiB: exit %d:\n%s", code, s)
	}
	if n := len(fake.called()); n != 1 {
		t.Errorf("the unit pass ran %d time(s), want 1", n)
	}
}

// The workspace substrate seeds nothing, so a seed limit there would be a
// setting that silently does nothing.
func TestSeedLimitIsRefusedWhereThereIsNoSeed(t *testing.T) {
	refuseModelEndpoints(t)
	root, _ := testsMapFixture(t)
	var out, errb bytes.Buffer
	code := runTests([]string{"map", "--repo-dir", root, "--substrate", substrateWorkspace, "--seed-limit", "100", "--no-selection-cache", "--", "vitest", "run"}, &out, &errb)
	if code != 2 || !strings.Contains(errb.String(), "--seed-limit") {
		t.Errorf("tests map --substrate workspace --seed-limit: exit %d, want 2 naming the flag:\n%s", code, errb.String())
	}

	out.Reset()
	errb.Reset()
	code = runCertifyRepo([]string{"--repo", root, "--dry-run", "--evidence", "--substrate", substrateWorkspace, "--seed-limit", "100", "--", "vitest", "run"}, &out, &errb)
	if code != 2 || !strings.Contains(errb.String(), "--seed-limit") {
		t.Errorf("certify --repo --substrate workspace --seed-limit: exit %d, want 2 naming the flag:\n%s", code, errb.String())
	}

	out.Reset()
	errb.Reset()
	code = runCertifyLocal([]string{"--code", filepath.Join(root, "src", "app", "Service.ts"), "--goal", "serves", "--seed-limit", "100"}, &out, &errb)
	if code != 2 || !strings.Contains(errb.String(), "--seed-limit") {
		t.Errorf("certify --local --seed-limit without --repo-dir: exit %d, want 2 naming the flag:\n%s", code, errb.String())
	}
}

// certify --repo's flag reaches the seed too: a dry run with --evidence on
// the jail builds it, and says why it could not.
func TestCertifyRepoSeedLimitFlagBoundsTheJailSeed(t *testing.T) {
	requireSandbox(t)
	refuseModelEndpoints(t)
	root, fake := testsMapFixture(t)
	seedOverOneMiB(t, root)
	var out, errb bytes.Buffer
	runCertifyRepo([]string{"--repo", root, "--dry-run", "--evidence", "--substrate", substrateJail, "--swarm", "1", "--seed-limit", "1", "--no-selection-cache", "--", "vitest", "run"}, &out, &errb)
	s := out.String() + "\n[stderr]\n" + errb.String()
	if !strings.Contains(s, "--seed-limit") || !strings.Contains(s, "1.2 MiB") {
		t.Errorf("the scan must say the seed was over --seed-limit, with its total:\n%s", s)
	}
	if n := len(fake.called()); n != 0 {
		t.Errorf("the unit pass ran %d time(s) on a seed over its limit", n)
	}
}

// buildRepoSeed is where certify --local's jail is seeded, and it is handed
// the operator's limit rather than reading one of its own.
func TestBuildRepoSeedHonoursTheLimitItIsGiven(t *testing.T) {
	root := t.TempDir()
	seedOverOneMiB(t, root)
	if _, err := buildRepoSeed("corral certify --local", root, "python", namedIsolator("bwrap"), nil, false, 1<<20, io.Discard); err == nil || !strings.Contains(err.Error(), "--seed-limit") {
		t.Errorf("1.2 MiB under a 1 MiB limit: err = %v, want a refusal naming --seed-limit", err)
	}
	seed, err := buildRepoSeed("corral certify --local", root, "python", namedIsolator("bwrap"), nil, false, 2<<20, io.Discard)
	if err != nil {
		t.Fatalf("1.2 MiB under a 2 MiB limit: %v", err)
	}
	seed.cleanup()
}

// The unit pass runs under the operator's bound when one is given, and under
// today's 15 minutes when none is.
func TestEvidenceTimeoutFlagBoundsTheUnitPass(t *testing.T) {
	refuseModelEndpoints(t)
	for _, tc := range []struct {
		name  string
		extra []string
		want  time.Duration
	}{
		{"no flag", nil, 15 * time.Minute},
		{"--evidence-timeout 2h", []string{"--evidence-timeout", "2h"}, 2 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, fake := testsMapFixture(t)
			args := append([]string{"map", "--repo-dir", root, "--substrate", substrateWorkspace, "--no-selection-cache"}, tc.extra...)
			var out, errb bytes.Buffer
			start := time.Now()
			if code := runTests(append(args, "--", "vitest", "run"), &out, &errb); code != 0 {
				t.Fatalf("exit %d\n%s", code, errb.String())
			}
			calls := fake.called()
			if len(calls) != 1 || calls[0].deadline.IsZero() {
				t.Fatalf("calls = %+v, want one pass under a deadline", calls)
			}
			if got := calls[0].deadline.Sub(start); got > tc.want+time.Minute || got < tc.want-time.Minute {
				t.Errorf("the pass's deadline is %s after the start, want about %s", got, tc.want)
			}
			if !strings.Contains(out.String(), "within "+tc.want.String()) {
				t.Errorf("the announce line must state the bound %s:\n%s", tc.want, out.String())
			}
		})
	}
	// certify --repo takes the same flag to the same pass.
	root, fake := testsMapFixture(t)
	start := time.Now()
	runUnitEvidenceScan(t, root, "--evidence-timeout", "2h", "--", "vitest", "run")
	calls := fake.called()
	if len(calls) != 1 || calls[0].deadline.Sub(start) < 2*time.Hour-time.Minute {
		t.Errorf("certify --repo --evidence-timeout 2h: calls = %+v, want one pass with a 2h deadline", calls)
	}
}

// Units the bound cut are counted, and the flag that moves the bound is named,
// on the scan line and in the tests map summary.
func TestEvidenceBoundCutIsDisclosedWithTheFlag(t *testing.T) {
	refuseModelEndpoints(t)
	const want = "1 unit(s) cut short by the 15m0s evidence bound"
	root, _ := testsMapFixture(t) // one unit "not run (time limit)"
	var out, errb bytes.Buffer
	if code := runTests([]string{"map", "--repo-dir", root, "--substrate", substrateWorkspace, "--no-selection-cache", "--", "vitest", "run"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d\n%s", code, errb.String())
	}
	if s := out.String(); !strings.Contains(s, want) || !strings.Contains(s, "--evidence-timeout") {
		t.Errorf("tests map must count the cut units and name --evidence-timeout:\n%s", s)
	}

	root, _ = testsMapFixture(t)
	s := runUnitEvidenceScan(t, root, "--", "vitest", "run")
	if !strings.Contains(s, want) || !strings.Contains(s, "--evidence-timeout") {
		t.Errorf("the scan line must count the cut units and name --evidence-timeout:\n%s", s)
	}

	// A pass the bound never reached says nothing about it.
	root = unitEvidenceTSFixture(t)
	(&fakeUnitPass{ev: reposcan.UnitEvidence{Ran: true, Units: []reposcan.UnitResult{
		unitRun("tests/composition/service.test.ts", map[string]lang.LineCounts{"src/app/Service.ts": {Executed: 1, Executable: 1}}),
	}}}).install(t)
	out.Reset()
	if code := runTests([]string{"map", "--repo-dir", root, "--substrate", substrateWorkspace, "--no-selection-cache", "--", "vitest", "run"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d\n%s", code, errb.String())
	}
	if strings.Contains(out.String(), "--evidence-timeout") {
		t.Errorf("no unit was cut, so no bound line:\n%s", out.String())
	}
}

// Python's evidence is one instrumented run, bounded by the same flag: the
// real workspace runner is handed the bound, and a run it stops says so.
func TestEvidenceTimeoutBoundsPythonsInstrumentedRun(t *testing.T) {
	refuseModelEndpoints(t)
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "pkg", "mod.py"), "def f():\n    return 1\n")
	mustWrite(t, filepath.Join(root, "tests", "test_mod.py"), "def test_f():\n    pass\n")
	prev := collectSelectionEvidence
	t.Cleanup(func() { collectSelectionEvidence = prev })
	collectSelectionEvidence = func(ctx context.Context, runner coverageRunner, files map[string]string, p lang.Plugin, _ []string, _ []string) reposcan.SelectionEvidence {
		// The project's run, as far as the bound is concerned: it takes a
		// second. The note is the one reposcan writes for a failed run.
		if _, err := runner.Enumerate(ctx, files, []string{"sleep", "1"}); err != nil {
			return reposcan.SelectionEvidence{Note: p.Name() + ": selection evidence run failed: " + err.Error()}
		}
		return reposcan.SelectionEvidence{Note: "fake: the run finished inside its bound"}
	}
	run := func(bound string) string {
		var out, errb bytes.Buffer
		runTests([]string{"map", "--repo-dir", root, "--substrate", substrateWorkspace, "--evidence-timeout", bound, "--no-selection-cache", "--", "python3", "-m", "pytest", "tests"}, &out, &errb)
		return out.String() + "\n[stderr]\n" + errb.String()
	}
	if s := run("100ms"); !strings.Contains(s, "100ms evidence bound") || !strings.Contains(s, "--evidence-timeout") {
		t.Errorf("a run the bound stopped must name the bound and --evidence-timeout:\n%s", s)
	}
	if s := run("1m"); !strings.Contains(s, "finished inside its bound") || strings.Contains(s, "evidence bound") {
		t.Errorf("a run inside a 1m bound must finish, with no bound line:\n%s", s)
	}
}

// certify --local's flag reaches the seed its --repo-dir jail is built from,
// and the refusal comes before any model is called.
func TestCertifyLocalSeedLimitFlagBoundsTheJailSeed(t *testing.T) {
	requireSandbox(t)
	refuseModelEndpoints(t)
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "go.mod"), "module example.com/m\n\ngo 1.22\n")
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n\nfunc A() int { return 1 }\n")
	mustWrite(t, filepath.Join(root, "pkg", "a_test.go"), "package pkg\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) { _ = A() }\n")
	seedOverOneMiB(t, root)
	var out, errb bytes.Buffer
	runCertifyLocal([]string{
		"--repo-dir", root, "--code", filepath.Join(root, "pkg", "a.go"), "--test", filepath.Join(root, "pkg", "a_test.go"),
		"--goal", "A returns one", "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off",
		"--no-ledger", "--seed-limit", "1", "--", "go", "test", "./pkg/",
	}, &out, &errb)
	if s := out.String() + errb.String(); !strings.Contains(s, "--seed-limit") || !strings.Contains(s, "1.2 MiB") {
		t.Errorf("certify --local --seed-limit 1 over 1.2 MiB must refuse naming the flag and the total:\n%s", s)
	}
}

// A dry run without --evidence runs no pass and seeds no jail, which is
// known from the arguments alone: both flags are refused there.
func TestBoundsAreRefusedOnADryRunWithoutEvidence(t *testing.T) {
	refuseModelEndpoints(t)
	root, fake := testsMapFixture(t)
	for _, flagArgs := range [][]string{{"--seed-limit", "100"}, {"--evidence-timeout", "1h"}} {
		var out, errb bytes.Buffer
		args := append([]string{"--repo", root, "--dry-run"}, flagArgs...)
		code := runCertifyRepo(append(args, "--", "vitest", "run"), &out, &errb)
		if code != 2 || !strings.Contains(errb.String(), flagArgs[0]) || !strings.Contains(errb.String(), "--evidence") {
			t.Errorf("certify --repo --dry-run %v: exit %d, want 2 naming the flag and --evidence:\n%s", flagArgs, code, errb.String())
		}
	}
	if n := len(fake.called()); n != 0 {
		t.Errorf("the unit pass ran %d time(s) on a refused run", n)
	}
}

// --whole-suite still runs the unit pass (candidacy needs it), so
// --evidence-timeout is honoured there, not refused; Python's instrumented
// run is skipped under it, and the selection line says the flag went unused.
func TestEvidenceTimeoutUnderWholeSuite(t *testing.T) {
	refuseModelEndpoints(t)
	t.Run("a unit language reads it", func(t *testing.T) {
		root, fake := testsMapFixture(t)
		var out, errb bytes.Buffer
		start := time.Now()
		code := runCertifyRepo([]string{"--repo", root, "--dry-run", "--evidence", "--whole-suite", "--substrate", substrateWorkspace,
			"--no-selection-cache", "--evidence-timeout", "2h", "--", "vitest", "run"}, &out, &errb)
		if code != 0 {
			t.Fatalf("exit %d\n%s", code, errb.String())
		}
		calls := fake.called()
		if len(calls) != 1 || calls[0].deadline.Sub(start) < 2*time.Hour-time.Minute {
			t.Errorf("under --whole-suite the unit pass must run under the flag's bound: calls = %+v", calls)
		}
	})
	t.Run("Python says it went unused", func(t *testing.T) {
		root := t.TempDir()
		mustWrite(t, filepath.Join(root, "pkg", "mod.py"), "def f():\n    return 1\n")
		mustWrite(t, filepath.Join(root, "tests", "test_mod.py"), "def test_f():\n    pass\n")
		run := func(extra ...string) string {
			args := append([]string{"--repo", root, "--dry-run", "--evidence", "--whole-suite", "--substrate", substrateWorkspace, "--no-selection-cache"}, extra...)
			var out, errb bytes.Buffer
			runCertifyRepo(append(args, "--", "python3", "-m", "pytest", "tests"), &out, &errb)
			return out.String() + "\n[stderr]\n" + errb.String()
		}
		const want = "--evidence-timeout not used (--whole-suite runs no instrumented run)"
		if s := run("--evidence-timeout", "1h"); !strings.Contains(s, want) {
			t.Errorf("missing %q:\n%s", want, s)
		}
		if s := run(); strings.Contains(s, "--evidence-timeout") {
			t.Errorf("a scan that named no bound must print what it printed before the flag:\n%s", s)
		}
	})
}

// A selection-cache hit runs nothing, so a named bound is never read; that
// cannot be refused in advance, so the line reporting the evidence says it.
func TestEvidenceTimeoutUnusedOnACacheHitIsSaid(t *testing.T) {
	refuseModelEndpoints(t)
	const want = "--evidence-timeout not used (cached evidence, no run)"

	// corral tests map, through the real selection cache.
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
	run := func(extra ...string) string {
		args := append([]string{"map", "--repo-dir", root, "--substrate", substrateWorkspace, "--cache-db", cacheDB}, extra...)
		var out, errb bytes.Buffer
		if code := runTests(append(args, "--", "vitest", "run"), &out, &errb); code != 0 {
			t.Fatalf("exit %d\n%s", code, errb.String())
		}
		return out.String()
	}
	if s := run("--evidence-timeout", "1h"); strings.Contains(s, want) {
		t.Errorf("the first run ran the pass, so it read the bound:\n%s", s)
	}
	if s := run("--evidence-timeout", "1h"); !strings.Contains(s, want) {
		t.Errorf("a cache hit must say the bound went unused:\n%s", s)
	}
	if n := len(fake.called()); n != 1 {
		t.Fatalf("the pass ran %d time(s), want 1 (the second run is a cache hit)", n)
	}
	if s := run(); strings.Contains(s, "--evidence-timeout") {
		t.Errorf("with no flag, a cache hit prints what it printed before the flag:\n%s", s)
	}

	// The scan line says the same, on the units line certify --repo prints.
	ex := &localExecutor{units: reposcan.UnitEvidence{Ran: true}, unitsReused: true, evidenceTimeoutGiven: true}
	var b bytes.Buffer
	printUnitPass(&b, ex)
	if !strings.Contains(b.String(), want) {
		t.Errorf("the scan's units line on a cache hit must say the bound went unused:\n%s", b.String())
	}
}

// A cache hit that prints the pass's Changed sentence says the evidence
// was reused, whether or not --evidence-timeout was given: otherwise an
// earlier run's "evidence: …" line reads as this run's. The reuse line
// comes before it.
func TestTestsMapSaysReusedWheneverChangedIsFromCache(t *testing.T) {
	refuseModelEndpoints(t)
	const changed = "each unit ran with --pool=threads, which the operator's command did not name"
	const reused = "evidence: reused from the selection cache"
	root := unitEvidenceTSFixture(t)
	gitRun := gitCmd(t, root)
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")
	fake := &fakeUnitPass{ev: reposcan.UnitEvidence{Ran: true, Changed: changed, Units: []reposcan.UnitResult{
		unitRun("tests/composition/service.test.ts", map[string]lang.LineCounts{"src/app/Service.ts": {Executed: 1, Executable: 1}}),
	}}}
	fake.install(t)
	cacheDB := filepath.Join(t.TempDir(), "cache.duckdb")
	run := func() string {
		var out, errb bytes.Buffer
		if code := runTests([]string{"map", "--repo-dir", root, "--substrate", substrateWorkspace, "--cache-db", cacheDB, "--", "vitest", "run"}, &out, &errb); code != 0 {
			t.Fatalf("exit %d\n%s", code, errb.String())
		}
		return out.String()
	}
	if s := run(); strings.Contains(s, reused) || !strings.Contains(s, "evidence: "+changed) {
		t.Errorf("the first run ran the pass: want its Changed line and no reuse line:\n%s", s)
	}
	s := run()
	if n := len(fake.called()); n != 1 {
		t.Fatalf("the pass ran %d time(s), want 1 (the second run is a cache hit)", n)
	}
	r, c := strings.Index(s, reused), strings.Index(s, "evidence: "+changed)
	if c < 0 {
		t.Fatalf("a cache hit must still print the cached pass's Changed:\n%s", s)
	}
	if r < 0 || r > c {
		t.Errorf("a cache hit printing Changed must say, before it, that the evidence was reused:\n%s", s)
	}
	if strings.Contains(s, "--evidence-timeout") {
		t.Errorf("with no flag, the reuse line carries no --evidence-timeout clause:\n%s", s)
	}
}

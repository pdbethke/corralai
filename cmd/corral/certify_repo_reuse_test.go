// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/reposcan"
	"github.com/pdbethke/corralai/internal/scanstore"
)

// This file proves the #110 finding inverted: T1 (the goal cache) and T2
// (the selection-evidence cache) were built separately, but the whole point
// is that they COMPOSE — with goals stable across an unchanged tree,
// GoalDigest (a component of every job's verdict-cache CacheKey, see
// EmitJobs) stabilises too, and a second scan of an unchanged tree becomes
// nearly free: goal reused, evidence reused, and now — the payoff neither
// earlier task could prove alone — the VERDICT ITSELF is served from the
// REAL ledgerCache, through the REAL reposcan.Scan cache path, with zero
// model calls and zero suite runs.
//
// No seam in runCertifyRepo lets a test hand it a fake audit result — the
// CLI always builds a real *localExecutor whose default .audit/.newBaseline
// speak to a real model and a real toolchain, and this project never
// drives `corral certify` against a model in a test. So reuseFixtureScan
// below calls the SAME package-level functions runCertifyRepo calls, in the
// same order (resolveGoalSource, reposcan.EmitJobs, localExecutor +
// reposcan.Scan, buildScanFileRows, recordCertifyRepoScan), skipping only
// the CLI's flag parsing and provider/jail preflights a fake audit has no
// use for — exactly the pattern TestNewLocalExecutorSkipsTheSeedCacheForWorkspaceSubstrate
// and TestLocalExecutorThreadsSubstrateIntoAuditInput already use to stub
// localExecutor.audit/.newBaseline directly.

// reuseFakeDeriver is a reposcan.Deriver that returns FIXED text per
// candidate path — required so the verdict-cache assertion below is not
// vacuous: a deriver that varied its answer on every call would make
// GoalDigest move on every scan regardless of any cache, and "the second
// scan reused the verdict" would prove nothing about goal stability. When
// salt is non-nil it is incremented and folded into the text instead — the
// one arm (the RED control) that must NOT be stable, mirroring how a real
// model's answer is not byte-identical across two independent calls.
type reuseFakeDeriver struct {
	calls *int
	salt  *int
}

func (d reuseFakeDeriver) Derive(_ context.Context, c reposcan.Candidate, _ string) (string, bool, error) {
	*d.calls++
	if d.salt != nil {
		*d.salt++
		return fmt.Sprintf("goal for %s (salt %d)", c.Path, *d.salt), true, nil
	}
	return "goal for " + c.Path, true, nil
}

// TestReuseFakeDeriverIsDeterministicPerPath pins the fixture invariant the
// whole file depends on, per the brief: two calls over the SAME candidate,
// with salt nil, must return byte-identical text — the precondition for
// "goal reused ⇒ same GoalDigest" to mean anything at all.
func TestReuseFakeDeriverIsDeterministicPerPath(t *testing.T) {
	calls := 0
	d := reuseFakeDeriver{calls: &calls}
	c := reposcan.Candidate{Path: "pkga/a.py", Lang: "python"}
	g1, _, _ := d.Derive(context.Background(), c, "irrelevant source A")
	g2, _, _ := d.Derive(context.Background(), c, "irrelevant source B")
	if g1 != g2 {
		t.Fatalf("reuseFakeDeriver is not deterministic per path: %q != %q — the fixture the rest of this file relies on is broken", g1, g2)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

// reuseFixtureVerdict is the FIXED, deterministic Verdict every fake audit
// in this file returns — it carries real spend and timing (like
// cacheHitResults' fresh.go) so the cache-hit assertions below have
// something to prove absent, not merely absent-because-never-populated.
func reuseFixtureVerdict() advpool.Verdict {
	return advpool.Verdict{
		DevKillRate: 0.75, MutantsTotal: 4, Survivors: 1, DevScored: true,
		ModelCalls: []advpool.ModelCall{
			{Role: advpool.RoleMutantGenerator, Model: "fake-mutant-model", Calls: 2, InputTokens: 200, OutputTokens: 20, Wall: 2 * time.Second},
			{Role: advpool.RoleTestWriter, Model: "fake-writer-model", Calls: 1, InputTokens: 150, OutputTokens: 40, Wall: 3 * time.Second},
		},
		Timing: advpool.Timing{DevPass: time.Minute, Total: 2 * time.Minute},
	}
}

// reuseFixtureRepo lays down two independent, paired python source/test
// files, git-inits and commits them. Two files (not one) matter for the
// asymmetry proof: scan 3 edits only a.py, and b.py's goal/verdict must
// stay reused while a.py's does not. Each pair lives in its OWN directory
// — a job's CacheKey also carries a PackageDigest (the containing
// directory's own digest, for languages where a sibling's bytes affect
// compilation), and two files sharing one directory would make editing
// a.py bust b.py's key too, which is not the asymmetry this file exists to
// prove.
func reuseFixtureRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	gitRun := gitCmd(t, root)
	mustWrite(t, filepath.Join(root, "pkga", "a.py"), "def f():\n    return 1\n")
	mustWrite(t, filepath.Join(root, "pkga", "test_a.py"), "def test_f():\n    assert True\n")
	mustWrite(t, filepath.Join(root, "pkgb", "b.py"), "def g():\n    return 2\n")
	mustWrite(t, filepath.Join(root, "pkgb", "test_b.py"), "def test_g():\n    assert True\n")
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")
	return root
}

// reuseScanResult is what one call to reuseFixtureScan produced, everything
// a test in this file needs to assert against.
type reuseScanResult struct {
	stdout   string
	results  []reposcan.FileResult
	scanID   int64
	files    []scanstore.File
	modelRow []scanstore.ModelCall
}

// reuseFixtureScan drives the goal-cache → selection-cache → verdict-cache
// pipeline exactly the way runCertifyRepo does, over the fixture repo at
// root, recording into the ledger at dsn. deriver and evidence are the two
// existing package-level fake-role seams (certifyRepoDeriver's factory
// shape and collectSelectionEvidence); audit stands in for the localExecutor
// seam neither T1 nor T2 needed — a real audit never runs.
func reuseFixtureScan(t *testing.T, root, dsn string, deriver reposcan.Deriver, noGoalCache bool, audit func(path string) advpool.Verdict) reuseScanResult {
	return reuseFixtureScanOpt(t, root, dsn, deriver, noGoalCache, false, audit)
}

// reuseFixtureScanOpt is reuseFixtureScan with the verdict cache switchable
// off, the way --no-verdict-cache switches it off in runCertifyRepo: the
// cache is addressed by DSN and an empty DSN misses every key.
func reuseFixtureScanOpt(t *testing.T, root, dsn string, deriver reposcan.Deriver, noGoalCache, noVerdictCache bool, audit func(path string) advpool.Verdict) reuseScanResult {
	t.Helper()
	var out bytes.Buffer
	var errb bytes.Buffer

	cands, excl, err := reposcan.Enumerate(root)
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	enumExcl := len(excl)
	selected := cands

	ex := newLocalExecutor(root, nil, substrateWorkspace, 0, io.Discard)
	defer ex.Close()
	ex.selectionCache = newSelectionLedgerCache(dsn)
	ex.newBaseline = func(context.Context, localAuditInput) (reposcan.BaselineRunner, func(), error) {
		return &scriptedBaseline{results: []bool{true, true}}, func() {}, nil
	}
	ex.audit = func(_ context.Context, in localAuditInput) (advpool.Verdict, error) {
		return audit(in.codePath), nil
	}

	var goalStore reposcan.GoalCacheStore
	if !noGoalCache {
		goalStore = newGoalLedgerCache(dsn)
	}
	gs, disclosure, code := resolveGoalSource(&errb, root, "", "test-model-x", false, len(selected),
		func(string) (reposcan.Deriver, error) { return deriver, nil }, goalStore, noGoalCache, true)
	if code != 0 {
		t.Fatalf("resolveGoalSource: code=%d stderr=%s", code, errb.String())
	}
	cachingGS, cacheWired := gs.(*reposcan.CachingGoalSource)
	if disclosure != "" && !cacheWired {
		fmt.Fprintln(&out, disclosure)
	}

	if len(selected) > 0 {
		selectionSources := enumeratedSourcePaths(cands, excl[:enumExcl])
		if reusedFrom, hit := ex.selectionCachePeek(selectionSources); hit {
			fmt.Fprintf(&out, "  selection: reused — tree unchanged since scan %d\n", reusedFrom)
		} else {
			fmt.Fprintln(&out, "  selection: running the suite once with per-test coverage instrumentation…")
		}
		ex.selection = ex.collectSelection(context.Background(), selectionSources)
		if !ex.selection.Ran {
			fmt.Fprintf(&out, "  selection: grading by the WHOLE suite — %s\n", ex.selection.Note)
		}
	}
	selectionMethod := ""
	if ex.selection.Ran {
		selectionMethod = "coverage-context"
	}
	auditConfig := auditConfigKey(false, selectionMethod, nil, "", "")

	cfg := reposcan.EmitConfig{
		Owner: "test-owner", Repo: "test-repo", Commit: "deadbeef", Root: root,
		EngineVersion: reposcan.VerdictGeneration, ModelSet: "test-model-set", AuditConfig: auditConfig,
		Substrate: substrateWorkspace,
		FileAuditConfig: func(c reposcan.Candidate) string {
			return fileSelectionKey(ex.selectionFor(reposcan.Job{Path: c.Path, TestPath: c.TestPath, Lang: c.Lang}))
		},
	}
	jobs, _, err := reposcan.EmitJobs(cfg, selected, gs)
	if err != nil {
		t.Fatalf("EmitJobs: %v", err)
	}
	if cacheWired {
		fresh, reused := cachingGS.Stats()
		if line := goalCacheDisclosureLine(disclosure, "test-model-x", version, fresh, reused); line != "" {
			fmt.Fprintln(&out, line)
		}
	}

	verdictCacheDSN := dsn
	if noVerdictCache {
		verdictCacheDSN = ""
	}
	results := reposcan.Scan(context.Background(), jobs, ex, newLedgerCache(verdictCacheDSN), 1)
	rep := reposcan.Aggregate("test-owner", "test-repo", "deadbeef", len(cands)+enumExcl, len(cands), results, excl)

	files := buildScanFileRows(results, rep.Excluded, reposcan.CoverageMap{}, "", root, io.Discard)
	modelRows := buildScanModelCallRows(results)

	st, err := scanstore.Open(dsn)
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	defer st.Close()

	scan := scanstore.Scan{
		Owner: "test-owner", Repo: "test-repo", Commit: "deadbeef",
		Substrate: substrateWorkspace, EngineVersion: reposcan.VerdictGeneration, ModelSet: "test-model-set",
		TotalFiles: len(cands) + enumExcl, Candidates: rep.Candidates, Audited: rep.Audited,
		KillRate: killRatePtr(rep.KillRate), CacheHits: rep.CacheHits,
		StartedAt: time.Now(), FinishedAt: time.Now(),
		SelectionMillis:     scanSelectionMillis(ex),
		SelectionReusedFrom: scanSelectionReusedFrom(ex),
	}
	id, rerr := recordCertifyRepoScan(st, scan, files, nil, modelRows, scanEventRows(ex), &errb)
	if rerr != nil {
		t.Fatalf("recordCertifyRepoScan: %v stderr=%s", rerr, errb.String())
	}
	if ex.pendingSelectionPut != nil {
		p := ex.pendingSelectionPut
		if perr := st.SelectionCachePut(context.Background(), p.TreeDigest, p.CmdDigest, p.Plugin, p.Substrate, p.Raw, "", id); perr != nil {
			t.Fatalf("SelectionCachePut: %v", perr)
		}
	}
	if errb.Len() != 0 {
		t.Fatalf("stderr must be empty: %q", errb.String())
	}

	return reuseScanResult{stdout: out.String(), results: results, scanID: id, files: files, modelRow: modelRows}
}

// stableSelectionEvidence returns fixed, content-stable evidence every
// call — its parsed OUTCOME (never mind that it is garbage the python
// selector cannot use) must stay identical across scans so a candidate
// whose own bytes never moved gets an identical FileAuditConfig component
// on every scan; a raw payload that changed shape per call (as the
// standalone selection-cache test uses) would make that assertion
// meaningless here, where the raw bytes also feed EmitJobs' cache key.
func stableSelectionEvidence(calls *int) func(ctx context.Context, runner coverageRunner, files map[string]string, p lang.Plugin, testCmd []string, sourcePaths []string) reposcan.SelectionEvidence {
	return func(context.Context, coverageRunner, map[string]string, lang.Plugin, []string, []string) reposcan.SelectionEvidence {
		*calls++
		return reposcan.SelectionEvidence{Ran: true, Raw: []byte("{}")}
	}
}

func fileResultFor(results []reposcan.FileResult, path string) (reposcan.FileResult, bool) {
	for _, r := range results {
		if r.Job.Path == path {
			return r, true
		}
	}
	return reposcan.FileResult{}, false
}

func fileRowFor(files []scanstore.File, path string) (scanstore.File, bool) {
	for _, f := range files {
		if f.Path == path {
			return f, true
		}
	}
	return scanstore.File{}, false
}

// TestSecondScanIsNearlyFree is the task's headline claim, in three scans
// over one fixture and one ledger DB:
//
//   - scan 1 is a clean miss on every cache: it derives both goals, runs the
//     instrumented selection pass once, and audits both files fresh.
//   - scan 2, over the BYTE-IDENTICAL tree, reuses everything: zero deriver
//     calls, zero selection runs, and — the payoff neither T1 nor T2 alone
//     proved — both files come back CacheHit through the REAL ledgerCache,
//     because GoalDigest (stable, because the goal was reused) left every
//     job's CacheKey unchanged from scan 1's.
//   - scan 2b is the RED control: with the goal cache OFF and a deriver that
//     is deliberately NOT stable, the tree is still byte-identical but the
//     verdict cache still MISSES — proving the scan-2 hit really does
//     depend on goal stability, not on some coincidence of the fixture.
//   - scan 3 edits one byte of a.py only: a.py re-derives, re-audits and
//     loses its cache hit; the selection pass re-runs (the tree changed);
//     but b.py's GOAL is still reused (its own bytes never moved) AND its
//     VERDICT is still a cache hit (its SourceDigest, GoalDigest and
//     TestSurfaceDigest — a.py is a SOURCE file, not part of the test
//     surface — all stayed put). That asymmetry is the design, not a bug.
func TestSecondScanIsNearlyFree(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	root := reuseFixtureRepo(t)
	dsn := filepath.Join(t.TempDir(), "scans.duckdb")

	deriveCalls := 0
	deriver := reuseFakeDeriver{calls: &deriveCalls}

	evidenceCalls := 0
	origEvidence := collectSelectionEvidence
	t.Cleanup(func() { collectSelectionEvidence = origEvidence })
	collectSelectionEvidence = stableSelectionEvidence(&evidenceCalls)

	audited := map[string]int{}
	audit := func(path string) advpool.Verdict {
		audited[path]++
		return reuseFixtureVerdict()
	}

	// --- scan 1: a clean miss on every cache. ---
	r1 := reuseFixtureScan(t, root, dsn, deriver, false, audit)
	if deriveCalls != 2 {
		t.Fatalf("scan 1: deriver called %d time(s), want 2 (one per file)", deriveCalls)
	}
	if evidenceCalls != 1 {
		t.Fatalf("scan 1: selection evidence collected %d time(s), want 1", evidenceCalls)
	}
	if audited["pkga/a.py"] != 1 || audited["pkgb/b.py"] != 1 {
		t.Fatalf("scan 1: audit calls = %+v, want exactly one each for a.py and b.py", audited)
	}
	for _, p := range []string{"pkga/a.py", "pkgb/b.py"} {
		res, ok := fileResultFor(r1.results, p)
		if !ok || !res.Gradable || res.CacheHit {
			t.Fatalf("scan 1: %s = %+v, want gradable and NOT a cache hit", p, res)
		}
	}
	if !strings.Contains(r1.stdout, "selection: running the suite once with per-test coverage instrumentation…") {
		t.Errorf("scan 1: stdout must announce the selection run, got:\n%s", r1.stdout)
	}
	if strings.Contains(r1.stdout, "selection: reused") || strings.Contains(r1.stdout, "goals: ") {
		t.Errorf("scan 1: must not claim any reuse yet, got:\n%s", r1.stdout)
	}
	wantFreshGoalsLine := "  goals derived per file by test-model-x@" + version + " — no goal-critic; each goal is judged after the fact by mutant yield"
	if !strings.Contains(r1.stdout, wantFreshGoalsLine) {
		t.Errorf("scan 1: stdout = %q, want it to contain the unchanged base disclosure %q", r1.stdout, wantFreshGoalsLine)
	}

	// --- scan 2: the SAME tree, the SAME ledger. Everything reuses. ---
	r2 := reuseFixtureScan(t, root, dsn, deriver, false, audit)
	if deriveCalls != 2 {
		t.Errorf("scan 2: deriver called again (total now %d) — an unchanged tree must not re-derive a single goal", deriveCalls)
	}
	if evidenceCalls != 1 {
		t.Errorf("scan 2: selection evidence collected again (total now %d) — an unchanged tree must not re-run the suite", evidenceCalls)
	}
	if audited["pkga/a.py"] != 1 || audited["pkgb/b.py"] != 1 {
		t.Errorf("scan 2: audit calls = %+v, want UNCHANGED from scan 1 — every job must be a verdict-cache hit, not a re-audit", audited)
	}
	for _, p := range []string{"pkga/a.py", "pkgb/b.py"} {
		res, ok := fileResultFor(r2.results, p)
		if !ok || !res.CacheHit {
			t.Fatalf("scan 2: %s = %+v, want CacheHit true — this is the composed claim: goal reused ⇒ stable GoalDigest ⇒ stable CacheKey ⇒ a REAL ledgerCache hit", p, res)
		}
		row, ok := fileRowFor(r2.files, p)
		if !ok {
			t.Fatalf("scan 2: no recorded row for %s", p)
		}
		// #173's cache-hit row rules: no spend, no timing, disclosed via
		// CacheHit rather than a fabricated zero.
		if row.TotalMillis != nil || row.DevPassMillis != nil {
			t.Errorf("scan 2: %s recorded timing on a reused verdict: %+v", p, row)
		}
	}
	if len(r2.modelRow) != 0 {
		t.Errorf("scan 2: scan_model_calls carries %d row(s), want 0 — a reused verdict spent nothing THIS scan: %+v", len(r2.modelRow), r2.modelRow)
	}
	wantSelectionReused := fmt.Sprintf("  selection: reused — tree unchanged since scan %d", r1.scanID)
	if !strings.Contains(r2.stdout, wantSelectionReused) {
		t.Errorf("scan 2: stdout = %q, want it to contain %q", r2.stdout, wantSelectionReused)
	}
	wantGoalsReused := fmt.Sprintf("  goals: 0 derived by test-model-x@%s, 2 reused (identical source)", version)
	if !strings.Contains(r2.stdout, wantGoalsReused) {
		t.Errorf("scan 2: stdout = %q, want it to contain %q", r2.stdout, wantGoalsReused)
	}

	// --- scan 2b: the RED control. Same unchanged tree, but with the goal
	// cache OFF and a deriver deliberately NOT stable (mirroring a real
	// model, whose answer is not byte-identical call to call) — GoalDigest
	// moves even though nothing on disk did, so the verdict cache MUST
	// miss. This is what makes the scan-2 hit above a claim about the
	// interplay, not a coincidence of the fixture: watched failing first
	// (with the caches effectively disabled) before scan 2 above was made
	// to pass.
	saltCalls, salt := 0, 0
	saltedDeriver := reuseFakeDeriver{calls: &saltCalls, salt: &salt}
	controlAudited := map[string]int{}
	controlAudit := func(path string) advpool.Verdict {
		controlAudited[path]++
		return reuseFixtureVerdict()
	}
	r2b := reuseFixtureScan(t, root, dsn, saltedDeriver, true, controlAudit)
	if saltCalls != 2 {
		t.Fatalf("scan 2b: deriver called %d time(s), want 2 — --no-goal-cache must derive every file every scan", saltCalls)
	}
	for _, p := range []string{"pkga/a.py", "pkgb/b.py"} {
		res, ok := fileResultFor(r2b.results, p)
		if !ok || res.CacheHit {
			t.Errorf("scan 2b (control): %s = %+v, want CacheHit FALSE — an unstable goal moved GoalDigest even though the tree did not", p, res)
		}
	}

	// --- scan 2c: --no-verdict-cache. Same unchanged tree, every other cache
	// on, and yet every file is re-audited: the operator's way to redo a
	// measurement the cache would otherwise keep serving.
	offAudited := map[string]int{}
	r2c := reuseFixtureScanOpt(t, root, dsn, deriver, false, true, func(path string) advpool.Verdict {
		offAudited[path]++
		return reuseFixtureVerdict()
	})
	if offAudited["pkga/a.py"] != 1 || offAudited["pkgb/b.py"] != 1 {
		t.Errorf("scan 2c (--no-verdict-cache): audit calls = %+v, want one each — the verdict cache must be OFF", offAudited)
	}
	for _, p := range []string{"pkga/a.py", "pkgb/b.py"} {
		if res, ok := fileResultFor(r2c.results, p); !ok || res.CacheHit {
			t.Errorf("scan 2c (--no-verdict-cache): %s = %+v, want CacheHit FALSE", p, res)
		}
	}

	// --- scan 3: one source byte of a.py changes. b.py is untouched. ---
	if err := os.WriteFile(filepath.Join(root, "pkga/a.py"), []byte("def f():\n    return 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r3 := reuseFixtureScan(t, root, dsn, deriver, false, audit)
	if deriveCalls != 3 {
		t.Errorf("scan 3: deriver called %d time(s) total, want 3 — a.py's changed bytes must re-derive, b.py's unchanged bytes must not", deriveCalls)
	}
	if evidenceCalls != 2 {
		t.Errorf("scan 3: selection evidence collected %d time(s) total, want 2 — a changed tracked byte must bust the selection cache", evidenceCalls)
	}
	if audited["pkga/a.py"] != 2 {
		t.Errorf("scan 3: a.py audited %d time(s) total, want 2 — its goal AND its verdict must be re-earned", audited["pkga/a.py"])
	}
	if audited["pkgb/b.py"] != 1 {
		t.Errorf("scan 3: b.py audited %d time(s) total, want UNCHANGED at 1 — nothing about b.py's own key moved", audited["pkgb/b.py"])
	}
	aRes, ok := fileResultFor(r3.results, "pkga/a.py")
	if !ok || aRes.CacheHit {
		t.Errorf("scan 3: a.py = %+v, want CacheHit false — its SourceDigest (and therefore its CacheKey) moved", aRes)
	}
	bRes, ok := fileResultFor(r3.results, "pkgb/b.py")
	if !ok || !bRes.CacheHit {
		t.Errorf("scan 3: b.py = %+v, want CacheHit TRUE — this is the asymmetry the design intends: b.py's SourceDigest, GoalDigest and TestSurfaceDigest (a.py is a SOURCE file, not part of the test surface) all stayed put even though the SCAN re-ran selection", bRes)
	}
	if strings.Contains(r3.stdout, "selection: reused") {
		t.Errorf("scan 3: stdout must not claim a selection reuse over a changed tree:\n%s", r3.stdout)
	}
}

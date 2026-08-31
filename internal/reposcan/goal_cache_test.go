// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeGoalCacheStore is an in-memory GoalCacheStore — no DuckDB, no
// scanstore import (reposcan must not depend on the ledger package: see the
// package doc on GoalCacheStore).
type fakeGoalCacheStore struct {
	rows map[string]fakeGoalCacheRow
}

type fakeGoalCacheRow struct {
	goal, provenance string
	ungoaled         bool
}

func newFakeGoalCacheStore() *fakeGoalCacheStore {
	return &fakeGoalCacheStore{rows: map[string]fakeGoalCacheRow{}}
}

func fakeGoalCacheKey(path, digest, model, promptRev string) string {
	return path + "|" + digest + "|" + model + "|" + promptRev
}

func (f *fakeGoalCacheStore) GoalCacheGet(_ context.Context, path, digest, model, promptRev string) (string, string, bool, bool, error) {
	row, ok := f.rows[fakeGoalCacheKey(path, digest, model, promptRev)]
	if !ok {
		return "", "", false, false, nil
	}
	return row.goal, row.provenance, row.ungoaled, true, nil
}

func (f *fakeGoalCacheStore) GoalCachePut(_ context.Context, path, digest, model, promptRev, goal, provenance string, ungoaled bool) error {
	f.rows[fakeGoalCacheKey(path, digest, model, promptRev)] = fakeGoalCacheRow{goal: goal, provenance: provenance, ungoaled: ungoaled}
	return nil
}

// countingGoalSource is a GoalSource — not a Deriver — that counts calls.
// CachingGoalSource wraps a GoalSource, so this is the level its own tests
// need to observe, rather than going through derivingGoalSource and its
// retries.
type countingGoalSource struct {
	calls int
	goal  Goal
	ok    bool
	err   error
}

func (c *countingGoalSource) GoalFor(Candidate) (Goal, bool, error) {
	c.calls++
	return c.goal, c.ok, c.err
}

// TestCachingGoalSourceReusesOnlyIdenticalBytes is Step 1's headline case: a
// goal derived from identical bytes is reused, byte-identical text and all;
// changing the bytes is a genuinely new question and must reach the inner
// source again.
func TestCachingGoalSourceReusesOnlyIdenticalBytes(t *testing.T) {
	root := t.TempDir()
	const path = "a.go"
	full := filepath.Join(root, path)
	if err := os.WriteFile(full, []byte("package p\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inner := &countingGoalSource{goal: Goal{Text: "must never panic", Provenance: "derived:m@v1"}, ok: true}
	store := newFakeGoalCacheStore()
	gs := NewCachingGoalSource(root, inner, store, "m", "gp1")

	c := Candidate{Path: path, Lang: "go"}
	g1, ok1, err1 := gs.GoalFor(c)
	if err1 != nil || !ok1 {
		t.Fatalf("first call: goal=%+v ok=%v err=%v", g1, ok1, err1)
	}
	if inner.calls != 1 {
		t.Fatalf("first call should reach inner exactly once, got %d", inner.calls)
	}

	g2, ok2, err2 := gs.GoalFor(c)
	if err2 != nil || !ok2 {
		t.Fatalf("second call: goal=%+v ok=%v err=%v", g2, ok2, err2)
	}
	if inner.calls != 1 {
		t.Errorf("a second GoalFor over IDENTICAL bytes must not reach inner again, got %d call(s)", inner.calls)
	}
	if g2.Text != g1.Text {
		t.Errorf("a cache hit must return the byte-identical goal text a fresh derivation would be a re-purchase of: got %q, want %q", g2.Text, g1.Text)
	}
	if !GoalWasReused(g2) {
		t.Errorf("a cache hit's provenance must say it was reused: %q", g2.Provenance)
	}
	if GoalWasReused(g1) {
		t.Errorf("the FIRST (freshly-derived) answer must not claim to be reused: %q", g1.Provenance)
	}

	// New bytes are a genuinely new question: the digest changes, so this
	// must be a miss, not a stale hit on the old content.
	if err := os.WriteFile(full, []byte("package p\n\nfunc F() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	g3, ok3, err3 := gs.GoalFor(c)
	if err3 != nil || !ok3 {
		t.Fatalf("third call: goal=%+v ok=%v err=%v", g3, ok3, err3)
	}
	if inner.calls != 2 {
		t.Errorf("changed bytes must reach inner again, got %d call(s)", inner.calls)
	}
	if GoalWasReused(g3) {
		t.Errorf("a fresh derivation over new bytes must not be marked reused: %q", g3.Provenance)
	}

	cgs, ok := gs.(*CachingGoalSource)
	if !ok {
		t.Fatalf("NewCachingGoalSource must return a *CachingGoalSource so a caller can read its stats back, got %T", gs)
	}
	if fresh, reused := cgs.Stats(); fresh != 2 || reused != 1 {
		t.Errorf("Stats() = fresh=%d reused=%d, want fresh=2 reused=1", fresh, reused)
	}
}

// TestUngoaledIsCachedToo: a candidate the inner source declined to give a
// goal for (ok=false, no error) is cached as UNGOALED, so re-asking about
// the same bytes is a cache hit too, not another paid call that gets the
// same "no goal" answer.
func TestUngoaledIsCachedToo(t *testing.T) {
	root := t.TempDir()
	const path = "gen.go"
	if err := os.WriteFile(filepath.Join(root, path), []byte("// generated, do not edit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inner := &countingGoalSource{ok: false}
	store := newFakeGoalCacheStore()
	gs := NewCachingGoalSource(root, inner, store, "m", "gp1")

	c := Candidate{Path: path, Lang: "go"}
	if _, ok, err := gs.GoalFor(c); err != nil || ok {
		t.Fatalf("first call: ok=%v err=%v, want ungoaled (ok=false, err=nil)", ok, err)
	}
	if inner.calls != 1 {
		t.Fatalf("first call should reach inner exactly once, got %d", inner.calls)
	}
	if _, ok, err := gs.GoalFor(c); err != nil || ok {
		t.Fatalf("second call: ok=%v err=%v, want ungoaled again", ok, err)
	}
	if inner.calls != 1 {
		t.Errorf("a cached UNGOALED answer must not re-derive the same 'no goal' verdict, got %d call(s)", inner.calls)
	}
}

// TestPinnedGoalsBypass is asserted at the cmd layer, not here:
// resolveGoalSource (cmd/corral/certify_repo.go) never constructs a
// CachingGoalSource for the --goals path — that branch returns
// fileGoalSource before the caching wiring is ever reached. See
// cmd/corral/certify_repo_goalcache_test.go.

// An infrastructure failure from the inner source (a real error, as opposed
// to ok=false) must never be cached — it is a property of the CALL, not of
// the file, and caching it would freeze a transient outage into a permanent
// refusal.
func TestCachingGoalSourceNeverCachesAnError(t *testing.T) {
	root := t.TempDir()
	const path = "a.go"
	if err := os.WriteFile(filepath.Join(root, path), []byte("package p\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inner := &countingGoalSource{err: os.ErrDeadlineExceeded}
	store := newFakeGoalCacheStore()
	gs := NewCachingGoalSource(root, inner, store, "m", "gp1")

	c := Candidate{Path: path, Lang: "go"}
	if _, _, err := gs.GoalFor(c); err == nil {
		t.Fatal("expected the inner error to propagate")
	}
	if _, _, err := gs.GoalFor(c); err == nil {
		t.Fatal("expected the inner error to propagate again")
	}
	if inner.calls != 2 {
		t.Errorf("an infrastructure failure must never be cached, so every call must reach inner: got %d call(s)", inner.calls)
	}
}

// fixedGoalSource is a GoalSource that always answers with the same goal —
// used only by TestCachingGoalSourceDigestMatchesEmitJobsSourceDigest, which
// needs EmitJobs and a CachingGoalSource to see the IDENTICAL goal text so
// their two cache keys are comparable.
type fixedGoalSource struct{ goal Goal }

func (f fixedGoalSource) GoalFor(Candidate) (Goal, bool, error) { return f.goal, true, nil }

// TestCachingGoalSourceDigestMatchesEmitJobsSourceDigest is the RIDER carried
// from Task 1's review: CachingGoalSource computes its own source digest
// (through a *os.Root it opens itself, in NewCachingGoalSource's
// constructor doc) independently of EmitJobs' own srcDigest computation
// (through a *os.Root EmitJobs opens itself, in its own call). Two
// independent computations that are SUPPOSED to key on the same bytes are
// exactly the shape that silently drifts — one gains a path.Clean, one
// reads through a different root, one starts symlink-following — and nothing
// would fail until a goal cache hit and a verdict cache miss (or the
// reverse) started disagreeing about whether a file's bytes had changed.
//
// This pins them together at the actual call sites: CachingGoalSource's Put
// records the digest it computed (captured here through the fake store,
// which is keyed on exactly that string); EmitJobs' real Job.CacheKey is
// then reproduced BY HAND from that captured digest plus every other input
// EmitJobs folds in (PackageDigest, GoalDigest, TestSurfaceDigest,
// EngineVersion, ModelSet, AuditConfig, Substrate — each computed with the
// same helpers EmitJobs itself calls, since this test lives in package
// reposcan). If CachingGoalSource's digest ever diverges from EmitJobs',
// the hand-built key stops matching the real one and this test fails.
func TestCachingGoalSourceDigestMatchesEmitJobsSourceDigest(t *testing.T) {
	root := t.TempDir()
	const relPath = "pkg/a.go"
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, relPath)), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, relPath), []byte("package pkg\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	goal := Goal{Text: "must never panic", Provenance: "derived:m@v1"}
	store := newFakeGoalCacheStore()
	cgs := NewCachingGoalSource(root, fixedGoalSource{goal: goal}, store, "model-x", "gp1")

	cand := Candidate{Path: relPath, Lang: "go"}
	if _, ok, err := cgs.GoalFor(cand); err != nil || !ok {
		t.Fatalf("CachingGoalSource.GoalFor: ok=%v err=%v", ok, err)
	}

	// Exactly one row was Put — pull the digest CachingGoalSource computed
	// back out of the fake store's key, the only place it is observable
	// from outside the package-private sourceDigest method.
	if len(store.rows) != 1 {
		t.Fatalf("expected exactly one goal_cache row, got %d", len(store.rows))
	}
	var cachedKey string
	for k := range store.rows {
		cachedKey = k
	}
	parts := strings.SplitN(cachedKey, "|", 4)
	if len(parts) != 4 {
		t.Fatalf("unexpected fake store key shape: %q", cachedKey)
	}
	cachedDigest := parts[1]
	if cachedDigest == "" {
		t.Fatal("CachingGoalSource computed an empty source digest")
	}

	// Now run the REAL EmitJobs over the same candidate/root, with a
	// GoalSource that answers with the identical goal text (so GoalDigest
	// matches too), and pull its actual Job.CacheKey.
	cfg := EmitConfig{
		Owner: "o", Repo: "r", Commit: "c", Root: root,
		EngineVersion: "v1", ModelSet: "m1", AuditConfig: "ac1",
		Substrate: SubstrateWorkspace,
	}
	jobs, excl, err := EmitJobs(cfg, []Candidate{cand}, fixedGoalSource{goal: goal})
	if err != nil {
		t.Fatalf("EmitJobs: %v", err)
	}
	if len(excl) != 0 || len(jobs) != 1 {
		t.Fatalf("EmitJobs: jobs=%d excl=%d, want 1 job and 0 exclusions", len(jobs), len(excl))
	}
	realKey := jobs[0].CacheKey

	// Reproduce EmitJobs' key BY HAND, substituting the digest
	// CachingGoalSource computed for SourceDigest and computing every other
	// component with the same helpers EmitJobs itself calls.
	osRoot, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = osRoot.Close() }()
	pkgDigest, err := DigestDir(osRoot, filepath.Dir(relPath))
	if err != nil {
		t.Fatalf("DigestDir: %v", err)
	}
	// No TestSurfacePaths and FileScopedTests false: EmitJobs' suiteDigest
	// is DigestTestSurface over just this candidate's TestPath, which is ""
	// here, so the surface is empty.
	testDigest, err := DigestTestSurface(osRoot, nil)
	if err != nil {
		t.Fatalf("DigestTestSurface: %v", err)
	}
	handBuiltKey := KeyInputs{
		SourceDigest:      cachedDigest,
		PackageDigest:     pkgDigest,
		GoalDigest:        digestString(goal.Text),
		TestSurfaceDigest: testDigest,
		EngineVersion:     cfg.EngineVersion,
		ModelSet:          cfg.ModelSet,
		AuditConfig:       cfg.AuditConfig,
		Substrate:         cfg.Substrate,
	}.CacheKey()

	if handBuiltKey != realKey {
		t.Errorf("CachingGoalSource's digest (%q) does not agree with EmitJobs' own SourceDigest for the same candidate/root: hand-built key %q != real Job.CacheKey %q", cachedDigest, handBuiltKey, realKey)
	}
}

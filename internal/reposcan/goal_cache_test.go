// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"context"
	"os"
	"path/filepath"
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

// SPDX-License-Identifier: Elastic-2.0

package scanstore

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "scans.duckdb"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestVerdictByCacheKeyRoundTrips(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	when := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)

	if _, err := s.Record(ctx, Scan{Owner: "acme", Repo: "r", Commit: "c"}, []File{{
		Path: "a.go", Lang: "go", Disposition: "audited", Gradable: true,
		CacheKey: "KEY1", VerdictJSON: `{"kill_rate":0.5}`, ComputedAt: when,
	}}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	js, at, ok, err := s.VerdictByCacheKey(ctx, "acme", "KEY1")
	if err != nil {
		t.Fatalf("VerdictByCacheKey: %v", err)
	}
	if !ok {
		t.Fatal("no hit for a key that was just recorded")
	}
	if js != `{"kill_rate":0.5}` {
		t.Fatalf("verdict JSON = %q", js)
	}
	if !at.Equal(when) {
		t.Fatalf("computedAt = %v, want %v", at, when)
	}
}

// Tenancy is enforced in SQL. A shared key across tenants must not leak.
func TestVerdictByCacheKeyIsOwnerScoped(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	if _, err := s.Record(ctx, Scan{Owner: "acme", Repo: "r"}, []File{{
		Path: "a.go", Disposition: "audited", Gradable: true,
		CacheKey: "SHARED", VerdictJSON: `{}`, ComputedAt: time.Now().UTC(),
	}}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if _, _, ok, err := s.VerdictByCacheKey(ctx, "other", "SHARED"); err != nil || ok {
		t.Fatalf("owner \"other\" read acme's verdict (ok=%v, err=%v)", ok, err)
	}
}

func TestVerdictByCacheKeyRejectsEmptyOwner(t *testing.T) {
	s := openTemp(t)
	if _, _, ok, err := s.VerdictByCacheKey(context.Background(), "", "KEY1"); ok || err == nil {
		t.Fatal("empty owner must be rejected, never treated as a shared bucket")
	}
}

// The ledger is append-only, so a key can have several rows. The newest scan
// wins; a row with no verdict JSON is not a hit.
func TestVerdictByCacheKeyTakesTheNewestAndSkipsEmpty(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	old := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)

	for _, f := range []struct {
		js string
		at time.Time
	}{{`{"v":"old"}`, old}, {`{"v":"new"}`, recent}} {
		if _, err := s.Record(ctx, Scan{Owner: "acme", Repo: "r", FinishedAt: f.at}, []File{{
			Path: "a.go", Disposition: "audited", Gradable: true,
			CacheKey: "K", VerdictJSON: f.js, ComputedAt: f.at,
		}}); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	js, _, ok, err := s.VerdictByCacheKey(ctx, "acme", "K")
	if err != nil || !ok {
		t.Fatalf("expected a hit: ok=%v err=%v", ok, err)
	}
	if js != `{"v":"new"}` {
		t.Fatalf("got the stale row: %q", js)
	}
}

// TestVerdictByCacheKeyBreaksTieOnID pins the tiebreak property itself,
// rather than relying on two Record calls landing at different wall-clock
// instants (which is what "newest wins" alone would depend on, and which is
// exactly the ambiguity a fast machine or batched recording can collapse).
// Record always stamps ts with time.Now().UTC() and offers no way to pin it,
// so this test reaches through Store's unexported db handle (legal: same
// package) to insert two scans rows with an IDENTICAL ts and different,
// sequence-allocated ids directly, then asserts the higher-id row wins.
func TestVerdictByCacheKeyBreaksTieOnID(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	tied := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)

	insertScanAndFile := func(js string) {
		t.Helper()
		var id int64
		if err := s.db.QueryRowContext(ctx, `INSERT INTO scans (id, ts, owner, repo)
			VALUES (nextval('scans_id'), ?, 'acme', 'r') RETURNING id`, tied).Scan(&id); err != nil {
			t.Fatalf("insert scan: %v", err)
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO scan_files
			(scan_id, path, disposition, gradable, cache_key, verdict_json, computed_at)
			VALUES (?, 'a.go', 'audited', true, 'TIE', ?, ?)`, id, js, tied); err != nil {
			t.Fatalf("insert scan_files: %v", err)
		}
	}

	insertScanAndFile(`{"v":"lower-id"}`)
	insertScanAndFile(`{"v":"higher-id"}`)

	js, _, ok, err := s.VerdictByCacheKey(ctx, "acme", "TIE")
	if err != nil || !ok {
		t.Fatalf("expected a hit: ok=%v err=%v", ok, err)
	}
	if js != `{"v":"higher-id"}` {
		t.Fatalf("tie must resolve to the higher (later-allocated) id, got %q", js)
	}
}

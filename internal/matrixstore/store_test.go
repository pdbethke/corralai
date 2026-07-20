// SPDX-License-Identifier: Elastic-2.0

package matrixstore_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/pdbethke/corralai/internal/matrixstore"
)

func openTmp(t *testing.T) *matrixstore.Store {
	t.Helper()
	s, err := matrixstore.Open(filepath.Join(t.TempDir(), "m.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestRecordAndDeleteCandidates(t *testing.T) {
	s := openTmp(t)
	ctx := context.Background()
	must := func(e error) {
		if e != nil {
			t.Fatal(e)
		}
	}
	must(s.Record(ctx, []matrixstore.Row{
		{RecordID: 1, Repo: "r", Commit: "c", TestSelector: "T::a", Kills: 2, MutantsTotal: 3, DeleteCandidate: false},
		{RecordID: 1, Repo: "r", Commit: "c", TestSelector: "T::b", Kills: 0, MutantsTotal: 3, DeleteCandidate: true},
	}))
	cands, err := s.DeleteCandidates(ctx)
	must(err)
	if len(cands) != 1 || cands[0].TestSelector != "T::b" {
		t.Fatalf("delete candidates = %+v", cands)
	}
	all, err := s.List(ctx)
	must(err)
	if len(all) != 2 {
		t.Fatalf("list = %d rows", len(all))
	}
}

// TestRecordHonorsCallerSuppliedTS proves Record uses the caller-supplied
// Row.TS for ordering rather than stamping its own time.Now(), mirroring
// bugcatch's caller-supplied-ts convention. Two Record calls write rows for
// the SAME (repo, commit, test_selector) key, but deliberately in the
// OPPOSITE order from their caller-supplied TS: the first call (earlier in
// wall-clock time) carries the LATER TS (2000, non-candidate); the second
// call (later in wall-clock time) carries the EARLIER TS (1000, a delete
// candidate). Correct "latest per key" resolution must follow the
// caller-supplied ts, not insertion/wall-clock order, so the first-inserted
// row (ts=2000, non-candidate) is the one DeleteCandidates should treat as
// latest, yielding zero candidates. Against the old time.Now()-stamping
// code, Record ignores Row.TS entirely and stamps its own call-time value,
// so the SECOND call (wall-clock later) always gets the larger stamped ts
// regardless of the caller's TS field — that makes the candidate row
// (inserted second) look "latest" and DeleteCandidates wrongly returns it.
// This test must fail before the fix and pass after it.
func TestRecordHonorsCallerSuppliedTS(t *testing.T) {
	s := openTmp(t)
	ctx := context.Background()
	must := func(e error) {
		if e != nil {
			t.Fatal(e)
		}
	}
	must(s.Record(ctx, []matrixstore.Row{
		{TS: 2000, RecordID: 1, Repo: "r", Commit: "c", TestSelector: "T::x", Kills: 3, MutantsTotal: 3, DeleteCandidate: false},
	}))
	must(s.Record(ctx, []matrixstore.Row{
		{TS: 1000, RecordID: 2, Repo: "r", Commit: "c", TestSelector: "T::x", Kills: 0, MutantsTotal: 3, DeleteCandidate: true},
	}))
	cands, err := s.DeleteCandidates(ctx)
	must(err)
	if len(cands) != 0 {
		t.Fatalf("delete candidates = %+v, want none (the caller-supplied ts=2000 non-candidate row is the true latest, despite being inserted first)", cands)
	}
}

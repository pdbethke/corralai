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

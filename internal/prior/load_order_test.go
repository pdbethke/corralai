// SPDX-License-Identifier: Elastic-2.0

package prior

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/lang"
)

// Through Load: run A recorded as a document (hunk, no outcome) and run B
// as a ledger entry whose row carries a DIFFERENT hunk under the same
// positional id and span. Two edits, each with its own text; B's outcome
// on B's edit. The first cut folded B into A because a ledger row had no
// hunk to be told apart by.
func TestLoadKeepsTwoRunsEditsApartAtOnePlace(t *testing.T) {
	dir := t.TempDir()
	sha := strings.Repeat("d", 64)
	if err := adequacy.WriteMutantSet(filepath.Join(dir, "runA.json"), adequacy.MutantSetFile{
		Format: adequacy.MutantSetFormat,
		Files: map[string]adequacy.MutantSetEntry{"f.py": {ParentSHA256: sha, Mutants: []adequacy.RecordedMutant{
			{ID: "s0/m1", Span: lang.LineRange{Start: 4, End: 4}, Search: "a + b", Replace: "a - b"},
		}}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := auditpush.PushBundle(dir, auditpush.Bundle{
		Scan: auditpush.ScanRow{Repo: "r", Commit: "c2", ScanID: 2}, SourcePushed: true,
		Mutants: []auditpush.MutantRow{
			{Repo: "r", ScanID: 2, Path: "f.py", MutantID: "s0/m1", ParentSHA256: sha, Outcome: "survived", SpanStart: 4, SpanEnd: 4, Shape: "other", Code: adequacy.EncodeHunk("a + b", "a * b")},
		},
	}); err != nil {
		t.Fatal(err)
	}
	p, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	tried, err := p.For("f.py", sha)
	if err != nil {
		t.Fatal(err)
	}
	if len(tried) != 2 {
		t.Fatalf("two runs' edits at one place, got %d:\n%s", len(tried), Render(tried))
	}
	for _, x := range tried {
		if x.Replace == "a * b" && x.Outcome != "survived" {
			t.Errorf("run B's edit lost its outcome: %+v", x)
		}
		if x.Replace == "a - b" && x.Outcome != "" {
			t.Errorf("run A's edit was given run B's outcome: %+v", x)
		}
	}
}

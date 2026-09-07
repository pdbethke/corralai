// SPDX-License-Identifier: Elastic-2.0

package prior

import (
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/lang"
)

// Two runs planted DIFFERENT edits at one place under the same positional
// id. Whichever order their records arrive in — the ledger's outcome rows
// carrying hunks, or a bare row beside a document — the prior holds two
// edits with their own outcomes, never one with the other's hunk. A first
// cut merged by arrival order (a Cursor read of #286, confirmed).
func TestSameIDSameSpanDifferentHunksAreTwoEditsInEitherOrder(t *testing.T) {
	sha := strings.Repeat("a", 64)
	runA := Tried{Path: "a.py", ParentSHA256: sha, ID: "s0/m1", Span: lang.LineRange{Start: 7, End: 7}, Search: "x > 0", Replace: "x >= 0", Outcome: "killed", KilledBy: "t_a"}
	runB := Tried{Path: "a.py", ParentSHA256: sha, ID: "s0/m1", Span: lang.LineRange{Start: 7, End: 7}, Search: "x > 0", Replace: "x < 0", Outcome: "survived"}
	for name, order := range map[string][]Tried{"A then B": {runA, runB}, "B then A": {runB, runA}} {
		got := mergeTried(order)["a.py"]
		if len(got) != 2 {
			t.Fatalf("%s: %d edit(s), want 2:\n%s", name, len(got), Render(got))
		}
		for _, g := range got {
			switch g.Replace {
			case runA.Replace:
				if g.Outcome != "killed" {
					t.Errorf("%s: run A's edit carries %q", name, g.Outcome)
				}
			case runB.Replace:
				if g.Outcome != "survived" {
					t.Errorf("%s: run B's edit carries %q", name, g.Outcome)
				}
			default:
				t.Errorf("%s: an edit with a hunk nobody planted: %q", name, g.Replace)
			}
		}
	}
	// A bare outcome row folds into the ONE hunked edit at its place in
	// either order; beside TWO hunked edits it stays its own record.
	bare := Tried{Path: "a.py", ParentSHA256: sha, ID: "s0/m1", Span: lang.LineRange{Start: 7, End: 7}, Outcome: "killed"}
	doc := Tried{Path: "a.py", ParentSHA256: sha, ID: "s0/m1", Span: lang.LineRange{Start: 7, End: 7}, Search: "x > 0", Replace: "x >= 0"}
	for name, order := range map[string][]Tried{"bare then doc": {bare, doc}, "doc then bare": {doc, bare}} {
		got := mergeTried(order)["a.py"]
		if len(got) != 1 || got[0].Search != "x > 0" || got[0].Outcome != "killed" {
			t.Errorf("%s: want one edit with the hunk and the outcome, got %+v", name, got)
		}
	}
	if got := mergeTried([]Tried{runA, runB, bare})["a.py"]; len(got) != 3 {
		t.Errorf("a bare row beside two hunked edits must stay separate, got %d", len(got))
	}
}

// Digest and Render do not depend on the order the sources were read in.
func TestDigestAndRenderAreOrderIndependent(t *testing.T) {
	sha := strings.Repeat("b", 64)
	edits := []Tried{
		{Path: "a.py", ParentSHA256: sha, ID: "s0/m2", Span: lang.LineRange{Start: 3, End: 3}, Search: "p", Replace: "q", Outcome: "killed"},
		{Path: "a.py", ParentSHA256: sha, ID: "s0/m1", Span: lang.LineRange{Start: 3, End: 3}, Search: "r", Replace: "s", Outcome: "survived"},
		{Path: "a.py", ParentSHA256: sha, ID: "s0/m1", Span: lang.LineRange{Start: 3, End: 3}, Search: "a", Replace: "b", Outcome: "survived"},
		{Path: "a.py", ParentSHA256: sha, ID: "s0/m3", Span: lang.LineRange{Start: 1, End: 2}, Search: "c", Replace: "d", Outcome: "killed"},
	}
	want, wantPara := Digest(edits), Render(edits)
	for _, perm := range [][]int{{3, 2, 1, 0}, {1, 3, 0, 2}, {2, 0, 3, 1}} {
		var p []Tried
		for _, i := range perm {
			p = append(p, edits[i])
		}
		if Digest(p) != want || Render(p) != wantPara {
			t.Fatalf("permutation %v changes the digest or the paragraph", perm)
		}
		if got := mergeTried(p)["a.py"]; Digest(got) != want {
			t.Fatalf("permutation %v through mergeTried changes the digest", perm)
		}
	}
}

// A ledger row carries its hunk in `code` (EncodeHunk) when the entry holds
// source; the prior reads it back as the edit, not as replace-only text.
func TestLedgerRowsCarryTheirHunkIntoThePrior(t *testing.T) {
	dir := t.TempDir()
	sha := strings.Repeat("c", 64)
	if _, err := auditpush.PushBundle(dir, auditpush.Bundle{
		Scan: auditpush.ScanRow{Repo: "r", Commit: "c1", ScanID: 1}, SourcePushed: true,
		Mutants: []auditpush.MutantRow{
			{Repo: "r", ScanID: 1, Path: "f.py", MutantID: "s0/m1", ParentSHA256: sha, Outcome: "killed", SpanStart: 2, SpanEnd: 2, Shape: "other", Code: adequacy.EncodeHunk("return x", "return -x")},
		},
	}); err != nil {
		t.Fatal(err)
	}
	p, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	tried, err := p.For("f.py", sha)
	if err != nil || len(tried) != 1 || tried[0].Search != "return x" || tried[0].Replace != "return -x" {
		t.Fatalf("the hunk did not come back: %+v (%v)", tried, err)
	}
	if !strings.Contains(Render(tried), "return -x") {
		t.Fatalf("Render does not quote the hunk:\n%s", Render(tried))
	}
}

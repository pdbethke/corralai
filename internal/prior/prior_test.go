// SPDX-License-Identifier: Elastic-2.0

package prior

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/scanstore"
)

func shaOf(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

// A directory holding a mutant-set document (the hunks) and a scan ledger
// (the outcomes) for the same run merges into one prior per edit: the
// document says WHAT was tried, the ledger says WHAT HAPPENED. Only edits
// recorded against the file's exact bytes are handed on.
func TestLoadMergesDocumentAndLedgerAndHonoursSameBytes(t *testing.T) {
	dir := t.TempDir()
	code := "def get(url):\n    return request(\"get\", url)\n"
	sha := shaOf(code)
	if err := adequacy.WriteMutantSet(filepath.Join(dir, "mutants.json"), adequacy.MutantSetFile{
		Format: adequacy.MutantSetFormat,
		Files: map[string]adequacy.MutantSetEntry{
			"api.py": {ParentSHA256: sha, Mutants: []adequacy.RecordedMutant{
				{ID: "s0/m1", Span: lang.LineRange{Start: 2, End: 2}, Search: `    return request("get", url)`, Replace: `    return request("post", url)`},
				{ID: "s0/m2", Span: lang.LineRange{Start: 2, End: 2}, Search: `    return request("get", url)`, Replace: `    return None`},
			}},
			"old.py": {ParentSHA256: shaOf("older bytes"), Mutants: []adequacy.RecordedMutant{{ID: "m1", Span: lang.LineRange{Start: 1, End: 1}, Search: "a", Replace: "b"}}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	st, err := scanstore.Open(filepath.Join(dir, "scans.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id, err := st.Record(ctx, scanstore.Scan{Repo: "r", Commit: "c"}, []scanstore.File{{Path: "api.py", Disposition: "audited"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RecordMutants(ctx, []scanstore.Mutant{
		{ScanID: id, Path: "api.py", MutantID: "s0/m1", ParentSHA256: sha, Outcome: "killed", KilledBy: "tests/test_api.py::test_get", SpanStart: 2, SpanEnd: 2, Shape: adequacy.ShapeConstantChanged},
		{ScanID: id, Path: "api.py", MutantID: "s0/m2", ParentSHA256: sha, Outcome: "survived", Proven: true, SpanStart: 2, SpanEnd: 2, Shape: adequacy.ShapeReturnChanged},
	}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	p, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	tried, err := p.For("api.py", sha)
	if err != nil || len(tried) != 2 {
		t.Fatalf("For = %d edits, err %v; want 2", len(tried), err)
	}
	if tried[0].Search == "" || tried[0].Outcome != "killed" || tried[0].KilledBy == "" {
		t.Errorf("merged edit lacks the hunk or the outcome: %+v", tried[0])
	}
	if !tried[1].Proven || tried[1].Shape != adequacy.ShapeReturnChanged {
		t.Errorf("merged survivor: %+v", tried[1])
	}
	// The same-bytes rule: known path, other bytes.
	if _, err := p.For("old.py", shaOf("current bytes")); !errors.Is(err, ErrDifferentVersion) {
		t.Errorf("a prior for other bytes must be refused, got %v", err)
	}
	// Never-seen path: nothing, no error.
	if got, err := p.For("never.py", sha); got != nil || err != nil {
		t.Errorf("unknown path: %v %v", got, err)
	}
	// The paragraph names place, shape, hunk and outcome, and asks for different faults.
	para := Render(tried)
	for _, want := range []string{"ALREADY TRIED", "2 edit(s)", "line 2, constant-changed", `request("post", url)`, "KILLED by tests/test_api.py::test_get", "line 2, return-changed", "SURVIVED, gap already proven", "DIFFERENT faults"} {
		if !strings.Contains(para, want) {
			t.Errorf("paragraph lacks %q:\n%s", want, para)
		}
	}
	// The digest is stable and empty for nothing.
	if Digest(tried) != Digest(tried) || Digest(nil) != "" || Digest(tried) == "" {
		t.Error("digest")
	}
}

// A prompt never grows without bound: past MaxRendered edits the rest are a count.
func TestRenderIsBounded(t *testing.T) {
	var tried []Tried
	for i := 0; i < MaxRendered+7; i++ {
		tried = append(tried, Tried{Span: lang.LineRange{Start: i + 1, End: i + 1}, Shape: "other", Outcome: "killed"})
	}
	para := Render(tried)
	if !strings.Contains(para, "… and 7 more.") || strings.Count(para, "\n") > MaxRendered+3 {
		t.Errorf("unbounded paragraph:\n%s", para)
	}
}

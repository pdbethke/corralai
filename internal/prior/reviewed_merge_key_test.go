// SPDX-License-Identifier: Elastic-2.0

package prior

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/lang"
)

// TestTwoRunsSamePositionalIDAreTwoEdits is a Claude Code reviewer's
// reproduction (`corral review --scope internal/prior`, 2026-09-07),
// inverted. Two runs recorded edits on the SAME file at the SAME bytes;
// mutant ids are positional per run, so both runs' first mutant is
// "s0/m1" while being a different edit. The merge used to key on (sha,
// id), dropping run 2's edit, undercounting, and grafting run 1's hunk
// onto run 2's line. Fails on the unfixed key.
func TestTwoRunsSamePositionalIDAreTwoEdits(t *testing.T) {
	dir := t.TempDir()
	code := "def f(x):\n    y = 1\n    if x > 10:\n        return 1\n    return None\n"
	h := sha256.Sum256([]byte(code))
	sha := hex.EncodeToString(h[:])

	// Run 1's record: a constant change, with no span recorded on the row.
	if err := adequacy.WriteMutantSet(filepath.Join(dir, "run1.json"), adequacy.MutantSetFile{
		Format: adequacy.MutantSetFormat,
		Files: map[string]adequacy.MutantSetEntry{
			"f.py": {ParentSHA256: sha, Mutants: []adequacy.RecordedMutant{
				{ID: "s0/m1", Search: "    y = 1", Replace: "    y = 2"},
			}},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// Run 2's record: a DIFFERENT edit, at line 42, that is again the first
	// mutant of shard 0.
	if err := adequacy.WriteMutantSet(filepath.Join(dir, "run2.json"), adequacy.MutantSetFile{
		Format: adequacy.MutantSetFormat,
		Files: map[string]adequacy.MutantSetEntry{
			"f.py": {ParentSHA256: sha, Mutants: []adequacy.RecordedMutant{
				{ID: "s0/m1", Span: lang.LineRange{Start: 42, End: 42}, Search: "    if x > 10:", Replace: "    if x >= 10:"},
			}},
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
	para := Render(tried)
	if len(tried) != 2 {
		t.Fatalf("2 edits are on record for these exact bytes; the prior hands the generator %d:\n%s", len(tried), para)
	}
	if !strings.Contains(para, "x >= 10") || !strings.Contains(para, "(2 edit(s) from earlier runs)") {
		t.Fatalf("run 2's edit or the count is missing:\n%s", para)
	}
	if strings.Contains(para, "line 42, unclassified: `y = 1`") {
		t.Fatalf("run 1's hunk was grafted onto run 2's line:\n%s", para)
	}
	_ = fmt.Sprintf
}

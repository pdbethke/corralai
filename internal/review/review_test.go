// SPDX-License-Identifier: Elastic-2.0

package review

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseReadsAFencedReplyAndNormalisesTiers(t *testing.T) {
	reply := "Here is my review.\n```json\n{\"opinion\":\"the router trusts its callers\",\"findings\":[" +
		"{\"claim\":\"a\",\"tier\":\"reproduced\",\"file\":\"x.go\",\"line\":3,\"severity\":\"High\",\"script\":\"exit 0\"}," +
		"{\"claim\":\"b\",\"tier\":\"code_read\",\"file\":\"y.go\",\"line\":9}," +
		"{\"claim\":\"c\",\"tier\":\"maybe\"}],\"sound\":[\"the parser\"]}\n```\nThanks."
	opinion, findings, sound, err := Parse(reply)
	if err != nil {
		t.Fatal(err)
	}
	if opinion != "the router trusts its callers" || len(sound) != 1 || len(findings) != 3 {
		t.Fatalf("opinion=%q sound=%v findings=%d", opinion, sound, len(findings))
	}
	if findings[0].ID != "R1" || findings[0].Declared != TierReproduced || findings[0].Severity != "high" || findings[0].Script != "exit 0" {
		t.Errorf("R1: %+v", findings[0])
	}
	if findings[1].Declared != TierCodeRead {
		t.Errorf("code_read must normalise to CODE-READ: %+v", findings[1])
	}
	// An unknown tier asserts nothing the run can check: HYPOTHESIS.
	if findings[2].Declared != TierHypothesis {
		t.Errorf("an unknown tier must record as HYPOTHESIS: %+v", findings[2])
	}
	if _, _, _, err := Parse("no json here"); err == nil {
		t.Error("a reply with no JSON object must be an error, not an empty review")
	}
}

type scripted map[string]struct {
	out  string
	code int
	err  error
}

func (s scripted) Run(_ context.Context, script string) (string, int, error) {
	r := s[script]
	return r.out, r.code, r.err
}

// TestReproduceOnlyEverDemotes: a REPRODUCED script that exits 0 stays;
// one that exits non-zero, cannot run, or is missing demotes to CODE-READ
// with the reason; a CODE-READ or HYPOTHESIS claim is never promoted even
// when its script would pass — the reviewer did not claim it.
func TestReproduceOnlyEverDemotes(t *testing.T) {
	r := &Review{Findings: []Finding{
		{ID: "R1", Declared: TierReproduced, Script: "holds"},
		{ID: "R2", Declared: TierReproduced, Script: "fails"},
		{ID: "R3", Declared: TierReproduced, Script: "broken"},
		{ID: "R4", Declared: TierReproduced},
		{ID: "R5", Declared: TierCodeRead, Script: "holds"},
		{ID: "R6", Declared: TierHypothesis},
	}}
	Reproduce(context.Background(), scripted{
		"holds":  {out: "got 7, want 8", code: 0},
		"fails":  {out: "ok", code: 1},
		"broken": {err: errors.New("sh: not found")},
	}, r)
	want := map[string]string{"R1": TierReproduced, "R2": TierCodeRead, "R3": TierCodeRead, "R4": TierCodeRead, "R5": TierCodeRead, "R6": TierHypothesis}
	for _, f := range r.Findings {
		if f.Tier != want[f.ID] {
			t.Errorf("%s: recorded %s, want %s (%s)", f.ID, f.Tier, want[f.ID], f.Demoted)
		}
	}
	if r.Findings[0].Stdout != "got 7, want 8" || r.Findings[0].ExitCode == nil || *r.Findings[0].ExitCode != 0 {
		t.Errorf("R1 must carry its output and exit: %+v", r.Findings[0])
	}
	for _, i := range []int{1, 2, 3} {
		if r.Findings[i].Demoted == "" {
			t.Errorf("%s demoted silently", r.Findings[i].ID)
		}
	}
	if r.Findings[4].ExitCode != nil {
		t.Error("a CODE-READ claim's script must not be run — the reviewer did not claim a reproduction")
	}
	rep, cr, hy := r.Counts()
	if rep != 1 || cr != 4 || hy != 1 {
		t.Errorf("counts = %d/%d/%d", rep, cr, hy)
	}
}

func TestLoadScopeCapsAndNamesWhatItDidNotShow(t *testing.T) {
	root := t.TempDir()
	must := func(p, c string) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, p), []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	must("pkg/a.go", strings.Repeat("a", 100))
	must("pkg/b.go", strings.Repeat("b", 100))
	must("pkg/c.go", strings.Repeat("c", 100))
	must("pkg/.git/config", "x")
	must("pkg/.corral/ledger/scans/e.json", "{}")
	must("pkg/bin.dat", "\x00\x01\x02")
	sc, err := LoadScope(root, "pkg", 250)
	if err != nil {
		t.Fatal(err)
	}
	if len(sc.Files) != 2 || sc.Files[0] != "pkg/a.go" || sc.Files[1] != "pkg/b.go" {
		t.Errorf("shown = %v, want a.go and b.go (sorted, under the cap)", sc.Files)
	}
	if !sc.Truncated || len(sc.Unshown) != 1 || sc.Unshown[0] != "pkg/c.go" {
		t.Errorf("truncated=%v unshown=%v, want c.go named", sc.Truncated, sc.Unshown)
	}
	brief := Brief("r", "c", "pkg", sc)
	if !strings.Contains(brief, "NOT shown") || !strings.Contains(brief, "pkg/c.go") {
		t.Error("the brief must tell the reviewer what it was not shown")
	}
	if strings.Contains(brief, ".git") || strings.Contains(brief, "bin.dat") || strings.Contains(brief, ".corral") {
		t.Error("the brief carries files the reviewer never needs")
	}
	if _, err := LoadScope(root, "nope", 250); err == nil {
		t.Error("a missing scope must be an error")
	}
}

// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"io"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/reposcan"
)

// The scan's test map rides beside the Selection it was made from, hop by
// hop — localExecutor.selection, localAuditInput, jailWiringInput, the
// JailScorer and the RunSpec — because every command built from the
// Selection after the scan (the authored pass, the uncovered dev pass, each
// mutant's span) goes through the engine, which reads the runner row and the
// grain from the map. Each hop is a field-by-field copy, and a copy fails
// open on whatever field nobody remembered: a map dropped at any hop leaves
// the authored test off the authored pass's command, and the pass then
// reports the selection's own tests as the authored test's proof. So this
// walks the real path, the way an audit does, and asserts the map at each
// hop and the command at the end.
func TestSelectionMapSurvivesToTheAuthoredCommand(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "pkg", "a.py"), "def f():\n    return 1\n")
	mustWrite(t, filepath.Join(root, "tests", "test_a.py"), "def test_x():\n    pass\n")
	py, _ := lang.ByName("python")
	ex := &localExecutor{repoDir: root, substrate: substrateWorkspace}
	ex.selection = reposcan.NewSelectionEvidence(py, py.TestCmd(), []byte(`{"format":"corral-selection-3","tests":1,"files":{`+
		`"pkg/a.py":{"tests":["tests/test_a.py::test_x"],"lines":{},"static":[]},`+
		`"tests/test_a.py":{"tests":["tests/test_a.py::test_x"],"lines":{},"static":[]}}}`))
	if ex.selection.Map.Runner == "" || ex.selection.Map.Method == "" {
		t.Fatalf("fixture: the evidence built no map: %+v", ex.selection)
	}
	job := reposcan.Job{Path: "pkg/a.py", TestPath: "tests/test_a.py", Lang: "python"}

	in := ex.auditInputFor(job)
	if len(in.selection.Tests) != 1 {
		t.Fatalf("fixture: selection = %+v, want the one test that executed the file", in.selection)
	}
	if !reflect.DeepEqual(in.selectionMap, ex.selection.Map) {
		t.Fatalf("localAuditInput.selectionMap = %+v, want the scan's map", in.selectionMap)
	}

	rs := newAuditRunSpec(in, auditRoles{}, runSubject{lang: "python", codePath: job.Path, devTestPath: job.TestPath})
	if !reflect.DeepEqual(rs.SelectionMap, ex.selection.Map) || rs.RepoDir != root {
		t.Fatalf("RunSpec: SelectionMap = %+v, RepoDir = %q; want the scan's map and %q", rs.SelectionMap, rs.RepoDir, root)
	}

	prep, err := prepareAuditJail(context.Background(), in, py, time.Minute, io.Discard)
	if err != nil {
		t.Fatalf("prepareAuditJail: %v", err)
	}
	defer prep.cleanup()
	scorer := prep.wiring.scorer
	if !reflect.DeepEqual(scorer.SelectionMap, ex.selection.Map) || scorer.RepoDir != root {
		t.Fatalf("JailScorer: SelectionMap = %+v, RepoDir = %q; want the scan's map and %q", scorer.SelectionMap, scorer.RepoDir, root)
	}

	// The end of the path: the authored pass runs the selection AND the
	// authored test, and the dev pass runs the selection.
	got := scorer.AuthoredCommand(job.Path, adequacy.ShellSplit(rs.TestCmd))
	if len(got) != len(in.selection.Cmd)+1 || !slices.Equal(got[:len(in.selection.Cmd)], in.selection.Cmd) {
		t.Fatalf("authored command = %v, want the selection %v plus the authored test's path", got, in.selection.Cmd)
	}
	if got, want := advpool.DevCommand(rs), adequacy.ShellJoin(in.selection.Cmd); got != want {
		t.Errorf("dev command = %q, want the selection %q", got, want)
	}
}

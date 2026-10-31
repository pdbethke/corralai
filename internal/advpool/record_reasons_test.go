// SPDX-License-Identifier: Elastic-2.0

package advpool_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/reposcan"
	"github.com/pdbethke/corralai/internal/sandbox"
	"github.com/pdbethke/corralai/internal/testmap"
)

// What a unit's failure planted, as a runner can print it: a secret-like
// argv token and an absolute host path. Neither may reach the record.
const (
	plantedSecret = "API_KEY=sk-live-PLANTED"
	plantedPath   = "/home/operator/private-checkout/node_modules/.bin/vitest"
)

// plantedRunner answers the unit pass by argv: the listing, a green unit,
// a unit whose jail could not start it (an error quoting the plants) and a
// red unit whose stderr quotes them.
type plantedRunner struct {
	res  map[string]sandbox.EnumerateResult
	errs map[string]error
}

func plantKey(cmd []string) string { return strings.Join(cmd, "\x00") }

func (r plantedRunner) Enumerate(ctx context.Context, files map[string]string, cmd []string) (string, error) {
	res, err := r.EnumerateDetailed(ctx, files, cmd)
	return res.Output, err
}

func (r plantedRunner) EnumerateDetailed(_ context.Context, _ map[string]string, cmd []string) (sandbox.EnumerateResult, error) {
	k := plantKey(cmd)
	if err, ok := r.errs[k]; ok {
		return sandbox.EnumerateResult{}, err
	}
	if res, ok := r.res[k]; ok {
		return res, nil
	}
	return sandbox.EnumerateResult{}, errors.New("unexpected command " + strings.Join(cmd, " "))
}

// THE RECORD NEVER QUOTES A FAILURE. A unit's reason reaches the verdict
// (UnreportedReason for a unit with no evidence, ExcludedReason for a red
// unit left out of a red baseline's retry), and the verdict is signed into
// the ledger and published. The unit pass once wrote the runner's error and
// the failed run's output tail into that reason, so an OS error naming a
// host path, or a stderr line echoing a secret in the operator's argv, was
// published. The reason is a fixed phrase per failure class; what the run
// printed is the unit's Detail, for the terminal, and shown here to have
// been planted, so the absence below is not an artefact of a quiet fixture.
func TestAUnitsFailureTextNeverReachesTheVerdict(t *testing.T) {
	p, ok := lang.ByName("typescript")
	if !ok {
		t.Fatal("no typescript plugin")
	}
	inv := lang.Cmd([]string{"vitest", "run"})
	list, ok := lang.ListUnitsCmd(p, inv)
	if !ok {
		t.Fatal("vitest has no unit listing")
	}
	unitCmd := func(u string) []string {
		cmd, ok := lang.UnitRunCmd(p, inv, u)
		if !ok {
			t.Fatalf("no one-unit command for %s", u)
		}
		cov, ok := p.(lang.CoverageReporter).CoverageCmd(lang.EvidenceUnitCmd(p, inv, cmd))
		if !ok {
			t.Fatalf("no coverage command for %v", cmd)
		}
		return cov
	}
	planted := plantedSecret + " " + plantedPath
	run := plantedRunner{
		res: map[string]sandbox.EnumerateResult{
			plantKey(list):                          {Output: "corral-units-cwd /w\n[{\"file\":\"/w/test/green.test.ts\"},{\"file\":\"/w/test/jail.test.ts\"},{\"file\":\"/w/test/red.test.ts\"}]\n"},
			plantKey(unitCmd("test/green.test.ts")): {Output: "# suite-exit 0\ncorral-js-coverage: v2\n7 10 src/x.ts\n"},
			plantKey(unitCmd("test/red.test.ts")): {Output: "# suite-exit 1\ncorral-js-coverage: v2\n",
				Stderr: "Error: spawn " + planted + " ENOENT\n"},
		},
		errs: map[string]error{
			plantKey(unitCmd("test/jail.test.ts")): errors.New("bwrap: execvp " + planted + ": permission denied"),
		},
	}
	ev := reposcan.CollectUnitEvidence(context.Background(), run, map[string]string{}, p, inv, "/w", 1, func(int, int) {})
	if !ev.Ran {
		t.Fatalf("the pass did not run: %q", ev.Note)
	}
	// The plants were really there: each failing unit's Detail carries
	// them for the operator's terminal.
	for _, u := range ev.Units {
		if u.Unit != "test/green.test.ts" && (!strings.Contains(u.Detail, plantedSecret) || !strings.Contains(u.Detail, plantedPath)) {
			t.Fatalf("%s: Detail %q lacks the planted text, so this test would prove nothing", u.Unit, u.Detail)
		}
	}

	files := map[string]string{"test/green.test.ts": "", "test/jail.test.ts": "", "test/red.test.ts": "", "src/x.ts": ""}
	m := reposcan.MapFromUnits(ev, reposcan.UnitFilesFor(p, files), inv, p)
	r, err := lang.RunnerByID(p, m.Runner)
	if err != nil {
		t.Fatal(err)
	}
	sel, err := testmap.Select(m, r, m.Command, "/repo", "src/x.ts", "test/green.test.ts")
	if err != nil {
		t.Fatal(err)
	}
	// The red baseline's first retry leaves the red unit out, which is
	// where its reason becomes ExcludedReason.
	retry, red := reposcan.WithoutRedUnits(m, p, "/repo", "src/x.ts", "test/green.test.ts", m.Command, sel)
	if len(red) != 1 || retry.Fallback != "" {
		t.Fatalf("retry = %+v, red %v: want the red unit left out", retry, red)
	}
	retry.Excluded = red // as the scan's first retry sets it (redUnitRetry)

	v := advpool.VerdictFromSpec(advpool.RunSpec{Lang: "typescript", CodePath: "src/x.ts", DevTestPath: "test/green.test.ts",
		TestCmd: "vitest run", Selection: retry, SelectionMap: m, RepoDir: "/repo"})
	ts := v.TestSelection
	if ts.UnreportedReason != "could not run: the jail could not start it" || ts.ExcludedReason != "run failed: exit 1" {
		t.Errorf("UnreportedReason %q, ExcludedReason %q: want the fixed phrases", ts.UnreportedReason, ts.ExcludedReason)
	}
	fields, err := json.Marshal(ts)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{plantedSecret, "sk-live", plantedPath, "/home/operator"} {
		if strings.Contains(string(fields), leak) {
			t.Errorf("the verdict's TestSelection carries %q: %s", leak, fields)
		}
		if strings.Contains(string(wire), leak) {
			t.Errorf("the marshalled verdict (VerdictJSON) carries %q", leak)
		}
	}
}

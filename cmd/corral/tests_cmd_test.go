// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/reposcan"
	"github.com/pdbethke/corralai/internal/testmap"
)

// testsMapFixture is a TypeScript tree with two sources and two units: one
// unit executes src/app/Service.ts, the other failed, and src/app/Idle.ts is
// measured by a unit that ran and never executed it.
func testsMapFixture(t *testing.T) (root string, fake *fakeUnitPass) {
	t.Helper()
	root = unitEvidenceTSFixture(t)
	mustWrite(t, filepath.Join(root, "src", "app", "Idle.ts"), "export function idle(): number { return 2; }\n")
	mustWrite(t, filepath.Join(root, "tests", "composition", "broken.test.ts"), "test('broken', () => {});\n")
	fake = &fakeUnitPass{ev: reposcan.UnitEvidence{Ran: true, Units: []reposcan.UnitResult{
		unitRun("tests/composition/service.test.ts", map[string]lang.LineCounts{
			"src/app/Service.ts": {Executed: 1, Executable: 1},
			"src/app/Idle.ts":    {Executed: 0, Executable: 1},
		}),
		{Unit: "tests/composition/broken.test.ts", Err: "not run (time limit)"},
	}}}
	fake.install(t)
	return root, fake
}

func TestTestsMapPrintsTheSummary(t *testing.T) {
	refuseModelEndpoints(t)
	root, _ := testsMapFixture(t)
	var out, errb bytes.Buffer
	code := runTests([]string{"map", "--repo-dir", root, "--substrate", substrateWorkspace, "--no-selection-cache", "--", "vitest", "run"}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out.String(), errb.String())
	}
	for _, want := range []string{
		"language: typescript, runner: vitest\n",
		"method: unit-evidence\n",
		"2 sources, 2 units (1 with no evidence)\n",
		"1 sources executed by no unit that reported; they are graded by the 1 units with no evidence\n",
		"no evidence: tests/composition/broken.test.ts: not run (time limit)\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing line %q in:\n%s", want, out.String())
		}
	}
}

func TestTestsMapWritesTheMapFile(t *testing.T) {
	refuseModelEndpoints(t)
	root, _ := testsMapFixture(t)
	dst := filepath.Join(t.TempDir(), "map.json")
	var out, errb bytes.Buffer
	code := runTests([]string{"map", "--repo-dir", root, "--substrate", substrateWorkspace, "--no-selection-cache", "--out", dst, "--", "vitest", "run"}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d\nstderr:\n%s", code, errb.String())
	}
	f, err := os.Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	m, err := testmap.Read(f)
	if err != nil {
		t.Fatalf("the written map does not read back: %v", err)
	}
	if m.Method != testmap.MethodUnitEvidence || len(m.Tests) != 2 || len(m.Sources) != 2 {
		t.Errorf("round-tripped map = method %q, %d tests, %d sources; want unit-evidence, 2, 2", m.Method, len(m.Tests), len(m.Sources))
	}
	if got := m.Unreported(); len(got) != 1 || got[0] != "tests/composition/broken.test.ts" {
		t.Errorf("Unreported = %v", got)
	}
}

// The jail runs units --workers at a time and corral names no count: a unit
// pass with none refuses before anything runs.
func TestTestsMapRefusesWithoutWorkers(t *testing.T) {
	refuseModelEndpoints(t)
	root, fake := testsMapFixture(t)
	var out, errb bytes.Buffer
	code := runTests([]string{"map", "--repo-dir", root, "--substrate", substrateJail, "--no-selection-cache", "--", "vitest", "run"}, &out, &errb)
	if code != 2 {
		t.Fatalf("exit %d, want 2\nstdout:\n%s\nstderr:\n%s", code, out.String(), errb.String())
	}
	if !strings.Contains(errb.String(), "--workers") {
		t.Errorf("the refusal must name --workers:\n%s", errb.String())
	}
	if n := len(fake.called()); n != 0 {
		t.Errorf("the unit pass ran %d time(s) despite the refusal", n)
	}
}

func TestTestsHelpExitsZero(t *testing.T) {
	for _, args := range [][]string{{"-h"}, {"map", "-h"}} {
		var out, errb bytes.Buffer
		if code := runTests(args, &out, &errb); code != 0 {
			t.Errorf("corral tests %v exit %d, want 0", args, code)
		}
		if !strings.Contains(out.String()+errb.String(), "corral tests map") {
			t.Errorf("corral tests %v printed no usage naming `corral tests map`", args)
		}
	}
}

// A file no unit that reported executed is graded by the units with no
// evidence (the engine adds every unreported unit to every selection), so the
// summary must not call it uncovered; with every unit reporting, it is.
func TestTestsMapDoesNotCallAFileUncoveredWhileUnitsAreUnreported(t *testing.T) {
	refuseModelEndpoints(t)
	run := func(t *testing.T, units []reposcan.UnitResult) string {
		root := unitEvidenceTSFixture(t)
		mustWrite(t, filepath.Join(root, "src", "app", "Idle.ts"), "export function idle(): number { return 2; }\n")
		(&fakeUnitPass{ev: reposcan.UnitEvidence{Ran: true, Units: units}}).install(t)
		var out, errb bytes.Buffer
		if code := runTests([]string{"map", "--repo-dir", root, "--substrate", substrateWorkspace, "--no-selection-cache", "--", "vitest", "run"}, &out, &errb); code != 0 {
			t.Fatalf("exit %d\n%s", code, errb.String())
		}
		return out.String()
	}
	served := unitRun("tests/composition/service.test.ts", map[string]lang.LineCounts{
		"src/app/Service.ts": {Executed: 1, Executable: 1},
		"src/app/Idle.ts":    {Executed: 0, Executable: 1},
	})
	t.Run("with a failed unit", func(t *testing.T) {
		s := run(t, []reposcan.UnitResult{served, {Unit: "tests/composition/broken.test.ts", Err: "run failed"}})
		if strings.Contains(s, "uncovered:") {
			t.Errorf("a file the unreported unit grades must not be listed uncovered:\n%s", s)
		}
		if want := "1 sources executed by no unit that reported; they are graded by the 1 units with no evidence\n"; !strings.Contains(s, want) {
			t.Errorf("missing %q:\n%s", want, s)
		}
	})
	t.Run("with every unit reporting", func(t *testing.T) {
		s := run(t, []reposcan.UnitResult{served})
		if !strings.Contains(s, "uncovered: src/app/Idle.ts\n") {
			t.Errorf("missing uncovered line:\n%s", s)
		}
		if strings.Contains(s, "graded by the") {
			t.Errorf("no unit is unreported, so no replacement line:\n%s", s)
		}
	})
}

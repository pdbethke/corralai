// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"context"
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
)

// TestAuthoredTestPath_RelocatesIntoDevTestDir is the regression test for the
// defect a paid pallets/flask audit surfaced on 2026-07-31: the pool authored
// a test, it compiled, it passed on clean code — and it graded NOTHING,
// because it was written to a sibling of the code file (src/flask/cli_test.py)
// and flask's pyproject.toml sets `testpaths = ["tests"]`, so the project's
// own test command never collected it. The run reported CompliantPass=true
// CanaryKilled=false and was correctly marked [TEST UNSOUND] by the positive
// control — but the coverage was lost.
//
// The fix relocates the authored test's STEM into the DEV test's own
// directory and then lets the language plugin name it. The dev test's
// directory is collected by construction: it holds the test that paired with
// this code file, and that suite demonstrably executes the file (it is what
// produced the dev-adequacy score). Each plugin's own convention is then what
// supplies the discovery-matching file NAME, so this stays language-agnostic
// — no parsing of testpaths / jest roots / rake FileLists, which is an
// endless per-language tail where every miss is a silent wrong verdict.
func TestAuthoredTestPath_RelocatesIntoDevTestDir(t *testing.T) {
	cases := []struct {
		name     string
		codePath string
		devTest  string
		want     string
	}{{
		name:     "python/flask — the measured shape",
		codePath: "src/flask/cli.py",
		devTest:  "tests/test_cli.py",
		want:     "tests/test_cli_corral.py",
	}, {
		name:     "go — must keep the _test.go suffix or it is not a test file at all",
		codePath: "internal/auth/login.go",
		devTest:  "internal/auth/login_test.go",
		want:     "internal/auth/login_corral_test.go",
	}, {
		name:     "ruby — dev test lives under test/, code under app/",
		codePath: "app/pricing.rb",
		devTest:  "test/pricing_test.rb",
		want:     "test/pricing_corral_test.rb",
	}, {
		name:     "javascript — dev test in a __tests__ dir",
		codePath: "src/foo.js",
		devTest:  "src/__tests__/foo.test.js",
		want:     "src/__tests__/foo_corral.test.js",
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := authoredTestPath(c.codePath, c.devTest, nil)
			if got != c.want {
				t.Fatalf("authoredTestPath(%q, %q) = %q, want %q", c.codePath, c.devTest, got, c.want)
			}
			if got == c.devTest {
				t.Fatalf("authored path collided with the DEV test %q — overlaying it would shadow the real suite", c.devTest)
			}
		})
	}
}

// TestAuthoredTestPath_NoDevTestFallsBackUnchanged pins that single-file mode
// (and any caller with no dev test path to offer) keeps TODAY's behavior
// byte-for-byte. Single-file mode deliberately overlays the DEV test at
// advPoolTestPath too (see scoreWorkspace), so the authored and dev paths
// being the same string there is the existing contract, not a collision.
func TestAuthoredTestPath_NoDevTestFallsBackUnchanged(t *testing.T) {
	for _, codePath := range []string{"passwd.py", "src/flask/cli.py", "internal/auth/login.go", "weird.unknownext"} {
		if got, want := authoredTestPath(codePath, "", nil), advPoolTestPath(codePath); got != want {
			t.Fatalf("authoredTestPath(%q, \"\") = %q, want the unchanged advPoolTestPath %q", codePath, got, want)
		}
	}
}

// TestAuthoredTestPath_NeverShadowsAnExistingRepoFile guards the soundness
// property directly: the authored test is a BRAND-NEW file overlaid onto the
// repo workspace, so if its computed path happens to name a file the repo
// already contains, overlaying it would silently replace real source — the
// same class of defect as the stale-.pyc phantom survivors (a measurement
// taken in a workspace that is not the one it claims to be).
func TestAuthoredTestPath_NeverShadowsAnExistingRepoFile(t *testing.T) {
	base := map[string]string{
		"tests/test_cli.py":        "REAL DEV TEST",
		"tests/test_cli_corral.py": "SOME REAL FILE THAT ALREADY EXISTS",
	}
	got := authoredTestPath("src/flask/cli.py", "tests/test_cli.py", base)
	if _, clash := base[got]; clash {
		t.Fatalf("authoredTestPath returned %q, which already exists in the repo — overlaying it would destroy real source", got)
	}
	if got == "" {
		t.Fatal("authoredTestPath must always yield a usable path")
	}
}

// TestScoreAuthoredReport_OverlaysAtTheRelocatedPath proves the wiring, not
// just the arithmetic: with DevTestPath set, the workspace the jail actually
// runs must carry the authored test at the RELOCATED path — and the positive
// control must canary that same path. A fix that computed the right string
// but kept overlaying at the old sibling would pass the unit cases above and
// change nothing about the flask run.
func TestScoreAuthoredReport_OverlaysAtTheRelocatedPath(t *testing.T) {
	const codePath = "src/flask/cli.py"
	const devTest = "tests/test_cli.py"
	want := authoredTestPath(codePath, devTest, nil)

	jail := &wellBehavedJail{
		codePath: codePath,
		testPath: want, // the canary is only "seen" at the relocated path
		passOn:   map[string]bool{"COMPLIANT": true, "MUTANT": false},
	}
	s := JailScorer{
		Jail:        jail,
		BaseFiles:   map[string]string{"go.mod": "module x\n"},
		DevTestPath: devTest,
	}

	mutants := []adequacy.Mutant{{ID: "m1", Code: "MUTANT"}}
	rep, err := s.ScoreAuthoredReport(context.Background(), codePath, "COMPLIANT", "AUTHORED-TEST", mutants, "pytest")
	if err != nil {
		t.Fatalf("ScoreAuthoredReport: %v", err)
	}
	if !rep.CanaryKilled {
		t.Fatal("positive control did not react at the relocated path — the authored test is still being written somewhere the run never reads")
	}
	if len(rep.Killed) != 1 {
		t.Fatalf("genuine proven kill did not come through: %+v", rep)
	}

	ws := s.authoredWorkspace(codePath, "AUTHORED-TEST")
	if ws[want] != "AUTHORED-TEST" {
		t.Fatalf("authored test not overlaid at %q; keys=%v", want, wsKeys(ws))
	}
	if _, wrong := ws[advPoolTestPath(codePath)]; wrong {
		t.Fatalf("authored test still overlaid at the uncollected sibling %q", advPoolTestPath(codePath))
	}
}

func wsKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

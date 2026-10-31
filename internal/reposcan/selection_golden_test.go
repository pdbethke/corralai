// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
)

// The production path — evidence bytes into NewSelectionEvidence, then For —
// must answer every relative-path Select case exactly as Python's deleted
// selector did. internal/testmap's equivalence test holds the engine to the
// same golden, but it builds its own map; this one goes through the map
// constructor scans actually use (BuildSelectionMap, aligned with no repo
// root), so a constructor that drifted from the engine's inputs fails here.

func goldenTestdata(t *testing.T, parts ...string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join(parts...))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// renderGolden is internal/testmap's render: %#v, held as length and sha256
// past 2 KiB.
func renderGolden(v string) string {
	if len(v) <= 2048 {
		return v
	}
	sum := sha256.Sum256([]byte(v))
	return fmt.Sprintf("len=%d sha256=%x head=%s", len(v), sum, v[:200])
}

// goldenErrText strips the package prefix, as the golden's errors are stored.
func goldenErrText(s string) string {
	for _, p := range []string{"lang: ", "testmap: "} {
		s = strings.TrimPrefix(s, p)
	}
	return s
}

func TestProductionSelectionMatchesPythonGolden(t *testing.T) {
	b, err := os.ReadFile(goldenTestdata(t, "..", "testmap", "testdata", "python-selector-golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	var golden map[string]string
	if err := json.Unmarshal(b, &golden); err != nil {
		t.Fatal(err)
	}
	rec, err := os.ReadFile(goldenTestdata(t, "..", "lang", "testdata", "pycov-contexts.json"))
	if err != nil {
		t.Fatal(err)
	}
	fixture := goldenTestdata(t, "..", "lang", "testdata", "pycov-fixture")
	var ctxs []string
	for i := 0; i < 3000; i++ {
		ctxs = append(ctxs, fmt.Sprintf(`"tests/test_big.py::test_%04d_%s"`, i, strings.Repeat("x", 20)))
	}
	big := []byte(`{"format":"corral-selection-3","tests":3000,"files":{` +
		`"pkg/calc.py":{"tests":[` + strings.Join(ctxs, ",") + `],"lines":{"0":[[2,2]],"1":[[6,6]]},"static":[[1,1]]},` +
		`"tests/test_big.py":{"tests":[` + ctxs[0] + `],"lines":{},"static":[]}}}`)

	type selCase struct {
		name             string
		evidence         []byte
		root, code, test string
		cmd              []string
	}
	// Every case of internal/testmap's pySelectCases whose evidence stores
	// relative paths — all of them but "aligns absolute report paths",
	// pinned below to its own behaviour.
	cases := []selCase{
		{"picks only tests that executed the file", rec, "", "pkg/calc.py", "tests/test_calc.py", []string{"python3", "-m", "pytest", "-q"}},
		{"reads lines and static", rec, "", "pkg/calc.py", "tests/test_calc.py", []string{"pytest"}},
		{"absent file whose test ran is an error", rec, "", "pkg/untested.py", "tests/test_calc.py", []string{"pytest"}},
		{"present with zero tests is uncovered", rec, "", "pkg/__init__.py", "tests/test_calc.py", []string{"pytest"}},
		{"absent file and absent test", rec, "", "pkg/not_in_report.py", "tests/test_never_ran.py", []string{"pytest"}},
		{"absent file and no test path", rec, "", "pkg/not_in_report.py", "", []string{"pytest"}},
		{"collapses to files over the argv cap", big, "", "pkg/calc.py", "tests/test_big.py", []string{"pytest"}},
		{"uncovered with a directory target", rec, fixture, "pkg/__init__.py", "tests/test_calc.py", []string{"pytest", "tests/"}},
		{"old coverage-json refused", []byte(`{"meta":{"show_contexts":true},"totals":{"covered_lines":1},"files":{"pkg/calc.py":{"summary":{"num_statements":1,"covered_lines":1},"contexts":{"1":["tests/test_calc.py::test_add|run"]}}}}`), "", "pkg/calc.py", "tests/test_calc.py", []string{"pytest"}},
		{"non-coverage json refused", []byte(`{"hello":1}`), "", "pkg/calc.py", "tests/test_calc.py", []string{"pytest"}},
		{"v1 refused", []byte(`{"format":"corral-selection-1","tests":1,"files":{"pkg/calc.py":["tests/test_calc.py::test_add"]}}`), "", "pkg/calc.py", "tests/test_calc.py", []string{"pytest"}},
	}
	for _, argv := range [][]string{
		{"pytest", "tests/"},
		{"python3", "-m", "pytest", "-q", "-k", "not slow", "tests/"},
		{"pytest", "tests/test_calc.py::test_add"},
		{"pytest", "--maxfail", "3"},
	} {
		cases = append(cases, selCase{"strips targets " + strings.Join(argv, " "), rec, fixture, "pkg/calc.py", "tests/test_calc.py", argv})
	}
	if live := livePyFixtureEvidence(t, fixture); live != nil {
		cases = append(cases, selCase{"live fixture evidence", live, fixture, "pkg/calc.py", "tests/test_calc.py", []string{"python3", "-m", "pytest", "-q"}})
	}

	py, _ := lang.ByName("python")
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			want, ok := golden["Select/"+c.name]
			if !ok {
				t.Fatalf("the golden holds no Select/%s", c.name)
			}
			sel := NewSelectionEvidence(py, c.cmd, c.evidence).For(py, c.root, c.code, c.test, c.cmd)
			// Excluded, ExcludedWhy and Always postdate the golden and no
			// recorded case sets any: nil here, and left out of the text (see
			// testmap's TestEngineMatchesPythonSelector).
			if sel.Excluded != nil || sel.ExcludedWhy != nil || sel.Always != nil {
				t.Fatalf("Excluded = %v, ExcludedWhy = %v, Always = %v, but no recorded Python case sets any", sel.Excluded, sel.ExcludedWhy, sel.Always)
			}
			got := renderGolden(strings.Replace(fmt.Sprintf("%#v", sel), ", Always:[]string(nil), Excluded:[]string(nil), ExcludedWhy:map[string]string(nil)}", "}", 1))
			if sel.Fallback != "" {
				got = "error: " + goldenErrText(sel.Fallback)
			}
			if got != want {
				t.Errorf("production path:\n got    %s\n golden %s", got, want)
			}
		})
	}
}

// livePyFixtureEvidence is a real pytest-cov run of the fixture, nil (logged)
// when pytest-cov is absent.
func livePyFixtureEvidence(t *testing.T, fixture string) []byte {
	t.Helper()
	if out, err := exec.Command("python3", "-c", "import pytest_cov, coverage").CombinedOutput(); err != nil {
		t.Logf("live fixture case not run: pytest-cov not importable: %s", out)
		return nil
	}
	py, _ := lang.ByName("python")
	cmd, ok := py.(lang.TestEvidence).Instrument([]string{"python3", "-m", "pytest", "-q"})
	if !ok {
		t.Fatal("Instrument refused the fixture command")
	}
	c := exec.Command(cmd[0], cmd[1:]...)
	c.Dir = fixture
	c.Env = append(os.Environ(), "PYTHONPATH="+fixture, "PYTHONDONTWRITEBYTECODE=1")
	out, err := c.Output()
	if err != nil {
		t.Fatalf("instrumented run: %v\n%s", err, out)
	}
	return out
}

// A RULED divergence, pinned so it stays a decision: Python's Select aligned
// an absolute evidence path under the repo root, but the production map is
// aligned with no root (BuildSelectionMap's doc: the reducer only ever
// writes repo-relative paths). Evidence storing pkg/calc.py absolutely
// therefore never measured pkg/calc.py, and the file grades by the whole
// suite, disclosed with why — never as uncovered.
func TestProductionSelectionDropsAbsoluteEvidencePathsDisclosed(t *testing.T) {
	rec, err := os.ReadFile(goldenTestdata(t, "..", "lang", "testdata", "pycov-contexts.json"))
	if err != nil {
		t.Fatal(err)
	}
	abs := []byte(strings.ReplaceAll(string(rec), `"pkg/calc.py"`, `"/repo/pkg/calc.py"`))
	py, _ := lang.ByName("python")
	sel := NewSelectionEvidence(py, []string{"pytest"}, abs).For(py, "/repo", "pkg/calc.py", "tests/test_calc.py", []string{"pytest"})
	if sel.Method != "" || sel.Cmd != nil || len(sel.Tests) != 0 {
		t.Fatalf("selection = %+v, want the whole suite", sel)
	}
	for _, want := range []string{"never pkg/calc.py", `paired test "tests/test_calc.py" did run`, "falling back to the whole suite"} {
		if !strings.Contains(sel.Fallback, want) {
			t.Errorf("Fallback = %q, want it to say %q", sel.Fallback, want)
		}
	}
}

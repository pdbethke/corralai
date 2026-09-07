// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
)

// The four confirmed findings of review 28c4ae555cbc (a Claude Code
// reviewer on internal/reposcan, Codex verifying); R3 was refuted and is
// not here. R1 and R2 are the reviewer's reproductions, inverted; R4 and
// R5 have the tests they should have had.

// R1 — one test file grades exactly one source file, and an explicit
// --tests pairing is a CLAIMANT on that file: a convention-derived pairing
// that lands on the same test the operator mapped another source to is
// the accidental collision this pass exists to catch, and used to be left
// alone because explicit members were dropped before the groups formed.
func TestExplicitPairingDemotesTheConventionClaimantOnTheSameTest(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("pkg/a.py", "def a():\n    return 1\n")
	write("pkg/shared.py", "def shared():\n    return 2\n")
	write("tests/test_shared.py", "def test_shared():\n    assert True\n")
	mapPath := filepath.Join(t.TempDir(), "tests.json")
	b, _ := json.Marshal(map[string]string{"pkg/a.py": "tests/test_shared.py"})
	if err := os.WriteFile(mapPath, b, 0o600); err != nil {
		t.Fatal(err)
	}
	tm, err := NewFileTestMap(mapPath)
	if err != nil {
		t.Fatal(err)
	}
	cands, excl, err := EnumerateWithTests(root, tm)
	if err != nil {
		t.Fatal(err)
	}
	var graded []string
	for _, c := range cands {
		if c.TestPath == "tests/test_shared.py" {
			graded = append(graded, c.Path)
		}
	}
	if len(graded) != 1 || graded[0] != "pkg/a.py" {
		t.Fatalf("tests/test_shared.py must grade exactly the operator-mapped pkg/a.py, grades %v", graded)
	}
	demoted := false
	for _, e := range excl {
		if e.Path == "pkg/shared.py" && e.Reason == ReasonAmbiguousTest {
			demoted = true
		}
	}
	if !demoted {
		t.Fatalf("the convention pairing pkg/shared.py -> tests/test_shared.py was not demoted as %s: %+v", ReasonAmbiguousTest, excl)
	}
	// Two explicit sources on one suite is a choice, not a collision: both stay.
	b, _ = json.Marshal(map[string]string{"pkg/a.py": "tests/test_shared.py", "pkg/shared.py": "tests/test_shared.py"})
	if err := os.WriteFile(mapPath, b, 0o600); err != nil {
		t.Fatal(err)
	}
	tm, _ = NewFileTestMap(mapPath)
	cands, excl, _ = EnumerateWithTests(root, tm)
	graded = graded[:0]
	for _, c := range cands {
		if c.TestPath == "tests/test_shared.py" {
			graded = append(graded, c.Path)
		}
	}
	if len(graded) != 2 {
		t.Fatalf("two operator-mapped sources on one suite must both stay: %v (excluded %+v)", graded, excl)
	}
}

// R2 — CanonicalKV is injective: a name or value carrying the form's own
// delimiters cannot render the same bytes as a different map, so
// KeyInputs.CacheKey never serves one herd's verdict for another.
func TestCanonicalKVIsInjective(t *testing.T) {
	forged := map[string]string{
		"critic":           "gemini-3.6-flash",
		"mutant-generator": "gemini-3.6-pro",
		"shadow":           "off",
		"test-writer":      "claude-sonnet-5,test-writer-shadow=claude-opus-5",
	}
	real := map[string]string{
		"critic":             "gemini-3.6-flash",
		"mutant-generator":   "gemini-3.6-pro",
		"shadow":             "off",
		"test-writer":        "claude-sonnet-5",
		"test-writer-shadow": "claude-opus-5",
	}
	a, b := CanonicalKV(forged), CanonicalKV(real)
	if a == b {
		t.Fatalf("two different herds render one ModelSet %q", a)
	}
	mk := func(ms string) string {
		return KeyInputs{
			SourceDigest: "src", PackageDigest: "pkg", GoalDigest: "goal",
			TestSurfaceDigest: "surf", EngineVersion: VerdictGeneration,
			ModelSet: ms, AuditConfig: "", Substrate: SubstrateJail,
		}.CacheKey()
	}
	if mk(a) == mk(b) {
		t.Fatalf("cache keys collide across different herds")
	}
	// Escaping, not length-prefixing: every string that never carried a
	// delimiter renders exactly as before, so nothing already cached or
	// recorded changes form.
	if got := CanonicalKV(real); got != "critic=gemini-3.6-flash,mutant-generator=gemini-3.6-pro,shadow=off,test-writer=claude-sonnet-5,test-writer-shadow=claude-opus-5" {
		t.Fatalf("a delimiter-free map must render as it always did: %q", got)
	}
	// And a few adversarial shapes stay apart.
	seen := map[string]map[string]string{}
	for _, m := range []map[string]string{
		{"a": "b,c=d"}, {"a": "b", "c": "d"}, {"a,c": "b=d"}, {"a": `b\`, "c": "d"}, {"a": `b\,c=d`}, {`a\`: "b"}, {"a": `\b`},
	} {
		s := CanonicalKV(m)
		if prev, dup := seen[s]; dup {
			t.Errorf("%v and %v render the same: %q", prev, m, s)
		}
		seen[s] = m
	}
}

// R4 — the most-covering test FILE is the file whose tests together
// executed the most lines, not the file of the single biggest test.
func TestMostCoveringTestFileSumsPerFile(t *testing.T) {
	sel := fakeSelector{index: map[string]lang.FileCoverage{
		"pkg/a.py": {Tests: map[string]int{
			"tests/test_big.py::test_one":   10, // one test, 10 lines
			"tests/test_many.py::test_a":    6,  // four tests, 24 lines
			"tests/test_many.py::test_b":    6,
			"tests/test_many.py::test_c":    6,
			"tests/test_many.py::test_d":    6,
			"tests/test_none.py::test_zero": 0,
		}, HasStatic: true, HasStatements: true},
		"pkg/tie.py": {Tests: map[string]int{
			"tests/x_test.py::t":      5,
			"tests/deep/y_test.py::t": 5,
		}, HasStatic: true, HasStatements: true},
	}}
	idx, ok := ParseEvidenceIndex(SelectionEvidence{Ran: true, Raw: []byte("x")}, sel)
	if !ok {
		t.Fatal("index")
	}
	n, most, _, _, measured := idx.CoverageFor("pkg/a.py")
	if !measured || n != 6 || most != "tests/test_many.py" {
		t.Fatalf("want tests/test_many.py (24 lines over 4 tests) over tests/test_big.py (10 in one): got %q, n=%d", most, n)
	}
	// A tie between files breaks deterministically: the more specific path.
	_, most, _, _, _ = idx.CoverageFor("pkg/tie.py")
	if most != "tests/deep/y_test.py" {
		t.Fatalf("tie must break to the more specific path, got %q", most)
	}
}

// R5 — churn is counted for a path git would C-quote: without -z, a
// non-ASCII path never matched its own history and ranked as churn 1
// under a RankInfo that still said "churn-x-size".
func TestChurnCountsAPathGitWouldQuote(t *testing.T) {
	root := t.TempDir()
	gitRun(t, root, "init", "-q")
	odd := "pkg/héllo wörld.go"
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{odd, "pkg/plain.go"} {
		if err := os.WriteFile(filepath.Join(root, rel), []byte("package pkg\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gitRun(t, root, "add", ".")
	gitRun(t, root, "commit", "-q", "-m", "base", "--no-gpg-sign")
	for i := 0; i < 3; i++ {
		touchCommit(t, root, odd)
	}
	churn, info := fileChurn(root)
	if info.Signal != "churn-x-size" {
		t.Fatalf("history is present; signal %q: %s", info.Signal, info.Note)
	}
	if churn[odd] != 4 {
		keys := make([]string, 0, len(churn))
		for k := range churn {
			keys = append(keys, k)
		}
		t.Fatalf("churn for %q = %d, want 4; recorded paths: %s", odd, churn[odd], strings.Join(keys, " | "))
	}
	if churn["pkg/plain.go"] != 1 {
		t.Fatalf("plain path churn = %d, want 1", churn["pkg/plain.go"])
	}
}

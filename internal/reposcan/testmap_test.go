// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"os"
	"path/filepath"
	"testing"
)

// Pairing is convention-based: a source file is paired with the first
// conventionally-named test that exists. That works when a project names tests
// after source files, and CANNOT work when it does not.
//
// expressjs/express is the clean example, and it is why the CI sweep pins it at
// ZERO candidates rather than inviting someone to "fix" pairing into false
// positives:
//
//	lib/application.js -> test/app.js, app.all.js, app.engine.js, app.param.js …
//	lib/response.js    -> test/res.send.js, res.json.js …
//
// `application -> app` and `response -> res` is a project's own shorthand. No
// filename heuristic derives it, and a heuristic loose enough to try would pair
// the wrong files — which plants mutants in one file and grades them against
// another's tests, producing a confident, signed, WRONG verdict.
//
// psf/requests is the subtler case: adapters.py DOES pair, to an 8-line
// test_adapters.py, while its real coverage lives in a 108KB test_requests.py.
// Convention found a file; it found the wrong one.
//
// So the tenant supplies the mapping. They know; corral cannot.
func writeTestMap(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "tests.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

func TestFileTestMap_OverridesConvention(t *testing.T) {
	tm, err := NewFileTestMap(writeTestMap(t, `{"src/requests/adapters.py":"tests/test_requests.py"}`))
	if err != nil {
		t.Fatalf("NewFileTestMap: %v", err)
	}
	got, ok := tm.TestFor("src/requests/adapters.py")
	if !ok || got != "tests/test_requests.py" {
		t.Fatalf("TestFor = (%q,%v), want the tenant's mapping", got, ok)
	}
	if _, ok := tm.TestFor("src/requests/help.py"); ok {
		t.Error("an unmapped path must fall through to convention, not be blocked")
	}
}

// TestFileTestMap_EmptyValueIsNotAMapping pins that a blank entry does not
// become a pairing to nowhere. Silently pairing a file to "" would grade it
// against an empty command — green on the baseline AND green on every mutant,
// which is a confident 0.00 kill rate that is not an error anywhere.
func TestFileTestMap_EmptyValueIsNotAMapping(t *testing.T) {
	tm, err := NewFileTestMap(writeTestMap(t, `{"a.py":"", "b.py":"   "}`))
	if err != nil {
		t.Fatalf("NewFileTestMap: %v", err)
	}
	for _, p := range []string{"a.py", "b.py"} {
		if _, ok := tm.TestFor(p); ok {
			t.Errorf("%s: an empty mapping must not pair", p)
		}
	}
}

// TestEnumerateWithTestMap_PairsTheUnpairable is the express case: source files
// a convention could never match, paired because the tenant said so.
func TestEnumerateWithTestMap_PairsTheUnpairable(t *testing.T) {
	root := t.TempDir()
	mk := func(rel, body string) {
		t.Helper()
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mk("lib/response.js", "function send(){ return 1 }\n")
	mk("lib/application.js", "function listen(){ return 1 }\n")
	mk("test/res.send.js", "// behaviour-named test\n")
	mk("test/app.listen.js", "// behaviour-named test\n")

	// Without a map: convention finds nothing, exactly as it does on express.
	cands, _, err := Enumerate(root)
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	if len(cands) != 0 {
		t.Fatalf("expected 0 candidates by convention, got %d (%+v)", len(cands), cands)
	}

	tm, err := NewFileTestMap(writeTestMap(t, `{
		"lib/response.js":    "test/res.send.js",
		"lib/application.js": "test/app.listen.js"
	}`))
	if err != nil {
		t.Fatalf("NewFileTestMap: %v", err)
	}
	cands, _, err = EnumerateWithTests(root, tm)
	if err != nil {
		t.Fatalf("EnumerateWithTests: %v", err)
	}
	if len(cands) != 2 {
		t.Fatalf("got %d candidates, want 2 — the tenant's mapping must pair what convention cannot: %+v", len(cands), cands)
	}
	for _, c := range cands {
		if c.TestPath == "" {
			t.Errorf("%s paired to nothing", c.Path)
		}
	}
}

// TestEnumerateWithTestMap_ManySourcesOneTestSurvivesAmbiguity is the property
// that makes the map usable on real repos. demoteAmbiguousPairings exists to
// catch ACCIDENTAL collisions from convention — two sources a heuristic happened
// to point at one test. A tenant mapping several sources onto the same suite
// (express: every lib file to test/) is DELIBERATE, and demoting it would throw
// away exactly the pairings the operator supplied on purpose.
func TestEnumerateWithTestMap_ManySourcesOneTestSurvivesAmbiguity(t *testing.T) {
	root := t.TempDir()
	mk := func(rel, body string) {
		t.Helper()
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mk("lib/a.js", "function a(){}\n")
	mk("lib/b.js", "function b(){}\n")
	mk("test/all.js", "// one suite for both\n")

	tm, err := NewFileTestMap(writeTestMap(t, `{"lib/a.js":"test/all.js","lib/b.js":"test/all.js"}`))
	if err != nil {
		t.Fatalf("NewFileTestMap: %v", err)
	}
	cands, excl, err := EnumerateWithTests(root, tm)
	if err != nil {
		t.Fatalf("EnumerateWithTests: %v", err)
	}
	if len(cands) != 2 {
		t.Fatalf("got %d candidates, want 2 — an explicit many-to-one mapping is deliberate, not an ambiguous collision: %+v (excluded: %+v)", len(cands), cands, excl)
	}
}

// TestEnumerateWithTestMap_MissingTargetIsRefused pins that a typo does not
// silently fall back to convention. Falling back would pair the file to
// something the tenant never chose, and they would have no way to see that
// their mapping was ignored.
func TestEnumerateWithTestMap_MissingTargetIsRefused(t *testing.T) {
	root := t.TempDir()
	full := filepath.Join(root, "lib/a.js")
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("function a(){}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tm, err := NewFileTestMap(writeTestMap(t, `{"lib/a.js":"test/does-not-exist.js"}`))
	if err != nil {
		t.Fatalf("NewFileTestMap: %v", err)
	}
	cands, excl, err := EnumerateWithTests(root, tm)
	if err != nil {
		t.Fatalf("EnumerateWithTests: %v", err)
	}
	if len(cands) != 0 {
		t.Fatalf("a mapping to a nonexistent file must not become a candidate: %+v", cands)
	}
	var found bool
	for _, e := range excl {
		if e.Path == "lib/a.js" && e.Reason == ReasonMappedTestMissing {
			found = true
		}
	}
	if !found {
		t.Errorf("want an explicit %q exclusion naming the file, got %+v", ReasonMappedTestMissing, excl)
	}
}

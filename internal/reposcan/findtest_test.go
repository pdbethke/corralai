// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
)

// TestEnumeratePairsItsdangerousShape is the motivating case: a src-layout
// package (src/itsdangerous/signer.py) whose real test lives one directory
// level deeper than any convention-derived mirror
// (tests/test_itsdangerous/test_signer.py — the directory itself carries a
// test_ prefix). Before the recursive fallback existed, corral's own
// TestPaths candidates all missed and the file was excluded as
// no-paired-test — the #1 stumble both `--repo` and `--local` hit on the
// real pallets/itsdangerous repo.
func TestEnumeratePairsItsdangerousShape(t *testing.T) {
	root := writeTree(t, map[string]string{
		"src/itsdangerous/signer.py":             "class Signer: pass\n",
		"tests/test_itsdangerous/test_signer.py": "def test_x(): pass\n",
	})

	cands, excl, err := Enumerate(root)
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	if len(cands) != 1 {
		t.Fatalf("candidates = %+v, excl = %+v, want exactly 1 candidate", cands, excl)
	}
	c := cands[0]
	if c.Path != "src/itsdangerous/signer.py" {
		t.Errorf("Path = %q, want src/itsdangerous/signer.py", c.Path)
	}
	if c.TestPath != "tests/test_itsdangerous/test_signer.py" {
		t.Errorf("TestPath = %q, want tests/test_itsdangerous/test_signer.py", c.TestPath)
	}
	if !c.ViaSearch {
		t.Error("ViaSearch = false, want true — this pairing only exists because of the recursive fallback")
	}
}

// TestFindTestItsdangerousShape is the same fixture through FindTest
// directly — the seam certify_local.go's --test default now uses.
func TestFindTestItsdangerousShape(t *testing.T) {
	root := writeTree(t, map[string]string{
		"src/itsdangerous/signer.py":             "class Signer: pass\n",
		"tests/test_itsdangerous/test_signer.py": "def test_x(): pass\n",
	})
	p, ok := lang.ByName("python")
	if !ok {
		t.Fatal("python plugin not registered")
	}

	res, err := FindTest(p, root, "src/itsdangerous/signer.py")
	if err != nil {
		t.Fatalf("FindTest: %v", err)
	}
	if !res.Found {
		t.Fatalf("Found = false, Tried = %v, Roots = %v — want a hit via search", res.Tried, res.Roots)
	}
	if res.Path != "tests/test_itsdangerous/test_signer.py" {
		t.Errorf("Path = %q, want tests/test_itsdangerous/test_signer.py", res.Path)
	}
	if !res.ViaSearch {
		t.Error("ViaSearch = false, want true")
	}
	if len(res.Tried) == 0 {
		t.Error("Tried is empty — the convention candidates should still be recorded even though none existed")
	}
}

// TestFindTestPrefersSiblingOverNested pins the binding constraint that
// nothing measured changes for an already-pairable file: when BOTH a sibling
// (convention list) and a plausible nested/search match exist, the sibling
// — found by the existing, unmodified TestPaths priority — wins, and the
// recursive fallback never even runs.
func TestFindTestPrefersSiblingOverNested(t *testing.T) {
	root := writeTree(t, map[string]string{
		"pkg/thing.py":             "x = 1\n",
		"pkg/test_thing.py":        "def test_sibling(): pass\n", // rank 0: sibling
		"tests/pkg/test_thing.py":  "def test_mirror(): pass\n",  // would ALSO satisfy TestPaths' own rank-1 mirror form
		"tests/deep/test_thing.py": "def test_deep(): pass\n",    // would only ever be found by the recursive fallback
	})
	p, ok := lang.ByName("python")
	if !ok {
		t.Fatal("python plugin not registered")
	}

	res, err := FindTest(p, root, "pkg/thing.py")
	if err != nil {
		t.Fatalf("FindTest: %v", err)
	}
	if !res.Found || res.Path != "pkg/test_thing.py" {
		t.Fatalf("Path = %q, Found = %v, want pkg/test_thing.py (the sibling)", res.Path, res.Found)
	}
	if res.ViaSearch {
		t.Error("ViaSearch = true, want false — the sibling convention candidate exists and must win outright")
	}

	// Enumerate must agree: the same fixture, pairing through the whole-repo
	// path, picks the identical sibling and never marks it ViaSearch.
	cands, _, err := Enumerate(root)
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	var got *Candidate
	for i := range cands {
		if cands[i].Path == "pkg/thing.py" {
			got = &cands[i]
		}
	}
	if got == nil {
		t.Fatalf("pkg/thing.py not found among candidates: %+v", cands)
	}
	if got.TestPath != "pkg/test_thing.py" || got.ViaSearch {
		t.Errorf("Candidate = %+v, want TestPath=pkg/test_thing.py ViaSearch=false", *got)
	}
}

// TestEnumerateConventionalPairingUnchanged pins the byte-identical
// constraint directly: a plain, conventionally-mirrored fixture (no
// itsdangerous-shaped gap) must resolve to EXACTLY the same TestPath, with
// ViaSearch false, as it did before the recursive fallback existed.
func TestEnumerateConventionalPairingUnchanged(t *testing.T) {
	root := writeTree(t, map[string]string{
		"aisuite/agents/artifact_store.py":    "x = 1\n",
		"tests/agents/test_artifact_store.py": "def test_x(): pass\n",
	})
	cands, _, err := Enumerate(root)
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	if len(cands) != 1 {
		t.Fatalf("candidates = %+v, want exactly 1", cands)
	}
	c := cands[0]
	if c.TestPath != "tests/agents/test_artifact_store.py" {
		t.Errorf("TestPath = %q, want tests/agents/test_artifact_store.py (byte-identical to the pre-existing convention match)", c.TestPath)
	}
	if c.ViaSearch {
		t.Error("ViaSearch = true, want false — this file was always pairable by convention alone")
	}
}

// TestFindTestNeverSearchesGitignoredDirs pins the bounded-search
// constraint: a gitignored copy under a searched root must never be found,
// exactly like Enumerate's own gitignore rule for the convention list.
func TestFindTestNeverSearchesGitignoredDirs(t *testing.T) {
	root := writeTree(t, map[string]string{
		".gitignore":                         "tests/.worktrees/\n",
		"src/itsdangerous/signer.py":         "class Signer: pass\n",
		"tests/.worktrees/wt/test_signer.py": "def test_x(): pass\n",
	})
	gitInit(t, root)
	p, ok := lang.ByName("python")
	if !ok {
		t.Fatal("python plugin not registered")
	}

	res, err := FindTest(p, root, "src/itsdangerous/signer.py")
	if err != nil {
		t.Fatalf("FindTest: %v", err)
	}
	if res.Found {
		t.Errorf("Found a test at %q inside a gitignored directory — must never be walked", res.Path)
	}

	// Same fixture through Enumerate's own path (present-map-based search):
	// the gitignored copy must be excluded, never selected as the pairing.
	cands, excl, err := Enumerate(root)
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	for _, c := range cands {
		if c.Path == "src/itsdangerous/signer.py" {
			t.Errorf("src/itsdangerous/signer.py paired to %q via a gitignored directory", c.TestPath)
		}
	}
	reasons := reasonsByPath(excl)
	if reasons["src/itsdangerous/signer.py"] != ReasonNoPairedTest {
		t.Errorf("reason = %q, want %q (nothing outside the gitignored dir exists to pair with)",
			reasons["src/itsdangerous/signer.py"], ReasonNoPairedTest)
	}
}

// TestFindTestMostSpecificWinsOnAmbiguousSearchHits pins the "most-specific
// match wins" tie-breaker: two files share the search basename, and the one
// whose directory echoes more of codePath's own directory must be chosen.
func TestFindTestMostSpecificWinsOnAmbiguousSearchHits(t *testing.T) {
	root := writeTree(t, map[string]string{
		"src/widgets/thing.py":        "x = 1\n",
		"tests/other/test_thing.py":   "def test_other(): pass\n",   // shares nothing with widgets
		"tests/widgets/test_thing.py": "def test_widgets(): pass\n", // shares "widgets" with codePath's dir
	})
	p, ok := lang.ByName("python")
	if !ok {
		t.Fatal("python plugin not registered")
	}

	res, err := FindTest(p, root, "src/widgets/thing.py")
	if err != nil {
		t.Fatalf("FindTest: %v", err)
	}
	if !res.Found || res.Path != "tests/widgets/test_thing.py" {
		t.Errorf("Path = %q, Found = %v, want tests/widgets/test_thing.py (the more specific match)", res.Path, res.Found)
	}
}

// TestFindTestReportsTriedAndRootsOnMiss pins the "the error must make the
// third guess unnecessary" requirement: a total miss still names every
// convention candidate that was checked and every root that was searched.
func TestFindTestReportsTriedAndRootsOnMiss(t *testing.T) {
	root := writeTree(t, map[string]string{
		"src/itsdangerous/signer.py": "class Signer: pass\n",
	})
	p, ok := lang.ByName("python")
	if !ok {
		t.Fatal("python plugin not registered")
	}

	res, err := FindTest(p, root, "src/itsdangerous/signer.py")
	if err != nil {
		t.Fatalf("FindTest: %v", err)
	}
	if res.Found {
		t.Fatalf("Found = true (%q), want false — no test exists anywhere in this fixture", res.Path)
	}
	if len(res.Tried) == 0 {
		t.Error("Tried is empty on a miss — the operator needs to see what was already ruled out")
	}
	if len(res.Roots) == 0 {
		t.Error("Roots is empty on a miss — the operator needs to see where the search looked")
	}
}

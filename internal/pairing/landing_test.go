// SPDX-License-Identifier: Elastic-2.0

package pairing

import (
	"errors"
	"slices"
	"testing"
)

// rustLikeRules are the Rust plugin's content rules, restated here because
// pairing cannot import lang; the registry-derived assertion that the real
// plugin lands the same way lives in lang's landing_test.go.
var rustLikeRules = []Rule{
	{Shape: Declared, Rank: 0},
	{Shape: Inline, Rank: 0},
	{Shape: TestDir, Dir: "tests"},
}

// memSources is a Sources over an in-memory tree: files is the set that
// exists, mods what each source declares.
func memSources(root string, files []string, mods map[string][]TestModule) Sources {
	return Sources{
		Root:        root,
		Exists:      func(rel string) bool { return slices.Contains(files, rel) },
		Universe:    func() ([]string, error) { return files, nil },
		TestModules: func(rel string) ([]TestModule, error) { return mods[rel], nil },
	}
}

// TestLandIsDerivedFromWhatTheSourceDeclares: the landing is the file the
// source's own declaration loads (composed into, one module below the
// source), the source itself for an inline module or none, and a path
// language's Primary name (a file of its own) untouched by any of it.
func TestLandIsDerivedFromWhatTheSourceDeclares(t *testing.T) {
	declared := []TestModule{{Name: "tests", PathAttr: "foo_tests.rs", CfgTest: true}}
	inline := []TestModule{{Name: "tests", Inline: true, CfgTest: true}}
	src := memSources("", []string{"src/foo.rs", "src/foo_tests.rs", "src/bar.rs", "src/baz.rs"},
		map[string][]TestModule{"src/foo.rs": declared, "src/bar.rs": inline})

	for _, tc := range []struct {
		code  string
		want  Landing
		about string
	}{
		{"src/foo.rs", Landing{Path: "src/foo_tests.rs", Shape: Declared, Label: "path-attr", Into: true, Depth: 1, DeclarationsRead: true}, "declared: the paired test file"},
		{"src/bar.rs", Landing{Path: "src/bar.rs", Shape: Inline, Label: "inline", Into: true, Depth: 0, DeclarationsRead: true}, "inline: the source itself"},
		{"src/baz.rs", Landing{Path: "src/baz.rs", Shape: Inline, Label: "inline", Into: true, Depth: 0, DeclarationsRead: true}, "no module: Primary, the source"},
	} {
		got, err := Land(rustLikeRules, tc.code, src)
		if err != nil {
			t.Fatalf("%s: %v", tc.code, err)
		}
		if !landingEqual(got, tc.want) {
			t.Errorf("%s (%s): Land = %+v, want %+v", tc.code, tc.about, got, tc.want)
		}
	}

	// A path language: Primary's name, a file of its own, and no read of
	// the source at all.
	py := []Rule{{Shape: Sibling, Name: "test_{base}.py"}}
	read := false
	got, err := Land(py, "pkg/mod.py", Sources{TestModules: func(string) ([]TestModule, error) { read = true; return nil, nil }})
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != "pkg/test_mod.py" || got.Into || got.Shape != Sibling || read {
		t.Errorf("python Land = %+v (read=%v), want pkg/test_mod.py, a new file, no read", got, read)
	}
}

// TestLandWithoutDeclarationsFailsClosedToPrimary: an entry that cannot read
// what the source declares (no Sources.TestModules — the brain, single-file
// mode) is never guessed at: the landing is Primary, which for a content
// language is the source itself, and DeclarationsRead says why.
func TestLandWithoutDeclarationsFailsClosedToPrimary(t *testing.T) {
	got, err := Land(rustLikeRules, "src/foo.rs", Sources{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != "src/foo.rs" || !got.Into || got.DeclarationsRead {
		t.Errorf("Land with no Sources = %+v, want the source itself, unread", got)
	}
}

// TestLandRefusesADeclarationOutsideTheRoot: a #[path] that leaves the root
// is never a landing (the same confinement Resolve applies — it is the same
// code), and the refused path is reported so the refusal can name it.
func TestLandRefusesADeclarationOutsideTheRoot(t *testing.T) {
	root := t.TempDir()
	mods := map[string][]TestModule{"src/foo.rs": {{Name: "tests", PathAttr: "../../outside_tests.rs", CfgTest: true}}}
	src := memSources(root, []string{"src/foo.rs", "../outside_tests.rs"}, mods)
	got, err := Land(rustLikeRules, "src/foo.rs", src)
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != "src/foo.rs" || !slices.Equal(got.RefusedOutsideRoot, []string{"../outside_tests.rs"}) {
		t.Errorf("Land = %+v, want the source (refused) with ../outside_tests.rs reported", got)
	}
}

// TestLandAndResolveAgree: the landing of a declared source IS the test
// Resolve pairs it to — one derivation, so the file a run grades with and
// the file it writes into cannot drift apart.
func TestLandAndResolveAgree(t *testing.T) {
	mods := map[string][]TestModule{"src/a/mod.rs": {{Name: "tests", CfgTest: true}}}
	src := memSources("", []string{"src/a/mod.rs", "src/a/tests.rs"}, mods)
	res, err := Resolve(rustLikeRules, "src/a/mod.rs", src)
	if err != nil {
		t.Fatal(err)
	}
	l, err := Land(rustLikeRules, "src/a/mod.rs", src)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Found || l.Path != res.Path || l.Label != res.Label || l.Shape != res.Shape {
		t.Errorf("Resolve = %+v, Land = %+v: they must name the same file", res, l)
	}
}

// TestLandPropagatesAReadError: "could not read the file" is never "the file
// declares no test" — the same rule Resolve keeps.
func TestLandPropagatesAReadError(t *testing.T) {
	boom := errors.New("boom")
	_, err := Land(rustLikeRules, "src/foo.rs", Sources{
		Exists:      func(string) bool { return false },
		TestModules: func(string) ([]TestModule, error) { return nil, boom },
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the read error", err)
	}
}

func landingEqual(a, b Landing) bool {
	return a.Path == b.Path && a.Shape == b.Shape && a.Label == b.Label && a.Into == b.Into &&
		a.Depth == b.Depth && a.DeclarationsRead == b.DeclarationsRead && slices.Equal(a.RefusedOutsideRoot, b.RefusedOutsideRoot)
}

// TestLandSaysWhenTheBuildCannotReadDeclarations: a reader that cannot read
// declarations at all (a no-cgo build) is not "declares none": the landing
// falls closed to Primary and says which refusal it is.
func TestLandSaysWhenTheBuildCannotReadDeclarations(t *testing.T) {
	got, err := Land(rustLikeRules, "src/foo.rs", Sources{
		Exists:      func(string) bool { return true },
		TestModules: func(string) ([]TestModule, error) { return nil, ErrDeclarationsUnreadable },
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != "src/foo.rs" || !got.DeclarationsUnreadable || got.DeclarationsRead {
		t.Errorf("Land = %+v, want the source, unreadable, not read", got)
	}
}

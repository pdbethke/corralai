// SPDX-License-Identifier: Elastic-2.0

// Package pairing decides, from each language's declared rules, where a
// source file's test may live (Candidates), which directories a search for one
// walks (Roots), and whether a file is a test (IsTest, strict) or might be one
// (MightBeTest, generous).
//
// It used to be six hand-written TestPaths (one per language plugin), three
// TestRoots restating their directories, and two is-test inverses
// (reposcan.isTestFile, cmd/corral looksLikeATestPath) that had already drifted
// apart. A language now DECLARES its conventions as []Rule — data, not code —
// and everything above is derived from that one declaration (DRY audit
// 2026-10-08, Round B1). A new language adds rules, or a new Shape in
// shapeTable; it never adds a pairing function.
//
// It is the one place a source's test is RESOLVED by convention or search,
// and the one place those conventions are declared: the candidates, the
// search roots, both test questions, and the two-stage resolution (Resolve:
// candidates that exist on disk first, then the search over Roots scored by
// shared path context, ranked by SearchRank). That algorithm used to be
// written twice in internal/reposcan, as FindTest over a fresh git listing
// and findInUniverse over Enumerate's universe; it is one function here, fed
// by Sources{Exists, Universe, TestModules}, and those two are now I/O
// adapters that supply only the disk, the listing and the file's declared
// test modules (gitVisibleFiles, walkSkippingBuildDirs, pathUnderSkippedDir,
// FindTest's os.Stat probe, reposcan's diskTestModules).
//
// Most Shapes are PATH shapes: where a test sits follows from the source's
// path alone. Rust added CONTENT shapes (Inline, Declared), whose answer is
// what the source declares — `mod tests { }` in the file, or
// `#[path = "x_tests.rs"] mod tests;` naming another — because in Rust that
// declaration, not a file name, is what makes a file a module's test. Each
// is still one shapeTable entry; pairing still does no parsing (the
// declarations arrive as TestModule values, read by repoindex).
//
// Every caller asks pairing; none makes its own choice from pairing's data.
// sourcesOrphanedByDeletedTests (cmd/corral/certify_repo.go) used to read
// Candidates directly as a membership check, which was blind to every
// content-derived path (a deleted Rust #[path] target orphaned its source
// silently); it now reads Resolve's own Tried, through
// reposcan.ResolutionPaths. It is still a membership check over what a
// resolution tries, not over the search: a test that was paired by search
// rather than by convention or declaration and is then deleted is therefore
// not reported as orphaning its source.
// The three questions have one function each: Primary names a test corral is
// about to write (advpool's synthetic and authored tests), Resolve finds the
// test that already exists (reposcan.FindTest and findInUniverse, and through
// FindTest both certify --local and certify --adversarial), and BestClaim
// settles which of several sources resolving to one test keeps it
// (reposcan.demoteAmbiguousPairings, which owns only the grouping and the
// explicit-pairing exemption). Round B1c made that true; before it, three
// callers indexed Candidates(...)[0] or compared Ranks themselves.
//
// The directory affixes the search normalizes away (testDirAffixes: prefixes
// test_ and spec_, suffixes _test and _spec) are DECLARED, not derived from
// Generic. An earlier plan derived them; it was dropped on purpose. Generic
// holds FILE-name markers (with {ext}, dotted .test/.spec, and Ruby's spec_
// now living in Ruby's rules), so deriving directory affixes from them would
// either drop spec_ or start normalizing directories like foo.test and fooTest
// — a behaviour change the corpus golden cannot see. A directory affix is a
// different convention ("this directory mirrors a source directory"), not a
// restatement of the file one, so it is its own declaration.
package pairing

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// Shape is where a rule's test file sits relative to its source.
type Shape string

const (
	Sibling      Shape = "sibling"       // <dir>/<Name>
	BesideDir    Shape = "beside-dir"    // <dir>/<Dir>/<Name>; Dir is also a search root
	ParallelTree Shape = "parallel-tree" // <Dir>/<sub>/<Name> (or <Dir>/<dir>/<Name> with KeepLeading)
	FlatRoot     Shape = "flat-root"     // <Dir>/<Name>, only within MaxDepth
	Recognize    Shape = "recognize"     // never a candidate or a root; read by IsTest and MightBeTest only
	Inline       Shape = "inline"        // the source itself, when it declares an inline test module (content)
	Declared     Shape = "declared"      // the file the source's own `mod` declaration loads (content; no pure candidate)
	TestDir      Shape = "test-dir"      // any file under a <Dir> path segment IS a test; Dir is also a search root
)

// shapeTraits is everything a Shape means to the readers of a Rule. Each
// field is one question a reader asks, so a reader never switches on a Shape
// itself — it asks the table.
type shapeTraits struct {
	// candidate builds the rule's test path from the source's directory and
	// the rule's Name with {base} filled in (src is the source path itself,
	// for the one Shape whose candidate it is); ok=false means "not for this
	// source" (FlatRoot past MaxDepth). nil means the Shape never produces a
	// candidate (Recognize, Declared, TestDir). Read by Candidates.
	candidate func(r Rule, src, dir, name string) (p string, ok bool)
	// searchRoot: the rule's Dir is a top-level directory the stage-2 search
	// walks. Read by Roots.
	searchRoot bool
	// testDir: the rule's Dir names a directory whose files MIGHT be tests.
	// Read by MightBeTest.
	testDir bool
	// nameMarksTest: the rule's Name is a "this file IS a test" pattern (see
	// Rule's marker invariant). Read by IsTest and MightBeTest.
	nameMarksTest bool
	// sameDir: a candidate of this Shape sits in the source's OWN directory —
	// the strongest evidence a pairing can carry. True for Sibling only
	// (TestOnlySiblingIsSameDir). Read by Dedupe.
	sameDir bool
	// content: the Shape is answered by what the SOURCE declares about its
	// own tests (Sources.TestModules), not by a path alone. Resolve tries a
	// content rule before every pure candidate, and refuses to run without
	// a TestModules reader rather than skip the rule. Read by Resolve.
	content bool
	// fromModules derives a content Shape's test paths from the modules the
	// source declares, each with the Label it resolves under, in the order
	// to try them; nil for a non-content Shape. Read by Resolve.
	fromModules func(src string, mods []TestModule) []labelled
	// dirMarksTest: a file with a path segment equal to the rule's Dir IS a
	// test, whatever its name — cargo's tests/ holds integration tests named
	// like any source (tests/parser.rs). Name is not read for such a rule,
	// which is how it keeps Rule's marker invariant. Read by IsTest (through
	// DirMarksTest).
	dirMarksTest bool
	// confined: the Shape's paths are named by the SOURCE (a repository-
	// controlled declaration), not derived by corral, so each must stay
	// inside Sources.Root or it is refused (insideRoot). Read by Resolve.
	confined bool
}

// labelled is one path a content Shape derived, and the Label a resolution
// through it carries ("inline", "path-attr", "default-module").
type labelled struct{ path, label string }

// shapeTable is the ONE place a Shape's semantics live. They used to be
// spread across two switches (Candidates, Roots) plus MightBeTest reading Dir
// from every rule, and Candidates' switch silently skipped a Shape it did not
// know — a rule applied at one door and not the others (Round B1 final
// review). A new Shape is now one entry here; TestEveryShapeHasTraits fails
// for a declared Shape without one, and traitsOf panics on an undeclared one.
var shapeTable = map[Shape]shapeTraits{
	Sibling: {
		candidate:     func(_ Rule, _, dir, name string) (string, bool) { return joinDir(dir, name), true },
		nameMarksTest: true,
		sameDir:       true,
	},
	BesideDir: {
		candidate:     func(r Rule, _, dir, name string) (string, bool) { return filepath.Join(dir, r.Dir, name), true },
		searchRoot:    true,
		testDir:       true,
		nameMarksTest: true,
	},
	ParallelTree: {
		candidate: func(r Rule, _, dir, name string) (string, bool) {
			sub := stripFirstSegment(dir)
			if r.KeepLeading {
				sub = dir
			}
			return filepath.Join(r.Dir, sub, name), true
		},
		searchRoot:    true,
		testDir:       true,
		nameMarksTest: true,
	},
	FlatRoot: {
		candidate: func(r Rule, _, dir, name string) (string, bool) {
			if dirDepth(dir) > r.MaxDepth {
				return "", false
			}
			return filepath.Join(r.Dir, name), true
		},
		searchRoot:    true,
		testDir:       true,
		nameMarksTest: true,
	},
	Recognize: {
		testDir:       true,
		nameMarksTest: true,
	},
	// Inline's candidate is the source itself, so Primary names the source
	// and an authored test lands in its own test module. As a CONTENT Shape
	// it resolves only when the source declares an inline test module; a
	// source without one never pairs to itself (rustPlugin's Review Focus 1).
	// Its Name is never read, so IsTest cannot call a source a test through
	// it — the case IsTest's doc anticipated when it dropped the fixed point.
	Inline: {
		candidate:   func(_ Rule, src, _, _ string) (string, bool) { return src, true },
		content:     true,
		fromModules: inlineModule,
	},
	// Declared has no pure candidate: without the source's own `mod`
	// declaration there is no test to name. It resolves the file that
	// declaration loads, by rustc's rule (declaredModule).
	Declared: {
		content:     true,
		fromModules: declaredModule,
		confined:    true,
	},
	TestDir: {
		searchRoot:   true,
		testDir:      true,
		dirMarksTest: true,
	},
}

// Content reports whether s is resolved from what a source declares about
// its own tests (Sources.TestModules) rather than from a path alone. A
// content Shape's candidate, when it has one, is the source itself.
func (s Shape) Content() bool { return traitsOf(s).content }

// inlineModule is Inline's fromModules: the source itself, when any module
// it declares has its body in the file.
func inlineModule(src string, mods []TestModule) []labelled {
	for _, m := range mods {
		if m.Inline {
			return []labelled{{path: filepath.ToSlash(src), label: "inline"}}
		}
	}
	return nil
}

// declaredModule is Declared's fromModules: for each out-of-line module
// (`mod tests;`) the source declares, the file rustc loads for it.
//
//   - With #[path = "p"], p joined to the DECLARING FILE's directory. For a
//     path attribute that is not inside an inline module block, the Rust
//     Reference ("Modules", the path attribute) makes it relative to the
//     directory the source file is located in — for mod.rs/lib.rs/main.rs
//     and for any other file alike — so src/a/b.rs with
//     #[path = "../elsewhere/x.rs"] loads src/elsewhere/x.rs. Labelled
//     "path-attr".
//   - Without it, rustc's default module file: <dir>/<stem>/<name>.rs (or
//     <name>/mod.rs) for a non-mod-rs file, and <dir>/<name>.rs (or
//     <name>/mod.rs) beside mod.rs, lib.rs and main.rs. Labelled
//     "default-module".
//
// lib.rs and main.rs stand for every crate root here: pairing sees paths, not
// Cargo.toml, so a crate root under another name (src/bin/tool.rs, build.rs)
// is read as a non-mod-rs file. Every path is offered, cleaned: whether it
// stays inside the repository is Resolve's question (the Shape is confined,
// see insideRoot), answered once for a relative and an absolute source alike
// and reported when it says no.
func declaredModule(src string, mods []TestModule) []labelled {
	src = filepath.ToSlash(src)
	dir := path.Dir(src)
	stem := strings.TrimSuffix(path.Base(src), path.Ext(src))
	modDir := path.Join(dir, stem)
	if b := path.Base(src); b == "mod.rs" || b == "lib.rs" || b == "main.rs" {
		modDir = dir
	}
	var out []labelled
	add := func(p, label string) {
		out = append(out, labelled{path: path.Clean(p), label: label})
	}
	for _, m := range mods {
		switch {
		case m.Inline:
			continue // Inline's, not Declared's
		case m.PathAttr != "":
			add(path.Join(dir, filepath.ToSlash(m.PathAttr)), "path-attr")
		default:
			add(path.Join(modDir, m.Name+".rs"), "default-module")
			add(path.Join(modDir, m.Name, "mod.rs"), "default-module")
		}
	}
	return out
}

// traitsOf is the only way to read shapeTable. An unknown Shape is a
// programming error — a Rule literal naming a Shape nobody declared — so it
// panics, as stepHash does for its own can't-happen case, rather than
// skipping the rule: a skipped rule is a convention silently not applied,
// which is the failure the table exists to end, and every Rule is a literal
// in a plugin, so any test that touches that plugin's rules finds it.
func traitsOf(s Shape) shapeTraits {
	t, ok := shapeTable[s]
	if !ok {
		panic(fmt.Sprintf("pairing: unknown Shape %q — every Shape needs a shapeTable entry (programming error)", string(s)))
	}
	return t
}

// DefaultRoot is searched for every language, before any root a rule names.
const DefaultRoot = "tests"

// Rule is one convention. Name may contain {base} (the source's file name
// without its extension), replaced literally, once.
//
// Read in the other direction — by IsTest and MightBeTest, asking whether a
// path IS a test rather than where one would be — Name is a pattern over a
// file's base name: {base} matches any run of characters (including none),
// and {ext} matches a dot followed by anything. {ext} is only meaningful
// there; Candidates never substitutes it.
//
// INVARIANT: every Name must carry a test marker beyond {base} and the
// extension — the test_ in test_{base}.py, the .test in {base}.test.js, the
// Test in {base}Test.php. IsTest reads EVERY rule's Name, whatever its Shape,
// as "a file with this name IS a test", so a Name that is only {base} plus an
// extension matches every source of the language: a Rust-style
// tests/{base}.rs rule would make src/lib.rs a test, and IsTest's yes removes
// a file from the audit — real code silently unaudited (Round B1 final
// review). The reading is deliberate rather than an accident of
// implementation: it is what lets IsTest recognize a parallel-tree test
// without knowing which source it belongs to. A language whose tests differ
// from its sources only by directory therefore needs a new Shape whose Name
// IsTest does not read, not a rule here — which is what TestDir is (its Dir
// marks the test; it has no Name), and why Inline and Declared, whose
// candidates are not test-named files, carry no Name either. lang's
// TestEveryCandidateIsATestAndNoSourceIs enforces the invariant for every
// registered plugin by round trip: no source is a test by its name, every
// pure candidate is one, and a content candidate is the source itself.
type Rule struct {
	Shape       Shape
	Name        string
	Dir         string
	KeepLeading bool
	MaxDepth    int
	Rank        int
}

// Candidate is one place a test may live, ranked most specific first.
//
// Rank is its EVIDENTIARY rank: how much real directory context the
// candidate's own construction encodes, NOT its position in the returned
// slice. Rank is what a cross-source ambiguity check (reposcan's
// demoteAmbiguousPairings) compares — a plain slice index would conflate "how
// specific is this match" with "how many earlier candidates happened to
// collapse onto the same string for THIS source", which are different things:
// a zero-directory-evidence match (e.g. Python's flat tests/test_foo.py) can
// arise from the mirror, stripped, OR flat form depending on how shallow the
// source is, and MUST rank identically (as the least specific of whichever
// forms produced it) no matter which one it was, or two equally-uninformative
// matches from different-depth sources would never tie and the safer "demote
// both" outcome would never fire. Dedupe enforces exactly that attribution
// when candidates collapse.
//
// Rank is comparable ACROSS languages (all start at 0 = sibling, the most
// specific a rule set can offer) but is otherwise language-defined; reposcan
// only ever compares ranks between candidates for the SAME resolved test
// path, never across different paths.
type Candidate struct {
	Path  string
	Rank  int
	Shape Shape
}

// Candidates derives codePath's test candidates from rules, in rule order,
// deduplicated by Dedupe's rule (see dedupe.go for why rank merges the way it
// does).
func Candidates(rules []Rule, codePath string) []Candidate {
	dir, base, _ := splitPath(codePath)
	var out []Candidate
	for _, r := range rules {
		build := traitsOf(r.Shape).candidate
		if build == nil {
			continue
		}
		p, ok := build(r, codePath, dir, strings.Replace(r.Name, "{base}", base, 1))
		if !ok {
			continue
		}
		out = append(out, Candidate{Path: p, Rank: r.Rank, Shape: r.Shape})
	}
	return Dedupe(out)
}

// Primary is the NAMING answer: the one candidate a new test for codePath is
// called, which is the first of Candidates(rules, codePath) in rule order. It
// never consults the disk. A caller looking for the test that already exists
// asks Resolve; a caller about to WRITE a test file (advpool's synthetic and
// authored tests) asks Primary. ok is false when no rule yields a candidate —
// the case Candidates(...)[0] used to answer with a panic.
func Primary(rules []Rule, codePath string) (Candidate, bool) {
	cands := Candidates(rules, codePath)
	if len(cands) == 0 {
		return Candidate{}, false
	}
	return cands[0], true
}

// Roots is the top-level directories a search walks for these rules:
// DefaultRoot, then every Dir a searchRoot Shape's rule names (shapeTable),
// trimmed of slashes, deduplicated, in order. (The six languages' TestRoots
// restated exactly these directories; deriving them removes the restatement.)
//
// They are the fallback for a repo whose test layout matches none of the
// Candidates derived from the source path alone (e.g. pallets/itsdangerous:
// `src/itsdangerous/signer.py` pairs with
// `tests/test_itsdangerous/test_signer.py`, one directory level deeper than
// any convention-derived mirror). The search over them (reposcan.FindTest)
// scores MATCHES by shared path context with the source, not by which root
// produced them, and never walks a directory the repository's own .gitignore
// excludes — so a root named here is never itself a way to widen the search
// into `node_modules` or `.venv`.
func Roots(rules []Rule) []string {
	out := []string{DefaultRoot}
	seen := map[string]bool{DefaultRoot: true}
	for _, r := range rules {
		if !traitsOf(r.Shape).searchRoot {
			continue
		}
		d := strings.Trim(filepath.ToSlash(r.Dir), "/")
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return out
}

// splitPath decomposes codePath into its directory (empty string for a
// top-level file — never "."), its base name WITHOUT extension, and its
// extension (including the leading dot). Candidates starts every Shape here:
// the sibling, beside-dir, parallel-tree and flat-root forms all derive from
// this one decomposition.
func splitPath(codePath string) (dir, base, ext string) {
	ext = filepath.Ext(codePath)
	b := filepath.Base(codePath)
	base = strings.TrimSuffix(b, ext)
	dir = filepath.Dir(codePath)
	if dir == "." {
		dir = ""
	}
	return dir, base, ext
}

// joinDir joins dir and name, treating an empty dir as "no directory"
// (plain name) rather than filepath.Join's "./name".
func joinDir(dir, name string) string {
	if dir == "" {
		return name
	}
	return filepath.Join(dir, name)
}

// stripFirstSegment removes the leading path component of a dir, e.g.
// "aisuite/agents" -> "agents", "agents" -> "", "" -> "". This is how a
// source file under one top-level directory (a package name, or a `src/`
// layout) maps onto a parallel test tree: real-world convention REPLACES the
// leading directory rather than nesting the whole original path under
// `tests/` (`aisuite/agents/artifact_store.py` pairs with
// `tests/agents/test_artifact_store.py`, not
// `tests/aisuite/agents/test_artifact_store.py`).
func stripFirstSegment(dir string) string {
	if dir == "" {
		return ""
	}
	parts := strings.SplitN(filepath.ToSlash(dir), "/", 2)
	if len(parts) == 2 {
		return filepath.FromSlash(parts[1])
	}
	return ""
}

// dirDepth returns the number of path segments in dir (0 for a top-level
// file). Used to bound how far a "no directory context at all" flat-tree
// test candidate is allowed to reach: a flat candidate is a plausible
// convention for a shallow source but a collision magnet for a deep one
// (e.g. `examples/javascript/js_example/views.py`, 3 segments deep, would
// otherwise generate the same flat candidate as a genuine top-level
// `src/flask/views.py`).
func dirDepth(dir string) int {
	if dir == "" {
		return 0
	}
	return len(strings.Split(filepath.ToSlash(dir), "/"))
}

// Generic is the language-independent test markers corral has always applied
// (reposcan.isTestFile and cmd/corral looksLikeATestPath, merged in Round B1).
// Name templates may use {ext} for "any extension". Dir-only entries are the
// directory names that make a path a POSSIBLE test (MightBeTest only).
//
// It is package data, the one place cross-language markers live, so they are
// not copied into six languages' rules. Each entry is the old substring or
// prefix check it replaces, restated as a pattern: "{base}_test{ext}" is
// "the base name contains _test." (it matches foo_test.bar.go, as the
// substring check did). The prefix marker is "test_{base}" with no {ext},
// because the check it replaces was a bare prefix: {base} already spans the
// extension of test_foo.py, and leaving {ext} off keeps the old yes for an
// extension-less test_runner script in a diff. {ext} cannot simply be made
// optional everywhere instead — "{base}.spec" would then call a PyInstaller
// coworker-server.spec a possible test, which the golden pins as no.
//
// A marker one language owns is NOT here; it is declared in that language's
// rules, where IsTest reads it for that language only: PHPUnit's
// "{base}Test.php" (php.go) and RSpec's prefix "spec_{base}.rb" (ruby.go). The
// prefix is the one marker the two old functions disagreed on — isTestFile
// applied it to every language, looksLikeATestPath to none — and the
// characterization golden fixes both answers (spec/spec_helper.rb IS a Ruby
// test; tasks/spec_runner.rake is NOT a possible test), which a Generic entry
// cannot satisfy and a Ruby rule does.
var Generic = []Rule{
	{Shape: Recognize, Name: "{base}_test{ext}"}, // Go foo_test.go, minitest foo_test.rb
	{Shape: Recognize, Name: "test_{base}"},      // Python test_foo.py, Ruby test_foo.rb
	{Shape: Recognize, Name: "{base}_spec{ext}"}, // RSpec foo_spec.rb, foo_spec.js, foo_spec.ts
	{Shape: Recognize, Name: "{base}.test{ext}"}, // foo.test.js, foo.test.ts
	{Shape: Recognize, Name: "{base}.spec{ext}"}, // foo.spec.js, foo.spec.ts
	{Shape: Recognize, Dir: "test"}, {Shape: Recognize, Dir: "tests"},
	{Shape: Recognize, Dir: "spec"}, {Shape: Recognize, Dir: "specs"},
	{Shape: Recognize, Dir: "__tests__"}, {Shape: Recognize, Dir: "testing"},
}

// IsTest reports whether rel is itself a test file under one language's
// rules: the STRICT question. Its answer removes a file from the audit, so a
// false yes silently skips auditing real code — which is why a directory
// alone never makes a file a test here (a conftest.py or a fixture under
// tests/ is test SUPPORT, accounted separately by reposcan, never a test),
// with one declared exception: a language whose toolchain itself treats a
// directory as tests says so with a TestDir rule (DirMarksTest), and only
// that language's IsTest reads it.
//
// The name patterns are the real check, and they do NOT depend on the shape
// of the candidate list at all: a parallel-tree test like
// tests/agents/test_artifact_store.py is caught by "test_{base}" exactly
// like a sibling test_artifact_store.py would be, so widening the candidates
// from one path to an ordered list changes nothing here. The patterns are
// every Name in rules (so PHPUnit's separator-less suffix, tests/CalcTest.php,
// is recognized from the PHP plugin's own "{base}Test.php" — the plugin's
// candidates cannot see it, since for a test file they propose
// tests/CalcTestTest.php, and before the marker existed every PHP test was
// counted as an unpaired source) plus every Name in Generic. Matching is
// case-sensitive: src/Latest.php is not a PHPUnit test. It is correct only
// under Rule's marker invariant.
//
// What IsTest cannot see is a test named by its source alone: Rust's
// #[path = "../elsewhere/x.rs"] can load a test module under any name, and
// x.rs carries no marker and sits in no test directory. Such a file is
// resolved as a test (Resolve, Declared) but not recognized as one here, so
// it stays an auditable source — the safe direction, since a false yes would
// silently remove real code from the audit.
//
// It once also asked whether rel appears among its OWN candidates (a
// "fixed point"). That check is gone: any candidate's base name is its rule's
// Name with {base} filled in, which the Name pattern already matches, so it
// could never add a yes — and for a future shape whose candidate IS the
// source (an inline test module) it would have called every source a test.
func IsTest(rules []Rule, rel string) bool {
	base := filepath.Base(rel)
	return nameMatches(rules, base, false) || nameMatches(Generic, base, false) || DirMarksTest(rules, rel)
}

// DirMarksTest reports whether rel sits under a directory one of rules
// declares to hold only tests (a dirMarksTest Shape: Rust's TestDir "tests").
// Any path segment counts, not only the first: a cargo workspace member's
// integration tests live at crates/<member>/tests/. It is the one way IsTest
// answers yes without a name marker, and it is exported so the registry-wide
// round trip (lang's TestEveryCandidateIsATestAndNoSourceIs) can tell a test
// by directory from a source wrongly called a test by its name.
//
// Unlike Generic's directory entries (MightBeTest only), this is the STRICT
// answer, so only a language whose toolchain treats the directory as tests —
// cargo compiles every tests/*.rs as an integration test crate — declares one.
func DirMarksTest(rules []Rule, rel string) bool {
	dirs := path.Dir(filepath.ToSlash(rel))
	for _, r := range rules {
		if !traitsOf(r.Shape).dirMarksTest {
			continue
		}
		d := strings.Trim(filepath.ToSlash(r.Dir), "/")
		if d == "" {
			continue
		}
		for _, seg := range strings.Split(dirs, "/") {
			if seg == d {
				return true
			}
		}
	}
	return false
}

// MightBeTest is the cheap, language-independent question the diff bound asks
// before any evidence exists: could this changed file be a test? It is the
// GENEROUS question over the same data IsTest reads — a false "yes" only costs
// one instrumented run, while a false "no" is the false-green this exists to
// prevent.
//
// So it says yes when any directory segment of rel is a Dir some rule (or
// Generic) names, or when rel's LOWERCASED base name matches any lowercased
// Name pattern — CalcTest.php, calctest.php and Test_Foo.py all might be tests.
// (It used to ask IsTest per language first; that was redundant, because the
// case-folded name match below runs over the same rules and already answers
// every case IsTest answers yes to — cmd/corral's
// TestIsTestImpliesMightBeTest holds it.) It takes every language's rules
// rather than one because a changed file is asked about before anyone knows
// which language claims it (README.md and a fixture under specs/ are asked
// too).
func MightBeTest(all [][]Rule, rel string) bool {
	rel = filepath.ToSlash(rel)
	sets := append([][]Rule{Generic}, all...)
	dirs := map[string]bool{}
	for _, rules := range sets {
		for _, r := range rules {
			if !traitsOf(r.Shape).testDir {
				continue
			}
			if d := strings.Trim(filepath.ToSlash(r.Dir), "/"); d != "" {
				dirs[d] = true
			}
		}
	}
	for _, seg := range strings.Split(path.Dir(rel), "/") {
		if dirs[seg] {
			return true
		}
	}
	base := strings.ToLower(path.Base(rel))
	for _, rules := range sets {
		if nameMatches(rules, base, true) {
			return true
		}
	}
	return false
}

// nameMatches reports whether base matches any nameMarksTest rule's Name
// pattern ({base} = any characters, {ext} = a dot then any characters). With
// fold, the pattern is lowercased first; the caller lowercases base.
func nameMatches(rules []Rule, base string, fold bool) bool {
	for _, r := range rules {
		if !traitsOf(r.Shape).nameMarksTest || r.Name == "" {
			continue
		}
		name := r.Name
		if fold {
			name = strings.ToLower(name)
		}
		if ok, err := path.Match(namePattern(name), base); err == nil && ok {
			return true
		}
	}
	return false
}

// namePattern turns a Name template into a path.Match pattern: every literal
// character that path.Match treats specially is escaped, {base} becomes "*"
// and {ext} becomes ".*". A base name never contains "/", so "*" spanning
// any run of characters is exactly the substring/prefix checks Generic
// replaced.
func namePattern(name string) string {
	var b strings.Builder
	for i := 0; i < len(name); {
		switch {
		case strings.HasPrefix(name[i:], "{base}"):
			b.WriteString("*")
			i += len("{base}")
		case strings.HasPrefix(name[i:], "{ext}"):
			b.WriteString(".*")
			i += len("{ext}")
		default:
			if strings.ContainsRune(`*?[]\`, rune(name[i])) {
				b.WriteByte('\\')
			}
			b.WriteByte(name[i])
			i++
		}
	}
	return b.String()
}

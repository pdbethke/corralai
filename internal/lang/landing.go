// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/pdbethke/corralai/internal/pairing"
)

// TestLander is the plugin extension for a language whose authored test is
// composed INTO an existing file — a pairing.Landing with Into set — rather
// than written as a file of its own. Optional per language (a path language
// has none); a language that implements it implements every method. Rust is
// the one: its tests live in a module the source declares, or inline in the
// source itself, so the authored test is appended to that file as a module
// of its own.
//
// It renders only the appended BLOCK; the target is never edited, so the
// project's own tests and its own `mod tests` are untouched by construction.
// ComposeLanding appends the block, and the language's TestConcatenator merges
// several blocks for one landing into one.
type TestLander interface {
	// LandTest wraps the writer's test body as the block appended to target
	// (the landing file's current content), reaching the code under test
	// across depth module levels (pairing.Landing.Depth). It must never
	// declare a name target already declares.
	LandTest(body string, depth int, target string) string
	// ReachCanaryTest is a test body that ALWAYS FAILS when it runs, as the
	// writer's body would be handed to LandTest. It is the positive control
	// for a landing in the source itself (pairing.Inline): there the
	// authored test has no file of its own to plant invalid source in, and
	// invalid source appended to the source asks only what the run's own
	// canary already asked — does the command compile this file? A command
	// that compiles it but whose filter never runs the appended block passes
	// that question, and the authored test would be credited with kills it
	// never ran for. A test that always fails, landed where the authored one
	// lands, fails the run exactly when the command RUNS the block. An
	// interface method, not an optional one: a language whose landed tests
	// could not be proven reached must not be able to land them at all.
	ReachCanaryTest() string
	// InlineTestIDs is the ids the test runner gives the tests of landed —
	// a block LandTest produced at depth 0 — once it is appended to
	// codePath itself (an Inline landing) in the repository at repoRoot,
	// each an exact name a runner row can select by (Runner.SelectCmd).
	// It is how an inline block's tests are run apart from the source's
	// own, which share their test binary: without the ids the authored
	// pass cannot be "alone" (advpool's selectionDisclosure). An error,
	// never a guess, when the block's tests cannot be listed or codePath
	// has no module path the language can name.
	InlineTestIDs(repoRoot, codePath, landed string) ([]string, error)
}

// landingRules is the rules a landing for codePath is derived from: the
// plugin its extension names, else Go's — the same fallback advPoolTestPath
// and advPoolBase use for a file no plugin claims, so the check and the write
// agree on a language.
func landingRules(codePath string) []pairing.Rule {
	if p, ok := Detect(codePath); ok {
		return p.TestRules()
	}
	g, _ := ByName("go") // always registered
	return g.TestRules()
}

// TestLanding is THE answer to "where does the authored test for codePath
// land": pairing.Land over the rules of the plugin codePath's extension names.
// Every door that refuses (CheckTestLanding) and every path that writes (the
// pool's validator and scorer, from the value a door computed) read it, so
// the file a run is admitted for and the file it writes into cannot differ.
//
// src is what the caller can read of the repository: reposcan.DiskSources
// over the audited repository for certify --local/--repo and doctor, the
// scan's own walk for Enumerate. pairing.Sources{} is the caller with no
// repository (the brain, certify --adversarial, single-file certify --local):
// a content language's declarations go unread and it lands on Primary — for
// Rust the source itself — which CheckTestLanding refuses, since unread
// declarations may hide a test module the landing would duplicate.
func TestLanding(codePath string, src pairing.Sources) (pairing.Landing, error) {
	return pairing.Land(landingRules(codePath), codePath, src)
}

// CheckTestLanding refuses a code file whose authored test corral cannot
// land, and is the ONE place that question is answered.
//
// l is the landing a door computed with TestLanding. The zero Landing means
// no landing was computed — a door with nothing to read it from — and is
// treated as TestLanding(codePath, pairing.Sources{}): the naming answer,
// which refuses a content language and admits every path one. So a door that
// forgets to pass a landing fails CLOSED, never open.
//
// Refused, each for what corral cannot read, never for where the test goes:
//   - l.Path is the source and the source's test-module declarations were
//     not read: a declaration outside the repository (RefusedOutsideRoot,
//     which corral never reads), a build that cannot read declarations at
//     all (no cgo), or an entry with no repository to read them from
//     (certify --adversarial and the brain run single-file). Each lands on
//     the source only because its declarations went unread, so a test
//     composed there could duplicate or shadow a test module that exists.
//   - l.Into and no TestLander can write into l.Path's language: appending to
//     a file whose syntax no plugin can compose would replace it instead.
//
// Admitted: a path landing (a new file of its own — every pre-Rust language),
// a Declared one (the separate test file the source's own `mod` declaration
// loads, appended to), and since 2026-11-24 an Inline one: a source whose
// tests are in the file itself, or that declares no test module, read and
// found so. Its authored block is appended to whatever code each grading run
// writes (adequacy.WithInlineTest), never written over the code, and a
// mutant that edits the source's own test module is invalid
// (adequacy.WithEvidenceLines). Until then such a file was refused here as
// "not built", because writing at the source's path would have overwritten
// the code under test.
//
// The condition is DERIVED from the landing, never a language name. Every
// entry that can start an audit asks before any jail, store or spend:
// advpool.Driver.StartRun (with the RunSpec's carried Landing), the brain's
// AdvPoolRuntime.StartRun and the certify --adversarial client (no
// repository: zero Landing), certify --local's resolveAuditPlugin (also every
// certify --repo job), doctor, and reposcan.Enumerate, which excludes such a
// file before pairing so it is never a candidate.
func CheckTestLanding(codePath string, l pairing.Landing) error {
	if l.Path == "" {
		var err error
		if l, err = TestLanding(codePath, pairing.Sources{}); err != nil {
			return err // unreachable: no TestModules, nothing to read
		}
	}
	langName := "this language's"
	if p, ok := Detect(codePath); ok {
		langName = "corral's " + p.PromptLang()
	}
	if l.Into {
		if p, ok := Detect(l.Path); !ok {
			return fmt.Errorf("%s: its test module is %s, which no language plugin can append a test to, so it cannot be audited (refused before any jail, store or spend)", codePath, l.Path)
		} else if _, ok := p.(TestLander); !ok {
			return fmt.Errorf("%s: its test module is %s, and %s cannot append a test to a %s file, so it cannot be audited (refused before any jail, store or spend)", codePath, l.Path, langName, p.PromptLang())
		}
	}
	if !sameFile(l.Path, codePath) {
		return nil
	}
	const tail = ", so it cannot be audited (refused before any jail, store or spend)"
	switch {
	case len(l.RefusedOutsideRoot) > 0:
		return fmt.Errorf("%s: %s test landing is not built for this file: the test module it declares (%s) is outside the audited repository, which corral never reads%s", codePath, langName, strings.Join(l.RefusedOutsideRoot, ", "), tail)
	case l.DeclarationsUnreadable:
		return fmt.Errorf("%s: %s test landing cannot be found by this corral build: it cannot read the file's test-module declarations (built without cgo, which the grammar needs)%s", codePath, langName, tail)
	case !l.DeclarationsRead:
		return fmt.Errorf("%s: %s test landing is not built for a file whose test-module declaration this entry cannot read — it needs the repository (certify --local and --repo with --repo-dir; certify --adversarial and the brain run single-file)%s", codePath, langName, tail)
	}
	return nil
}

// DevTestOf is the developer's own test file for codePath when its authored
// test lands in the source itself (an Inline landing), and decided reports
// whether the landing decides it at all. For an Inline landing the answer
// is the source, when it declares its own inline test module
// (pairing.Landing.OwnModule), and none ("") when it declares no test
// module corral could resolve: none at all, a declared `mod tests;` whose
// file is missing, or a declaration the reader does not parse. Such a file
// is graded by the tests the evidence says execute it, and no file is named
// its test that does not hold one. A test file a unit covering the source
// happens to be (an integration test) is not the developer's test FOR this
// file, and naming the source as its own test when it holds none would show
// the writer and the critic a test file that is not one (an assigned critic
// is then not run: advpool.CriticNotRunReason). Any other landing is not
// decided here (decided false): the door's pairing answers, as it always
// has.
func DevTestOf(codePath string, l pairing.Landing) (path string, decided bool) {
	if !l.Into || l.Path == "" || !sameFile(l.Path, codePath) {
		return "", false
	}
	if l.OwnModule {
		return codePath, true
	}
	return "", true
}

// InlineTestIDs is the TestLander's InlineTestIDs for codePath's language:
// the exact ids the runner gives the tests of landed once it is appended to
// codePath. An error when no TestLander claims codePath.
func InlineTestIDs(repoRoot, codePath, landed string) ([]string, error) {
	p, ok := Detect(codePath)
	if !ok {
		return nil, fmt.Errorf("lang: no language plugin claims %s, so no test id can be named for a block landed there", codePath)
	}
	lander, ok := p.(TestLander)
	if !ok {
		return nil, fmt.Errorf("lang: corral's %s lands no test in %s", p.PromptLang(), codePath)
	}
	return lander.InlineTestIDs(repoRoot, codePath, landed)
}

// sameFile compares two paths in one frame, cleaned.
func sameFile(a, b string) bool {
	return filepath.Clean(filepath.FromSlash(a)) == filepath.Clean(filepath.FromSlash(b))
}

// LandBody is the authored test as it is graded and delivered: for an Into
// landing, the writer's body wrapped by the landing file's TestLander as the
// block appended there; for a path landing, body unchanged (a file of its
// own). target is the landing file's current content. A missing TestLander
// returns body unchanged — CheckTestLanding has already refused that landing.
func LandBody(l pairing.Landing, target, body string) string {
	if !l.Into {
		return body
	}
	p, ok := Detect(l.Path)
	if !ok {
		return body
	}
	lander, ok := p.(TestLander)
	if !ok {
		return body
	}
	return lander.LandTest(body, l.Depth, target)
}

// ReachCanaryBlock is the positive control's block for landing l: the
// TestLander's always-failing test (ReachCanaryTest), landed by LandTest at
// l's depth onto target exactly as an authored test is, so the control runs
// under the same module and filter the real block does. An error, never a
// substitute, when no TestLander can compose l.Path.
func ReachCanaryBlock(l pairing.Landing, target string) (string, error) {
	p, ok := Detect(l.Path)
	if !ok {
		return "", fmt.Errorf("lang: no language plugin claims %s, so no reach canary can be landed there", l.Path)
	}
	lander, ok := p.(TestLander)
	if !ok {
		return "", fmt.Errorf("lang: corral's %s cannot land a test in %s, so no reach canary can be landed there", p.PromptLang(), l.Path)
	}
	return lander.LandTest(lander.ReachCanaryTest(), l.Depth, target), nil
}

// ComposeLanding is what a run writes at l.Path: for an Into landing, target
// — the landing file's own bytes, unchanged — with the landed block appended;
// for a path landing, the test alone, as the new file it is. It is the one
// overlay rule for an authored test, used by every workspace the pool builds
// for one (advpool's CompileTest and authoredWorkspace).
func ComposeLanding(l pairing.Landing, target, landed string) string {
	if !l.Into {
		return landed
	}
	sep := "\n"
	if target == "" || strings.HasSuffix(target, "\n") {
		sep = ""
	}
	return target + sep + "\n" + landed
}

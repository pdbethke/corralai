// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/pdbethke/corralai/internal/pairing"
)

// TestLander is the OPTIONAL plugin extension for a language whose authored
// test is composed INTO an existing file — a pairing.Landing with Into set —
// rather than written as a file of its own. Rust is the one: its tests live in
// a module the source declares, so the authored test is appended to that
// module's file as a module of its own.
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
// Rust the source itself — which CheckTestLanding refuses.
func TestLanding(codePath string, src pairing.Sources) (pairing.Landing, error) {
	return pairing.Land(landingRules(codePath), codePath, src)
}

// CheckTestLanding refuses a code file whose authored test would be written
// over the file itself, and is the ONE place that question is answered.
//
// l is the landing a door computed with TestLanding. The zero Landing means
// no landing was computed — a door with nothing to read it from — and is
// treated as TestLanding(codePath, pairing.Sources{}): the naming answer,
// which refuses a content language and admits every path one. So a door that
// forgets to pass a landing fails CLOSED, never open.
//
// Refused:
//   - l.Path is the source. A Rust file whose tests are inline, or that
//     declares no test module, lands on itself; composing a test INTO the
//     file the mutants replace needs the workspace overlay (adequacy.Overlay,
//     Round B4) and is not built. Writing there would overwrite the code under
//     test — on --substrate workspace, a journaled write into the operator's
//     real checkout.
//   - l.Into and no TestLander can write into l.Path's language: appending to
//     a file whose syntax no plugin can compose would replace it instead.
//
// Admitted: a path landing (a new file of its own — every pre-Rust language),
// and a Declared one (the separate test file the source's own `mod`
// declaration loads, appended to).
//
// The condition is DERIVED from the landing, never a language name: a
// language gains a landing by making the condition false for it. Every entry
// that can start an audit asks before any jail, store or spend:
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
	if !sameFile(l.Path, codePath) {
		if l.Into {
			if p, ok := Detect(l.Path); !ok {
				return fmt.Errorf("%s: its test module is %s, which no language plugin can append a test to, so it cannot be audited (refused before any jail, store or spend)", codePath, l.Path)
			} else if _, ok := p.(TestLander); !ok {
				return fmt.Errorf("%s: its test module is %s, and %s cannot append a test to a %s file, so it cannot be audited (refused before any jail, store or spend)", codePath, l.Path, langName, p.PromptLang())
			}
		}
		return nil
	}
	const tail = " — a test for this file would be written over the file itself, so it cannot be audited (refused before any jail, store or spend)"
	switch {
	case len(l.RefusedOutsideRoot) > 0:
		return fmt.Errorf("%s: %s test landing is not built for this file: the test module it declares (%s) is outside the audited repository, which corral never reads%s", codePath, langName, strings.Join(l.RefusedOutsideRoot, ", "), tail)
	case !l.DeclarationsRead:
		return fmt.Errorf("%s: %s test landing is not built for a file whose test-module declaration this entry cannot read — it needs the repository (certify --local and --repo with --repo-dir; certify --adversarial and the brain run single-file)%s", codePath, langName, tail)
	default:
		return fmt.Errorf("%s: %s test landing is not built yet for a file whose tests live in the file itself, or that declares no separate test file that exists%s", codePath, langName, tail)
	}
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

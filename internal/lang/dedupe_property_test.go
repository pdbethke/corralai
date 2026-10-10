// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"path/filepath"
	"testing"

	"github.com/pdbethke/corralai/internal/pairing"
	"github.com/pdbethke/corralai/internal/pairing/pairingtest"
)

// This file pins, as a property, the invariant pairing.Dedupe documents but
// does not enforce (see its doc comment in internal/pairing/dedupe.go):
// today, "attribute the least-specific (max) rank among colliding
// non-sibling forms" and the principled "attribute the STRONGEST (min) rank
// among forms that actually assert real directory evidence"
// (pairingtest.PrincipledMerge) agree for every shipped plugin — because no
// shipped plugin's TestRules can currently produce a collision group that
// mixes a non-vacuous, non-sibling form with a weaker one.
//
// It lives in package lang, not pairing, because it must read every
// registered plugin's REAL TestRules: a restated copy of a language's rules
// would let the declaration and this check drift apart, which is the failure
// the rules exist to end. The synthetic violator proving the check can fail
// stays beside Dedupe, in internal/pairing.

// corpusDirs spans several path vocabularies at depths 0 through 3,
// including sources that live UNDER a parallel test root (tests/x, test/a,
// spec/a) — the one family that mixes a Rank-0 sibling match with weaker
// forms in the SAME collision group (e.g. tests/utils.py, test/a/foo.rb: the
// sibling string-collides with a same-source stripped/flat form).
var corpusDirs = []string{
	// depth 0
	"",
	// depth 1
	"src", "lib", "aisuite", "agents", "pkgA", "examples", "tests", "test", "spec", "__tests__",
	// depth 2 (includes the parallel-test-root-mixing family)
	"src/pkg", "aisuite/agents", "pkgA/agents", "pkgB/agents", "examples/celery",
	"tests/x", "test/a", "spec/a", "docs/sub", "mypkg/sub",
	// depth 3
	"examples/celery/task_app", "examples/javascript/js_example", "src/flask/sansio",
	"tests/agents/nested", "test/a/b", "spec/a/b", "a/b/c",
}

var corpusBases = []string{"foo", "utils", "x", "conf", "artifact_store"}

// sourceExt is the source extension a plugin's corpus paths use, read from
// its own declaration — the extension of its first Sibling rule's Name — so
// no hand-kept language list can go stale. The caller's p.Detect check is
// what proves the derived extension is one the plugin really claims.
func sourceExt(rules []pairing.Rule) (string, bool) {
	for _, r := range rules {
		if r.Shape == pairing.Sibling {
			return filepath.Ext(r.Name), true
		}
	}
	return "", false
}

// TestDedupeMatchesPrincipledRuleForShippedPlugins is the equivalence check.
// For every registered plugin, across the whole corpus, production's
// pairing.Candidates (and so Dedupe) over the plugin's real TestRules must
// agree with pairingtest.PrincipledMerge over the same rules' raw forms
// (pairingtest.RawForms).
func TestDedupeMatchesPrincipledRuleForShippedPlugins(t *testing.T) {
	names := pluginNames()
	if len(names) == 0 {
		t.Fatal("empty registry — property was not actually exercised")
	}

	for _, name := range names {
		p := registry[name]
		t.Run(name, func(t *testing.T) {
			rules := p.TestRules()
			ext, ok := sourceExt(rules)
			if !ok {
				t.Fatalf("plugin %q declares no Sibling rule, so its source extension cannot be derived and its rules go unchecked", name)
			}
			checked := 0
			for _, dir := range corpusDirs {
				for _, base := range corpusBases {
					codePath := filepath.Join(dir, base+ext)
					if !p.Detect(codePath) {
						t.Fatalf("%s does not detect corpus path %q", name, codePath)
					}
					want := pairingtest.PrincipledMerge(pairingtest.RawForms(rules, codePath))
					got := pairing.Candidates(rules, codePath)
					if !pairingtest.SameMerge(want, got) {
						t.Errorf("%s: Candidates(%q) = %+v, principled rule wants %+v", name, codePath, got, want)
					}
					checked++
				}
			}
			if checked == 0 {
				t.Fatal("empty corpus — property was not actually exercised")
			}
		})
	}
}

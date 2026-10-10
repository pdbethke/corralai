// SPDX-License-Identifier: Elastic-2.0

package pairing_test

import (
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/pairing"
)

// TestSpecPrefixIsRubysAlone pins Ruling 6 directly, over the real plugins'
// rules: RSpec's spec_ prefix is declared by Ruby, not by Generic. So
// spec/spec_helper.rb is a Ruby test; spec_foo.js is not a JavaScript test;
// and rubocop's tasks/spec_runner.rake is not even a possible test — the
// answer the characterization golden fixes, and the one a Generic spec_ entry
// would flip.
func TestSpecPrefixIsRubysAlone(t *testing.T) {
	rules := func(name string) []pairing.Rule {
		p, ok := lang.ByName(name)
		if !ok {
			t.Fatalf("%s plugin not registered", name)
		}
		return p.TestRules()
	}
	if !pairing.IsTest(rules("ruby"), "spec/spec_helper.rb") {
		t.Error(`IsTest(ruby, "spec/spec_helper.rb") = false, want true — Ruby's Recognize "spec_{base}.rb" rule is not being read`)
	}
	if pairing.IsTest(rules("javascript"), "spec_foo.js") {
		t.Error(`IsTest(javascript, "spec_foo.js") = true, want false — the spec_ prefix leaked out of Ruby's rules`)
	}
	if pairing.MightBeTest(lang.AllTestRules(), "tasks/spec_runner.rake") {
		t.Error(`MightBeTest("tasks/spec_runner.rake") = true, want false — the spec_ prefix became language-independent`)
	}
}

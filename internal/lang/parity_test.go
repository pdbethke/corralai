// SPDX-License-Identifier: Elastic-2.0

package lang

import "testing"

// Every language has every capability, or a written reason it does not. An
// exemption for a capability the language now has fails too, so the list
// only shrinks (the same staleness rule repoindex's langSpecs exemptions
// follow).
func TestEveryLanguageHasEveryCapabilityOrSaysWhy(t *testing.T) {
	keys := map[string]bool{}
	for _, c := range Capabilities {
		keys[c.Key] = true
	}
	exempt := map[[2]string]bool{}
	for _, e := range ParityExemptions {
		if e.Reason == "" {
			t.Errorf("%s/%s: an exemption needs a reason", e.Lang, e.Capability)
		}
		if _, ok := ByName(e.Lang); !ok || !keys[e.Capability] {
			t.Errorf("%s/%s: exemption names no registered language or capability", e.Lang, e.Capability)
		}
		if exempt[[2]string{e.Lang, e.Capability}] {
			t.Errorf("%s/%s: exempted twice", e.Lang, e.Capability)
		}
		exempt[[2]string{e.Lang, e.Capability}] = true
	}
	for _, n := range Names() {
		p, _ := ByName(n)
		if _, ok := paritySamples[n]; !ok {
			t.Errorf("%s has no parity sample: critic-list cannot be judged", n)
		}
		for _, c := range Capabilities {
			has, ex := c.Has(p), exempt[[2]string{n, c.Key}]
			switch {
			case !has && !ex:
				t.Errorf("%s lacks %q and has no exemption saying why", n, c.Title)
			case has && ex:
				t.Errorf("%s now has %q: delete its exemption", n, c.Title)
			}
		}
	}
}

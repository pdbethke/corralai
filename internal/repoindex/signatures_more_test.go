//go:build cgo

// SPDX-License-Identifier: Elastic-2.0

package repoindex

import "testing"

func idsOf(sigs []Signature) map[string]Signature {
	out := map[string]Signature{}
	for _, s := range sigs {
		id := s.Name
		if s.Receiver != "" {
			id = s.Receiver + "." + s.Name
		}
		out[id] = s
	}
	return out
}

// Ruby, JavaScript and TypeScript had NO signature extractor at all:
// ExtractSignatures returned "no signature extractor for language". The
// tree-sitter grammars were already wired for chunking — only the extractors
// were missing.
//
// The cost was not cosmetic. advpool.ShardSymbols bin-packs SYMBOLS, so with
// none the mutant-generator falls back to a single whole-file seat, and its
// prompt's SIGNATURE SURFACE is empty — for exactly the languages that already
// pair worst (expressjs/express is pinned at ZERO candidates in the CI sweep).
func TestRubySignatures(t *testing.T) {
	const src = `
def free(a)
  if a then 1 else 2 end
end

class Thing
  def meth(x)
    x.each { |y| return 1 if y }
  end

  def self.built(z)
    z
  end
end

module Helpers
  def helper(q)
    q
  end
end
`
	sigs, err := ExtractSignatures(src, "ruby")
	if err != nil {
		t.Fatalf("ExtractSignatures(ruby): %v", err)
	}
	got := idsOf(sigs)
	for _, want := range []string{"free", "Thing.meth", "Thing.built", "Helpers.helper"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %q; got %v", want, keysOfSigs(got))
		}
	}
	if s := got["free"]; s.Receiver != "" || s.Kind != "func" {
		t.Errorf("free = %+v, want a free function with no receiver", s)
	}
	if s := got["Thing.meth"]; s.Kind != "method" {
		t.Errorf("Thing.meth Kind = %q, want method", s.Kind)
	}
	// A branching method must not score the straight-line floor.
	if s := got["free"]; s.Complexity <= 1 {
		t.Errorf("free complexity = %d, want > 1 — an if/else is not straight-line", s.Complexity)
	}
}

func TestJavaScriptSignatures(t *testing.T) {
	const src = `
function free(a) { if (a) return 1; return 0 }

class Thing {
  meth(x) { for (const y of x) { if (y) return 1 } return 0 }
  static built(z) { return z }
}

const arrow = (a) => (a ? 1 : 0);
`
	sigs, err := ExtractSignatures(src, "javascript")
	if err != nil {
		t.Fatalf("ExtractSignatures(javascript): %v", err)
	}
	got := idsOf(sigs)
	for _, want := range []string{"free", "Thing.meth", "Thing.built", "arrow"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %q; got %v", want, keysOfSigs(got))
		}
	}
	// A named arrow assigned to a const is a first-class unit of JS code; missing
	// it would leave whole modules with no symbols at all.
	if s := got["arrow"]; s.Kind != "func" {
		t.Errorf("arrow Kind = %q, want func", s.Kind)
	}
	if s := got["Thing.meth"]; s.Complexity <= 1 {
		t.Errorf("Thing.meth complexity = %d, want > 1 — a loop containing an if is not straight-line", s.Complexity)
	}
}

func TestTypeScriptSignatures(t *testing.T) {
	const src = `
function free(a: number): number { if (a) return 1; return 0 }

class Thing {
  meth(x: number[]): number { return 0 }
}

interface Shape {
  area(): number
}
`
	sigs, err := ExtractSignatures(src, "typescript")
	if err != nil {
		t.Fatalf("ExtractSignatures(typescript): %v", err)
	}
	got := idsOf(sigs)
	for _, want := range []string{"free", "Thing.meth"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %q; got %v", want, keysOfSigs(got))
		}
	}
	// An interface method has NO BODY: there is nothing to mutate and nothing a
	// test could catch. Counting it would inflate the symbol count with units
	// the audit can never grade, and hand a shard a seat with no work in it.
	if _, present := got["Shape.area"]; present {
		t.Error("interface method Shape.area was extracted — a declaration has no body to mutate")
	}
}

// TestNewLanguagesMeasureComplexity pins that every newly-supported language
// actually MEASURES complexity rather than returning the floor. A missing
// branchNodeTypes entry yields a silent 1 for every symbol — "this code is
// trivial" where the truth is "never measured", which is the shape this project
// guards against everywhere else.
func TestNewLanguagesMeasureComplexity(t *testing.T) {
	for _, c := range []struct{ lang, src string }{
		{"ruby", "def f(a)\n  if a\n    a.each { |x| return 1 if x }\n  end\n  0\nend\n"},
		{"javascript", "function f(a){ if(a){ for(const x of a){ if(x) return 1 } } return 0 }"},
		{"typescript", "function f(a: number[]): number { if(a){ for(const x of a){ if(x) return 1 } } return 0 }"},
	} {
		sigs, err := ExtractSignatures(c.src, c.lang)
		if err != nil {
			t.Errorf("%s: %v", c.lang, err)
			continue
		}
		if len(sigs) == 0 {
			t.Errorf("%s: no signatures extracted", c.lang)
			continue
		}
		if sigs[0].Complexity <= 1 {
			t.Errorf("%s: complexity = %d for a branch-heavy function, want > 1 — the node set is not matching", c.lang, sigs[0].Complexity)
		}
	}
}

// TestJavaScriptCommonJSAssignments covers the DOMINANT shape in CommonJS-era
// JavaScript: `res.send = function send(body) {}`. It is not a declaration, not
// a class method and not a const arrow, so an extractor handling only those
// finds almost nothing in real code — express/lib/response.js yielded 2 symbols
// before this and 20 after, with res.send scoring complexity 23.
//
// That matters beyond the number: advpool.ShardSymbols bin-packs symbols, so a
// file that indexes as 2 symbols collapses to one generator seat regardless of
// how much code it actually contains.
func TestJavaScriptCommonJSAssignments(t *testing.T) {
	const src = `
var res = {};
res.send = function send(body) {
  if (body) { for (const k in body) { if (k) return 1 } }
  return 0;
};
Thing.prototype.run = function () { return 1 };
bare = function () { return 2 };
`
	sigs, err := ExtractSignatures(src, "javascript")
	if err != nil {
		t.Fatalf("ExtractSignatures: %v", err)
	}
	got := idsOf(sigs)
	for _, want := range []string{"res.send", "Thing.run", "bare"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %q; got %v", want, keysOfSigs(got))
		}
	}
	// `Thing.prototype.run` must read as Thing.run — the prototype hop is
	// noise in an identity a human or a prompt has to resolve.
	if _, ok := got["Thing.prototype.run"]; ok {
		t.Error("receiver kept the .prototype hop; Thing.run is the useful identity")
	}
	if s := got["res.send"]; s.Complexity <= 1 {
		t.Errorf("res.send complexity = %d, want > 1 — a loop containing an if is not straight-line", s.Complexity)
	}
}

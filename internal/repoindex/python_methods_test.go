//go:build cgo

// SPDX-License-Identifier: Elastic-2.0

package repoindex

import "testing"

const pyClassSrc = `
def module_level(x):
    if x:
        return 1
    return 0

class Adapter:
    def send(self, request, stream=False):
        if request:
            for h in request.headers:
                if h:
                    return 1
        return 0

    @property
    def closed(self):
        return self._closed

    @staticmethod
    def helper(a, b):
        return a or b

class Other:
    def send(self):
        return None
`

// The Python extractor walked only the MODULE's top level and accepted only
// function_definition, so class_definition was skipped outright and every
// method inside it was invisible. On psf/requests that reported ONE symbol for
// ~500 lines built around HTTPAdapter.
//
// Three things downstream inherit that blind spot, and the third is the
// serious one:
//
//   - complexity under-reports class-heavy files
//   - the mutant-generator's signature surface is near-empty, so its prompt has
//     almost nothing to work from
//   - advpool.ShardSymbols bin-packs SYMBOLS, so one visible symbol yields ONE
//     shard instead of up to eight: the parallel generator seats silently
//     collapse, and the per-shard mutant budget with them
//
// Most real Python is class-based, so this was the common case, not the edge.
func TestPythonExtractsClassMethods(t *testing.T) {
	sigs, err := ExtractSignatures(pyClassSrc, "python")
	if err != nil {
		t.Fatalf("ExtractSignatures: %v", err)
	}

	byID := map[string]Signature{}
	for _, s := range sigs {
		id := s.Name
		if s.Receiver != "" {
			id = s.Receiver + "." + s.Name
		}
		byID[id] = s
	}

	for _, want := range []string{
		"module_level",   // module-level functions must still be found
		"Adapter.send",   // a plain method
		"Adapter.closed", // @property — decorated
		"Adapter.helper", // @staticmethod — decorated
		"Other.send",     // same NAME, different class
	} {
		if _, ok := byID[want]; !ok {
			t.Errorf("missing symbol %q; got %v", want, keysOfSigs(byID))
		}
	}

	// Same-named methods in different classes must stay distinct, or
	// advpool.symbolIdentity (Receiver + "." + Name) collapses them and a
	// shard silently probes one function twice while another goes unprobed.
	if a, b := byID["Adapter.send"], byID["Other.send"]; a.Receiver == b.Receiver {
		t.Errorf("Adapter.send and Other.send share receiver %q — identities would collide", a.Receiver)
	}
}

// TestPythonMethodComplexityIsMeasured pins that methods are not merely LISTED
// but measured: a branch-heavy method scoring the floor of 1 would mean the
// subtree walk never reached it, and complexity is what balances the shards.
func TestPythonMethodComplexityIsMeasured(t *testing.T) {
	sigs, err := ExtractSignatures(pyClassSrc, "python")
	if err != nil {
		t.Fatalf("ExtractSignatures: %v", err)
	}
	for _, s := range sigs {
		if s.Receiver == "Adapter" && s.Name == "send" {
			if s.Complexity <= 1 {
				t.Fatalf("Adapter.send complexity = %d, want > 1 — two ifs and a loop are not straight-line", s.Complexity)
			}
			if s.Lines <= 1 {
				t.Errorf("Adapter.send Lines = %d, want its real span", s.Lines)
			}
			if s.Kind != "method" {
				t.Errorf("Adapter.send Kind = %q, want \"method\" so a consumer can tell it from a free function", s.Kind)
			}
			return
		}
	}
	t.Fatal("Adapter.send was not extracted at all")
}

// TestPythonModuleFunctionsUnchanged pins that free functions keep exactly
// their previous shape — no receiver, kind "func" — so nothing that already
// worked is disturbed by descending into classes.
func TestPythonModuleFunctionsUnchanged(t *testing.T) {
	sigs, err := ExtractSignatures(pyClassSrc, "python")
	if err != nil {
		t.Fatalf("ExtractSignatures: %v", err)
	}
	for _, s := range sigs {
		if s.Name != "module_level" {
			continue
		}
		if s.Receiver != "" {
			t.Errorf("module_level Receiver = %q, want empty", s.Receiver)
		}
		if s.Kind != "func" {
			t.Errorf("module_level Kind = %q, want \"func\"", s.Kind)
		}
		return
	}
	t.Fatal("module_level went missing — descending into classes must not drop top-level functions")
}

func keysOfSigs(m map[string]Signature) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

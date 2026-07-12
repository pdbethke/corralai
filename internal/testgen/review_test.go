// SPDX-License-Identifier: Elastic-2.0

package testgen

import (
	"context"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
)

func TestParseVerdicts(t *testing.T) {
	resp := "MUTANT m1: GAP: the test never exercises empty grants\n" +
		"MUTANT m2: equivalent: a wildcard grant is outside the goal's model\n" +
		"some preamble line that isn't a verdict\n" +
		"MUTANT m3: WISHYWASHY: unknown class is skipped\n"
	vs := parseVerdicts(resp)
	if len(vs) != 2 {
		t.Fatalf("got %d verdicts, want 2 (garbage + unknown-class skipped): %+v", len(vs), vs)
	}
	if vs[0].MutantID != "m1" || !vs[0].RealGap || vs[0].Rationale != "the test never exercises empty grants" {
		t.Errorf("v1 wrong: %+v", vs[0])
	}
	if vs[1].MutantID != "m2" || vs[1].RealGap || vs[1].Rationale != "a wildcard grant is outside the goal's model" {
		t.Errorf("v2 wrong (case-insensitive equivalent?): %+v", vs[1])
	}
}

func TestTriageSurvivors(t *testing.T) {
	f := &fakeLLM{resp: "MUTANT m2: GAP: the test does not cover the wildcard path\n"}
	survivors := []adequacy.Mutant{{ID: "m2", Code: "package target\nfunc F() bool { return true }"}}
	vs, err := TriageSurvivors(context.Background(), f, "deny by default", "package target\nfunc F() bool { return false }", "package target\n// test", survivors)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 || vs[0].MutantID != "m2" || !vs[0].RealGap {
		t.Fatalf("verdicts wrong: %+v", vs)
	}
	// prompt carries the goal, the compliant code, the test, and each survivor (id + code)
	for _, want := range []string{"deny by default", "return false", "// test", "MUTANT m2", "return true"} {
		if !strings.Contains(f.gotUser, want) {
			t.Errorf("review prompt missing %q; got:\n%s", want, f.gotUser)
		}
	}
	// independence: the reviewer's system prompt is its own, not the writer/generator's
	if !strings.Contains(f.gotSystem, "TEST-REVIEWER") {
		t.Errorf("reviewer system prompt not used: %s", f.gotSystem)
	}
}

func TestTriageSurvivorsEmpty(t *testing.T) {
	f := &fakeLLM{resp: "should not be called"}
	vs, err := TriageSurvivors(context.Background(), f, "g", "c", "t", nil)
	if err != nil || vs != nil {
		t.Fatalf("no survivors → (nil,nil); got %v, %v", vs, err)
	}
	if f.called {
		t.Fatal("no survivors must NOT call the model")
	}
}

func TestTriageSurvivorsNoneParseable(t *testing.T) {
	if _, err := TriageSurvivors(context.Background(), &fakeLLM{resp: "no verdicts here"},
		"g", "c", "t", []adequacy.Mutant{{ID: "m1", Code: "x"}}); err == nil {
		t.Fatal("unparseable reviewer response must error")
	}
}

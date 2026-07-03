// SPDX-License-Identifier: Elastic-2.0

package rolemodel

import (
	"testing"
)

func TestDemoModelsParse(t *testing.T) {
	// Mirrors MODELS_ROLE_MODELS in deploy/demo/Makefile: the two finding-FILERS
	// (pentester + reviewer) MUST be on different models so model_comparison
	// renders a real A-vs-B side-by-side. Both default to local Ollama — key-free.
	//
	// Group A (pentester):  ollama / qwen2.5-coder:7b
	// Group B (reviewer):   ollama / llama3.2:3b  (distinct local model)
	// builder + tester:     ollama / qwen2.5-coder:7b (don't file findings)
	env := "pentester=ollama:qwen2.5-coder:7b,reviewer=ollama:llama3.2:3b,builder=ollama:qwen2.5-coder:7b,tester=ollama:qwen2.5-coder:7b"
	p, bad := Parse(env)
	if len(bad) > 0 {
		t.Fatalf("unexpected malformed entries: %v", bad)
	}
	if len(p) != 4 {
		t.Fatalf("expected 4 roles, got %d", len(p))
	}
	cases := []struct{ role, backend, model string }{
		{"pentester", "ollama", "qwen2.5-coder:7b"},
		{"reviewer", "ollama", "llama3.2:3b"},
		{"builder", "ollama", "qwen2.5-coder:7b"},
		{"tester", "ollama", "qwen2.5-coder:7b"},
	}
	for _, c := range cases {
		ref, ok := p.Lookup(c.role)
		if !ok {
			t.Errorf("role %q not found", c.role)
			continue
		}
		if ref.Backend != c.backend {
			t.Errorf("role %q: backend want %q got %q", c.role, c.backend, ref.Backend)
		}
		if ref.Model != c.model {
			t.Errorf("role %q: model want %q got %q", c.role, c.model, ref.Model)
		}
	}
	// The two finding-filers must differ in model, or there's no side-by-side.
	pen, _ := p.Lookup("pentester")
	rev, _ := p.Lookup("reviewer")
	if pen.Backend == rev.Backend && pen.Model == rev.Model {
		t.Fatalf("pentester and reviewer resolve to the SAME model (%s:%s) — model_comparison would render one row", pen.Backend, pen.Model)
	}
}

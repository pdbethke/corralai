// SPDX-License-Identifier: Elastic-2.0

package rolemodel_test

import (
	"sync"
	"testing"

	"github.com/pdbethke/corralai/internal/rolemodel"
)

// TestConcurrentAccess reproduces the shared-policy race: the staffing engine
// writes the policy (Set) while the UI topology loop reads it (Lookup/Snapshot).
// Before Policy carried its own RWMutex this crashed the process under -race
// with "concurrent map writes". Run with `go test -race`.
func TestConcurrentAccess(t *testing.T) {
	p := rolemodel.New()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(3)
		go func() { defer wg.Done(); p.Set("builder", rolemodel.ModelRef{Model: "qwen"}) }()
		go func() { defer wg.Done(); _, _ = p.Lookup("builder") }()
		go func() { defer wg.Done(); _ = p.Snapshot(); _ = p.Len() }()
	}
	wg.Wait()
}

func TestParse(t *testing.T) {
	t.Run("valid backend:model and bare model", func(t *testing.T) {
		p, malformed := rolemodel.Parse("reviewer=anthropic:claude-opus,builder=qwen2.5-coder, bad-entry ,x=")
		if ref, ok := p.Lookup("reviewer"); !ok || ref.Backend != "anthropic" || ref.Model != "claude-opus" {
			t.Errorf("reviewer: got %+v, ok=%v", ref, ok)
		}
		if ref, ok := p.Lookup("builder"); !ok || ref.Backend != "" || ref.Model != "qwen2.5-coder" {
			t.Errorf("builder: got %+v, ok=%v", ref, ok)
		}
		if len(malformed) != 2 {
			t.Errorf("expected 2 malformed entries, got %v: %v", len(malformed), malformed)
		}
		found := map[string]bool{}
		for _, m := range malformed {
			found[m] = true
		}
		if !found["bad-entry"] {
			t.Errorf("expected 'bad-entry' in malformed, got %v", malformed)
		}
		if !found["x="] {
			t.Errorf("expected 'x=' in malformed, got %v", malformed)
		}
	})

	t.Run("empty string returns empty policy no malformed", func(t *testing.T) {
		p, malformed := rolemodel.Parse("")
		if p.Len() != 0 {
			t.Errorf("expected empty policy, got %v", p.Snapshot())
		}
		if len(malformed) != 0 {
			t.Errorf("expected no malformed, got %v", malformed)
		}
	})
}

func TestAvailable(t *testing.T) {
	pool := []rolemodel.ModelRef{
		{Backend: "anthropic", Model: "claude-opus"},
		{Backend: "ollama", Model: "qwen"},
	}

	t.Run("in pool with backend match", func(t *testing.T) {
		p, _ := rolemodel.Parse("reviewer=anthropic:claude-opus")
		ref, ok := p.Available("reviewer", pool)
		if !ok {
			t.Fatal("expected ok=true")
		}
		if ref.Backend != "anthropic" || ref.Model != "claude-opus" {
			t.Errorf("unexpected ref: %+v", ref)
		}
	})

	t.Run("not in pool", func(t *testing.T) {
		p, _ := rolemodel.Parse("reviewer=gemini:gemini-3")
		_, ok := p.Available("reviewer", pool)
		if ok {
			t.Error("expected ok=false for model not in pool")
		}
	})

	t.Run("backend mismatch when policy specifies backend", func(t *testing.T) {
		// policy says backend=gemini but pool only has anthropic for claude-opus
		p, _ := rolemodel.Parse("reviewer=gemini:claude-opus")
		_, ok := p.Available("reviewer", pool)
		if ok {
			t.Error("expected ok=false when backend specified but mismatches")
		}
	})

	t.Run("bare model matches any backend in pool", func(t *testing.T) {
		p, _ := rolemodel.Parse("reviewer=qwen")
		ref, ok := p.Available("reviewer", pool)
		if !ok {
			t.Fatal("expected ok=true for bare model match")
		}
		if ref.Model != "qwen" {
			t.Errorf("unexpected ref: %+v", ref)
		}
	})

	t.Run("role not in policy", func(t *testing.T) {
		p, _ := rolemodel.Parse("reviewer=anthropic:claude-opus")
		_, ok := p.Available("builder", pool)
		if ok {
			t.Error("expected ok=false for role not in policy")
		}
	})
}

func TestReconcile(t *testing.T) {
	p, _ := rolemodel.Parse("reviewer=claude-opus")

	t.Run("drift detected", func(t *testing.T) {
		expected, drift := rolemodel.Reconcile("reviewer", "gemini-3", p)
		if expected != "claude-opus" {
			t.Errorf("expected 'claude-opus', got %q", expected)
		}
		if !drift {
			t.Error("expected drift=true")
		}
	})

	t.Run("no drift", func(t *testing.T) {
		expected, drift := rolemodel.Reconcile("reviewer", "claude-opus", p)
		if expected != "claude-opus" {
			t.Errorf("expected 'claude-opus', got %q", expected)
		}
		if drift {
			t.Error("expected drift=false")
		}
	})

	t.Run("role not in policy", func(t *testing.T) {
		expected, drift := rolemodel.Reconcile("builder", "anything", p)
		if expected != "" {
			t.Errorf("expected empty string, got %q", expected)
		}
		if drift {
			t.Error("expected drift=false for unknown role")
		}
	})
}

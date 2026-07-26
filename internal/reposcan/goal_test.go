package reposcan

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileGoalSourceReadsGoals(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "goals.json")
	body := `{"pkg/a.go": "must reject negative balances"}`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	gs, err := NewFileGoalSource(p)
	if err != nil {
		t.Fatalf("NewFileGoalSource: %v", err)
	}

	g, ok, err := gs.GoalFor(Candidate{Path: "pkg/a.go"})
	if err != nil || !ok {
		t.Fatalf("GoalFor: ok=%v err=%v", ok, err)
	}
	if g.Text != "must reject negative balances" {
		t.Errorf("Text = %q", g.Text)
	}
	if g.Provenance != "file" {
		t.Errorf("Provenance = %q, want file", g.Provenance)
	}
}

// Fail closed: a file with no goal is UNGOALED, never given a made-up goal.
func TestFileGoalSourceMissingGoalIsUngoaled(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "goals.json")
	if err := os.WriteFile(p, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	gs, err := NewFileGoalSource(p)
	if err != nil {
		t.Fatal(err)
	}
	g, ok, err := gs.GoalFor(Candidate{Path: "pkg/a.go"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatalf("want ungoaled, got goal %+v", g)
	}
}

// An empty or whitespace goal is not a goal.
func TestFileGoalSourceBlankGoalIsUngoaled(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "goals.json")
	if err := os.WriteFile(p, []byte(`{"pkg/a.go": "   "}`), 0o644); err != nil {
		t.Fatal(err)
	}
	gs, _ := NewFileGoalSource(p)
	if _, ok, _ := gs.GoalFor(Candidate{Path: "pkg/a.go"}); ok {
		t.Fatal("blank goal was accepted")
	}
}

// Missing file returns error.
func TestFileGoalSourceMissingFileReturnsError(t *testing.T) {
	_, err := NewFileGoalSource("/nonexistent/path/goals.json")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

// Malformed JSON returns error.
func TestFileGoalSourceMalformedJSONReturnsError(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "goals.json")
	if err := os.WriteFile(p, []byte(`{invalid json`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := NewFileGoalSource(p)
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

// Multiple goals in map work correctly.
func TestFileGoalSourceMultipleGoals(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "goals.json")
	body := `{
		"pkg/a.go": "goal A",
		"pkg/b.go": "goal B",
		"pkg/c.go": ""
	}`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	gs, err := NewFileGoalSource(p)
	if err != nil {
		t.Fatalf("NewFileGoalSource: %v", err)
	}

	// Test first goal
	g1, ok1, _ := gs.GoalFor(Candidate{Path: "pkg/a.go"})
	if !ok1 || g1.Text != "goal A" {
		t.Errorf("pkg/a.go: ok=%v text=%q", ok1, g1.Text)
	}

	// Test second goal
	g2, ok2, _ := gs.GoalFor(Candidate{Path: "pkg/b.go"})
	if !ok2 || g2.Text != "goal B" {
		t.Errorf("pkg/b.go: ok=%v text=%q", ok2, g2.Text)
	}

	// Test empty string (should be ungoaled)
	_, ok3, _ := gs.GoalFor(Candidate{Path: "pkg/c.go"})
	if ok3 {
		t.Error("pkg/c.go: empty string was accepted as a goal")
	}

	// Test path not in map (should be ungoaled)
	_, ok4, _ := gs.GoalFor(Candidate{Path: "pkg/d.go"})
	if ok4 {
		t.Error("pkg/d.go: nonexistent path was accepted as a goal")
	}
}

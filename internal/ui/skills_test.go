// SPDX-License-Identifier: Elastic-2.0

package ui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/pdbethke/corralai/internal/artifacts"
	"github.com/pdbethke/corralai/internal/coord"
)

func TestSkillNameFromPath(t *testing.T) {
	cases := map[string]string{
		"skills/deploy/SKILL.md":       "deploy",
		"skills/using-corralai.md":     "using-corralai",
		"skills/roles/tester/SKILL.md": "roles/tester",
		"hooks/branch-guard.sh":        "hooks/branch-guard.sh", // no skills/ prefix -> as-is
	}
	for in, want := range cases {
		if got := skillNameFromPath(in); got != want {
			t.Errorf("skillNameFromPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSkillDescription(t *testing.T) {
	withFM := "---\nname: deploy\ndescription: ship the thing safely\n---\n\n# deploy\nbody"
	if got := skillDescription([]byte(withFM)); got != "ship the thing safely" {
		t.Errorf("description = %q, want %q", got, "ship the thing safely")
	}
	quoted := "---\ndescription: \"quoted one\"\n---\nbody"
	if got := skillDescription([]byte(quoted)); got != "quoted one" {
		t.Errorf("description = %q, want %q", got, "quoted one")
	}
	if got := skillDescription([]byte("# no frontmatter here\nbody")); got != "" {
		t.Errorf("description = %q, want empty for no frontmatter", got)
	}
	if got := skillDescription([]byte("---\nname: x\n---\nno description field")); got != "" {
		t.Errorf("description = %q, want empty when field absent", got)
	}
}

// TestSkillsEndpoint covers the Skills tab's read surface: /api/skills lists
// only live kind='skill' artifacts (hooks excluded), name-derived from path,
// with a best-effort description from frontmatter.
func TestSkillsEndpoint(t *testing.T) {
	dir := t.TempDir()
	cs, err := coord.Open(filepath.Join(dir, "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	astore, err := artifacts.Open(filepath.Join(dir, "a.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer astore.Close()

	if _, _, err := astore.Put("skills/deploy/SKILL.md", []byte("---\ndescription: ship it\n---\nbody"), "op", 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := astore.Put("hooks/branch-guard.sh", []byte("#!/bin/sh\necho guard"), "op", 0); err != nil {
		t.Fatal(err)
	}

	h := Handler(Deps{Coord: cs, MemOwners: map[string]bool{}, Artifacts: astore})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/skills", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/skills status = %d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Skills []skillView `json:"skills"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Skills) != 1 {
		t.Fatalf("skills count = %d, want 1 (hooks excluded): %+v", len(payload.Skills), payload.Skills)
	}
	sk := payload.Skills[0]
	if sk.Name != "deploy" {
		t.Errorf("name = %q, want %q", sk.Name, "deploy")
	}
	if sk.Path != "skills/deploy/SKILL.md" {
		t.Errorf("path = %q, want %q", sk.Path, "skills/deploy/SKILL.md")
	}
	if sk.Description != "ship it" {
		t.Errorf("description = %q, want %q", sk.Description, "ship it")
	}
}

// TestSkillsEndpointNilStore ensures a nil Artifacts dep degrades to an empty
// list rather than a 500 — same posture as every other optional dependency.
func TestSkillsEndpointNilStore(t *testing.T) {
	dir := t.TempDir()
	cs, err := coord.Open(filepath.Join(dir, "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	h := Handler(Deps{Coord: cs, MemOwners: map[string]bool{}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/skills", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var payload struct {
		Skills []skillView `json:"skills"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Skills == nil || len(payload.Skills) != 0 {
		t.Fatalf("skills = %+v, want empty non-nil list", payload.Skills)
	}
}

// TestAgentDetailCarriesFleetSkills: the inspector's "skills it has" section
// reads agentDetail.skills, which must be the fleet's canonical set (the
// brain has no per-agent equip tracking — see the honest-limit comment in
// ui.go's agentDetail).
func TestAgentDetailCarriesFleetSkills(t *testing.T) {
	dir := t.TempDir()
	cs, err := coord.Open(filepath.Join(dir, "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	astore, err := artifacts.Open(filepath.Join(dir, "a.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer astore.Close()
	if _, _, err := astore.Put("skills/using-corralai/SKILL.md", []byte("---\ndescription: how to use corralai\n---\nbody"), "op", 0); err != nil {
		t.Fatal(err)
	}

	h := Handler(Deps{Coord: cs, MemOwners: map[string]bool{}, Artifacts: astore})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agent?name=some-agent", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Skills []skillView `json:"skills"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Skills) != 1 || payload.Skills[0].Name != "using-corralai" {
		t.Fatalf("skills = %+v, want fleet's one skill", payload.Skills)
	}
}

// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/reposcan"
)

// The regression this whole task exists to prevent: with ModelSet hardcoded to
// "unset", two audits of identical source under DIFFERENT models produce the
// SAME cache key. Enable a cache against that key and a model switch silently
// serves the previous model's verdict into a signed record.
func TestCacheKeyVariesWithTheCriticModel(t *testing.T) {
	base := reposcan.KeyInputs{
		SourceDigest: "src", PackageDigest: "pkg", GoalDigest: "goal",
		TestSurfaceDigest: "test", EngineVersion: "gen1",
		AuditConfig: "", Substrate: reposcan.SubstrateWorkspace,
	}
	a, b := base, base
	a.ModelSet = modelSetKey("claude-sonnet-5", "claude-sonnet-5", "claude-haiku-4-5", "off")
	b.ModelSet = modelSetKey("claude-sonnet-5", "claude-sonnet-5", "gemini-3.6-flash", "off")

	if a.CacheKey() == b.CacheKey() {
		t.Fatal("two different critic models produced the same cache key — a cached verdict would cross a model switch")
	}
}

func TestModelSetKeyIsCanonicalAndNamesEveryRole(t *testing.T) {
	got := modelSetKey("claude-sonnet-5", "claude-sonnet-5", "claude-haiku-4-5", "off")
	want := "critic=claude-haiku-4-5,mutant-generator=claude-sonnet-5,shadow=off,test-writer=claude-sonnet-5"
	if got != want {
		t.Fatalf("modelSetKey = %q, want %q", got, want)
	}
}

// resolveRoleModels must return RESOLVED names, never the raw flag text: an
// empty --writer-model means the default, and if the key recorded "" the
// default-flagged run and the explicitly-flagged run would key differently
// for an identical audit.
func TestResolveRoleModelsFillsDefaults(t *testing.T) {
	w, m, c, _ := resolveRoleModels(localAuditInput{})
	if w != defaultLocalWriterModel || m != defaultLocalMutantModel || c != defaultLocalCriticModel {
		t.Fatalf("resolveRoleModels with no overrides = (%q, %q, %q), want the defaults", w, m, c)
	}
	if strings.TrimSpace(w) == "" {
		t.Fatal("writer resolved to empty")
	}
}

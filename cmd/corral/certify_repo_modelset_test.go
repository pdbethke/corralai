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
	a.ModelSet = modelSetKey("claude-sonnet-5", "claude-sonnet-5", "claude-haiku-4-5", "off", "")
	b.ModelSet = modelSetKey("claude-sonnet-5", "claude-sonnet-5", "gemini-3.6-flash", "off", "")

	if a.CacheKey() == b.CacheKey() {
		t.Fatal("two different critic models produced the same cache key — a cached verdict would cross a model switch")
	}
}

func TestModelSetKeyIsCanonicalAndNamesEveryRole(t *testing.T) {
	got := modelSetKey("claude-sonnet-5", "claude-sonnet-5", "claude-haiku-4-5", "off", "")
	want := "critic=claude-haiku-4-5,mutant-generator=claude-sonnet-5,shadow=off,test-writer=claude-sonnet-5"
	if got != want {
		t.Fatalf("modelSetKey = %q, want %q", got, want)
	}
}

// resolveRoleModels is the ONE place a seat's model is resolved, so the cache
// key and the run can never disagree about which models an audit used. They
// must not: the key records the herd, and a key derived from a different
// resolution than the run would let a verdict be reused across a model change.
//
// This replaces a test asserting that an unnamed seat resolved to a DEFAULT.
// corral has no default models — an unnamed grading seat is refused by
// resolveAuditRoles, not filled — so the contract here is that the resolver
// reports exactly what it was given.
func TestResolveRoleModelsAppliesNoDefaults(t *testing.T) {
	w, m, c, sh, shw := resolveRoleModels(localAuditInput{})
	if w != "" || m != "" || c != "" || sh != "" || shw != "" {
		t.Fatalf("unnamed seats resolved to (%q, %q, %q, %q, %q), want all empty — no seat has a default", w, m, c, sh, shw)
	}

	// "off" is a resolution, not a model name: it must reach the key as "",
	// so a deliberately disabled critic never keys as a model called "off".
	_, _, off, _, _ := resolveRoleModels(localAuditInput{criticModel: "off"})
	if off != "" {
		t.Fatalf("critic \"off\" resolved to %q, want \"\"", off)
	}

	// Whitespace is trimmed, so " claude-sonnet-5 " and "claude-sonnet-5"
	// cannot key as two different herds for the same audit.
	w2, m2, _, _, _ := resolveRoleModels(localAuditInput{writerModel: "  gemini-3.6-flash  ", mutantModel: "x"})
	if w2 != "gemini-3.6-flash" || m2 != "x" {
		t.Fatalf("resolved = (%q, %q), want trimmed passthrough", w2, m2)
	}
}

// The challenger writer must NOT change the key when off: a run with it off
// is byte-identical to a pre-feature run, so the cache key must be too, or
// shipping this feature invalidates every cached verdict in existence.
// Naming a challenger DOES change the key, which is required so enabling it
// forces the re-run that collects the measurement instead of hitting cache.
func TestModelSetKeyOmitsShadowWriterWhenEmptyButIncludesItWhenNamed(t *testing.T) {
	preFeature := "critic=claude-haiku-4-5,mutant-generator=claude-sonnet-5,shadow=off,test-writer=claude-sonnet-5"

	off := modelSetKey("claude-sonnet-5", "claude-sonnet-5", "claude-haiku-4-5", "off", "")
	if off != preFeature {
		t.Fatalf("modelSetKey with an empty shadow writer = %q, want the pre-feature key %q — an off-by-default seat must not invalidate existing cache entries", off, preFeature)
	}

	named := modelSetKey("claude-sonnet-5", "claude-sonnet-5", "claude-haiku-4-5", "off", "gemma4")
	if named == preFeature {
		t.Fatal("modelSetKey did not change when a shadow writer was named — enabling the challenger would silently hit a cached verdict and collect no measurement")
	}
}

// The GRADING MODE is part of a verdict's identity: --whole-suite and
// coverage-guided selection run different tests per mutant, so they are
// answers to different questions and must never share a cache key.
func TestAuditConfigKeyVariesWithTheGradingMode(t *testing.T) {
	if auditConfigKey(false, "", nil, "") == auditConfigKey(true, "", nil, "") {
		t.Fatal("--whole-suite did not change AuditConfig — it changes which tests run per mutant, so it changes the verdict")
	}
	if auditConfigKey(false, "", nil, "") == auditConfigKey(false, "coverage-context", nil, "") {
		t.Fatal("a scan that SELECTED keys the same as one whose instrumented run failed and fell back to the whole suite — the fallback would serve a selected verdict")
	}
}

func TestAuditConfigKeyOmitsUnsetSettings(t *testing.T) {
	if got := auditConfigKey(false, "", nil, ""); got != "" {
		t.Fatalf("auditConfigKey with nothing set = %q, want empty", got)
	}
	// An empty argv must serialize as ABSENT, not as the digest of an empty
	// string: the plugin-default path has to key exactly as it always has, or
	// enabling this fix would invalidate every verdict already earned.
	if got := auditConfigKey(false, "", []string{}, ""); got != "" {
		t.Fatalf("auditConfigKey with an empty argv = %q, want empty", got)
	}
}

func TestAuditConfigKeyIsStable(t *testing.T) {
	if a, b := auditConfigKey(true, "", nil, ""), auditConfigKey(true, "", nil, ""); a != b {
		t.Fatalf("auditConfigKey is not deterministic: %q vs %q", a, b)
	}
	if want := "whole-suite=true"; auditConfigKey(true, "", nil, "") != want {
		t.Fatalf("auditConfigKey = %q, want %q", auditConfigKey(true, "", nil, ""), want)
	}
}

// CRITICAL: the operator's `-- <cmd>` IS the grading surface — the baseline
// and every mutant are run with it. Without it in the key, `certify --repo .
// --record -- pytest -q tests/unit` and a later `-- pytest -q` (the whole
// suite) compute an identical KeyInputs for unchanged source: the full-suite
// run HITS, then reports and signs a kill rate measured against a subset it
// never ran.
func TestCacheKeyVariesWithTheOperatorsTestCommand(t *testing.T) {
	base := reposcan.KeyInputs{
		SourceDigest: "src", PackageDigest: "pkg", GoalDigest: "goal",
		TestSurfaceDigest: "test", EngineVersion: "gen1", ModelSet: "m",
		Substrate: reposcan.SubstrateWorkspace,
	}
	subset, whole := base, base
	subset.AuditConfig = auditConfigKey(false, "", []string{"pytest", "-q", "tests/unit"}, "")
	whole.AuditConfig = auditConfigKey(false, "", []string{"pytest", "-q"}, "")

	if subset.CacheKey() == whole.CacheKey() {
		t.Fatal("two different `-- <cmd>` test commands produced the same cache key — a whole-suite run would sign a kill rate measured against a subset it never ran")
	}
}

// The argv is folded to a sha256 rather than written verbatim: a real check
// command is long and routinely contains `=` and `,`, which are CanonicalKV's
// OWN delimiters — a verbatim value could forge or corrupt other components.
func TestAuditConfigKeyHashesTheArgvRatherThanEmbeddingIt(t *testing.T) {
	got := auditConfigKey(false, "", []string{"pytest", "-q", "--cov=src", "-k", "a,b"}, "")
	if strings.Contains(got, "pytest") || strings.Contains(got, ",b") {
		t.Fatalf("auditConfigKey embedded the raw argv (%q) — CanonicalKV delimiters (= and ,) must not come from operator text", got)
	}
	if !strings.HasPrefix(got, "check-argv=") {
		t.Fatalf("auditConfigKey = %q, want a check-argv=<hex> component", got)
	}
	// Joined on \x00, so no re-splitting of the argv can collide: ["a b"] and
	// ["a", "b"] are different commands and must key differently.
	if auditConfigKey(false, "", []string{"a b"}, "") == auditConfigKey(false, "", []string{"a", "b"}, "") {
		t.Fatal("argv words were joined ambiguously — two different commands share a key")
	}
}

// --min-kill-rate decides the process EXIT CODE (repoScanExitCode); it cannot
// change a measurement. Keying it meant the CI merge-gate invocation — the one
// that always passes a threshold — could never reuse a nightly verdict, which
// is most of the value the cache exists to deliver.
func TestAuditConfigKeyIgnoresMinKillRate(t *testing.T) {
	if got := auditConfigKey(false, "", nil, ""); strings.Contains(got, "min-kill-rate") {
		t.Fatalf("auditConfigKey = %q, want no min-kill-rate component", got)
	}
}

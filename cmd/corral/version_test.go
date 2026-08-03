// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"runtime/debug"
	"testing"
)

// `go install github.com/pdbethke/corralai/cmd/corral@latest` — the install
// path in the README, and the one every first-time reader uses — passes no
// -ldflags, so the stamped version stays "dev" and `corral version` cannot tell
// anyone what they are running. Go embeds the module version in build info for
// exactly this case; use it.
func TestResolveVersion_UsesBuildInfoWhenNotStamped(t *testing.T) {
	bi := func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Main: debug.Module{Version: "v0.3.0"}}, true
	}
	if got := resolveVersion("dev", bi); got != "v0.3.0" {
		t.Fatalf("resolveVersion = %q, want v0.3.0 from build info", got)
	}
}

// An explicitly stamped build wins: the release pipeline knows more than the
// module graph does (and may be building from a checkout, not a tagged module).
func TestResolveVersion_StampedWins(t *testing.T) {
	bi := func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Main: debug.Module{Version: "v0.3.0"}}, true
	}
	if got := resolveVersion("v9.9.9-rc1", bi); got != "v9.9.9-rc1" {
		t.Fatalf("resolveVersion = %q, want the stamped value to win", got)
	}
}

// A local `go build` reports "(devel)" — that is not a version and must not be
// printed as one. Same for an absent build info. Both fall back to "dev",
// today's behaviour, so nothing regresses for a developer working in-tree.
func TestResolveVersion_DevelAndMissingFallBackToDev(t *testing.T) {
	devel := func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, true
	}
	if got := resolveVersion("dev", devel); got != "dev" {
		t.Fatalf("resolveVersion = %q, want dev for a (devel) build", got)
	}

	missing := func() (*debug.BuildInfo, bool) { return nil, false }
	if got := resolveVersion("dev", missing); got != "dev" {
		t.Fatalf("resolveVersion = %q, want dev when build info is unavailable", got)
	}

	empty := func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Main: debug.Module{Version: ""}}, true
	}
	if got := resolveVersion("dev", empty); got != "dev" {
		t.Fatalf("resolveVersion = %q, want dev for an empty module version", got)
	}
}

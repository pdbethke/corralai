// SPDX-License-Identifier: Elastic-2.0

//go:build linux

package sandbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// argvGoldenFile is every backend's argv for a run with no Target, captured
// from the code as it stood BEFORE Options.Target existed (main at 76662270,
// 2026-11-17). It is never regenerated: a run with no Target must produce
// exactly what it produced before, byte for byte, and a golden rewritten from
// the new code would prove nothing about that.
const argvGoldenFile = "testdata/argv-without-target.json"

// argvGoldenCase is one deterministic Wrap: fixed paths (a PerEntry bind's
// host does not exist, so it expands to nothing and is never overlaid), a
// fixed env, and no cargo home masks (wholeBoundTrees is emptied).
type argvGoldenCase struct {
	name string
	iso  Isolator
	opts Options
	env  []string
}

func argvGoldenCases() []argvGoldenCase {
	env := []string{"PATH=/usr/bin", "HOME=/root", "GOTOOLCHAIN=auto", "KEEP=1"}
	over := bwrapIsolator{overlay: true, lowers: newLowerCache(func(string) bool { return true })}
	return []argvGoldenCase{
		{"bwrap/plain", bwrapIsolator{}, Options{Workspace: "/w"}, env},
		{"bwrap/network", bwrapIsolator{}, Options{Workspace: "/w", Network: true}, env},
		{"bwrap/binds", bwrapIsolator{}, Options{Workspace: "/w", ReadOnlyBinds: []Bind{
			{Host: "/deps/vendor", Target: "/w/vendor"},
			{Host: "/nonexistent-corral-golden/node_modules", Target: "/w/node_modules", PerEntry: true},
		}}, env},
		{"bwrap/overlaying-binds", over, Options{Workspace: "/w", ReadOnlyBinds: []Bind{
			{Host: "/deps/vendor", Target: "/w/vendor"},
			{Host: "/nonexistent-corral-golden/node_modules", Target: "/w/node_modules", PerEntry: true},
		}}, env},
		{"bwrap/seeded", over, Options{Workspace: "/tmp/corral-seed", SeedLower: "/tmp/corral-seed", ReadOnlyBinds: []Bind{
			{Host: "/nonexistent-corral-golden/node_modules", Target: "/tmp/corral-seed/node_modules", PerEntry: true},
		}}, env},
		{"bwrap/warm-cache", over, Options{Workspace: "/w", Cache: &CacheMount{Host: "/cache", Target: CacheTarget, Env: "GOCACHE"}}, env},
		{"container/plain", containerIsolator{runtime: "docker", image: "img"}, Options{Workspace: "/ws", ReadOnlyBinds: []Bind{
			{Host: "/deps/vendor", Target: "/ws/vendor"},
		}}, env},
		{"container/network", containerIsolator{runtime: "podman", image: "img"}, Options{Workspace: "/ws", Network: true}, env},
	}
}

func wrapArgvGoldenCases(t *testing.T) map[string][]string {
	t.Helper()
	old := wholeBoundTrees
	wholeBoundTrees = nil
	t.Cleanup(func() { wholeBoundTrees = old })
	out := map[string][]string{}
	for _, c := range argvGoldenCases() {
		argv, err := c.iso.Wrap("go test ./...", c.opts, c.env)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		out[c.name] = argv
	}
	return out
}

// Empty Target: every backend's argv is byte for byte what it was before
// Options.Target existed.
func TestBwrapArgvWithoutATargetIsUnchanged(t *testing.T) {
	raw, err := os.ReadFile(filepath.Clean(argvGoldenFile))
	if err != nil {
		t.Fatalf("the golden file is missing; it is never regenerated: %v", err)
	}
	var want map[string][]string
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	got := wrapArgvGoldenCases(t)
	if len(want) != len(got) {
		t.Errorf("golden has %d cases, the test %d", len(want), len(got))
	}
	for name, w := range want {
		if g, ok := got[name]; !ok || !slices.Equal(g, w) {
			t.Errorf("%s: argv changed with no Target\n got: %q\nwant: %q", name, g, w)
		}
	}
}

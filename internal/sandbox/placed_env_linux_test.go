// SPDX-License-Identifier: Elastic-2.0

//go:build linux

package sandbox

import (
	"path"
	"slices"
	"strings"
	"testing"
)

// hostEnvWithTMPDIR is a host environment as MinimalEnv forwards it, with a
// TMPDIR that names a HOST directory.
var hostEnvWithTMPDIR = []string{"PATH=/usr/bin", "LANG=C.UTF-8", "TMPDIR=/home/op/tmp"}

// A run whose workspace is placed at a Target must not be handed the host's
// TMPDIR: it named a host directory that existed in the jail only because the
// workspace used to be mounted at its own path. The sandbox drops it, so every
// caller that sets Target gets the rule, not only adequacy. Without a Target
// the env is forwarded as it always was.
func TestBwrapDropsTheHostTMPDIROnlyForAPlacedRun(t *testing.T) {
	b := bwrapIsolator{}
	placed, err := b.Wrap("true", Options{Workspace: "/w", Target: WorkTarget}, hostEnvWithTMPDIR)
	if err != nil {
		t.Fatal(err)
	}
	if setsEnv(placed, "--setenv", "TMPDIR") {
		t.Errorf("a placed run was handed the host TMPDIR: %v", placed)
	}
	if !argvHas(placed, "--setenv", "LANG", "C.UTF-8") {
		t.Errorf("a placed run lost the rest of its env: %v", placed)
	}
	unplaced, err := b.Wrap("true", Options{Workspace: "/w"}, hostEnvWithTMPDIR)
	if err != nil {
		t.Fatal(err)
	}
	if !argvHas(unplaced, "--setenv", "TMPDIR", "/home/op/tmp") {
		t.Errorf("a run at its host path lost the host TMPDIR: %v", unplaced)
	}
}

func TestContainerDropsTheHostTMPDIROnlyForAPlacedRun(t *testing.T) {
	c := containerIsolator{runtime: "docker", image: "img"}
	placed, err := c.Wrap("true", Options{Workspace: "/w", Target: WorkTarget}, hostEnvWithTMPDIR)
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(placed, func(a string) bool { return strings.HasPrefix(a, "TMPDIR=") }) {
		t.Errorf("a placed run was handed the host TMPDIR: %v", placed)
	}
	if !argvHas(placed, "-e", "LANG=C.UTF-8") {
		t.Errorf("a placed run lost the rest of its env: %v", placed)
	}
	unplaced, err := c.Wrap("true", Options{Workspace: "/w"}, hostEnvWithTMPDIR)
	if err != nil {
		t.Fatal(err)
	}
	if !argvHas(unplaced, "-e", "TMPDIR=/home/op/tmp") {
		t.Errorf("a run at its host path lost the host TMPDIR: %v", unplaced)
	}
}

// setsEnv reports whether argv has flag followed by key (bwrap's
// --setenv KEY VALUE).
func setsEnv(argv []string, flag, key string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag && argv[i+1] == key {
			return true
		}
	}
	return false
}

// bwrapMountDests is every mount destination in a bwrap argv: the last
// operand of each flag that mounts something, read from bwrap's own syntax
// (flag, then its operands; the destination is the last).
func bwrapMountDests(t *testing.T, argv []string) []string {
	t.Helper()
	// operands each mounting flag takes; the destination is the last one.
	mounting := map[string]int{
		"--bind": 2, "--ro-bind": 2, "--ro-bind-try": 2, "--bind-try": 2, "--dev-bind": 2,
		"--tmpfs": 1, "--proc": 1, "--dev": 1, "--dir": 1, "--mqueue": 1,
		"--symlink": 2, "--overlay": 1, "--tmp-overlay": 1, "--ro-overlay": 1,
	}
	// operands of every other flag Wrap uses, so the walk stays aligned.
	other := map[string]int{
		"--unshare-all": 0, "--die-with-parent": 0, "--new-session": 0, "--clearenv": 0,
		"--share-net": 0, "--setenv": 2, "--unsetenv": 1, "--chdir": 1, "--overlay-src": 1,
	}
	var dests []string
	for i := 1; i < len(argv); i++ {
		a := argv[i]
		if a == "--" {
			return dests
		}
		if n, ok := mounting[a]; ok {
			dests = append(dests, argv[i+n])
			i += n
			continue
		}
		n, ok := other[a]
		if !ok {
			t.Fatalf("Wrap emitted bwrap flag %q that this walk does not know; add it to mounting (if it mounts something) or to other: %v", a, argv)
		}
		i += n
	}
	t.Fatalf("no -- in %v", argv)
	return nil
}

// jailOwnMounts is a hand-kept list of what Wrap mounts for itself, because
// the argv is built inline and there is no single list to derive it from. So
// this test derives it: it builds bwrap's argv for a representative set of
// options and fails on any mount destination that is neither the workspace
// target (or a path under it) nor the cache target, nor covered by
// jailOwnMounts. A mount added to Wrap fails here until the list knows it.
func TestJailOwnMountsCoversEveryMountWrapMakes(t *testing.T) {
	over := bwrapIsolator{overlay: true, overlayAt: true, lowers: newLowerCache(func(string) bool { return true })}
	plain := bwrapIsolator{}
	deps := []Bind{
		{Host: "/deps/vendor", Target: WorkTarget + "/vendor"},
		{Host: t.TempDir(), Target: WorkTarget + "/node_modules", PerEntry: true},
	}
	cacheDir := t.TempDir() // a frozen cache must be a directory the overlay can list
	cache := &CacheMount{Host: cacheDir, Target: CacheTarget, Env: "GOCACHE", Frozen: true}
	warm := &CacheMount{Host: cacheDir, Target: CacheTarget, Env: "GOCACHE"}
	for _, c := range []struct {
		name string
		b    bwrapIsolator
		opts Options
	}{
		{"plain", plain, Options{Workspace: "/w", Target: WorkTarget}},
		{"network", plain, Options{Workspace: "/w", Target: WorkTarget, Network: true}},
		{"dependency binds", plain, Options{Workspace: "/w", Target: WorkTarget, ReadOnlyBinds: deps}},
		{"overlaid dependency binds", over, Options{Workspace: "/w", Target: WorkTarget, ReadOnlyBinds: deps}},
		{"frozen cache", over, Options{Workspace: "/w", Target: WorkTarget, Cache: cache}},
		{"warming cache", over, Options{Workspace: "/w", Target: WorkTarget, Cache: warm}},
		{"seed with cache and binds", over, Options{Workspace: "/w", Target: WorkTarget, SeedLower: "/w", Cache: cache, ReadOnlyBinds: deps}},
	} {
		argv, err := c.b.Wrap("true", c.opts, []string{"PATH=/usr/bin"})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		for _, d := range bwrapMountDests(t, argv) {
			d = path.Clean(d)
			if within(d, WorkTarget) || (c.opts.Cache != nil && within(d, c.opts.Cache.Target)) {
				continue
			}
			if !slices.ContainsFunc(jailOwnMounts, func(r string) bool { return within(d, r) }) {
				t.Errorf("%s: Wrap mounts %s, which jailOwnMounts does not cover, so a workspace Target there would not be refused", c.name, d)
			}
		}
	}
}

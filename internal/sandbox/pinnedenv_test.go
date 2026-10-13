// SPDX-License-Identifier: Elastic-2.0

package sandbox

import (
	"slices"
	"strings"
	"testing"
)

// hostileHostEnv is a host env that tries to override every pin — derived
// from pinnedEnv, so a new pin is attacked without editing this file — plus a
// PATH (the container drops it, bwrap forwards it) and a key that must be
// forwarded untouched.
func hostileHostEnv() []string {
	env := []string{"PATH=/host/bin", "KEEP=1"}
	for _, p := range pinnedEnv(linuxJailDirs) {
		env = append(env, p.Key+"="+hostileValue(p.Key))
	}
	return env
}

func hostileValue(key string) string { return "hostile-" + key }

// envSettings collects every environment assignment in a wrapped argv, keyed
// by variable name, in argv order. The container form is `-e K=V` (flag "-e",
// one following argument); the bwrap form is `--setenv K V` (flag
// "--setenv", two following arguments).
func envSettings(argv []string, flag string) map[string][]string {
	out := map[string][]string{}
	for i := 0; i < len(argv); i++ {
		if argv[i] != flag {
			continue
		}
		switch flag {
		case "-e":
			if i+1 < len(argv) {
				k, v, _ := strings.Cut(argv[i+1], "=")
				out[k] = append(out[k], v)
			}
		case "--setenv":
			if i+2 < len(argv) {
				out[argv[i+1]] = append(out[argv[i+1]], argv[i+2])
			}
		}
	}
	return out
}

// assertPinsHold: every pinnedEnv key is set exactly once, to its pinned
// value, and no host value for it survives.
func assertPinsHold(t *testing.T, got map[string][]string) {
	t.Helper()
	for _, p := range pinnedEnv(linuxJailDirs) {
		if vs := got[p.Key]; !slices.Equal(vs, []string{p.Value}) {
			t.Errorf("%s set to %q, want exactly [%q]", p.Key, vs, p.Value)
		}
	}
}

func TestContainerWrapReadsPinnedEnv(t *testing.T) {
	argv, err := containerIsolator{runtime: "docker", image: "img"}.Wrap("true", Options{Workspace: "/w"}, hostileHostEnv())
	if err != nil {
		t.Fatal(err)
	}
	got := envSettings(argv, "-e")
	assertPinsHold(t, got)
	if _, ok := got["PATH"]; ok {
		t.Error("the host PATH must not reach the container")
	}
	if !slices.Equal(got["KEEP"], []string{"1"}) {
		t.Errorf("KEEP = %q, want forwarded once as 1", got["KEEP"])
	}
}

func TestIsPinned(t *testing.T) {
	for _, p := range pinnedEnv(linuxJailDirs) {
		if !isPinned(p.Key) {
			t.Errorf("isPinned(%q) = false", p.Key)
		}
	}
	if isPinned("KEEP") || isPinned("PATH") {
		t.Error("KEEP and PATH are not pins")
	}
}

// TestPinsCarryTheBackendsOwnDirs: HOME and TMPDIR are values each backend
// supplies, so the macOS jail can point them inside its workspace while
// bwrap and the container keep their tmpfs mounts.
func TestPinsCarryTheBackendsOwnDirs(t *testing.T) {
	got := map[string]string{}
	for _, p := range pinnedEnv(jailDirs{Home: "/ws/.corral-home", Tmp: "/ws/.corral-tmp"}) {
		got[p.Key] = p.Value
	}
	if got["HOME"] != "/ws/.corral-home" || got["TMPDIR"] != "/ws/.corral-tmp" {
		t.Errorf("HOME=%q TMPDIR=%q, want the dirs passed in", got["HOME"], got["TMPDIR"])
	}
	if got["GOTOOLCHAIN"] != "local" {
		t.Errorf("GOTOOLCHAIN=%q, want local on every backend", got["GOTOOLCHAIN"])
	}
}

// TestTMPDIRIsPinned: a host TMPDIR (MinimalEnv forwards one) names a host
// path that does not exist in the jail; it must be replaced, never forwarded.
func TestTMPDIRIsPinned(t *testing.T) {
	if !isPinned("TMPDIR") {
		t.Fatal("TMPDIR must be a pin")
	}
}

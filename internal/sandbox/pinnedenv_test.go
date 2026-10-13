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
	for _, p := range pinnedEnv {
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
	for _, p := range pinnedEnv {
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
	for _, p := range pinnedEnv {
		if !isPinned(p.Key) {
			t.Errorf("isPinned(%q) = false", p.Key)
		}
	}
	if isPinned("KEEP") || isPinned("PATH") {
		t.Error("KEEP and PATH are not pins")
	}
}

// offlinePinKeys are the pins that exist to keep a GRADED run off the
// network. They are looked up by key, not by position, so the test fails by
// name if a row is removed or renamed.
var offlinePinKeys = []string{"CARGO_NET_OFFLINE"}

// TestRustPinsAreInTheTable: the three Rust pins the jail sets. RUSTUP_HOME
// points at the system-wide rustup (the only one inside the /usr the jail
// mounts), CARGO_HOME at the writable tmpfs HOME, and the offline flag.
func TestRustPinsAreInTheTable(t *testing.T) {
	want := map[string]string{
		"RUSTUP_HOME":       "/usr/local/rustup",
		"CARGO_HOME":        "/home/agent/.cargo",
		"CARGO_NET_OFFLINE": "true",
	}
	for k, v := range want {
		found := false
		for _, p := range pinnedEnv {
			if p.Key == k {
				found = true
				if p.Value != v {
					t.Errorf("%s pinned to %q, want %q", k, p.Value, v)
				}
			}
		}
		if !found {
			t.Errorf("%s is not pinned", k)
		}
	}
}

// TestOfflinePinNeverReachesANetworkedJail is the part-one carry: the
// networked vendoring step (`cargo vendor`, run with Options.Network) must
// not be told CARGO_NET_OFFLINE, or it can never download anything. The host's
// own value must not leak into it either. Graded runs (Network false) get the
// pin, and the host cannot override it. Both backends read the one table.
func TestOfflinePinNeverReachesANetworkedJail(t *testing.T) {
	type backend struct {
		name string
		wrap func(Options, []string) ([]string, error)
		flag string
	}
	backends := []backend{
		{"bwrap", func(o Options, e []string) ([]string, error) { return (bwrapIsolator{}).Wrap("true", o, e) }, "--setenv"},
		{"container", func(o Options, e []string) ([]string, error) {
			return containerIsolator{runtime: "docker", image: "img"}.Wrap("true", o, e)
		}, "-e"},
	}
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			net, err := b.wrap(Options{Workspace: "/w", Network: true}, hostileHostEnv())
			if err != nil {
				t.Fatal(err)
			}
			got := envSettings(net, b.flag)
			for _, k := range offlinePinKeys {
				if vs, ok := got[k]; ok {
					t.Errorf("networked jail sets %s = %q; vendoring would be offline", k, vs)
				}
			}
			// The non-offline pins still hold on the networked jail.
			for _, p := range pinnedEnv {
				if slices.Contains(offlinePinKeys, p.Key) {
					continue
				}
				if vs := got[p.Key]; !slices.Equal(vs, []string{p.Value}) {
					t.Errorf("networked jail: %s = %q, want [%q]", p.Key, vs, p.Value)
				}
			}
			off, err := b.wrap(Options{Workspace: "/w"}, hostileHostEnv())
			if err != nil {
				t.Fatal(err)
			}
			assertPinsHold(t, envSettings(off, b.flag))
		})
	}
}

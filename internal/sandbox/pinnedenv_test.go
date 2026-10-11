// SPDX-License-Identifier: Elastic-2.0

package sandbox

import "testing"

// pinHostEnv is a host env that tries to override every pin, plus a PATH the
// container must drop and a key that must be forwarded untouched.
var pinHostEnv = []string{"HOME=/root", "GOTOOLCHAIN=auto", "PATH=/host/bin", "KEEP=1"}

// countPair counts how often key,value appear adjacent after flag in argv; for
// the container, pass flag "-e" and the joined "k=v" as value with key "".
func countAfter(argv []string, flag string, rest ...string) int {
	n := 0
	for i := 0; i+1+len(rest) <= len(argv); i++ {
		if argv[i] != flag {
			continue
		}
		ok := true
		for j, r := range rest {
			if argv[i+1+j] != r {
				ok = false
			}
		}
		if ok {
			n++
		}
	}
	return n
}

func TestContainerWrapReadsPinnedEnv(t *testing.T) {
	argv, err := containerIsolator{runtime: "docker", image: "img"}.Wrap("true", Options{Workspace: "/w"}, pinHostEnv)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pinnedEnv {
		if got := countAfter(argv, "-e", p.Key+"="+p.Value); got != 1 {
			t.Errorf("pin %s=%s appears %d times, want 1", p.Key, p.Value, got)
		}
	}
	for _, bad := range []string{"HOME=/root", "GOTOOLCHAIN=auto", "PATH=/host/bin"} {
		if countAfter(argv, "-e", bad) != 0 {
			t.Errorf("%s must not reach the container", bad)
		}
	}
	if countAfter(argv, "-e", "KEEP=1") != 1 {
		t.Error("KEEP=1 must be forwarded")
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

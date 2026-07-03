// SPDX-License-Identifier: Elastic-2.0

package sandbox

import (
	"strings"
	"testing"
)

// argvHas reports whether argv contains the consecutive token sequence want.
func argvHas(argv []string, want ...string) bool {
	for i := 0; i+len(want) <= len(argv); i++ {
		match := true
		for j, w := range want {
			if argv[i+j] != w {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func TestNoneWrapIsRawSh(t *testing.T) {
	argv, err := (noneIsolator{}).Wrap("echo hi", Options{Workspace: "/w"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(argv, " ") != "sh -c echo hi" {
		t.Fatalf("none should be a raw sh -c, got %v", argv)
	}
}

func TestResolveNoneRequiresUnsafeOverride(t *testing.T) {
	if _, err := Resolve(Config{Backend: "none"}); err == nil {
		t.Fatal("none without UnsafeHost must be rejected")
	}
	iso, err := Resolve(Config{Backend: "none", UnsafeHost: true})
	if err != nil || iso.Name() != "none" {
		t.Fatalf("none with override should resolve, got %v %v", iso, err)
	}
}

func TestResolveContainerNeedsImage(t *testing.T) {
	t.Setenv("CORRALAI_EXEC_IMAGE", "")
	// Set an explicit runtime so Resolve skips PATH detection; Preflight will
	// then fail on the empty image before attempting a LookPath on the runtime.
	t.Setenv("CORRALAI_EXEC_RUNTIME", "docker")
	_, err := Resolve(Config{Backend: "container"})
	if err == nil {
		t.Fatal("container backend with no image must error")
	}
	if !containsWord(err.Error(), "image") {
		t.Fatalf("error should mention image, got: %v", err)
	}
}

// containsWord is a simple substring check used in test assertions.
func containsWord(s, sub string) bool {
	return len(s) > 0 && len(sub) > 0 && (s == sub || len(s) >= len(sub) && containsSub(s, sub))
}

func containsSub(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestContainerWrap(t *testing.T) {
	c := containerIsolator{runtime: "docker", image: "img"}

	// Default: no network, workspace bound, caps dropped, HOME pinned.
	argv, err := c.Wrap("echo hi", Options{Workspace: "/ws"}, []string{"HOME=/root", "PATH=/usr/bin"})
	if err != nil {
		t.Fatal(err)
	}
	if !argvHas(argv, "--network=none") {
		t.Fatalf("expected --network=none by default, got %v", argv)
	}
	if !argvHas(argv, "--cap-drop=ALL") {
		t.Fatal("expected --cap-drop=ALL")
	}
	if !argvHas(argv, "-v", "/ws:/ws") {
		t.Fatal("expected workspace volume -v /ws:/ws")
	}
	if !argvHas(argv, "-w", "/ws") {
		t.Fatal("expected -w /ws")
	}
	if !argvHas(argv, "-e", "HOME=/home/agent") {
		t.Fatal("expected -e HOME=/home/agent")
	}
	// Host HOME must NOT be forwarded.
	if argvHas(argv, "-e", "HOME=/root") {
		t.Fatal("host HOME must not be forwarded as -e")
	}
	// PATH from env should be forwarded.
	if !argvHas(argv, "-e", "PATH=/usr/bin") {
		t.Fatal("expected PATH forwarded via -e")
	}
	// Command must be the final three elements.
	n := len(argv)
	if n < 3 || argv[n-3] != "sh" || argv[n-2] != "-c" || argv[n-1] != "echo hi" {
		t.Fatalf("argv should end with sh -c <command>, got %v", argv[n-3:])
	}

	// With network: --network=bridge.
	argv2, err := c.Wrap("echo hi", Options{Workspace: "/ws", Network: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !argvHas(argv2, "--network=bridge") {
		t.Fatalf("expected --network=bridge when Network is true, got %v", argv2)
	}
}

func TestResolveUnknownBackend(t *testing.T) {
	if _, err := Resolve(Config{Backend: "bogus"}); err == nil {
		t.Fatal("unknown backend should error")
	}
}

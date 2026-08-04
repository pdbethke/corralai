// SPDX-License-Identifier: Elastic-2.0

//go:build linux

package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBwrapWrapReadOnlyBinds(t *testing.T) {
	b := bwrapIsolator{}
	argv, err := b.Wrap("echo hi", Options{
		Workspace:     "/tmp/ws",
		ReadOnlyBinds: []Bind{{Host: "/proj/node_modules", Target: "/tmp/ws/node_modules"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "--ro-bind /proj/node_modules /tmp/ws/node_modules") {
		t.Fatalf("bwrap argv missing ro-bind: %v", argv)
	}
	// the dep bind must come AFTER the workspace bind so the mountpoint parent exists
	wsIdx := strings.Index(joined, "--bind /tmp/ws /tmp/ws")
	depIdx := strings.Index(joined, "--ro-bind /proj/node_modules")
	if wsIdx < 0 || depIdx < 0 || depIdx < wsIdx {
		t.Fatalf("dep bind must follow workspace bind: ws=%d dep=%d", wsIdx, depIdx)
	}
}

func TestContainerWrapReadOnlyBinds(t *testing.T) {
	c := containerIsolator{image: "img", runtime: "docker"}
	argv, err := c.Wrap("echo hi", Options{
		Workspace:     "/tmp/ws",
		ReadOnlyBinds: []Bind{{Host: "/proj/node_modules", Target: "/tmp/ws/node_modules"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "-v /proj/node_modules:/tmp/ws/node_modules:ro") {
		t.Fatalf("container argv missing ro volume: %v", argv)
	}
	// the -v bind flag must precede the image arg (docker/podman parse flags
	// before the positional image, and a bind flag placed after it would be
	// swallowed as a command arg instead of a mount)
	bindIdx := strings.Index(joined, "-v /proj/node_modules:/tmp/ws/node_modules:ro")
	imgIdx := strings.Index(joined, "img")
	if bindIdx < 0 || imgIdx < 0 || imgIdx < bindIdx {
		t.Fatalf("bind flag must precede the image arg: bind=%d img=%d argv=%v", bindIdx, imgIdx, argv)
	}
}

// TestPerEntryBindsSkipsCacheDirsKeepsBin pins the rule that unblocks auditing
// a JavaScript project at all. A dependency tree is not read-only in practice:
// vite/vitest write node_modules/.vite, jest and friends write .cache. Mounting
// the whole tree read-only makes that write fail with EROFS *after the tests
// pass*, and the audit can only report it as a failed baseline — a
// build/environment verdict on a project that is fine.
//
// So: every real entry is mounted (paths must stay identical — node resolves
// module realpaths, and a symlink farm hands the test file and the runner two
// different copies of the same package), dot-entries are skipped so a cache dir
// can be created fresh in the writable workspace, and .bin survives because the
// test command's executables live there.
func TestPerEntryBindsSkipsCacheDirsKeepsBin(t *testing.T) {
	host := t.TempDir()
	for _, d := range []string{"vitest", "@scope", ".bin", ".vite", ".cache"} {
		if err := os.MkdirAll(filepath.Join(host, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got := perEntryBinds(Bind{Host: host, Target: "/w/node_modules", PerEntry: true})

	targets := map[string]bool{}
	for _, b := range got {
		targets[b.Target] = true
	}
	for _, want := range []string{"/w/node_modules/vitest", "/w/node_modules/@scope", "/w/node_modules/.bin"} {
		if !targets[want] {
			t.Errorf("expected %s to be mounted; got %v", want, targets)
		}
	}
	for _, bad := range []string{"/w/node_modules/.vite", "/w/node_modules/.cache"} {
		if targets[bad] {
			t.Errorf("%s must NOT be mounted read-only: that reproduces the EROFS this exists to avoid", bad)
		}
	}
}

// TestPerEntryBindsUnreadableHostExpandsToNothing: a dep dir that cannot be
// read must not fail argv construction. The jail then lacks dependencies and
// the suite says so in its own words, which is a far better diagnosis than an
// opaque wrapper error.
func TestPerEntryBindsUnreadableHostExpandsToNothing(t *testing.T) {
	if got := perEntryBinds(Bind{Host: filepath.Join(t.TempDir(), "absent"), Target: "/w/node_modules"}); len(got) != 0 {
		t.Fatalf("expected no binds for an unreadable host, got %v", got)
	}
}

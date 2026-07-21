// SPDX-License-Identifier: Elastic-2.0

//go:build linux

package sandbox

import (
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
	if !strings.Contains(strings.Join(argv, " "), "-v /proj/node_modules:/tmp/ws/node_modules:ro") {
		t.Fatalf("container argv missing ro volume: %v", argv)
	}
}

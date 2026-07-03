// SPDX-License-Identifier: Elastic-2.0

//go:build linux

package sandbox

import "testing"

func TestBwrapWrapNetOffByDefault(t *testing.T) {
	argv, err := (bwrapIsolator{}).Wrap("echo hi", Options{Workspace: "/workspace"}, []string{"PATH=/usr/bin"})
	if err != nil {
		t.Fatal(err)
	}
	if !argvHas(argv, "--unshare-all") {
		t.Fatal("expected --unshare-all (net off)")
	}
	if argvHas(argv, "--share-net") {
		t.Fatal("did not expect --share-net when Network is false")
	}
	if !argvHas(argv, "--setenv", "PATH", "/usr/bin") {
		t.Fatal("expected env passed via --setenv")
	}
	if !argvHas(argv, "--bind", "/workspace", "/workspace") || !argvHas(argv, "--chdir", "/workspace") {
		t.Fatal("expected the workspace bound + chdir")
	}
	if !argvHas(argv, "--", "sh", "-c", rlimitPrelude+"echo hi") {
		t.Fatal("expected the command (with rlimit prelude) after --")
	}
	if !argvHas(argv, "--clearenv") {
		t.Fatal("expected --clearenv (the command must inherit no host env)")
	}
	if argvHas(argv, "--ro-bind-try", "/etc/resolv.conf", "/etc/resolv.conf") {
		t.Fatal("resolv.conf should not be bound when net is off")
	}
	if !argvHas(argv, "--setenv", "HOME", "/home/agent") {
		t.Fatal("expected a writable HOME set inside the jail")
	}
	if !argvHas(argv, "--tmpfs", "/home/agent") {
		t.Fatal("expected a tmpfs backing the jail HOME")
	}
}

func TestBwrapWrapNetOn(t *testing.T) {
	argv, err := (bwrapIsolator{}).Wrap("go build", Options{Workspace: "/w", Network: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !argvHas(argv, "--share-net") {
		t.Fatal("expected --share-net when Network is true")
	}
	if !argvHas(argv, "--ro-bind-try", "/etc/resolv.conf", "/etc/resolv.conf") {
		t.Fatal("expected resolv.conf bound for DNS when networked")
	}
}

func TestBwrapWrapRequiresWorkspace(t *testing.T) {
	if _, err := (bwrapIsolator{}).Wrap("x", Options{}, nil); err == nil {
		t.Fatal("expected an error when no workspace is set")
	}
}

func TestBwrapWrapRlimitPrelude(t *testing.T) {
	argv, err := (bwrapIsolator{}).Wrap("echo hi", Options{Workspace: "/w"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if last := argv[len(argv)-1]; last != rlimitPrelude+"echo hi" {
		t.Fatalf("final argv element = %q, want rlimitPrelude+%q", last, "echo hi")
	}
}

func TestBwrapWrapDropsHostHome(t *testing.T) {
	argv, err := (bwrapIsolator{}).Wrap("true", Options{Workspace: "/w"}, []string{"HOME=/root", "PATH=/usr/bin"})
	if err != nil {
		t.Fatal(err)
	}
	if argvHas(argv, "--setenv", "HOME", "/root") {
		t.Fatal("host HOME must not be forwarded into the jail")
	}
	if !argvHas(argv, "--setenv", "HOME", "/home/agent") {
		t.Fatal("jail HOME should be /home/agent")
	}
}

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
	// Loopback name resolution must work even with the net unshared: without
	// /etc/hosts, resolving "localhost" fails with EAI_AGAIN and any test runner
	// that coordinates workers over loopback (vitest, jest) dies before running a
	// single test — surfacing as COULD-NOT-GRADE, which reads like a broken
	// project rather than a jail gap. This binds names, not reachability.
	if !argvHas(argv, "--ro-bind-try", "/etc/hosts", "/etc/hosts") {
		t.Fatal("expected /etc/hosts bound so 'localhost' resolves inside the jail")
	}
	if !argvHas(argv, "--ro-bind-try", "/etc/nsswitch.conf", "/etc/nsswitch.conf") {
		t.Fatal("expected /etc/nsswitch.conf bound so the hosts file is consulted")
	}
	// The passwd database, same shape as the two above: a system database the
	// RUNTIME expects, whose absence fails at IMPORT rather than in a test.
	// Without it the jail's uid has no passwd entry and getpass.getuser() raises
	// `KeyError: getpwuid(): uid not found: 1000`. PyTorch calls exactly that
	// while computing its cache directory, at import — so any Python project
	// transitively importing torch or transformers dies before collection and
	// reports COULD-NOT-GRADE, indistinguishable from a broken project. Found by
	// auditing a third-party FastAPI service whose conftest imports the app.
	if !argvHas(argv, "--ro-bind-try", "/etc/passwd", "/etc/passwd") {
		t.Fatal("expected /etc/passwd bound so getpwuid()/getpass.getuser() resolve inside the jail")
	}
	if !argvHas(argv, "--ro-bind-try", "/etc/group", "/etc/group") {
		t.Fatal("expected /etc/group bound so gid lookups resolve alongside passwd")
	}
	// The hashes live in /etc/shadow and nothing a test runner does needs them.
	// Binding it would be a real leak, so this pins the absence rather than
	// leaving it to reviewer vigilance.
	if argvHas(argv, "--ro-bind-try", "/etc/shadow", "/etc/shadow") {
		t.Fatal("/etc/shadow must NEVER be bound into the jail")
	}
	// Debian/Ubuntu put installed gems in /var/lib/gems, OUTSIDE /usr. Without
	// this, `gem install rspec` is invisible in the jail while the executable in
	// /usr/local/bin is not, producing "can't find gem rspec-core ... with
	// executable rspec" — the whole RubyGems ecosystem, unusable. It hid because
	// Ruby had only been exercised against minitest, a default gem under /usr.
	if !argvHas(argv, "--ro-bind-try", "/var/lib/gems", "/var/lib/gems") {
		t.Fatal("expected /var/lib/gems bound so Debian-installed gems are visible in the jail")
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

// TestBwrapWrapPinsGoToolchainLocal pins Bug A's fix: the offline jail forces
// GOTOOLCHAIN=local so `go` cannot attempt a network toolchain download, and the
// host's own GOTOOLCHAIN never leaks through to override it.
func TestBwrapWrapPinsGoToolchainLocal(t *testing.T) {
	argv, err := (bwrapIsolator{}).Wrap("go test ./...", Options{Workspace: "/w"}, []string{"GOTOOLCHAIN=auto", "PATH=/usr/bin"})
	if err != nil {
		t.Fatal(err)
	}
	if !argvHas(argv, "--setenv", "GOTOOLCHAIN", "local") {
		t.Fatal("offline jail must pin GOTOOLCHAIN=local")
	}
	if argvHas(argv, "--setenv", "GOTOOLCHAIN", "auto") {
		t.Fatal("host GOTOOLCHAIN=auto must not leak into the jail")
	}
}

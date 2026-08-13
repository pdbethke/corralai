// SPDX-License-Identifier: Elastic-2.0

//go:build linux

package sandbox

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// rlimitPrelude is prepended to every bwrap command as a coarse fork-bomb and
// disk-write guard. The 2>/dev/null is intentional: the sh inside the jail may
// lack /proc/sys support; we tolerate failures silently. These are best-effort
// limits — the container/cgroup is responsible for hard enforcement.
const rlimitPrelude = "ulimit -u 1024 2>/dev/null; ulimit -f 4194304 2>/dev/null; "

// --- bwrap: Linux unprivileged namespace jail (default) ---

type bwrapIsolator struct{}

func (bwrapIsolator) Name() string { return "bwrap" }

func (bwrapIsolator) Preflight() error {
	if _, err := exec.LookPath("bwrap"); err != nil {
		return fmt.Errorf("bwrap not found on PATH: %w", err)
	}
	// Prove unprivileged user namespaces actually work — a version check alone
	// passes on kernels that compiled userns out, then every real run fails.
	out, err := exec.Command("bwrap", "--unshare-all", "--die-with-parent", "--new-session", "--ro-bind", "/", "/", "--", "true").CombinedOutput()
	if err != nil {
		return fmt.Errorf("bwrap cannot create a sandbox (user namespaces disabled?): %v: %s", err, bytes.TrimSpace(out))
	}
	return nil
}

func (bwrapIsolator) Wrap(command string, opts Options, env []string) ([]string, error) {
	if opts.Workspace == "" {
		return nil, errors.New("bwrap: workspace required")
	}
	argv := []string{"bwrap",
		"--unshare-all",     // user+pid+ipc+uts+cgroup+net namespaces; no privileged caps
		"--die-with-parent", // killed if the agent dies
		"--new-session",     // detach controlling terminal
		"--clearenv",        // start from nothing; only --setenv below reaches the command
	}
	if opts.Network {
		argv = append(argv, "--share-net") // undo --unshare-all's net isolation
	}
	for _, kv := range env {
		if i := strings.IndexByte(kv, '='); i > 0 {
			if kv[:i] == "HOME" || kv[:i] == "GOTOOLCHAIN" {
				continue // HOME/GOTOOLCHAIN are pinned below, not inherited from the host
			}
			argv = append(argv, "--setenv", kv[:i], kv[i+1:])
		}
	}
	// A writable HOME on tmpfs so toolchains that cache under $HOME (go build's
	// GOCACHE, npm, pip) work. Ephemeral per command; the workspace is for artifacts.
	argv = append(argv, "--setenv", "HOME", "/home/agent")
	// The jail is offline (no --share-net unless Options.Network). Pin
	// GOTOOLCHAIN=local so `go` NEVER tries to DOWNLOAD a go.mod-pinned toolchain
	// (which, with no network, either hangs or dies with a cryptic "toolchain not
	// available"). A repo requiring a newer Go than the jail's then fails with a
	// clear "go.mod requires go >= X" error instead — an honest, actionable signal.
	argv = append(argv, "--setenv", "GOTOOLCHAIN", "local")
	// Minimal read-only root (usrmerged Linux): the command can't read /home or
	// host secrets, only the toolchain. The workspace is the ONLY writable path.
	argv = append(argv,
		"--ro-bind", "/usr", "/usr",
		"--symlink", "usr/bin", "/bin",
		"--symlink", "usr/sbin", "/sbin",
		"--symlink", "usr/lib", "/lib",
		"--symlink", "usr/lib64", "/lib64",
		"--ro-bind-try", "/etc/ssl", "/etc/ssl",
		"--ro-bind-try", "/etc/ca-certificates", "/etc/ca-certificates",
		// Name resolution for LOOPBACK ONLY. `--unshare-all` gives the command its
		// own network namespace, which still has a working `lo` — but with no
		// /etc/hosts, resolving the literal name "localhost" fails with
		// `EAI_AGAIN`. Test runners that coordinate workers over loopback (vitest
		// and jest both do) then die before a single test runs, and the whole file
		// reports COULD-NOT-GRADE as a "build/environment failure" — indistinguishable
		// from a genuinely broken project. Binding these two files leaks no host
		// state worth having (they map localhost and say "check /etc/hosts first")
		// and grants no reachability: there is still no route off `lo`.
		"--ro-bind-try", "/etc/hosts", "/etc/hosts",
		"--ro-bind-try", "/etc/nsswitch.conf", "/etc/nsswitch.conf",
		// The passwd database, for the same reason and with the same shape as
		// the two lines above: it is a system database the RUNTIME expects to
		// exist, and its absence fails at import time rather than in a test.
		//
		// Without it the jail's uid has no passwd entry, so getpass.getuser()
		// raises `KeyError: getpwuid(): uid not found: 1000`. PyTorch calls
		// exactly that while computing its cache directory, at import — so any
		// Python project that transitively imports torch or transformers dies
		// before collection, and the file reports COULD-NOT-GRADE as a
		// "build/environment failure" indistinguishable from a broken project.
		// Found by auditing a third-party FastAPI service whose conftest imports
		// the app, which imports sentence-transformers.
		//
		// /etc/group comes along because the same lookups reach for it (Python's
		// getpass falls back through the group database on some paths, and Go's
		// os/user resolves gids the same way).
		//
		// This leaks usernames and home paths into the jail and nothing else.
		// /etc/shadow is NOT bound and must never be: the hashes live there, and
		// nothing a test runner does needs them.
		"--ro-bind-try", "/etc/passwd", "/etc/passwd",
		"--ro-bind-try", "/etc/group", "/etc/group",
		// Debian/Ubuntu install RubyGems OUTSIDE /usr — `gem install rspec`
		// lands in /var/lib/gems/<ver>, not /usr/lib/ruby/gems. Binding only
		// /usr therefore makes the entire RubyGems ecosystem invisible in the
		// jail while the executable in /usr/local/bin stays visible, so the
		// failure is the maximally confusing "can't find gem rspec-core (>=
		// 0.a) with executable rspec" — the binary is right there.
		//
		// It hid for a long time because Ruby auditing had only ever been
		// exercised against minitest, a DEFAULT gem shipped under
		// /usr/lib/ruby/gems, which /usr already covered. Every gem a project
		// actually installs was broken.
		//
		// Read-only, and only the gem root: this grants no writable state and
		// no credentials, exactly like the toolchain in /usr it sits beside.
		"--ro-bind-try", "/var/lib/gems", "/var/lib/gems",
		"--proc", "/proc",
		"--dev", "/dev",
		"--tmpfs", "/tmp",
		"--tmpfs", "/home/agent",
		"--bind", opts.Workspace, opts.Workspace, // bind AFTER tmpfs so a /tmp workspace survives
		"--chdir", opts.Workspace,
	)
	for _, bnd := range opts.ReadOnlyBinds {
		// A PerEntry bind becomes one --ro-bind per top-level entry, so the
		// parent stays a writable workspace directory a toolchain can create
		// its cache in. Ordering is load-bearing: these come AFTER the
		// workspace --bind above, because bwrap creates a mountpoint only when
		// its parent already exists and is writable.
		if bnd.PerEntry {
			for _, e := range perEntryBinds(bnd) {
				argv = append(argv, "--ro-bind", e.Host, e.Target)
			}
			continue
		}
		argv = append(argv, "--ro-bind", bnd.Host, bnd.Target)
	}
	if opts.Network {
		argv = append(argv, "--ro-bind-try", "/etc/resolv.conf", "/etc/resolv.conf")
	}
	return append(argv, "--", "sh", "-c", rlimitPrelude+command), nil
}

// newBwrapIsolator returns a preflighted bwrapIsolator, or an error if bwrap is
// unavailable on this host (missing binary or user namespaces disabled).
func newBwrapIsolator() (Isolator, error) {
	b := bwrapIsolator{}
	if err := b.Preflight(); err != nil {
		return nil, err
	}
	return b, nil
}

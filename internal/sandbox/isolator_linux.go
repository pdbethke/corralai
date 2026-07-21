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
			if kv[:i] == "HOME" {
				continue // host HOME is meaningless in the jail; a writable one is set below
			}
			argv = append(argv, "--setenv", kv[:i], kv[i+1:])
		}
	}
	// A writable HOME on tmpfs so toolchains that cache under $HOME (go build's
	// GOCACHE, npm, pip) work. Ephemeral per command; the workspace is for artifacts.
	argv = append(argv, "--setenv", "HOME", "/home/agent")
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
		"--proc", "/proc",
		"--dev", "/dev",
		"--tmpfs", "/tmp",
		"--tmpfs", "/home/agent",
		"--bind", opts.Workspace, opts.Workspace, // bind AFTER tmpfs so a /tmp workspace survives
		"--chdir", opts.Workspace,
	)
	for _, bnd := range opts.ReadOnlyBinds {
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

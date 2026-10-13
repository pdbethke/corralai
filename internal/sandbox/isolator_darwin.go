// SPDX-License-Identifier: Elastic-2.0

//go:build darwin

package sandbox

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// rlimitPreludeDarwin is prepended to every sandbox-exec command as a coarse
// fork-bomb and disk-write guard, mirroring rlimitPrelude in
// isolator_linux.go (same -u/-f values, kept in sync by hand since the two
// files live under different //go:build tags and can't share a const). The
// 2>/dev/null is intentional: sh inside the jail may reject a limit on some
// macOS configurations; we tolerate failures silently. Best-effort only —
// sandbox-exec has no cgroup-equivalent hard backstop on macOS.
const rlimitPreludeDarwin = "ulimit -u 1024 2>/dev/null; ulimit -f 4194304 2>/dev/null; "

type sandboxExecIsolator struct{}

func newSandboxExecIsolator() (Isolator, error) {
	s := sandboxExecIsolator{}
	if err := s.Preflight(); err != nil {
		return nil, err
	}
	return s, nil
}

func (sandboxExecIsolator) Name() string { return "sandbox-exec" }

// Preflight checks the binary exists. Whether the generated profile actually
// isolates and runs is the shared self-test's job (selftest.go), which runs
// the real Wrap → Run path: the old `(allow default)` probe passed while the
// real profile was invalid.
func (sandboxExecIsolator) Preflight() error {
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		return fmt.Errorf("sandbox-exec not found on PATH: %w", err)
	}
	return nil
}

// jailDirs puts the jail's HOME and TMPDIR inside the workspace:
// sandbox-exec has no tmpfs, and the workspace is the only writable tree.
// workspace must already be resolved.
func (sandboxExecIsolator) jailDirs(workspace string) jailDirs {
	return jailDirs{Home: filepath.Join(workspace, ".corral-home"), Tmp: filepath.Join(workspace, ".corral-tmp")}
}

// Wrap runs command under sandbox-exec with the profile sbplProfile builds,
// starting from an EMPTY environment (`/usr/bin/env -i`) and setting only the
// forwarded variables and the pins, which is bwrap's --clearenv/--setenv.
//
// Every path is resolved before it reaches the profile: SBPL matches
// canonical paths, and os.MkdirTemp("") workspaces live under /var/folders,
// a symlink into /private/var.
func (s sandboxExecIsolator) Wrap(command string, opts Options, env []string) ([]string, error) {
	if opts.Workspace == "" {
		return nil, errors.New("sandbox-exec: workspace required")
	}
	ws := resolved(opts.Workspace)
	dirs := s.jailDirs(ws)

	// The operator's REAL home, not the env's HOME (which the pins replace,
	// and which sudo or a CI runner may have pointed elsewhere).
	opHome := ""
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		opHome = resolved(h)
	}
	ro := make([]string, 0, len(opts.ReadOnlyBinds))
	for _, bnd := range opts.ReadOnlyBinds {
		// macOS has no bind mounts: the command reads the dir in place at
		// Host, so Target is unused here.
		ro = append(ro, resolved(bnd.Host))
	}
	profile, err := sbplProfile(sbplInput{
		Workspace:    ws,
		OperatorHome: opHome,
		ReadOnly:     ro,
		Masks:        secretMasks(wholeBoundTrees),
		Network:      opts.Network,
	})
	if err != nil {
		return nil, err
	}

	argv := []string{"sandbox-exec", "-p", profile, "/usr/bin/env", "-i"}
	for _, kv := range env {
		if i := strings.IndexByte(kv, '='); i > 0 && kv[0] != '-' && !isPinned(kv[:i]) {
			argv = append(argv, kv)
		}
	}
	for _, p := range pinnedEnv(dirs) {
		argv = append(argv, p.Key+"="+p.Value)
	}
	// HOME and TMPDIR are per-command, as bwrap's tmpfs is: cleared before the
	// command and removed after it (the EXIT trap keeps the command's exit
	// status). The fetch step runs several commands in one staging dir that
	// later seeds the audit, so nothing may carry between them. Go's module
	// cache is read-only, so make it writable before rm. A SIGKILL on timeout
	// skips the trap; the next command's pre-clear covers that.
	hq, tq := shQuote(dirs.Home), shQuote(dirs.Tmp)
	clean := "chmod -R u+w " + hq + " " + tq + " 2>/dev/null; rm -rf " + hq + " " + tq
	mkdirs := clean + "; mkdir -p " + hq + " " + tq + "; trap '" + strings.ReplaceAll(clean, "'", `'\''`) + "' EXIT; "
	return append(argv, "/bin/sh", "-c", rlimitPreludeDarwin+mkdirs+command), nil
}

//go:build darwin

package sandbox

import (
	"errors"
	"fmt"
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

func (sandboxExecIsolator) Preflight() error {
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		return fmt.Errorf("sandbox-exec not found on PATH: %w", err)
	}
	// Verify that we can execute a simple sandbox-exec command
	out, err := exec.Command("sandbox-exec", "-p", "(version 1) (allow default)", "true").CombinedOutput()
	if err != nil {
		return fmt.Errorf("sandbox-exec preflight failed: %v: %s", err, string(out))
	}
	return nil
}

func (sandboxExecIsolator) Wrap(command string, opts Options, env []string) ([]string, error) {
	if opts.Workspace == "" {
		return nil, errors.New("sandbox-exec: workspace required")
	}

	// HOME comes from the (already minimal, secret-free) env. We deny reads of
	// it below so a command can't lift the operator's credentials.
	home := ""
	for _, kv := range env {
		if strings.HasPrefix(kv, "HOME=") {
			home = strings.TrimPrefix(kv, "HOME=")
			break
		}
	}

	var sb strings.Builder
	sb.WriteString("(version 1)\n(deny default)\n")
	sb.WriteString("(allow process*)\n")
	sb.WriteString("(allow signal*)\n")
	sb.WriteString("(allow sysctl*)\n")

	// Deny-by-default reads (matching Linux bwrap). Allow ONLY the toolchain
	// paths a build/test needs plus the workspace — not the whole host FS.
	// macOS /etc is a symlink to /private/etc, so allow the real /private
	// paths.
	for _, p := range []string{"/usr", "/usr/local", "/bin", "/sbin", "/System/Library", "/Library/Developer", "/opt/homebrew", "/private/etc/ssl", "/private/etc/ca-certificates"} {
		sb.WriteString(fmt.Sprintf("(allow file-read* (subpath %q))\n", p))
	}

	// Belt-and-braces: explicitly deny the operator's secret stores so a
	// hijacked agent can't read them even if a future change widens the
	// allowlist above — matching bwrap, which never binds $HOME into the
	// jail. Last matching rule wins in SBPL, so these denies override any
	// allow above, and the workspace re-allow below overrides the $HOME deny
	// when the workspace lives under $HOME. Now redundant with deny-by-default
	// but kept as defense in depth.
	if home != "" {
		sb.WriteString(fmt.Sprintf("(deny file-read* (subpath %q))\n", home))
	}
	sb.WriteString("(deny file-read* (subpath \"/Library/Keychains\"))\n")
	sb.WriteString("(deny file-read* (subpath \"/private/var/db/dslocal\"))\n")
	// Re-allow reads of the workspace itself (even if it lives under $HOME).
	sb.WriteString(fmt.Sprintf("(allow file-read* (subpath %q))\n", opts.Workspace))

	// Writable paths. Unlike bwrap (which gives the jail its own private
	// tmpfs at /tmp via --tmpfs), sandbox-exec has no per-run tmpfs primitive:
	// SBPL grants access to real host paths, so there is no way to hand the
	// command an isolated /tmp short of a bind-mount facility macOS doesn't
	// expose here. We therefore do NOT allow writes to the shared host
	// /tmp, /var/tmp, or /private/tmp — a command that wants scratch space
	// gets a per-run directory under the workspace instead (tmpDir below),
	// and we point TMPDIR at it so well-behaved toolchains (go, node, mktemp,
	// python's tempfile) pick it up automatically. This is a WEAKER guarantee
	// than bwrap's tmpfs for tools that hardcode "/tmp/..." instead of
	// honoring TMPDIR — those will simply fail closed (deny-by-default) here
	// rather than write to a shared host path.
	tmpDir := filepath.Join(opts.Workspace, ".corral-tmp")
	sb.WriteString(fmt.Sprintf("(allow file-write* (subpath %q))\n", opts.Workspace))

	if opts.Network {
		sb.WriteString("(allow network*)\n")
		sb.WriteString("(allow mach-lookup)\n")
	}

	profile := sb.String()

	tmpPrelude := fmt.Sprintf("mkdir -p %q 2>/dev/null; export TMPDIR=%q; ", tmpDir, tmpDir)
	argv := []string{"sandbox-exec", "-p", profile, "sh", "-c", rlimitPreludeDarwin + tmpPrelude + command}
	return argv, nil
}

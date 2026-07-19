// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/sandbox"
)

// bwrapJail implements Jail over sandbox.Run, using backend — which MUST be a
// real isolation backend resolved via sandbox.Resolve. It writes the candidate
// file set into a fresh, disposable workspace and runs testCmd inside it.
//
// LOAD-BEARING CONTRACT (mirrors internal/brain/gate.go's jailAdapter):
// RunTest reports passed=true ONLY on a genuine sandbox.Result.ExitCode == 0.
// A nil backend, a timed-out run, or a run that could not be started at all
// (sandbox.Result.Err set) NEVER reads as passed — RunTest returns a non-nil
// error in those cases instead of (true, nil) or a silently-false pass. That
// interpretation itself lives in sandbox.RunGuarded, the single home of the
// "a failed run must not read as success" invariant shared with
// internal/brain/gate.go's jailAdapter.
type bwrapJail struct {
	backend sandbox.Isolator
	timeout time.Duration
}

// NewJail builds the real bwrap-sandboxed Jail for the adequacy scorer.
// backend must be resolved via sandbox.Resolve — never construct an
// alternate, weaker isolation path here. A nil backend is accepted (RunTest
// will refuse to run rather than fall back to unsandboxed execution).
func NewJail(backend sandbox.Isolator, timeout time.Duration) Jail {
	return bwrapJail{backend: backend, timeout: timeout}
}

// RunTest writes files into a fresh temp workspace and runs testCmd inside
// the jail. It refuses to run at all when backend is nil — corral never
// falls back to running untrusted test+code unsandboxed.
func (j bwrapJail) RunTest(ctx context.Context, files map[string]string, testCmd []string) (bool, error) {
	if j.backend == nil {
		return false, errors.New("adequacy: no sandbox backend — refusing to run untrusted test+code unsandboxed")
	}
	if len(testCmd) == 0 {
		return false, errors.New("adequacy: empty test command")
	}

	dir, err := os.MkdirTemp("", "corral-adequacy-*")
	if err != nil {
		return false, fmt.Errorf("adequacy: create workspace: %w", err)
	}
	defer os.RemoveAll(dir) // #nosec G104 -- best-effort cleanup of our own disposable temp dir

	// World-readable/traversable perms (not the Go default 0600/0700).
	//
	// WHY: the container backend (internal/sandbox/container.go) always runs
	// with --cap-drop=ALL, which strips CAP_DAC_OVERRIDE, and the standard
	// language images (python:slim, node:slim, etc.) default to a container
	// user of root — but that "root" is a *different* uid in the container's
	// user namespace than the host uid that owns this MkdirTemp workspace.
	// Without CAP_DAC_OVERRIDE, that container-root is subject to ordinary
	// Unix permission checks, so it cannot open a 0600 file or traverse a
	// 0700 dir owned by a different uid: every --jail container run failed
	// to even read its own workspace before this fix (confirmed by hand
	// against a live docker run — PermissionError during pytest's own config
	// discovery). The bwrap backend runs the sandboxed process as the SAME
	// host uid, so it never depended on the restrictive perms for isolation
	// in the first place; loosening them costs it nothing.
	//
	// We chose "make the workspace world-readable" over "run the container
	// as --user <hostuid>:<hostgid>" because the latter is fragile across
	// images (many images don't tolerate an arbitrary non-root uid, and
	// their entrypoints/tmpfs HOME may not be writable by it) and actively
	// dangerous on podman rootless, which already remaps the container's
	// root to the host uid via its own user namespace — forcing --user
	// there would double-map and could break a currently-working case.
	// World-readable perms work identically regardless of runtime, image,
	// or uid-mapping scheme.
	//
	// Exposure this accepts: for the few hundred milliseconds this scratch
	// dir exists, its contents (the operator's OWN code-under-audit, not a
	// secret) are readable by any other local user on a shared host — a
	// non-issue on a single-user dev box or CI runner, and it does not
	// change what the *sandbox* isolates (network, read-only rootfs,
	// cap-drop, and the anti-escape path guard below are untouched). This
	// scope is deliberately narrow: only this disposable adequacy workspace
	// gets loosened perms, nowhere else in the codebase.
	if err := os.Chmod(dir, 0o755); err != nil { // #nosec G302 -- deliberately world-readable, see comment above
		return false, fmt.Errorf("adequacy: chmod workspace: %w", err)
	}

	for path, content := range files {
		// #nosec G304 -- path is one of corral's own synthetic filenames (mutant
		// filenames / base fixture keys), not attacker-controlled; still cleaned
		// via filepath.Clean and confined under dir below.
		clean := filepath.Clean(path)
		if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || filepath.IsAbs(clean) {
			return false, fmt.Errorf("adequacy: refusing to write file outside workspace: %q", path)
		}
		full := filepath.Join(dir, clean)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil { // #nosec G301 -- deliberately world-readable, see comment above
			return false, fmt.Errorf("adequacy: create parent dirs for %q: %w", path, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil { // #nosec G306 -- deliberately world-readable, see comment above
			return false, fmt.Errorf("adequacy: write %q: %w", path, err)
		}
	}

	res, err := sandbox.RunGuarded(ctx, strings.Join(testCmd, " "), sandbox.Options{
		Workspace: dir,
		Backend:   j.backend,
		Network:   false,
		Timeout:   j.timeout,
	})
	if err != nil {
		return false, err
	}
	return res.ExitCode == 0, nil
}

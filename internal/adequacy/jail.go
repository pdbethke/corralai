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

// ErrTestTimeout is the sentinel RunTest wraps and returns when a run did not
// finish within its timeout (sandbox.Result.TimedOut). It lets a caller
// distinguish "the run hung" from any other infra failure via errors.Is,
// WITHOUT changing the load-bearing contract: a timed-out run still returns
// (passed=false, err!=nil) — this only makes that error identifiable, never
// makes a timeout read as success.
var ErrTestTimeout = errors.New("adequacy: test run timed out")

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

// NewEnumerator builds the real bwrap-sandboxed Enumerator for the tests×
// mutants matrix. Same backend/timeout contract as NewJail — in fact the
// SAME concrete type (bwrapJail) satisfies both interfaces, so a caller
// wiring both a Jail and an Enumerator off one backend gets identical
// workspace/perm handling for each.
func NewEnumerator(backend sandbox.Isolator, timeout time.Duration) Enumerator {
	return bwrapJail{backend: backend, timeout: timeout}
}

// writeWorkspace materializes files into a fresh, disposable temp directory,
// with the SAME anti-traversal guard and backend-conditioned perms RunTest
// and Enumerate both need. The caller owns cleanup (os.RemoveAll on the
// returned dir) and running whatever command it wants inside it.
//
// Workspace perms are the Go-default LOCKED-DOWN 0700/0600 by default, and
// loosened to world-readable (0755/0644) ONLY for the container backend.
//
// WHY the container needs it: internal/sandbox/container.go always runs
// with --cap-drop=ALL, which strips CAP_DAC_OVERRIDE, and the standard
// language images (python:slim, node:slim, …) default to a container user
// of root — but that "root" is a *different* uid in the container's user
// namespace than the host uid that owns this MkdirTemp workspace. Without
// CAP_DAC_OVERRIDE, that container-root is subject to ordinary Unix
// permission checks, so it cannot open a 0600 file or traverse a 0700 dir
// owned by a different uid: every --jail container run failed to even read
// its own workspace before this (confirmed by hand against a live docker
// run — PermissionError during pytest's own config discovery). We loosen
// the perms rather than run the container as --user <hostuid> because
// --user is fragile across images (many don't tolerate an arbitrary
// non-root uid) and double-maps dangerously on podman rootless.
//
// WHY bwrap stays locked down: bwrap runs the sandboxed process as the
// SAME host uid, so it reads 0700/0600 fine and never needed the loosening.
// Loosening it there would be gratuitous — on a shared host it would expose
// the operator's code-under-audit + tests to any other local user for the
// lifetime of the run, for no benefit. So the exposure is confined to the
// container backend, which is the only one that requires it.
//
// Either way the loosening is read-only (never world-WRITABLE, so no
// mid-run tampering), touches only this disposable adequacy workspace, and
// changes nothing the *sandbox* isolates (network, read-only rootfs,
// cap-drop, and the anti-escape path guard below are untouched). No secret
// is ever written here — only the operator's code, tests, and mutants.
func (j bwrapJail) writeWorkspace(files map[string]string) (dir string, err error) {
	if j.backend == nil {
		return "", errors.New("adequacy: no sandbox backend — refusing to run untrusted test+code unsandboxed")
	}
	dir, err = os.MkdirTemp("", "corral-adequacy-*")
	if err != nil {
		return "", fmt.Errorf("adequacy: create workspace: %w", err)
	}

	dirPerm, filePerm := os.FileMode(0o700), os.FileMode(0o600)
	if j.backend.Name() == "container" {
		dirPerm, filePerm = 0o755, 0o644
	}
	if err := os.Chmod(dir, dirPerm); err != nil { // #nosec G302 -- 0700 default; 0755 only for the container backend, see comment above
		os.RemoveAll(dir) // #nosec G104 -- best-effort cleanup on our own failure path
		return "", fmt.Errorf("adequacy: chmod workspace: %w", err)
	}

	for path, content := range files {
		// #nosec G304 -- path is one of corral's own synthetic filenames (mutant
		// filenames / base fixture keys), not attacker-controlled; still cleaned
		// via filepath.Clean and confined under dir below.
		clean := filepath.Clean(path)
		if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || filepath.IsAbs(clean) {
			os.RemoveAll(dir) // #nosec G104 -- best-effort cleanup on our own failure path
			return "", fmt.Errorf("adequacy: refusing to write file outside workspace: %q", path)
		}
		full := filepath.Join(dir, clean)
		if err := os.MkdirAll(filepath.Dir(full), dirPerm); err != nil { // #nosec G301 -- 0700 default; 0755 only for the container backend, see comment above
			os.RemoveAll(dir) // #nosec G104 -- best-effort cleanup on our own failure path
			return "", fmt.Errorf("adequacy: create parent dirs for %q: %w", path, err)
		}
		if err := os.WriteFile(full, []byte(content), filePerm); err != nil { // #nosec G306 -- 0600 default; 0644 only for the container backend, see comment above
			os.RemoveAll(dir) // #nosec G104 -- best-effort cleanup on our own failure path
			return "", fmt.Errorf("adequacy: write %q: %w", path, err)
		}
	}
	return dir, nil
}

// RunTest writes files into a fresh temp workspace and runs testCmd inside
// the jail. It refuses to run at all when backend is nil — corral never
// falls back to running untrusted test+code unsandboxed.
func (j bwrapJail) RunTest(ctx context.Context, files map[string]string, testCmd []string) (bool, error) {
	if len(testCmd) == 0 {
		return false, errors.New("adequacy: empty test command")
	}
	dir, err := j.writeWorkspace(files)
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(dir) // #nosec G104 -- best-effort cleanup of our own disposable temp dir

	res, err := sandbox.RunGuarded(ctx, strings.Join(testCmd, " "), sandbox.Options{
		Workspace: dir,
		Backend:   j.backend,
		Network:   false,
		Timeout:   j.timeout,
	})
	if err != nil {
		if res.TimedOut {
			return false, fmt.Errorf("%w: %s", ErrTestTimeout, res.Err)
		}
		return false, err
	}
	return res.ExitCode == 0, nil
}

// Enumerate is RunTest's stdout-returning sibling: same disposable
// workspace/perms/anti-traversal handling (writeWorkspace), but reports
// sandbox.Result.Output instead of collapsing the run to a bool. An empty
// output on a real (non-error) run is a legitimate "no tests" answer, not a
// failure — only a genuine timeout or infra failure to start the run
// returns a non-nil error, mirroring RunTest's own contract.
func (j bwrapJail) Enumerate(ctx context.Context, files map[string]string, cmd []string) (string, error) {
	if len(cmd) == 0 {
		return "", errors.New("adequacy: empty list command")
	}
	dir, err := j.writeWorkspace(files)
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir) // #nosec G104 -- best-effort cleanup of our own disposable temp dir

	res, err := sandbox.RunGuarded(ctx, strings.Join(cmd, " "), sandbox.Options{
		Workspace: dir,
		Backend:   j.backend,
		Network:   false,
		Timeout:   j.timeout,
	})
	if err != nil {
		if res.TimedOut {
			return "", fmt.Errorf("%w: %s", ErrTestTimeout, res.Err)
		}
		return "", err
	}
	return res.Output, nil
}

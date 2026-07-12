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

	for path, content := range files {
		// #nosec G304 -- path is one of corral's own synthetic filenames (mutant
		// filenames / base fixture keys), not attacker-controlled; still cleaned
		// via filepath.Clean and confined under dir below.
		clean := filepath.Clean(path)
		if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || filepath.IsAbs(clean) {
			return false, fmt.Errorf("adequacy: refusing to write file outside workspace: %q", path)
		}
		full := filepath.Join(dir, clean)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			return false, fmt.Errorf("adequacy: create parent dirs for %q: %w", path, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
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

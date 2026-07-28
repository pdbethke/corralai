// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// WorkspaceRunner is a Jail (and Enumerator) that runs against a REAL
// checkout instead of building an isolated workspace: it writes each file
// from the mutant map over the tree, runs the command there, and restores.
//
// It is the substrate for running inside a CI job — a GitHub Actions runner
// is already an ephemeral, isolated VM with the repository checked out and
// the project's own toolchain installed, which is precisely what the bwrap
// jail laboriously reconstructs. The runner IS the isolation boundary, so
// there is nothing to rebuild.
//
// It is NOT a substitute for the jail on a developer's machine or on a
// hosted service running someone else's code: it provides no isolation of
// its own. Use it only where the surrounding environment is already
// disposable.
type WorkspaceRunner struct {
	root string
}

// NewWorkspaceRunner returns a WorkspaceRunner rooted at root, an existing
// checkout directory.
func NewWorkspaceRunner(root string) *WorkspaceRunner { return &WorkspaceRunner{root: root} }

// Verify reports whether the workspace is usable before the first job. It
// exists because a mutant left behind by a crashed run would silently poison
// every later job, and on an ephemeral runner nobody would ever see it.
func (w *WorkspaceRunner) Verify() error {
	fi, err := os.Stat(w.root)
	if err != nil {
		return fmt.Errorf("adequacy: workspace %s: %w", w.root, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("adequacy: workspace %s is not a directory", w.root)
	}
	return nil
}

// resolve confines a repo-relative key to the checkout. The keys come from
// repo-relative candidate paths, but this is the one place a bad key would
// write to the runner's filesystem outside the repository, so it is checked
// here rather than trusted.
func (w *WorkspaceRunner) resolve(rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("adequacy: workspace path %q is absolute", rel)
	}
	full := filepath.Join(w.root, filepath.FromSlash(rel))
	cleanRoot := filepath.Clean(w.root) + string(os.PathSeparator)
	if !strings.HasPrefix(filepath.Clean(full)+string(os.PathSeparator), cleanRoot) {
		return "", fmt.Errorf("adequacy: workspace path %q escapes the checkout", rel)
	}
	return full, nil
}

// savedFile is one entry of the apply/restore ledger: the resolved path plus
// enough state to put the checkout back exactly as it was.
type savedFile struct {
	path     string
	original []byte
	existed  bool
}

// applyFiles overlays files onto the checkout and returns a restore func that
// undoes exactly that overlay: an existing file gets its original bytes
// written back; a file that did not exist gets removed. The caller MUST
// invoke restore via defer, before checking any error from applyFiles itself
// for the entries that did get applied — the ledger only contains entries
// that were actually written, so a partial failure still unwinds cleanly.
//
// This is the single place the crash-safety guarantee is written: RunTest
// and Enumerate both call it, so a failing command, a timeout, or a panic in
// either method restores the tree the same way.
func (w *WorkspaceRunner) applyFiles(files map[string]string) (restore func(), err error) {
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	// Sorted for a deterministic apply order, so a failure midway leaves a
	// reproducible state rather than a map-order-dependent one.
	sort.Strings(keys)

	var ledger []savedFile
	restore = func() {
		// Reverse order, so nested creations unwind cleanly.
		for i := len(ledger) - 1; i >= 0; i-- {
			s := ledger[i]
			if s.existed {
				_ = os.WriteFile(s.path, s.original, 0o600)
				continue
			}
			_ = os.Remove(s.path)
		}
	}

	for _, rel := range keys {
		full, rerr := w.resolve(rel)
		if rerr != nil {
			return restore, rerr
		}
		orig, rerr := os.ReadFile(full) // #nosec G304 -- confined by resolve above
		existed := rerr == nil
		if rerr != nil && !os.IsNotExist(rerr) {
			return restore, fmt.Errorf("adequacy: reading %s: %w", rel, rerr)
		}
		if !existed {
			if merr := os.MkdirAll(filepath.Dir(full), 0o750); merr != nil {
				return restore, fmt.Errorf("adequacy: creating %s: %w", rel, merr)
			}
		}
		ledger = append(ledger, savedFile{path: full, original: orig, existed: existed})
		if werr := os.WriteFile(full, []byte(files[rel]), 0o600); werr != nil {
			return restore, fmt.Errorf("adequacy: writing %s: %w", rel, werr)
		}
	}
	return restore, nil
}

// RunTest applies files over the checkout, runs testCmd in it, and restores.
//
// Restoration runs via defer so it survives a failing command, a timeout, and
// a panic. A file that did not exist before is REMOVED rather than left as a
// stray, because a leftover file is indistinguishable from part of the repo.
func (w *WorkspaceRunner) RunTest(ctx context.Context, files map[string]string, testCmd []string) (bool, error) {
	if len(testCmd) == 0 {
		return false, errors.New("adequacy: workspace runner needs a test command")
	}

	restore, err := w.applyFiles(files)
	defer restore()
	if err != nil {
		return false, err
	}

	cmd := exec.CommandContext(ctx, testCmd[0], testCmd[1:]...) // #nosec G204 -- the project's own test command, supplied by its workflow
	cmd.Dir = w.root
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return false, ErrTestTimeout
	}
	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			return false, nil // a non-zero exit is a RESULT, not an error
		}
		return false, fmt.Errorf("adequacy: running the test command: %w", runErr)
	}
	return true, nil
}

// Enumerate applies files over the checkout, runs cmd in it capturing
// stdout, and restores — the same apply/run/restore discipline RunTest uses,
// via the same applyFiles helper, so a matrix enumeration leaves the tree in
// exactly the state RunTest would. A non-zero exit is a RESULT here too (the
// caller wants whatever the command printed, e.g. a test listing that a
// mutant made incomplete), not an error.
func (w *WorkspaceRunner) Enumerate(ctx context.Context, files map[string]string, cmd []string) (string, error) {
	if len(cmd) == 0 {
		return "", errors.New("adequacy: workspace runner needs a command")
	}

	restore, err := w.applyFiles(files)
	defer restore()
	if err != nil {
		return "", err
	}

	var stdout bytes.Buffer
	c := exec.CommandContext(ctx, cmd[0], cmd[1:]...) // #nosec G204 -- the project's own list command, supplied by its workflow
	c.Dir = w.root
	c.Stdout = &stdout
	runErr := c.Run()
	if ctx.Err() != nil {
		return stdout.String(), ErrTestTimeout
	}
	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			return stdout.String(), nil // a non-zero exit is a RESULT, not an error
		}
		return stdout.String(), fmt.Errorf("adequacy: running the enumerate command: %w", runErr)
	}
	return stdout.String(), nil
}

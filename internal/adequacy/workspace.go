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
	"time"
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
	root    string
	timeout time.Duration
}

// defaultWorkspaceTimeout is the wall-clock bound a WorkspaceRunner built
// with timeout <= 0 uses. It mirrors the jail's own default for an
// unspecified timeout (sandbox.RunGuarded substitutes 60s when
// Options.Timeout is zero), so neither substrate can be constructed with "no
// bound at all". In practice the value is always supplied: cmd/corral plumbs
// the run's --timeout (defaulting to 10 minutes) into NewWorkspaceRunner from
// the same place it plumbs it into NewJail.
const defaultWorkspaceTimeout = 60 * time.Second

// NewWorkspaceRunner returns a WorkspaceRunner rooted at root, an existing
// checkout directory, bounding every command it runs to timeout.
//
// The bound is not optional decoration. The jail path passes its construction
// timeout into every sandbox run, so baseline, canary and mutants are all
// bounded there; Score only wraps MUTANT runs in a deadline of its own, and
// on `certify --repo` the surrounding ctx is context.Background() with no
// per-job deadline. Without this field a deadlocking baseline suite — more
// plausible here than in the jail, because this substrate has network — would
// hang the CI job forever instead of producing ErrTestTimeout.
//
// timeout <= 0 means defaultWorkspaceTimeout, never "unbounded".
func NewWorkspaceRunner(root string, timeout time.Duration) *WorkspaceRunner {
	if timeout <= 0 {
		timeout = defaultWorkspaceTimeout
	}
	return &WorkspaceRunner{root: root, timeout: timeout}
}

// bound derives the context one command runs under: the runner's own
// wall-clock bound, or the caller's deadline when that is tighter (context's
// own semantics — a derived timeout never extends a parent's).
func (w *WorkspaceRunner) bound(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, w.timeout)
}

// Verify checks that the workspace root exists and is a directory. It is a
// pre-flight sanity check, not a dirty-tree scan: it cannot detect a mutant
// or a stray file left behind by an earlier crashed run, because it has no
// record of what the tree looked like before. That guarantee instead comes
// from applyFiles' restore always running (see RunTest/Enumerate) — Verify
// only catches the case where the configured root is missing or is not a
// directory at all, before the first job wastes time discovering that.
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

// savedFile is one entry of the apply/restore ledger: the repo-relative path
// plus enough state to put the checkout back exactly as it was.
type savedFile struct {
	rel      string
	original []byte
	existed  bool
}

// mkdirAllTracking creates dir and any missing ancestors of it, opened
// through root so a symlink component cannot smuggle the creation outside
// the checkout, and returns exactly the directories it created — in
// shallow-to-deep order — so a later restore can remove exactly those and
// nothing else. dir "." (the workspace root itself) needs no creation and
// returns (nil, nil).
func mkdirAllTracking(root *os.Root, dir string) ([]string, error) {
	dir = filepath.ToSlash(filepath.Clean(dir))
	if dir == "." || dir == "" {
		return nil, nil
	}
	parts := strings.Split(dir, "/")
	var created []string
	cur := ""
	for _, p := range parts {
		if cur == "" {
			cur = p
		} else {
			cur = cur + "/" + p
		}
		if _, err := root.Stat(cur); err == nil {
			continue // already there; not ours to remove later
		} else if !os.IsNotExist(err) {
			return created, fmt.Errorf("adequacy: checking %s: %w", cur, err)
		}
		if err := root.Mkdir(cur, 0o750); err != nil {
			return created, fmt.Errorf("adequacy: creating %s: %w", cur, err)
		}
		created = append(created, cur)
	}
	return created, nil
}

// applyFiles overlays files onto the checkout and returns a restore func that
// undoes exactly that overlay: an existing file gets its original bytes
// written back; a file that did not exist gets removed; a directory this
// call created to hold a new file is removed too (deepest first, and only if
// still empty — something else may have put content there, in which case
// leaving the directory behind is the safe failure, not a forced delete of
// data this runner didn't write). The caller MUST invoke restore via defer,
// before checking any error from applyFiles itself — the ledger only
// contains entries that were actually applied, so a partial failure still
// unwinds cleanly.
//
// Every filesystem access here goes through an *os.Root opened on w.root,
// never through a plain os.* call joined onto a string path: Root refuses to
// resolve a name — absolute, "..", or a symlink component — that would leave
// the directory tree it was opened on, even though the checkout may contain
// a symlink committed by whoever authored the change under audit. A lexical
// prefix check on the joined path cannot make that guarantee, because it
// only inspects the path string, never what a symlink on disk actually
// points to.
//
// This is the single place the crash-safety guarantee is written: RunTest
// and Enumerate both call it, so a failing command, a timeout, or a panic in
// either method restores the tree the same way.
func (w *WorkspaceRunner) applyFiles(files map[string]string) (restore func(), err error) {
	root, rerr := os.OpenRoot(w.root)
	if rerr != nil {
		return func() {}, fmt.Errorf("adequacy: opening workspace %s: %w", w.root, rerr)
	}

	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	// Sorted for a deterministic apply order, so a failure midway leaves a
	// reproducible state rather than a map-order-dependent one.
	sort.Strings(keys)

	var fileLedger []savedFile
	var dirsCreated []string // shallow-to-deep creation order
	restore = func() {
		defer func() { _ = root.Close() }()
		// Reverse order, so nested creations unwind cleanly.
		for i := len(fileLedger) - 1; i >= 0; i-- {
			s := fileLedger[i]
			if s.existed {
				_ = root.WriteFile(s.rel, s.original, 0o600)
				continue
			}
			_ = root.Remove(s.rel)
		}
		// Deepest directory first; Remove no-ops (returns an error we
		// discard) if anything else left the directory non-empty — a stray
		// directory is a smaller failure than deleting data we didn't write.
		for i := len(dirsCreated) - 1; i >= 0; i-- {
			_ = root.Remove(dirsCreated[i])
		}
	}

	for _, rel := range keys {
		if filepath.IsAbs(rel) {
			return restore, fmt.Errorf("adequacy: workspace path %q is absolute", rel)
		}
		orig, rerr := root.ReadFile(rel)
		existed := rerr == nil
		if rerr != nil && !os.IsNotExist(rerr) {
			return restore, fmt.Errorf("adequacy: reading %s: %w", rel, rerr)
		}
		if !existed {
			created, merr := mkdirAllTracking(root, filepath.Dir(filepath.FromSlash(rel)))
			if merr != nil {
				return restore, fmt.Errorf("adequacy: creating parent of %s: %w", rel, merr)
			}
			dirsCreated = append(dirsCreated, created...)
		}
		fileLedger = append(fileLedger, savedFile{rel: rel, original: orig, existed: existed})
		if werr := root.WriteFile(rel, []byte(files[rel]), 0o600); werr != nil {
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

	ctx, cancel := w.bound(ctx)
	defer cancel()
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

	ctx, cancel := w.bound(ctx)
	defer cancel()
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

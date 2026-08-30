// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/sandbox"
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
	root      string
	timeout   time.Duration
	maxOutput int // 0 => unbounded (bytes.Buffer, today's behavior); see WithWorkspaceMaxOutput
	// perRunEnv, when set, is called FRESH before EVERY individual command
	// this runner executes (once per applyRunRestore call — see there) to
	// get extra "VAR=value" env assignments plus a cleanup to run once that
	// one command has finished. This is how a language plugin's
	// lang.Plugin.WorkspaceRunEnv reaches this substrate — see
	// WithPerRunEnv's doc comment for why "fresh before every call" (never
	// once for the whole runner's lifetime) is the load-bearing part of
	// this field's contract.
	perRunEnv func() (env []string, cleanup func())
	// treeEnv is set by WithTreeEnv and DELIBERATELY never read by this
	// runner: the option is declared as a WorkspaceOption purely so a caller
	// can pass it in the same list as every other option, but it applies per
	// TREE, which only WorkspacePool knows about. The pool applies the
	// caller's option list to a probe runner, lifts this field out for
	// itself, and composes it with perRunEnv per tree. A WorkspaceRunner
	// constructed directly with WithTreeEnv therefore ignores it — there is
	// exactly one tree in that case and it is the checkout the caller already
	// named. See WithTreeEnv.
	treeEnv func(treeRoot string) []string
}

// WorkspaceOption configures a WorkspaceRunner at construction
// (NewWorkspaceRunner), the same variadic-functional-option shape
// JailOption already uses for bwrapJail (WithReadOnlyBinds/WithMaxOutput).
type WorkspaceOption func(*WorkspaceRunner)

// WithWorkspaceMaxOutput bounds Enumerate's combined stdout+stderr to n
// bytes (head-truncated via sandbox.CappedWriter, sandbox.TruncationMarker
// appended) instead of buffering the command's output into an unbounded
// bytes.Buffer.
//
// The workspace substrate never goes through sandbox.Run/Options.MaxOutput
// at all — it execs the command directly, since the runner's own
// environment (a CI job) IS the isolation boundary, not corral — so nothing
// bounded its output before this option existed. That was fine for
// RunTest's ordinary per-mutant runs (a compiler error or a test failure
// summary is small), but reposcan's coverage pre-flight instruments the
// WHOLE suite and reads back one combined profile: measured against
// google.golang.org/grpc's own `go test ./...` with -coverpkg=./..., that
// profile is 253 MB before reduction, read entirely into memory with no
// cap on this substrate (827 MB peak RSS, vs 96 MB before -coverpkg — see
// go.go's CoverageCmd doc comment for the reduction that also fixes this
// from the other side). Default 0 (via NewWorkspaceRunner without this
// option) preserves today's exact unbounded behavior for every other
// caller — RunTest and RunTestVerbose never accept this option and stay on
// bytes.Buffer unconditionally; only Enumerate consults maxOutput, and only
// a WorkspaceRunner built specifically for the pre-flight call (never the
// one certify_local.go builds for ordinary mutant runs) sets it.
func WithWorkspaceMaxOutput(n int) WorkspaceOption {
	return func(w *WorkspaceRunner) { w.maxOutput = n }
}

// WithPerRunEnv registers f as the runner's per-run environment source,
// called by applyRunRestore immediately before EVERY command it execs —
// RunTest, RunTestVerbose, and Enumerate all funnel through it — never once
// at construction time and reused. That distinction is the entire point:
// this substrate mutates the SAME real checkout in place across the
// baseline, the canary, and every mutant in one audit, so a value computed
// once (e.g. one temp directory) and shared across those calls would still
// let a later call's same-second, same-length write collide with an
// earlier call's own cache entry in that shared directory — see
// lang.Plugin.WorkspaceRunEnv's doc comment for the measured mechanism this
// exists to close (python.go's __pycache__ hole).
//
// f's cleanup return value is invoked once that single command has
// finished (success, failure, or timeout alike — see applyRunRestore),
// before the next call's f() runs.
//
// f is typically a language plugin's WorkspaceRunEnv method value; nil (the
// zero value, never set) means "nothing extra" — every call's cmd.Env stays
// the default (nil, meaning inherit this process's own environment
// unmodified), preserving today's exact behavior for any caller that never
// sets this option.
func WithPerRunEnv(f func() (env []string, cleanup func())) WorkspaceOption {
	return func(w *WorkspaceRunner) { w.perRunEnv = f }
}

// WithTreeEnv supplies extra "VAR=value" env assignments derived from the
// root of the tree a command is about to run in — the seam a caller needs to
// state a rule like "PYTHONPATH must point at THIS copy of the checkout, not
// the original" without knowing how many copies exist or where they are.
//
// It is a WorkspaceOption so callers pass it in the same variadic list as
// WithPerRunEnv and friends, but it is honoured only by WorkspacePool, which
// calls f once per tree and prepends the result to that tree's per-run env
// (tree env first, then the language plugin's own WithPerRunEnv output, so a
// plugin can still override). On a bare WorkspaceRunner it is a NO-OP: a
// single runner has one tree, the root the caller already chose, so there is
// nothing this could tell it that it does not already know.
func WithTreeEnv(f func(treeRoot string) []string) WorkspaceOption {
	return func(w *WorkspaceRunner) { w.treeEnv = f }
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
func NewWorkspaceRunner(root string, timeout time.Duration, opts ...WorkspaceOption) *WorkspaceRunner {
	if timeout <= 0 {
		timeout = defaultWorkspaceTimeout
	}
	w := &WorkspaceRunner{root: root, timeout: timeout}
	for _, o := range opts {
		o(w)
	}
	return w
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
	return w.applyRunRestore(ctx, files, testCmd, nil, nil)
}

// RunTestVerbose is RunTest that ALSO returns the command's combined
// stdout+stderr, the same contract the jail's own RunTestVerbose has.
//
// advpool's compile-verify path type-asserts its Jail to this optional
// interface and, when the assertion fails, degrades to a bare pass/fail — so
// the test-writer is told "does not compile" with no compiler output to fix
// itself from, and spends retries repeating the same mistake. Implementing it
// here keeps that feedback on the substrate whose pitch is cost.
//
// Output is returned even on a non-nil error, so a timeout still carries
// whatever the command printed before it was killed.
func (w *WorkspaceRunner) RunTestVerbose(ctx context.Context, files map[string]string, testCmd []string) (bool, string, error) {
	var out bytes.Buffer
	ok, err := w.applyRunRestore(ctx, files, testCmd, &out, &out)
	return ok, out.String(), err
}

// RunTestDetailed is RunTestVerbose handing the output back as BYTES, capped
// to the last maxDetailedOutput of it — the adequacy.DetailedJail contract the
// scorer uses to name the test that killed a mutant.
//
// The tail, not the head: a runner puts its failure SUMMARY last (pytest's
// "short test summary info", `go test`'s FAIL lines), so a run that printed
// megabytes of trace still yields the half that can answer "which test".
//
// The cap is enforced AS THE BYTES ARRIVE (tailWriter), not by trimming a
// buffer afterwards. This runs once per mutant, in every tree, concurrently;
// buffering the whole run first held every byte a stack-trace-heavy suite
// printed, dozens of times over, to return 64 KiB of it. See tailWriter's
// own doc.
//
// A NOTE ON WHAT THIS COSTS, since it is not free the way "discard the
// output" is. Handing applyRunRestore a non-nil writer makes os/exec give the
// child an OS PIPE (a nil writer gets /dev/null and no goroutine at all), and
// cmd.Wait then blocks on a copying goroutine that only finishes when every
// holder of the write end closes it. A grandchild the suite leaves running
// inherits that descriptor and would otherwise hold Wait open past the
// deadline, forever. What bounds it is sandbox.GuardProcess's WaitDelay,
// applied in applyRunRestore — see the comment there, and GuardProcess's own.
// So the detailed path is safe, but it is safe BECAUSE of that guard, not
// because the capture is small: do not remove the guard on the grounds that
// this runner has a timeout.
//
// Output rides along even on a non-nil error, exactly as RunTestVerbose does.
func (w *WorkspaceRunner) RunTestDetailed(ctx context.Context, files map[string]string, testCmd []string) (bool, []byte, error) {
	out := newTailWriter(maxDetailedOutput)
	ok, err := w.applyRunRestore(ctx, files, testCmd, out, out)
	return ok, out.Bytes(), err
}

// applyRunRestore is the single implementation of this runner's
// apply/run/restore discipline: overlay files onto the checkout, run cmd in
// it under the runner's wall-clock bound, and restore via defer — so a
// failing command, a timeout, and a panic all unwind the tree identically.
// RunTest, RunTestVerbose and Enumerate differ ONLY in which streams they
// capture, which is what stdout/stderr (either may be nil, meaning discard)
// express. A non-zero exit is a RESULT, not an error, for all three.
func (w *WorkspaceRunner) applyRunRestore(ctx context.Context, files map[string]string, cmdArgv []string, stdout, stderr io.Writer) (bool, error) {
	if len(cmdArgv) == 0 {
		return false, errors.New("adequacy: workspace runner needs a command")
	}

	restore, err := w.applyFiles(files)
	defer restore()
	if err != nil {
		return false, err
	}

	ctx, cancel := w.bound(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, cmdArgv[0], cmdArgv[1:]...) // #nosec G204 -- the project's own command, supplied by its workflow
	cmd.Dir = w.root
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	// perRunEnv is called FRESH for THIS call only — never once for the
	// runner's whole lifetime — and its cleanup runs before applyRunRestore
	// returns, regardless of how the command exited. See WithPerRunEnv's
	// doc comment for why per-call freshness (not just "set once, reused")
	// is what actually closes the hole it exists for.
	if w.perRunEnv != nil {
		extra, envCleanup := w.perRunEnv()
		defer envCleanup()
		if len(extra) > 0 {
			cmd.Env = append(os.Environ(), extra...)
		}
	}
	// The wall-clock bound above is only a bound on the CONTEXT; without
	// these three guards it is not a bound on the call. This substrate execs
	// the command directly (the CI runner is the isolation boundary, so
	// nothing here goes through sandbox.Run), and stdout/stderr are plain
	// io.Writers, so os/exec hands the child an OS pipe and copies from it in
	// a goroutine that cmd.Wait blocks on. A grandchild that outlives the
	// command inherits the write end and keeps that goroutine — and therefore
	// Wait — alive FOREVER, deadline or no deadline. An ordinary suite that
	// leaves a background worker running is enough to trigger it. See
	// sandbox.GuardProcess.
	sandbox.GuardProcess(cmd)
	runErr := cmd.Run()
	// Order matters, and this check comes FIRST. Once WaitDelay exists, Wait
	// can return late for a reason that has nothing to do with the command:
	// the command exited on its own schedule and a leaked descendant held the
	// pipe until either WaitDelay or the deadline force-closed it. In that
	// case the command's own exit status is the honest answer, and
	// ErrTestTimeout would be a claim about the command that is simply false
	// ("corral could not run your suite" for a suite that ran and passed).
	//
	// st.Exited() is precisely the distinction: it is true only for a process
	// that reached exit() itself, and false for one the cancel func killed —
	// so a genuinely hanging command still falls through to ErrTestTimeout
	// below, exactly as before.
	if st := cmd.ProcessState; st != nil && st.Exited() {
		return st.ExitCode() == 0, nil // a non-zero exit is a RESULT, not an error
	}
	if ctx.Err() != nil {
		return false, ErrTestTimeout
	}
	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			return false, nil // a non-zero exit is a RESULT, not an error
		}
		return false, fmt.Errorf("adequacy: running the command: %w", runErr)
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
	if w.maxOutput > 0 {
		buf := sandbox.NewCappedWriter(w.maxOutput)
		_, err := w.applyRunRestore(ctx, files, cmd, buf, nil)
		return buf.String(), err
	}
	var stdout bytes.Buffer
	_, err := w.applyRunRestore(ctx, files, cmd, &stdout, nil)
	return stdout.String(), err
}

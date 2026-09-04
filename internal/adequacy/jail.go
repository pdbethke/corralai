// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"errors"
	"fmt"
	"github.com/pdbethke/corralai/internal/lang"
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
	backend   sandbox.Isolator
	timeout   time.Duration
	binds     []DepBind
	maxOutput int // 0 => sandbox.Run's own default (16 KiB); see WithMaxOutput
}

// DepBind is a read-only dependency dir to mount into the jail: Host is the
// absolute host path, Rel is the repo-relative path where it lives (and
// where the test command expects it). RunTest/Enumerate resolve Rel against
// the per-run temp workspace to build sandbox.Bind.Target — Rel alone is not
// enough because only RunTest/Enumerate know the per-run dir.
type DepBind struct {
	Host string // absolute host dir
	Rel  string // repo-relative dir (slash-separated), e.g. "node_modules"
}

// JailOption configures a bwrapJail at construction (NewJail/NewEnumerator).
type JailOption func(*bwrapJail)

// WithReadOnlyBinds sets the dependency dirs mounted read-only into every
// run this jail performs. Binds are constant for the jail's whole lifetime;
// only their Target (resolved from Rel against the per-run workspace) varies
// per call.
func WithReadOnlyBinds(binds []DepBind) JailOption {
	return func(j *bwrapJail) { j.binds = binds }
}

// WithMaxOutput raises (or lowers) the combined stdout+stderr cap for every
// run this jail performs, overriding sandbox.Run's own 16 KiB default. The
// default is the right choice for graded test/mutant runs (their signal is
// pass/fail, not the transcript), but it silently head-truncates anything
// that legitimately needs to return a large machine-readable payload on
// stdout — e.g. the coverage pre-flight's `coverage json` report, which was
// measured at 467 KB on a real project (pallets/flask): every real run
// through the 16 KiB default truncates before ParseCoverage ever sees valid
// JSON, so the pre-flight could never succeed on any non-toy repository.
// Callers that need this MUST build a jail/enumerator specifically for that
// purpose (see reposcan's caller) rather than raising the cap for every
// other run this jail also performs, which would let a runaway, misbehaving
// test process buffer arbitrarily more into memory for no benefit.
func WithMaxOutput(n int) JailOption {
	return func(j *bwrapJail) { j.maxOutput = n }
}

// NewJail builds the real bwrap-sandboxed Jail for the adequacy scorer.
// backend must be resolved via sandbox.Resolve — never construct an
// alternate, weaker isolation path here. A nil backend is accepted (RunTest
// will refuse to run rather than fall back to unsandboxed execution).
func NewJail(backend sandbox.Isolator, timeout time.Duration, opts ...JailOption) Jail {
	j := bwrapJail{backend: backend, timeout: timeout}
	for _, o := range opts {
		o(&j)
	}
	return j
}

// NewEnumerator builds the real bwrap-sandboxed Enumerator for the tests×
// mutants matrix. Same backend/timeout contract as NewJail — in fact the
// SAME concrete type (bwrapJail) satisfies both interfaces, so a caller
// wiring both a Jail and an Enumerator off one backend gets identical
// workspace/perm handling for each.
func NewEnumerator(backend sandbox.Isolator, timeout time.Duration, opts ...JailOption) Enumerator {
	j := bwrapJail{backend: backend, timeout: timeout}
	for _, o := range opts {
		o(&j)
	}
	return j
}

// resolveBinds resolves the jail's constant repo-relative DepBinds into
// absolute sandbox.Bind targets under this run's temp workspace dir. Shared
// by RunTest and Enumerate so the two never drift.
func (j bwrapJail) resolveBinds(dir string) ([]sandbox.Bind, error) {
	var roBinds []sandbox.Bind
	for _, b := range j.binds {
		// Defense-in-depth against a TOCTOU: the dep dir was a real directory
		// when the walk captured it, but it is bind-mounted here, later. lstat
		// it now and REFUSE if it became a symlink in between — bwrap/docker
		// resolve the bind SOURCE at mount time, so a Host swapped to a
		// symlink→/etc/... would otherwise expose that target read-only in the
		// jail. Only the final component is checked (a legit symlink in the
		// repo's own path prefix must not false-reject).
		fi, err := os.Lstat(b.Host)
		if err != nil {
			return nil, fmt.Errorf("adequacy: dependency bind %s: %w", b.Host, err)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("adequacy: refusing to bind %q: dependency directory is a symlink", b.Host)
		}
		// PerEntry: mount each top-level entry rather than the whole tree, so
		// the dependency directory itself stays writable. Toolchains write
		// caches inside their dep dir as a matter of course (node_modules/.vite,
		// node_modules/.cache), and a whole-tree read-only mount turns that into
		// an EROFS that surfaces as "the baseline failed" on a healthy project.
		// Nothing is copied either way — this is the same mounts, one level down.
		roBinds = append(roBinds, sandbox.Bind{
			Host:     b.Host,
			Target:   filepath.Join(dir, filepath.FromSlash(b.Rel)),
			PerEntry: true,
		})
	}
	return roBinds, nil
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

// runInJail writes files into a fresh temp workspace and runs cmd inside the
// jail, returning the raw sandbox result. It refuses to run at all when backend
// is nil — corral never falls back to running untrusted test+code unsandboxed.
// RunTest, RunTestVerbose, and Enumerate all funnel through here so the
// workspace/binds/timeout handling never drifts between them.
// shellQuote single-quotes one argv element so the jail's `sh -c` treats it as a
// literal: an embedded ' is closed, escaped, and reopened. testCmd is an ARGV
// (a program plus its literal arguments) — to run a compound shell command, pass
// it as a single explicit element, e.g. []string{"sh", "-c", "a && b"}.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shellJoin renders an argv as one sh-safe command string, each element quoted.
// It replaces a bare strings.Join(cmd, " "), which leaked argv metacharacters
// into `sh -c`: a `-run '^Foo$|^Bar'` regex (with $, |, ()) was re-parsed into
// pipes/subshells, corrupting the command and fail-closing the audit for the
// wrong reason. Quoting makes the argv literal, exactly as exec would.
func shellJoin(cmd []string) string {
	quoted := make([]string, len(cmd))
	for i, a := range cmd {
		quoted[i] = shellQuote(a)
	}
	return strings.Join(quoted, " ")
}

// runInJail runs cmd over files in a fresh disposable workspace, capturing
// the run's OPENING up to the jail's own output cap (see WithMaxOutput).
func (j bwrapJail) runInJail(ctx context.Context, files map[string]string, cmd []string) (sandbox.Result, error) {
	return j.runInJailCapturing(ctx, files, cmd, j.maxOutput, false)
}

// runInJailCapturing is runInJail with the capture policy spelled out:
// maxOutput bounds what is held (0 => sandbox.Run's own 16 KiB default), and
// keepTail decides WHICH END survives it. Every other aspect of the run —
// binds, env, timeout, exit code, timeout classification — is identical, and
// deliberately so: the two capture policies must never become two different
// runs.
func (j bwrapJail) runInJailCapturing(ctx context.Context, files map[string]string, cmd []string, maxOutput int, keepTail bool) (sandbox.Result, error) {
	if len(cmd) == 0 {
		return sandbox.Result{}, errors.New("adequacy: empty command")
	}
	dir, err := j.writeWorkspace(files)
	if err != nil {
		return sandbox.Result{}, err
	}
	defer os.RemoveAll(dir) // #nosec G104 -- best-effort cleanup of our own disposable temp dir

	roBinds, berr := j.resolveBinds(dir)
	if berr != nil {
		return sandbox.Result{}, berr
	}
	// Make the operator's OWN toolchain reachable. The jail mounts /usr, which
	// covers a distribution package and /usr/local — and nothing else, so a
	// compiler installed by asdf, nvm, rustup, pyenv, mise or Homebrew was
	// invisible and the run failed with "<tool>: not found", blaming the
	// project. Resolved from the command itself, so nothing is guessed.
	//
	// EVERY interpreter the command runs, not argv[0]. The coverage pre-flight
	// and test selection wrap the suite in `sh -c` (set up a temp dir, run,
	// reduce), so argv[0] was "sh" and the operator's toolchain — reachable
	// for the ordinary scoring runs in the SAME scan — was never bound for the
	// instrumented ones. `corral doctor` said the toolchain was reachable
	// inside the sandbox, which was true for the command it probed and false
	// for the wrapped one the pre-flight actually ran.
	seen := map[string]bool{}
	for _, interp := range lang.InterpretersIn(cmd) {
		tb, terr := toolchainBindFor(interp)
		if terr != nil {
			return sandbox.Result{}, terr
		}
		if tb.Host != "" && !seen[tb.Host] {
			seen[tb.Host] = true
			roBinds = append(roBinds, tb)
			// pip --user's shim in ~/.local/bin imports from ~/.local/lib;
			// the two are bound as a pair, never their parent.
			if filepath.Base(tb.Host) == "bin" && filepath.Base(filepath.Dir(tb.Host)) == ".local" {
				lib := filepath.Join(filepath.Dir(tb.Host), "lib")
				if _, err := os.Stat(lib); err == nil && !seen[lib] {
					seen[lib] = true
					roBinds = append(roBinds, sandbox.Bind{Host: lib, Target: lib})
				}
			}
		}
	}
	res, err := sandbox.RunGuarded(ctx, shellJoin(cmd), sandbox.Options{
		Workspace:     dir,
		Backend:       j.backend,
		Network:       false,
		Timeout:       j.timeout,
		ReadOnlyBinds: roBinds,
		Env:           envWithDepBinPaths(sandbox.MinimalEnv(), roBinds),
		MaxOutput:     maxOutput, // 0 => sandbox.Run's own 16 KiB default
		KeepTail:      keepTail,
	})
	if err != nil {
		if res.TimedOut {
			return res, fmt.Errorf("%w: %s", ErrTestTimeout, res.Err)
		}
		return res, err
	}
	return res, nil
}

// RunTest reports whether testCmd exited 0 inside the jail.
func (j bwrapJail) RunTest(ctx context.Context, files map[string]string, testCmd []string) (bool, error) {
	res, err := j.runInJail(ctx, files, testCmd)
	if err != nil {
		return false, err
	}
	return res.ExitCode == 0, nil
}

// RunTestVerbose is RunTest that ALSO returns the jail's combined stdout+stderr.
// The compile-verify path uses it so a non-compiling test's actual compiler
// error is surfaced to the caller (and fed back to the test-writer on retry)
// instead of a bare "does not compile". Output is returned even on a non-nil
// error so a timeout/infra failure can still carry whatever the jail printed.
func (j bwrapJail) RunTestVerbose(ctx context.Context, files map[string]string, testCmd []string) (bool, string, error) {
	res, err := j.runInJail(ctx, files, testCmd)
	if err != nil {
		return false, res.Output, err
	}
	return res.ExitCode == 0, res.Output, nil
}

// RunTestDetailed is RunTestVerbose's byte-returning sibling: the
// adequacy.DetailedJail contract the scorer uses to name the test that killed
// a mutant (killed_by).
//
// The jail already KEEPS the run's output — RunTestVerbose returns the same
// bytes — so implementing this costs nothing: the same run, the same exit
// code, the same verdict. Without it, `--substrate jail` recorded NULL in
// killed_by for every mutant it killed, and a column that exists for one
// substrate out of two is a column no cross-repo query can trust.
//
// Capped to the LAST maxDetailedOutput: a runner puts its failure summary at
// the end, which is the half that can answer "which test".
//
// THE TAIL HAS TO BE KEPT AT THE SOURCE, and for a long time it was not. This
// path ran through runInJail's ordinary capture, which passes MaxOutput 0 —
// sandbox.Run's 16 KiB default — and sandbox.CappedWriter keeps the HEAD. So
// on any suite verbose enough to print past 16 KiB (pytest -v over a few
// hundred tests, comfortably), the bytes that arrived here were the run's
// OPENING and the trailing summary was already gone; the tailBytes call below
// then trimmed a buffer that no longer contained what it was trimming for.
// killed_by came back NULL on `--substrate jail` for exactly the verbose
// suites where naming the killing test matters most, with nothing to show it
// had happened: the run passed, the verdict was right, one column was empty.
//
// So this path asks the sandbox for a TAIL-keeping capture of
// maxDetailedOutput (sandbox.Options.KeepTail). Pass/fail semantics are
// untouched — same command, same exit code, same timeout handling — only
// which end of an over-long output survives. tailBytes stays as the belt on
// the contract's own cap.
//
// Output rides along even on a non-nil error, exactly as RunTestVerbose does.
func (j bwrapJail) RunTestDetailed(ctx context.Context, files map[string]string, testCmd []string) (bool, []byte, error) {
	res, err := j.runInJailCapturing(ctx, files, testCmd, maxDetailedOutput, true)
	out := tailBytes([]byte(res.Output), maxDetailedOutput)
	if err != nil {
		return false, out, err
	}
	return res.ExitCode == 0, out, nil
}

// Enumerate is RunTest's stdout-returning sibling: same disposable
// workspace/perms/anti-traversal handling (writeWorkspace), but reports
// sandbox.Result.Output instead of collapsing the run to a bool. An empty
// output on a real (non-error) run is a legitimate "no tests" answer, not a
// failure — only a genuine timeout or infra failure to start the run
// returns a non-nil error, mirroring RunTest's own contract.
func (j bwrapJail) Enumerate(ctx context.Context, files map[string]string, cmd []string) (string, error) {
	res, err := j.runInJail(ctx, files, cmd)
	if err != nil {
		return "", err
	}
	return res.Output, nil
}

// EnumerateDetailed is Enumerate's optional richer twin (see
// reposcan.CollectSelectionEvidence's unexported detailedRunner): the jail
// backend already captures the exit code and COMBINED stdout+stderr for
// every run (sandbox.Result), so unlike the workspace substrate's own
// EnumerateDetailed there is nothing extra to capture here — Output already
// carries whatever a failed run wrote to either stream, Stderr is left
// empty to avoid reporting the same bytes twice.
func (j bwrapJail) EnumerateDetailed(ctx context.Context, files map[string]string, cmd []string) (sandbox.EnumerateResult, error) {
	res, err := j.runInJail(ctx, files, cmd)
	return sandbox.EnumerateResult{Output: res.Output, ExitCode: res.ExitCode}, err
}

// envWithDepBinPaths prepends each bound dependency directory's `.bin` to PATH
// inside the jail.
//
// The host's PATH is useless here: a developer runs `tsc` or `vitest` because
// their shell resolves it through ./node_modules/.bin in the REPO, and that
// absolute path does not exist inside the jail, whose workspace is a fresh temp
// directory. Without this, a language plugin's own compile check
// (`tsc --noEmit`) dies with "tsc: not found" — so the test-writer can never
// author a compiling test, and every survivor stays unproven no matter how good
// the model was. The audit still grades, and quietly reports proven_missed 0.
//
// Only `.bin` under an actual dependency bind is added, and only paths, never
// the host's own PATH entries: nothing on the host becomes reachable that the
// binds did not already make reachable.
func envWithDepBinPaths(env []string, binds []sandbox.Bind) []string {
	var prefix []string
	for _, b := range binds {
		if filepath.Base(b.Target) != "node_modules" {
			continue
		}
		prefix = append(prefix, filepath.Join(b.Target, ".bin"))
	}
	if len(prefix) == 0 {
		return env
	}
	out := make([]string, 0, len(env))
	found := false
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			found = true
			out = append(out, "PATH="+strings.Join(prefix, ":")+":"+strings.TrimPrefix(kv, "PATH="))
			continue
		}
		out = append(out, kv)
	}
	if !found {
		out = append(out, "PATH="+strings.Join(prefix, ":"))
	}
	return out
}

// ShellJoin renders an argv as one sh-safe string, each element quoted — the
// exported form of shellJoin, for callers that must FLATTEN an argv into the
// single TestCmd string a RunSpec carries.
//
// Pair it with ShellSplit. A plain strings.Join is not reversible: an argument
// containing a space (an inline `-e` script, a `--filter=a b`, a path with a
// space) is silently torn into several arguments by the strings.Fields on the
// far side, and the command that runs is not the command the operator typed.
// That produced a syntax error from a Ruby one-liner cut at its first comma,
// reported as "your suite failed".
func ShellJoin(cmd []string) string { return shellJoin(cmd) }

// ShellSplit is ShellJoin's inverse: it parses a command string into argv,
// honoring single quotes, double quotes and backslash escapes the way sh does.
//
// It replaces strings.Fields, which splits on whitespace ALONE and so cannot
// round-trip anything ShellJoin quoted. On a command with no quoting — every
// simple `go test ./...` — it returns exactly what strings.Fields returned, so
// existing stored commands are unaffected.
//
// An unterminated quote yields the remainder as one final argument rather than
// an error: the caller's next step runs the command and reports the runner's
// own complaint, which is a better diagnosis than a parse error from us.
func ShellSplit(s string) []string {
	var args []string
	var cur strings.Builder
	var quote rune // 0, '\'' or '"'
	started := false

	flush := func() {
		if started {
			args = append(args, cur.String())
			cur.Reset()
			started = false
		}
	}
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case quote == '\'':
			// Inside single quotes nothing is special, not even backslash.
			if c == '\'' {
				quote = 0
				continue
			}
			cur.WriteRune(c)
		case quote == '"':
			if c == '\\' && i+1 < len(rs) {
				// Only these are escapable inside double quotes in sh; any
				// other backslash is literal.
				if n := rs[i+1]; n == '"' || n == '\\' || n == '$' || n == '`' {
					cur.WriteRune(n)
					i++
					continue
				}
			}
			if c == '"' {
				quote = 0
				continue
			}
			cur.WriteRune(c)
		case c == '\'' || c == '"':
			quote = c
			started = true
		case c == '\\' && i+1 < len(rs):
			cur.WriteRune(rs[i+1])
			i++
			started = true
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			flush()
		default:
			cur.WriteRune(c)
			started = true
		}
	}
	flush()
	return args
}

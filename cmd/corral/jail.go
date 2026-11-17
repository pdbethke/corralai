// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/reposcan"
	"github.com/pdbethke/corralai/internal/sandbox"
)

// newRunJail is the ONE place in this package that turns a resolved sandbox
// backend into the adequacy.Jail an actual run scores mutants with. Every
// caller — the real `--local` scoring/validation/enumeration jails AND
// doctor's own preflight probe — MUST go through this function, never call
// adequacy.NewJail directly, so the two can never quietly diverge.
//
// This exists because they DID diverge once, silently: doctor built its own
// jail with no dependency binds while a `--repo-dir` run built one with
// depBinds (auto-detected .venv/node_modules/vendor dirs) via a second,
// separate call to adequacy.NewJail. The preflight and the run were probing
// two different sandbox realities, and doctor had no way to notice — the
// rehearsal that motivated this: doctor reported the toolchain reachable,
// the real run then died with `.venv/bin/python: not found`, 27k tokens
// spent on mutants that were never graded. depBinds nil/empty behaves
// identically to omitting the option — WithReadOnlyBinds(nil) leaves the
// jail's binds unset — so this is always safe to call unconditionally,
// bare mode included.
func newRunJail(iso sandbox.Isolator, timeout time.Duration, depBinds []adequacy.DepBind, opts ...adequacy.JailOption) adequacy.Jail {
	return adequacy.NewJail(iso, timeout, append([]adequacy.JailOption{adequacy.WithReadOnlyBinds(depBinds)}, opts...)...)
}

// gradingRunCache is what newGradingJail decided about a file's shared tool
// cache, and the one value every consumer reads it from.
type gradingRunCache struct {
	// Rewrite is the command rewrite that forbids a cached test result, for
	// every command the grading jail is handed (advpool.JailScorer and
	// JailValidator CommandRewrite). nil unless the cache is shared: an
	// unshared run keeps the operator's command byte for byte.
	Rewrite func(cmd []string) []string
	// Record is the DECISION made before any run: "shared"; "unshared:
	// <reason>"; or "" when the runner has no run cache at all. It is not what
	// the file row records: a cache decided shared may never be warmed. The
	// row's RunCache is final(jail), read when the file's run ends.
	Record string
	// warmRejected is set by the warm check when the dev pass's own command
	// is one the rewrite cannot give the flag that forbids a cached result,
	// the one reason a decided-shared cache goes unwarmed with a dev pass.
	// nil unless the cache is decided shared.
	warmRejected *atomic.Bool
}

// runCacheShared is the record of a file whose cache was shared AND warmed.
const runCacheShared = "shared"

// final is the RunCache the file's row records, read when the file's run
// ends and BEFORE its jail is closed: what the jail did, not what was decided.
// A cache decided shared is "shared" only if a warming run actually ran on it;
// otherwise the cache was mounted but never warmed, every run saw it empty,
// and the record says why. Every other record is the decision, unchanged.
func (gc gradingRunCache) final(j adequacy.Jail) string {
	if gc.Record != runCacheShared {
		return gc.Record
	}
	if rj, ok := j.(adequacy.RunCacheJail); ok && rj.Warmed() {
		return runCacheShared
	}
	why := "no dev pass ran"
	if gc.warmRejected != nil && gc.warmRejected.Load() {
		why = "the dev suite's command could not be given the flag that forbids a cached result"
	}
	return "unshared: never warmed (" + why + ")"
}

// newGradingJail is the ONE place that decides whether a file's grading shares
// a tool cache, for every site that builds a grading jail (never call
// newRunJail with WithRunCache anywhere else).
//
// THE SAFETY RULE. A cache shared among runs also holds test RESULTS, and go
// replays one instead of running the test. So a file shares a cache only when
// the command that WARMS it (baseline, the command Score runs on the
// unmutated code) forbids a cached result: lang.RunCacheCmd must say it
// rewrote it. Then the warming run stores no result, frozen runs cannot store
// one for each other, and every other command is rewritten the same way.
// Anything else — a shell wrapper, an env prefix, an unlisted flag, a command
// that runs no test — gets no cache and no rewrite: today's run, with the
// reason on the record. A jail that then reports its cache unshared (a backend
// that cannot overlay, a directory that cannot be made) drops the rewrite too.
//
// The caller closes a returned jail that is an adequacy.RunCacheJail when the
// file's run ends (it removes the cache directory); closing a jail with no
// cache is a no-op.
func newGradingJail(iso sandbox.Isolator, timeout time.Duration, depBinds []adequacy.DepBind, plug lang.Plugin, inv lang.Invocation, baseline []string) (adequacy.Jail, gradingRunCache) {
	if plug == nil {
		return newRunJail(iso, timeout, depBinds), gradingRunCache{}
	}
	env, ok := lang.RunCacheFor(plug, inv)
	if !ok {
		return newRunJail(iso, timeout, depBinds), gradingRunCache{}
	}
	if _, state := lang.RunCacheCmd(plug, inv, baseline); state != lang.RunCacheRewritten {
		return newRunJail(iso, timeout, depBinds), gradingRunCache{
			Record: "unshared: the test command cannot be given the flag that forbids a cached result (" + state.String() + ")",
		}
	}
	jail := newRunJail(iso, timeout, depBinds, adequacy.WithRunCache(env))
	rj, isRunCache := jail.(adequacy.RunCacheJail)
	if !isRunCache {
		return jail, gradingRunCache{Record: "unshared: the jail cannot share a cache"}
	}
	if c := rj.RunCache(); c.Unshared != "" {
		return jail, gradingRunCache{Record: "unshared: " + c.Unshared}
	}
	return jail, gradingRunCache{Rewrite: lang.RunCacheArgv(plug, inv), Record: runCacheShared, warmRejected: new(atomic.Bool)}
}

// warmCheckFor is JailScorer.WarmCheck for a file whose cache is shared (nil
// otherwise, so nothing warms): true for exactly the commands lang.RunCacheCmd
// rewrites to forbid a cached result. The dev pass asks it of its own baseline
// command, at the point that command is known, so a shape the row cannot read
// (RunCacheUnreadable, RunCacheNoTest) never warms, whatever the file's
// recorded command was.
func warmCheckFor(plug lang.Plugin, inv lang.Invocation, gc gradingRunCache) func(cmd []string) bool {
	if gc.Rewrite == nil {
		return nil
	}
	return func(cmd []string) bool {
		_, state := lang.RunCacheCmd(plug, inv.WithArgv(cmd), cmd)
		if state != lang.RunCacheRewritten {
			if gc.warmRejected != nil {
				gc.warmRejected.Store(true)
			}
			return false
		}
		return true
	}
}

// fileRunCache is the RunCache a file's row records: what its jail did
// (gradingRunCache.final), but only for a file that was graded here. A baseline that failed
// (COULD-NOT-GRADE), a suite that never touches the file, and a file never
// graded record "", whatever the wiring had decided before the baseline ran.
// The one rule behind the --repo result (Execute), the --local result
// (localFileResult) and the row builder.
func fileRunCache(r reposcan.FileResult) string {
	if !r.Gradable {
		return ""
	}
	return r.RunCache
}

// runCacheUnsharedLine is the line a file's own output carries when its
// grading shares no cache, "" when it does, or has none to share.
func runCacheUnsharedLine(path, record string) string {
	reason, ok := strings.CutPrefix(record, "unshared: ")
	if !ok {
		return ""
	}
	return "build cache: not shared for " + path + ": " + reason
}

// closeGradingJail removes a grading jail's cache directory, if it has one.
// A directory that could not be removed is said on stderr, once, naming it
// (the error carries the path): it changes no exit code, since the audit's
// verdict does not depend on it, but a leftover directory in the temporary
// directory must not go unmentioned.
func closeGradingJail(j adequacy.Jail) {
	if rj, ok := j.(adequacy.RunCacheJail); ok {
		if err := rj.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "corral: could not remove a build cache directory: %v\n", err)
		}
	}
}

// newRunEnumerator is newRunJail's Enumerator-returning sibling — same
// backend/timeout/binds contract, same single-definition requirement. Wired
// wherever an Enumerator is built alongside a Jail off the same inputs.
func newRunEnumerator(iso sandbox.Isolator, timeout time.Duration, depBinds []adequacy.DepBind, opts ...adequacy.JailOption) adequacy.Enumerator {
	return adequacy.NewEnumerator(iso, timeout, append([]adequacy.JailOption{adequacy.WithReadOnlyBinds(depBinds)}, opts...)...)
}

// resolveLocalJail resolves the sandbox for a --local run from the --jail flag
// (or auto), returning an actionable error and NEVER a weaker/unsandboxed
// backend. Fail closed: corral does not run untrusted test+code unsandboxed.
//
// jailFlag empty or "auto" tries the platform default backend (bwrap on
// Linux, sandbox-exec on macOS) via sandbox.Resolve. A bwrap userns/preflight
// failure — the Ubuntu 24.04 apparmor default that disables unprivileged user
// namespaces — is turned into an actionable error via bwrapUnavailableError
// instead of the raw plumbing error. "container" resolves the docker/podman
// backend and surfaces sandbox.Resolve's own clear error when no runtime/
// image is configured. "none" (and any UnsafeHost path) is refused here
// unconditionally: --local always sandboxes, regardless of what the
// separate, env-gated test-suite unsafe path allows internally.
func resolveLocalJail(jailFlag string) (sandbox.Isolator, error) {
	return resolveJailFn(jailFlag, true)
}

// resolveJailFn is the single seam EVERY sandbox resolution in this command
// goes through. A package var solely so a test can COUNT resolutions: "the
// repo scan resolves the sandbox once for the whole scan and hands it to
// every job" is a claim about how many times the backend probe runs, and it
// cannot be checked by comparing the isolators themselves — bwrapIsolator is
// an empty struct, so two INDEPENDENT resolutions compare equal and an
// identity assertion can never fail. Never reassigned in production.
var resolveJailFn = resolveJail

// resolveRepoJail resolves the sandbox for `corral certify --repo` from its
// --jail flag (or auto). Same fail-closed rules as resolveLocalJail, and the
// same `--jail container` advice on a bwrap failure, since --repo offers it.
func resolveRepoJail(jailFlag string) (sandbox.Isolator, error) {
	return resolveJailFn(jailFlag, true)
}

// resolveScanJail resolves the sandbox for `corral review`. Same fail-closed
// rules as resolveLocalJail, but review exposes no --jail flag, so a bwrap
// failure must not advise `--jail container`: an operator cannot reach that
// escape hatch from the command that printed the message.
func resolveScanJail() (sandbox.Isolator, error) {
	return resolveJailFn("", false)
}

// resolveJail is the shared implementation. containerFallback says whether the
// calling command actually exposes --jail, and therefore whether the bwrap
// advice may point at `--jail container`.
func resolveJail(jailFlag string, containerFallback bool) (sandbox.Isolator, error) {
	flag := strings.TrimSpace(jailFlag)

	if flag == "none" {
		return nil, errors.New(
			"corral certify --local: --jail none is not supported — --local always sandboxes the code and tests it runs; " +
				"the unsandboxed AGENT_EXEC_UNSAFE_HOST path is an internal test-only escape hatch, not a product option")
	}

	backend := flag
	if backend == "auto" {
		backend = ""
	}

	iso, err := sandbox.Resolve(sandbox.Config{Backend: backend})
	if err == nil {
		// Defense in depth: Resolve should never hand back "none" unless
		// UnsafeHost was set (which we never set above), but refuse to
		// propagate it regardless of how it got here.
		if iso.Name() == "none" {
			return nil, errors.New("corral certify --local: refusing to run unsandboxed (\"none\" backend); this should be unreachable")
		}
		return iso, nil
	}

	// Only bwrap gets the apparmor-specific actionable rewrite; an explicit
	// --jail container failure should surface sandbox.Resolve's own message
	// (already clear: install docker/podman or set CORRALAI_EXEC_IMAGE).
	if backend == "" || backend == "bwrap" {
		return nil, bwrapUnavailableError(err, containerFallback)
	}
	return nil, err
}

// bwrapUnavailableError turns a raw bwrap Preflight/Resolve failure into a
// concise, copy-pasteable actionable error: it names the Ubuntu 24.04+ apparmor
// cause (unprivileged userns restricted by the distro default), the fix — load
// Ubuntu's own bwrap-userns-restrict profile — and, only for commands that
// actually expose --jail, the container fallback. It used to print a
// hand-written /etc/apparmor.d/bwrap marked unconfined; that profile shadows
// the shipped one and leaves bwrap without CAP_NET_ADMIN (measured on Ubuntu
// 26.04, where it broke bwrap and GNOME's glycin), so it now says to remove it. It is a pure formatter — no bwrap invocation — so it is
// unit-testable without a working (or degraded) bwrap on the host.
func bwrapUnavailableError(cause error, containerFallback bool) error {
	tail := `Otherwise run on a host where bwrap can isolate: this command has no
sandbox-backend override.`
	if containerFallback {
		tail = `Or skip bwrap entirely: --jail container (needs docker or podman, plus
CORRALAI_EXEC_IMAGE set to an image with your toolchain, e.g.
CORRALAI_EXEC_IMAGE=python:3.12-bookworm).`
	}
	return fmt.Errorf(
		`no working bwrap sandbox: %w

corral never runs untrusted test/mutant code unsandboxed. On Ubuntu 24.04 and
later, AppArmor restricts the unprivileged user namespaces bwrap needs. Ubuntu
ships a profile for bwrap, bwrap-userns-restrict; load it (on 24.04 it may
still be under /usr/share/apparmor/extra-profiles):

  [ -f /etc/apparmor.d/bwrap-userns-restrict ] || sudo cp /usr/share/apparmor/extra-profiles/bwrap-userns-restrict /etc/apparmor.d/
  sudo apparmor_parser -r /etc/apparmor.d/bwrap-userns-restrict

Do not write your own /etc/apparmor.d/bwrap marked unconfined: it takes bwrap
away from the shipped profile and leaves it without the capabilities it needs.
If one is there from earlier advice (corral printed it before 1.0), remove it:

  sudo apparmor_parser -R /etc/apparmor.d/bwrap && sudo rm /etc/apparmor.d/bwrap

%s`, cause, tail)
}

// hasContainerRuntime reports whether docker or podman is on PATH, so tests
// that expect the "no runtime found" error can skip cleanly on a host that
// happens to have one installed rather than flake.
func hasContainerRuntime() bool {
	if _, err := exec.LookPath("podman"); err == nil {
		return true
	}
	if _, err := exec.LookPath("docker"); err == nil {
		return true
	}
	return false
}

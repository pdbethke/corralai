// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/sandbox"
)

// Two bounds stop corral from running on a repository too large for them,
// and both are safety bounds rather than measurements: the jail seed limit
// (how much repository text is copied into every jailed run) and the
// evidence pass bound (how long the one instrumented run, or the whole unit
// pass, may take). Each was sized for the repositories corral was proven on,
// and each stopped a real one: REA (morluto/rea) has 91.1 MiB of seedable
// text and 2,186 vitest units, of which 1,918 were "not run (time limit)"
// after 15 minutes on the workspace substrate (2026-10-27).
//
// So both are operator-settable, and neither moves without the operator:
// with no flag the bound is exactly the one that applied before the flag
// existed. They were not made required, which would break every existing
// invocation to name a number that suits nearly every repository; nor raised,
// which would spend the safety margin on every repository to suit one.
// Whatever stops a run names the flag that moves its bound, so the operator
// who meets one learns the way past it in the same line.
//
// Where each flag is refused, because what the flag names is known from the
// arguments alone not to exist:
//   - --seed-limit: on --substrate workspace (certify --repo, tests map),
//     which seeds no jail; on certify --local without --repo-dir, which seeds
//     no repository; on certify --repo --dry-run without --evidence, which
//     builds no executor.
//   - --evidence-timeout: on certify --repo --dry-run without --evidence,
//     which runs no evidence pass.
//
// Where a given flag can still go unread, and is SAID to on the line that
// reports the evidence (evidenceTimeoutUnused), because it cannot be known
// before the run:
//   - --evidence-timeout on a selection-cache hit: the evidence is served,
//     nothing runs, and whether the cache holds the tree is not known until
//     it is asked.
//   - --evidence-timeout under --whole-suite when the scan's language is
//     Python: its one instrumented run is skipped. It is not refused under
//     --whole-suite, because every other language's unit pass still runs
//     there (candidacy needs it) and reads the bound, and the language is
//     only known after enumeration.
//   - --seed-limit when the scan never builds a seed: a jail run whose
//     evidence is served from the cache and that audits no file (an empty
//     --diff-base, every file cached). Not disclosed: the seed is built on
//     the first jailed run, so an unread limit there changed nothing a
//     reader could misread.

// seedLimitBuiltinMiB is the jail seed limit without --seed-limit: 64 MiB of
// text, the bound loadRepoFiles always had. It is a guard against seeding a
// checkout that cannot be copied per run, not a claim about any repository.
const seedLimitBuiltinMiB = 64

// seedLimitBuiltin is seedLimitBuiltinMiB in bytes, for callers with no
// flag of their own (corral review, whose seed is a worktree at a commit).
const seedLimitBuiltin = int64(seedLimitBuiltinMiB) << 20

// seedLimitHelp is --seed-limit's help, shared by every verb that seeds a
// jail from a repository so the cost is stated once, from the code: the seed
// is read into memory once per language per scan (repoSeed, seedCache), and
// every jailed run — each baseline, mutant, authored test and evidence unit —
// writes all of it to a fresh directory under $TMPDIR and removes it after
// (adequacy's writeWorkspace / runInJailCapturing).
const seedLimitHelp = "MiB of repository text the jail is seeded with before the repository is refused as too large (without this flag: 64). Counted over every text file the seed copies, whatever its size: binaries outside test fixtures, .git, build output and bound dependency dirs are not counted, and the run names every binary it leaves out. The cost of raising it: the seed is held in memory once per language per run, and every baseline and mutant run writes the whole seed to its own fresh directory under $TMPDIR and removes it afterwards, so the disk in use is the seed times the runs in flight at once. The unit evidence pass writes it once and overlays it copy-on-write for each unit when bwrap has --tmp-overlay (0.10 and later), and otherwise also writes one per unit, and says so"

// evidenceTimeoutHelp is --evidence-timeout's help, shared by certify --repo
// and corral tests map, which run the same pass.
const evidenceTimeoutHelp = "how long the evidence pass may run (without this flag: 15m): Python's one instrumented suite run, or the whole unit pass of every other language, where each unit's own run is bounded the same. A unit not started when it passes is recorded \"not run (time limit)\" and one still running is stopped; both report no evidence, and every selection then includes them. The run says how many units it cut and names this flag. A Go duration, e.g. 2h"

// resolveSeedLimit is --seed-limit in bytes: the operator's when the flag was
// given, else seedLimitBuiltin. A given value that is not a positive number
// of MiB is refused, never read as "no limit" or as "the built-in one".
func resolveSeedLimit(fs *flag.FlagSet, mib int) (int64, error) {
	if !flagWasSet(fs, "seed-limit") {
		return seedLimitBuiltin, nil
	}
	if mib < 1 || int64(mib) > math.MaxInt64>>20 {
		return 0, fmt.Errorf("--seed-limit must be a positive number of MiB, got %d", mib)
	}
	return int64(mib) << 20, nil
}

// resolveEvidenceTimeout is --evidence-timeout: the operator's when the flag
// was given, else selectionTimeout. A given value that is not positive is
// refused: a zero bound would cut every unit before it started.
func resolveEvidenceTimeout(fs *flag.FlagSet, d time.Duration) (time.Duration, error) {
	if !flagWasSet(fs, "evidence-timeout") {
		return selectionTimeout, nil
	}
	if d <= 0 {
		return 0, fmt.Errorf("--evidence-timeout must be a positive duration, got %s", d)
	}
	return d, nil
}

// evidenceCutNote is the one wording for units the evidence bound cut, on the
// scan line and in corral tests map: how many, the bound, and the flag.
func evidenceCutNote(cut int, bound time.Duration) string {
	return fmt.Sprintf("%d unit(s) cut short by the %s evidence bound (not started, or stopped, when it passed); --evidence-timeout raises it", cut, bound)
}

// evidenceBoundNote is evidenceCutNote's twin for Python's single
// instrumented run, appended to the run's own failure note.
func evidenceBoundNote(bound time.Duration) string {
	return fmt.Sprintf(" — the run reached the %s evidence bound; --evidence-timeout raises it", bound)
}

// timeoutWatch records whether a run through the runner it wraps was stopped
// by the runner's own time bound (adequacy.ErrTestTimeout — both substrates'
// runners return it for a run their timeout killed). Python's evidence is
// one run whose failure reposcan reports as a note; the note carries the
// runner's error text but not WHICH bound stopped it, and an error string is
// no thing to match on, so the bound is observed here, at the call.
//
// wrap keeps the runner's shape: a runner with the detailed contract
// (EnumerateDetailed) is wrapped by one that has it, and one without by one
// that has not, so reposcan's type check reads the same runner either way.
type timeoutWatch struct {
	mu       sync.Mutex
	timedOut bool
}

func (w *timeoutWatch) note(err error) {
	if errors.Is(err, adequacy.ErrTestTimeout) {
		w.mu.Lock()
		w.timedOut = true
		w.mu.Unlock()
	}
}

func (w *timeoutWatch) hit() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.timedOut
}

type detailedCoverageRunner interface {
	coverageRunner
	EnumerateDetailed(ctx context.Context, files map[string]string, cmd []string) (sandbox.EnumerateResult, error)
}

type watchedRunner struct {
	inner coverageRunner
	w     *timeoutWatch
}

func (r watchedRunner) Enumerate(ctx context.Context, files map[string]string, cmd []string) (string, error) {
	out, err := r.inner.Enumerate(ctx, files, cmd)
	r.w.note(err)
	return out, err
}

type watchedDetailedRunner struct {
	watchedRunner
	detailed detailedCoverageRunner
}

func (r watchedDetailedRunner) EnumerateDetailed(ctx context.Context, files map[string]string, cmd []string) (sandbox.EnumerateResult, error) {
	res, err := r.detailed.EnumerateDetailed(ctx, files, cmd)
	r.w.note(err)
	return res, err
}

func (w *timeoutWatch) wrap(r coverageRunner) coverageRunner {
	plain := watchedRunner{inner: r, w: w}
	if d, ok := r.(detailedCoverageRunner); ok {
		return watchedDetailedRunner{watchedRunner: plain, detailed: d}
	}
	return plain
}

// evidenceTimeoutUnused is the clause a line about evidence that ran nothing
// appends when the operator named --evidence-timeout: the bound was never
// read, and why. "" when the flag was not given, so a scan that named no
// bound prints exactly what it printed before the flag existed.
func (l *localExecutor) evidenceTimeoutUnused(why string) string {
	if !l.evidenceTimeoutGiven {
		return ""
	}
	return "; --evidence-timeout not used (" + why + ")"
}

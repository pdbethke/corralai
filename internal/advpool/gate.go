// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/buildstore"
	"github.com/pdbethke/corralai/internal/certify"
	golang "github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/testgen"
	"github.com/pdbethke/corralai/internal/transparency"
)

// pluginFor resolves the language plugin from the code file's extension,
// fail-closed on an unknown language (the gate never grades what it cannot
// run).
func pluginFor(codePath string) (golang.Plugin, error) {
	p, ok := golang.Detect(codePath)
	if !ok {
		return nil, fmt.Errorf("advpool: no language plugin for %q — refusing to grade", codePath)
	}
	return p, nil
}

// advPoolTestPath derives the synthetic test-file name a candidate test is
// written to in the jail workspace from the code file's own path via the
// resolved language plugin's own convention. Falls back to the legacy go
// convention (same base name, `_test.go` suffix, same directory) when no
// plugin resolves — kept identical to the prior implementation so an
// unresolvable path still behaves exactly as before.
func advPoolTestPath(codePath string) string {
	if p, err := pluginFor(codePath); err == nil {
		return p.TestPaths(codePath)[0].Path
	}
	ext := filepath.Ext(codePath)
	base := strings.TrimSuffix(codePath, ext)
	dir := filepath.Dir(codePath)
	if dir == "." {
		return base + "_test.go"
	}
	return filepath.Join(dir, filepath.Base(base)+"_test.go")
}

// authoredTestMarker distinguishes the pool's authored test file from the
// dev's own test living in the same directory. Deliberately part of the STEM
// (not the extension or a prefix) so each plugin's own TestPaths convention
// still wraps it into a discovery-matching name — `_corral` survives into
// tests/test_cli_corral.py, login_corral_test.go, pricing_corral_test.rb and
// foo_corral.test.js alike.
const authoredTestMarker = "_corral"

// authoredTestCollisionLimit bounds the disambiguation search below. Reaching
// it means ten distinct candidate names were all already real repo files,
// which is not a shape any real project has — falling back is honest
// degradation, and the positive control still grades the result.
const authoredTestCollisionLimit = 10

// authoredTestPath derives the path the POOL-AUTHORED test is written to in
// repo-aware mode. It differs from advPoolTestPath (which names the synthetic
// SINGLE-FILE test path) in the one way that decides whether the authored
// test grades anything at all: the file must land somewhere the project's own
// unmodified test command actually COLLECTS.
//
// advPoolTestPath returns a sibling of the code file, which is wrong for any
// project that confines discovery to a test root. pallets/flask is the
// measured case: `testpaths = ["tests"]` in pyproject.toml means a test
// written to src/flask/cli_test.py is never collected, so it compiles, it
// "passes", and it grades NOTHING — the exact CompliantPass=true
// CanaryKilled=false [TEST UNSOUND] verdict a paid audit produced on
// 2026-07-31.
//
// The fix relocates the code file's STEM into the DEV TEST's own directory
// and lets the language plugin name it from there. The dev test's directory
// is collected BY CONSTRUCTION — it holds the test that paired with this code
// file, and that suite demonstrably executes the file, since running it is
// what produced the dev-adequacy score this whole run is measured against.
// Deriving the NAME from the plugin keeps this language-agnostic: corral
// never parses `testpaths`, jest `roots`, or a rake FileList, an endless
// per-language tail in which every miss is a silent wrong verdict rather than
// a loud one.
//
// `base` is the repo workspace (may be nil): the returned path is overlaid
// onto it, so a name the repo ALREADY contains would silently replace real
// source — the same class of defect as the stale-.pyc phantom survivors, a
// measurement taken in a workspace that is not the one it claims to be. Any
// clash is disambiguated away; an empty devTestPath (single-file mode, or any
// caller with no dev test to offer) falls back to advPoolTestPath unchanged.
func authoredTestPath(codePath, devTestPath string, base map[string]string) string {
	fallback := advPoolTestPath(codePath)
	if strings.TrimSpace(devTestPath) == "" {
		return fallback
	}
	p, err := pluginFor(codePath)
	if err != nil {
		return fallback
	}

	dir := filepath.Dir(devTestPath)
	ext := filepath.Ext(codePath)
	stem := strings.TrimSuffix(filepath.Base(codePath), ext)

	taken := func(q string) bool {
		if q == "" || q == devTestPath || q == codePath {
			return true
		}
		_, exists := base[q]
		return exists
	}

	for i := 0; i < authoredTestCollisionLimit; i++ {
		marker := authoredTestMarker
		if i > 0 {
			marker = fmt.Sprintf("%s%d", authoredTestMarker, i)
		}
		// Hand the plugin a synthetic SOURCE path (code stem + marker, sited
		// in the dev test's directory) and take its own rank-0 test name for
		// it — so the result matches whatever `_test.go` / `test_*.py` /
		// `*_test.rb` / `*.test.js` shape that language's runners discover.
		cands := p.TestPaths(filepath.Join(dir, stem+marker+ext))
		if len(cands) == 0 {
			return fallback
		}
		if got := filepath.ToSlash(cands[0].Path); !taken(got) {
			return got
		}
	}
	return fallback
}

// goScaffold is the exact go workspace scaffold/default test command kept
// from the prior implementation's fallback (mirrors
// internal/controlgate.LangScaffold("go"), duplicated here in miniature so
// this leaf package need not import internal/controlgate).
func goScaffold() (base map[string]string, testCmd []string) {
	return map[string]string{"go.mod": "module control\ngo 1.26\n"}, []string{"go", "test", "./..."}
}

// advPoolBase returns the workspace scaffold + default test command for the
// code file's resolved language plugin. The default testCmd is deliberately
// RECURSIVE for go ("./...", never controlgate.LangScaffold's own "./"
// default): a run's code_path is very commonly a subdirectory (e.g.
// internal/auth/login.go), which lands the candidate files under
// internal/auth/ in the jail workspace — "go test ./" from the module root
// only ever sees the root package (no .go files there), so a non-recursive
// default would silently no-op the scorer/compile-check for every
// subdirectory target. This is the SAME asymmetry bug CompileTest had (I-1):
// the scorer already honors the run's own TestCmd when set, this only fixes
// the fallback used when TestCmd is empty.
//
// Unknown/unresolvable codePath falls back to the exact go scaffold/cmd kept
// from the prior go-only implementation — callers that can fail instead
// (StartRun) preflight first and refuse the run rather than silently
// grading under the wrong language.
func advPoolBase(codePath string) (base map[string]string, testCmd []string) {
	if p, err := pluginFor(codePath); err == nil {
		return p.Scaffold(), p.TestCmd()
	}
	return goScaffold()
}

// JailScorer adapts adequacy.Score (the SAME deterministic, brain-side,
// jail-run mutation scorer the control gate uses) to advpool.Scorer. This is
// the soundness-#1 seam: the driver never trusts a worker's self-reported
// kill rate, only what this Scorer actually observes running in the jail.
type JailScorer struct {
	Jail adequacy.Jail
	// BaseFiles, when non-nil, switches the scorer into REPO-AWARE mode: the
	// jail workspace is seeded with these files (a whole cloned repo/package,
	// keyed by repo-relative path) instead of the synthetic single-file
	// scaffold, `codePath` is the repo-relative path of the file under audit
	// (so a mutant overwrites the real file IN CONTEXT), and the project's OWN
	// test command (the run's TestCmd) grades it. The dev's tests already live
	// in BaseFiles, so — unlike single-file mode — no synthetic dev-test is
	// overlaid. nil preserves the exact single-file behavior byte-for-byte.
	BaseFiles map[string]string
	// MutantTimeout, when > 0, overrides adequacy.Score's auto-derived
	// per-mutant timeout (see adequacy.WithMutantTimeout) with an explicit
	// cap — the plumbing for `certify --local --test-timeout`. The zero
	// value (so every existing JailScorer{} literal keeps today's behavior
	// unchanged) means auto-derive from the healthy baseline's own runtime.
	MutantTimeout time.Duration
	// DevTestPath is the repo-relative path of the DEV's own paired test, used
	// only to site the POOL-AUTHORED test somewhere the project actually
	// collects — see authoredTestPath. Empty (the zero value, so every
	// existing literal is unchanged) keeps the old sibling-of-the-code-file
	// placement, which single-file mode wants and which silently graded
	// nothing on any project that confines discovery to a test root.
	DevTestPath string
	// Lang and Selection shape the AUTHORED pass's command (authoredCmd);
	// the DEV pass's is built by its callers, from the same fields on the
	// RunSpec, via DevCommand. Lang names the plugin whose WithAuthoredTest appends a
	// test file's path — under selection the authored test is not in the
	// evidence and would otherwise never be collected, which reads as TEST
	// UNSOUND for every file.
	Lang      string
	Selection golang.Selection
	// Concurrency, when > 1, scores that many mutants at once. The zero value
	// (every existing JailScorer{} literal) is strictly sequential — today's
	// behavior, unchanged.
	//
	// THE CALLER OWNS THE SAFETY ARGUMENT, because it depends entirely on which
	// Jail is wired in (see adequacy.WithConcurrency): bwrapJail does its own
	// os.MkdirTemp per call and is safe; adequacy.WorkspacePool borrows one of
	// its N private trees per call and is safe up to N (its Trees() is the
	// only honest value); a bare adequacy.WorkspaceRunner mutates ONE checkout
	// in place with no mutex and MUST stay at 1. This field cannot
	// tell the difference, so it must never be set from anywhere that doesn't
	// know the substrate — see cmd/corral's resolveMutantConcurrency, which is
	// the single place that decision is made.
	Concurrency int
	// FailureParser, when non-nil, names the first test that failed on each
	// KILLED mutant, read out of the runner's own output (see
	// adequacy.WithFailureParser). nil — every existing JailScorer{} literal
	// — records nothing, which is what the ledger held before this existed.
	//
	// It is passed in rather than derived from Lang here so that the ONE
	// place that resolves a plugin for a run also decides whether that
	// plugin's runner output is parseable at all.
	FailureParser golang.FailureParser
}

// scoreOpts is the option list every adequacy.Score call in this file shares.
// Factored out because there are three such calls with identical options and a
// fourth would be added by the next feature — a per-call copy is exactly how
// one path silently keeps scoring sequentially while the others parallelize.
func (s JailScorer) baseScoreOpts() []adequacy.ScoreOption {
	return []adequacy.ScoreOption{
		adequacy.WithMutantTimeout(s.MutantTimeout),
		adequacy.WithConcurrency(s.Concurrency),
		adequacy.WithFailureParser(s.FailureParser),
	}
}

// gatedScoreOpts is baseScoreOpts PLUS the mutant compile gate. It is used by
// the DEV-scoring paths only — the ones that produce DevKillRate, the number
// the signed record asserts.
//
// ScoreAuthoredReport deliberately does NOT use it. The mutants it is handed
// are SURVIVORS of the dev pass, so they have already demonstrated they build
// and run; re-gating them buys nothing, costs a check per survivor, and adds a
// real failure mode — the authored test shares the workspace, so a test that
// compiles but trips a stricter checker would mark every survivor invalid and
// erase the very set being adjudicated.
func (s JailScorer) gatedScoreOpts(codePath string, base map[string]string) []adequacy.ScoreOption {
	opts := s.baseScoreOpts()

	// THE MUTANT COMPILE GATE. Without it, a mutant that does not build makes
	// the test command exit non-zero and adequacy scored that as a KILL — the
	// suite credited with catching a bug that never existed in runnable form.
	// The inflation lands hardest on low-coverage code, where more mutations
	// fail to build and fewer are genuinely caught, and corral's product is a
	// SIGNED record asserting "your tests catch K% of injected bugs".
	//
	// The check is the LANGUAGE PLUGIN's own (the same CompileCheck sequence
	// JailValidator.CompileTest already uses for authored tests), so nothing
	// here pattern-matches compiler output — which would silently misclassify
	// for python, ruby, javascript and typescript.
	p, err := pluginFor(codePath)
	if err != nil {
		// An unsupported language cannot be audited at all; it fails with a
		// real message elsewhere. Inventing a gate here would only turn that
		// into "every mutant invalid", which is a worse diagnosis.
		return opts
	}
	testPath := authoredTestPath(codePath, s.DevTestPath, s.BaseFiles)
	if _, ok := base[testPath]; !ok {
		// A per-file checker (python, ruby, node) is handed BOTH paths and
		// fails on one that is not in the workspace — which would mark EVERY
		// mutant invalid and erase the exam entirely. The code file is always
		// present, and gating the mutant is what this is for.
		testPath = codePath
	}
	if cc := p.CompileCheck(codePath, testPath); len(cc) > 0 {
		opts = append(opts, adequacy.WithMutantCompileCheck(cc))
	}
	return opts
}

func (s JailScorer) Score(ctx context.Context, codePath, code, test string, mutants []adequacy.Mutant, testCmd string) (float64, []adequacy.Mutant, error) {
	scoreBase, cmd := s.scoreWorkspace(codePath, test, testCmd)

	rep, err := adequacy.Score(ctx, s.Jail, scoreBase, codePath, code, mutants, cmd, s.gatedScoreOpts(codePath, scoreBase)...)
	if err != nil {
		return 0, nil, fmt.Errorf("advpool: score: %w", err)
	}
	return rep.KillRate(), survivorsFrom(rep, mutants), nil
}

// ScoreReport is Score's richer sibling: it reuses the SAME scoreWorkspace +
// adequacy.Score call Score itself makes above, but returns the raw
// adequacy.Report instead of collapsing it to a kill rate + survivor slice —
// so a caller can distinguish a baseline that couldn't pass (CompliantPass
// false) from a genuine zero-kill (CompliantPass true, len(Killed)==0).
//
// It runs EXACTLY the command it is handed. It used to narrow that command to
// the run's Selection itself, which broke the matrix — whose whole point is to
// score one named test selector at a time — and which is why narrowing now
// lives at the callers that mean "the run's command" (see DevCommand).
func (s JailScorer) ScoreReport(ctx context.Context, codePath, code, test string, mutants []adequacy.Mutant, testCmd string) (adequacy.Report, error) {
	scoreBase, cmd := s.scoreWorkspace(codePath, test, testCmd)

	rep, err := adequacy.Score(ctx, s.Jail, scoreBase, codePath, code, mutants, cmd, s.gatedScoreOpts(codePath, scoreBase)...)
	if err != nil {
		return adequacy.Report{}, fmt.Errorf("advpool: score report: %w", err)
	}
	return rep, nil
}

// ScoreFor is Score with a per-mutant command: identical in every respect
// except that each mutant is graded by cmdFor(m) instead of the shared
// testCmd. It is JailScorer's half of PerMutantScorer — see that interface
// for why the richer contract is optional. A nil cmdFor is exactly Score.
func (s JailScorer) ScoreFor(ctx context.Context, codePath, code, test string, mutants []adequacy.Mutant, testCmd string, cmdFor adequacy.CommandFor) (float64, []adequacy.Mutant, error) {
	scoreBase, cmd := s.scoreWorkspace(codePath, test, testCmd)

	opts := append(s.gatedScoreOpts(codePath, scoreBase), adequacy.WithCommandFor(cmdFor))
	rep, err := adequacy.Score(ctx, s.Jail, scoreBase, codePath, code, mutants, cmd, opts...)
	if err != nil {
		return 0, nil, fmt.Errorf("advpool: score: %w", err)
	}
	return rep.KillRate(), survivorsFrom(rep, mutants), nil
}

// ScoreReportFor is ScoreReport with a per-mutant command — JailScorer's
// other half of PerMutantScorer. The returned Report carries PerMutant: what
// each mutant was actually graded with, which is what lets the verdict
// disclose the narrowing instead of merely performing it.
//
// The baseline and the compile gate still run the shared command: they are
// questions about the FILE (does the suite pass at all, does this mutant
// compile), not about one mutant's span.
func (s JailScorer) ScoreReportFor(ctx context.Context, codePath, code, test string, mutants []adequacy.Mutant, testCmd string, cmdFor adequacy.CommandFor) (adequacy.Report, error) {
	scoreBase, cmd := s.scoreWorkspace(codePath, test, testCmd)

	opts := append(s.gatedScoreOpts(codePath, scoreBase), adequacy.WithCommandFor(cmdFor))
	rep, err := adequacy.Score(ctx, s.Jail, scoreBase, codePath, code, mutants, cmd, opts...)
	if err != nil {
		return adequacy.Report{}, fmt.Errorf("advpool: score report: %w", err)
	}
	return rep, nil
}

// ScoreAuthoredReport scores a POOL-authored test (the test-writer's output)
// against mutants — the AuthoredScorer extension tickPoolAdequacy prefers
// over ScoreReport. Identical to ScoreReport in single-file mode (BaseFiles
// nil): scoreWorkspace already overlays `test` at the synthetic path there.
//
// In repo-aware mode (BaseFiles set) it explicitly overlays `test` at
// advPoolTestPath(codePath) — the same path JailValidator.CompileTest already
// overlays it at (see CompileTest above) — instead of silently discarding it
// the way scoreWorkspace does. scoreWorkspace's drop is CORRECT for the DEV
// test (already on disk; overlaying would shadow the real suite — see its own
// doc) but WRONG for an AUTHORED test: it is a brand-new file the repo does
// not already contain, so there is nothing to shadow. Before this method
// existed, tickPoolAdequacy called Score/ScoreReport directly, and in
// repo-aware mode the authored test's content never reached any workspace the
// jail ran — the pool silently re-scored the DEV suite against its own
// already-known survivors, so they "survived" again and ProvenMissed computed
// to 0 for EVERY repo-aware run, unconditionally. The asymmetry was easy to
// miss because CompileTest DOES overlay (so the compile gate passed and
// TestWriterFailed stayed false) while SCORING silently used a workspace that
// never contained the test at all.
// RuleAuthoredAlone is the per-mutant rule the AUTHORED pass records: the
// survivor was run against the authored test and nothing else.
const RuleAuthoredAlone = "authored-alone"

// AuthoredAlone reports whether this run's authored pass proves survivors
// with the authored test alone — true exactly when the language has a
// TestSelector and the run carries a selection. It is spec-derived so the
// verdict can say it without asking the scorer.
func AuthoredAlone(rs RunSpec) bool {
	return selectorFor(rs.Lang, rs.Selection) != nil
}

func (s JailScorer) ScoreAuthoredReport(ctx context.Context, codePath, code, test string, mutants []adequacy.Mutant, testCmd string) (adequacy.Report, error) {
	scoreBase, base := s.scoreWorkspace(codePath, test, testCmd)
	cmd := s.authoredCmd(codePath, base)
	if s.BaseFiles != nil {
		scoreBase = s.authoredWorkspace(codePath, test)
	}

	// The mutants here are SURVIVORS of the dev pass: no selected dev test
	// killed them, so re-running those tests cannot kill them either — the
	// only test that can is the authored one. Each survivor therefore runs
	// the authored test ALONE, which is both cheaper (one test instead of
	// the file's selection, per survivor) and more honest: a dev test that
	// flaked during this pass used to count as the authored test proving a
	// gap. The compliance baseline and the canary keep the shared command —
	// they ask whether the authored test is real, not whether it kills.
	opts := s.baseScoreOpts()
	if ts := s.selector(); ts != nil {
		alone := ts.WithAuthoredTest(golang.Selection{Base: s.Selection.Base}, base, authoredTestPath(codePath, s.DevTestPath, s.BaseFiles))
		opts = append(opts, adequacy.WithCommandFor(func(adequacy.Mutant) adequacy.MutantCommand {
			return adequacy.MutantCommand{Cmd: alone, Tests: 1, Rule: RuleAuthoredAlone}
		}))
	}
	rep, err := adequacy.Score(ctx, s.Jail, scoreBase, codePath, code, mutants, cmd, opts...)
	if err != nil {
		return adequacy.Report{}, fmt.Errorf("advpool: score authored report: %w", err)
	}

	// POSITIVE CONTROL (repo-aware mode only — single-file mode has no
	// project discovery config to be excluded by; see
	// verifyAuthoredTestReaches' own doc). CompliantPass and CanaryKilled
	// above are checks on the WHOLE test command, which a dev suite that
	// already transitively imports codePath satisfies on its own — they say
	// nothing about whether cmd ever actually reached the authored test's
	// own file. Only worth running once mutants were actually scored
	// (rep.Total > 0); a CompliantPass==false or Total==0 run is already
	// unsound via the existing checks below, with no need to spend another
	// jail run proving it twice.
	if s.BaseFiles != nil && rep.Total > 0 {
		reaches, verr := s.verifyAuthoredTestReaches(ctx, codePath, cmd)
		if verr != nil {
			return adequacy.Report{}, fmt.Errorf("advpool: positive-control authored test: %w", verr)
		}
		if !reaches {
			// A DIAGNOSIS, not a score: the run never actually reached the
			// authored test, so nothing rep observed about the survivors
			// means anything. Route it through the SAME CanaryKilled==false
			// path tickPoolAdequacy already treats as PoolTestUnsound —
			// no new state needed, this is exactly what "the suite never
			// read the file" already means, just proven against the
			// authored test's own path instead of codePath.
			rep.CanaryKilled = false
			// ...and say WHICH of the two things went wrong. Routing this into
			// CanaryKilled alone was correct about the mechanism and lossy
			// about the diagnosis: the operator was told the authored test
			// "did not pass on the unmutated code (or never reads the file)"
			// and left to guess. It is nearly always the command, and the fix
			// is one word wider — but only if we say so.
			rep.AuthoredTestUnreached = true
		}
	}
	return rep, nil
}

// CompliantFailure runs the authored test against the UNMUTATED code and
// returns the runner's own combined output — the detail that turns a
// clean-code-failure retry into a corrective one, exactly as CompileError's
// output does for a build failure.
//
// Reuses the SAME verboseJail optional extension CompileTest already uses, so
// a backend that can surface output does, and one that cannot degrades to ""
// rather than failing the run. Costs one extra run, and only on the failure
// path — the scoring run that produced CompliantPass=false has already
// returned by the time this is called, and Report carries no output to reuse.
func (s JailScorer) CompliantFailure(ctx context.Context, codePath, code, test, testCmd string) string {
	vj, ok := s.Jail.(verboseJail)
	if !ok {
		return ""
	}
	ws, cmd := s.scoreWorkspace(codePath, test, testCmd)
	if s.BaseFiles != nil {
		ws = s.authoredWorkspace(codePath, test)
	}
	// The UNMUTATED code: this asks "why does your test fail on software that
	// is correct", which is the only question worth feeding back.
	ws[codePath] = code
	_, out, err := vj.RunTestVerbose(ctx, ws, cmd)
	if err != nil {
		return ""
	}
	return out
}

// authoredWorkspace builds the repo-aware scoring workspace with `content`
// overlaid at the authored test's own path — the SAME overlay
// ScoreAuthoredReport itself needs (a brand-new file the repo does not
// already contain, so there is nothing to shadow; see the method's own doc)
// and the positive control below needs too, just with the canary in place
// of the real test. Extracted so both call sites build the identical
// workspace shape.
func (s JailScorer) authoredWorkspace(codePath, content string) map[string]string {
	ws := make(map[string]string, len(s.BaseFiles)+1)
	for k, v := range s.BaseFiles {
		ws[k] = v
	}
	ws[authoredTestPath(codePath, s.DevTestPath, s.BaseFiles)] = content
	return ws
}

// verifyAuthoredTestReaches is corral's POSITIVE CONTROL for a pool-authored
// test scored in repo-aware mode: the same idea as adequacy.CanaryCode
// (deliberately invalid source that any check genuinely reading a file MUST
// fail on), applied to the AUTHORED TEST'S OWN FILE instead of the code
// under audit. ScoreAuthoredReport's ordinary CompliantPass/CanaryKilled
// checks run the WHOLE project test command and ask "did SOMETHING fail" —
// satisfied for free whenever the dev suite already imports codePath
// (extremely common), regardless of whether the authored test ever ran at
// all. This asks a narrower, unmasked question: does the project's own
// unmodified test command even NOTICE the file at the authored test's own
// path?
//
// It overlays adequacy.CanaryCode — invalid source in every language corral
// supports — at exactly the path ScoreAuthoredReport itself writes the
// authored test to (advPoolTestPath(codePath)), leaves codePath at its real,
// compiling content, and runs cmd unmodified (the SAME command the real
// scoring run used, never a scoped/explicit-path variant — the point is to
// prove what the ACTUAL scoring invocation does, not what a more targeted
// one could do). Two outcomes:
//
//   - The run FAILS (or times out — a hang IS a reaction, same convention
//     adequacy.Score's own canary uses): the command genuinely parses/
//     imports the file at that path, so it plausibly saw the real authored
//     test too. reaches=true.
//   - The run PASSES: nothing at that path affected the outcome — either
//     the project's own discovery config never looks there (pallets/flask's
//     `testpaths = ["tests"]` excluding src/flask/, the shape this fix
//     exists for) or something else masks it. Either way the authored
//     test's own verdict from the real scoring run is worthless.
//     reaches=false.
func (s JailScorer) verifyAuthoredTestReaches(ctx context.Context, codePath string, cmd []string) (bool, error) {
	ws := s.authoredWorkspace(codePath, adequacy.CanaryCode)
	passed, err := s.Jail.RunTest(ctx, ws, cmd)
	if err != nil {
		if errors.Is(err, adequacy.ErrTestTimeout) {
			return true, nil // hung on invalid source at the authored test's path -> it reacted
		}
		return false, err
	}
	return !passed, nil
}

// AuthoredTestPathFor is the path the pool-authored test is written to, for
// an error message that can name the file rather than describe it.
func AuthoredTestPathFor(codePath, devTestPath string) string {
	return authoredTestPath(codePath, devTestPath, nil)
}

// AuthoredTestWouldBeCollected reports whether the project's own test command
// would RUN a test written to the authored path — the exported form of the
// positive control, so a caller can ask BEFORE spending a run rather than
// learning it from an unsound verdict afterwards.
//
// The check existed already; only its timing was wrong. It ran after the
// test-writer had authored a killing test, which meant an operator whose
// command names a single FILE paid for a full audit, received
// proven_missed: 0, and was then told to widen the command and run again. Two
// runs and a contradictory-looking pair of verdicts to discover something one
// jail execution and no model calls can settle up front.
//
// authoredTestPath deliberately lands the file in the DEV TEST's directory on
// the reasoning that the directory is "collected by construction". That holds
// for a command naming a directory or a discovery-based runner, and fails for
// a command naming one file — `node tests/x.test.js`, `pytest tests/x_test.py`
// — which is an ordinary way to invoke a suite, not an exotic one.
//
// Returns (true, nil) when the authored test would be collected. Costs one
// jail run and nothing else.
func (s JailScorer) AuthoredTestWouldBeCollected(ctx context.Context, codePath string, cmd []string) (bool, error) {
	if s.BaseFiles == nil {
		// Single-file mode overlays the test at the plugin's own synthetic
		// path and supplies the command, so collection is not in question.
		return true, nil
	}
	return s.verifyAuthoredTestReaches(ctx, codePath, cmd)
}

// selector is the run's TestSelector, or nil when the run has no Selection
// or the language has no selector — in which case the authored pass runs the
// resolved command untouched, exactly as before selection existed.
func (s JailScorer) selector() golang.TestSelector {
	return selectorFor(s.Lang, s.Selection)
}

// DevCommand is the DEV pass's command for a run: the string a caller that
// means "grade the dev suite as this run grades it" hands to a Scorer.
//
// It is a CALLER-side helper, not a scorer-internal rewrite, and that is the
// fix it embodies. As a rewrite inside ScoreReport it did two wrong things at
// once: it narrowed a command the caller had chosen deliberately (the
// matrix's own per-test selector — see driver_matrix.go), and it did nothing
// at all for Score, so the shadow pass graded the WHOLE suite against the
// same mutants the primary graded against the selection. Two passes that are
// meant to be a controlled comparison were answering different questions.
//
// Every caller that means "the run's command" calls this; a caller that
// issues its own command (the matrix, the critic's auto-refute) passes that
// command through untouched.
// The run's own TestCmd string is returned VERBATIM when nothing narrowed,
// not re-rendered: ShellJoin quotes every element, so round-tripping an
// unchanged command would rewrite `pytest tests/` as `'pytest' 'tests/'` on
// every pre-selection path. Equivalent to a shell, but not byte-identical,
// and this string is carried, logged and compared.
func DevCommand(rs RunSpec) string {
	base := adequacy.ShellSplit(rs.TestCmd)
	narrowed := DevCommandArgv(rs.Selection, rs.Lang, base, rs.DevTestPath)
	if slices.Equal(narrowed, base) {
		return rs.TestCmd
	}
	return adequacy.ShellJoin(narrowed)
}

// DevCommandArgv is DevCommand's argv form, and the SINGLE definition of what
// the dev pass runs. cmd/corral's executor resolves its own baseline command
// for the same job (see localExecutor.testCmd) and the two must be identical
// — a narrowed scoring run graded against an unnarrowed baseline compares
// different things and silently corrupts every kill rate — so both go through
// here rather than each re-deriving it.
//
// The selection's own narrowed command when it selected tests. Nothing is
// appended: the dev suite already lives in the checkout. When the selection
// is EMPTY (uncovered) the paired dev test file runs ALONE — the evidence
// says it never reaches the code, so every mutant survives BY MEASUREMENT at
// the cost of one small file, and the verdict is marked Uncovered rather than
// printing that 0.00.
//
// A zero Selection (or a language with no selector) returns base unchanged,
// byte-identical to every pre-selection path.
func DevCommandArgv(sel golang.Selection, langName string, base []string, devTestPath string) []string {
	ts := selectorFor(langName, sel)
	if ts == nil {
		return base
	}
	if len(sel.Tests) > 0 {
		return append([]string{}, sel.Cmd...)
	}
	if devTestPath == "" {
		// Uncovered, but there is no paired test file to run alone. A repo
		// candidate always has one (pairing is what made it a candidate), so
		// this is the defensive branch: appending "" would hand pytest an
		// empty positional argument, and falling through to a bare stripped
		// base would collect the ENTIRE suite while the record said
		// "uncovered". Return the base the caller already had — the same
		// whole-suite command every pre-selection path ran — and let the
		// Selection's own Fallback/Uncovered fields say what happened.
		return base
	}
	return ts.WithAuthoredTest(sel, base, devTestPath)
}

// DevCommandFor is DevCommand at the MUTANT grain: the closure the dev (and
// shadow) pass hands a scorer so each mutant is graded by the tests that
// actually reach the lines it changed, rather than by the whole file
// selection every mutant shares.
//
// It returns nil — meaning "exactly today's behaviour" — whenever there is
// nothing honest to narrow BY: no per-test line evidence (a v1-shaped
// Selection recorded before evidence carried line ranges), no selected tests
// (an uncovered file: the file selection is already empty, and narrowing an
// empty set is not a measurement), or no TestSelector for the language. A nil
// CommandFor is the one value adequacy.WithCommandFor treats as "grade every
// mutant with the shared command", so the fallback is byte-for-byte the
// pre-per-mutant path rather than a second, subtly different one.
func DevCommandFor(rs RunSpec) adequacy.CommandFor {
	sel := rs.Selection
	// Evidence that cannot narrow the tests that will actually run is no
	// evidence: a whole-suite run, a v1-shaped Selection, an uncovered file,
	// or one whose node ids were collapsed to containing files. Each of them
	// must grade with the file's one shared command — today's behaviour,
	// byte for byte — rather than per mutant against a lookup that misses.
	if !sel.NarrowableByLine() {
		return nil
	}
	ts := selectorFor(rs.Lang, sel)
	if ts == nil {
		return nil
	}
	return func(m adequacy.Mutant) adequacy.MutantCommand {
		cmd, tests, rule := ts.ForSpan(sel, m.Span)
		return adequacy.MutantCommand{Cmd: cmd, Tests: len(tests), Rule: rule}
	}
}

// selectorFor is the run's TestSelector, or nil when the run has no
// Selection or the language has no selector.
func selectorFor(langName string, sel golang.Selection) golang.TestSelector {
	if sel.Method == "" {
		return nil
	}
	p, ok := golang.ByName(langName)
	if !ok {
		return nil
	}
	ts, _ := p.(golang.TestSelector)
	return ts
}

// AuthoredCommand is the command the AUTHORED pass really runs for codePath,
// given the run's own base command — the exported form of authoredCmd, for a
// caller that must ask a question ABOUT that pass before it happens.
//
// The pre-flight (AuthoredTestWouldBeCollected) is the caller this exists
// for. It was being handed the run's BASE command, which is not what the
// authored pass runs: the authored pass appends the authored test's own path
// precisely so a project whose discovery config excludes it still collects
// it. Checking the base command therefore refused audits — before any model
// ran, naming the operator's command as the fault — for a problem the run had
// already solved.
func (s JailScorer) AuthoredCommand(codePath string, base []string) []string {
	return s.authoredCmd(codePath, base)
}

// authoredCmd is the AUTHORED pass's command: the selection plus the path
// the authored test is actually placed at (authoredWorkspace writes it
// there). The evidence run never saw that file, so without this the
// narrowed command would never collect it and every file would read
// TEST UNSOUND.
func (s JailScorer) authoredCmd(codePath string, cmd []string) []string {
	ts := s.selector()
	if ts == nil {
		return cmd
	}
	return ts.WithAuthoredTest(s.Selection, cmd, authoredTestPath(codePath, s.DevTestPath, s.BaseFiles))
}

// scoreWorkspace builds the jail base file-map and the test command for a
// scoring run. In single-file mode (BaseFiles nil) it reproduces the original
// behavior exactly: the language scaffold plus the dev test overlaid at the
// plugin's synthetic test path, defaulting the command to the plugin's when
// the run carries none. In repo-aware mode (BaseFiles set) the whole repo IS
// the base, the dev test already lives inside it (so `test` is NOT overlaid —
// overlaying would shadow the real suite), and the run's own TestCmd (the
// project's command) is authoritative — there is no synthetic default.
func (s JailScorer) scoreWorkspace(codePath, test, testCmd string) (map[string]string, []string) {
	if s.BaseFiles != nil {
		base := make(map[string]string, len(s.BaseFiles))
		for k, v := range s.BaseFiles {
			base[k] = v
		}
		return base, adequacy.ShellSplit(testCmd)
	}
	base, defaultCmd := advPoolBase(codePath)
	cmd := adequacy.ShellSplit(testCmd)
	if len(cmd) == 0 {
		cmd = defaultCmd
	}
	scoreBase := make(map[string]string, len(base)+1)
	for k, v := range base {
		scoreBase[k] = v
	}
	scoreBase[advPoolTestPath(codePath)] = test
	return scoreBase, cmd
}

// JailEnumerator implements the driver's TestEnumerator over the SAME
// jail/workspace conventions JailScorer uses (scoreWorkspace), so a matrix
// enumeration sees the identical scaffold/BaseFiles-mode a subsequent
// ScoreReport call for one of its selectors would see. It holds an
// adequacy.Enumerator rather than adequacy.Jail — RunTest only ever answers
// pass/fail, never stdout, so enumeration needs the stdout-capturing sibling
// (see internal/adequacy.Enumerator).
type JailEnumerator struct {
	Jail adequacy.Enumerator
	// BaseFiles mirrors JailScorer.BaseFiles: nil preserves single-file mode.
	BaseFiles map[string]string
}

// Enumerate builds the same jail workspace scoreWorkspace would for a scoring
// run (base scaffold/whole-repo plus the dev test overlaid at its synthetic
// path in single-file mode), overlays codePath -> code exactly as
// adequacy.Score's own run closure does, and runs listCmd through the
// stdout-capturing jail.
func (e JailEnumerator) Enumerate(ctx context.Context, codePath, code, test string, listCmd []string) (string, error) {
	scoreBase, _ := (JailScorer{BaseFiles: e.BaseFiles}).scoreWorkspace(codePath, test, "")
	files := make(map[string]string, len(scoreBase)+1)
	for k, v := range scoreBase {
		files[k] = v
	}
	files[codePath] = code
	return e.Jail.Enumerate(ctx, files, listCmd)
}

// JailValidator brain-side-validates a worker's structured artifacts before
// the driver trusts them: CompileTest jail-compiles a candidate test against
// the code (via `go vet`, which type-checks test files without executing
// them — the "does it compile" check, never "does it pass", which would
// corrupt CompileTest's meaning); ParseMutants is testgen's proven
// mutant-output parser (the Task 1.2 seam), reused verbatim so a distributed
// worker's raw response parses identically to the in-process generator's own
// output.
//
// CompileTest MUST cover the same scope the Scorer actually runs against
// (I-1): a subdirectory code_path (e.g. internal/auth/login.go) lands the
// candidate code+test under internal/auth/ in the jail workspace, so
// `go vet ./` (module root, non-recursive) sees zero .go files there and
// fails EVERY authored test regardless of whether it actually compiles —
// the run then never converges. `go vet ./...` is recursive and always
// covers whatever directory the files actually landed in.
type JailValidator struct {
	Jail adequacy.Jail
	// BaseFiles mirrors JailScorer.BaseFiles: in repo-aware mode the authored
	// test is compile-checked against the WHOLE repo (so a test that imports
	// the package resolves), not the bare single-file scaffold. nil preserves
	// the original single-file behavior.
	BaseFiles map[string]string
	// DevTestPath mirrors JailScorer.DevTestPath and MUST be set to the same
	// value: CompileTest overlays the candidate test at authoredTestPath, and
	// if the validator and the scorer disagreed about that path the pool would
	// compile-check a file at one location and grade a file at another.
	DevTestPath string
}

// CompileError is returned by CompileTest when the authored test builds a
// workspace but does not compile. It carries the FAILING command's own
// compiler output (never a preceding command's — see the note in
// CompileTest's loop) so the driver can feed it back to the test-writer on
// retry instead of blindly re-asking — a bare "does not compile" taught the
// writer nothing, so it repeated the same mistake until it exhausted its
// attempts. Output is empty only when the jail could not surface it (the
// RunTest fallback path).
type CompileError struct {
	Output string
	// Cmd is the argv of the specific command in CompileCheck's sequence
	// that failed (or is empty, for the RunTest fallback / the empty-sequence
	// case), included in Error() so a multi-command sequence (ruby -c /
	// node --check, one file per invocation) says WHICH file's check failed,
	// not just that the sequence as a whole didn't pass.
	Cmd []string
}

func (e *CompileError) Error() string {
	switch {
	case strings.TrimSpace(e.Output) == "" && len(e.Cmd) == 0:
		return "advpool: test does not compile"
	case strings.TrimSpace(e.Output) == "":
		return fmt.Sprintf("advpool: test does not compile (command %v)", e.Cmd)
	default:
		return fmt.Sprintf("advpool: test does not compile (command %v):\n%s", e.Cmd, e.Output)
	}
}

// verboseJail is the optional Jail extension that also returns the compiler
// output. bwrapJail implements it; a Jail that doesn't falls back to the bare
// RunTest path below — still a correct pass/fail, just no output to feed back.
type verboseJail interface {
	RunTestVerbose(ctx context.Context, files map[string]string, cmd []string) (bool, string, error)
}

func (v JailValidator) CompileTest(ctx context.Context, codePath, code, test string) error {
	p, err := pluginFor(codePath)
	if err != nil {
		return err
	}
	base := p.Scaffold()
	if v.BaseFiles != nil {
		base = v.BaseFiles
	}
	ws := make(map[string]string, len(base)+2)
	for k, val := range base {
		ws[k] = val
	}
	ws[codePath] = code
	testPath := authoredTestPath(codePath, v.DevTestPath, v.BaseFiles)
	ws[testPath] = test

	// CompileCheck returns a SEQUENCE of commands (see lang.Plugin.CompileCheck's
	// doc comment): most plugins yield exactly one, but a plugin whose checker
	// can only look at one file per invocation (ruby -c, node --check) yields
	// one per file, meant to run in order and stop at the first failure — the
	// exact "run A, then B only if A succeeded" semantics a shell's `&&` used
	// to express. This runs that sequence directly over v.Jail, WITHOUT ever
	// joining it into a single shell command: v.Jail may be a bare
	// exec.Command on the workspace substrate (internal/adequacy/workspace.go),
	// which has no shell to interpret `&&` at all — see this method's own
	// history (the pallets/flask PYTHONPYCACHEPREFIX regression, and its
	// ruby/js && sibling) for exactly what silently breaks when a plugin's
	// CompileCheck output assumes one.
	seq := p.CompileCheck(codePath, testPath)
	if len(seq) == 0 {
		// An empty sequence must NOT read as "nothing to check, therefore
		// compiles": that is exactly the failure mode this method's own
		// history is about — a validation gate reporting "compiles" without
		// ever invoking a single command. No plugin returns this today (all
		// five yield >=1 command), but a future plugin's early-return, or a
		// stub/fake Jail-adjacent Plugin (e.g. a test double) forgetting a
		// case, must fail CLOSED here, not silently pass every test as
		// compiling. See lang.Plugin.CompileCheck's doc comment for the
		// contract this enforces.
		return fmt.Errorf("advpool: compile-verify test: %s plugin's CompileCheck(%q, %q) returned an empty command sequence — refusing to report a compile pass without running anything", p.Name(), codePath, testPath)
	}
	vj, verbose := v.Jail.(verboseJail)
	for _, cmd := range seq {
		if verbose {
			compiles, output, err := vj.RunTestVerbose(ctx, ws, cmd)
			if err != nil {
				return fmt.Errorf("advpool: compile-verify test: %w", err)
			}
			if !compiles {
				// Output/Cmd carry ONLY this failing command's own output —
				// never a preceding, PASSING command's output. A multi-command
				// sequence (ruby -c code, ruby -c test) means a passing first
				// command would otherwise prefix the real diagnostic with a
				// success message ("Syntax OK\n<real error>"), which is noise
				// the test-writer has to see past on retry, not a fact worth
				// feeding it. Cmd names which command in the sequence failed,
				// since "test does not compile" alone no longer says whether
				// it was the code file or the test file's own check.
				return &CompileError{Output: output, Cmd: cmd}
			}
			continue
		}
		compiles, err := v.Jail.RunTest(ctx, ws, cmd)
		if err != nil {
			return fmt.Errorf("advpool: compile-verify test: %w", err)
		}
		if !compiles {
			return &CompileError{Cmd: cmd}
		}
	}
	return nil
}

func (v JailValidator) ParseMutants(raw, original string) ([]adequacy.Mutant, error) {
	return testgen.ParseMutantsOutput(raw, original)
}

func (v JailValidator) ParseTest(raw string) string {
	return testgen.ParseTestOutput(raw)
}

// CertSigner signs a terminal Verdict via the SAME certify chain
// certifyBuild/report_build uses — mirroring certifyBuild's own body
// (internal/brain/buildcert.go), duplicated here (rather than called) since
// this leaf package cannot import internal/brain (brain already imports
// advpool; the reverse would be a cycle). The verdict is marshaled and
// sha256-digested (mirroring controlgate.PostControlGate's digest pattern)
// so the signed record's output_digest is a tamper-evident fingerprint of
// every Verdict field (subject = repo@commit, byproducts = the digest), then
// certified with a distinct actor so a signed advpool record is never
// confused with a human `corral certify` submission, a merge-gate run, or a
// control-gate run.
//
// CertSigner implements the driver's Signer interface, and is deliberately
// narrower than brain.Options: it takes ONLY the three fields SignVerdict
// actually reads (the signing key, the build store, the transparency
// witness) — no Telemetry field, so unlike the brain-hosted advpoolSigner
// this does not emit the brain's "build_certified" telemetry event; that is
// the one intentional behavior narrowing of this move (the CLI has no
// telemetry store to feed).
type CertSigner struct {
	Key     ed25519.PrivateKey
	Store   *buildstore.Store
	Witness transparency.Witness
}

func (s CertSigner) SignVerdict(ctx context.Context, v Verdict) (int64, string, error) {
	exitCode := 0
	if v.Status != StatusCertified {
		exitCode = 1
	}
	b, err := json.Marshal(v)
	if err != nil {
		return 0, "", fmt.Errorf("advpool: marshal verdict: %w", err)
	}
	sum := sha256.Sum256(b)
	digest := "sha256:" + hex.EncodeToString(sum[:])

	// producedBy surfaces the run's role assignment as human-readable
	// "role:model" strings directly on the signed record (M-2), rather than
	// leaving the models only re-derivable by unpacking output_digest against
	// a separately-stored Verdict. Sorted so the record is deterministic.
	roles := make([]string, 0, len(v.ModelsByRole))
	for role := range v.ModelsByRole {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	producedBy := make([]string, 0, len(roles))
	for _, role := range roles {
		entry := role + ":" + v.ModelsByRole[role]
		if role == RoleMutantGeneratorShadow || role == RoleTestWriterShadow {
			// A challenger seat is measurement, not the exam: the generator's
			// mutants never entered the set the dev suite was graded against,
			// and the writer's authored suite never touched ProvenMissed. An
			// unmarked entry here would read to a record's audience as a model
			// that helped SET the certification, so say plainly that it did
			// not.
			entry += " (non-gating)"
		}
		producedBy = append(producedBy, entry)
	}

	const actor = "corral-advpool"
	steps := []certify.Step{
		{
			Kind:    "context",
			Actor:   actor,
			Subject: v.Repo + "@" + v.Commit,
			Detail: map[string]any{
				"repo":   v.Repo,
				"commit": v.Commit,
				"branch": "",
			},
		},
		{
			Kind:    "execution",
			Actor:   actor,
			Subject: "corral/adversarial-pool",
			Detail: map[string]any{
				"exit_code":       exitCode,
				"ok":              exitCode == 0,
				"duration_s":      0.0,
				"output_digest":   digest,
				"regions_total":   v.RegionsTotal,
				"regions_probed":  v.RegionsProbed,
				"dropped_regions": v.DroppedRegions,
			},
		},
	}
	built, head := certify.BuildLedger(steps)

	br := certify.BuildRecord{
		Repo:         v.Repo,
		Commit:       v.Commit,
		Actor:        actor,
		Command:      "corral/adversarial-pool",
		ExitCode:     exitCode,
		OutputDigest: digest,
		ProducedBy:   producedBy,
	}
	stmt := certify.BuildAttestation(br, head)

	// Sign the FULL canonical statement (not just the head) as a DSSE
	// envelope: a head-only signature leaves the predicate freely editable in
	// storage without invalidating the signature. The envelope embeds its own
	// copy of the canonical statement bytes it signed, so a later VerifyDSSE
	// call checks the identical bytes the signature covers with no separate
	// canonical-bytes column to keep in sync.
	envelope, err := certify.SignDSSE(stmt, s.Key, "brain")
	if err != nil {
		return 0, "", fmt.Errorf("advpool: signing statement: %w", err)
	}
	canonical, err := certify.CanonicalStatement(stmt)
	if err != nil {
		return 0, "", fmt.Errorf("advpool: canonicalizing statement: %w", err)
	}

	stepsJSON, err := certify.MarshalSteps(built)
	if err != nil {
		return 0, "", fmt.Errorf("advpool: marshaling steps: %w", err)
	}

	// Anchor the signed envelope to the transparency witness — an ADDITIONAL
	// trustless guarantee, never a build-blocking gate: an unreachable log
	// must degrade the record to anchored=false, not fail the run. s.Witness
	// == nil means anchoring is disabled entirely (same outcome).
	var rekorJSON string
	var anchored bool
	if s.Witness != nil {
		entry, anchorErr := s.Witness.Anchor(ctx, envelope)
		if anchorErr != nil {
			// Loud but keyless: never log the envelope's signing key material
			// (there is none here — envelope/entry are both public artifacts —
			// but keep the message scoped to repo/commit for consistency with
			// the brain's own warning style).
			log.Printf("advpool: transparency witness unreachable for %s@%s, degrading to anchored=false: %v", v.Repo, v.Commit, anchorErr)
		} else {
			entryJSON, marshalErr := json.Marshal(entry)
			if marshalErr != nil {
				log.Printf("advpool: encoding transparency entry for %s@%s, degrading to anchored=false: %v", v.Repo, v.Commit, marshalErr)
			} else {
				rekorJSON = string(entryJSON)
				anchored = true
			}
		}
	}

	// pass mirrors the "execution" step's own ok field above — it's the same
	// exit_code == 0 check, denormalized to a queryable column so a dashboard
	// doesn't have to unpack steps/statement JSON per row for a cheap status
	// filter.
	pass := exitCode == 0

	id, err := s.Store.Save(v.Repo, v.Commit, "", actor, head, string(envelope), string(canonical), string(stepsJSON), rekorJSON, anchored,
		"", "", "", "", pass)
	if err != nil {
		return 0, "", err
	}
	return id, head, nil
}

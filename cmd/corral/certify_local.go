// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/agentworker"
	"github.com/pdbethke/corralai/internal/buildstore"
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/repoindex"
	"github.com/pdbethke/corralai/internal/reposcan"
	"github.com/pdbethke/corralai/internal/sandbox"
)

// Decorrelation default (design 2026-07-18): two DISTINCT Claude models off a
// single ANTHROPIC_API_KEY. test-writer and mutant-generator share the strong
// model; the test-critic is a different (cheaper, decorrelated) model so it is
// never grading tests written by its own model — CheckDecorrelation is
// satisfied with one key. Any of the three is overridable via
// --writer-model / --critic-model / --mutant-model.
const (
	defaultLocalWriterModel = "claude-sonnet-5"
	defaultLocalMutantModel = "claude-sonnet-5"
	defaultLocalCriticModel = "claude-haiku-4-5"
)

// defaultLocalShadowModel is the challenger seat's model. Cheap and already the
// critic's model, so it needs no additional provider credential. Mirrors
// advpool.DefaultShadowModel — kept as a local alias (not a straight `=
// advpool.DefaultShadowModel`) only so this file's existing doc comment/name
// stay put; the brain's hosted path resolves the SAME constant via
// advpool.ResolveShadowModel.
const defaultLocalShadowModel = advpool.DefaultShadowModel

// localBee is the queue bee name the single in-process worker claims under.
// A local run has exactly one claimant, so the name is a constant.
const localBee = "corral-local"

// localMissionID is the fixed run/mission id for a --local run. The queue is a
// fresh, ephemeral SQLite store per invocation (one run, one claimant), so the
// driver's caller-supplied mission id can be a constant — there is no mission
// table to collide with (queue.Store is standalone).
const localMissionID = 1

// localLeaseSeconds is the claim lease for the in-process worker. Generous
// because a single frontier LLM role can take a while and there is no rival
// claimant to hand off to — the lease only ever matters as the queue's own
// bookkeeping, never for contention.
const localLeaseSeconds = 3600

// localCertifyThreshold is the minimum dev kill-rate a --local run auto-certifies
// at — the same human-gate threshold the brain's pool uses by default. Below it
// (or with any blocking finding) the run routes to needs-review, never certified.
const localCertifyThreshold = 0.8

// runCertifyLocal implements `corral certify --local`: a COMPLETE adversarial-pool
// audit run entirely in-process — no brain daemon, no MCP, no OIDC token, no
// separate worker processes. It reads the user's own provider key from the
// environment, drives the pure advpool.Driver over a real jail-backed
// Scorer/Validator and the real certify-chain Signer, and prints a signed,
// offline-verifiable verdict. Soundness is unchanged from the distributed path:
// the kill-rate is still measured by executing tests in a sandbox jail (never a
// self-report), decorrelation is still enforced, and the verdict is still signed.
func runCertifyLocal(args []string, stdout, stderr io.Writer) int {
	flagArgs, checkArgv := splitCertifyArgs(args)

	fs := flag.NewFlagSet("certify --local", flag.ContinueOnError)
	fs.SetOutput(stderr)
	_ = fs.Bool("local", false, "run the adversarial pool in-process (this mode)")
	codePath := fs.String("code", "", "path of the code under review (required)")
	testPath := fs.String("test", "", "path of the dev's test (default: the sibling test of --code)")
	langFlag := fs.String("lang", "", "source language (default: inferred from --code extension)")
	goal := fs.String("goal", "", "the correctness/security goal the code must satisfy (required)")
	nMutants := fs.Int("n-mutants", 0, "PER-SHARD seeded-violation mutant budget (default 5) — this is NOT the run's total: total mutants scored scale with --max-shards (default "+fmt.Sprint(advpool.DefaultMaxShards)+") shards, and DOUBLE again if the shadow challenger is on (default). E.g. the default 5 with the default 8 shards means up to ~40 primary + ~40 shadow = ~80 full dev-suite jail executions, not 5 — `--n-mutants 20` means roughly ~320")
	writerModel := fs.String("writer-model", "", "model for the test-writer role (default "+defaultLocalWriterModel+")")
	criticModel := fs.String("critic-model", "", "model for the test-critic role, which must differ from the writer's; \"off\" disables the critic entirely (it is advisory and never gates the verdict, so a single-vendor run with only one usable model can drop it) (default "+defaultLocalCriticModel+")")
	mutantModel := fs.String("mutant-model", "", "model for the mutant-generator role (default "+defaultLocalMutantModel+")")
	jailFlag := fs.String("jail", "", "sandbox backend: bwrap|container (Linux), sandbox-exec (macOS) (default: auto-detect for this OS; \"none\" is not supported — --local always sandboxes)")
	timeout := fs.Duration("timeout", 10*time.Minute, "give up if the run makes no progress for this long (not a hard wall-clock cap — a single slow LLM call can overshoot it)")
	testTimeout := fs.Duration("test-timeout", 0, "hard cap on a SINGLE test-suite run in the jail (0 = auto: derived from the healthy suite's own runtime, so a mutant that makes the suite hang is killed fast instead of eating the whole --timeout). Raise it only if your suite legitimately runs long")
	poll := fs.Duration("poll", 2*time.Second, "how long to wait between drive iterations when nothing is claimable")
	repoFlag := fs.String("repo", "", "repository (default: git remote.origin.url, else \"local\")")
	commitFlag := fs.String("commit", "", "commit sha (default: git rev-parse HEAD, else \"local\")")
	outFlag := fs.String("out", "", "also write the signed verdict as a self-contained record file, re-verifiable offline with `corral certify verify <file> --pubkey <hex> --allow-unanchored`")
	repoDirFlag := fs.String("repo-dir", "", "audit --code IN THE CONTEXT of this cloned repo/package: the whole tree is seeded into the jail, the file is mutated in place, and the project's OWN test command (given after `--`) grades it — so real multi-file projects with package imports work (--code/--test are repo-relative)")
	recordFlag := fs.String("record", "", "write a replayable tape of the run (the pool's reasoning beats, task lifecycle, and findings) to this JSON file — the same {events:[…]} shape the corralai.dev cockpit replays")
	swarmFlag := fs.Int("swarm", 0, "max concurrent audit workers (0 = auto-size to this host's cores). The BUDGET clamp: independent role tasks run in parallel up to this bound, so a big audit swarms without melting the box")
	maxShardsFlag := fs.Int("max-shards", 0, "max mutant-generator seats fanned out across the file's functions (0 = "+fmt.Sprint(advpool.DefaultMaxShards)+"). Bounds PARALLELISM only — every function is probed regardless; --n-mutants is the PER-SHARD budget")
	shadowModelFlag := fs.String("shadow-model", "", "challenger model that attacks every region a SECOND time for a region-controlled head-to-head (default "+defaultLocalShadowModel+"; \"off\" disables). Recorded for comparison — NEVER gates the verdict")
	matrixFlag := fs.Bool("matrix", false, "opt into the tests×mutants matrix: after the primary pass, re-score EVERY dev test ALONE against the run's mutants — a per-test adequacy readout + a delete-candidate list, instead of one dev-suite-wide number. COSTLY: T tests × M mutants extra jail runs (T×M, on top of the primary pass), so leave off by default on a big suite")
	var bindDirFlag stringSlice
	fs.Var(&bindDirFlag, "bind-dir", "extra repo-relative dependency dir to mount read-only into the jail instead of copying it into the workspace (repeatable; node_modules/vendor/.venv/venv/.bundle are auto-detected) — --repo-dir mode only")
	noBindDepsFlag := fs.Bool("no-bind-deps", false, "copy dependency dirs into the jail workspace instead of bind-mounting them read-only (the pre-bind behavior; subject to the workspace size cap)")
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}

	if strings.TrimSpace(*codePath) == "" {
		fmt.Fprintln(stderr, "corral certify --local: --code is required")
		return 2
	}
	if strings.TrimSpace(*goal) == "" {
		fmt.Fprintln(stderr, "corral certify --local: --goal is required")
		return 2
	}
	// --bind-dir/--no-bind-deps only apply to --repo-dir mode: loadRepoFiles
	// (the only thing that reads them) is never called for a single-file
	// --code path. Without --repo-dir they'd be silently unread — refuse
	// loudly instead so an operator's misplaced flag doesn't look like a
	// no-op.
	if strings.TrimSpace(*repoDirFlag) == "" && (len(bindDirFlag) > 0 || *noBindDepsFlag) {
		fmt.Fprintln(stderr, "corral certify --local: --bind-dir/--no-bind-deps require --repo-dir (they configure how the cloned tree is seeded into the jail; a single --code file has no dependency dirs to bind or copy)")
		return 2
	}
	// --bind-dir asks to BIND a dir read-only; --no-bind-deps asks to COPY every
	// dep dir instead. Together they contradict: --no-bind-deps would silently
	// win and copy the explicitly-requested --bind-dir target. Refuse the
	// contradiction rather than do the opposite of one flag quietly.
	if len(bindDirFlag) > 0 && *noBindDepsFlag {
		fmt.Fprintln(stderr, "corral certify --local: --bind-dir and --no-bind-deps conflict (--bind-dir binds a dir read-only; --no-bind-deps copies all dep dirs — pick one)")
		return 2
	}

	// --record: collect the run into a replayable tape. The sink is the driver's
	// EventSink (pool reasoning beats) and is also fed the task lifecycle +
	// findings from the drive loop, so one ordered stream is the tape.
	var rec *recordSink
	if strings.TrimSpace(*recordFlag) != "" {
		rec = &recordSink{}
	}

	// The persistent build ledger + signing key `corral certify`/`corral certify
	// verify`/`corral certify pubkey` use, so a --local verdict is signed by the
	// user's own key and lands in the same offline-verifiable ledger. Opened
	// LAZILY, by auditOneFile, at exactly the point the pre-extraction code
	// opened it: every cheap validation (unknown --lang, absent toolchain,
	// collapsed decorrelation, missing provider key) must still fail fast
	// WITHOUT creating a DuckDB file. The handles are captured here because
	// --out below reads the signed record back out of the same store.
	var bs *buildstore.Store
	var key ed25519.PrivateKey
	defer func() {
		if bs != nil {
			bs.Close()
		}
	}()
	openStore := func() (*buildstore.Store, ed25519.PrivateKey, error) {
		st, err := buildstore.Open(localBuildDBPath())
		if err != nil {
			return nil, nil, fmt.Errorf("opening build ledger: %w", err)
		}
		k, err := buildstore.LoadOrCreateSigningKey(localCertifyKeyPath())
		if err != nil {
			st.Close()
			return nil, nil, fmt.Errorf("loading signing key: %w", err)
		}
		bs, key = st, k
		return st, k, nil
	}

	verdict, err := auditOneFile(context.Background(), localAuditInput{
		repoDir: strings.TrimSpace(*repoDirFlag), codePath: *codePath,
		testPath: strings.TrimSpace(*testPath), goal: strings.TrimSpace(*goal),
		lang: strings.TrimSpace(*langFlag),

		swarm: *swarmFlag, mutantTimeout: *testTimeout, timeout: *timeout,
		poll: *poll, nMutants: *nMutants, maxShards: *maxShardsFlag,

		writerModel: *writerModel, criticModel: *criticModel,
		mutantModel: *mutantModel, shadowModel: *shadowModelFlag,

		jail: *jailFlag, checkArgv: checkArgv,
		bindDirs: bindDirFlag, noBindDeps: *noBindDepsFlag,

		repo: strings.TrimSpace(*repoFlag), commit: strings.TrimSpace(*commitFlag),

		matrix: *matrixFlag, record: rec, bugCatchDB: localBugCatchDBPath(),
		openStore: openStore,
		stdout:    stdout, stderr: stderr,
	})
	if err != nil {
		fmt.Fprintf(stderr, "corral certify --local: %v\n", err)
		if isAuditUsageError(err) {
			return 2
		}
		return 1
	}

	// --out writes the signed record as a self-contained file the user can
	// re-verify offline. A --local record is signed by the user's OWN key but
	// never publicly witnessed (Witness is nil), so the verify hint carries
	// --allow-unanchored — an honest "signed by you, not third-party anchored"
	// claim, not a silent omission.
	// bs/key are non-nil whenever auditOneFile returned a verdict (it opens the
	// ledger before it drives the run); the nil guard is belt-and-braces so a
	// future unsigned --local mode degrades to "no record file" rather than a
	// panic.
	if out := strings.TrimSpace(*outFlag); out != "" && bs != nil {
		if err := writeLocalRecordFile(out, bs, key, verdict); err != nil {
			// Non-fatal: the verdict already printed and is signed in the ledger.
			fmt.Fprintf(stderr, "corral certify --local: writing --out %s: %v\n", out, err)
		} else {
			pubHex := hex.EncodeToString(key.Public().(ed25519.PublicKey))
			fmt.Fprintf(stdout, "\nwrote signed record to %s — re-verify offline:\n  corral certify verify %s --pubkey %s --allow-unanchored\n", out, out, pubHex)
		}
	}

	// --record: flush the replayable tape.
	if out := strings.TrimSpace(*recordFlag); out != "" && rec != nil {
		if err := rec.writeTape(out); err != nil {
			fmt.Fprintf(stderr, "corral certify --local: writing --record %s: %v\n", out, err)
		} else {
			fmt.Fprintf(stdout, "\nwrote a replayable tape (%d beats) to %s\n", len(rec.events), out)
		}
	}

	if verdict.Status == advpool.StatusCertified {
		return 0
	}
	return 3
}

// localAuditInput is everything ONE file's adversarial audit needs. It is the
// seam between a driver (the --local CLI, the --repo scan, and later a hosted
// service) and the audit itself: every caller resolves these inputs its own
// way, and they all share one implementation of the run.
//
// Zero values are the documented defaults throughout (see auditOneFile), so a
// caller only sets what it actually knows.
type localAuditInput struct {
	// The subject. repoDir empty = single-file mode (codePath/testPath are
	// filesystem paths); non-empty = repo-aware mode (they are repo-relative,
	// the whole tree is seeded into the jail). testPath empty = the language
	// plugin's sibling convention. lang empty = detect from codePath.
	repoDir  string
	codePath string
	testPath string
	goal     string
	lang     string

	// Budgets. Zero means the stock default for each.
	swarm int
	// mutantConcurrency scores this many mutants at once within ONE file.
	// Zero/1 is strictly sequential — today's behavior for every caller that
	// does not set it. It is only ever applied to a BWRAP-JAIL scorer, never to
	// the workspace runner (which mutates one checkout in place, unsynchronized
	// — see resolveMutantConcurrency, the single place that decision is made).
	mutantConcurrency int
	mutantTimeout     time.Duration
	timeout           time.Duration
	poll              time.Duration
	nMutants          int
	maxShards         int

	// Role models. Empty means this file's stock default.
	writerModel, criticModel, mutantModel, shadowModel string

	// Jail + workspace. jail empty = auto-detect this OS's backend (never
	// unsandboxed). checkArgv is the project's own test command, required in
	// repo-aware mode.
	checkArgv  []string
	jail       string
	bindDirs   []string
	noBindDeps bool

	// substrate selects where the audit runs: "" or substrateJail (today's
	// behavior) builds and mutates inside the bwrap jail; substrateWorkspace
	// mutates repoDir in place and skips jail construction entirely — the
	// caller (an ephemeral CI runner) IS the isolation boundary.
	substrate string

	// seed, when non-nil, is a prebuilt repo workspace SHARED across a scan's
	// jobs (see seedCache): jail prep is per-repo-and-language work, not
	// per-file work, and building it here would repeat the tree copy, the
	// vendoring and the tree walk twice for every audited file. Nil (the
	// `certify --local` position) means this run builds — and owns — its own.
	seed *repoSeed

	// iso, when non-nil, is a pre-resolved sandbox shared across a scan's
	// jobs. Resolving probes the backend, which is a scan-wide constant —
	// doing it per file re-probes bwrap once per audited file. Nil means
	// resolve one here, which is the `certify --local` path.
	iso sandbox.Isolator

	// The signed subject's identity. Empty falls back to git, else "local".
	repo, commit string

	// Effects, all optional. A nil openStore means the verdict is NOT signed
	// (the repo scan's H1a position — signing is H1c); an empty bugCatchDB
	// means no scorecard feed; a nil record means no tape.
	//
	// openStore is a FUNCTION, not an open handle, so the ledger is created
	// only once a run is actually going to happen: every usage error above it
	// fails fast without touching the user's DuckDB file.
	matrix     bool
	record     *recordSink
	bugCatchDB string
	openStore  func() (*buildstore.Store, ed25519.PrivateKey, error)

	// Where the run's human-readable progress goes. nil = io.Discard (the
	// repo scan's position: N concurrent files would interleave into mush).
	stdout, stderr io.Writer

	// cmdName prefixes WARNINGS written straight to stderr (errors are
	// returned, and the caller prefixes those itself). Empty means `corral
	// certify --local`, which is what makes the extraction invisible to that
	// command's existing output.
	cmdName string
}

// localAuditError distinguishes a USAGE error (bad flags/inputs — the CLI's
// exit 2) from an internal failure (exit 1), so the extracted audit can keep
// the exit-code contract `certify --local` already has without printing
// anything itself.
type localAuditError struct {
	usage bool
	msg   string
}

func (e localAuditError) Error() string { return e.msg }

func auditUsageErr(format string, a ...any) error {
	return localAuditError{usage: true, msg: fmt.Sprintf(format, a...)}
}

func auditErr(format string, a ...any) error {
	return localAuditError{msg: fmt.Sprintf(format, a...)}
}

// isAuditUsageError reports whether err is an auditOneFile usage error (exit
// 2 rather than exit 1). Anything else — including a nil-safe non-audit error
// — is an internal failure.
func isAuditUsageError(err error) bool {
	var ae localAuditError
	return errors.As(err, &ae) && ae.usage
}

// resolveAuditPlugin validates the subject and resolves the language plugin
// the jail will grade with — from in.lang, else the code file's extension.
// Fail closed on an unknown language or an absent toolchain: the gate never
// grades what it cannot run. Shared by auditOneFile and baselineRunnerFor so
// the baseline is measured with the SAME plugin the audit will grade with.
func resolveAuditPlugin(in localAuditInput) (lang.Plugin, error) {
	// Fail closed for non-CLI callers: the CLI checks these at flag-parse
	// time (before it opens a ledger), but a scan job or a hosted request
	// must never reach the jail with no subject or no goal — an audit with an
	// empty goal grades against nothing.
	if strings.TrimSpace(in.codePath) == "" {
		return nil, auditUsageErr("--code is required")
	}
	if strings.TrimSpace(in.goal) == "" {
		return nil, auditUsageErr("--goal is required")
	}
	var plug lang.Plugin
	if in.lang != "" {
		p, ok := lang.ByName(in.lang)
		if !ok {
			return nil, auditUsageErr("unknown --lang %q", in.lang)
		}
		plug = p
	} else {
		p, ok := lang.Detect(in.codePath)
		if !ok {
			return nil, auditUsageErr("unknown language for --code %s (pass --lang)", in.codePath)
		}
		plug = p
	}
	// in.checkArgv is the operator's own `-- <cmd>` when one was given (both
	// certify --local's own flag and certify --repo's per-job testCmd route
	// through this same field, see localExecutor.Execute) — it names exactly
	// how the suite runs and is stronger evidence than any stock guess this
	// plugin could make about the host's default toolchain. See
	// lang.Plugin.Preflight's doc comment.
	if err := plug.Preflight(in.checkArgv); err != nil {
		return nil, auditErr("%s toolchain unavailable — refusing to grade: %v", plug.Name(), err)
	}
	return plug, nil
}

// auditJailPrep is the jail-side of one file's audit: the resolved test path,
// the code + dev-test bytes, and the jail-backed scorer/validator/enumerator
// wiring. cleanup releases any Go-vendor staging dir and is always non-nil.
type auditJailPrep struct {
	testPath   string
	code       []byte
	devTest    []byte
	wiring     jailWiring
	importPath string
	cleanup    func()
}

// prepareAuditJail reads the subject + its dev test and builds the jail-backed
// wiring for them. It is the single place that turns a localAuditInput into a
// runnable jail, shared by the full audit (auditOneFile) and the
// baseline-only run (baselineRunnerFor) — so the baseline is measured in
// exactly the workspace the audit will score mutants in, never a lookalike.
func prepareAuditJail(in localAuditInput, plug lang.Plugin, timeout time.Duration, stdout io.Writer) (auditJailPrep, error) {
	var p auditJailPrep
	p.cleanup = func() {}

	// In repo-aware mode, the code/test paths are repo-relative (or absolute
	// under the repo); the file lives inside the cloned tree. Resolve to
	// filesystem paths for reading; the workspace keys are computed
	// repo-relative by buildJailWiring.
	repoDir := strings.TrimSpace(in.repoDir)
	fsPath := func(q string) string {
		if repoDir == "" || filepath.IsAbs(q) {
			return q
		}
		return filepath.Join(repoDir, q)
	}

	code, err := os.ReadFile(fsPath(in.codePath)) // #nosec G304 -- operator-supplied path to the file under review
	if err != nil {
		return p, auditUsageErr("reading --code %s: %v", in.codePath, err)
	}
	tp := strings.TrimSpace(in.testPath)
	if tp == "" {
		tp = plug.TestPaths(in.codePath)[0].Path
	}
	devTest, err := os.ReadFile(fsPath(tp)) // #nosec G304 -- operator-supplied (or sibling-derived) test path
	if err != nil {
		return p, auditUsageErr("reading test %s: %v (pass --test to override)", tp, err)
	}

	// Resolve the jail. NEVER fall back to unsandboxed — resolveLocalJail fails
	// closed if the requested/auto backend can't isolate on this host (and
	// refuses "none" outright), returning an actionable message.
	//
	// in.iso, when set, is the scan's already-resolved isolator (see
	// localExecutor): the backend is a scan-wide constant, so a repo scan
	// resolves it once and hands it to every job rather than re-probing bwrap
	// per file. `certify --local` passes nothing, so iso is nil here and this
	// resolves its own exactly as before.
	//
	// The workspace substrate is exempt, and MUST be: buildJailWiring's
	// workspace branch never touches in.iso (there is no jail to build — the
	// caller IS the isolation boundary), and on that path in.iso is always
	// nil because newLocalExecutor deliberately skips the scan-wide
	// resolution. Resolving one here anyway would fail closed on a
	// GitHub-hosted runner (no bubblewrap installed; Ubuntu 24.04 disables
	// unprivileged user namespaces) and turn EVERY audited file into
	// `could not audit: no working bwrap sandbox` — a COULD-NOT-GRADE red
	// build from jail work this substrate exists to skip. Where bwrap does
	// exist it would also re-probe the backend once per audited file,
	// defeating the resolve-once-per-scan invariant.
	iso := in.iso
	if iso == nil && in.substrate != substrateWorkspace {
		var err error
		iso, err = resolveLocalJail(in.jail)
		if err != nil {
			return p, auditErr("%v", err)
		}
	}

	wiring, err := buildJailWiring(jailWiringInput{
		iso: iso, timeout: timeout, testTimeout: in.mutantTimeout,
		codePath: in.codePath, testPath: tp, repoDir: repoDir, langName: plug.Name(), fsPath: fsPath,
		code: code, devTest: devTest, checkArgv: in.checkArgv,
		bindDirFlag: in.bindDirs, noBindDepsFlag: in.noBindDeps, stdout: stdout,
		seed: in.seed, substrate: in.substrate, mutantConcurrency: in.mutantConcurrency,
	})
	if err != nil {
		return p, auditUsageErr("%v", err)
	}

	// Derive the test-writer's real import fact NOW, while a real filesystem
	// (fsPath, scoped to repoDir when one was given) is in scope — the ONLY
	// place in the --local/--repo path that has one; the brain/MCP path
	// (internal/brain/advpool.go) has no checkout to consult and leaves
	// RunSpec.ImportPath unset, honestly. wiring.codeKey (not in.codePath) is
	// what actually lands in RunSpec.CodePath below, so it is what must be
	// derived from: in single-file mode it is already the bare base name
	// (buildJailWiring's else branch), so the derivation below never even
	// calls exists — the base name IS correct there, unchanged from before
	// this fix (see lang.Plugin.ImportPath's doc comment).
	importPath, _ := plug.ImportPath(wiring.codeKey, func(q string) bool {
		_, statErr := os.Stat(fsPath(q))
		return statErr == nil
	})

	return auditJailPrep{testPath: tp, code: code, devTest: devTest, wiring: wiring, importPath: importPath, cleanup: wiring.cleanup}, nil
}

// jailBaselineRunner runs a candidate's UNMUTATED suite in the jail, once per
// RunBaseline call, off the SAME scorer the audit grades mutants with:
// adequacy.Score with an empty mutant list executes exactly the healthy
// baseline and reports whether it passed (see adequacy.Report.CompliantPass).
// That is the honest definition of "the baseline ran" — no second jail
// invocation, no reimplementation of the workspace.
type jailBaselineRunner struct {
	ctx     context.Context
	scorer  advpool.JailScorer
	codeKey string
	code    string
	devTest string
	testCmd string
	// lastOutput is the runner's own output from the most recent FAILING
	// baseline (see adequacy.Report.BaselineOutput). Pointer-shared so a value
	// copy of this struct still reports it — reposcan.CheckBaselineStable
	// takes the runner as an interface and calls it repeatedly.
	lastOutput *string
}

func (b jailBaselineRunner) RunBaseline() (bool, error) {
	rep, err := b.scorer.ScoreReport(b.ctx, b.codeKey, b.code, b.devTest, nil, b.testCmd)
	if err != nil {
		return false, err
	}
	// Keep the runner's own words on a FAILING baseline. This is the string
	// that turns "baseline does not pass unmutated" — the least debuggable
	// outcome an audit can produce — into an actionable error.
	if !rep.CompliantPass && b.lastOutput != nil {
		*b.lastOutput = rep.BaselineOutput
	}
	return rep.CompliantPass, nil
}

// BaselineOutput reports the most recent failing baseline's output, if any.
// Satisfied by the value receiver above via the shared pointer.
func (b jailBaselineRunner) BaselineOutput() string {
	if b.lastOutput == nil {
		return ""
	}
	return *b.lastOutput
}

// baselineRunnerFor builds the baseline-only runner for one job — honesty
// invariant 2's input: a suite that does not agree with itself across repeated
// unmutated runs cannot produce a meaningful kill rate, so the scan reports it
// as ungradable rather than scoring a coin flip.
//
// It returns a cleanup the caller MUST call once it is done with the runner
// (it releases any Go-vendor staging dir the jail bind-mounts). The brief's
// signature is widened by that cleanup and an error: building the jail can
// fail (missing toolchain, unreadable subject), and swallowing either would
// mean reporting a stable baseline that was never actually run.
func baselineRunnerFor(ctx context.Context, in localAuditInput) (reposcan.BaselineRunner, func(), error) {
	noop := func() {}
	plug, err := resolveAuditPlugin(in)
	if err != nil {
		return nil, noop, err
	}
	timeout := in.timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	prep, err := prepareAuditJail(in, plug, timeout, io.Discard)
	if err != nil {
		prep.cleanup()
		return nil, noop, err
	}
	return jailBaselineRunner{
		ctx:     ctx,
		scorer:  prep.wiring.scorer,
		codeKey: prep.wiring.codeKey,
		code:    string(prep.code),
		devTest: string(prep.devTest),
		testCmd: strings.Join(in.checkArgv, " "),
		// Fresh per runner: two files' baseline failures must never be
		// attributed to each other.
		lastOutput: new(string),
	}, prep.cleanup, nil
}

// auditRoles is the resolved role→model assignment for one run, plus the
// role→backend router that will actually execute each seat.
type auditRoles struct {
	assign                 advpool.RoleAssignment
	shadow                 string
	writer, mutant, critic string
	chatterFor             func(role string) agentworker.Chatter
}

// resolveAuditRoles resolves the role models, enforces decorrelation, requires
// the provider credential, and builds the role→backend router. It is pure of
// jail/store I/O on purpose so a caller can run it ONCE as a preflight — a
// repo scan must discover a missing key before it fans out, not once per file
// after each has already run its baseline in the jail.
func resolveAuditRoles(in localAuditInput, stderr io.Writer) (auditRoles, error) {
	var r auditRoles
	if stderr == nil {
		stderr = io.Discard
	}
	// Resolve the models and enforce decorrelation BEFORE doing any I/O — an
	// operator override that collapses critic==writer must fail fast, not after
	// opening stores and a jail.
	writer := orDefault(in.writerModel, defaultLocalWriterModel)
	mutant := orDefault(in.mutantModel, defaultLocalMutantModel)
	critic := advpool.ResolveOptionalModel(in.criticModel, defaultLocalCriticModel)
	shadow := resolveShadowModel(in.shadowModel)
	assign := advpool.RoleAssignment{
		advpool.RoleMutantGenerator: mutant,
		advpool.RoleTestWriter:      writer,
		advpool.RoleTestCritic:      critic,
	}
	if shadow != "" {
		// Additive only: CheckDecorrelation compares critic vs writer alone, so
		// a shadow model equal to the critic's (the stock default) is expected
		// and must NOT error — it is a measurement seat, never a grading one.
		assign[advpool.RoleMutantGeneratorShadow] = shadow
	}
	if shadow != "" && shadow == mutant {
		// A head-to-head of a model against ITSELF is not a comparison — it
		// would be silently recorded as one, and read later as evidence about
		// two models. Not fatal (an operator may want the same-model variance
		// baseline on purpose), but never silent.
		fmt.Fprintf(stderr, "%s: warning: --shadow-model %q is the same model as the mutant-generator — the recorded head-to-head compares a model against itself, not two models\n", orDefault(in.cmdName, "corral certify --local"), shadow)
	}
	if err := advpool.CheckDecorrelation(assign); err != nil {
		return r, auditUsageErr("%v — pass distinct --writer-model / --critic-model", err)
	}

	// Require a provider key. The default role models are Claude, so unless the
	// operator selected a different MODEL_BACKEND we need ANTHROPIC_API_KEY. When
	// MODEL_BACKEND is unset we default it to anthropic so FromEnv() builds the
	// Claude backend the default models expect (rather than the ollama default).
	backendSel := strings.TrimSpace(os.Getenv("MODEL_BACKEND"))
	// When MODEL_BACKEND is unset, the backend this run needs is the one its
	// ASSIGNED MODELS imply — not Claude by assumption. Naming gemini-* models
	// for every seat has already said which vendor the run uses; requiring
	// MODEL_BACKEND as well is requiring the operator to say it twice, and the
	// error when they don't names the WRONG vendor ("export your Claude key"
	// on an all-Gemini run — found by the first real CI run of the Action).
	//
	// Only the unambiguous case is inferred: every seat on one cloud vendor.
	// A mixed assignment is the cross-vendor critic design and is left to
	// localChatterFor below; an explicit MODEL_BACKEND is an operator pointing
	// every seat at one endpoint on purpose and is never overruled.
	if backendSel == "" {
		if vendor, model := soleAssignedCloudModel(assign); vendor != "" && vendor != "anthropic" {
			// ForModel is the single source of truth for which credential a
			// model needs, and its error already names the right variable —
			// so this both validates the key and refuses with an actionable
			// message, before any jail or store is opened.
			if _, err := agentbackend.ForModel(model); err != nil {
				return r, auditUsageErr("%v", err)
			}
			backendSel = backendForVendor(vendor)
			if err := os.Setenv("MODEL_BACKEND", backendSel); err != nil {
				return r, auditErr("selecting %s backend: %v", backendSel, err)
			}
			// The challenger seat is excluded from the inference above (it
			// never gates a verdict), but it still runs and still needs a
			// credential — and it carries a CLAUDE default, so an otherwise
			// all-Gemini scan really does contain an Anthropic seat. Refuse
			// here naming THAT seat: the old message said "export your Claude
			// key" about a run with no Claude in it anywhere the operator
			// could see, which describes a configuration they never chose.
			if sm := strings.TrimSpace(assign[advpool.RoleMutantGeneratorShadow]); sm != "" && agentbackend.VendorOf(sm) != vendor {
				if _, err := agentbackend.ForModel(sm); err != nil {
					return r, auditUsageErr("the challenger seat (--shadow-model) is %q, a different vendor from this run's graded %s seats, and %v. It only records a comparison and never gates the verdict — pass --shadow-model off to drop it, or give it a %s model", sm, vendor, err, vendor)
				}
			}
		}
	}
	if onDefaultClaudePath() {
		if agentbackend.Secret("ANTHROPIC_API_KEY") == "" {
			return r, auditUsageErr("no $ANTHROPIC_API_KEY set — export your Claude key, or select another provider with MODEL_BACKEND + its key")
		}
		if backendSel == "" {
			if err := os.Setenv("MODEL_BACKEND", "anthropic"); err != nil {
				return r, auditErr("selecting anthropic backend: %v", err)
			}
		}
	}

	// Resolve the role→backend router NOW, before opening the jail or any
	// store: a cross-vendor critic (e.g. --critic-model gemini-3.5-flash on
	// the default Claude path) needs its own vendor's key, and a missing key
	// must refuse the run here — fail closed at the top, not mid-run after
	// jails/stores/mutants are already in flight.
	chatterFor, err := localChatterFor(assign)
	if err != nil {
		return r, auditUsageErr("%v", err)
	}

	return auditRoles{assign: assign, shadow: shadow, writer: writer, mutant: mutant, critic: critic, chatterFor: chatterFor}, nil
}

// auditOneFile runs ONE file's complete adversarial-pool audit in-process and
// returns the converged verdict. This is the whole of what `corral certify
// --local` does between parsing its flags and writing its --out/--record
// files, extracted so the repo scan (and later a hosted worker) drives the
// SAME implementation rather than a second copy that would drift.
//
// It prints the run's human-readable progress and the rendered verdict to
// in.stdout, but never prints its errors: those come back typed (see
// localAuditError) so each caller can prefix and exit as its own mode
// requires.
func auditOneFile(ctx context.Context, in localAuditInput) (advpool.Verdict, error) {
	stdout, stderr := in.stdout, in.stderr
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	var zero advpool.Verdict

	// Budget defaults — the same values `certify --local`'s flags carry, kept
	// here so every caller of the seam (scan job, hosted worker) inherits them
	// rather than each re-deciding what "no budget given" means.
	timeout := in.timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	poll := in.poll
	if poll <= 0 {
		poll = 2 * time.Second
	}

	plug, err := resolveAuditPlugin(in)
	if err != nil {
		return zero, err
	}

	// Resolve the models, enforce decorrelation, and prove the provider key is
	// present BEFORE doing any I/O — an operator override that collapses
	// critic==writer, or a missing key, must fail fast, not after opening
	// stores and a jail.
	roles, err := resolveAuditRoles(in, stderr)
	if err != nil {
		return zero, err
	}
	assign, shadow := roles.assign, roles.shadow
	writer, mutant, critic := roles.writer, roles.mutant, roles.critic
	chatterFor := roles.chatterFor

	// Read the subject + its dev test and build the jail-backed wiring for
	// them. Single-file mode keys by BASENAME (a flat scaffold; the adequacy
	// jail refuses absolute/`..` keys); repo-aware mode seeds the jail with
	// the whole cloned tree and keys the file under audit by its
	// REPO-RELATIVE path, so a mutant overwrites the real file in context and
	// the project's own tests (which import the package) resolve.
	prep, err := prepareAuditJail(in, plug, timeout, stdout)
	if err != nil {
		prep.cleanup()
		return zero, err
	}
	// Release any Go-vendor staging dir once the run completes (the jail
	// bind-mounts vendor/ from it, so it must outlive scoring).
	defer prep.cleanup()
	tp, code, devTest := prep.testPath, prep.code, prep.devTest
	scorer, validator, jailEnum := prep.wiring.scorer, prep.wiring.validator, prep.wiring.jailEnum
	codeKey, devTestKey := prep.wiring.codeKey, prep.wiring.devTestKey

	// Open the ephemeral queue — one run, one claimant, discarded on return.
	qdir, err := os.MkdirTemp("", "corral-local-queue-")
	if err != nil {
		return zero, auditErr("temp queue dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(qdir) }()
	q, err := queue.Open(filepath.Join(qdir, "queue.sqlite3"))
	if err != nil {
		return zero, auditErr("opening queue: %v", err)
	}
	defer func() { _ = q.Close() }()

	// Build the pure driver over the REAL jail-backed scorer/validator and the
	// REAL certify-chain signer.
	d, err := advpool.NewDriver(q, scorer, validator, assign, localCertifyThreshold)
	if err != nil {
		return zero, auditErr("%v", err)
	}
	// A nil openStore means UNSIGNED on purpose: the repo scan (H1a) produces
	// a report, not a sealed statement — signing/anchoring is H1c. Never
	// half-sign: no store, no Signer at all.
	if in.openStore != nil {
		st, k, serr := in.openStore()
		if serr != nil {
			return zero, auditErr("%v", serr)
		}
		d.Signer = advpool.CertSigner{Key: k, Store: st, Witness: nil}
	}
	d.Enumerator = jailEnum

	// --record: the tape sink is the driver's EventSink (pool reasoning beats)
	// and is also fed the task lifecycle + findings from the drive loop below,
	// so one ordered stream is the tape. nil = no tape (rec.add is nil-safe).
	rec := in.record
	if rec != nil {
		d.Events = rec
	}
	actorFor := func(role string) string { return recordActor(role, assign[role]) }
	// The wall-clock backstop: a run that hasn't converged by --timeout is signed
	// as a needs-review verdict and returned, so the CLI always gets an answer.
	//
	// When a shadow model is configured, RunDeadline itself must carry the SAME
	// allowance the outer context bound gets below (see outerBound) — NOT just
	// the scoring-side credit runShadowPass already gives back (see
	// advpool.ShadowTimeBudget). The challenger's mutant-GENERATION LLM calls
	// happen in runReadyTasks, entirely outside the driver, so nothing credits
	// that wall-clock to the deadline the way runShadowPass credits shadow
	// SCORING. With the swarm auto-sized to localSwarmAutoCap and shadow
	// roughly doubling generator calls, that uncredited generation time can by
	// itself push a run's elapsed wall-clock past RunDeadline before it
	// converges — and Tick's timeout path (see timeoutVerdict) then forces
	// StatusNeedsReview. That is shadow work changing the verdict's Status,
	// the exact breach the shadow budget exists to prevent, just via the
	// generation channel instead of the scoring one. Widening RunDeadline
	// itself closes it: see resolveRunDeadline.
	d.RunDeadline = resolveRunDeadline(timeout, shadow)

	// Resolve repo/commit for the signed subject (best-effort git, else "local").
	repo := strings.TrimSpace(in.repo)
	if repo == "" {
		if v, gerr := (realRunner{}).GitOutput("config", "--get", "remote.origin.url"); gerr == nil && v != "" {
			repo = v
		} else {
			repo = "local"
		}
	}
	commit := strings.TrimSpace(in.commit)
	if commit == "" {
		if v, gerr := (realRunner{}).GitOutput("rev-parse", "HEAD"); gerr == nil && v != "" {
			commit = v
		} else {
			commit = "local"
		}
	}

	// The bug-catching scorecard feed — the ONLY thing that makes the shadow
	// head-to-head durable: BugCatch was previously wired solely in the brain,
	// and the brain never sets a ShadowModel, so on the only path where a
	// challenger actually runs, every comparison row was computed and
	// discarded. Opening it is best-effort on purpose: metrics are NOT the
	// gate, so a store that will not open must warn and let the audit run,
	// never abort it.
	// An empty bugCatchDB means the caller wants no scorecard feed at all (the
	// repo scan's position: N concurrent audits must not contend on one
	// single-process DuckDB file).
	var shadowRowsRecorded *int64
	if strings.TrimSpace(in.bugCatchDB) != "" {
		var closeBugCatch func()
		closeBugCatch, _, shadowRowsRecorded = wireLocalBugCatch(d, in.bugCatchDB, repo, commit, stderr)
		defer closeBugCatch()
	}

	n := in.nMutants
	if n <= 0 {
		n = 5
	}
	rs := advpool.RunSpec{
		Repo: repo, Commit: commit, Goal: strings.TrimSpace(in.goal),
		CodePath: codeKey, Code: string(code),
		DevTestPath: devTestKey, DevTestCode: string(devTest),
		TestCmd:     strings.Join(in.checkArgv, " "),
		NMutants:    n,
		Lang:        plug.Name(),
		MaxShards:   resolveMaxShards(in.maxShards),
		ShadowModel: shadow,
		Matrix:      in.matrix,
		ImportPath:  prep.importPath,
	}

	// Signatures are best-effort (mirrors the brain's StartRun): a failure just
	// degrades the prompt to no signatures, never refuses the run.
	sigs, serr := repoindex.ExtractSignatures(rs.Code, rs.Lang)
	if serr != nil {
		sigs = nil
	}

	if err := d.StartRun(localMissionID, rs, sigs); err != nil {
		return zero, auditErr("starting run: %v", err)
	}

	fmt.Fprintf(stdout, "auditing %s against its own tests — mutant-generator=%s test-writer=%s test-critic=%s\n",
		in.codePath, mutant, writer, critic)

	// The outer context bound is slightly beyond the driver's own RunDeadline
	// (which already carries the shadow allowance — see resolveRunDeadline) so
	// the driver gets the chance to emit its signed timeout verdict before ctx
	// cancels the loop. Layered on the CALLER's ctx, so a scan-wide cancel
	// still reaches every in-flight file.
	outerBound := resolveRunDeadline(timeout, shadow) + 30*time.Second
	ctx, cancel := context.WithTimeout(ctx, outerBound)
	defer cancel()

	// Size the concurrent audit swarm and say so out loud — the "won't bankrupt
	// me / won't melt the box" bound made visible. Independent role tasks run in
	// parallel up to this bound; it's clamped to the host's cores (auto) or the
	// operator's --swarm budget.
	swarm := resolveSwarm(in.swarm)
	d.MatrixWorkers = swarm
	if in.swarm > 0 {
		fmt.Fprintf(stdout, "swarm: %d concurrent workers (--swarm budget)\n", swarm)
	} else {
		fmt.Fprintf(stdout, "swarm: %d concurrent workers (auto-sized to %d cores)\n", swarm, runtime.NumCPU())
	}
	shards := advpool.ShardSymbols(sigs, rs.MaxShards)
	if len(shards) > 0 {
		packed := 0
		for _, sh := range shards {
			packed += len(sh.Symbols)
		}
		fmt.Fprintf(stdout, "regions: %d generator seats over %d functions\n", len(shards), packed)
	} else if len(sigs) == 0 {
		fmt.Fprintf(stdout, "regions: 1 generator seat (whole file — no symbol surface extracted)\n")
	} else {
		fmt.Fprintf(stdout, "regions: 1 generator seat (whole file — too few functions to split)\n")
	}
	// len(shards) is the shadow seat count too — one challenger per PRIMARY
	// region, never a separate partition (see RoleMutantGeneratorShadow).
	// BuildDAG only fans the challenger out alongside a SHARDED run, so an
	// unsharded file gets no shadow seat at all: say nothing rather than
	// announce "0 challenger seats", which is noise about work that was never
	// going to happen. The claim that anything was actually RECORDED cannot
	// be made yet — see the print after driveLocalRun below.
	if shadow != "" && len(shards) > 0 {
		fmt.Fprintf(stdout, "shadow: %d challenger seat(s) (%s) — a head-to-head measurement, never gating\n", len(shards), shadow)
	}

	verdict, err := driveLocalRun(ctx, d, q, localMissionID, chatterFor, poll, time.Sleep, stdout, rec, actorFor, swarm)
	if err != nil {
		return zero, auditErr("%v", err)
	}

	renderAdvVerdict(stdout, in.codePath, advVerdictFromPool(*verdict))

	// --matrix: print the per-test adequacy summary + delete-candidate list.
	// st.Matrix is nil unless --matrix was set AND the phase actually ran
	// (see advpool.RunState.Matrix's doc comment) — the summary is entirely
	// opt-in and silent otherwise.
	if st, ok := d.RunStatus(localMissionID); ok && st.Matrix != nil {
		renderMatrixSummary(stdout, *st.Matrix)
		rec.add("pool_matrix", "corral-advpool", in.codePath, matrixTapeDetail(*st.Matrix))
	}

	// The "recorded to the scorecard" claim can only be made in PAST TENSE
	// once it is actually true: printing it unconditionally whenever shadow is
	// enabled was false in three cases — the metrics store failed to open (and
	// that warning goes to stderr BEFORE this line ran, so stdout alone read
	// as an unqualified false claim), the run hit its deadline (the timeout
	// path signs a verdict but never calls the metrics sink), or every shadow
	// seat ended unmeasured (a provider failure, or the shadow budget skip —
	// NOT a parse failure, which is deliberately recorded as measured=true,
	// dropped=true and DOES emit a row). shadowRowsRecorded is nil (metrics store never
	// opened) or 0 (opened, but nothing landed) in exactly those cases, so
	// this only fires once rows are actually sitting in the store.
	if shadow != "" && len(shards) > 0 && shadowRowsRecorded != nil {
		if n := atomic.LoadInt64(shadowRowsRecorded); n > 0 {
			fmt.Fprintf(stdout, "shadow: recorded %d row(s) to the scorecard\n", n)
		}
	}

	// Hand the pool's authored test back: when it killed a survivor the dev suite
	// missed, print it so the dev can adopt it.
	if st, ok := d.RunStatus(localMissionID); ok && strings.TrimSpace(st.AuthoredTest) != "" {
		fmt.Fprintf(stdout, "\nthe herd authored a test that catches a gap your suite missed — add it to %s:\n\n", tp)
		fmt.Fprintln(stdout, strings.TrimRight(st.AuthoredTest, "\n"))
	}

	return *verdict, nil
}

// jailWiringInput bundles the inputs buildJailWiring needs. It is a params
// struct only to keep the signature readable — every field is a plain input,
// none optional.
type jailWiringInput struct {
	iso            sandbox.Isolator
	timeout        time.Duration
	testTimeout    time.Duration
	codePath       string
	testPath       string
	repoDir        string
	langName       string // resolved language plugin name — drives Go dep vendoring
	fsPath         func(string) string
	code           []byte
	devTest        []byte
	checkArgv      []string
	bindDirFlag    []string
	noBindDepsFlag bool
	stdout         io.Writer
	seed           *repoSeed // non-nil: use this prebuilt, SHARED seed instead of building one
	substrate      string    // "" or substrateJail = the bwrap jail (today's behavior); substrateWorkspace = mutate repoDir in place, no jail
	// mutantConcurrency is applied ONLY to the bwrap-jail scorers below, never
	// to the workspace runner. See localAuditInput.mutantConcurrency.
	mutantConcurrency int
}

// Substrate names for jailWiringInput.substrate / localAuditInput.substrate,
// assigned (not re-spelled) from reposcan's constants so the value that
// selects which substrate RUNS and the value that later gets recorded as
// what ran (reposcan.KeyInputs.Substrate) can never drift apart. "" is
// equivalent to substrateJail — the zero value must be today's shipped
// behavior so every existing caller (which never sets this field) is
// unaffected.
const (
	substrateJail      = reposcan.SubstrateJail
	substrateWorkspace = reposcan.SubstrateWorkspace
)

// workspaceFromSeed returns a private copy of the seed's workspace text with
// overlay applied. The seed is SHARED across concurrent jobs and must never be
// mutated; this copies string headers, not bytes, so it is cheap even for a
// large tree.
func workspaceFromSeed(seed repoSeed, overlay map[string]string) map[string]string {
	w := make(map[string]string, len(seed.files)+len(overlay))
	for k, v := range seed.files {
		w[k] = v
	}
	for k, v := range overlay {
		w[k] = v
	}
	return w
}

// jailWiring is what buildJailWiring resolves: the jail-backed
// scorer/validator/enumerator, the workspace keys for the code + dev-test files,
// and the read-only dependency binds (empty in single-file mode).
type jailWiring struct {
	scorer     advpool.JailScorer
	validator  advpool.JailValidator
	jailEnum   advpool.JailEnumerator
	codeKey    string
	devTestKey string
	depBinds   []adequacy.DepBind
	// cleanup releases any temp staging dir created for Go dep vendoring
	// (see ensureGoVendored). Always non-nil; a no-op when nothing was staged.
	// The caller MUST defer it after the run completes — the jail bind-mounts
	// vendor/ from the staged copy, so it has to outlive scoring.
	cleanup func()
}

// buildJailWiring resolves the jail-backed scorer/validator/enumerator and the
// workspace keys for a --local run, branching on whether --repo-dir was set.
// Single-file mode keys by BASENAME (a flat scaffold; the adequacy jail
// refuses absolute/`..` keys, so an absolute --code must be normalized here).
// --repo-dir mode seeds the jail with the whole cloned tree and keys the file
// under audit by its REPO-RELATIVE path, so a mutant overwrites the real file
// in context and the project's own tests (which import the package) resolve.
//
// On error, the returned err is already a fully-formatted "corral certify
// --local: ..." message ready to print as-is; the caller always exits 2 for
// a non-nil error from this function (every failure path here is a usage/
// input error, never an internal one).
func buildJailWiring(in jailWiringInput) (w jailWiring, err error) {
	w.cleanup = func() {}
	// If wiring fails AFTER a vendor staging dir was created, release it here —
	// the caller only defers cleanup on the success path.
	defer func() {
		if err != nil && w.cleanup != nil {
			w.cleanup()
			w.cleanup = func() {}
		}
	}()
	if in.substrate == substrateWorkspace {
		// The runner IS the isolation boundary — an ephemeral VM with the repo
		// checked out and the toolchain installed. Nothing to seed, nothing to
		// vendor, nothing to bind: skip the jail entirely.
		if in.repoDir == "" {
			return w, fmt.Errorf("--substrate workspace needs --repo-dir: it mutates a real checkout")
		}
		if len(in.checkArgv) == 0 {
			return w, fmt.Errorf("--repo-dir requires the project's own test command after `--`, e.g. `-- python3 -m pytest tests/test_recipes.py`")
		}
		// WithPerRunEnv wires the resolved language plugin's own
		// per-run environment (e.g. python.go's fresh __pycache__
		// redirect) into EVERY baseline/canary/mutant/authored-test
		// invocation this runner makes — see WithPerRunEnv's and
		// lang.Plugin.WorkspaceRunEnv's doc comments for why this
		// substrate specifically needs it (it mutates the SAME real
		// checkout across every one of those calls, unlike the jail).
		// in.langName was set from the already-resolved plugin's own
		// Name() (see buildJailWiring's caller), so ByName here can only
		// fail if the registry itself changed between resolution and
		// this call — never in practice — and degrades to no extra env
		// rather than failing the whole audit over it.
		var runnerOpts []adequacy.WorkspaceOption
		if plug, ok := lang.ByName(in.langName); ok {
			runnerOpts = append(runnerOpts, adequacy.WithPerRunEnv(plug.WorkspaceRunEnv))
		}
		runner := adequacy.NewWorkspaceRunner(in.repoDir, in.timeout, runnerOpts...)
		if verr := runner.Verify(); verr != nil {
			return w, verr
		}
		w.cleanup = func() {}

		// Same key computation as the jail's repo-aware branch: the mutant
		// overlay must target the repo-relative path adequacy.Score writes.
		ck, rerr := filepath.Rel(in.repoDir, in.fsPath(in.codePath))
		if rerr != nil || strings.HasPrefix(ck, "..") {
			return w, fmt.Errorf("--code %s is not inside --repo-dir %s", in.codePath, in.repoDir)
		}
		dk, rerr := filepath.Rel(in.repoDir, in.fsPath(in.testPath))
		if rerr != nil || strings.HasPrefix(dk, "..") {
			return w, fmt.Errorf("--test %s is not inside --repo-dir %s", in.testPath, in.repoDir)
		}
		w.codeKey, w.devTestKey = filepath.ToSlash(ck), filepath.ToSlash(dk)

		// EMPTY but NON-NIL. scoreWorkspace (internal/advpool/gate.go:143)
		// branches on BaseFiles != nil: non-nil takes the repo-aware path and
		// uses the run's own TestCmd; nil rebuilds a synthetic single-file
		// scaffold. Empty-non-nil therefore yields a base of {}, so only the
		// mutant is overlaid — the rest of the repo is already on disk and
		// must NOT be rewritten over itself.
		base := map[string]string{}
		w.scorer = advpool.JailScorer{Jail: runner, BaseFiles: base, MutantTimeout: in.testTimeout, DevTestPath: w.devTestKey}
		w.validator = advpool.JailValidator{Jail: runner, BaseFiles: base, DevTestPath: w.devTestKey}
		w.jailEnum = advpool.JailEnumerator{Jail: runner, BaseFiles: base}
		// w.depBinds stays nil: there is nothing to bind read-only when the
		// real tree is already present.
		return w, nil
	}
	if in.repoDir != "" {
		if len(in.checkArgv) == 0 {
			return w, fmt.Errorf("--repo-dir requires the project's own test command after `--`, e.g. `-- python3 -m pytest tests/test_recipes.py`")
		}
		var seed repoSeed
		if in.seed != nil {
			// Shared across a scan's jobs: use as-is, and do NOT take ownership
			// of its cleanup — the seedCache owns that for the whole scan.
			seed = *in.seed
		} else {
			// Provision external Go deps for the offline jail (no-op for other
			// langs, non-modules, or already-vendored repos). Seed from the
			// returned dir.
			var serr error
			seed, serr = buildRepoSeed("corral certify --local", in.repoDir, in.langName, in.iso.Name(), in.bindDirFlag, in.noBindDepsFlag, in.stdout)
			if serr != nil {
				return w, serr
			}
			w.cleanup = seed.cleanup
		}
		depBinds := seed.binds
		w.depBinds = depBinds
		ck, rerr := filepath.Rel(in.repoDir, in.fsPath(in.codePath))
		if rerr != nil || strings.HasPrefix(ck, "..") {
			return w, fmt.Errorf("--code %s is not inside --repo-dir %s", in.codePath, in.repoDir)
		}
		dk, rerr := filepath.Rel(in.repoDir, in.fsPath(in.testPath))
		if rerr != nil || strings.HasPrefix(dk, "..") {
			return w, fmt.Errorf("--test %s is not inside --repo-dir %s", in.testPath, in.repoDir)
		}
		w.codeKey, w.devTestKey = filepath.ToSlash(ck), filepath.ToSlash(dk)
		// The just-read code/test are authoritative in the map (identical to the
		// on-disk copy, but explicit so a mutant overlay targets the right key).
		// A private copy: the seed may be SHARED across concurrent jobs and must
		// never be mutated in place.
		repoFiles := workspaceFromSeed(seed, map[string]string{
			w.codeKey:    string(in.code),
			w.devTestKey: string(in.devTest),
		})
		jail := adequacy.NewJail(in.iso, in.timeout, adequacy.WithReadOnlyBinds(depBinds))
		// enumerator backs the tests×mutants matrix's test-listing step
		// (--matrix). Wired unconditionally off the SAME backend/timeout/binds
		// as jail (bwrapJail satisfies both interfaces) — a nil
		// advpool.Driver.Enumerator makes tickMatrix always skip regardless of
		// RunSpec.Matrix, so wiring it here costs nothing when --matrix is off
		// (the flag is the real gate).
		enumerator := adequacy.NewEnumerator(in.iso, in.timeout, adequacy.WithReadOnlyBinds(depBinds))
		w.scorer = advpool.JailScorer{Jail: jail, BaseFiles: repoFiles, MutantTimeout: in.testTimeout, DevTestPath: w.devTestKey, Concurrency: in.mutantConcurrency}
		w.validator = advpool.JailValidator{Jail: jail, BaseFiles: repoFiles, DevTestPath: w.devTestKey}
		w.jailEnum = advpool.JailEnumerator{Jail: enumerator, BaseFiles: repoFiles}
		if len(depBinds) > 0 {
			names := make([]string, 0, len(depBinds))
			for _, b := range depBinds {
				names = append(names, b.Rel)
			}
			fmt.Fprintf(in.stdout, "deps: bound %d dir(s) read-only (%s) — not copied into the jail seed\n", len(depBinds), strings.Join(names, ", "))
		}
	} else {
		w.codeKey = filepath.Base(in.codePath)
		w.devTestKey = filepath.Base(in.testPath)
		jail := adequacy.NewJail(in.iso, in.timeout)
		enumerator := adequacy.NewEnumerator(in.iso, in.timeout)
		w.scorer = advpool.JailScorer{Jail: jail, MutantTimeout: in.testTimeout, Concurrency: in.mutantConcurrency}
		w.validator = advpool.JailValidator{Jail: jail}
		w.jailEnum = advpool.JailEnumerator{Jail: enumerator}
	}
	return w, nil
}

// localSwarmAutoCap keeps a default (no --swarm) run polite: even on a
// many-core box, auto-sizing won't spawn an absurd worker count for what is,
// today, a handful of independent role tasks. The cap lifts naturally as the
// fan-out slices land (per-region generators, the tests×mutants matrix).
const localSwarmAutoCap = 8

// resolveSwarm sizes the concurrent audit swarm — the first, honest cut of the
// resource-aware optimizer. The operator's --swarm budget wins if set; else it
// auto-clamps to this host's cores (minus one for the driver/OS, capped). This
// is the cost/resource bound the swarm answers "no, it won't bankrupt or melt
// you" with; RAM and yield-weighted allocation land in a later slice.
func resolveSwarm(flag int) int {
	if flag > 0 {
		return flag
	}
	n := runtime.NumCPU() - 1
	if n < 1 {
		n = 1
	}
	if n > localSwarmAutoCap {
		n = localSwarmAutoCap
	}
	return n
}

// resolveMaxShards resolves the generator fan-out width: the operator's
// --max-shards budget, else the stock default.
func resolveMaxShards(flag int) int {
	if flag > 0 {
		return flag
	}
	return advpool.DefaultMaxShards
}

// resolveRunDeadline sizes the driver's own wall-clock backstop
// (advpool.Driver.RunDeadline). When a shadow model is configured it widens
// the deadline by advpool.ShadowTimeBudget(timeout) — the SAME allowance the
// outer context bound (outerBound, below) gives itself — so that shadow work
// can never change the run's Status by pushing it past RunDeadline into a
// timeout needs-review verdict (see timeoutVerdict).
//
// This closes a gap the existing scoring-side credit does not: runShadowPass
// already credits back the wall-clock it spends SCORING shadow mutants (see
// advpool.ShadowTimeBudget's doc comment), advancing run.startedAt so scoring
// alone cannot exhaust the deadline. But the challenger's mutant-GENERATION
// LLM calls happen in runReadyTasks, entirely outside the driver — nothing
// credits that time back the way runShadowPass does for scoring. With shadow
// on (the default) roughly doubling generator calls, that uncredited
// generation wall-clock can by itself carry a run past RunDeadline before it
// converges. Widening the deadline itself, rather than trying to credit
// generation time after the fact from inside cmd/corral (which has no
// equivalent hook to the driver's tick loop), gives generation the same
// headroom scoring already has.
func resolveRunDeadline(timeout time.Duration, shadow string) time.Duration {
	return advpool.ResolveRunDeadline(timeout, shadow)
}

// writeLocalRecordFile exports the signed --local verdict as a self-contained
// record file in the SAME shape `corral certify verify` reads (certRecord) and
// the daemon's `certify --out` writes, so a --local record round-trips through
// the identical offline verifier. It reconstructs the file from the signed
// record persisted in the local ledger (the CLI never sees the DSSE envelope
// itself — CertSigner signs and stores it inside the driver): buildstore.Get
// layers steps/signature/rekor/anchored onto the statement map, and the ledger
// head comes from the verdict. Statement is cosmetic (verify checks the
// envelope's own embedded statement), so the extra layered keys are stripped
// only for a clean human-readable file.
func writeLocalRecordFile(path string, bs *buildstore.Store, key ed25519.PrivateKey, v advpool.Verdict) error {
	if v.RecordID <= 0 {
		return fmt.Errorf("no signed record was produced (signing skipped or failed)")
	}
	m, ok, err := bs.Get(v.RecordID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("record %d not found in the local ledger", v.RecordID)
	}
	sig, _ := m["signature"].(string)
	rekor, _ := m["rekor"].(string)
	anchored, _ := m["anchored"].(bool)
	// steps comes back as an untyped decoded value; round-trip through JSON to
	// land it as the []map[string]any certRecord.Steps expects.
	var steps []map[string]any
	if sb, e := json.Marshal(m["steps"]); e == nil {
		_ = json.Unmarshal(sb, &steps)
	}
	stmt := make(map[string]any, len(m))
	for k, val := range m {
		switch k {
		case "steps", "signature", "rekor", "anchored",
			"commit_message", "commit_author", "commit_date", "commit_signature", "pass":
			// layered-on columns, not part of the human-readable statement
		default:
			stmt[k] = val
		}
	}
	rec := certRecord{
		Statement: stmt,
		Signature: sig,
		Steps:     steps,
		Head:      v.RecordHead,
		PublicKey: hex.EncodeToString(key.Public().(ed25519.PublicKey)),
		Rekor:     rekor,
		Anchored:  anchored,
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600) // #nosec G306 -- a signed record is public artifact; 0600 is conservative
}

// advVerdictFromPool converts a concrete advpool.Verdict to the advVerdict wire
// shape renderAdvVerdict prints (the same type the --adversarial path decodes
// off the brain), so both certify modes render identically.
func advVerdictFromPool(v advpool.Verdict) advVerdict {
	out := advVerdict{
		Repo: v.Repo, Commit: v.Commit, Lang: v.Lang,
		DevKillRate: v.DevKillRate, MutantsTotal: v.MutantsTotal,
		Survivors: v.Survivors, ProvenMissed: v.ProvenMissed,
		ModelsByRole: v.ModelsByRole, Status: v.Status,
		RecordID: v.RecordID, RecordHead: v.RecordHead,
		RegionsTotal: v.RegionsTotal, RegionsProbed: v.RegionsProbed,
		DroppedRegions:   v.DroppedRegions,
		TestWriterFailed: v.TestWriterFailed,
		PoolTestUnsound:  v.PoolTestUnsound,
		BaselineFailed:   v.BaselineFailed,
		BaselineOutput:   v.BaselineOutput,
		SuiteIgnoresFile: v.SuiteIgnoresFile,
		TimedOut:         v.TimedOut,
		DevScored:        v.DevScored,
	}
	for _, f := range v.VacuousFindings {
		out.VacuousFindings = append(out.VacuousFindings, advFinding{
			Type: f.Type, Severity: f.Severity, Target: f.Target,
			Evidence: f.Evidence, ReporterModel: f.ReporterModel,
		})
	}
	return out
}

// orDefault returns v trimmed, or def when v is empty.
func orDefault(v, def string) string {
	if s := strings.TrimSpace(v); s != "" {
		return s
	}
	return def
}

// resolveShadowModel resolves the challenger model: the operator's
// --shadow-model, "off" to disable, else the stock default. The disable words
// are matched case-INSENSITIVELY — `--shadow-model OFF` plainly means off, and
// silently treating it as a model name would send every challenger seat to a
// provider that has no such model.
func resolveShadowModel(flag string) string {
	return advpool.ResolveShadowModel(flag)
}

// localBuildDBPath mirrors cmd/corral/main.go's CORRALAI_BUILD_DB resolution so
// a --local verdict lands in the SAME signed build-record ledger `corral
// certify` writes to and `corral certify verify` reads from.
func localBuildDBPath() string {
	if p := strings.TrimSpace(os.Getenv("CORRALAI_BUILD_DB")); p != "" {
		return p
	}
	home := ""
	if u, err := os.UserHomeDir(); err == nil {
		home = u
	} else if usr, err := user.Current(); err == nil {
		home = usr.HomeDir
	}
	return filepath.Join(home, ".claude", "corralai_build.duckdb")
}

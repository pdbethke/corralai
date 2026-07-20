// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/agentworker"
	"github.com/pdbethke/corralai/internal/bugcatch"
	"github.com/pdbethke/corralai/internal/buildstore"
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/repoindex"
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
	criticModel := fs.String("critic-model", "", "model for the test-critic role (default "+defaultLocalCriticModel+")")
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

	// Resolve the language plugin the jail will grade with — from --lang, else
	// the code file's extension. Fail closed on an unknown language: the gate
	// never grades what it cannot run.
	var plug lang.Plugin
	if strings.TrimSpace(*langFlag) != "" {
		p, ok := lang.ByName(strings.TrimSpace(*langFlag))
		if !ok {
			fmt.Fprintf(stderr, "corral certify --local: unknown --lang %q\n", *langFlag)
			return 2
		}
		plug = p
	} else {
		p, ok := lang.Detect(*codePath)
		if !ok {
			fmt.Fprintf(stderr, "corral certify --local: unknown language for --code %s (pass --lang)\n", *codePath)
			return 2
		}
		plug = p
	}
	if err := plug.Preflight(); err != nil {
		fmt.Fprintf(stderr, "corral certify --local: %s toolchain unavailable — refusing to grade: %v\n", plug.Name(), err)
		return 1
	}

	// Resolve the models and enforce decorrelation BEFORE doing any I/O — an
	// operator override that collapses critic==writer must fail fast, not after
	// opening stores and a jail.
	writer := orDefault(*writerModel, defaultLocalWriterModel)
	mutant := orDefault(*mutantModel, defaultLocalMutantModel)
	critic := orDefault(*criticModel, defaultLocalCriticModel)
	shadow := resolveShadowModel(*shadowModelFlag)
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
		fmt.Fprintf(stderr, "corral certify --local: warning: --shadow-model %q is the same model as the mutant-generator — the recorded head-to-head compares a model against itself, not two models\n", shadow)
	}
	if err := advpool.CheckDecorrelation(assign); err != nil {
		fmt.Fprintf(stderr, "corral certify --local: %v — pass distinct --writer-model / --critic-model\n", err)
		return 2
	}

	// Require a provider key. The default role models are Claude, so unless the
	// operator selected a different MODEL_BACKEND we need ANTHROPIC_API_KEY. When
	// MODEL_BACKEND is unset we default it to anthropic so FromEnv() builds the
	// Claude backend the default models expect (rather than the ollama default).
	backendSel := strings.TrimSpace(os.Getenv("MODEL_BACKEND"))
	if backendSel == "" || backendSel == "anthropic" || backendSel == "claude" {
		if agentbackend.Secret("ANTHROPIC_API_KEY") == "" {
			fmt.Fprintln(stderr, "corral certify --local: no $ANTHROPIC_API_KEY set — export your Claude key, or select another provider with MODEL_BACKEND + its key")
			return 2
		}
		if backendSel == "" {
			if err := os.Setenv("MODEL_BACKEND", "anthropic"); err != nil {
				fmt.Fprintf(stderr, "corral certify --local: selecting anthropic backend: %v\n", err)
				return 1
			}
		}
	}

	// In --repo-dir mode, --code/--test are repo-relative (or absolute under the
	// repo); the file lives inside the cloned tree. Resolve to filesystem paths
	// for reading; the workspace keys are computed repo-relative below.
	repoDir := strings.TrimSpace(*repoDirFlag)
	fsPath := func(p string) string {
		if repoDir == "" || filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(repoDir, p)
	}

	// Read the code + dev test.
	code, err := os.ReadFile(fsPath(*codePath)) // #nosec G304 -- operator-supplied path to the file under review
	if err != nil {
		fmt.Fprintf(stderr, "corral certify --local: reading --code %s: %v\n", *codePath, err)
		return 2
	}
	tp := strings.TrimSpace(*testPath)
	if tp == "" {
		tp = plug.TestPath(*codePath)
	}
	devTest, err := os.ReadFile(fsPath(tp)) // #nosec G304 -- operator-supplied (or sibling-derived) test path
	if err != nil {
		fmt.Fprintf(stderr, "corral certify --local: reading test %s: %v (pass --test to override)\n", tp, err)
		return 2
	}

	// Resolve the jail. NEVER fall back to unsandboxed — resolveLocalJail fails
	// closed if the requested/auto backend can't isolate on this host (and
	// refuses "none" outright), returning an actionable message.
	iso, err := resolveLocalJail(*jailFlag)
	if err != nil {
		fmt.Fprintf(stderr, "corral certify --local: %v\n", err)
		return 1
	}
	jail := adequacy.NewJail(iso, *timeout)

	// Open the local stores: an ephemeral queue (one run), and the SAME
	// persistent build ledger + signing key `corral certify`/`corral certify
	// verify`/`corral certify pubkey` use, so a --local verdict is signed by the
	// user's own key and lands in the same offline-verifiable ledger.
	qdir, err := os.MkdirTemp("", "corral-local-queue-")
	if err != nil {
		fmt.Fprintf(stderr, "corral certify --local: temp queue dir: %v\n", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(qdir) }()
	q, err := queue.Open(filepath.Join(qdir, "queue.sqlite3"))
	if err != nil {
		fmt.Fprintf(stderr, "corral certify --local: opening queue: %v\n", err)
		return 1
	}
	defer func() { _ = q.Close() }()

	bs, err := buildstore.Open(localBuildDBPath())
	if err != nil {
		fmt.Fprintf(stderr, "corral certify --local: opening build ledger: %v\n", err)
		return 1
	}
	defer bs.Close()

	key, err := buildstore.LoadOrCreateSigningKey(localCertifyKeyPath())
	if err != nil {
		fmt.Fprintf(stderr, "corral certify --local: loading signing key: %v\n", err)
		return 1
	}

	// Resolve the workspace keys + jail-backed scorer/validator. Single-file mode
	// keys by BASENAME (a flat scaffold; the adequacy jail refuses absolute/`..`
	// keys, so an absolute --code must be normalized here). --repo-dir mode
	// seeds the jail with the whole cloned tree and keys the file under audit by
	// its REPO-RELATIVE path, so a mutant overwrites the real file in context
	// and the project's own tests (which import the package) resolve.
	var scorer advpool.JailScorer
	var validator advpool.JailValidator
	var codeKey, devTestKey string
	if repoDir != "" {
		if len(checkArgv) == 0 {
			fmt.Fprintln(stderr, "corral certify --local: --repo-dir requires the project's own test command after `--`, e.g. `-- python3 -m pytest tests/test_recipes.py`")
			return 2
		}
		repoFiles, lerr := loadRepoFiles(repoDir)
		if lerr != nil {
			fmt.Fprintf(stderr, "corral certify --local: reading --repo-dir %s: %v\n", repoDir, lerr)
			return 2
		}
		ck, rerr := filepath.Rel(repoDir, fsPath(*codePath))
		if rerr != nil || strings.HasPrefix(ck, "..") {
			fmt.Fprintf(stderr, "corral certify --local: --code %s is not inside --repo-dir %s\n", *codePath, repoDir)
			return 2
		}
		dk, rerr := filepath.Rel(repoDir, fsPath(tp))
		if rerr != nil || strings.HasPrefix(dk, "..") {
			fmt.Fprintf(stderr, "corral certify --local: --test %s is not inside --repo-dir %s\n", tp, repoDir)
			return 2
		}
		codeKey, devTestKey = filepath.ToSlash(ck), filepath.ToSlash(dk)
		// The just-read code/test are authoritative in the map (identical to the
		// on-disk copy, but explicit so a mutant overlay targets the right key).
		repoFiles[codeKey] = string(code)
		repoFiles[devTestKey] = string(devTest)
		scorer = advpool.JailScorer{Jail: jail, BaseFiles: repoFiles, MutantTimeout: *testTimeout}
		validator = advpool.JailValidator{Jail: jail, BaseFiles: repoFiles}
	} else {
		codeKey = filepath.Base(*codePath)
		devTestKey = filepath.Base(tp)
		scorer = advpool.JailScorer{Jail: jail, MutantTimeout: *testTimeout}
		validator = advpool.JailValidator{Jail: jail}
	}

	// Build the pure driver over the REAL jail-backed scorer/validator and the
	// REAL certify-chain signer.
	d, err := advpool.NewDriver(q, scorer, validator, assign, localCertifyThreshold)
	if err != nil {
		fmt.Fprintf(stderr, "corral certify --local: %v\n", err)
		return 1
	}
	d.Signer = advpool.CertSigner{Key: key, Store: bs, Witness: nil}

	// --record: collect the run into a replayable tape. The sink is the driver's
	// EventSink (pool reasoning beats) and is also fed the task lifecycle +
	// findings from the drive loop below, so one ordered stream is the tape.
	var rec *recordSink
	if strings.TrimSpace(*recordFlag) != "" {
		rec = &recordSink{}
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
	d.RunDeadline = resolveRunDeadline(*timeout, shadow)

	// Resolve repo/commit for the signed subject (best-effort git, else "local").
	repo := strings.TrimSpace(*repoFlag)
	if repo == "" {
		if v, gerr := (realRunner{}).GitOutput("config", "--get", "remote.origin.url"); gerr == nil && v != "" {
			repo = v
		} else {
			repo = "local"
		}
	}
	commit := strings.TrimSpace(*commitFlag)
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
	closeBugCatch, _, shadowRowsRecorded := wireLocalBugCatch(d, localBugCatchDBPath(), repo, commit, stderr)
	defer closeBugCatch()

	n := *nMutants
	if n <= 0 {
		n = 5
	}
	rs := advpool.RunSpec{
		Repo: repo, Commit: commit, Goal: strings.TrimSpace(*goal),
		CodePath: codeKey, Code: string(code),
		DevTestPath: devTestKey, DevTestCode: string(devTest),
		TestCmd:     strings.Join(checkArgv, " "),
		NMutants:    n,
		Lang:        plug.Name(),
		MaxShards:   resolveMaxShards(*maxShardsFlag),
		ShadowModel: shadow,
	}

	// Signatures are best-effort (mirrors the brain's StartRun): a failure just
	// degrades the prompt to no signatures, never refuses the run.
	sigs, serr := repoindex.ExtractSignatures(rs.Code, rs.Lang)
	if serr != nil {
		sigs = nil
	}

	if err := d.StartRun(localMissionID, rs, sigs); err != nil {
		fmt.Fprintf(stderr, "corral certify --local: starting run: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "auditing %s against its own tests — mutant-generator=%s test-writer=%s test-critic=%s\n",
		*codePath, mutant, writer, critic)

	// The outer context bound is slightly beyond the driver's own RunDeadline
	// (which already carries the shadow allowance — see resolveRunDeadline) so
	// the driver gets the chance to emit its signed timeout verdict before ctx
	// cancels the loop.
	outerBound := resolveRunDeadline(*timeout, shadow) + 30*time.Second
	ctx, cancel := context.WithTimeout(context.Background(), outerBound)
	defer cancel()

	chatterFor := localChatterFor(assign)

	// Size the concurrent audit swarm and say so out loud — the "won't bankrupt
	// me / won't melt the box" bound made visible. Independent role tasks run in
	// parallel up to this bound; it's clamped to the host's cores (auto) or the
	// operator's --swarm budget.
	swarm := resolveSwarm(*swarmFlag)
	if *swarmFlag > 0 {
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

	verdict, err := driveLocalRun(ctx, d, q, localMissionID, chatterFor, *poll, time.Sleep, stdout, rec, actorFor, swarm)
	if err != nil {
		fmt.Fprintf(stderr, "corral certify --local: %v\n", err)
		return 1
	}

	renderAdvVerdict(stdout, *codePath, advVerdictFromPool(*verdict))

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

	// --out writes the signed record as a self-contained file the user can
	// re-verify offline. A --local record is signed by the user's OWN key but
	// never publicly witnessed (Witness is nil above), so the verify hint
	// carries --allow-unanchored — an honest "signed by you, not third-party
	// anchored" claim, not a silent omission.
	if out := strings.TrimSpace(*outFlag); out != "" {
		if err := writeLocalRecordFile(out, bs, key, *verdict); err != nil {
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

	// Hand the pool's authored test back: when it killed a survivor the dev suite
	// missed, print it so the dev can adopt it.
	if st, ok := d.RunStatus(localMissionID); ok && strings.TrimSpace(st.AuthoredTest) != "" {
		fmt.Fprintf(stdout, "\nthe herd authored a test that catches a gap your suite missed — add it to %s:\n\n", tp)
		fmt.Fprintln(stdout, strings.TrimRight(st.AuthoredTest, "\n"))
	}

	if verdict.Status == advpool.StatusCertified {
		return 0
	}
	return 3
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

// recEvent is one beat of a replay tape — the exact {ts,kind,actor,subject,
// detail} shape the corralai.dev cockpit (recordings.astro / replay-player.js)
// reconstructs a run from. ts is a monotonic 1-based index (the scrub position);
// the cockpit orders and plays beats by it.
type recEvent struct {
	Ts      int            `json:"ts"`
	Kind    string         `json:"kind"`
	Actor   string         `json:"actor,omitempty"`
	Subject string         `json:"subject,omitempty"`
	Detail  map[string]any `json:"detail,omitempty"`
}

// recordSink collects a --local run's events into a replayable tape. It doubles
// as the driver's advpool.EventSink (the pool_subject/pool_dev_adequacy/
// pool_verdict reasoning beats) AND is fed the task lifecycle + findings from
// the in-process drive loop, so one ordered stream carries everything the
// cockpit needs. Concurrency-safe: the driver and the drive loop are the same
// goroutine here, but guard anyway so a future concurrent worker stays correct.
type recordSink struct {
	mu     sync.Mutex
	ts     int
	events []recEvent
}

func (r *recordSink) add(kind, actor, subject string, detail map[string]any) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ts++
	r.events = append(r.events, recEvent{Ts: r.ts, Kind: kind, Actor: actor, Subject: subject, Detail: detail})
}

// Emit implements advpool.EventSink: the driver's pool reasoning beats, all
// attributed to the pool itself (matching the corral-advpool actor the hosted
// recordings use).
func (r *recordSink) Emit(_ int64, kind, subject string, detail map[string]any) {
	r.add(kind, "corral-advpool", subject, detail)
}

func (r *recordSink) writeTape(path string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.events == nil {
		r.events = []recEvent{}
	}
	data, err := json.MarshalIndent(map[string]any{"events": r.events}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644) // #nosec G306 -- a replay tape is a public artifact
}

// recordActor renders a stable, role-distinct worker id for the tape's roster/
// canvas, e.g. "claude-sonnet-5/test-writer" — so the decorrelated herd shows
// each seat separately even when two roles share a model.
func recordActor(role, model string) string {
	if model == "" {
		return role
	}
	return model + "/" + role
}

// loadRepoFiles walks root and returns every regular text file keyed by its
// slash-separated repo-relative path — the seed for --repo-dir's jail
// workspace. It skips .git, files over 1 MiB (data/fixtures, not source), and
// anything that isn't valid UTF-8 (binaries the text-only jail can't carry),
// and caps the total so a huge checkout can't blow up the workspace. The keys
// are exactly the paths a mutant overlay and the project's own test command
// reference (e.g. `more_itertools/recipes.py`, `tests/test_recipes.py`).
func loadRepoFiles(root string) (map[string]string, error) {
	const maxFile = 1 << 20   // 1 MiB per file
	const maxTotal = 64 << 20 // 64 MiB of text total
	// os.Root confines every open to the repo dir: a symlink pointing outside
	// the tree can't be followed, so a malicious checkout can't smuggle
	// /etc/passwd into the jail workspace (gosec G122 / CWE-367 TOCTOU).
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()

	files := make(map[string]string)
	var total int64
	walkErr := fs.WalkDir(r.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil // never follow a symlink out of the repo
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		if info.Size() > maxFile {
			return nil
		}
		f, oerr := r.Open(rel) // root-scoped: cannot escape the repo dir
		if oerr != nil {
			return oerr
		}
		b, rerr := io.ReadAll(f)
		_ = f.Close()
		if rerr != nil {
			return rerr
		}
		if !utf8.Valid(b) {
			return nil // binary — the jail workspace is text-only
		}
		total += int64(len(b))
		if total > maxTotal {
			return fmt.Errorf("repo has more than %d MiB of text — too large to seed the jail workspace", maxTotal>>20)
		}
		files[rel] = string(b) // fs.WalkDir yields slash-separated paths
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return files, nil
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

// localTickMaxErrors mirrors internal/brain/advpool.go's advPoolTickMaxErrors:
// the brain's tick loop tolerates up to this many CONSECUTIVE Tick errors on a
// run before giving up, because the driver deliberately returns a Tick error
// on RECOVERABLE conditions (an unparseable mutant-generator result or a
// non-compiling test-writer result) after REOPENING the task so a fresh claim
// re-prompts the model — see driver.go's tickDevAdequacy/tickPoolAdequacy
// ReopenTask+"reissued for retry" paths. A --local run must tolerate the same
// bound the brain does: giving up on the first such error would abort the
// whole audit on a frontier model's common non-compiling first attempt.
const localTickMaxErrors = 20

// driveLocalRun is the in-process drive loop — the testable seam. It advances
// the pure driver to convergence exactly the way the brain's tick loop + a
// remote worker interoperate, but with BOTH sides in one process: Tick advances
// the DAG (dev-adequacy scoring, test-writer promotion, pool-adequacy, aggregate,
// signing) while runReadyTasks claims each ready role task and runs it through
// the in-process agentworker.RunRole, completing it (and filing the critic's
// findings) back onto the same queue. The order is Tick, then drain every ready
// task, repeat — Tick between drains is what promotes the dependent test-writer
// once the survivors are known, so the two must interleave, never run one to
// exhaustion before the other.
//
// A Tick error is tolerated up to localTickMaxErrors CONSECUTIVE times — the
// same bound and "reissued for retry" tolerance internal/brain/advpool.go's
// tick loop applies — because the driver has already reopened the offending
// task, so the next drain re-claims and re-runs it. The counter resets to zero
// on any Tick that makes progress (returns without error); only after
// localTickMaxErrors CONSECUTIVE errors does the loop give up and return the
// error (an infra failure, not a recoverable artifact).
//
// chatterFor maps a task's role to the model backend that runs it (injected so
// tests can supply fakes). It returns the converged Verdict, or an error if
// ctx expired before convergence, a role's LLM call failed outright, or Tick
// errored localTickMaxErrors times in a row.
func driveLocalRun(ctx context.Context, d *advpool.Driver, q *queue.Store, missionID int64, chatterFor func(role string) agentworker.Chatter, poll time.Duration, sleep func(time.Duration), progress io.Writer, rec *recordSink, actorFor func(role string) string, swarm int) (*advpool.Verdict, error) {
	consecutiveTickErrors := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("timed out before the pool converged: %w", err)
		}
		verdict, err := d.Tick(ctx, missionID)
		if err != nil {
			consecutiveTickErrors++
			fmt.Fprintf(progress, "certify --local: tick error, reissued for retry (%d/%d): %v\n", consecutiveTickErrors, localTickMaxErrors, err)
			if consecutiveTickErrors >= localTickMaxErrors {
				return nil, fmt.Errorf("giving up after %d consecutive tick errors: %w", localTickMaxErrors, err)
			}
			sleep(poll)
			continue
		}
		consecutiveTickErrors = 0
		if verdict != nil {
			return verdict, nil
		}
		ran, err := runReadyTasks(ctx, q, missionID, chatterFor, rec, actorFor, swarm, progress)
		if err != nil {
			return nil, err
		}
		if !ran {
			// Nothing was claimable this round (e.g. the only ready task is a
			// dependent one the next Tick will promote) — wait, then re-tick.
			sleep(poll)
		}
	}
}

// runReadyTasks claims and runs every currently-ready task on the queue,
// returning whether it ran at least one. Each claimed task is routed to the
// in-process agentworker.RunRole for its role; a test-critic's findings are
// stamped with the run/task/reporter context and filed on the queue BEFORE the
// task is completed, so the driver's aggregate step sees them. A role LLM
// error (the Chatter call itself failing, e.g. a network/API error) aborts the
// run — that is not the recoverable case. The recoverable case (a malformed or
// non-compiling artifact) is handled one layer up: the driver reopens the
// task and returns an error from Tick, which driveLocalRun tolerates up to
// localTickMaxErrors times, so the reopened task IS re-claimed and re-run here
// on the next drain — a real retry, not just a reissue that nothing consumes.
// runReadyTasks drains every currently-claimable task through a BOUNDED pool of
// `swarm` concurrent workers, returning whether at least one ran. This is the
// execution substrate of the swarm: independent role tasks (e.g. the
// mutant-generator and the test-critic, both ready at the start of a run) run in
// parallel instead of one-at-a-time. It stays inside the Tick→drain→Tick
// structure — a drain never unlocks new tasks (only Tick promotes dependents),
// so a worker that sees no claimable task is genuinely done for this drain.
//
// Bounded by design (the cost answer): at most `swarm` workers run at once, and
// `swarm` itself is clamped to the host + the operator budget upstream. The
// first hard error cancels the rest and is returned; the queue's atomic claim
// guarantees no task is run twice even under concurrent workers.
func runReadyTasks(ctx context.Context, q *queue.Store, missionID int64, chatterFor func(role string) agentworker.Chatter, rec *recordSink, actorFor func(role string) string, swarm int, progress io.Writer) (bool, error) {
	if swarm < 1 {
		swarm = 1
	}
	// Workers write progress concurrently; serialize so two notices can't
	// interleave mid-line.
	out := &syncWriter{w: progress}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		mu       sync.Mutex
		ranAny   bool
		firstErr error
		wg       sync.WaitGroup
	)
	fail := func(err error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		mu.Unlock()
		cancel() // stop the other workers promptly
	}

	for i := 0; i < swarm; i++ {
		wg.Add(1)
		go func(workerID string) {
			defer wg.Done()
			for {
				if ctx.Err() != nil {
					return
				}
				task, err := q.ClaimNext(workerID, nil, localLeaseSeconds)
				if err != nil {
					fail(fmt.Errorf("claiming next task: %w", err))
					return
				}
				if task == nil {
					return // nothing claimable → this worker is done for the drain
				}
				mu.Lock()
				ranAny = true
				mu.Unlock()
				if err := runOneTask(ctx, q, missionID, workerID, task, chatterFor, rec, actorFor, out); err != nil {
					fail(err)
					return
				}
			}
		}(fmt.Sprintf("%s-%d", localBee, i))
	}
	wg.Wait()
	return ranAny, firstErr
}

// runOneTask claims-through a single task: routes it to its role's in-process
// agentworker, files any findings, records the tape lifecycle, and completes it
// on the queue under workerID. Pure per-task work — safe to run concurrently
// with other workers (distinct tasks, atomic queue ops, mutex-guarded tape).
func runOneTask(ctx context.Context, q *queue.Store, missionID int64, workerID string, task *queue.Task, chatterFor func(role string) agentworker.Chatter, rec *recordSink, actorFor func(role string) string, progress io.Writer) error {
	if progress == nil {
		progress = io.Discard
	}
	ch := chatterFor(task.Role)
	if ch == nil {
		return fmt.Errorf("no model backend for role %q", task.Role)
	}
	// Tape (no-op when rec is nil): the task appears, is claimed by its seat,
	// files its findings, and completes — the lifecycle the cockpit renders.
	actor := ""
	if actorFor != nil {
		actor = actorFor(task.Role)
	}
	rec.add("task_created", "", task.Key, map[string]any{"role": task.Role, "title": task.Title})
	rec.add("task_claimed", actor, task.Key, map[string]any{"role": task.Role, "title": task.Title})
	result, findings, rerr := agentworker.RunRole(ctx, ch, task.Role, task.Instruction)
	if rerr != nil {
		if task.Role == advpool.RoleMutantGeneratorShadow {
			// A challenger seat is MEASUREMENT, never the gate — so its LLM
			// call failing must not take the certification down with it. This
			// path is otherwise role-blind: runReadyTasks turns any error here
			// into fail(), which cancels every in-flight PRIMARY worker, and
			// driveLocalRun returns it directly (outside the
			// consecutiveTickErrors tolerance, which only wraps d.Tick) — so
			// the run exits 1 with NO verdict. With shadow on by default, a
			// challenger-model 429, a network blip, or a typo'd
			// --shadow-model would kill an audit the primary seats were about
			// to pass.
			//
			// Completing with the ShadowProviderFailedResult sentinel (rather
			// than leaving the task pending) is what lets the run proceed: the
			// primary all-shards-terminal gate never waits on a shadow task at
			// all, and the driver recognizes the sentinel and leaves the seat
			// UNMEASURED rather than attempting to parse it.
			//
			// The sentinel matters, not just "some non-empty completion": an
			// EMPTY result is indistinguishable, downstream, from a real
			// challenger reply that failed to parse — ParseMutants("") always
			// errors, which used to fall into the driver's ordinary
			// parse-failure branch and record a MEASURED, DROPPED, zero-yield
			// row for a model that was never actually asked the question. That
			// is fabricated data landing in the shared scorecard that feeds
			// model routing (see advpool.ShadowProviderFailedResult) — worse
			// than recording nothing.
			fmt.Fprintf(progress, "certify --local: challenger seat %s failed (%v) — measurement skipped, the audit continues\n", task.Key, rerr)
			result, findings = advpool.ShadowProviderFailedResult, nil
		} else {
			return fmt.Errorf("running role %q: %w", task.Role, rerr)
		}
	}
	for _, f := range findings {
		f.MissionID = missionID
		f.TaskID = task.ID
		f.Reporter = task.Role
		normalizeFinding(&f)
		if _, err := q.AddFinding(f); err != nil {
			return fmt.Errorf("recording %q finding: %w", task.Role, err)
		}
		rec.add("finding_reported", actor, f.Target, map[string]any{
			"severity": f.Severity, "type": f.Type, "evidence": f.Evidence, "role": task.Role,
		})
	}
	rec.add("task_done", actor, task.Key, map[string]any{"role": task.Role, "result": result})
	if _, err := q.Complete(task.ID, workerID, result); err != nil {
		return fmt.Errorf("completing role %q: %w", task.Role, err)
	}
	return nil
}

// syncWriter serializes concurrent writes from the bounded worker pool onto
// one progress stream, so two workers' notices cannot interleave mid-line. A
// nil target is treated as io.Discard by the caller.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.w == nil {
		return len(p), nil
	}
	return s.w.Write(p)
}

// validFindingType / validFindingSeverity are the queue.AddFinding-accepted
// values. A critic model can return an off-list type/severity; rather than fail
// the whole run on AddFinding's validation, normalizeFinding coerces an unknown
// value to the safe default (note / low) so the finding is still recorded.
var validFindingType = map[string]bool{
	"vuln": true, "bug": true, "design-flaw": true, "missing-req": true,
	"regression": true, "note": true, "change-request": true, "ops": true,
}
var validFindingSeverity = map[string]bool{"low": true, "medium": true, "high": true, "critical": true}

func normalizeFinding(f *queue.Finding) {
	if !validFindingType[f.Type] {
		f.Type = "note"
	}
	if !validFindingSeverity[f.Severity] {
		f.Severity = "low"
	}
}

// localChatterFor builds the role→backend router for a real run: the base
// backend from FromEnv() (MODEL_BACKEND-selected), switched to each role's
// assigned model via WithModel when the backend supports it. A single ANTHROPIC
// key + the anthropic backend serves all three Claude models this way.
func localChatterFor(assign advpool.RoleAssignment) func(role string) agentworker.Chatter {
	base := agentbackend.FromEnv()
	sw, canSwitch := base.(agentbackend.ModelSwitcher)
	return func(role string) agentworker.Chatter {
		if model := assign[role]; canSwitch && model != "" {
			return agentbackend.AsChatter(sw.WithModel(model))
		}
		return agentbackend.AsChatter(base)
	}
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

// localBugCatchDBPath mirrors localBuildDBPath for the bug-catching scorecard
// store, so a --local run's per-seat metrics (including the shadow
// head-to-head) persist next to the signed build ledger and feed the same
// `corral scorecard` surface the daemon's runs feed.
func localBugCatchDBPath() string {
	if p := strings.TrimSpace(os.Getenv("CORRALAI_BUGCATCH_DB")); p != "" {
		return p
	}
	home := ""
	if u, err := os.UserHomeDir(); err == nil {
		home = u
	} else if usr, err := user.Current(); err == nil {
		home = usr.HomeDir
	}
	return filepath.Join(home, ".claude", "corralai_bugcatch.duckdb")
}

// localBugCatchSink persists a converged --local run's per-seat observations
// into the DuckDB scorecard store, stamping the run context (ts, repo, commit,
// source="pool") the pure driver does not carry. The daemon-side analogue is
// internal/brain.advpoolBugCatchSink; this exists because the brain is the
// only place BugCatch was ever wired, while `certify --local` is the ONLY
// writer of RunSpec.ShadowModel — so on the only path where shadow actually
// runs, every shadow row was computed and thrown away.
//
// Recording is best-effort by design: metrics are not the gate, so a failed
// write warns and the audit stands.
type localBugCatchSink struct {
	store        *bugcatch.Store
	missionID    int64
	repo, commit string
	warn         io.Writer
	// shadowRows counts shadow rows actually WRITTEN to the store (never
	// merely computed) — see wireLocalBugCatch's third return value. Read via
	// atomic because Record is called from the driver's tick path; today that
	// is always the single in-process --local goroutine, but this sink has no
	// business assuming that stays true.
	shadowRows *int64
}

// wireLocalBugCatch opens the scorecard store at path and points the driver's
// BugCatch feed at it, returning a closer that is always safe to call, plus a
// live counter of shadow rows actually persisted (nil if the store never
// opened). A store that will not open is a WARNING, never a failure: the
// audit's verdict does not depend on metrics, so refusing to run over an
// unwritable metrics file would trade the whole product for the telemetry.
//
// The counter exists so a caller can print an honest, PAST-TENSE "recorded to
// the scorecard" claim: printing it merely because shadow was ENABLED was
// false whenever the store failed to open, the run hit its deadline (which
// signs a verdict without ever calling BugCatch), or every shadow seat ended
// unmeasured — see runCertifyLocal.
func wireLocalBugCatch(d *advpool.Driver, path, repo, commit string, warn io.Writer) (closer func(), opened bool, shadowRows *int64) {
	bcs, err := bugcatch.Open(path)
	if err != nil {
		if warn != nil {
			fmt.Fprintf(warn, "corral certify --local: opening scorecard store (metrics only — the audit continues): %v\n", err)
		}
		return func() {}, false, nil
	}
	var n int64
	d.BugCatch = localBugCatchSink{
		store: bcs, missionID: localMissionID, repo: repo, commit: commit, warn: warn, shadowRows: &n,
	}
	return func() { _ = bcs.Close() }, true, &n
}

func (s localBugCatchSink) Record(recordID int64, recordHead string, obs []advpool.BugCatchObservation) {
	if s.store == nil {
		return
	}
	now := time.Now()
	rows := make([]bugcatch.Observation, 0, len(obs))
	shadowCount := int64(0)
	for _, o := range obs {
		if o.Shadow {
			shadowCount++
		}
		rows = append(rows, bugcatch.Observation{
			TS: now, RecordID: recordID, RecordHead: recordHead,
			MissionID: s.missionID, Repo: s.repo, Commit: s.commit,
			Model: o.Model, Role: o.Role, Source: "pool",
			Catches: o.Catches, Opportunities: o.Opportunities,
			SoundTests: o.SoundTests, AuthoredTests: o.AuthoredTests,
			CriticFlags: o.CriticFlags, MutantsPlanted: o.MutantsPlanted, MutantsSurvived: o.MutantsSurvived,
			Shard: o.Shard, Region: o.Region, RegionComplexity: o.RegionComplexity, RegionLines: o.RegionLines,
			TestComplexity: o.TestComplexity, ParseRetries: o.ParseRetries, Dropped: o.Dropped, Shadow: o.Shadow,
		})
	}
	if err := s.store.Record(context.Background(), rows); err != nil {
		if s.warn != nil {
			fmt.Fprintf(s.warn, "corral certify --local: recording scorecard metrics failed (the verdict stands): %v\n", err)
		}
		return
	}
	if s.shadowRows != nil && shadowCount > 0 {
		atomic.AddInt64(s.shadowRows, shadowCount)
	}
}

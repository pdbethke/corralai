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
	"github.com/pdbethke/corralai/internal/buildstore"
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/repoindex"
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
	if onDefaultClaudePath() {
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

	// Resolve the role→backend router NOW, before opening the jail or any
	// store: a cross-vendor critic (e.g. --critic-model gemini-3.5-flash on
	// the default Claude path) needs its own vendor's key, and a missing key
	// must refuse the run here — fail closed at the top, not mid-run after
	// jails/stores/mutants are already in flight.
	chatterFor, err := localChatterFor(assign)
	if err != nil {
		fmt.Fprintf(stderr, "corral certify --local: %v\n", err)
		return 2
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
	wiring, err := buildJailWiring(jailWiringInput{
		iso: iso, timeout: *timeout, testTimeout: *testTimeout,
		codePath: *codePath, testPath: tp, repoDir: repoDir, langName: plug.Name(), fsPath: fsPath,
		code: code, devTest: devTest, checkArgv: checkArgv,
		bindDirFlag: bindDirFlag, noBindDepsFlag: *noBindDepsFlag, stdout: stdout,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	// Release any Go-vendor staging dir once the run completes (the jail
	// bind-mounts vendor/ from it, so it must outlive scoring).
	defer wiring.cleanup()
	scorer, validator, jailEnum := wiring.scorer, wiring.validator, wiring.jailEnum
	codeKey, devTestKey := wiring.codeKey, wiring.devTestKey

	// Build the pure driver over the REAL jail-backed scorer/validator and the
	// REAL certify-chain signer.
	d, err := advpool.NewDriver(q, scorer, validator, assign, localCertifyThreshold)
	if err != nil {
		fmt.Fprintf(stderr, "corral certify --local: %v\n", err)
		return 1
	}
	d.Signer = advpool.CertSigner{Key: key, Store: bs, Witness: nil}
	d.Enumerator = jailEnum

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
		Matrix:      *matrixFlag,
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

	// Size the concurrent audit swarm and say so out loud — the "won't bankrupt
	// me / won't melt the box" bound made visible. Independent role tasks run in
	// parallel up to this bound; it's clamped to the host's cores (auto) or the
	// operator's --swarm budget.
	swarm := resolveSwarm(*swarmFlag)
	d.MatrixWorkers = swarm
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

	// --matrix: print the per-test adequacy summary + delete-candidate list.
	// st.Matrix is nil unless --matrix was set AND the phase actually ran
	// (see advpool.RunState.Matrix's doc comment) — the summary is entirely
	// opt-in and silent otherwise.
	if st, ok := d.RunStatus(localMissionID); ok && st.Matrix != nil {
		renderMatrixSummary(stdout, *st.Matrix)
		rec.add("pool_matrix", "corral-advpool", *codePath, matrixTapeDetail(*st.Matrix))
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
	if in.repoDir != "" {
		if len(in.checkArgv) == 0 {
			return w, fmt.Errorf("corral certify --local: --repo-dir requires the project's own test command after `--`, e.g. `-- python3 -m pytest tests/test_recipes.py`")
		}
		// Provision external Go deps for the offline jail (no-op for other langs,
		// non-modules, or already-vendored repos). Seed from the returned dir.
		seedDir, cleanup, verr := ensureGoVendored(in.repoDir, in.langName, in.stdout)
		if verr != nil {
			return w, verr
		}
		w.cleanup = cleanup
		repoFiles, depBinds, lerr := loadRepoFiles(seedDir, buildLoadOpts(in.iso.Name(), in.bindDirFlag, in.noBindDepsFlag))
		if lerr != nil {
			return w, fmt.Errorf("corral certify --local: reading --repo-dir %s: %v", in.repoDir, lerr)
		}
		w.depBinds = depBinds
		ck, rerr := filepath.Rel(in.repoDir, in.fsPath(in.codePath))
		if rerr != nil || strings.HasPrefix(ck, "..") {
			return w, fmt.Errorf("corral certify --local: --code %s is not inside --repo-dir %s", in.codePath, in.repoDir)
		}
		dk, rerr := filepath.Rel(in.repoDir, in.fsPath(in.testPath))
		if rerr != nil || strings.HasPrefix(dk, "..") {
			return w, fmt.Errorf("corral certify --local: --test %s is not inside --repo-dir %s", in.testPath, in.repoDir)
		}
		w.codeKey, w.devTestKey = filepath.ToSlash(ck), filepath.ToSlash(dk)
		// The just-read code/test are authoritative in the map (identical to the
		// on-disk copy, but explicit so a mutant overlay targets the right key).
		repoFiles[w.codeKey] = string(in.code)
		repoFiles[w.devTestKey] = string(in.devTest)
		jail := adequacy.NewJail(in.iso, in.timeout, adequacy.WithReadOnlyBinds(depBinds))
		// enumerator backs the tests×mutants matrix's test-listing step
		// (--matrix). Wired unconditionally off the SAME backend/timeout/binds
		// as jail (bwrapJail satisfies both interfaces) — a nil
		// advpool.Driver.Enumerator makes tickMatrix always skip regardless of
		// RunSpec.Matrix, so wiring it here costs nothing when --matrix is off
		// (the flag is the real gate).
		enumerator := adequacy.NewEnumerator(in.iso, in.timeout, adequacy.WithReadOnlyBinds(depBinds))
		w.scorer = advpool.JailScorer{Jail: jail, BaseFiles: repoFiles, MutantTimeout: in.testTimeout}
		w.validator = advpool.JailValidator{Jail: jail, BaseFiles: repoFiles}
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
		w.scorer = advpool.JailScorer{Jail: jail, MutantTimeout: in.testTimeout}
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
		BaselineFailed:   v.BaselineFailed,
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

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

// THERE ARE NO DEFAULT MODELS. Not here, not anywhere on the audit path.
//
// corral's claim is that it is model-agnostic — "across any model, local 7B to
// frontier" — and a binary that ships with one vendor's model names baked in
// does not make that claim, it makes an exception to it. The defaults used to
// be two Claude models off a single ANTHROPIC_API_KEY, which meant a stranger
// arriving with an OpenAI key, a Gemini key, an OpenRouter key or a local
// Ollama daemon hit a failure on their first command and had to discover five
// flags to get past it. We use Claude; we do not make anyone else.
//
// So every seat is named by the operator, and a run with an unnamed seat is
// REFUSED with a message that says which seats are empty and which provider
// credentials it can actually see (see herdNotConfiguredErr). The one rule that
// survives is decorrelation — the test-critic must differ from the test-writer
// — and that is a PROPERTY, not a vendor: it is satisfied by any two distinct
// models from any provider.
//
// The challenger (shadow) seat is OFF unless named, for the same reason: its
// default was a Claude model that stayed on through an otherwise all-Gemini
// run, silently requiring an Anthropic key nobody asked for.

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
	writerModel := fs.String("writer-model", "", "model for the test-writer role — REQUIRED, corral has no default models. Takes a registry alias (.corral/models.json) or a concrete model name")
	criticModel := fs.String("critic-model", "", "model for the test-critic role, which must differ from the writer's; \"off\" disables the critic entirely (it is advisory and never gates the verdict, so a single-vendor run with only one usable model can drop it). No default")
	mutantModel := fs.String("mutant-model", "", "model for the mutant-generator role — REQUIRED, corral has no default models. Takes a registry alias (.corral/models.json) or a concrete model name")
	jailFlag := fs.String("jail", "", "sandbox backend: bwrap|container (Linux), sandbox-exec (macOS) (default: auto-detect for this OS; \"none\" is not supported — --local always sandboxes). \"container\" needs CORRALAI_EXEC_IMAGE set to a toolchain image, e.g. CORRALAI_EXEC_IMAGE=python:3.12-bookworm")
	timeout := fs.Duration("timeout", 10*time.Minute, "give up if the run makes no progress for this long (not a hard wall-clock cap — a single slow LLM call can overshoot it)")
	testTimeout := fs.Duration("test-timeout", 0, "hard cap on a SINGLE test-suite run in the jail (0 = auto: derived from the healthy suite's own runtime, so a mutant that makes the suite hang is killed fast instead of eating the whole --timeout). Raise it only if your suite legitimately runs long")
	noFailFast := fs.Bool("no-fail-fast", false, noFailFastHelp)
	poll := fs.Duration("poll", 2*time.Second, "how long to wait between drive iterations when nothing is claimable")
	repoFlag := fs.String("repo", "", "repository (default: git remote.origin.url, else \"local\")")
	commitFlag := fs.String("commit", "", "commit sha (default: git rev-parse HEAD, else \"local\")")
	outFlag := fs.String("out", "", "also write the signed verdict as a self-contained record file, re-verifiable offline with `corral certify verify <file> --pubkey <hex> --allow-unanchored`")
	quietFlag := fs.Bool("quiet", false, "suppress the live progress echo on stderr (the verdict, --out and --record are unaffected)")
	repoDirFlag := fs.String("repo-dir", "", "audit --code IN THE CONTEXT of this cloned repo/package: the whole tree is seeded into the jail, the file is mutated in place, and the project's OWN test command (given after `--`) grades it — so real multi-file projects with package imports work (--code/--test are repo-relative)")
	recordStreamFlag := fs.String("record-stream", "", "stream each run event as newline-delimited JSON to this file AS IT HAPPENS — the same events --record collects into a tape at the end, so a watcher (`tail -f`, the cockpit) can follow a run in flight instead of waiting hours for it to finish. Independent of --record: either, both, or neither")

	recordFlag := fs.String("record", "", "write a replayable tape of the run (the pool's reasoning beats, task lifecycle, and findings) to this JSON file — the same {events:[…]} shape the corralai.dev cockpit replays")
	swarmFlag := fs.Int("swarm", 0, "max concurrent audit workers (0 = auto-size to this host's cores). The BUDGET clamp: independent role tasks run in parallel up to this bound, so a big audit swarms without melting the box")
	maxShardsFlag := fs.Int("max-shards", 0, "max mutant-generator seats fanned out across the file's functions (0 = "+fmt.Sprint(advpool.DefaultMaxShards)+"). Bounds PARALLELISM only — every function is probed regardless; --n-mutants is the PER-SHARD budget")
	shadowModelFlag := fs.String("shadow-model", "", "challenger model that attacks every region a SECOND time for a region-controlled head-to-head. OFF unless named. Recorded for comparison — NEVER gates the verdict")
	writerModeFlag := fs.String("writer-mode", "", "how the test-writer attacks this file's survivors: `per-survivor` (the default) makes ONE call per survivor — each carrying the file once as a cacheable shared prefix plus that survivor's diff, each repaired on its own budget and each PROVEN ALONE against its own mutant — or `batched`, the original shape: one call carrying every survivor, one repair budget, one proof pass over all of them. Nothing measured changes between them (a survivor is proven iff an authored test kills it alone and passes on the original, either way); what changes is that one unbuildable test no longer spends the whole file's retries and takes every other survivor down with it. Each survivor's proof in per-survivor mode runs its OWN compliant baseline (a compliant pass plus a canary, per seat), so a file with N survivors pays N baselines where batched paid one: on a repo whose suite takes a minute, prefer --writer-mode batched or expect N baselines' worth of wall clock.")
	shadowWriterModelFlag := fs.String("shadow-writer-model", "", "challenger WRITER model that authors a second suite against the SAME mutant set for a mutant-controlled head-to-head. OFF unless named. Recorded for correlation — NEVER gates the verdict")
	matrixFlag := fs.Bool("matrix", false, "opt into the tests×mutants matrix: after the primary pass, re-score EVERY dev test ALONE against the run's mutants — a per-test adequacy readout + a delete-candidate list, instead of one dev-suite-wide number. COSTLY: T tests × M mutants extra jail runs (T×M, on top of the primary pass), so leave off by default on a big suite")
	var localEndpointFlag stringSlice
	fs.Var(&localEndpointFlag, "local-endpoint", "place a LOCAL seat on a specific ollama daemon, as <role>=<url> (repeatable; e.g. test-writer=http://localhost:11436). A daemon is pinned to a GPU by its own environment (HIP_VISIBLE_DEVICES / CUDA_VISIBLE_DEVICES), so this is how two models occupy two cards at once — corral selects the DAEMON, never the device. Without it every local seat shares OLLAMA_URL, one card and one VRAM budget. Roles: mutant-generator, test-writer, test-critic, mutant-generator-shadow, test-writer-shadow. An unknown role, a duplicate role, a non-absolute url, or an endpoint on a seat holding a CLOUD model is refused rather than ignored")

	mutantsFlag := fs.String("mutants", "", "REPLAY a recorded mutant set (see --record-mutants) instead of generating one: --code is graded against exactly the mutants recorded for it, and no mutant-generator model call is made. Refused (exit 2) if the file is absent from the set or its bytes have changed since it was recorded — a mutant is a single-point edit of specific bytes, and re-applying it to different ones grades an exam nobody wrote. Reads a corral-mutants-2 document, or an older corral-mutants-1 one, whose whole-file mutants still replay byte-for-byte.")
	recordMutantsFlag := fs.String("record-mutants", "", "write the mutants this run actually GRADED to this file, as a replayable corral-mutants-2 document — each mutant its SEARCH/REPLACE hunk, tied to the sha256 of the source it is an edit of. Mutants are authored by a model, so an ordinary run re-draws the exam every time; pin the set and a later comparison measures the thing you changed instead of generator variance. Written even when the verdict is needs-review. A v2 document re-recorded from a --mutants replay of an older corral-mutants-1 set contains that set's WHOLE-FILE entries, not hunks — the run graded what was recorded, and re-recording it does not manufacture anchors it never had")
	var bindDirFlag stringSlice
	fs.Var(&bindDirFlag, "bind-dir", "extra repo-relative dependency dir to mount read-only into the jail instead of copying it into the workspace (repeatable; node_modules/vendor/.venv/venv/.bundle are auto-detected) — --repo-dir mode only")
	noBindDepsFlag := fs.Bool("no-bind-deps", false, "copy dependency dirs into the jail workspace instead of bind-mounting them read-only (the pre-bind behavior; subject to the workspace size cap)")
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}

	// Validated here, before anything is spent: the mode changes how many
	// model calls a run makes and what its verdict discloses, so a typo must
	// exit 2 rather than quietly take the default and hand back a different
	// measurement than the one that was asked for.
	writerMode, wmErr := advpool.ResolveWriterMode(*writerModeFlag)
	if wmErr != nil {
		fmt.Fprintf(stderr, "%s: %v\n", "corral certify --local", wmErr)
		return 2
	}

	// The model registry (docs/design/model-registry.md). Same contract as the
	// --repo path: a declared alias resolves to its concrete model HERE, before
	// anything else reads the value, and anything that is not a declared alias
	// stays exactly as typed. With no registry declared this changes nothing.
	seatReg, regErr := resolveSeatRegistry("corral certify --local", *repoDirFlag,
		certifySeats(nil, mutantModel, writerModel, criticModel, shadowModelFlag, shadowWriterModelFlag), stderr)
	if regErr != nil {
		fmt.Fprintf(stderr, "corral certify --local: %v\n", regErr)
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
	localEndpoints, lerr := parseLocalEndpoints(localEndpointFlag)
	if lerr != nil {
		fmt.Fprintf(stderr, "corral certify --local: %v\n", lerr)
		return 2
	}
	// A local registry entry places its seat on its own daemon through the
	// SAME role->url map --local-endpoint fills; the explicit flag wins.
	localEndpoints = mergeLocalEndpoints(localEndpoints, seatReg.localEndpoints())

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
	// The sink now exists on EVERY run, not only under --record: it is what
	// feeds the live progress echo. Without it a run printed four lines and
	// then went silent for minutes while eight seats worked, which reads as a
	// hang rather than as work. The tape is still only WRITTEN when --record
	// asks for one.
	rec := &recordSink{}
	if !*quietFlag {
		rec.live = stderr
	}
	// --record-stream: open the live event stream BEFORE any work starts, so a
	// watcher attached at t=0 sees the first beat. Truncated rather than
	// appended: a stream is one run's, and silently continuing a previous run's
	// file would hand a watcher two interleaved timelines.
	if p := strings.TrimSpace(*recordStreamFlag); p != "" {
		f, ferr := os.Create(p) // #nosec G304 -- operator-supplied output path, same contract as --record
		if ferr != nil {
			fmt.Fprintf(stderr, "corral certify --local: --record-stream %s: %v\n", p, ferr)
			return 2
		}
		defer func() { _ = f.Close() }()
		rec.stream = f
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

	// --mutants: read and CHECKED before the audit starts, against the exact
	// bytes about to be graded. The check is the whole point — a mutant is a
	// single-point edit of specific source, so replaying it against anything
	// else grades an exam nobody wrote and nobody reviewed.
	var presetMutants []adequacy.Mutant
	if p := strings.TrimSpace(*mutantsFlag); p != "" {
		set, serr := adequacy.ReadMutantSet(p)
		if serr != nil {
			fmt.Fprintf(stderr, "corral certify --local: %v\n", serr)
			return 2
		}
		// The lookup key is the SAME codeKey the recorder writes under — see
		// localMutantSetKey. Keying on the raw --code string instead made a
		// run unable to replay its own recording whenever --code carried a
		// directory component.
		setKey, srcPath := localMutantSetKey(*repoDirFlag, *codePath)
		src, rerr := os.ReadFile(srcPath) // #nosec G304 -- the file this run was asked to audit
		if rerr != nil {
			fmt.Fprintf(stderr, "corral certify --local: --mutants: reading %s to check it against the recorded set: %v\n", *codePath, rerr)
			return 2
		}
		ms, merr := set.MutantsFor(setKey, string(src))
		if merr != nil {
			fmt.Fprintf(stderr, "corral certify --local: --mutants: %v\n", merr)
			return 2
		}
		presetMutants = ms
		fmt.Fprintf(stdout, "replaying %d recorded mutant(s) for %s from %s — no mutant-generator model call will be made\n", len(ms), setKey, p)
	}

	// --record-mutants: flushed after the run on EVERY exit path below, a
	// needs-review verdict included. A red run's exam is the one most worth
	// being able to reproduce.
	var mutantSink func(string, []adequacy.Mutant)
	if p := strings.TrimSpace(*recordMutantsFlag); p != "" {
		rc := newMutantSetRecorder()
		mutantSink = rc.sink
		defer func() {
			n, werr := rc.write(p)
			if werr != nil {
				fmt.Fprintf(stderr, "corral certify --local: --record-mutants NOT written: %v\n", werr)
				return
			}
			// 0/0: `certify --local` audits one file and has no verdict
			// cache, so there is no denominator or cache-hit count to
			// disclose — see mutantSetRecorder.report.
			rc.report(stdout, p, n, 0, 0)
		}()
	}

	auditCtx, stopSignals := auditContext(stderr)
	defer stopSignals()

	verdict, err := auditOneFile(auditCtx, localAuditInput{
		repoDir: strings.TrimSpace(*repoDirFlag), codePath: *codePath,
		testPath: strings.TrimSpace(*testPath), goal: strings.TrimSpace(*goal),
		lang: strings.TrimSpace(*langFlag),

		swarm: *swarmFlag, mutantTimeout: *testTimeout, timeout: *timeout,
		noFailFast: *noFailFast,
		poll:       *poll, nMutants: *nMutants, maxShards: *maxShardsFlag,

		writerModel: *writerModel, criticModel: *criticModel,
		mutantModel: *mutantModel, shadowModel: *shadowModelFlag,
		shadowWriterModel: *shadowWriterModelFlag,
		seatProviders:     seatReg.seatProviders(),
		writerMode:        writerMode,

		jail: *jailFlag, checkArgv: checkArgv,
		localEndpoints: localEndpoints,
		bindDirs:       bindDirFlag, noBindDeps: *noBindDepsFlag,

		repo: strings.TrimSpace(*repoFlag), commit: strings.TrimSpace(*commitFlag),

		presetMutants: presetMutants, mutantSink: mutantSink,

		matrix: *matrixFlag, record: rec, bugCatchDB: localBugCatchDBPath(), criticScoreDB: localCriticScoreDBPath(),
		mutantAttemptsDB: localMutantAttemptsDBPath(),
		openStore:        openStore,
		stdout:           stdout, stderr: stderr,
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
	// does not set it. On the bwrap-jail substrate it is that many disposable
	// jails; on the WORKSPACE substrate it is that many private trees in an
	// adequacy.WorkspacePool (it used to be pinned to 1 there, because a bare
	// WorkspaceRunner mutates one checkout in place, unsynchronized — the pool
	// is what removed the shared tree). resolveMutantConcurrency is the single
	// place that number is decided; the pool's own probe is what may still
	// reduce it, per file, to a number the suite can actually survive.
	mutantConcurrency int
	// noFailFast turns OFF the per-mutant stop-at-first-failure flag — see
	// noFailFastHelp for what that costs. False (the default) lets a killed
	// mutant stop at the one failing test that killed it.
	noFailFast    bool
	mutantTimeout time.Duration
	timeout       time.Duration
	poll          time.Duration
	nMutants      int
	maxShards     int

	// Role models. Empty means this file's stock default.
	writerModel, criticModel, mutantModel, shadowModel string
	shadowWriterModel                                  string

	// seatProviders is role -> provider for the seats the model registry
	// resolved (and, for a concrete model name, the provider inferred from it).
	// Empty is the ordinary case and means "infer from the model name", which
	// is what every caller did before the registry existed. Its only use today
	// is DISCLOSURE: decorrelation still refuses on the model name alone.
	seatProviders map[string]string

	// writerMode is the resolved --writer-mode: how the writer seat attacks
	// this file's survivors. Already validated by ResolveWriterMode at the
	// flag boundary, so it is one of the two canonical spellings by the time
	// it reaches here — never an operator's raw string.
	writerMode string

	// localEndpoints places a LOCAL seat on a specific ollama daemon
	// (role -> base URL). A daemon is pinned to a GPU by its own environment,
	// so this is how two models occupy two cards at once; corral selects the
	// daemon, never the device. Empty keeps every local seat on OLLAMA_URL.
	localEndpoints map[string]string

	// Jail + workspace. jail empty = auto-detect this OS's backend (never
	// unsandboxed). checkArgv is the project's own test command, required in
	// repo-aware mode.
	checkArgv  []string
	jail       string
	bindDirs   []string
	noBindDeps bool

	// baseArgv is checkArgv BEFORE coverage-guided narrowing: the operator's
	// own `-- <cmd>` or the plugin's stock recursive command. It is what
	// RunSpec.TestCmd carries, because the narrowing happens downstream from
	// the run's Selection, once per pass: the driver runs advpool.DevCommand
	// for the dev pass (and the shadow pass), and the scorer's authoredCmd
	// adds the pool's own test — which no evidence run ever saw — for the
	// authored pass. Empty means "same as checkArgv" — the `--local` path,
	// which never narrows.
	baseArgv []string

	// selection is what the scan's one instrumented run decided for THIS
	// file: the tests that executed it, or a Fallback saying why the whole
	// suite must run instead. Zero on the `--local` path, which is exactly
	// the pre-selection behaviour.
	selection lang.Selection

	// presetMutants REPLAYS a recorded mutant set for THIS file instead of
	// generating one: the run seeds no mutant-generator seat and grades the
	// dev suite against exactly these mutants. nil generates as before. The
	// caller is responsible for having proven these mutants are edits of the
	// bytes about to be audited — see adequacy.MutantSetFile.MutantsFor,
	// which is the only thing that can prove it.
	presetMutants []adequacy.Mutant

	// mutantSink, when non-nil, is handed the mutants this file's dev pass
	// actually GRADED, once. It is how `--record-mutants` accumulates a
	// replayable set across a whole scan. nil records nothing.
	mutantSink func(codePath string, ms []adequacy.Mutant)

	// eventSink, when non-nil, is wired to the driver as its EventSink
	// instead of `record` — the `certify --repo` position (a scan-wide
	// scanEventSink, one adapter per file, see localExecutor.auditInputFor),
	// as distinct from `certify --local`'s `record` tape sink. The two are
	// mutually exclusive in practice: nil is every `--local` caller's
	// position, and `record` is nil on every `--repo` caller's.
	eventSink advpool.EventSink

	// concurrency, when non-nil, is where the workspace substrate RECORDS
	// what its concurrency probe decided for this file: how many private
	// trees the pool actually got, and why, if it was downgraded to one (see
	// adequacy.WorkspacePool.Probe). It is a pointer sink, not a value,
	// because this input is passed BY VALUE through every layer between the
	// scan and the pool — a value field would be written on a copy and the
	// answer would die there, which is the silently-discarded-measurement
	// shape this codebase keeps producing.
	//
	// nil records nothing, which is every caller that is not on the workspace
	// substrate: the jail builds no trees and has nothing to disclose.
	concurrency *adequacy.Disclosure

	// selectionDuration is how long the SCAN's single instrumented coverage
	// run took (cmd/corral's collectSelection), handed down so this file's
	// RunSpec can carry it onto the verdict.
	//
	// Zero whenever no such run happened: `certify --local` runs no scan-wide
	// selection pass at all, and --whole-suite (or an unsupported language)
	// returns from collectSelection before it instruments anything. The
	// measurement is taken around the RUN, not around the call, precisely so
	// those cases record nothing — the ledger then stores NULL and the report
	// prints "—", rather than a near-zero that reads as a selection pass that
	// was free.
	selectionDuration time.Duration

	// pool, when non-nil, is where THIS file-job's workspace pool lives: the
	// first thing that needs it builds and probes it, everything after that
	// borrows the same one, and the CALLER (localExecutor.Execute) closes it
	// once the whole job is done. Nil means "build your own and own it",
	// which is `certify --local`'s position and every jail-substrate caller's.
	pool *workspacePool

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
	// criticScoreDB is the critic-accuracy store for this run. Same contract
	// as bugCatchDB: empty means no feed at all, which is what the repo scan
	// wants (N concurrent audits must not contend on one single-process
	// DuckDB file).
	criticScoreDB string
	// mutantAttemptsDB is the writer-seat correlation store for this run. Same
	// contract again: empty means no feed. The repo scan leaves it empty for
	// the same DuckDB-contention reason AND because it exposes no
	// --shadow-writer-model, so it can never produce a pair to record.
	mutantAttemptsDB string
	openStore        func() (*buildstore.Store, ed25519.PrivateKey, error)

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
	// scanReason, when set, is the reposcan disposition this error deserves in
	// a whole-repo report — see ScanReason.
	scanReason string
}

func (e localAuditError) Error() string { return e.msg }

// ScanReason implements reposcan.ReasonCarrier: a repo scan asks an executor
// error which disposition it deserves, so a precise refusal is not flattened
// into the catch-all executor-error. Empty means "no opinion".
func (e localAuditError) ScanReason() string { return e.scanReason }

func auditUsageErr(format string, a ...any) error {
	return localAuditError{usage: true, msg: fmt.Sprintf(format, a...)}
}

// auditNotCollectedErr is auditUsageErr for the ONE refusal a repo scan must
// report distinctly: the caller's test command would not run the test this
// audit writes. It is a fact about the invocation, not a corral failure.
func auditNotCollectedErr(format string, a ...any) error {
	return localAuditError{usage: true, scanReason: reposcan.ReasonTestCmdCannotCollect, msg: fmt.Sprintf(format, a...)}
}

func auditErr(format string, a ...any) error {
	return localAuditError{msg: fmt.Sprintf(format, a...)}
}

// noTestFoundErr renders a reposcan.FindTest miss as the message an operator
// actually needs: not just "no test found", but every convention candidate
// FindTest already ruled out and every root its recursive fallback already
// searched — the rehearsal this whole change exists for made a stranger
// guess wrong TWICE (a --repo run that silently excluded the file, then a
// --local run that guessed one sibling path and died trying to open it)
// before ever learning where corral had actually looked. This message is the
// third guess made unnecessary.
func noTestFoundErr(codePath string, res reposcan.SearchResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "no test found for %s (pass --test to override). Looked for:\n", codePath)
	for _, t := range res.Tried {
		fmt.Fprintf(&b, "  %s\n", t)
	}
	if len(res.Roots) > 0 {
		fmt.Fprintf(&b, "and searched %s recursively for a matching basename\n", strings.Join(res.Roots, ", "))
	}
	return strings.TrimRight(b.String(), "\n")
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
func prepareAuditJail(ctx context.Context, in localAuditInput, plug lang.Plugin, timeout time.Duration, stdout io.Writer) (auditJailPrep, error) {
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
		// --test always overrides everything below it; only reached when the
		// operator named no test at all. searchRoot is where FindTest resolves
		// its candidates AND its recursive fallback against: repoDir in
		// repo-aware mode (codePath is repo-relative), the working directory
		// otherwise (codePath is already a filesystem path relative to it) —
		// the same root fsPath itself joins against.
		searchRoot := repoDir
		if searchRoot == "" {
			searchRoot = "."
		}
		res, ferr := reposcan.FindTest(plug, searchRoot, in.codePath)
		if ferr != nil {
			return p, auditErr("searching for a test for %s: %v", in.codePath, ferr)
		}
		if !res.Found {
			return p, auditUsageErr("%s", noTestFoundErr(in.codePath, res))
		}
		tp = res.Path
		if res.ViaSearch {
			// The plugin's own naming convention never found this — every
			// TestPaths candidate came up empty, and this is the ONE line
			// that discloses the pairing came from somewhere else instead of
			// silently presenting it as though it were the expected sibling.
			fmt.Fprintf(stdout, "  paired by search: %s\n", tp)
		}
	}
	devTest, err := os.ReadFile(fsPath(tp)) // #nosec G304 -- operator-supplied (or convention/search-derived) test path
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

	wiring, err := buildJailWiring(ctx, jailWiringInput{
		iso: iso, timeout: timeout, testTimeout: in.mutantTimeout,
		codePath: in.codePath, testPath: tp, repoDir: repoDir, langName: plug.Name(), fsPath: fsPath,
		code: code, devTest: devTest, checkArgv: in.checkArgv,
		baseArgv: in.baseArgv, selection: in.selection,
		bindDirFlag: in.bindDirs, noBindDepsFlag: in.noBindDeps, stdout: stdout,
		seed: in.seed, substrate: in.substrate, mutantConcurrency: in.mutantConcurrency,
		concurrency: in.concurrency, pool: in.pool, noFailFast: in.noFailFast,
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
	prep, err := prepareAuditJail(ctx, in, plug, timeout, io.Discard)
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
		// ShellJoin, not strings.Join: under test selection checkArgv carries
		// pytest node ids, and a parametrized id
		// (tests/test_a.py::test_x[hello world]) contains a SPACE. A plain
		// join is not reversible, so the re-split downstream would tear that
		// id in two and the baseline would fail on a test that does not
		// exist — surfacing as COULD-NOT-GRADE, blamed on the project. Same
		// joiner newAuditRunSpec already uses; pairs with adequacy.ShellSplit.
		testCmd: adequacy.ShellJoin(in.checkArgv),
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
	// shadowWriter is the CHALLENGER writer model, carried here for the SAME
	// reason shadow is: resolveRoleModels resolves it, but the only consumer
	// that can act on it is the RunSpec, and a field that stops at the
	// RoleAssignment never reaches advpool's driver. Dropping it here is
	// precisely how --shadow-writer-model came to force a cache miss, demand a
	// credential, and name a seat in the SIGNED record while never enqueueing
	// one line of challenger work — see newAuditRunSpec.
	shadowWriter string
	chatterFor   func(role string) agentworker.Chatter
	// meters is ONE agentbackend.UsageMeter PER ROLE this run actually
	// dispatches — built by auditRoleMeters and never populated for a role
	// left empty or resolved to "off". An audit's cost is O(mutants x suite
	// runtime) on the execution side and O(tokens) on the model side; the
	// ledger already records the first half (per file, per phase), and this
	// is the second (per file, per ROLE — see modelCallsFromMeters, which
	// turns this map into the []advpool.ModelCall a Verdict carries).
	meters map[string]*agentbackend.UsageMeter
}

// herdNotConfiguredErr refuses a run whose grading seats have no model, and
// says what to do about it.
//
// This is the failure a stranger meets first, so it is the most important error
// message in the tool. It does two things a bare "flag required" cannot: it
// names WHICH seats are empty, and it reports which provider credentials are
// actually visible in this environment — because the usual cause is "I have a
// key, I just don't know what corral wants from me."
//
// It deliberately does NOT suggest a model name. Naming one would reintroduce
// the default through the error message, and the vendor whose key happens to be
// present is not our choice to make. It names the provider and lets the
// operator pick from that provider's own catalogue.
func herdNotConfiguredErr(cmdName, writer, mutant string) error {
	var empty []string
	if writer == "" {
		empty = append(empty, "--writer-model (test-writer)")
	}
	if mutant == "" {
		empty = append(empty, "--mutant-model (mutant-generator)")
	}
	if len(empty) == 0 {
		return nil
	}
	cmd := orDefault(cmdName, "corral certify --local")

	var seen []string
	for _, probe := range []struct{ env, provider string }{
		{"ANTHROPIC_API_KEY", "Anthropic"},
		{"OPENAI_API_KEY", "OpenAI"},
		{"GEMINI_API_KEY", "Google"},
		{"GOOGLE_API_KEY", "Google"},
		{"OPENROUTER_API_KEY", "OpenRouter"},
	} {
		if strings.TrimSpace(os.Getenv(probe.env)) != "" {
			seen = append(seen, fmt.Sprintf("%s (%s)", probe.env, probe.provider))
		}
	}
	creds := "none — no provider credential is set in this environment"
	if len(seen) > 0 {
		creds = strings.Join(seen, ", ")
	}
	if b := strings.TrimSpace(os.Getenv("MODEL_BACKEND")); b != "" {
		creds += fmt.Sprintf("; MODEL_BACKEND=%s", b)
	}

	return auditUsageErr(
		"no model is assigned to %s.\n"+
			"corral has no default models on purpose — it is model-agnostic, so every seat is yours to name.\n"+
			"credentials visible here: %s\n"+
			"assign each seat a model that provider serves, e.g.:\n"+
			"  %s --writer-model <model> --mutant-model <model> --critic-model <model>\n"+
			"the only rule is that the critic must differ from the writer (that is the decorrelation the verdict rests on);\n"+
			"--critic-model off drops the critic entirely — it is advisory and never gates the verdict.\n"+
			"`corral doctor` checks a herd, and its credentials, for free before you spend anything",
		strings.Join(empty, " and "), creds, cmd)
}

// resolveRoleModels is the naming half of resolveAuditRoles: it applies the
// defaults and the "off" spellings, and does nothing else — no decorrelation
// check, no credential requirement, no backend construction.
//
// It exists separately because the cache key needs the RESOLVED model names on
// the free `--dry-run` path too, where there is no key to check and nothing to
// spend. Keeping one spelling of "what model does this role actually use"
// stops the key and the preflight from ever disagreeing.
func resolveRoleModels(in localAuditInput) (writer, mutant, critic, shadow, shadowWriter string) {
	// No defaults behind any seat — corral has none. "off" resolves to "" for
	// the optional seats, and an empty grading seat is refused by the
	// caller (herdNotConfiguredErr) rather than filled.
	//
	// This is the ONE place a seat's model is resolved, so the cache key and
	// the run always agree about which models an audit used. They must: the
	// key records the herd, and a key derived from a different resolution than
	// the run would let a verdict be reused across a model change.
	return strings.TrimSpace(in.writerModel),
		strings.TrimSpace(in.mutantModel),
		advpool.ResolveOptionalModel(in.criticModel, ""),
		resolveShadowModel(in.shadowModel),
		advpool.ResolveOptionalModel(in.shadowWriterModel, "")
}

// modelSetKey is the canonical KeyInputs.ModelSet for a resolved role set.
//
// Role names are the wire names used everywhere else in this codebase
// (test-writer, mutant-generator, critic, shadow) so a ledger row is legible
// without a decoder ring. A disabled role keeps its resolved value ("off")
// rather than being dropped: "the critic was deliberately disabled" and "the
// critic ran" are different audits and must key differently.
func modelSetKey(writer, mutant, critic, shadow, shadowWriter string) string {
	kv := map[string]string{
		"test-writer":      writer,
		"mutant-generator": mutant,
		"critic":           critic,
		"shadow":           shadow,
	}
	// Omitted when off, DELIBERATELY breaking the keep-disabled-seats-in-the-key
	// convention the other optional seats follow. The critic's on/off changes
	// what an audit MEASURES, so it must key differently. The challenger writer
	// cannot change the verdict at all — a run with it off is byte-identical to
	// a pre-feature run, so including an empty entry would invalidate every
	// cached verdict in existence on upgrade, for no change in meaning.
	//
	// Naming a challenger DOES add the entry, which is required: without it,
	// enabling the challenger would hit a cached verdict, skip the run, and
	// silently collect no measurement at all.
	if shadowWriter != "" {
		kv["test-writer-shadow"] = shadowWriter
	}
	return reposcan.CanonicalKV(kv)
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
	writer, mutant, critic, shadow, shadowWriter := resolveRoleModels(in)
	if err := herdNotConfiguredErr(in.cmdName, writer, mutant); err != nil {
		return r, err
	}
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
	if shadowWriter != "" {
		assign[advpool.RoleTestWriterShadow] = shadowWriter
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

	// Provider-aware decorrelation, DISCLOSED not enforced. The guard above
	// compares MODEL NAMES and always has: two distinct names pass it, even
	// when both come off the same vendor's training pipeline. That refusal is
	// deliberately left exactly as it is — tightening it would break every
	// single-vendor run in existence — but the fact it cannot see is now
	// available as data (the registry declares `provider`), and a fact corral
	// knows and does not say is the shape of bug this project exists to find.
	//
	// Said once, at the seam where decorrelation is decided, so the operator
	// reads it before spending rather than inferring it from two model names
	// afterwards.
	if v := sharedSeatProvider(in.seatProviders, writer, critic); v != "" {
		fmt.Fprintf(stderr, "%s: decorrelation: test-writer (%s) and test-critic (%s) are different models from the SAME provider (%s) — the critic is an independent MODEL but not an independent VENDOR. Point one seat at another provider for a cross-vendor read.\n",
			orDefault(in.cmdName, "corral certify --local"), writer, critic, v)
	}

	// Require a provider key for whatever the operator actually named. There
	// are no default models, so nothing here assumes a vendor: the backend a
	// run needs is the one its ASSIGNED models imply.
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
		if vendor, model := soleAssignedCloudModel(assign); vendor != "" {
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
	// THERE IS NO DEFAULT VENDOR. This used to be a Claude special case: an
	// UNSET MODEL_BACKEND was read as "the default Claude path", so a run
	// demanded ANTHROPIC_API_KEY no matter which models the operator had
	// named. Unset does not mean Claude — it means "infer from the assigned
	// models", which the block above now does for every vendor including
	// Anthropic.
	//
	// When the models name no cloud vendor at all (local/ollama names, or a
	// name matching no prefix), nothing is demanded and FromEnv builds its
	// ollama default, which needs no key. Asking for an Anthropic key there
	// named a vendor the operator had not chosen and could not see anywhere in
	// their own command.

	// Resolve the role→backend router NOW, before opening the jail or any
	// store: a cross-vendor critic (e.g. a Gemini critic against a Claude
	// writer) needs its own vendor's key, and a missing key must refuse the
	// run here — fail closed at the top, not mid-run after jails, stores and
	// mutants are already in flight.
	meters := auditRoleMeters(assign)
	chatterFor, err := localChatterFor(assign, meters, in.localEndpoints)
	if err != nil {
		return r, auditUsageErr("%v", err)
	}

	return auditRoles{assign: assign, shadow: shadow, shadowWriter: shadowWriter, writer: writer, mutant: mutant, critic: critic, chatterFor: chatterFor, meters: meters}, nil
}

// runSubject is the FILE-and-repo half of a RunSpec: everything
// prepareAuditJail and the git lookups resolved, as distinct from the
// flag-and-model half that localAuditInput and auditRoles already carry.
// Grouping it keeps newAuditRunSpec to three parameters instead of eleven.
type runSubject struct {
	repo, commit         string
	codePath, code       string
	devTestPath, devTest string
	lang, importPath     string
}

// orArgv returns a when it has words, else b. The narrowed and the base
// command are two different things and only one caller ever sets both; this
// keeps "unset means the base IS the check command" in one place.
func orArgv(a, b []string) []string {
	if len(a) > 0 {
		return a
	}
	return b
}

// newAuditRunSpec assembles one file's RunSpec from the resolved flags, the
// resolved role models, and the prepared subject.
//
// It is a FUNCTION, not the inline literal it used to be, because the
// CLI→RunSpec seam had no test at all — and that is exactly what hid the
// challenger writer being resolved, credential-checked, written into the cache
// key and into the signed record's ModelsByRole, and then never placed on the
// RunSpec any consumer reads. Every seat model this run will use is now
// assembled in one testable place.
// normalizedConcurrency turns the workspace substrate's raw probe answer
// into the Concurrency a Verdict carries.
//
// A nil pointer, or any Trees < 1, is the ONE "not recorded" state and stays
// the zero value — it is NOT rounded up to 1. The jail substrate never sets
// in.concurrency at all (it scores in N disposable jails, not trees), so a
// normalized "1" there would print, store and SIGN a measurement nothing
// made. A real workspace measurement of one tree IS Trees 1 and is disclosed
// like any other; what is never disclosed is the absence of a measurement.
func normalizedConcurrency(d *adequacy.Disclosure) advpool.Concurrency {
	if d == nil || d.Trees < 1 {
		return advpool.Concurrency{}
	}
	// Shared rides along with the count: the dep dirs every tree links are
	// half the disclosure, and a reader told "6 trees" without them cannot
	// see the one channel between those trees.
	return advpool.Concurrency{Trees: d.Trees, Note: d.Note, Shared: d.Shared}
}

// poolDuration is what the workspace substrate spent before it could score
// anything: copying the checkout into N private trees, then probing them with
// the baseline and the canary. Both halves come from the ONE Disclosure the
// probe wrote (adequacy.WorkspacePool.Probe), so the number on the verdict is
// the number the pool actually measured, not a second derivation.
//
// nil is the jail substrate, which builds no trees and has nothing to
// disclose; a pool of one copies and probes nothing and reports zero. Either
// way the phase did not happen and the ledger stores NULL.
func poolDuration(d *adequacy.Disclosure) time.Duration {
	if d == nil {
		return 0
	}
	return d.CopyDuration + d.ProbeDuration
}

func newAuditRunSpec(in localAuditInput, roles auditRoles, subj runSubject) advpool.RunSpec {
	n := in.nMutants
	if n <= 0 {
		n = 5
	}
	return advpool.RunSpec{
		Repo: subj.repo, Commit: subj.commit, Goal: strings.TrimSpace(in.goal),
		CodePath: subj.codePath, Code: subj.code,
		DevTestPath: subj.devTestPath, DevTestCode: subj.devTest,
		// Quoted, not space-joined: TestCmd is a STRING that gets re-split
		// downstream, and a plain Join is not reversible — an argument
		// containing a space (an inline -e script, --filter="a b") comes back
		// as several arguments and the command that runs is not the one the
		// operator typed. Pairs with adequacy.ShellSplit.
		// The BASE command, never the narrowed one: the run's Selection is
		// applied downstream, per pass (advpool.DevCommand at the driver's
		// dev and shadow call sites; JailScorer.authoredCmd for the authored
		// pass), so narrowing here would narrow twice and would drop the
		// authored test from the pool's own pass. Empty baseArgv is the
		// `--local` path, where checkArgv IS the base.
		TestCmd:   adequacy.ShellJoin(orArgv(in.baseArgv, in.checkArgv)),
		Selection: in.selection,
		// Concurrency carries the workspace substrate's probe answer onto
		// the RunSpec the driver actually reads — the ONLY hop that does,
		// so every verdict, report line and ledger row can disclose how
		// many trees scored this file, or why it only got one. Normalized
		// here, not left to the reader: a nil pointer (the jail substrate,
		// which builds no trees at all) or a zero Trees (never a fact the
		// probe itself asserts) both mean "one tree, no note" — writing
		// that out as Trees 1 is what keeps a signed record from ever
		// reading "trees: 0", a claim nothing measured.
		Concurrency: normalizedConcurrency(in.concurrency),
		// The two phases the DRIVER cannot time, measured by the code that
		// ran them: the scan's one instrumented selection pass, and the
		// workspace pool's copies plus its concurrency probe. Both are zero
		// for a caller that did neither, and zero reaches the ledger as NULL
		// — see advpool.RunSpec.SelectionDuration.
		SelectionDuration: in.selectionDuration,
		PoolDuration:      poolDuration(in.concurrency),
		NMutants:          n,
		Lang:              subj.lang,
		MaxShards:         resolveMaxShards(in.maxShards),
		// Both challenger seats, from the SAME resolved struct the
		// RoleAssignment was built from — so a seat that is named, paid for and
		// recorded is also a seat the driver can actually run.
		ShadowModel:       roles.shadow,
		ShadowWriterModel: roles.shadowWriter,
		// HOW the writer attacks. The CLI's default is per-survivor; an
		// EMPTY value here means batched, which is what a caller outside the
		// CLI (the brain, a test) gets — see RunSpec.WriterMode.
		WriterMode: in.writerMode,
		Matrix:     in.matrix,
		ImportPath: subj.importPath,
		// nil = generate, exactly as every caller did before --mutants existed.
		PresetMutants: in.presetMutants,
	}
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
	prep, err := prepareAuditJail(ctx, in, plug, timeout, stdout)
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
	// --record-mutants: nil leaves the driver recording nothing, which is
	// every pre-existing caller's position.
	d.MutantSink = in.mutantSink

	// --record: the tape sink is the driver's EventSink (pool reasoning beats)
	// and is also fed the task lifecycle + findings from the drive loop below,
	// so one ordered stream is the tape. nil = no tape (rec.add is nil-safe).
	rec := in.record
	if rec != nil {
		d.Events = rec
	}
	// `certify --repo`'s position: no tape, but the scan's own event sink —
	// see localAuditInput.eventSink's doc for why the two are mutually
	// exclusive in practice.
	if in.eventSink != nil {
		d.Events = in.eventSink
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
	// The critic feed, on the same terms. Wired here for the same reason the
	// scorecard feed is: the brain was the only place CriticFindings was ever
	// attached, so on the path everyone actually runs, every critic finding was
	// computed, printed once and discarded — leaving `corral scorecard`'s
	// C-PREC column permanently empty for anyone without a brain.
	var criticRowsRecorded *int64
	if strings.TrimSpace(in.criticScoreDB) != "" {
		var closeCritic func()
		closeCritic, _, criticRowsRecorded = wireLocalCriticScore(d, in.criticScoreDB, repo, commit, stderr)
		defer closeCritic()
	}
	_ = criticRowsRecorded

	// The writer-seat correlation feed, on the same terms. Wired ONLY here
	// because `certify --local` is the only command that can set
	// RunSpec.ShadowWriterModel — and until this existed, advpool computed the
	// pair, found d.MutantAttempts nil, and threw every row away.
	var attemptRowsRecorded *int64
	if strings.TrimSpace(in.mutantAttemptsDB) != "" {
		var closeAttempts func()
		closeAttempts, _, attemptRowsRecorded = wireLocalMutantAttempts(d, in.mutantAttemptsDB, repo, commit, stderr)
		defer closeAttempts()
	}

	rs := newAuditRunSpec(in, roles, runSubject{
		repo: repo, commit: commit,
		codePath: codeKey, code: string(code),
		devTestPath: devTestKey, devTest: string(devTest),
		lang: plug.Name(), importPath: prep.importPath,
	})

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

	// PREFLIGHT, before a single model call: would the project's own test
	// command actually RUN the test this audit is going to author?
	//
	// If it would not, everything downstream still happens — mutants get
	// planted, the suite gets scored, money gets spent — and the verdict comes
	// back with proven_missed: 0 plus a note telling the operator to widen
	// their command and run the whole thing again. The check costs one jail
	// execution and no inference, so paying for it afterwards was never a
	// trade, just an ordering mistake. Refuse here, and name the fix.
	//
	// Asked about the command the AUTHORED PASS actually runs, not rs.TestCmd
	// (the base): under selection that pass NAMES the authored test's own
	// path, so a project whose discovery config would never find it collects
	// it anyway. Checking the base command refused those audits for a problem
	// the run had already solved.
	authoredArgv := scorer.AuthoredCommand(rs.CodePath, adequacy.ShellSplit(rs.TestCmd))
	if collected, cerr := scorer.AuthoredTestWouldBeCollected(ctx, rs.CodePath, authoredArgv); cerr == nil && !collected {
		return zero, auditNotCollectedErr(
			"your test command would not run the test this audit writes, so it could not prove a gap even if it found one.\n"+
				"  corral writes its killing test beside your own, as: %s\n"+
				"  Your command does not collect that file — a command naming a single test file is the usual cause.\n"+
				"  Widen it to the directory or the runner's own discovery: `-- pytest tests/` rather than `-- pytest tests/test_one.py`;\n"+
				"  `-- npx jest tests/` rather than a single spec path.\n"+
				"  Nothing was spent — this is checked before any model runs.",
			advpool.AuthoredTestPathFor(rs.CodePath, rs.DevTestPath))
	}

	verdict, err := driveLocalRun(ctx, d, q, localMissionID, chatterFor, poll, time.Sleep, stdout, rec, actorFor, swarm)
	if err != nil {
		// The meters are exercised INSIDE driveLocalRun — a seat can dispatch
		// several calls before an infrastructure failure (a closed queue, a
		// marshalling error) aborts the run partway through. That spend
		// already happened and already cost money; losing it from the
		// returned verdict is how a scan's totals would undercount a file
		// that errored after spending, not before. Stamped on the zero
		// verdict this returns — the failure state, not a fabricated success
		// — so the caller's scan-wide total (built by summing every file's
		// own ModelCalls) still includes it.
		zero.ModelCalls = modelCallsFromMeters(roles.meters)
		return zero, auditErr("%v", err)
	}
	// Carried onto the verdict from the SAME meters localChatterFor wrote
	// into — never re-derived — so the ledger and warehouse (once --record
	// or --push carry this verdict onward) match exactly what this run's
	// stdout line reports below.
	verdict.ModelCalls = modelCallsFromMeters(roles.meters)

	renderAdvVerdict(stdout, in.codePath, advVerdictFromPool(*verdict))
	renderModelSpend(stdout, verdict.ModelCalls)

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

	// Same PAST-TENSE discipline for the challenger WRITER's head-to-head, and
	// the same silence when nothing landed: a challenger that was named but
	// ended unmeasured, or a primary that never genuinely graded, writes no
	// rows PAIR-OR-NOTHING and must not be reported as if it had.
	if rs.ShadowWriterModel != "" && attemptRowsRecorded != nil {
		if n := atomic.LoadInt64(attemptRowsRecorded); n > 0 {
			fmt.Fprintf(stdout, "shadow-writer: recorded %d per-mutant outcome(s) for the two writer seats\n", n)
		}
	}

	// Hand the pool's authored test back: when it killed a survivor the dev suite
	// missed, print it so the dev can adopt it.
	if st, ok := d.RunStatus(localMissionID); ok {
		if strings.TrimSpace(st.AuthoredTest) != "" {
			fmt.Fprintf(stdout, "\nthe herd authored a test that catches a gap your suite missed — add it to %s:\n\n", tp)
			fmt.Fprintln(stdout, strings.TrimRight(st.AuthoredTest, "\n"))
		}
		// And every proven part the language's concatenator would not fold
		// into that file. Each is a test that was written, compiled and RUN
		// to kill the survivor it names, so it must reach the operator whole
		// — see reposcan.WeakFile.AuthoredExtra.
		for _, p := range st.AuthoredExtra {
			fmt.Fprintf(stdout, "\nproven test for survivor %s — a SEPARATE file, it cannot be merged with the one above:\n", p.MutantID)
			if r := strings.TrimSpace(p.Reason); r != "" {
				fmt.Fprintf(stdout, "  why: %s\n", r)
			}
			fmt.Fprintln(stdout, "")
			fmt.Fprintln(stdout, strings.TrimRight(p.Source, "\n"))
		}
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
	baseArgv       []string
	selection      lang.Selection
	bindDirFlag    []string
	noBindDepsFlag bool
	stdout         io.Writer
	seed           *repoSeed // non-nil: use this prebuilt, SHARED seed instead of building one
	substrate      string    // "" or substrateJail = the bwrap jail (today's behavior); substrateWorkspace = mutate repoDir in place, no jail
	// mutantConcurrency is how many mutants this file scores at once — on
	// the jail substrate, that many disposable jails; on the workspace
	// substrate, that many PRIVATE TREES in the pool. See
	// localAuditInput.mutantConcurrency.
	mutantConcurrency int
	// noFailFast mirrors localAuditInput.noFailFast.
	noFailFast bool
	// concurrency is localAuditInput.concurrency, threaded through: the
	// workspace branch writes the probe's answer there. nil records nothing.
	concurrency *adequacy.Disclosure
	// pool is localAuditInput.pool, threaded through: the job's one pool,
	// filled in here on first use and closed by the job's owner. nil means
	// this wiring builds and owns a pool of its own.
	pool *workspacePool
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

// workspacePool is ONE file-job's pool of private trees, threaded through
// the job the way repoSeed is threaded through a scan's language: built and
// PROBED exactly once, then shared by everything that grades that file.
//
// It has to be a box around the pointer rather than the pointer itself
// because localAuditInput travels BY VALUE from the executor down to
// buildJailWiring, so a pool constructed at the bottom could never be seen at
// the top. That mattered: the first cut built and probed one pool for the
// baseline-stability check and a SECOND for the audit, which is two copies of
// the whole checkout and four probe rounds per file where the design priced
// one — and left the printed disclosure and the recorded one free to
// disagree, since two probes of a flaky suite can answer differently.
//
// Whoever creates the box owns Close: buildJailWiring fills it in and does
// NOT hang the pool off jailWiring.cleanup, because that cleanup is released
// between the baseline and the audit. `certify --local`, which passes no box,
// keeps the old ownership (the wiring's cleanup closes the pool).
type workspacePool struct{ pool *adequacy.WorkspacePool }

// close releases the trees. Nil-safe on both levels: a jail-substrate job has
// no box, and a job that failed before wiring has a box with no pool.
func (h *workspacePool) close() {
	if h == nil || h.pool == nil {
		return
	}
	h.pool.Close()
	h.pool = nil
}

// newWorkspacePool and probeWorkspacePool are the pool's construction and
// probe seams. They exist as variables ONLY so a test can count them: "the
// pool is built once and probed once per file" is a property no assertion
// about the pool itself can see, and the duplicate-construction bug above was
// invisible to every test that looked only at the result.
var (
	newWorkspacePool = adequacy.NewWorkspacePool

	probeWorkspacePool = func(ctx context.Context, p *adequacy.WorkspacePool, base map[string]string, codePath, compliantCode string, testCmd []string) (*adequacy.WorkspacePool, adequacy.Disclosure) {
		return p.Probe(ctx, base, codePath, compliantCode, testCmd)
	}
)

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
func buildJailWiring(ctx context.Context, in jailWiringInput) (w jailWiring, err error) {
	w.cleanup = func() {}
	// If wiring fails AFTER a vendor staging dir was created, release it here —
	// the caller only defers cleanup on the success path.
	defer func() {
		if err != nil && w.cleanup != nil {
			w.cleanup()
			w.cleanup = func() {}
		}
	}()
	// WHICH TEST WAS AWAKE. The language plugin either can read its runner's
	// failure summary or it cannot; there is no middle answer, and a plugin
	// that does not implement lang.FailureParser leaves every killed_by NULL
	// rather than having an id guessed for it. Resolved ONCE here, off the
	// same in.langName every other plugin lookup in this function uses, and
	// handed to all three scorer wirings so no substrate silently drops the
	// column.
	var failureParser lang.FailureParser
	if plug, ok := lang.ByName(in.langName); ok {
		failureParser, _ = plug.(lang.FailureParser)
	}

	// A KILLED MUTANT NEEDS ONE FAILING TEST. Resolved off the same plugin,
	// in the same place, for the same reason: the flag belongs to the RUNNER
	// (see lang.FailFaster), and adequacy re-proves the healthy suite still
	// passes with it before a single mutant is graded with it. nil under
	// --no-fail-fast, which is byte-for-byte what corral always did.
	var failFast adequacy.FailFastFor
	if !in.noFailFast {
		if plug, ok := lang.ByName(in.langName); ok {
			if _, isFF := plug.(lang.FailFaster); isFF {
				failFast = func(cmd []string) ([]string, bool) { return lang.FailFastArgsFor(plug, cmd) }
			}
		}
	}

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
		// Keys first: the concurrency probe below needs the repo-relative
		// path of the file under audit, because what it PROVES is that each
		// tree runs the suite against ITS OWN copy of that file.
		//
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

		// ONE pool per file-job. The job's baseline-stability runner and its
		// audit both come through here, and building a second pool for the
		// second of them would copy the checkout twice and probe it twice —
		// paying 4N suite invocations for a design that priced 2N, and
		// letting the printed disclosure and the recorded one disagree. When
		// the caller supplied a box (localExecutor.Execute, which owns Close
		// for the whole job) the pool is built into it once and reused after.
		var pool *adequacy.WorkspacePool
		if in.pool != nil && in.pool.pool != nil {
			pool = in.pool.pool
		} else {
			// How many mutants this file may score at once — and therefore
			// how many PRIVATE TREES to copy. resolveMutantConcurrency is the
			// only place that number is decided; 1 (every caller that does
			// not set it, `certify --local` included) makes NewWorkspacePool
			// return exactly the WorkspaceRunner on the real checkout this
			// branch has always built: no copy, no probe, no behaviour
			// change.
			trees := in.mutantConcurrency
			if trees < 1 {
				trees = 1
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
				// WithTreeEnv is per-TREE and therefore only meaningful once
				// there is more than one: it gives a copy its own import path
				// (Python) and its own SHARE of the box (Go). The share is
				// DIVIDED — N trees each assuming all cores thrash the
				// machine and can fail the probe on contention alone,
				// downgrading a suite that is perfectly safe. A plugin that
				// implements no lang.TreeEnver gets no tree env, which is the
				// honest answer for a language whose toolchain neither
				// records an absolute import path nor fans out by itself.
				// A pool that downgrades to the checkout itself drops the
				// tree env entirely — see adequacy.NewWorkspacePool.
				if te, ok := plug.(lang.TreeEnver); ok && trees > 1 {
					share := runtime.NumCPU() / trees
					if share < 1 {
						share = 1
					}
					runnerOpts = append(runnerOpts, adequacy.WithTreeEnv(func(tree string) []string {
						return te.TreeEnv(tree, share)
					}))
				}
			}

			built, disc, perr := newWorkspacePool(ctx, in.repoDir, trees, in.timeout, runnerOpts...)
			if perr != nil {
				return w, perr
			}
			if verr := built.Verify(); verr != nil {
				built.Close()
				return w, verr
			}
			// THE PROBE, run before a single mutant is scored and on exactly
			// the files the scorer's own baseline uses: the unmutated subject
			// in every tree at once (does this suite survive N of itself?)
			// and adequacy.CanaryCode in every tree (does each tree import
			// its OWN copy, or did an editable install point them all back at
			// the original checkout?). Either answer being no returns a
			// ONE-tree pool on the real checkout — today's behaviour — with
			// the reason attached, never a parallel run that grades one
			// tree's mutant with another tree's suite.
			//
			// It costs two extra ROUNDS per file — the baseline in all N
			// trees at once, then the canary in all N at once, so 2N suite
			// invocations but only two suite runtimes of wall clock — which
			// is the price of not signing a kill rate the substrate cannot
			// support. A pool of one skips it entirely (Probe returns
			// immediately), so nothing that runs serially today pays for it.
			built, disc = probeWorkspacePool(ctx, built, base, w.codeKey, string(in.code), in.checkArgv)
			if in.concurrency != nil {
				*in.concurrency = disc
			}
			pool = built
			if in.pool != nil {
				// The job owns it from here: NOT hung off w.cleanup, which is
				// released between the baseline check and the audit.
				in.pool.pool = pool
			} else {
				w.cleanup = pool.Close
			}
		}

		// Concurrency is the pool's REAL tree count, read back after the
		// probe rather than the count that was asked for: a downgraded pool
		// has ONE tree, and a scorer told to run six mutants at once against
		// it would queue five of them behind the borrow channel for the whole
		// audit. The number that scores must be the number that exists.
		w.scorer = advpool.JailScorer{Jail: pool, BaseFiles: base, MutantTimeout: in.testTimeout, DevTestPath: w.devTestKey, Concurrency: pool.Trees(), Lang: in.langName, Selection: in.selection, FailureParser: failureParser, FailFast: failFast}
		w.validator = advpool.JailValidator{Jail: pool, BaseFiles: base, DevTestPath: w.devTestKey}
		w.jailEnum = advpool.JailEnumerator{Jail: pool, BaseFiles: base}
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
		jail := newRunJail(in.iso, in.timeout, depBinds)
		// enumerator backs the tests×mutants matrix's test-listing step
		// (--matrix). Wired unconditionally off the SAME backend/timeout/binds
		// as jail (bwrapJail satisfies both interfaces) — a nil
		// advpool.Driver.Enumerator makes tickMatrix always skip regardless of
		// RunSpec.Matrix, so wiring it here costs nothing when --matrix is off
		// (the flag is the real gate).
		enumerator := newRunEnumerator(in.iso, in.timeout, depBinds)
		w.scorer = advpool.JailScorer{Jail: jail, BaseFiles: repoFiles, MutantTimeout: in.testTimeout, DevTestPath: w.devTestKey, Concurrency: in.mutantConcurrency, Lang: in.langName, Selection: in.selection, FailureParser: failureParser, FailFast: failFast}
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
		jail := newRunJail(in.iso, in.timeout, nil)
		enumerator := newRunEnumerator(in.iso, in.timeout, nil)
		w.scorer = advpool.JailScorer{Jail: jail, MutantTimeout: in.testTimeout, Concurrency: in.mutantConcurrency, Lang: in.langName, Selection: in.selection, FailureParser: failureParser, FailFast: failFast}
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
		MutantsInvalid: v.MutantsInvalid,
		Survivors:      v.Survivors, ProvenMissed: v.ProvenMissed,
		ModelsByRole: v.ModelsByRole, Status: v.Status,
		RecordID: v.RecordID, RecordHead: v.RecordHead,
		RegionsTotal: v.RegionsTotal, RegionsProbed: v.RegionsProbed,
		DroppedRegions:   v.DroppedRegions,
		DuplicateMutants: v.DuplicateMutants,
		TestWriterFailed: v.TestWriterFailed,
		PoolTestUnsound:  v.PoolTestUnsound,
		BaselineFailed:   v.BaselineFailed,
		BaselineOutput:   v.BaselineOutput,
		// Field-by-field converters here have now dropped a field twice in one
		// day. Anything added to advpool.Verdict must be added here too.
		AuthoredTestNotCollected: v.AuthoredTestNotCollected,
		SuiteIgnoresFile:         v.SuiteIgnoresFile,
		TimedOut:                 v.TimedOut,
		DevScored:                v.DevScored,
		PoolScored:               v.PoolScored,
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

// renderModelSpend reports what the run actually consumed from the
// providers, broken out by role — see costLine, the one place this format is
// spelled out, shared with `corral scans show --timing`.
//
// An audit costs O(mutants x the target's suite runtime) in execution and
// O(tokens) in model calls, and until now corral reported neither half at the
// end of a run — so "what did that cost me" had no answer, from the tool whose
// central caveat is that audits are expensive.
func renderModelSpend(w io.Writer, calls []advpool.ModelCall) {
	if line := costLine(calls); line != "" {
		fmt.Fprintln(w, line)
	}
}

// noFailFastHelp is the ONE wording for the escape hatch, shared by
// `certify --local` and `certify --repo` so the two can never drift.
const noFailFastHelp = "grade every mutant with the WHOLE selected test set instead of stopping at the first failing test. " +
	"By default a killed mutant stops at the one test that killed it (pytest -x, go test -failfast, jest --bail, phpunit --stop-on-failure), " +
	"which is most of the per-mutant cost on a repo with a real suite; the verdict is identical either way, and the baseline always runs everything. " +
	"COSTS: turning this off makes each killed mutant pay for its whole selected set again — on a 77s suite that is the dominant term in the audit. " +
	"Use it only if your suite is order-dependent or flaky in a way that makes an early stop misleading."

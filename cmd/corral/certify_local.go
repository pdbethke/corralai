// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/agentworker"
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
	nMutants := fs.Int("n-mutants", 0, "how many seeded-violation mutants (default 5)")
	writerModel := fs.String("writer-model", "", "model for the test-writer role (default "+defaultLocalWriterModel+")")
	criticModel := fs.String("critic-model", "", "model for the test-critic role (default "+defaultLocalCriticModel+")")
	mutantModel := fs.String("mutant-model", "", "model for the mutant-generator role (default "+defaultLocalMutantModel+")")
	jailFlag := fs.String("jail", "", "sandbox backend: bwrap|container|sandbox-exec|none (default: auto-detect for this OS)")
	timeout := fs.Duration("timeout", 10*time.Minute, "give up if the run makes no progress for this long (not a hard wall-clock cap — a single slow LLM call can overshoot it)")
	poll := fs.Duration("poll", 2*time.Second, "how long to wait between drive iterations when nothing is claimable")
	repoFlag := fs.String("repo", "", "repository (default: git remote.origin.url, else \"local\")")
	commitFlag := fs.String("commit", "", "commit sha (default: git rev-parse HEAD, else \"local\")")
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
	assign := advpool.RoleAssignment{
		advpool.RoleMutantGenerator: mutant,
		advpool.RoleTestWriter:      writer,
		advpool.RoleTestCritic:      critic,
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

	// Read the code + dev test.
	code, err := os.ReadFile(*codePath) // #nosec G304 -- operator-supplied path to the file under review
	if err != nil {
		fmt.Fprintf(stderr, "corral certify --local: reading --code %s: %v\n", *codePath, err)
		return 2
	}
	tp := strings.TrimSpace(*testPath)
	if tp == "" {
		tp = plug.TestPath(*codePath)
	}
	devTest, err := os.ReadFile(tp) // #nosec G304 -- operator-supplied (or sibling-derived) test path
	if err != nil {
		fmt.Fprintf(stderr, "corral certify --local: reading test %s: %v (pass --test to override)\n", tp, err)
		return 2
	}

	// Resolve the jail. NEVER fall back to unsandboxed — Resolve fails closed if
	// the requested/auto backend can't isolate on this host, and so do we.
	iso, err := sandbox.Resolve(sandbox.Config{Backend: strings.TrimSpace(*jailFlag)})
	if err != nil {
		fmt.Fprintf(stderr, "corral certify --local: no working sandbox jail: %v\n", err)
		fmt.Fprintln(stderr, "  the audit runs the dev's tests against mutants inside a sandbox; corral never runs them unsandboxed.")
		fmt.Fprintln(stderr, "  try --jail container (needs docker/podman), or on Ubuntu enable unprivileged userns for bwrap.")
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

	// Build the pure driver over the REAL jail-backed scorer/validator and the
	// REAL certify-chain signer.
	d, err := advpool.NewDriver(q, advpool.JailScorer{Jail: jail}, advpool.JailValidator{Jail: jail}, assign, localCertifyThreshold)
	if err != nil {
		fmt.Fprintf(stderr, "corral certify --local: %v\n", err)
		return 1
	}
	d.Signer = advpool.CertSigner{Key: key, Store: bs, Witness: nil}
	// The wall-clock backstop: a run that hasn't converged by --timeout is signed
	// as a needs-review verdict and returned, so the CLI always gets an answer.
	d.RunDeadline = *timeout

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

	n := *nMutants
	if n <= 0 {
		n = 5
	}
	rs := advpool.RunSpec{
		Repo: repo, Commit: commit, Goal: strings.TrimSpace(*goal),
		CodePath: *codePath, Code: string(code),
		DevTestPath: tp, DevTestCode: string(devTest),
		TestCmd:  strings.Join(checkArgv, " "),
		NMutants: n,
		Lang:     plug.Name(),
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

	// The outer context bound is slightly beyond the driver's own RunDeadline so
	// the driver gets the chance to emit its signed timeout verdict before ctx
	// cancels the loop.
	ctx, cancel := context.WithTimeout(context.Background(), *timeout+30*time.Second)
	defer cancel()

	chatterFor := localChatterFor(assign)
	verdict, err := driveLocalRun(ctx, d, q, localMissionID, chatterFor, *poll, time.Sleep, stdout)
	if err != nil {
		fmt.Fprintf(stderr, "corral certify --local: %v\n", err)
		return 1
	}

	renderAdvVerdict(stdout, *codePath, advVerdictFromPool(*verdict))

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
func driveLocalRun(ctx context.Context, d *advpool.Driver, q *queue.Store, missionID int64, chatterFor func(role string) agentworker.Chatter, poll time.Duration, sleep func(time.Duration), progress io.Writer) (*advpool.Verdict, error) {
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
		ran, err := runReadyTasks(ctx, q, missionID, chatterFor)
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
func runReadyTasks(ctx context.Context, q *queue.Store, missionID int64, chatterFor func(role string) agentworker.Chatter) (bool, error) {
	ran := false
	for {
		if err := ctx.Err(); err != nil {
			return ran, err
		}
		task, err := q.ClaimNext(localBee, nil, localLeaseSeconds)
		if err != nil {
			return ran, fmt.Errorf("claiming next task: %w", err)
		}
		if task == nil {
			return ran, nil
		}
		ch := chatterFor(task.Role)
		if ch == nil {
			return ran, fmt.Errorf("no model backend for role %q", task.Role)
		}
		result, findings, rerr := agentworker.RunRole(ctx, ch, task.Role, task.Instruction)
		if rerr != nil {
			return ran, fmt.Errorf("running role %q: %w", task.Role, rerr)
		}
		for _, f := range findings {
			f.MissionID = missionID
			f.TaskID = task.ID
			f.Reporter = task.Role
			normalizeFinding(&f)
			if _, err := q.AddFinding(f); err != nil {
				return ran, fmt.Errorf("recording %q finding: %w", task.Role, err)
			}
		}
		if _, err := q.Complete(task.ID, localBee, result); err != nil {
			return ran, fmt.Errorf("completing role %q: %w", task.Role, err)
		}
		ran = true
	}
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

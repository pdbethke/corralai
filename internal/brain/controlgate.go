// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/controlgate"
	"github.com/pdbethke/corralai/internal/controlspec"
	"github.com/pdbethke/corralai/internal/gate"
)

// fileReader reads a checked-out file's content. repo.Engine satisfies it
// (ReadFile(dir, path)); tests inject a fake.
type fileReader interface {
	ReadFile(dir, path string) (string, error)
}

// controlCertMaxAttempts bounds how many consecutive times a single PR head may
// re-run because signing (certify) failed before the runner gives up and posts a
// terminal `error`. Without a cap, a PERSISTENT signing misconfiguration would
// re-checkout + re-run every vetted test in the jail on every poll forever while
// the check sat stuck at `pending` — real compute burn for no progress. A few
// retries still absorb a transient signing blip.
const controlCertMaxAttempts = 3

// controlRunner gates one PR head against a control owner's VETTED tests:
// checkout head → ListVetted(owner) → read each target's head content → run
// the vetted tests in the jail → sign + post corral/control-gate.
//
// FAIL-CLOSED (under test): success is posted ONLY on an all-pass verdict that
// was signed first. Missing target → that control fails; zero vetted → failure;
// jail/checkout error → non-success (unsigned); certify error → nothing posted
// and the SHA is NOT recorded, so the poll RETRIES — but only up to
// MaxCertAttempts times, after which a persistent signing failure is terminal
// (`error` posted + SHA recorded) rather than an unbounded re-run loop.
type controlRunner struct {
	byRepo   map[string]string // repo ("owner/name") -> control-owner principal
	Base     map[string]string // the workspace scaffold (from langScaffold)
	TestCmd  []string          // the test command (from langScaffold)
	Checkout gate.Checkouter
	Reader   fileReader
	Cert     controlgate.Certifier
	Status   gate.StatusPoster
	Spec     *controlspec.Store
	Jail     adequacy.Jail
	RunStore *gate.Store
	Record   func(repo, sha string) string
	Now      func() time.Time
	// attempts counts consecutive certify failures per "repo@sha" so the runner
	// can give up after MaxCertAttempts. It is in-memory only (reset on brain
	// restart — acceptable: rare, still bounded per process, and a restart may
	// itself fix the signing config). Accessed only from the single poller
	// goroutine, so it needs no lock.
	attempts        map[string]int
	MaxCertAttempts int
}

func (r *controlRunner) Run(ctx context.Context, repoURL string, p gate.Policy, pr gate.PRRef) error {
	owner, ok := r.byRepo[p.Repo]
	if !ok {
		return fmt.Errorf("controlgate: no control policy for repo %q", p.Repo)
	}
	target := r.Record(p.Repo, pr.HeadSHA)
	_ = r.Status.SetCommitStatus(ctx, repoURL, pr.HeadSHA, p.Context, "pending", target, "corral control-gate running")

	dest, err := os.MkdirTemp("", "corral-control-")
	if err != nil {
		return r.fail(ctx, repoURL, p, pr, target, "error", "workspace: "+err.Error())
	}
	defer func() { _ = os.RemoveAll(dest) }()

	if err := r.Checkout.CheckoutPR(ctx, repoURL, pr.Number, pr.HeadSHA, dest); err != nil {
		return r.fail(ctx, repoURL, p, pr, target, "error", "checkout: "+err.Error())
	}

	vetted, err := r.Spec.ListVetted(owner)
	if err != nil {
		return r.fail(ctx, repoURL, p, pr, target, "error", "list vetted: "+err.Error())
	}
	if len(vetted) == 0 {
		return r.fail(ctx, repoURL, p, pr, target, "failure", "no vetted controls for "+owner)
	}

	var checks []controlgate.ControlCheck
	var missing []controlgate.ControlTestResult
	for _, gt := range vetted {
		head, err := r.Reader.ReadFile(dest, gt.Target)
		if err != nil {
			// Target absent/unreadable at head → fail-closed: this control fails.
			missing = append(missing, controlgate.ControlTestResult{Goal: gt.Goal, Target: gt.Target, Passed: false})
			continue
		}
		checks = append(checks, controlgate.ControlCheck{Test: gt, HeadCode: head, CodePath: gt.CodePath, TestPath: gt.TestPath})
	}

	res, err := controlgate.RunControlGate(ctx, r.Jail, r.Base, checks, r.TestCmd)
	if err != nil {
		return r.fail(ctx, repoURL, p, pr, target, "error", "jail: "+err.Error())
	}
	res.Results = append(res.Results, missing...)
	if len(missing) > 0 {
		res.Pass = false
	}

	req := controlgate.PostRequest{
		RepoURL:   repoURL,
		HeadSHA:   pr.HeadSHA,
		Context:   p.Context,
		RecordURL: func(sha string) string { return r.Record(p.Repo, sha) },
	}
	key := p.Repo + "@" + pr.HeadSHA
	if err := controlgate.PostControlGate(ctx, r.Cert, r.Status, req, res); err != nil {
		// No unsigned green: certify failed, nothing was posted. Below the cap,
		// do NOT record the SHA — leave it for the next poll to retry (a transient
		// signing blip self-heals). At the cap, a persistent failure is terminal:
		// post `error` and record the SHA so we stop re-running the jail forever.
		r.attempts[key]++
		if r.attempts[key] < r.MaxCertAttempts {
			return err
		}
		delete(r.attempts, key)
		return r.fail(ctx, repoURL, p, pr, target, "error", "sign (gave up after retries): "+err.Error())
	}
	delete(r.attempts, key) // signed OK — clear any prior transient-failure count
	if err := r.RunStore.Save(gate.Run{Repo: p.Repo, HeadSHA: pr.HeadSHA, PR: pr.Number, Passed: res.Pass, RecordID: 0, RanAt: r.Now()}); err != nil {
		log.Printf("control-gate: save dedupe %s@%s: %v", p.Repo, pr.HeadSHA, err)
	}
	return nil
}

// controlSpecStore returns the controlspec store StartControlGate + the control
// tools share. If opts.ControlSpec is set it is reused (owns=false — the caller
// owns its lifetime); otherwise a new store is opened at opts.ControlSpecDB
// (owns=true — StartControlGate must close it on a later error).
func controlSpecStore(opts Options) (store *controlspec.Store, owns bool, err error) {
	if opts.ControlSpec != nil {
		return opts.ControlSpec, false, nil
	}
	dsn := opts.ControlSpecDB
	if dsn == "" {
		dsn = "corralai_control_spec.duckdb"
	}
	s, err := controlspec.OpenStore(dsn)
	return s, true, err
}

// StartControlGate wires and starts the control gate: the controlspec store
// (vetted tests), a separate gate.Store (SHA dedup, distinct from the merge
// gate so their dedup keys never collide), an adequacy jail over the shared
// backend, and a gate.Poller driving controlRunner. Off switches mirror
// StartGate: empty ControlPolicies → (nil,nil,nil); nil GateBackend or nil
// Repo → logged (nil,nil,nil). Returns the two opened stores.
func StartControlGate(ctx context.Context, opts Options) (*gate.Store, *controlspec.Store, error) {
	if len(opts.ControlPolicies) == 0 {
		return nil, nil, nil
	}
	if opts.GateBackend == nil {
		log.Printf("control-gate: DISABLED — CORRALAI_CONTROL_GATE is set (%d polic(ies)) but no sandbox backend; refusing to run PR tests unsandboxed (set CORRALAI_GATE_EXEC_BACKEND)", len(opts.ControlPolicies))
		return nil, nil, nil
	}
	if opts.Repo == nil {
		log.Printf("control-gate: DISABLED — CORRALAI_CONTROL_GATE is set but no repo.Engine is configured (Options.Repo is nil)")
		return nil, nil, nil
	}

	spec, ownsSpec, err := controlSpecStore(opts)
	if err != nil {
		return nil, nil, fmt.Errorf("control-gate: open spec store: %w", err)
	}
	runDSN := opts.ControlGateDB
	if runDSN == "" {
		runDSN = "corralai_control_gate.duckdb"
	}
	runStore, err := gate.OpenStore(runDSN)
	if err != nil {
		if ownsSpec {
			_ = spec.Close()
		}
		return nil, nil, fmt.Errorf("control-gate: open run store: %w", err)
	}

	record := opts.GateRecordURL
	if record == nil {
		record = defaultGateRecordURL
	}

	// v1 assumes one language/scaffold per brain (all control policies share it);
	// the first policy's lang selects it. langScaffold validated every policy's
	// lang at parse time, so this cannot be !ok for a policy that got this far.
	base, testCmd, _ := controlgate.LangScaffold(opts.ControlPolicies[0].Lang)

	byRepo := make(map[string]string, len(opts.ControlPolicies))
	var policies []gate.Policy
	for _, cp := range opts.ControlPolicies {
		byRepo[cp.Repo] = cp.Owner
		var bases []string
		if cp.Base != "" {
			bases = []string{cp.Base}
		}
		policies = append(policies, gate.Policy{Repo: cp.Repo, Base: bases, Context: "corral/control-gate"})
	}

	runner := &controlRunner{
		byRepo:          byRepo,
		Base:            base,
		TestCmd:         testCmd,
		Checkout:        opts.Repo,
		Reader:          opts.Repo,
		Cert:            certifierAdapter{opts: opts},
		Status:          opts.Repo,
		Spec:            spec,
		Jail:            adequacy.NewJail(opts.GateBackend, gate.DefaultGateTimeout),
		RunStore:        runStore,
		Record:          record,
		Now:             time.Now,
		attempts:        make(map[string]int),
		MaxCertAttempts: controlCertMaxAttempts,
	}

	interval := opts.ControlPollInterval
	if interval <= 0 {
		interval = 2 * time.Minute
	}
	poller := &gate.Poller{Policies: policies, List: opts.Repo, Store: runStore, Run: runner.Run, Interval: interval}
	log.Printf("control-gate: ENABLED — %d polic(ies), polling every %s", len(policies), interval)
	owners := make([]string, 0, len(opts.ControlPolicies))
	for _, cp := range opts.ControlPolicies {
		owners = append(owners, cp.Owner)
	}
	log.Printf("control-gate: owner(s) %v — these MUST match the authenticated principals that author controls", owners)
	go poller.Loop(ctx)
	return runStore, spec, nil
}

// fail posts a non-success status (unsigned — a failure needs no signature)
// and records the SHA so the poller doesn't re-run it. Delegates to
// gate.FailClosed so this stays byte-identical to gate.Runner.fail.
func (r *controlRunner) fail(ctx context.Context, repoURL string, p gate.Policy, pr gate.PRRef, target, state, msg string) error {
	return gate.FailClosed(ctx, r.RunStore, r.Status, repoURL, p.Repo, pr, p.Context, target, state, msg, r.Now)
}

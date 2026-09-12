// SPDX-License-Identifier: Elastic-2.0

package gate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"os"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/repo"
)

// PRRef is a local alias for repo.PRRef so callers of this package don't
// need to import internal/repo just to build a PRRef literal.
type PRRef = repo.PRRef

// Checkouter checks out an untrusted PR head at an exact, verified sha into
// destDir. Implemented in production by repo.Engine.CheckoutPR; defined
// here (rather than imported as an interface from repo) so the brain's
// concrete adapters can satisfy it without an import cycle.
type Checkouter interface {
	CheckoutPR(ctx context.Context, repoURL string, pr int, sha, destDir string) error
}

// Jail runs command against workspace inside the bwrap sandbox — the only
// place untrusted PR code is ever executed. network gates whether the jail
// gets network access (per Policy.AllowNet). timeout is the hard deadline
// the jail kills the process past (per Policy.TimeoutS, or
// DefaultGateTimeout — see Runner.Run) — without it, a policy has no way to
// override the sandbox package's own 60s default, which times out any real
// test suite and permanently blocks merge.
type Jail interface {
	Run(ctx context.Context, command, workspace string, network bool, timeout time.Duration) (exitCode int, output string, err error)
}

// Certifier signs a gate run's outcome via the Task-3 certify seam
// (brain.certifyBuild), returning the signed record's id and the resulting
// chain head.
type Certifier interface {
	Certify(ctx context.Context, repo, commit, command string, exitCode int, outputDigest string) (recordID int64, head string, err error)
}

// StatusPoster posts a commit status (pending/success/failure/error) back
// to the forge hosting repoURL.
type StatusPoster interface {
	SetCommitStatus(ctx context.Context, repoURL, sha, context, state, targetURL, description string) error
}

// Runner gates one PR head at a time: checkout the exact PR head commit,
// run the repo's declared check inside the jail, sign the outcome, store
// the dedupe/index row, and post the resulting commit status.
//
// FAIL-CLOSED invariant (load-bearing, under test): Run posts commit status
// "success" ONLY when the jail check truly executed and exited 0 AND every
// prior step (checkout, sign, store) succeeded. Every internal error path
// posts "failure" or "error" and stores Passed=false — NEVER "success".
type Runner struct {
	Checkout Checkouter
	Jail     Jail
	Certify  Certifier
	Status   StatusPoster
	Store    *Store
	// RecordURL builds the status target_url for a (repo, sha) -> the
	// /api/gate/run link (Task 5 wires the real endpoint).
	RecordURL func(repo, sha string) string
	// Now is an injected clock; the runner never calls time.Now() itself so
	// RanAt stays deterministic under test.
	Now func() time.Time
}

// Run gates one PR head. Fail-closed: success is posted ONLY on a real exit-0.
func (r *Runner) Run(ctx context.Context, repoURL string, p Policy, pr PRRef) error {
	// NORMALIZE THE POLICY FIRST, so every later use — the status posts, the
	// store row, the fail-closed path — reads the SAME values.
	//
	// Two defaults had been applied only in the places that happened to need
	// them: Store.Save substituted "corral/gate" for an empty Context while
	// Run posted the empty string straight to the forge (which then files it
	// under its own default context), so the status and the dedupe row
	// disagreed about which check had reported; and the timeout bound added to
	// ParsePolicies did nothing for a Policy built programmatically, leaving
	// the int64 overflow reachable here. Both are the same mistake: a rule
	// applied at the door that PARSES a policy and not at the door that ACTS
	// on one. (Cold review round three, 2026-09-12, R4 and R5.)
	p = p.normalized()

	target := r.RecordURL(p.Repo, pr.HeadSHA)
	_ = r.Status.SetCommitStatus(ctx, repoURL, pr.HeadSHA, p.Context, "pending", target, "corral gate running")

	// A POLICY WITH NO COMMAND MUST NEVER PASS. ParsePolicies refuses one,
	// but a Policy built programmatically (brain Options.GatePolicies) is not
	// parsed: an empty CheckCmd reached the jail as `sh -c ""`, which exits 0,
	// and the gate posted "success" for a check that ran nothing — a green on
	// a question nobody asked. The rule belongs at the door that ACTS on the
	// policy, not only at the one that parses it.
	// (Cold review 2026-09-12, R6.)
	if len(p.CheckCmd) == 0 || strings.TrimSpace(strings.Join(p.CheckCmd, " ")) == "" {
		return r.fail(ctx, repoURL, p, pr, target, "error", "policy has no check command — refusing to report a result for a check that would run nothing")
	}

	dest, err := os.MkdirTemp("", "corral-gate-")
	if err != nil {
		return r.fail(ctx, repoURL, p, pr, target, "error", "workspace: "+err.Error())
	}
	defer func() { _ = os.RemoveAll(dest) }()

	if err := r.Checkout.CheckoutPR(ctx, repoURL, pr.Number, pr.HeadSHA, dest); err != nil {
		return r.fail(ctx, repoURL, p, pr, target, "error", "checkout: "+err.Error())
	}

	timeout := p.effectiveTimeout()
	exit, output, runErr := r.Jail.Run(ctx, strings.Join(p.CheckCmd, " "), dest, p.AllowNet, timeout)
	if runErr != nil {
		return r.fail(ctx, repoURL, p, pr, target, "error", "jail: "+runErr.Error())
	}
	sum := sha256.Sum256([]byte(output))
	digest := "sha256:" + hex.EncodeToString(sum[:])

	recordID, _, certErr := r.Certify.Certify(ctx, p.Repo, pr.HeadSHA, strings.Join(p.CheckCmd, " "), exit, digest)
	if certErr != nil {
		return r.fail(ctx, repoURL, p, pr, target, "error", "sign: "+certErr.Error())
	}

	passed := exit == 0
	// A FAILED Save USED TO BE LOGGED AND IGNORED, and then "success" was
	// posted anyway — contradicting this type's own documented invariant
	// ("success ONLY when checkout, sign AND store all succeeded"). Worse, with
	// no dedupe row the poller re-ran the jail and re-certified on every tick,
	// appending a new signed record each time, forever, while the status
	// target_url pointed at a record that could not be looked up.
	//
	// So a store failure is now a fail-closed exit like any other. That does
	// mean a full disk blocks a merge whose tests actually passed — which is
	// the correct direction for a REQUIRED check, and is what the invariant
	// above already promised. (Cold review 2026-09-12, R1 — reproduced.)
	if err := r.Store.Save(Run{Repo: p.Repo, HeadSHA: pr.HeadSHA, PR: pr.Number, Passed: passed, Context: p.Context, RecordID: recordID, RanAt: r.Now()}); err != nil {
		log.Printf("gate: save dedupe %s@%s: %v", p.Repo, pr.HeadSHA, err)
		return r.fail(ctx, repoURL, p, pr, target, "error", "store: "+err.Error()+" — the run is not recorded, so its result is not reported")
	}
	state := "failure"
	if passed {
		state = "success"
	}
	// R4: the verdict is delivered BEFORE the row is marked delivered, and the
	// row is only marked once the forge has it — see Store.MarkPosted.
	if err := r.Status.SetCommitStatus(ctx, repoURL, pr.HeadSHA, p.Context, state, target, gateDesc(passed)); err != nil {
		return err
	}
	if err := r.Store.MarkPosted(p.Repo, pr.HeadSHA, p.Context); err != nil {
		log.Printf("gate: marking %s@%s delivered: %v", p.Repo, pr.HeadSHA, err)
	}
	return nil
}

// fail is the single path for every internal-error exit: it always stores
// Passed=false and always posts a non-success state, which is what keeps
// the fail-closed invariant a structural property of Run rather than
// something each call site has to remember to uphold. Delegates to
// FailClosed so the merge runner and the control runner (internal/brain's
// controlRunner.fail) can never drift on this invariant.
func (r *Runner) fail(ctx context.Context, repoURL string, p Policy, pr PRRef, target, state, msg string) error {
	return FailClosed(ctx, r.Store, r.Status, repoURL, p.Repo, pr, p.Context, target, state, msg, r.Now)
}

// gateDesc renders the short commit-status description for a gate outcome.
func gateDesc(passed bool) string {
	if passed {
		return "corral gate passed"
	}
	return "corral gate failed"
}

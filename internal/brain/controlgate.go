// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"fmt"
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

// controlRunner gates one PR head against a control owner's VETTED tests:
// checkout head → ListVetted(owner) → read each target's head content → run
// the vetted tests in the jail → sign + post corral/control-gate.
//
// FAIL-CLOSED (under test): success is posted ONLY on an all-pass verdict that
// was signed first. Missing target → that control fails; zero vetted → failure;
// jail/checkout error → non-success (unsigned); certify error → nothing posted
// and the SHA is NOT recorded (retried next poll).
type controlRunner struct {
	byRepo   map[string]string // repo ("owner/name") -> control-owner principal
	Base     map[string]string // the workspace scaffold (from langScaffold)
	TestCmd  []string          // the test command (from langScaffold)
	Checkout gate.Checkouter
	Reader   fileReader
	Cert     controlgate.Certifier
	Status   controlgate.StatusPoster
	Spec     *controlspec.Store
	Jail     adequacy.Jail
	RunStore *gate.Store
	Record   func(repo, sha string) string
	Now      func() time.Time
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
	if err := controlgate.PostControlGate(ctx, r.Cert, r.Status, req, res); err != nil {
		// No unsigned green: certify failed, nothing was posted. Do NOT record
		// the SHA — leave it for the next poll to retry.
		return err
	}
	_ = r.RunStore.Save(gate.Run{Repo: p.Repo, HeadSHA: pr.HeadSHA, PR: pr.Number, Passed: res.Pass, RecordID: 0, RanAt: r.Now()})
	return nil
}

// fail posts a non-success status (unsigned — a failure needs no signature)
// and records the SHA so the poller doesn't re-run it. Mirrors gate.Runner.fail.
func (r *controlRunner) fail(ctx context.Context, repoURL string, p gate.Policy, pr gate.PRRef, target, state, msg string) error {
	_ = r.RunStore.Save(gate.Run{Repo: p.Repo, HeadSHA: pr.HeadSHA, PR: pr.Number, Passed: false, RanAt: r.Now()})
	return r.Status.SetCommitStatus(ctx, repoURL, pr.HeadSHA, p.Context, state, target, msg)
}

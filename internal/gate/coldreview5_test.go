// SPDX-License-Identifier: Elastic-2.0

package gate

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A review of main at 6951ca4c, 2026-09-15 (reviewer claude-code:
// claude-fable-5-1, verifier codex:gpt-6-astra), entry 8be2189163b0 on the
// ledger. R1 and R2 are here; R3–R7 are still open.

// TestTwoPoliciesCannotShareOneStatus is R1 (high).
//
// THE DEFECT: two policies for one repo that both omitted context= were both
// accepted and both normalized to corral/gate. The poller keys its dedupe on
// (repo, head, context), so the policy whose variable sorted first ran and
// the second was skipped on every head, forever — and the first one's status
// was posted under the shared context, standing in for a check that never
// ran. Two policies that would answer the same pull request under the same
// status are now refused at parse time, loudly, instead of one of them
// disappearing.
func TestTwoPoliciesCannotShareOneStatus(t *testing.T) {
	env := []string{
		"CORRALAI_GATE_POLICY_A=repo=o/r,base=main,cmd=go test ./...",
		"CORRALAI_GATE_POLICY_B=repo=o/r,base=main,cmd=go vet ./...",
	}
	pols, bad := ParsePolicyEnv(env)
	if len(pols) != 1 || pols[0].CheckCmd != "go test ./..." {
		t.Fatalf("policies = %+v: want only A, the first by name", pols)
	}
	if len(bad) != 1 || !strings.Contains(bad[0], "CORRALAI_GATE_POLICY_B") || !strings.Contains(bad[0], "CORRALAI_GATE_POLICY_A") || !strings.Contains(bad[0], "context=") {
		t.Fatalf("bad = %q: want B refused, naming A and telling the operator to set context=", bad)
	}
}

// The collision is a SAME-PULL-REQUEST collision. Policies on one repo whose
// bases cannot both match a pull request, or that report under distinct
// contexts, never share a status and stay legal.
func TestPoliciesThatCannotAnswerTheSamePullRequestAreFine(t *testing.T) {
	for name, env := range map[string][]string{
		"disjoint bases": {
			"CORRALAI_GATE_POLICY_A=repo=o/r,base=main,cmd=true",
			"CORRALAI_GATE_POLICY_B=repo=o/r,base=release,cmd=true",
		},
		"distinct contexts": {
			"CORRALAI_GATE_POLICY_A=repo=o/r,base=main,cmd=true",
			"CORRALAI_GATE_POLICY_B=repo=o/r,base=main,context=corral/vet,cmd=true",
		},
		"different repos": {
			"CORRALAI_GATE_POLICY_A=repo=o/r,cmd=true",
			"CORRALAI_GATE_POLICY_B=repo=o/s,cmd=true",
		},
	} {
		t.Run(name, func(t *testing.T) {
			pols, bad := ParsePolicyEnv(env)
			if len(pols) != 2 || len(bad) != 0 {
				t.Fatalf("policies=%d bad=%q: want both accepted", len(pols), bad)
			}
		})
	}
	// An unset base means ALL bases, so it overlaps any named base.
	_, bad := ParsePolicyEnv([]string{
		"CORRALAI_GATE_POLICY_A=repo=o/r,cmd=true",
		"CORRALAI_GATE_POLICY_B=repo=o/r,base=release,cmd=true",
	})
	if len(bad) != 1 {
		t.Fatalf("bad = %q: a policy on every base and one on release both answer a release PR", bad)
	}
}

// TestASignedVerdictIsRedeliveredNotRecertified is R2.
//
// THE DEFECT: a row whose status never reached the forge was "retried" by
// calling Run again — a new checkout, a new jail run, and a NEW SIGNED
// RECORD on every tick for as long as the post failed. A permanent refusal
// (403 without the statuses scope, 422 on the context) became an unbounded
// re-certify loop. A row that already carries a signed record now has its
// verdict RE-POSTED and nothing re-run. A row with no record (a fail-closed
// run cut short by shutdown, which is what the retry exists for) is still
// re-run, exactly as before.
func TestASignedVerdictIsRedeliveredNotRecertified(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Save(Run{Repo: "o/r", HeadSHA: "abc", PR: 1, Passed: true, RecordID: 42,
		Context: "corral/gate", StatusPosted: false, RanAt: time.Unix(0, 0)}); err != nil {
		t.Fatal(err)
	}

	runs, redelivered := 0, 0
	p := &Poller{
		Policies: []Policy{{Repo: "o/r", Base: []string{"main"}, Context: "corral/gate", CheckCmd: "true"}},
		List:     &fakeLister{prs: []PRRef{{Number: 1, HeadSHA: "abc", Base: "main"}}},
		Store:    store,
		Run: func(ctx context.Context, repoURL string, pol Policy, pr PRRef) error {
			runs++
			return nil
		},
		Redeliver: func(ctx context.Context, repoURL string, pol Policy, pr PRRef, prev Run) error {
			redelivered++
			if prev.RecordID != 42 || !prev.Passed {
				t.Errorf("Redeliver got %+v: want the stored signed verdict", prev)
			}
			return errors.New("forge still refuses") // a permanent 403
		},
	}
	for i := 0; i < 3; i++ {
		_ = p.Tick(context.Background())
	}
	if runs != 0 {
		t.Fatalf("Run called %d times: a signed verdict must never be re-run and re-signed to deliver it", runs)
	}
	if redelivered != 3 {
		t.Fatalf("Redeliver called %d times, want 3 (once per tick while the forge refuses)", redelivered)
	}

	// With no Redeliver wired, a signed verdict is still never re-run.
	p.Redeliver = nil
	_ = p.Tick(context.Background())
	if runs != 0 {
		t.Fatalf("Run called with no Redeliver wired: a signed verdict must never be re-signed")
	}
}

// Runner.Redeliver posts the STORED verdict and marks it delivered; it runs
// nothing and signs nothing.
func TestRunnerRedeliverPostsTheStoredVerdict(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	prev := Run{Repo: "o/r", HeadSHA: "abc", PR: 1, Passed: false, RecordID: 9,
		Context: "corral/gate", StatusPosted: false, RanAt: time.Unix(0, 0)}
	if err := store.Save(prev); err != nil {
		t.Fatal(err)
	}
	status := &fakeStatusPoster{}
	cert := &fakeCertifier{}
	r := &Runner{Status: status, Store: store, Certify: cert,
		RecordURL: func(repo, sha string) string { return "/r/" + sha }, Now: func() time.Time { return time.Unix(1, 0) }}

	if err := r.Redeliver(context.Background(), "https://github.com/o/r", testPolicy(), PRRef{Number: 1, HeadSHA: "abc", Base: "main"}, prev); err != nil {
		t.Fatal(err)
	}
	if len(status.states) != 1 || status.states[0] != "failure" {
		t.Fatalf("posted %v: want exactly the stored verdict, failure", status.states)
	}
	if cert.calls != 0 {
		t.Fatalf("Redeliver signed %d record(s); it must sign none", cert.calls)
	}
	got, ok, err := store.GetByHead("o/r", "abc", "corral/gate")
	if err != nil || !ok || !got.StatusPosted {
		t.Fatalf("row after redelivery = %+v ok=%v err=%v: want StatusPosted", got, ok, err)
	}
}

// R1's rule at the door that ACTS on policies. A Policy built
// programmatically (brain Options.GatePolicies) never passes through
// ParsePolicyEnv, so the poller holds the same rule with the same function:
// the second of two colliding policies is skipped loudly, and the first is
// the one that runs.
func TestThePollerHoldsTheSharedStatusRuleToo(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	var ran []string
	p := &Poller{
		Policies: []Policy{
			{Repo: "o/r", Base: []string{"main"}, CheckCmd: "first"},
			{Repo: "o/r", Base: []string{"main"}, CheckCmd: "second"},
		},
		List:  &fakeLister{prs: []PRRef{{Number: 1, HeadSHA: "abc", Base: "main"}}},
		Store: store,
		Run: func(ctx context.Context, repoURL string, pol Policy, pr PRRef) error {
			ran = append(ran, pol.CheckCmd)
			return nil
		},
	}
	_ = p.Tick(context.Background())
	if strings.Join(ran, ",") != "first" {
		t.Fatalf("ran %v: want only the first of two policies sharing one status", ran)
	}
}

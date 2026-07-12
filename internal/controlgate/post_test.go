// SPDX-License-Identifier: Elastic-2.0

package controlgate

import (
	"context"
	"errors"
	"testing"
)

type fakeCert struct {
	gotCommand string
	gotExit    int
	gotDigest  string
	err        error
}

func (f *fakeCert) Certify(_ context.Context, repo, commit, command string, exit int, digest string) (int64, string, error) {
	f.gotCommand, f.gotExit, f.gotDigest = command, exit, digest
	return 7, "head7", f.err
}

type fakePoster struct {
	called                                 bool
	repoURL, sha, ctx, state, target, desc string
}

func (f *fakePoster) SetCommitStatus(_ context.Context, repoURL, sha, context, state, target, desc string) error {
	f.called = true
	f.repoURL, f.sha, f.ctx, f.state, f.target, f.desc = repoURL, sha, context, state, target, desc
	return nil
}

func TestPostControlGate(t *testing.T) {
	req := PostRequest{RepoURL: "github.com/o/r", HeadSHA: "abc", Context: "corral/control-gate",
		RecordURL: func(sha string) string { return "https://brain/rec/" + sha }}

	// PASS → success, exit 0, status posted with the description + record URL.
	cert, poster := &fakeCert{}, &fakePoster{}
	passRes := ControlResult{Pass: true, Results: []ControlTestResult{{Goal: "g1", Target: "t1", Passed: true}}}
	if err := PostControlGate(context.Background(), cert, poster, req, passRes); err != nil {
		t.Fatal(err)
	}
	if cert.gotExit != 0 || cert.gotDigest == "" {
		t.Errorf("certify args: exit=%d digest=%q", cert.gotExit, cert.gotDigest)
	}
	if poster.state != "success" || poster.target != "https://brain/rec/abc" || poster.desc != "all 1 controls passed" || poster.ctx != "corral/control-gate" {
		t.Errorf("posted: %+v", poster)
	}

	// FAIL → failure, exit 1.
	cert2, poster2 := &fakeCert{}, &fakePoster{}
	failRes := ControlResult{Pass: false, Results: []ControlTestResult{{Goal: "g1", Target: "t1", Passed: false}}}
	_ = PostControlGate(context.Background(), cert2, poster2, req, failRes)
	if cert2.gotExit != 1 || poster2.state != "failure" {
		t.Errorf("fail path: exit=%d state=%q", cert2.gotExit, poster2.state)
	}

	// NO UNSIGNED GREEN: a certify error → error returned AND status NOT posted.
	cert3, poster3 := &fakeCert{err: errors.New("sign failed")}, &fakePoster{}
	if err := PostControlGate(context.Background(), cert3, poster3, req, passRes); err == nil {
		t.Fatal("certify error must return an error")
	}
	if poster3.called {
		t.Fatal("must NOT post a status when signing failed (no unsigned green)")
	}
}

func TestDescribeResult(t *testing.T) {
	if got := describeResult(ControlResult{}); got != "no controls apply" {
		t.Errorf("empty: %q", got)
	}
	allPass := ControlResult{Pass: true, Results: []ControlTestResult{{Goal: "g1", Target: "t1", Passed: true}, {Goal: "g2", Target: "t2", Passed: true}}}
	if got := describeResult(allPass); got != "all 2 controls passed" {
		t.Errorf("all-pass: %q", got)
	}
	oneFail := ControlResult{Pass: false, Results: []ControlTestResult{{Goal: "g1", Target: "t1", Passed: true}, {Goal: "g2", Target: "t2", Passed: false}}}
	if got := describeResult(oneFail); got != "1/2 controls FAILED: g2@t2" {
		t.Errorf("one-fail: %q", got)
	}
}

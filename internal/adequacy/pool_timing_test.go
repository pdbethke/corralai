// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"testing"
	"time"
)

// TestPoolDisclosesCopyAndProbeDurations: the workspace substrate pays for
// its parallelism twice before a single mutant is scored — once to copy the
// checkout N times, once to run the baseline and the canary in every tree at
// once. Both were unmeasured, so a file whose audit spent minutes on setup
// reported only its dev pass and the minutes were invisible.
//
// A pool of ONE copies nothing and probes nothing, and must say so with a
// zero the ledger stores as NULL — never a fabricated cost for work that did
// not happen.
func TestPoolDisclosesCopyAndProbeDurations(t *testing.T) {
	root := newGitRepo(t, map[string]string{"a.py": "x=0\n"}, nil)
	cmd := []string{"sh", "-c", `[ "$(cat a.py)" = "x=0" ]`} // passes compliant, fails the canary

	p, d, err := NewWorkspacePool(context.Background(), root, 2, time.Minute)
	if err != nil {
		t.Fatalf("NewWorkspacePool: %v", err)
	}
	defer p.Close()
	if d.Trees != 2 {
		t.Fatalf("disclosure = %+v, want a pool of 2 (the fixture must be a git checkout)", d)
	}
	if d.CopyDuration <= 0 {
		t.Errorf("CopyDuration = %v — copying the checkout twice is not free and must be measured", d.CopyDuration)
	}
	if d.ProbeDuration != 0 {
		t.Errorf("CopyDuration's disclosure reports ProbeDuration %v before Probe ever ran", d.ProbeDuration)
	}

	_, pd := p.Probe(context.Background(), nil, "a.py", "x=0\n", cmd)
	if pd.Trees != 2 {
		t.Fatalf("probe disclosure = %+v, want the healthy 2-tree answer", pd)
	}
	if pd.ProbeDuration <= 0 {
		t.Errorf("ProbeDuration = %v — the probe runs the suite 2N times and must be measured", pd.ProbeDuration)
	}
	if pd.CopyDuration != d.CopyDuration {
		t.Errorf("the probe's disclosure lost the copy it was built on: %v vs %v", pd.CopyDuration, d.CopyDuration)
	}
}

func TestPoolOfOneReportsNoCopyOrProbeTime(t *testing.T) {
	root := newGitRepo(t, map[string]string{"a.py": "x=0\n"}, nil)
	p, d, err := NewWorkspacePool(context.Background(), root, 1, time.Minute)
	if err != nil {
		t.Fatalf("NewWorkspacePool: %v", err)
	}
	defer p.Close()
	if d.CopyDuration != 0 || d.ProbeDuration != 0 {
		t.Fatalf("a pool of one disclosed copy=%v probe=%v; it copies nothing and probes nothing", d.CopyDuration, d.ProbeDuration)
	}
	_, pd := p.Probe(context.Background(), nil, "a.py", "x=0\n", []string{"true"})
	if pd.CopyDuration != 0 || pd.ProbeDuration != 0 {
		t.Fatalf("a pool of one's probe disclosed copy=%v probe=%v; Probe returns immediately", pd.CopyDuration, pd.ProbeDuration)
	}
}

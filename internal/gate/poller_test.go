// SPDX-License-Identifier: Elastic-2.0

package gate

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// fakeLister is a PRLister test double returning a fixed set of open PRs
// regardless of the (repoURL, base) it's asked about.
type fakeLister struct {
	prs   []PRRef
	err   error
	calls int
}

func (f *fakeLister) ListOpenPRs(ctx context.Context, repoURL, base string) ([]PRRef, error) {
	f.calls++
	return f.prs, f.err
}

// TestPollerGatesNewHeadOnce is the named dedupe test: the poller must run
// a given (repo, head sha) exactly once, even across repeated Tick calls,
// because the second Tick finds the SHA already in the Store.
func TestPollerGatesNewHeadOnce(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var runs int
	p := &Poller{
		Policies: []Policy{{Repo: "o/r", Base: []string{"main"}, Context: "corral/gate", CheckCmd: []string{"true"}}},
		List:     &fakeLister{prs: []PRRef{{Number: 1, HeadSHA: "abc", Base: "main"}}},
		Store:    store,
		Run: func(ctx context.Context, repoURL string, pol Policy, pr PRRef) error {
			runs++
			return store.Save(Run{Repo: pol.Repo, HeadSHA: pr.HeadSHA, PR: pr.Number, RanAt: time.Unix(0, 0)})
		},
	}
	_ = p.Tick(context.Background())
	_ = p.Tick(context.Background()) // same SHA → no second run
	if runs != 1 {
		t.Fatalf("runs = %d, want 1 (dedupe by head SHA)", runs)
	}
}

// TestPollerRunsEachNewHeadAcrossPolicies covers the multi-policy,
// multi-base fan-out: each policy is polled independently and every base
// branch declared on a policy is checked.
func TestPollerRunsEachNewHeadAcrossPolicies(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	listA := &fakeLister{prs: []PRRef{{Number: 1, HeadSHA: "a1", Base: "main"}}}
	listB := &fakeLister{prs: []PRRef{{Number: 2, HeadSHA: "b1", Base: "main"}}}

	var ran []string
	p := &Poller{
		Policies: []Policy{
			{Repo: "o/a", Base: []string{"main"}, Context: "corral/gate", CheckCmd: []string{"true"}},
			{Repo: "o/b", Base: []string{"main"}, Context: "corral/gate", CheckCmd: []string{"true"}},
		},
		// Route by policy repo via a small dispatcher list, since Poller.List
		// is a single PRLister shared across policies in production (the
		// forge engine); here two fakes stand in for two different repos'
		// results by keying off repoURL is unnecessary — a single fake
		// dispatch based on call count would be brittle, so instead this
		// test drives Tick per-policy indirectly by using one fake that
		// returns different PRs per call in sequence.
		List:  &sequencedLister{results: [][]PRRef{listA.prs, listB.prs}},
		Store: store,
		Run: func(ctx context.Context, repoURL string, pol Policy, pr PRRef) error {
			ran = append(ran, pol.Repo+"@"+pr.HeadSHA)
			return store.Save(Run{Repo: pol.Repo, HeadSHA: pr.HeadSHA, PR: pr.Number, RanAt: time.Unix(0, 0)})
		},
	}
	if err := p.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(ran) != 2 {
		t.Fatalf("ran = %v, want 2 entries (one per policy)", ran)
	}
}

// sequencedLister returns its results slice one call at a time, in order —
// used to simulate two different repos' PR lists across two policies
// without needing per-repoURL routing logic in the fake.
type sequencedLister struct {
	results [][]PRRef
	i       int
}

func (s *sequencedLister) ListOpenPRs(ctx context.Context, repoURL, base string) ([]PRRef, error) {
	if s.i >= len(s.results) {
		return nil, nil
	}
	r := s.results[s.i]
	s.i++
	return r, nil
}

// TestPollerListErrorLoggedNeverCrashes: a lister error for one policy must
// not stop the poller from continuing (and not panic/return in a way that
// looks like success) — logged loudly, degrade-never-block.
func TestPollerListErrorLoggedNeverCrashes(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var runs int
	p := &Poller{
		Policies: []Policy{{Repo: "o/r", Base: []string{"main"}, Context: "corral/gate", CheckCmd: []string{"true"}}},
		List:     &fakeLister{err: errors.New("forge unavailable")},
		Store:    store,
		Run: func(ctx context.Context, repoURL string, pol Policy, pr PRRef) error {
			runs++
			return nil
		},
	}
	// Must not panic.
	_ = p.Tick(context.Background())
	if runs != 0 {
		t.Fatalf("runs = %d, want 0 (list errored, nothing to run)", runs)
	}
}

// TestPollerLoopHonorsCancellation: Loop must return promptly once ctx is
// cancelled, rather than blocking forever on the ticker.
func TestPollerLoopHonorsCancellation(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	p := &Poller{
		Policies: []Policy{{Repo: "o/r", Base: []string{"main"}, Context: "corral/gate", CheckCmd: []string{"true"}}},
		List:     &fakeLister{},
		Store:    store,
		Run:      func(ctx context.Context, repoURL string, pol Policy, pr PRRef) error { return nil },
		Interval: time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		p.Loop(ctx)
		close(done)
	}()
	time.Sleep(5 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Loop did not return after ctx cancellation")
	}
}

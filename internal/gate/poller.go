// SPDX-License-Identifier: Elastic-2.0

package gate

import (
	"context"
	"log"
	"time"
)

// PRLister lists open PRs targeting base (all bases if base == "") for the
// repo at repoURL. repo.Engine satisfies this; tests inject a fake.
type PRLister interface {
	ListOpenPRs(ctx context.Context, repoURL, base string) ([]PRRef, error)
}

// Poller finds PR heads a Policy covers that haven't been gated yet (per
// Store's dedupe index) and drives Run for each one. It is the ONLY thing
// in this package that decides "is this SHA new" — Run/Runner never
// second-guess it, so dedupe stays a single, structural property of Tick
// rather than something every caller has to remember.
type Poller struct {
	Policies []Policy
	List     PRLister
	Store    *Store
	// Run gates one PR head (repoURL, the owning Policy, the PR). In
	// production this is (*gate.Runner).Run; tests inject a fake.
	Run func(ctx context.Context, repoURL string, p Policy, pr PRRef) error
	// Interval is how often Loop calls Tick. <=0 => Loop defaults to 1 minute.
	Interval time.Duration
}

// repoURLFor builds the GitHub HTTPS URL for a Policy's owner/name repo.
// Gate policies are GitHub-only for v1 (see the self-review note in the
// task brief) — Gitea/GitLab providers already return ErrUnsupported for
// ListOpenPRs/SetCommitStatus, so a policy naming a non-GitHub repo simply
// fails loudly at List/Run time rather than being silently mis-routed here.
func repoURLFor(p Policy) string {
	return "https://github.com/" + p.Repo
}

// Tick makes one pass over every Policy: for each declared base branch (or
// "" — all bases — if none is declared), list open PRs and Run every head
// whose (repo, sha) isn't already in Store. Errors from List or Run are
// logged loudly and never abort the pass — one bad repo/policy must not
// starve the others (design directive: degrade, never block/crash).
func (p *Poller) Tick(ctx context.Context) error {
	for _, pol := range p.Policies {
		bases := pol.Base
		if len(bases) == 0 {
			bases = []string{""}
		}
		repoURL := repoURLFor(pol)
		for _, base := range bases {
			prs, err := p.List.ListOpenPRs(ctx, repoURL, base)
			if err != nil {
				log.Printf("gate: poller: list open PRs for %s@%s: %v", pol.Repo, base, err)
				continue
			}
			for _, pr := range prs {
				_, ok, err := p.Store.GetBySHA(pol.Repo, pr.HeadSHA)
				if err != nil {
					log.Printf("gate: poller: dedupe lookup %s@%s: %v", pol.Repo, pr.HeadSHA, err)
					continue
				}
				if ok {
					continue // already gated — dedupe by head SHA
				}
				if err := p.Run(ctx, repoURL, pol, pr); err != nil {
					log.Printf("gate: poller: run %s#%d@%s: %v", pol.Repo, pr.Number, pr.HeadSHA, err)
				}
			}
		}
	}
	return nil
}

// Loop calls Tick every Interval until ctx is cancelled. It is the poller's
// only long-running entry point: StartGate runs it in its own goroutine.
// A Tick error is logged (Tick itself already logs the specifics); Loop
// never exits on error — only on ctx.Done() — so a transient forge outage
// never permanently stops gating.
func (p *Poller) Loop(ctx context.Context) {
	interval := p.Interval
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := p.Tick(ctx); err != nil {
				log.Printf("gate: poller: tick: %v", err)
			}
		}
	}
}

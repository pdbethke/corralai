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
	// Redeliver re-posts a verdict that was SIGNED but never reached the
	// forge, from its stored row, without running or signing anything. In
	// production this is (*gate.Runner).Redeliver. nil means a signed but
	// undelivered verdict is logged and left for the next tick, never
	// re-run: re-running it would sign a new record every tick for as long
	// as the forge refused the post.
	Redeliver func(ctx context.Context, repoURL string, p Policy, pr PRRef, prev Run) error
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
	var acting []Policy
	for _, pol := range p.Policies {
		// The SAME normalization the runner applies, so the dedupe lookup
		// asks for the row under the context the runner saved it under.
		pol = pol.normalized()
		// The rule ParsePolicyEnv states is held HERE too, at the door that
		// acts on policies: a Policy built programmatically never passes
		// through the parser, and two policies answering one pull request
		// under one status would leave the second silently never run.
		// (Review of main at 6951ca4c, 2026-09-15, R1.)
		if i := sharesAStatusWith(acting, pol); i >= 0 {
			log.Printf("gate: poller: SKIPPING a policy for %s: it would report under status %q on the same pull requests as an earlier policy, so only one could ever run — give one a distinct context", pol.Repo, pol.Context)
			continue
		}
		acting = append(acting, pol)
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
				// Keyed on the POLICY'S CONTEXT, not the head alone: two
				// policies on one repo each owe the forge their own status,
				// and a head-only key let the first one run silence the
				// second forever. (R3.)
				prev, ok, err := p.Store.GetByHead(pol.Repo, pr.HeadSHA, pol.Context)
				if err != nil {
					log.Printf("gate: poller: dedupe lookup %s@%s ctx %s: %v", pol.Repo, pr.HeadSHA, pol.Context, err)
					continue
				}
				// A row whose verdict never reached the forge is NOT done: the
				// post failed and nothing would ever retry it, so the check
				// sat pending until a new commit arrived. (R4.)
				if ok && prev.StatusPosted {
					continue // already gated under this context, and the forge has the verdict
				}
				if ok && !prev.StatusPosted {
					log.Printf("gate: poller: %s@%s ctx %s was gated but its status never posted — re-delivering", pol.Repo, pr.HeadSHA, pol.Context)
					// A SIGNED VERDICT IS RE-POSTED, NEVER RE-RUN. Calling Run
					// here re-checked-out, re-ran the jail and appended a NEW
					// signed record on every tick for as long as the post
					// failed, so a permanent refusal (403, 422) became an
					// unbounded re-certify loop. Only a row with no record —
					// a fail-closed run cut short, which is what this retry
					// was built for — is run again. (Review of main at
					// 6951ca4c, 2026-09-15, R2 — reproduced.)
					if prev.RecordID != 0 {
						if p.Redeliver == nil {
							log.Printf("gate: poller: %s@%s ctx %s has a signed verdict (record %d) and no redelivery path — leaving it; it will not be re-signed", pol.Repo, pr.HeadSHA, pol.Context, prev.RecordID)
							continue
						}
						if err := p.Redeliver(ctx, repoURL, pol, pr, prev); err != nil {
							log.Printf("gate: poller: re-delivering %s#%d@%s (record %d): %v", pol.Repo, pr.Number, pr.HeadSHA, prev.RecordID, err)
						}
						continue
					}
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

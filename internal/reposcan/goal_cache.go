// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"context"
	"os"
)

// GoalCacheStore is the persistence seam a CachingGoalSource looks up and
// records derived goals through. Defined HERE, not imported from scanstore:
// reposcan does not depend on the ledger package, so this interface is the
// small shape scanstore implements and cmd/corral wires the two together
// with — the same import-direction rule the verdict cache (reposcan.Cache)
// already follows.
//
// ok=true with ungoaled=true means "this exact (path, sourceDigest, model,
// promptRev) was looked up before, and the answer was NO GOAL" — the
// deriver's own "NONE" is a fact worth reusing exactly like a real goal is:
// re-asking a model the same question about unchanged bytes just to hear
// "no goal" again is the same repurchase this whole cache exists to avoid.
type GoalCacheStore interface {
	GoalCacheGet(ctx context.Context, path, sourceDigest, model, promptRev string) (goal, provenance string, ungoaled, ok bool, err error)
	GoalCachePut(ctx context.Context, path, sourceDigest, model, promptRev, goal, provenance string, ungoaled bool) error
}

// CachingGoalSource wraps another GoalSource so a goal derived from
// IDENTICAL bytes (same path, same source digest, same model, same prompt
// revision) is served from store rather than re-derived. Exported — rather
// than NewCachingGoalSource returning a bare GoalSource a caller cannot see
// inside — so a caller that wired one can read back how many calls it
// actually cost afterwards; see Stats.
//
// A cache hit returns the byte-identical Goal.Text the inner source
// produced when it was first derived — a hit that returned different text
// would be exactly the kind of silent drift a content-addressed cache
// exists to rule out — with Provenance carrying the ORIGINAL "derived:
// model@version" plus a " (reused)" marker, so GoalWasDerived still holds
// (it IS a derived goal) and GoalWasReused newly holds (this particular
// answer was not paid for again).
//
// A miss — including an infrastructure ERROR from the inner source, which
// is never cached (see GoalFor) — falls through to inner, and on success
// (or on a definitive "no goal") is written back so the next scan over the
// same bytes need not pay for it again.
type CachingGoalSource struct {
	root      string
	inner     GoalSource
	store     GoalCacheStore
	model     string
	promptRev string
	// writable gates the Put half only — Get always runs regardless (see
	// NewCachingGoalSource's doc). false is the shape a scan without
	// --record uses: it may still READ a fact recorded by an earlier,
	// recorded scan, but it writes nothing of its own — the same
	// read-always/write-under---record rule the selection cache follows
	// (collectSelection's Get is unconditional; its Put happens only in
	// runCertifyRepo's *recordFlag block).
	writable bool

	fresh  int
	reused int
}

// NewCachingGoalSource wires a caching layer in front of inner.
//
// root is required to compute a candidate's source digest the SAME way
// EmitJobs computes srcDigest (DigestFile through an *os.Root rooted here):
// a Candidate carries only a repo-relative path, so this is the one place
// that resolves it against the checkout — the plan's own signature for this
// constructor omits root, but Candidate has nowhere else to get one from,
// and a cache keyed on the wrong bytes is worse than no cache at all. See
// this task's report for the full note.
//
// writable, when false, disables GoalCachePut entirely: a scan run without
// --record can still READ another scan's recorded fact (Get is never
// gated), but it must not itself write model-derived text about the
// operator's source into the default ledger just because it happened to
// derive a goal — before this gate, a bare `corral certify --repo
// --derive-model X` (no --record at all) silently grew the goal_cache
// table on every run. See the selection cache's identical PUT-only gate,
// wired at this file's own call site in cmd/corral/certify_repo.go.
func NewCachingGoalSource(root string, inner GoalSource, store GoalCacheStore, model, promptRev string, writable bool) GoalSource {
	return &CachingGoalSource{root: root, inner: inner, store: store, model: model, promptRev: promptRev, writable: writable}
}

// Stats returns how many candidates actually reached the inner source
// (fresh — a real derivation, paid for) versus how many were served from
// the cache (reused — a repeat question about unchanged bytes). Used by the
// CLI to build its "goals: N derived by X@Y, M reused" disclosure line once
// a scan is done asking.
func (s *CachingGoalSource) Stats() (fresh, reused int) { return s.fresh, s.reused }

func (s *CachingGoalSource) GoalFor(c Candidate) (Goal, bool, error) {
	digest, derr := s.sourceDigest(c.Path)
	if derr == nil && s.store != nil {
		goal, provenance, ungoaled, ok, err := s.store.GoalCacheGet(context.Background(), c.Path, digest, s.model, s.promptRev)
		if err == nil && ok {
			s.reused++
			if ungoaled {
				return Goal{}, false, nil
			}
			return Goal{Text: goal, Provenance: provenance + goalReusedSuffix}, true, nil
		}
	}

	g, ok, err := s.inner.GoalFor(c)
	if err != nil {
		// An infrastructure failure is a property of the CALL, not of the
		// file: caching it would freeze a transient outage (a timeout, a
		// rate limit) into a permanent "no goal" for bytes nobody ever
		// actually asked a model about successfully.
		return g, ok, err
	}
	s.fresh++
	if s.store != nil && derr == nil && s.writable {
		// A write failure must never fail the scan — the same fail-closed
		// rule verdict_cache.go's ledgerCache follows. Worst case, the next
		// scan pays for this file again.
		if ok {
			_ = s.store.GoalCachePut(context.Background(), c.Path, digest, s.model, s.promptRev, g.Text, g.Provenance, false)
		} else {
			_ = s.store.GoalCachePut(context.Background(), c.Path, digest, s.model, s.promptRev, "", "", true)
		}
	}
	return g, ok, err
}

// sourceDigest is DigestFile, opened fresh for this one candidate: a
// CachingGoalSource is constructed once per scan and asked about many
// files sequentially (the same access pattern derivingGoalSource itself
// has), so opening one *os.Root per call costs nothing a whole-scan Root
// wouldn't have anyway, and keeps this type independent of EmitJobs' own
// Root lifetime.
func (s *CachingGoalSource) sourceDigest(relPath string) (string, error) {
	root, err := os.OpenRoot(s.root)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	return DigestFile(root, relPath)
}

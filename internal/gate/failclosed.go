// SPDX-License-Identifier: Elastic-2.0

package gate

import (
	"context"
	"log"
	"time"
)

// FailClosed is the single home of the gate fail-closed exit: record the head
// as Passed=false (so it isn't re-run), then post a non-success commit status.
// Both the merge runner and the control runner delegate here so the safety
// invariant lives in ONE place. A Save error is logged (not swallowed) — a
// dropped dedupe write would otherwise re-run the gate every poll, invisibly.
func FailClosed(ctx context.Context, store *Store, status StatusPoster, repoURL, repo string, pr PRRef, statusCtx, target, state, msg string, now func() time.Time) error {
	if err := store.Save(Run{Repo: repo, HeadSHA: pr.HeadSHA, PR: pr.Number, Passed: false, Context: statusCtx, RanAt: now()}); err != nil {
		log.Printf("gate: fail-closed save dedupe %s@%s: %v", repo, pr.HeadSHA, err)
	}
	// A TRANSIENT FAILURE WAS MADE PERMANENT. On shutdown the context is
	// cancelled, the jail returns "cancelled", this path stores Passed=false —
	// and then the status post, which uses that same cancelled context, fails
	// too. The row said "gated", so after restart the poller skipped the head
	// while the forge still showed "pending": a pull request stranded by a
	// clean shutdown, needing a new commit to escape. The same applied to any
	// transient checkout or network error.
	//
	// The fix is not to guess which errors are transient — it is to record
	// whether the verdict was DELIVERED. An undelivered row is retried by the
	// poller, so a cancelled run is re-gated on the next tick instead of
	// standing as a permanent refusal nobody was told about.
	// (Cold review 2026-09-12, R5.)
	if err := status.SetCommitStatus(ctx, repoURL, pr.HeadSHA, statusCtx, state, target, msg); err != nil {
		return err
	}
	if err := store.MarkPosted(repo, pr.HeadSHA, statusCtx); err != nil {
		log.Printf("gate: fail-closed marking %s@%s delivered: %v", repo, pr.HeadSHA, err)
	}
	return nil
}

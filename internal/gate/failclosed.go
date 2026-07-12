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
	if err := store.Save(Run{Repo: repo, HeadSHA: pr.HeadSHA, PR: pr.Number, Passed: false, RanAt: now()}); err != nil {
		log.Printf("gate: fail-closed save dedupe %s@%s: %v", repo, pr.HeadSHA, err)
	}
	return status.SetCommitStatus(ctx, repoURL, pr.HeadSHA, statusCtx, state, target, msg)
}

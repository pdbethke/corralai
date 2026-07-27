// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"math"
	"sort"
	"time"
)

// WeakFile is one entry in the ranked weakest-files list.
type WeakFile struct {
	Path      string
	KillRate  float64
	Survivors int
}

// RepoReport is the repo-level result. It is mostly ACCOUNTING, because that
// is what makes the headline number honest: a reader can see exactly what the
// score covers and what it does not.
type RepoReport struct {
	Owner, Repo, Commit string

	TotalFiles int
	// Candidates is the number of files enumeration judged AUDITABLE, before
	// any of them were dropped for want of a goal. It is not the number of
	// jobs: counting jobs would erase every ungoaled file from the ratio
	// below, so a repo with one goal out of five hundred candidates would
	// report 100% audited.
	Candidates int
	Audited    int
	Excluded   []Exclusion
	Ungradable map[string]int

	// KillRate is over Audited ONLY, never over the repo. NaN when nothing
	// was audited — a 0.0 there would read as "terrible tests" when the truth
	// is "no measurement was made".
	KillRate float64

	Weakest     []WeakFile
	CacheHits   int
	GeneratedAt time.Time
}

// AuditedFraction is the share of candidates that produced a real score. The
// coverage floor (H1c) is applied to this.
func (r RepoReport) AuditedFraction() float64 {
	if r.Candidates == 0 {
		return 0
	}
	return float64(r.Audited) / float64(r.Candidates)
}

// Aggregate rolls per-file results into the repo report.
//
// candidates is the PRE-GOAL candidate count from enumeration, not len(results):
// results only covers candidates that became jobs, and the difference — files
// with no goal — belongs in the audited-fraction denominator, accounted under
// ReasonUngoaled. Passing len(results) here would report "100% audited" for a
// repo where one file in five hundred had a goal.
func Aggregate(owner, repo, commit string, totalFiles, candidates int, results []FileResult, excl []Exclusion) RepoReport {
	// Fail safe rather than print a fraction above 1.0: a caller that
	// under-counts candidates gets the honest floor, never a flattering ratio.
	if candidates < len(results) {
		candidates = len(results)
	}
	rep := RepoReport{
		Owner: owner, Repo: repo, Commit: commit,
		TotalFiles:  totalFiles,
		Candidates:  candidates,
		Excluded:    excl,
		Ungradable:  map[string]int{},
		GeneratedAt: time.Now(),
	}
	// Ungoaled files never became jobs, so they are absent from results.
	// Count them from the exclusions rather than by subtracting, so a future
	// pre-job drop reason cannot be silently mislabelled as ungoaled.
	for _, e := range excl {
		if e.Reason == ReasonUngoaled {
			rep.Ungradable[ReasonUngoaled]++
		}
	}

	var sum float64
	for _, r := range results {
		if r.CacheHit {
			rep.CacheHits++
		}
		if !r.Gradable {
			reason := r.Reason
			if reason == "" {
				reason = ReasonExecutorError
			}
			rep.Ungradable[reason]++
			continue
		}
		rep.Audited++
		sum += r.Verdict.DevKillRate
		rep.Weakest = append(rep.Weakest, WeakFile{
			Path:      r.Job.Path,
			KillRate:  r.Verdict.DevKillRate,
			Survivors: r.Verdict.Survivors,
		})
	}

	if rep.Audited == 0 {
		rep.KillRate = math.NaN()
	} else {
		rep.KillRate = sum / float64(rep.Audited)
	}

	sort.SliceStable(rep.Weakest, func(i, j int) bool {
		return rep.Weakest[i].KillRate < rep.Weakest[j].KillRate
	})
	return rep
}

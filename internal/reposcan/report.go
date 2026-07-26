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
func Aggregate(owner, repo, commit string, totalFiles int, results []FileResult, excl []Exclusion) RepoReport {
	rep := RepoReport{
		Owner: owner, Repo: repo, Commit: commit,
		TotalFiles:  totalFiles,
		Candidates:  len(results),
		Excluded:    excl,
		Ungradable:  map[string]int{},
		GeneratedAt: time.Now(),
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

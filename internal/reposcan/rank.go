// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// RankInfo describes HOW candidates were ordered, so a report can disclose the
// selection rule rather than presenting an unexplainable order. A ranking
// nobody can explain is the same problem this project criticises in black-box
// model routing.
type RankInfo struct {
	Signal string // "churn-x-size" | "size-only"
	Note   string // why the signal degraded; empty when it did not
}

// Rank orders candidates most-worth-auditing first, by commit count times
// source size. Both inputs are local and cheap — no model call — and the rule
// fits in one sentence, which matters because it is disclosed.
//
// Degradation: without usable git history (a tarball, a shallow clone, no git
// binary) it ranks by size alone and says so. A scan of an exported tree must
// still run. Likewise a candidate that cannot be stat'ed (deleted between
// enumeration and ranking, or unreadable) is sized 0 and ranked last rather
// than costing the whole scan — one bad path must not be worth 430 files.
// Ranking cannot fail, which is why it returns no error.
func Rank(root string, cands []Candidate) ([]Candidate, RankInfo) {
	churn, info := fileChurn(root)

	size := make(map[string]int64, len(cands))
	unsized := 0
	for _, c := range cands {
		fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(c.Path)))
		if err != nil {
			// Size 0 sorts it to the tail: it is still a candidate, still
			// accounted, just never preferred over a file we could measure.
			size[c.Path] = 0
			unsized++
			continue
		}
		size[c.Path] = fi.Size()
	}
	// Disclosed, like every other degradation: a reader must be able to see
	// that the order was computed with missing information.
	if unsized > 0 {
		note := fmt.Sprintf("%d candidate(s) could not be sized and were ranked last", unsized)
		if info.Note == "" {
			info.Note = note
		} else {
			info.Note += "; " + note
		}
	}

	score := func(c Candidate) int64 {
		n := int64(churn[c.Path])
		if n < 1 {
			n = 1 // never zero out a file just because git did not see it
		}
		return n * size[c.Path]
	}

	out := append([]Candidate(nil), cands...)
	// SliceStable so equal scores keep enumeration order: a scan of the same
	// tree must select the same files every time.
	sort.SliceStable(out, func(i, j int) bool { return score(out[i]) > score(out[j]) })
	return out, info
}

// fileChurn counts commits touching each repo-relative path. A failure is not
// an error: it degrades the signal and is reported through RankInfo.
func fileChurn(root string) (map[string]int, RankInfo) {
	// A shallow clone has a working .git, so `git log` succeeds — but its
	// history is TRUNCATED, and a file whose commits predate the cutoff looks
	// untouched. Ranking on that would silently bias a signal the report
	// discloses, so treat it the same as no history at all.
	shallow := exec.CommandContext(context.Background(), "git", "rev-parse", "--is-shallow-repository") // #nosec G204 -- fixed binary, literal args
	shallow.Dir = root
	if out, err := shallow.Output(); err == nil && strings.TrimSpace(string(out)) == "true" {
		return nil, RankInfo{
			Signal: "size-only",
			Note:   "shallow clone — commit history is truncated, so churn would be biased; ranked by source size alone",
		}
	}

	cmd := exec.CommandContext(context.Background(), "git", "log", "--format=", "--name-only") // #nosec G204 -- fixed binary, literal args
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil || len(strings.TrimSpace(string(out))) == 0 {
		return nil, RankInfo{
			Signal: "size-only",
			Note:   "no usable git history in this tree — ranked by source size alone",
		}
	}
	churn := map[string]int{}
	for _, line := range strings.Split(string(out), "\n") {
		if p := strings.TrimSpace(line); p != "" {
			churn[p]++
		}
	}
	return churn, RankInfo{Signal: "churn-x-size"}
}

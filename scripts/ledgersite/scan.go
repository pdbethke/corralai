// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/killrate"
)

// ScanView is one certify scan entry as a page: what ran, on which commit,
// and what execution measured per file. It renders the bundle the run
// signed and computes nothing the bundle does not already say, except the
// kill-rate interval, which comes from the row's own counts through the
// same internal/killrate the CLI prints it with.
type ScanView struct {
	ChainView
	Scan      auditpush.ScanRow
	RepoName  string // owner/name for a recognised host, else the recorded remote
	CommitURL string
	ShortSHA  string
	// Retracted is true when a retraction in force names this entry. The
	// page still exists, so a link to it does not break, and says so.
	Retracted       bool
	RetractedReason string
	SourcePushed    bool
	Audited         []ScanFileView
	// NotAudited counts the files the scan considered and did not audit,
	// by reason. A repository-wide scan carries hundreds of these rows;
	// listing them one by one buries the files that were measured.
	NotAudited []ReasonCount
	Graded     int
	Killed     int
	Rate       string // over every audited file, "" when nothing was graded
	Survived   int
	Proven     int
	Seats      []string // "role: model [tool version]", from the recorded model set
}

// ScanFileView is one audited file with the band its rate carries.
type ScanFileView struct {
	auditpush.Row
	Killed      int
	Rate        string // "0.32", or "" when nothing was graded
	Interval    string // "0.19–0.49", or "" when there is no interval
	Survivors   []MutantView
	Unsurfaced  int // survivors recorded without a diff (source withheld)
	HasAuthored bool
}

// MutantView is one surviving mutant: where, whether a test corral wrote
// and ran caught it, and the change as a diff when the source was pushed.
type MutantView struct {
	ID     string
	Line   int
	Proven bool
	Diff   string
}

// ReasonCount is how many files a scan left out for one reason.
type ReasonCount struct {
	Reason string
	Count  int
}

func buildScanView(e auditpush.LedgerEntry, c ChainView, retraction *auditpush.LedgerEntry) ScanView {
	b := e.Bundle
	sv := ScanView{ChainView: c, Scan: b.Scan, SourcePushed: b.SourcePushed}
	sv.RepoName = strings.TrimPrefix(repoWebURL(b.Scan.Repo), "https://github.com/")
	if sv.RepoName == "" {
		sv.RepoName = b.Scan.Repo
	}
	if base := repoWebURL(b.Scan.Repo); base != "" && b.Scan.Commit != "" {
		sv.CommitURL = base + "/commit/" + b.Scan.Commit
	}
	sv.ShortSHA = shortSHA(b.Scan.Commit)
	if retraction != nil {
		sv.Retracted, sv.RetractedReason = true, retraction.Reason
	}
	sv.Seats = seatsOf(b.Scan.ModelSet)

	byPath := map[string][]auditpush.MutantRow{}
	for _, m := range b.Mutants {
		byPath[m.Path] = append(byPath[m.Path], m)
	}
	reasons := map[string]int{}
	for _, f := range b.Files {
		if f.Disposition != "audited" {
			r := f.Reason
			if r == "" {
				r = "not audited"
			}
			reasons[r]++
			continue
		}
		fv := ScanFileView{Row: f, HasAuthored: strings.TrimSpace(f.AuthoredTest) != ""}
		// A rate is shown only where the row has one and something was
		// graded: an uncovered or ungraded file's rate is withheld, never a
		// 0.00 standing in for a could-not-measure.
		if f.KillRate != nil && f.MutantsGraded > 0 {
			fv.Killed = killrate.Killed(f.MutantsGraded, f.Survivors)
			fv.Rate = fmt.Sprintf("%.2f", *f.KillRate)
			// The band is over the DISTINCT measured edits when the row
			// recorded them, the sample the certification gate used. An
			// older row has only MutantsGraded, which counts duplicates, so
			// its band says so rather than passing for the other.
			if f.MutantsDistinct != nil && f.MutantsDistinctSurvived != nil {
				if lo, hi, ok := killrate.Interval(*f.MutantsDistinct, *f.MutantsDistinctSurvived); ok {
					fv.Interval = fmt.Sprintf("%.2f–%.2f (n=%d distinct)", lo, hi, *f.MutantsDistinct)
				}
			} else if lo, hi, ok := killrate.Interval(f.MutantsGraded, f.Survivors); ok {
				fv.Interval = fmt.Sprintf("%.2f–%.2f (n=%d, duplicates counted)", lo, hi, f.MutantsGraded)
			}
			sv.Graded += f.MutantsGraded
			sv.Killed += fv.Killed
		}
		sv.Survived += f.Survivors
		sv.Proven += f.ProvenMissed
		for _, m := range byPath[f.Path] {
			if m.Outcome != "survived" {
				continue
			}
			mv := MutantView{ID: m.MutantID, Line: m.SpanStart, Proven: m.Proven, Diff: diffOf(m.Code)}
			if mv.Diff == "" {
				fv.Unsurfaced++
			}
			fv.Survivors = append(fv.Survivors, mv)
		}
		sort.SliceStable(fv.Survivors, func(i, j int) bool {
			if fv.Survivors[i].Proven != fv.Survivors[j].Proven {
				return fv.Survivors[i].Proven
			}
			return fv.Survivors[i].Line < fv.Survivors[j].Line
		})
		sv.Audited = append(sv.Audited, fv)
	}
	if sv.Graded > 0 {
		sv.Rate = fmt.Sprintf("%.2f", float64(sv.Killed)/float64(sv.Graded))
	}
	for r, n := range reasons {
		sv.NotAudited = append(sv.NotAudited, ReasonCount{r, n})
	}
	sort.Slice(sv.NotAudited, func(i, j int) bool {
		if sv.NotAudited[i].Count != sv.NotAudited[j].Count {
			return sv.NotAudited[i].Count > sv.NotAudited[j].Count
		}
		return sv.NotAudited[i].Reason < sv.NotAudited[j].Reason
	})
	return sv
}

// diffOf renders a mutant's recorded search/replace as a line diff. "" when
// nothing was recorded (source withheld) or the record is not that shape:
// the page then says the diff is not available rather than inventing one.
func diffOf(code string) string {
	if strings.TrimSpace(code) == "" {
		return ""
	}
	var sr struct {
		Search  string `json:"search"`
		Replace string `json:"replace"`
	}
	if err := json.Unmarshal([]byte(code), &sr); err != nil || sr.Search == "" {
		return ""
	}
	old := strings.Split(sr.Search, "\n")
	neu := strings.Split(sr.Replace, "\n")
	// Trim the lines both sides share at either end, and the common
	// indentation, so the diff shows the change rather than its context.
	for len(old) > 0 && len(neu) > 0 && old[0] == neu[0] {
		old, neu = old[1:], neu[1:]
	}
	for len(old) > 0 && len(neu) > 0 && old[len(old)-1] == neu[len(neu)-1] {
		old, neu = old[:len(old)-1], neu[:len(neu)-1]
	}
	indent := commonIndent(append(append([]string{}, old...), neu...))
	var b strings.Builder
	for _, l := range old {
		b.WriteString("- " + strings.TrimPrefix(l, indent) + "\n")
	}
	for _, l := range neu {
		b.WriteString("+ " + strings.TrimPrefix(l, indent) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func commonIndent(lines []string) string {
	indent := ""
	first := true
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		lead := l[:len(l)-len(strings.TrimLeft(l, " \t"))]
		if first {
			indent, first = lead, false
			continue
		}
		for !strings.HasPrefix(lead, indent) {
			indent = indent[:len(indent)-1]
		}
	}
	return indent
}

// seatsOf splits the recorded model set ("role=model [tool],role=model")
// into one line per seat, as recorded.
func seatsOf(modelSet string) []string {
	var out []string
	for _, p := range strings.Split(modelSet, ",") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		if role, model, ok := strings.Cut(p, "="); ok {
			p = role + ": " + model
		}
		out = append(out, p)
	}
	return out
}

func shortSHA(commit string) string {
	if len(commit) > 10 {
		return commit[:10]
	}
	return commit
}

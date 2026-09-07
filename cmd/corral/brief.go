// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/review"
)

// corral brief — the auditor's report: what the record says is still OPEN
// on a set of paths, handed to whoever writes next. A person, a coding
// agent, corral does not care; it never writes the code. The same move
// `--prior` makes for the mutant generator ("here is what was tried"),
// made for the author: here is the fault your tests missed, here is the
// test that closes it, here is the claim that reproduced against this
// file and what a person ruled. Every line points at an entry; nothing is
// rendered that the ledger does not hold.

// briefFlags are the verb's flags.
type briefFlags struct {
	repoDir, ledger, changed string
	scopes                   multiFlag
	jsonOut                  bool
	maxItems                 int
}

// multiFlag collects a repeatable string flag.
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, strings.TrimSpace(v)); return nil }

// Brief is the report: per path, what is open, newest first.
type Brief struct {
	Repo      string      `json:"repo"`
	Ledger    string      `json:"ledger"`
	Commits   []string    `json:"commits"`
	Entries   int         `json:"entries_read"`
	Retracted int         `json:"entries_retracted"`
	Paths     []BriefPath `json:"paths"`
	Cut       string      `json:"cut,omitempty"`
}

// BriefPath is one file's open items.
type BriefPath struct {
	Path        string        `json:"path"`
	Scan        *BriefScan    `json:"scan,omitempty"`
	Survivors   []BriefMutant `json:"survivors,omitempty"`
	ProvenGaps  []BriefMutant `json:"proven_gaps,omitempty"`
	Findings    []BriefClaim  `json:"findings,omitempty"`
	DidNotHold  int           `json:"claims_did_not_hold"`
	Undecided   int           `json:"claims_undecided"`
	NoScan      bool          `json:"no_scan"`
	NoReview    bool          `json:"no_review"`
	Disposition string        `json:"disposition,omitempty"`
}

// BriefScan is the newest scan's verdict on the file.
type BriefScan struct {
	Commit      string   `json:"commit"`
	Entry       string   `json:"entry"`
	Pushed      string   `json:"pushed"`
	Disposition string   `json:"disposition"`
	Reason      string   `json:"reason,omitempty"`
	KillRate    *float64 `json:"kill_rate,omitempty"`
	Survivors   int      `json:"survivors"`
	ProvenGaps  int      `json:"proven_gaps"`
	Uncovered   bool     `json:"uncovered"`
	Passed      *bool    `json:"passed,omitempty"`
	AuthoredTst string   `json:"authored_test,omitempty"`
}

// BriefMutant is one planted fault the suite missed.
type BriefMutant struct {
	ID       string `json:"id"`
	Span     string `json:"span"`
	Shape    string `json:"shape,omitempty"`
	Search   string `json:"search,omitempty"`
	Replace  string `json:"replace,omitempty"`
	KilledBy string `json:"killed_by,omitempty"`
}

// BriefClaim is one review finding that stands against the file.
type BriefClaim struct {
	Review     string `json:"review"`
	Ref        string `json:"ref"`
	Commit     string `json:"commit"`
	Reviewer   string `json:"reviewer"`
	Line       int    `json:"line,omitempty"`
	Severity   string `json:"severity,omitempty"`
	Claim      string `json:"claim"`
	Declared   string `json:"declared"`
	Recorded   string `json:"recorded"`
	Demoted    string `json:"demoted,omitempty"`
	Verifier   string `json:"verifier,omitempty"`
	Verdict    string `json:"verdict,omitempty"`
	Argument   string `json:"argument,omitempty"`
	Adjudged   string `json:"adjudicated,omitempty"`
	AdjudgedBy string `json:"adjudicated_by,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Outcome    string `json:"outcome"`
}

func runBrief(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("corral brief", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var f briefFlags
	fs.StringVar(&f.repoDir, "repo", ".", "the checkout the paths are relative to")
	fs.StringVar(&f.ledger, "ledger", "", ledgerReadHelp)
	fs.Var(&f.scopes, "scope", "a file or directory (repo-relative) to report on; repeatable")
	fs.StringVar(&f.changed, "changed", "", "report on the files changed since this git ref (base...HEAD), instead of or as well as --scope")
	fs.BoolVar(&f.jsonOut, "json", false, "the report as one JSON document, for an agent to read")
	fs.IntVar(&f.maxItems, "max-items", 50, "at most this many items (survivors, gaps, claims) across the report; the cut is named")
	if err := fs.Parse(flagsFirst(fs, args)); err != nil {
		return 2
	}
	if len(f.scopes) == 0 && strings.TrimSpace(f.changed) == "" {
		fmt.Fprintln(stderr, "corral brief: name what to report on — --scope <path> (repeatable) and/or --changed <base ref>")
		return 2
	}
	root, err := filepath.Abs(f.repoDir)
	if err != nil {
		fmt.Fprintf(stderr, "corral brief: %v\n", err)
		return 2
	}
	paths := append([]string(nil), f.scopes...)
	if f.changed != "" {
		changed, err := changedFiles(root, f.changed)
		if err != nil {
			fmt.Fprintf(stderr, "corral brief: %v\n", strings.TrimPrefix(err.Error(), "corral certify --repo: "))
			return 2
		}
		paths = append(paths, changed...)
	}
	dir := f.ledger
	if dir == "" {
		dir = defaultLedgerDir(root)
	}
	entries, err := auditpush.ReadLedgerDir(dir)
	if err != nil {
		fmt.Fprintf(stderr, "corral brief: %v\n", err)
		return 1
	}
	b := buildBrief(dir, entries, paths, f.maxItems)
	if f.jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(b); err != nil {
			fmt.Fprintf(stderr, "corral brief: %v\n", err)
			return 1
		}
		return 0
	}
	fmt.Fprint(stdout, renderBrief(b))
	return 0
}

// buildBrief reads the live record for paths. Newest entries win for the
// scan verdict; every standing review claim is listed.
func buildBrief(dir string, all []auditpush.LedgerEntry, paths []string, maxItems int) Brief {
	live := auditpush.LiveEntries(all)
	b := Brief{Ledger: dir, Entries: len(all), Retracted: len(all) - len(live)}
	adj := auditpush.Adjudications(live)
	commits := map[string]bool{}
	byPath := map[string]*BriefPath{}
	get := func(p string) *BriefPath {
		if bp, ok := byPath[p]; ok {
			return bp
		}
		bp := &BriefPath{Path: p, NoScan: true, NoReview: true}
		byPath[p] = bp
		return bp
	}
	under := func(file string) bool {
		file = filepath.ToSlash(filepath.Clean(file))
		for _, p := range paths {
			p = filepath.ToSlash(filepath.Clean(p))
			if p == "." || file == p || strings.HasPrefix(file, p+"/") {
				return true
			}
		}
		return false
	}
	// Scans, newest first: the first scan that graded a file is its verdict.
	for i := len(live) - 1; i >= 0; i-- {
		e := live[i]
		if !e.IsScan() {
			continue
		}
		if b.Repo == "" {
			b.Repo = e.Bundle.Scan.Repo
		}
		for _, r := range e.Bundle.Files {
			if !under(r.Path) {
				continue
			}
			bp := get(r.Path)
			if !bp.NoScan {
				continue // an older scan of a file already reported
			}
			bp.NoScan = false
			commits[e.Bundle.Scan.Commit] = true
			bp.Scan = &BriefScan{Commit: e.Bundle.Scan.Commit, Entry: e.Hash, Pushed: e.Pushed.UTC().Format("2006-01-02 15:04"),
				Disposition: r.Disposition, Reason: r.Reason, KillRate: r.KillRate, Survivors: r.Survivors, ProvenGaps: r.ProvenMissed,
				Uncovered: r.Uncovered, Passed: r.Passed, AuthoredTst: r.AuthoredTest}
			bp.Disposition = r.Disposition
			for _, m := range e.Bundle.Mutants {
				if m.Path != r.Path || m.Outcome != "survived" {
					continue
				}
				bm := BriefMutant{ID: m.MutantID, Span: spanText(m.SpanStart, m.SpanEnd), Shape: m.Shape, KilledBy: m.KilledBy}
				if s, rep, ok := adequacy.DecodeHunk(m.Code); ok {
					bm.Search, bm.Replace = s, rep
				}
				if m.Proven {
					bp.ProvenGaps = append(bp.ProvenGaps, bm)
				} else {
					bp.Survivors = append(bp.Survivors, bm)
				}
			}
		}
	}
	// Reviews, newest first: every finding under the paths, with its outcome.
	for i := len(live) - 1; i >= 0; i-- {
		e := live[i]
		if e.Kind != auditpush.KindReview || e.Review == nil {
			continue
		}
		if b.Repo == "" {
			b.Repo = e.Review.Repo
		}
		for _, fd := range e.Review.Findings {
			if fd.File == "" || !under(fd.File) {
				continue
			}
			bp := get(fd.File)
			bp.NoReview = false
			commits[e.Review.Commit] = true
			ref := e.Hash + "#" + fd.ID
			var a *review.Adjudicated
			var ar *auditpush.Adjudication
			if x, ok := adj[ref]; ok {
				a = &review.Adjudicated{Verdict: x.Verdict, By: x.By}
				ar = &x
			}
			o := review.OutcomeOf(fd, a)
			switch {
			case !o.Known:
				bp.Undecided++
				continue
			case !o.Held:
				bp.DidNotHold++
				continue
			}
			c := BriefClaim{Review: e.Hash, Ref: shortRef(ref), Commit: e.Review.Commit, Reviewer: e.Review.ReviewerModel,
				Line: fd.Line, Severity: fd.Severity, Claim: fd.Claim, Declared: fd.Declared, Recorded: fd.Tier, Demoted: fd.Demoted, Outcome: o.By}
			if x := fd.Refutation; x != nil {
				c.Verifier, c.Verdict, c.Argument = x.Model, x.Verdict, x.Argument
			}
			if ar != nil {
				c.Adjudged, c.AdjudgedBy, c.Reason = ar.Verdict, ar.By, ar.Reason
			}
			bp.Findings = append(bp.Findings, c)
		}
	}
	for p := range byPath {
		b.Paths = append(b.Paths, *byPath[p])
	}
	sort.Slice(b.Paths, func(i, j int) bool { return b.Paths[i].Path < b.Paths[j].Path })
	for c := range commits {
		b.Commits = append(b.Commits, c)
	}
	sort.Strings(b.Commits)
	// The bound: items across the report, in path order; the cut names
	// where the rest are, so a reader is pointed somewhere, not nowhere.
	kept, dropped, firstCut := 0, 0, ""
	take := func(have int, path string) int {
		room := maxItems - kept
		if room < 0 {
			room = 0
		}
		if have <= room {
			kept += have
			return have
		}
		if firstCut == "" {
			firstCut = path
		}
		kept += room
		dropped += have - room
		return room
	}
	for i := range b.Paths {
		bp := &b.Paths[i]
		bp.ProvenGaps = bp.ProvenGaps[:take(len(bp.ProvenGaps), bp.Path)]
		bp.Survivors = bp.Survivors[:take(len(bp.Survivors), bp.Path)]
		bp.Findings = bp.Findings[:take(len(bp.Findings), bp.Path)]
	}
	if dropped > 0 {
		b.Cut = fmt.Sprintf("%d item(s) not listed (--max-items %d), from %s on — the entries hold them", dropped, maxItems, firstCut)
	}
	return b
}

// spanText renders a line range as the prior does.
func spanText(start, end int) string {
	switch {
	case start == 0:
		return "line ?"
	case end <= start:
		return fmt.Sprintf("line %d", start)
	}
	return fmt.Sprintf("lines %d-%d", start, end)
}

// renderBrief is the text form: what is open, where, and what closes it.
func renderBrief(b Brief) string {
	var w strings.Builder
	fmt.Fprintf(&w, "brief — %s: %d path(s), from %d entr%s in %s", b.Repo, len(b.Paths), b.Entries, plural(b.Entries, "y", "ies"), b.Ledger)
	if b.Retracted > 0 {
		fmt.Fprintf(&w, " (%d retracted, left out)", b.Retracted)
	}
	w.WriteString("\n")
	if len(b.Commits) > 0 {
		short := make([]string, 0, len(b.Commits))
		for _, c := range b.Commits {
			short = append(short, shortHash(c))
		}
		fmt.Fprintf(&w, "  commit(s) on record: %s\n", strings.Join(short, ", "))
	}
	if len(b.Paths) == 0 {
		w.WriteString("  nothing on record for these paths — no scan graded them and no review named them\n")
		return w.String()
	}
	for _, p := range b.Paths {
		fmt.Fprintf(&w, "\n%s\n", p.Path)
		switch {
		case p.NoScan:
			w.WriteString("  scan: none on record\n")
		case p.Scan.Uncovered:
			fmt.Fprintf(&w, "  scan @ %s (%s): no test reaches this file — nothing was graded\n", shortHash(p.Scan.Commit), p.Scan.Pushed)
		case p.Scan.KillRate == nil:
			fmt.Fprintf(&w, "  scan @ %s (%s): %s", shortHash(p.Scan.Commit), p.Scan.Pushed, p.Scan.Disposition)
			if p.Scan.Reason != "" {
				fmt.Fprintf(&w, " — %s", p.Scan.Reason)
			}
			w.WriteString("\n")
		default:
			fmt.Fprintf(&w, "  scan @ %s (%s): kill rate %.2f, %d survivor(s), %d proven gap(s)\n", shortHash(p.Scan.Commit), p.Scan.Pushed, *p.Scan.KillRate, p.Scan.Survivors, p.Scan.ProvenGaps)
		}
		for _, m := range p.ProvenGaps {
			fmt.Fprintf(&w, "  gap, proven — %s, %s: your tests missed this fault; a test that catches it is on record\n", m.Span, shapeOr(m.Shape))
			writeHunk(&w, m)
		}
		if len(p.ProvenGaps) > 0 && p.Scan != nil && p.Scan.AuthoredTst != "" {
			w.WriteString("    the test that closes it:\n")
			w.WriteString(indentLines(strings.TrimRight(p.Scan.AuthoredTst, "\n"), "      "))
			w.WriteString("\n")
		}
		for _, m := range p.Survivors {
			fmt.Fprintf(&w, "  survivor — %s, %s: your tests missed this fault; no test on record catches it yet\n", m.Span, shapeOr(m.Shape))
			writeHunk(&w, m)
		}
		for _, c := range p.Findings {
			where := ""
			if c.Line > 0 {
				where = fmt.Sprintf(" line %d,", c.Line)
			}
			fmt.Fprintf(&w, "  claim %s —%s %s (%s): %s\n", c.Ref, where, strings.ToLower(orText(c.Severity, "unrated")), c.Outcome, c.Claim)
			fmt.Fprintf(&w, "    by %s @ %s; declared %s, recorded %s", c.Reviewer, shortHash(c.Commit), c.Declared, c.Recorded)
			if c.Verdict != "" {
				fmt.Fprintf(&w, "; verifier %s: %s", c.Verifier, c.Verdict)
			}
			w.WriteString("\n")
			if c.Adjudged != "" {
				fmt.Fprintf(&w, "    %s by %s: %s\n", c.Adjudged, c.AdjudgedBy, c.Reason)
			}
		}
		if p.DidNotHold > 0 || p.Undecided > 0 {
			fmt.Fprintf(&w, "  %d claim(s) did not hold, %d with no outcome yet — in the entries, not listed here\n", p.DidNotHold, p.Undecided)
		}
		if p.NoReview && !p.NoScan {
			w.WriteString("  review: none on record\n")
		}
	}
	if b.Cut != "" {
		fmt.Fprintf(&w, "\n  … %s\n", b.Cut)
	}
	return w.String()
}

func writeHunk(w *strings.Builder, m BriefMutant) {
	if m.Search == "" && m.Replace == "" {
		return
	}
	fmt.Fprintf(w, "    - %s\n    + %s\n", oneLineOf(m.Search), oneLineOf(m.Replace))
}

func oneLineOf(s string) string {
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	if r := []rune(s); len(r) > 100 {
		s = string(r[:97]) + "…"
	}
	return s
}

func shapeOr(s string) string   { return orText(s, "unclassified") }
func shortHash(h string) string { return tailOr(h, 12) }
func tailOr(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
func orText(s, alt string) string {
	if strings.TrimSpace(s) == "" {
		return alt
	}
	return s
}
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// shortRef renders "<hash>#Rn" as "<hash12>#Rn", as the ledger does.
func shortRef(ref string) string {
	h, id, _ := strings.Cut(ref, "#")
	if len(h) > 12 {
		h = h[:12]
	}
	if id == "" {
		return h
	}
	return h + "#" + id
}

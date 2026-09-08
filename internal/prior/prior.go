// SPDX-License-Identifier: Elastic-2.0

// Package prior turns what earlier runs recorded about a file into what the
// next run's generator is told before it plants: the edits already tried on
// THIS EXACT VERSION of the file, each with its outcome, so the exam moves
// to shapes and places the last one did not reach instead of re-rolling the
// same faults.
//
// Two honesty rules govern it.
//
// SAME BYTES ONLY. A prior is keyed on the file's sha256. An edit recorded
// against a different version of the file may describe code that no longer
// exists, at lines that have moved; telling the generator about it would be
// telling it about a different file. A mismatch is reported, not applied.
//
// A PRIOR CHANGES THE EXAM. A run that knows what survived last time sits a
// harder, different exam than one that does not, and its kill rate is not
// comparable to the earlier run's. So every verdict that received a prior
// says so — how many edits, from where, under what digest — on the report
// line, in both ledgers and in the signed statement, and the digest is in
// the cache key so a cached verdict is never served across the boundary.
package prior

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/lang"
)

// Tried is one edit an earlier run graded.
type Tried struct {
	Path         string
	ParentSHA256 string
	ID           string
	Span         lang.LineRange
	Shape        string
	// Search/Replace are the hunk when the source carried one (a mutant-set
	// document); a ledger row carries the span and shape only.
	Search, Replace string
	// Outcome is "killed" or "survived"; KilledBy names the test when the
	// runner said; Proven says the pool authored a killing test for a
	// survivor.
	Outcome  string
	KilledBy string
	Proven   bool
}

// Prior is everything a source held, indexed by path.
type Prior struct {
	Source  string
	byPath  map[string][]Tried
	sources int
}

// Load reads a prior from source: a corral-mutants document (.json), a
// LEDGER DIRECTORY (the signed entries `certify --repo` writes — outcomes,
// spans and shapes, and hunks when the entry carries source), or a
// directory holding any number of these — every file found is merged, a
// ledger row and a document entry for the same (path, id) becoming one
// Tried carrying both the outcome and the hunk.
func Load(source string) (*Prior, error) {
	p := &Prior{Source: source, byPath: map[string][]Tried{}}
	st, err := os.Stat(source)
	if err != nil {
		return nil, fmt.Errorf("prior: %w", err)
	}
	var all []Tried
	upsert := func(t Tried) { all = append(all, t) }
	var files []string
	if st.IsDir() {
		// A ledger directory's entries first: they carry outcomes AND
		// hunks in one document each. Retracted entries are skipped
		// (auditpush.ScanEntries): a retracted run did not happen, as far
		// as the next exam is concerned.
		// A tampered chain must not steer a run that has not happened yet:
		// the prior is read straight into the generator's prompt. This is
		// the fifth door, added when the shared guard's own doc was found
		// claiming a coverage it did not have. (Round five, R3.)
		if verr := auditpush.RequireIntactChain(source); verr != nil {
			return nil, verr
		}
		if all, lerr := auditpush.ReadLedgerDir(source); lerr != nil {
			return nil, lerr
		} else if entries := auditpush.ScanEntries(all); len(entries) > 0 {
			p.sources++
			for _, e := range entries {
				for _, m := range e.Bundle.Mutants {
					// Every planted edit was tried — an invalid or timed-out
					// one included: the next generator must not re-roll it
					// because it went unjudged. Render says what happened.
					t := Tried{Path: m.Path, ParentSHA256: m.ParentSHA256, ID: m.MutantID,
						Span: lang.LineRange{Start: m.SpanStart, End: m.SpanEnd}, Shape: m.Shape,
						Outcome: m.Outcome, KilledBy: m.KilledBy, Proven: m.Proven}
					// The hunk, when the entry carries source (the local
					// entry always does; a pushed one only with
					// --push-source). A row without one is a bare outcome.
					if search, replace, ok := adequacy.DecodeHunk(m.Code); ok {
						t.Search, t.Replace = search, replace
					}
					upsert(t)
				}
			}
		}
		if err := filepath.WalkDir(source, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			if filepath.Base(filepath.Dir(path)) == auditpush.ScansSubdir {
				return nil // ledger entries: read above
			}
			if strings.ToLower(filepath.Ext(path)) == ".json" {
				files = append(files, path)
			}
			return nil
		}); err != nil {
			return nil, fmt.Errorf("prior: walking %s: %w", source, err)
		}
	} else {
		files = []string{source}
	}
	sort.Strings(files)
	for _, f := range files {
		tried, lerr := fromDocument(f)
		if lerr != nil {
			return nil, lerr
		}
		p.sources++
		for _, t := range tried {
			upsert(t)
		}
	}
	p.byPath = mergeTried(all)
	return p, nil
}

// mergeTried folds every record of one EDIT into one Tried, whatever order
// the records arrived in, and orders the result totally.
//
// The key identifies the edit, not the mutant id: ids are positional per
// run ("s0/m1" is every run's first mutant of shard 0), so a key of (sha,
// id) folded a later run's different edit into an earlier one — dropping
// it, undercounting, and grafting one run's hunk onto another run's line
// (found by a Claude Code reviewer, let stand by Codex). The span joins
// the key, and the HUNK joins it whenever a record carries one: two runs'
// different edits at one place never merge. A record with no hunk (a
// pushed row without --push-source; a cache hit that ran no dev pass) is
// folded into the one hunked edit at its (sha, id, span) when there is
// exactly one — and kept as its own bare record when there are several,
// because nothing says which edit it was, and a spare "tried" line costs
// less than a lost one. A first cut merged by arrival order, so the same
// two sources gave different priors depending on which was read first
// (a Cursor read of the fix, confirmed by reasoning).
func mergeTried(all []Tried) map[string][]Tried {
	fold := func(into *Tried, t Tried) {
		if into.Search == "" && t.Search != "" {
			into.Search, into.Replace = t.Search, t.Replace
		}
		if into.Outcome == "" && t.Outcome != "" {
			into.Outcome, into.KilledBy, into.Proven = t.Outcome, t.KilledBy, t.Proven
		}
		if into.Shape == "" {
			into.Shape = t.Shape
		}
		if into.Span.IsZero() {
			into.Span = t.Span
		}
	}
	type group struct {
		hunked map[string]*Tried // search+replace → the edit
		bare   *Tried
	}
	base := func(t Tried) string {
		return fmt.Sprintf("%s\x00%s\x00%s\x00%d-%d", t.Path, t.ParentSHA256, t.ID, t.Span.Start, t.Span.End)
	}
	groups := map[string]*group{}
	var order []string
	for _, t := range all {
		k := base(t)
		g := groups[k]
		if g == nil {
			g = &group{hunked: map[string]*Tried{}}
			groups[k] = g
			order = append(order, k)
		}
		if t.Search == "" {
			if g.bare == nil {
				tt := t
				g.bare = &tt
			} else {
				fold(g.bare, t)
			}
			continue
		}
		hk := t.Search + "\x00" + t.Replace
		if have := g.hunked[hk]; have != nil {
			fold(have, t)
		} else {
			tt := t
			g.hunked[hk] = &tt
		}
	}
	out := map[string][]Tried{}
	for _, k := range order {
		g := groups[k]
		if g.bare != nil && len(g.hunked) == 1 {
			for _, h := range g.hunked {
				fold(h, *g.bare)
			}
			g.bare = nil
		}
		for _, h := range g.hunked {
			out[h.Path] = append(out[h.Path], *h)
		}
		if g.bare != nil {
			out[g.bare.Path] = append(out[g.bare.Path], *g.bare)
		}
	}
	for path := range out {
		sort.Slice(out[path], func(i, j int) bool { return lessTried(out[path][i], out[path][j]) })
	}
	return out
}

// sorted is tried in lessTried order, as a copy: Render and Digest read
// the same edits in the same order whatever order a caller holds them in.
func sorted(tried []Tried) []Tried {
	out := append([]Tried(nil), tried...)
	sort.Slice(out, func(i, j int) bool { return lessTried(out[i], out[j]) })
	return out
}

// lessTried is a total order over edits: by line, then span end, then id,
// then parent hash, then the hunk — so Render and Digest never depend on
// the order the sources were read in.
func lessTried(a, b Tried) bool {
	switch {
	case a.Span.Start != b.Span.Start:
		return a.Span.Start < b.Span.Start
	case a.Span.End != b.Span.End:
		return a.Span.End < b.Span.End
	case a.ID != b.ID:
		return a.ID < b.ID
	case a.ParentSHA256 != b.ParentSHA256:
		return a.ParentSHA256 < b.ParentSHA256
	case a.Search != b.Search:
		return a.Search < b.Search
	default:
		return a.Replace < b.Replace
	}
}

func fromDocument(path string) ([]Tried, error) {
	set, err := adequacy.ReadMutantSet(path)
	if err != nil {
		return nil, fmt.Errorf("prior: %s: %w", path, err)
	}
	var out []Tried
	for file, e := range set.Files {
		for _, m := range e.Mutants {
			out = append(out, Tried{
				Path: file, ParentSHA256: e.ParentSHA256, ID: m.ID, Span: m.Span,
				Search: m.Search, Replace: m.Replace,
				Shape: adequacy.ShapeOfHunk(m.Search, m.Replace),
			})
		}
	}
	return out, nil
}

// ErrDifferentVersion is the same-bytes rule refusing: the source holds
// edits for this path, but recorded against other bytes. ErrNoVersion is
// the other refusal: the source holds edits for this path recorded against
// NO bytes at all (no parent hash), which is not "a different version" and
// must not be reported as one.
var (
	ErrDifferentVersion = errors.New("prior: recorded against a different version of the file")
	ErrNoVersion        = errors.New("prior: recorded with no version of the file to match against")
)

// For returns the edits tried on path at exactly sha, sorted by line. It
// returns ErrDifferentVersion when the source knows the path only under
// other hashes, and (nil, nil) when it never saw the path at all.
func (p *Prior) For(path, sha string) ([]Tried, error) {
	if p == nil {
		return nil, nil
	}
	all := p.byPath[path]
	if len(all) == 0 {
		return nil, nil
	}
	var same []Tried
	anyVersion := false
	for _, t := range all {
		if t.ParentSHA256 == sha {
			same = append(same, t)
		}
		if t.ParentSHA256 != "" {
			anyVersion = true
		}
	}
	if len(same) == 0 {
		if !anyVersion {
			return nil, ErrNoVersion
		}
		return nil, ErrDifferentVersion
	}
	return same, nil
}

// Digest is a stable fingerprint of a set of tried edits — what the verdict
// records and the cache key carries. Two runs handed the same prior share
// it; a run handed none has "".
func Digest(tried []Tried) string {
	if len(tried) == 0 {
		return ""
	}
	h := sha256.New()
	for _, t := range sorted(tried) {
		// The hunk is in the hash: Render quotes it, so two priors that
		// hand the generator different text must never share a digest.
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00%d-%d\x00%s\x00%s\x00%s\x00%v\x00%s\x00%s\n", t.Path, t.ParentSHA256, t.ID, t.Span.Start, t.Span.End, t.Shape, t.Outcome, t.KilledBy, t.Proven, t.Search, t.Replace)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// MaxRendered bounds how many edits the paragraph lists; the rest are
// summarised by count so a prompt never grows without bound.
const MaxRendered = 40

// Render is the paragraph the generator reads. It names each edit by place
// and shape, quotes the hunk when there is one, and says what happened to
// it — then asks for DIFFERENT faults. Nothing here is an instruction to
// avoid a place because it was covered; a killed edit says the suite watches
// that place, a proven survivor says a gap is already on record, an
// unproven survivor says the last run could not tell — all three are
// reasons to spend this run elsewhere.
func Render(tried []Tried) string {
	if len(tried) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "ALREADY TRIED on this exact version of the file (%d edit(s) from earlier runs). Do NOT repeat these edits or their shape at the same place; plant DIFFERENT faults — other decision points, other kinds of change:\n", len(tried))
	ordered := sorted(tried)
	for i, t := range ordered {
		if i == MaxRendered {
			// The edits are sorted by line, so the cut always drops the
			// FILE'S TAIL; say where the undisclosed ones are, or the
			// generator is told to plant elsewhere and pointed nowhere.
			// The tail of the ORDERED list — the caller's order is not
			// what was listed (review dcd0874fecb8#R1: the range named
			// was of whichever edits sat past index 40 in the caller's
			// slice).
			rest := ordered[MaxRendered:]
			fmt.Fprintf(&b, "  … and %d more, %s — not listed here, but tried; plant elsewhere in that stretch too.\n", len(rest), spanOf(rest))
			break
		}
		where := "somewhere in the file"
		if !t.Span.IsZero() {
			if t.Span.Start == t.Span.End {
				where = fmt.Sprintf("line %d", t.Span.Start)
			} else {
				where = fmt.Sprintf("lines %d–%d", t.Span.Start, t.Span.End)
			}
		}
		shape := t.Shape
		if shape == "" {
			shape = "unclassified"
		}
		fmt.Fprintf(&b, "  - %s, %s", where, shape)
		if t.Search != "" {
			fmt.Fprintf(&b, ": `%s` → `%s`", oneLine(t.Search), oneLine(t.Replace))
		}
		switch {
		case t.Outcome == "killed" && t.KilledBy != "":
			fmt.Fprintf(&b, " — KILLED by %s (the suite watches this)", t.KilledBy)
		case t.Outcome == "killed":
			b.WriteString(" — KILLED (the suite watches this)")
		case t.Outcome == "survived" && t.Proven:
			b.WriteString(" — SURVIVED, gap already proven and on record")
		case t.Outcome == "survived":
			b.WriteString(" — SURVIVED, unproven")
		case t.Outcome == "invalid":
			b.WriteString(" — INVALID (did not compile or was not a fault); do not plant it again")
		case t.Outcome == "timed_out":
			b.WriteString(" — TIMED OUT under the suite; unjudged, not a gap")
		case t.Outcome != "":
			fmt.Fprintf(&b, " — %s", t.Outcome)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func oneLine(s string) string {
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	if r := []rune(s); len(r) > 80 {
		s = string(r[:77]) + "…"
	}
	return s
}

// spanOf names the lines a run of edits covers, for the summary line.
func spanOf(tried []Tried) string {
	lo, hi := 0, 0
	for _, t := range tried {
		if t.Span.IsZero() {
			continue
		}
		if lo == 0 || t.Span.Start < lo {
			lo = t.Span.Start
		}
		if t.Span.End > hi {
			hi = t.Span.End
		}
	}
	if lo == 0 {
		return "at unrecorded places"
	}
	return fmt.Sprintf("between lines %d and %d", lo, hi)
}

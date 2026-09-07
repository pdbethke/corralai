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
	merged := map[string]map[string]*Tried{} // path → edit key → tried
	// The key identifies the EDIT, not the mutant id: ids are positional
	// per run ("s0/m1" is every run's first mutant of shard 0), so a key of
	// (sha, id) folded a later run's different edit into an earlier one —
	// dropping it, undercounting, and grafting one run's hunk onto another
	// run's line. Found by a Claude Code reviewer, let stand by Codex. The
	// span joins the key; two records of the SAME edit (the document's hunk
	// row and the ledger's outcome row, same run) share it, and two runs'
	// edits at the same place with the same positional id are told apart
	// by their hunks when both carry one.
	upsert := func(t Tried) {
		if merged[t.Path] == nil {
			merged[t.Path] = map[string]*Tried{}
		}
		key := fmt.Sprintf("%s\x00%s\x00%d-%d", t.ParentSHA256, t.ID, t.Span.Start, t.Span.End)
		if have, ok := merged[t.Path][key]; ok && have.Search != "" && t.Search != "" && have.Search != t.Search {
			key += "\x00" + t.Search
		}
		if have, ok := merged[t.Path][key]; ok {
			// Merge: the document brings the hunk, the ledger the outcome.
			if have.Search == "" && t.Search != "" {
				have.Search, have.Replace = t.Search, t.Replace
			}
			if have.Outcome == "" && t.Outcome != "" {
				have.Outcome, have.KilledBy, have.Proven = t.Outcome, t.KilledBy, t.Proven
			}
			if have.Shape == "" {
				have.Shape = t.Shape
			}
			if have.Span.IsZero() {
				have.Span = t.Span
			}
			return
		}
		tt := t
		merged[t.Path][key] = &tt
	}
	var files []string
	if st.IsDir() {
		// A ledger directory's entries first: they carry outcomes AND
		// hunks in one document each. Retracted entries are skipped
		// (auditpush.ScanEntries): a retracted run did not happen, as far
		// as the next exam is concerned.
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
					if m.Code != "" {
						t.Replace = m.Code
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
	for path, byID := range merged {
		for _, t := range byID {
			p.byPath[path] = append(p.byPath[path], *t)
		}
		sort.Slice(p.byPath[path], func(i, j int) bool {
			a, b := p.byPath[path][i], p.byPath[path][j]
			if a.Span.Start != b.Span.Start {
				return a.Span.Start < b.Span.Start
			}
			return a.ID < b.ID
		})
	}
	return p, nil
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
	for _, t := range tried {
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
	for i, t := range tried {
		if i == MaxRendered {
			// The edits are sorted by line, so the cut always drops the
			// FILE'S TAIL; say where the undisclosed ones are, or the
			// generator is told to plant elsewhere and pointed nowhere.
			rest := tried[MaxRendered:]
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

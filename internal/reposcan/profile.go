// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"sort"

	"github.com/pdbethke/corralai/internal/lang"
)

// LanguageStat is one language's share of a repository, as the enumeration
// already knows it.
type LanguageStat struct {
	Lang string
	// Auditable is source files with a paired test — what a scan can actually
	// grade.
	Auditable int
	// NoPairedTest is source files this language recognises that have no test
	// paired to them. Free, and often the most actionable number in the whole
	// report: nothing about it requires a model, a jail, or a key.
	NoPairedTest int
	// Ambiguous is source files whose resolved test path is also claimed by
	// another source file. These are exactly where corral KNOWS it is
	// uncertain, so they are the right place to ask a human rather than guess.
	Ambiguous int
	// TestFiles is the repo's own test files. Counted separately and never
	// folded into NoPairedTest: doing so would inflate "files with no test" and
	// turn a well-tested repo into a scary one.
	TestFiles int
}

// Total is every file this language accounts for.
func (s LanguageStat) Total() int {
	return s.Auditable + s.NoPairedTest + s.Ambiguous + s.TestFiles
}

// BuildLanguageProfile turns the enumeration's own results into a per-language
// inventory.
//
// The scan already detects a language for every walked file — that is where the
// "no-language" tally comes from — but keeps the result only as a rejection
// count. So it can say "121 files aren't code" and cannot say "this repo is
// Python: 68 files, 6 auditable, 21 with no paired test", which is both more
// useful and entirely free: no model call, no jail, no key, no money.
//
// Files no plugin recognises are deliberately absent rather than grouped under
// an empty or invented name: a README and an .editorconfig are not a
// "markdown project", and the existing no-language count already reports them
// honestly as "not code corral audits".
func BuildLanguageProfile(cands []Candidate, excl []Exclusion) []LanguageStat {
	stats := map[string]*LanguageStat{}
	get := func(name string) *LanguageStat {
		if name == "" {
			return nil
		}
		if s, ok := stats[name]; ok {
			return s
		}
		s := &LanguageStat{Lang: name}
		stats[name] = s
		return s
	}

	for _, c := range cands {
		if s := get(c.Lang); s != nil {
			s.Auditable++
		}
	}

	for _, e := range excl {
		// Re-detect from the path: an Exclusion carries only Path and Reason,
		// and re-running the SAME lang.Detect the enumerator used keeps the two
		// from ever disagreeing about what a file is.
		name := ""
		if p, ok := lang.Detect(e.Path); ok {
			name = p.Name()
		}
		s := get(name)
		if s == nil {
			continue // genuinely not code corral audits
		}
		switch e.Reason {
		case ReasonNoPairedTest:
			s.NoPairedTest++
		case ReasonAmbiguousTest:
			s.Ambiguous++
		case ReasonIsTest:
			s.TestFiles++
		}
		// Every other reason (not-selected, skipped-dir, not-a-regular-file,
		// ungoaled, …) is deliberately NOT attributed here: those are scan
		// bookkeeping about files this profile has already counted elsewhere or
		// that say nothing about the repo's test posture. Counting them would
		// double-count a file and make Total() a number nobody can reconcile.
	}

	out := make([]LanguageStat, 0, len(stats))
	for _, s := range stats {
		out = append(out, *s)
	}
	// Most auditable first so the rows a reader can act on lead; ties broken by
	// name so the ordering is stable across identical runs (this feeds a report
	// and a UI, and reshuffling reads as churn that isn't there).
	sort.Slice(out, func(i, j int) bool {
		if out[i].Auditable != out[j].Auditable {
			return out[i].Auditable > out[j].Auditable
		}
		return out[i].Lang < out[j].Lang
	})
	return out
}

// SPDX-License-Identifier: Elastic-2.0

package recordview

import (
	"fmt"

	"github.com/pdbethke/corralai/internal/ledger"
)

// LanguageRow is one line of the Languages page.
type LanguageRow struct {
	Lang       string
	Local, CI  *ledger.ProofRun
	ProvedAGap bool
	Reason     string // set when Local and CI are both nil: why "—"
}

// WithLanguages computes the Languages page. langs is the language registry
// (lang.Names()), runnerLangs the runner table's ID→languages. self empty →
// LanguagesRefusal names the flag; every other page is unchanged. The rule
// for what counts lives in ledger.LanguageProofs; this only lays it out, so
// the public page and corral ui cannot disagree. A site with no ledger
// behind it (a warehouse) makes no claim about a chain and gets no page.
func (s Site) WithLanguages(langs []string, runnerLangs map[string][]string, self []string, selfFlag string) Site {
	if !s.HasLedger {
		return s
	}
	s.LanguagesBuilt = true
	s.Languages, s.LanguagesRefusal = nil, ""
	if len(self) == 0 {
		s.LanguagesRefusal = fmt.Sprintf("The Languages page needs %s: the repositories whose runs are self-audit and never count as proof.", selfFlag)
		return s
	}
	proofs, err := ledger.LanguageProofs(s.entries, s.checks, self, ledger.ProofFloor, runnerLangs)
	if err != nil {
		s.LanguagesRefusal = "The Languages page could not be computed: " + err.Error()
		return s
	}
	for _, l := range langs {
		p := proofs[l]
		row := LanguageRow{Lang: l, Local: p.Local, CI: p.CI, ProvedAGap: p.ProvedAGap}
		if p.Local == nil && p.CI == nil {
			switch {
			case p.Unchecked > 0:
				row.Reason = "signatures not checked: render with the ledger's public key"
			case p.BelowFloor > 0:
				row.Reason = "every run on this record predates generation " + ledger.ProofFloor
			default:
				row.Reason = "no run on this record"
			}
		}
		s.Languages = append(s.Languages, row)
	}
	return s
}

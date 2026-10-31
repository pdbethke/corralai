// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"encoding/json"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/reposcan"
	"github.com/pdbethke/corralai/internal/shadowpool"
)

// recordedInventory is ScanRow.Inventory: what the walk found before any audit
// ran, every number taken from the value the scan prints it from. It is JSON
// text on the ScanRow (which is compared whole, so no map field), and each key
// below is one printed figure:
//
//	excluded            the walk line's "N file(s) excluded from the audit"
//	excluded_by_reason  printExclusions' "N <reason>" tally
//	landing_not_built   noEvidenceSourcesNote's per-language refused-source count
//	languages           printLanguageProfile's per-language line
//	preflight           printPreflightReport's executed / not-measured counts
//	unsized_candidates  Rank's "N candidate(s) could not be sized" note
//
// Preflight and unsized_candidates are pointers: a scan that ran no pre-flight
// or no ranking has no count of either, and an absent key says so where a 0
// would say "measured, none".
type recordedInventory struct {
	Excluded         int                `json:"excluded"`
	ExcludedByReason map[string]int     `json:"excluded_by_reason,omitempty"`
	LandingNotBuilt  map[string]int     `json:"landing_not_built,omitempty"`
	Languages        []recordedLanguage `json:"languages,omitempty"`
	Preflight        *recordedPreflight `json:"preflight,omitempty"`
	UnsizedCands     *int               `json:"unsized_candidates,omitempty"`
}

// recordedLanguage is one reposcan.LanguageStat, with every bucket its
// printed line draws on (printLanguageProfile prints the sum of four of them
// as the language's source-file total).
type recordedLanguage struct {
	Lang            string `json:"lang"`
	Auditable       int    `json:"auditable"`
	NoPairedTest    int    `json:"no_paired_test"`
	Ambiguous       int    `json:"ambiguous"`
	LandingNotBuilt int    `json:"landing_not_built"`
	OtherExcluded   int    `json:"other_excluded"`
	TestFiles       int    `json:"test_files"`
}

// recordedPreflight is the coverage pre-flight's three bucket counts.
type recordedPreflight struct {
	Executed    int `json:"executed"`
	NotMeasured int `json:"not_measured"`
	Unexercised int `json:"unexercised"`
}

// exclusionsByReason is printExclusions' tally and the inventory's, one
// function so the two cannot count differently.
func exclusionsByReason(excl []reposcan.Exclusion) map[string]int {
	byReason := map[string]int{}
	for _, e := range excl {
		byReason[e.Reason]++
	}
	return byReason
}

// landingRefusalsByLang is how many sources each language had refused for
// their test landing, "unknown-language" for a path no plugin detects: the
// counts noEvidenceSourcesNote prints and the inventory records.
func landingRefusalsByLang(excl []reposcan.Exclusion) map[string]int {
	refused := map[string]int{}
	for _, e := range excl {
		if !reposcan.IsLandingRefusal(e.Reason) {
			continue
		}
		name := "unknown-language"
		if p, ok := lang.Detect(e.Path); ok {
			name = p.Name()
		}
		refused[name]++
	}
	return refused
}

// newRecordedInventory is the walk's inventory, taken from the candidates and
// exclusions the scan then prints (printLanguageProfile, printExclusions).
func newRecordedInventory(cands []reposcan.Candidate, excl []reposcan.Exclusion) *recordedInventory {
	inv := &recordedInventory{Excluded: len(excl), ExcludedByReason: exclusionsByReason(excl), LandingNotBuilt: landingRefusalsByLang(excl)}
	for _, s := range reposcan.BuildLanguageProfile(cands, excl) {
		inv.Languages = append(inv.Languages, recordedLanguage{
			Lang: s.Lang, Auditable: s.Auditable, NoPairedTest: s.NoPairedTest, Ambiguous: s.Ambiguous,
			LandingNotBuilt: s.LandingNotBuilt, OtherExcluded: s.OtherExcluded, TestFiles: s.TestFiles,
		})
	}
	return inv
}

// withUnsized records Rank's count of candidates it could not size; ranked is
// false for a scan that ran no ranking, which records none.
func (inv *recordedInventory) withUnsized(ranked bool, info reposcan.RankInfo) {
	if inv == nil || !ranked {
		return
	}
	n := info.Unsized
	inv.UnsizedCands = &n
}

// withPreflight records the pre-flight's buckets, from the same split
// printPreflightReport prints. A pre-flight that could not run measured
// nothing: its note is ScanRow.PreflightNote and there is no count here.
func (inv *recordedInventory) withPreflight(cm reposcan.CoverageMap, sources []string) {
	if inv == nil || !cm.Ran {
		return
	}
	f := splitPreflightFindings(sources, cm)
	inv.Preflight = &recordedPreflight{Executed: f.executed, NotMeasured: f.notMeasured, Unexercised: len(f.unexercised)}
}

// text is the ScanRow.Inventory value: "" for a scan that walked nothing.
func (inv *recordedInventory) text() string {
	if inv == nil {
		return ""
	}
	b, err := json.Marshal(inv)
	if err != nil {
		return ""
	}
	return string(b)
}

// shadowDrawJSON is ScanRow.ShadowDraw: each drawn seat's selection exactly as
// the draw made it (shadowpool.Selection's own JSON: role, language, every
// member's Beta parameters and sample, the member chosen, the seed as
// --shadow-seed accepts it, whether the scorecard was read and how many rows).
// "" unless a draw happened.
func shadowDrawJSON(sels []shadowpool.Selection) string {
	if len(sels) == 0 {
		return ""
	}
	b, err := json.Marshal(sels)
	if err != nil {
		return ""
	}
	return string(b)
}

// tokenCapReached is ScanRow.MaxTokensReached, read from the budget budgetLine
// prints from. nil when no cap was set (a nil budget).
func tokenCapReached(b *agentbackend.TokenBudget) *bool {
	if b == nil {
		return nil
	}
	hit, _, _, _ := b.Exhausted()
	return &hit
}

// recordedWriterAttempts is Row.WriterAttempts: the attempts-per-survivor
// spread writerModeDisclosure prints and the count of ungraded seats it prints
// beside the call count. The spread is absent when no seat reported an
// attempt count (nothing to take a minimum of), and the whole record is empty
// unless the per-survivor writer ran: a batched run has one seat for the whole
// file, and a verdict that never reached the writer has neither figure.
type recordedWriterAttempts struct {
	Min           *int `json:"min,omitempty"`
	Median        *int `json:"median,omitempty"`
	Max           *int `json:"max,omitempty"`
	SeatsUngraded int  `json:"seats_ungraded"`
}

func writerAttemptsJSON(mode string, attempts *advpool.TestsPerMutantSpread, ungraded int) string {
	if mode != advpool.WriterModePerSurvivor || (attempts == nil && ungraded == 0) {
		return ""
	}
	rec := recordedWriterAttempts{SeatsUngraded: ungraded}
	if attempts != nil {
		rec.Min, rec.Median, rec.Max = &attempts.Min, &attempts.Median, &attempts.Max
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return ""
	}
	return string(b)
}

// examCountsScored is a per-file mutant count for the ledger: nil unless the
// exam was scored (Verdict.DevScored), so a verdict that never graded reads
// as unknown and not as "none unmeasured".
func examCountsScored(v advpool.Verdict, n int) *int {
	if !v.DevScored {
		return nil
	}
	return &n
}

// scanTokenBudget is the budget budgetLine prints from for a --repo scan: the
// executor's, and none when the scan built no executor.
func scanTokenBudget(ex *localExecutor) *agentbackend.TokenBudget {
	if ex == nil {
		return nil
	}
	return ex.tokenBudget
}

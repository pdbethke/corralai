// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	"github.com/pdbethke/corralai/internal/repoindex"
	"github.com/pdbethke/corralai/internal/reposcan"
)

// scanInventory is the machine-readable form of what `certify --repo
// --dry-run` already prints: a repository's audit surface, computed with NO
// model call, NO jail and NO key.
//
// It exists so a UI (or a tenant's own tooling) can consume the inventory
// instead of scraping stdout — which is what the CI sweep does today, with
// PCRE, against a human-facing report nobody may reformat without breaking it.
//
// The schema deliberately reports the FUNNEL rather than a headline
// percentage. "6 auditable" out of 130 walked files is 4%, which reads as
// damning; out of 6 candidates it is 100%, which reads as dishonest. Neither is
// the truth. The truth is the sequence — walked, then not-code, then tests,
// then unpaired, then auditable — and a consumer given every term can render it
// honestly. Collapsing it here would bake a spin decision into the API.
type scanInventory struct {
	Repo string `json:"repo"`
	// Walked is every file the scan looked at, the only honest denominator.
	Walked int `json:"walked"`
	// Candidates are source files with a paired test — the auditable surface.
	Candidates int `json:"candidates"`
	// Jobs is what this scan WOULD run, after --top/--diff-base bounding.
	// Distinct from Candidates on purpose: a bound is a choice, not a property
	// of the repository, and conflating them hides that the operator narrowed
	// the surface.
	Jobs int `json:"jobs"`
	// Ranking names how candidates were ordered, because it is not always the
	// same: a shallow clone has no usable history, so churn silently
	// degrades to size alone. A consumer must be able to say which it got
	// rather than imply a signal that was never available.
	Ranking   string             `json:"ranking"`
	Languages []languageStatJSON `json:"languages"`
	// ExcludedByReason is the machine-stable reason tally.
	//
	// CAUTION for consumers: Candidates + these counts does NOT necessarily
	// equal Walked. The two overlap — a file can be a candidate AND excluded
	// (an "ungoaled" candidate is both) — which is why Walked is reported as
	// its own measured value and must never be reconstructed by summing. A UI
	// that derived it would silently disagree with corral about the size of
	// the repository.
	ExcludedByReason map[string]int  `json:"excluded_by_reason"`
	Auditable        []auditableJSON `json:"auditable"`
}

type languageStatJSON struct {
	Lang         string `json:"lang"`
	Auditable    int    `json:"auditable"`
	NoPairedTest int    `json:"no_paired_test"`
	// Ambiguous is where corral KNOWS its pairing is uncertain — the right
	// place for a UI to ask a human rather than present a guess as a fact.
	Ambiguous int `json:"ambiguous"`
	TestFiles int `json:"test_files"`
}

type auditableJSON struct {
	Path string `json:"path"`
	// TestPath is the pairing corral INFERRED from filename convention. It is
	// not evidence that the file's real tests live there: psf/requests pairs
	// adapters.py to an 8-line test_adapters.py while its actual coverage sits
	// in a 108KB test_requests.py. A UI showing this should invite correction,
	// not present it as settled.
	TestPath string `json:"test_path"`
	Lang     string `json:"lang"`
	// Complexity is ABSENT (nil) unless corral actually measured it. A
	// signature extractor exists only for Go and Python; ExtractSignatures
	// returns "no signature extractor for language" for ruby, javascript and
	// typescript. Emitting 0 — or the floor of 1 — for those would render
	// "this code is trivial" where the truth is "never measured", the same
	// not-measured-as-measurement shape as a NULL kill rate printed as 0.00.
	Complexity *complexityJSON `json:"complexity,omitempty"`
}

// complexityJSON is cyclomatic-style complexity: 1 + the branch, loop, case,
// catch and boolean-operator nodes in a symbol's tree-sitter subtree. That is
// the same decision-point approximation gocyclo, radon and eslint's
// `complexity` rule use in practice — true McCabe walks a control-flow graph,
// which essentially no production tool does. The METHOD is therefore standard,
// but the numbers are comparable WITHIN corral rather than against a specific
// tool's report: tools disagree about whether `else`, `default`, ternaries and
// boolean operators count, and corral's per-language node sets are its own.
//
// Max and Total are both carried rather than collapsed into one score. Total
// tracks file length — a long file of getters outranks a short file with one
// hairy branch — while Max names the worst single symbol. Which should drive a
// ranking is a judgement, so the API supplies both and leaves the choice to the
// consumer, exactly as the funnel does instead of shipping a percentage.
type complexityJSON struct {
	Symbols int `json:"symbols"`
	Max     int `json:"max"`
	Total   int `json:"total"`
}

// fileComplexity measures one file, or reports nil when corral cannot. nil is
// the honest answer for an unmeasured language and must never be softened into
// a zero value.
func fileComplexity(path string, src []byte, langName string) *complexityJSON {
	sigs, err := repoindex.ExtractSignatures(string(src), langName)
	if err != nil || len(sigs) == 0 {
		return nil
	}
	out := &complexityJSON{Symbols: len(sigs)}
	for _, s := range sigs {
		out.Total += s.Complexity
		if s.Complexity > out.Max {
			out.Max = s.Complexity
		}
	}
	return out
}

// writeScanInventory emits the inventory as indented JSON.
func writeScanInventory(w io.Writer, inv scanInventory) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(inv)
}

// buildScanInventory assembles the inventory from the same values the human
// report is built from, so the two can never disagree about a repository.
func buildScanInventory(repo string, walked int, ranking string, cands []reposcan.Candidate, jobs int, excl []reposcan.Exclusion) scanInventory {
	return buildScanInventoryAt("", repo, walked, ranking, cands, jobs, excl)
}

// buildScanInventoryAt is buildScanInventory with a repo root, so each
// auditable file can be read and measured. root == "" skips measurement
// entirely (the unit-test path): complexity is then absent rather than zero,
// which is the same honest answer an unmeasured language gets.
func buildScanInventoryAt(root, repo string, walked int, ranking string, cands []reposcan.Candidate, jobs int, excl []reposcan.Exclusion) scanInventory {
	inv := scanInventory{
		Repo:             repo,
		Walked:           walked,
		Candidates:       len(cands),
		Jobs:             jobs,
		Ranking:          ranking,
		ExcludedByReason: map[string]int{},
		Auditable:        make([]auditableJSON, 0, len(cands)),
	}
	for _, e := range excl {
		inv.ExcludedByReason[e.Reason]++
	}
	for _, s := range reposcan.BuildLanguageProfile(cands, excl) {
		inv.Languages = append(inv.Languages, languageStatJSON{
			Lang: s.Lang, Auditable: s.Auditable, NoPairedTest: s.NoPairedTest,
			Ambiguous: s.Ambiguous, TestFiles: s.TestFiles,
		})
	}
	for _, c := range cands {
		a := auditableJSON{Path: c.Path, TestPath: c.TestPath, Lang: c.Lang}
		if root != "" {
			// Best-effort and local: tree-sitter parsing, no model call. An
			// unreadable file yields no measurement rather than a zero.
			if src, err := os.ReadFile(filepath.Join(root, c.Path)); err == nil { // #nosec G304 -- operator-supplied repo root + enumerated relative path
				a.Complexity = fileComplexity(c.Path, src, c.Lang)
			}
		}
		inv.Auditable = append(inv.Auditable, a)
	}
	return inv
}

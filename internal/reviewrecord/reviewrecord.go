// SPDX-License-Identifier: Elastic-2.0

// Package reviewrecord holds the review record's types and nothing else:
// what a review ledger entry decodes into. It imports only the standard
// library so the ledger code can compile for the browser (GOOS=js
// GOARCH=wasm) without pulling in the review engine (clip, treedirs, lang,
// sandbox, statedir). internal/review aliases every name here, so the
// engine and its callers are unchanged. JSON tags and field order are the
// on-disk contract: entries are hashed over their bytes.
package reviewrecord

import "time"

// The tiers, as the design names them. Declared by the reviewer; RECORDED by
// the run, which only ever demotes.
const (
	TierReproduced = "REPRODUCED"
	TierCodeRead   = "CODE-READ"
	TierHypothesis = "HYPOTHESIS"
)

// Finding is one claim. Declared is the tier the reviewer asserted; Tier is
// the tier the record carries after the run — equal to Declared, or lower,
// with Demoted saying why. A finding's identity within its review is ID
// (R1, R2, …); across the record it is the review entry's hash plus that id.
type Finding struct {
	ID       string `json:"id"`
	Claim    string `json:"claim"`
	Declared string `json:"declared"`
	Tier     string `json:"tier"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Severity string `json:"severity,omitempty"`
	// Script is a POSIX sh script the run executed from the repository root
	// at the reviewed commit. The contract the brief states: exit 0 if and
	// only if the defect is demonstrated, and print the evidence. Stdout is
	// the tail of what it printed (stderr folded in); ExitCode is how it
	// ended, nil when it never ran (no script, or the run refused it).
	Script   string `json:"script,omitempty"`
	Stdout   string `json:"stdout,omitempty"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Demoted  string `json:"demoted,omitempty"`
	// Unrun is why the HARNESS could not run the script — the worktree
	// gone, sh missing, a timeout — as distinct from a script that ran and
	// did not demonstrate the claim. A finding with Unrun set was never put
	// to the tree: it has no outcome, and grades nobody (found by review
	// 61dc210a39fd#R1: an infrastructure failure was being charged to the
	// reviewer as a claim that fell "by execution").
	Unrun string `json:"unrun,omitempty"`
	// Refutation is the verifier seat's answer, when one ran (see
	// verifier.go). A refutation that reproduced demotes this finding.
	Refutation *Refutation `json:"refutation,omitempty"`
}

// Review is the record of one review: what was reviewed, by which model,
// on what substrate, the findings as recorded, the sound list, and the
// opinion — the prose, which is carried and never signed on its own.
type Review struct {
	Repo   string `json:"repo"`
	Commit string `json:"commit"`
	// The audited party: who made the commit, by name — author, committer,
	// and the Co-authored-by trailers one per line (an agent, in code an
	// agent helped write). Empty when the checkout could not say.
	Author        string `json:"author,omitempty"`
	Committer     string `json:"committer,omitempty"`
	CoAuthors     string `json:"co_authors,omitempty"`
	Scope         string `json:"scope"`
	ReviewerModel string `json:"reviewer_model"`
	// ReviewerTool / VerifierTool name the coding CLI and its version when
	// the seat was agentic ("2.1.263 (Claude Code)"), "" for an API seat:
	// a row in models rank is a tool AND a version AND a model.
	ReviewerTool string `json:"reviewer_tool,omitempty"`
	VerifierTool string `json:"verifier_tool,omitempty"`
	// ReviewerModelResolved / VerifierModelResolved report whether the model
	// field beside them names an actual MODEL, or merely the tool that ran
	// one. An agentic seat given without a pin ("claude-code", "antigravity")
	// records the AGENT in the model field — which is correct for the
	// decorrelation rule, because that rule compares seats, and wrong as a
	// model: nothing downstream can say whether Sonnet or Opus answered.
	//
	// Any per-model comparison MUST filter on these. A tool name sitting in
	// a model column is a could-not-measure, and without this field it reads
	// as a measurement — the defect this repository keeps rediscovering.
	//
	// Absent on every entry written before these fields existed, which reads
	// as false: unresolved. That is the safe default and it is why the field
	// is stated positively. Entries are hashed over their bytes on disk (see
	// auditpush.EntryHash), so adding them does not disturb an older hash.
	ReviewerModelResolved bool `json:"reviewer_model_resolved,omitempty"`
	VerifierModelResolved bool `json:"verifier_model_resolved,omitempty"`
	// Lang is the language most of the shown files are in, "" when mixed
	// or unknown: the row's language dimension in `models rank`.
	Lang string `json:"lang,omitempty"`
	// VerifierModel and VerifierOpinion are the third seat's, empty when no
	// verifier ran. The decorrelation rule holds: never the reviewer's model.
	VerifierModel   string `json:"verifier_model,omitempty"`
	VerifierOpinion string `json:"verifier_opinion,omitempty"`
	// VerifierNote says what of the verifier's reply the run could not
	// use: a verdict on an id the review does not have, a second verdict
	// on one it does (the first stands). Never dropped silently.
	VerifierNote string    `json:"verifier_note,omitempty"`
	Substrate    string    `json:"substrate"`
	StartedAt    time.Time `json:"started_at"`
	Findings     []Finding `json:"findings"`
	// Sound is what the reviewer looked at and could not break. Required:
	// absence of findings in a subsystem nobody looked at is not evidence,
	// and this is what makes the review's scope legible.
	Sound   []string `json:"sound"`
	Opinion string   `json:"opinion"`
	// FilesShown is what the reviewer was actually given, so a reader can
	// tell "found nothing in X" from "never saw X".
	FilesShown   []string `json:"files_shown"`
	BytesShown   int      `json:"bytes_shown"`
	Truncated    bool     `json:"truncated,omitempty"`
	InputTokens  int64    `json:"input_tokens,omitempty"`
	OutputTokens int64    `json:"output_tokens,omitempty"`
	// Calls is how many model calls the token counts above were folded from
	// (the reviewer's, and the verifier's when it answered), and
	// UnmeteredCalls how many of those reported no usage at all — an agent
	// seat on the `text` wire. When the two are equal the token counts
	// measure nothing and must be read as "not metered", never as a free
	// review (auditpush.TokensUnmetered). Absent on an entry written before
	// they existed, which reads as 0/0: unknown, and never unmetered.
	Calls          int `json:"calls,omitempty"`
	UnmeteredCalls int `json:"unmetered_calls,omitempty"`
	// Coverage is the run's own note on how much the review can be said to
	// cover: "" when the sound list and the findings between them say what
	// was looked at, else why they do not (see CoverageNote). A review with
	// no findings and a thin sound list is a review whose coverage is
	// unknown — and a verifier told there is nothing to refute says so too.
	Coverage string `json:"coverage,omitempty"`
	// StatementSHA256 is the sha256 of the --attest statement written for
	// this review, when one was: the statement is written FIRST (it hashes
	// the reproductions), then the entry, so the entry names the statement
	// and the statement names the reproductions, and a reader holding
	// either can find the other.
	StatementSHA256 string `json:"statement_sha256,omitempty"`
}

// Refutation is the verifier's answer to one finding. Declared is the tier
// the verifier asserted for its refutation; Tier is what the record
// carries after its script ran — the same demote-only rule the reviewer's
// findings live under. Verdict is VerdictRefuted or VerdictStands.
type Refutation struct {
	Model    string `json:"model"`
	Verdict  string `json:"verdict"`
	Argument string `json:"argument"`
	Declared string `json:"declared,omitempty"`
	Tier     string `json:"tier,omitempty"`
	Script   string `json:"script,omitempty"`
	Stdout   string `json:"stdout,omitempty"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Demoted  string `json:"demoted,omitempty"`
	// Unrun is why the harness could not run the script (see
	// Finding.Unrun): a refutation nobody executed moves nothing.
	Unrun string `json:"unrun,omitempty"`
}

// The verifier's two verdicts.
const (
	VerdictRefuted = "REFUTED"
	VerdictStands  = "STANDS"
)

// Counts summarises the record's tiers.
func (r Review) Counts() (reproduced, codeRead, hypothesis int) {
	for _, f := range r.Findings {
		switch f.Tier {
		case TierReproduced:
			reproduced++
		case TierCodeRead:
			codeRead++
		default:
			hypothesis++
		}
	}
	return
}

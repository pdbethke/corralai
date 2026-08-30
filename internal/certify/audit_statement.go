// SPDX-License-Identifier: Elastic-2.0

package certify

// The audit statement — the receipt a team can hand to someone who was not
// there.
//
// BuildAttestation (certify.go) describes a BUILD: what produced an artifact.
// This describes an AUDIT: whether a change's own tests would have caught it
// being broken. Same in-toto envelope, deliberately different predicate type,
// because calling a test-adequacy result "SLSA provenance" would be borrowing
// a word that means something else — and a verifier filtering on predicate
// type would then collect both and be unable to tell them apart.
//
// It exists to be consumed by GitHub's attestation API (actions/attest), which
// signs keylessly through OIDC + Sigstore. That matters more than the format:
// a --local run signs with a key generated on the machine, so on an ephemeral
// CI runner every run is signed by a fresh key that chains to nothing. An
// attestation chains to the repository and the workflow that produced it, which
// is the thing a reviewer actually wants to check.

// AuditPredicateType is corral's own predicate type. Minted rather than reused
// so `gh attestation verify --predicate-type` selects audit verdicts and
// nothing else.
const AuditPredicateType = "https://corralai.dev/certify/audit/v1"

// AuditedFile is one file's result inside an audit statement.
type AuditedFile struct {
	Path string `json:"path"`
	// KillRate is a POINTER so that "not measured" is expressible. An
	// uncovered file — no test executes it — has no rate to sign: the report
	// withholds the number and the ledger stores NULL, and a statement that
	// signed a 0.0 anyway would be the one place the withheld number leaked
	// out, over a signature. nil is omitted from the attestation entirely.
	KillRate     *float64 `json:"killRate,omitempty"`
	Survivors    int      `json:"survivors"`
	ProvenMissed int      `json:"provenMissed"`
	// The honesty flags travel WITH the numbers, never separately. A
	// provenMissed of 0 means "nothing was proven" rather than "the suite is
	// clean" whenever one of these is set, and a consumer reading the number
	// without them would draw the opposite conclusion from the right data.
	TimedOut         bool `json:"timedOut,omitempty"`
	TestWriterFailed bool `json:"testWriterFailed,omitempty"`
	PoolTestUnsound  bool `json:"poolTestUnsound,omitempty"`
	// Which measurement the rate above IS — the tests coverage evidence
	// showed execute this file (TestSelection, SelectedTests of SuiteTests),
	// or the whole suite and why (SelectionFallback). Uncovered says no test
	// executes the file at all, which is why KillRate may be nil. A signed
	// number without the question it answers is not verifiable.
	TestSelection     string `json:"testSelection,omitempty"`
	SelectedTests     int    `json:"selectedTests,omitempty"`
	SuiteTests        int    `json:"suiteTests,omitempty"`
	SelectionFallback string `json:"selectionFallback,omitempty"`
	Uncovered         bool   `json:"uncovered,omitempty"`
	// And at which GRAIN the rate was measured. PerMutant says each mutant
	// was graded by the tests that reach its own lines, which makes
	// SelectedTests the file's UNION rather than any mutant's denominator —
	// so the spread travels with it. A verifier handed "0.65 over 234 of
	// 620" and nothing else would reasonably conclude every mutant faced
	// 234 tests; the spread is what refutes that.
	// TestsPerMutant is nil — never a zero-filled struct — when no spread was
	// measured: an ordinary shared-command run, or a per-mutant run whose
	// every mutant was rejected by the compile gate before anything could be
	// graded. A signed 0-to-0 range would be a measurement nobody made.
	PerMutant bool `json:"perMutant,omitempty"`
	// ProvenByAuthoredAlone: provenMissed was established by the authored
	// test alone — the strongest form of the claim, and the one a verifier
	// should expect from any selection-graded file.
	ProvenByAuthoredAlone bool `json:"provenByAuthoredAlone,omitempty"`
	// WriterMode is HOW the writer seat attacked this file's survivors:
	// "per-survivor" (one call, one repair budget and one proof each) or
	// "batched" (one call carrying them all). Omitted when the run named no
	// mode — signing either spelling for a run that recorded neither would
	// attest to a fact nobody established.
	WriterMode     string                `json:"writerMode,omitempty"`
	TestsPerMutant *TestsPerMutantSpread `json:"testsPerMutant,omitempty"`
	// Trees and ConcurrencyNote disclose how many private trees the
	// workspace substrate's probe scored this file with at once, or —
	// when it granted only one — why. A MEASURED one is signed like any
	// other count: a verifier must be able to see the exam ran at
	// concurrency 1 without inferring it from an absent key. Trees < 1 is
	// the other thing entirely — nothing measured it (the jail substrate
	// builds no trees; a cached pre-concurrency verdict carries none) — and
	// is omitted rather than signed as a 0 no measurement supports.
	// ConcurrencyNote signs only when the substrate had a reason to give.
	Trees           int    `json:"trees,omitempty"`
	ConcurrencyNote string `json:"concurrencyNote,omitempty"`
	// SharedDirs names the dependency directories symlinked into every tree
	// rather than copied. Signed alongside the count because "6 trees" is an
	// isolation claim and these are the exact places it does not hold: a
	// suite that writes into its own .venv during a run writes through a
	// shared link. Absent when nothing was shared.
	SharedDirs []string `json:"sharedDirs,omitempty"`
}

// TestsPerMutantSpread is how many tests each graded mutant ran: the
// smallest, the middle and the largest.
//
// It is this package's own copy of advpool.TestsPerMutantSpread rather than
// that type: advpool imports certify (it signs its verdicts), so certify
// cannot import advpool back. The pointer is the load-bearing half either
// way — an unmeasured spread is absent, not three zeros.
type TestsPerMutantSpread struct {
	Min    int `json:"min"`
	Median int `json:"median"`
	Max    int `json:"max"`
}

// AuditStatement describes one scan: which commit, which files, what was
// measured, and under which thresholds.
type AuditStatement struct {
	Repo    string
	Commit  string
	Files   []AuditedFile
	Audited int
	// Candidates is every file the scan COULD have audited. Carried so a
	// reader can see the denominator: "3 files clean" means something
	// different out of 4 than out of 400, and a statement that omitted it
	// would flatter the run by silence.
	Candidates      int
	ModelsByRole    map[string]string
	MinKillRate     *float64
	MaxProvenMissed *int
	Passed          bool
	// ScanID is the local scan ledger's row id for this run (see
	// scanstore.Store.Record), or 0 when the ledger was not written
	// (--record was not given). 0 is the honest value here, not a sentinel
	// for "unknown": a reader who wants a real join key already needed
	// --record, and a statement that fabricated a nonzero id would claim a
	// ledger row that does not exist.
	ScanID int64 `json:"scanId"`
	// WarehouseRowsSHA256 is the sha256 of the canonical JSON of the
	// warehouse rows this scan produces — computed BEFORE this statement is
	// signed, from the same rows a --push would write (with ScanID set and
	// StatementSHA256 left empty, since the statement's own hash cannot
	// depend on itself). It lets a holder of BOTH the statement and the
	// pushed rows check them against each other in either order, instead of
	// only trusting the row's one-way pointer back.
	//
	// A SELF-CONSISTENCY CHECK OVER THE BUNDLE, NOT A REPRODUCIBILITY CLAIM
	// OVER THE WAREHOUSE. Two of the writer's conversions are lossy on
	// purpose — kill_rate is stored NULL for an uncovered file, and the
	// tests_per_mutant_* columns are dropped when the run was not graded per
	// mutant — so rebuilding the canonical JSON from a SELECT does not give
	// these bytes back. What this value proves is that the statement and the
	// rows came from one run and neither has been altered since; it does not
	// let a third party recompute the hash from the warehouse alone. See
	// cmd/corral's warehouseRowsSHA256 for the full list.
	WarehouseRowsSHA256 string `json:"warehouseRowsSha256,omitempty"`
}

// BuildAuditAttestation renders the statement as an in-toto Statement v1.
//
// The subject is the audited COMMIT, not a built artifact: the claim is about
// a change, and the thing a reviewer wants bound to the signature is the
// revision they are being asked to merge. gitCommit is in-toto's own digest
// key for exactly this.
func BuildAuditAttestation(s AuditStatement) map[string]any {
	files := make([]map[string]any, 0, len(s.Files))
	for _, f := range s.Files {
		entry := map[string]any{
			"path":         f.Path,
			"survivors":    f.Survivors,
			"provenMissed": f.ProvenMissed,
		}
		// Signed whenever a measurement exists — including a measured ONE, so
		// a verifier can see the exam ran at concurrency 1 without inferring
		// it from an absent key. Absent when nothing measured it: the key is
		// missing, never a 0 that reads as a count.
		if f.Trees >= 1 {
			entry["trees"] = f.Trees
			if f.ConcurrencyNote != "" {
				entry["concurrencyNote"] = f.ConcurrencyNote
			}
			if len(f.SharedDirs) > 0 {
				entry["sharedDirs"] = f.SharedDirs
			}
		}
		// Omitted, never zero-filled: an absent killRate says "no rate was
		// measured for this file", and a consumer that reads a 0.0 instead
		// would sign — and report — a number nobody measured.
		if f.KillRate != nil {
			entry["killRate"] = *f.KillRate
		}
		if f.Uncovered {
			entry["uncovered"] = true
		}
		if f.TestSelection != "" {
			entry["testSelection"] = f.TestSelection
			entry["selectedTests"] = f.SelectedTests
			entry["suiteTests"] = f.SuiteTests
		}
		// Only when the run actually graded per mutant: a zero-filled spread
		// on a shared-command file would be a signed claim about a
		// measurement that was never made.
		if f.ProvenByAuthoredAlone {
			entry["provenByAuthoredAlone"] = true
		}
		if f.PerMutant {
			entry["perMutant"] = true
			// The spread only when one was actually MEASURED. A per-mutant
			// run whose every mutant was rejected by the compile gate
			// measured none, and signing zeros for it would put a range
			// nobody measured over a signature — the same reason an
			// uncovered file signs no killRate. The absence is structural:
			// the field is a pointer, so there are no zeros to sign.
			if s := f.TestsPerMutant; s != nil {
				entry["testsPerMutantMin"] = s.Min
				entry["testsPerMutantMedian"] = s.Median
				entry["testsPerMutantMax"] = s.Max
			}
		}
		if f.SelectionFallback != "" {
			entry["selectionFallback"] = f.SelectionFallback
		}
		// WHICH SHAPE earned the provenMissed above. Signed only when the run
		// recorded one: a verdict from a caller that named no mode, or from
		// before the mode existed, has no fact here, and stamping either
		// spelling would attest to something nobody established.
		if f.WriterMode != "" {
			entry["writerMode"] = f.WriterMode
		}
		if f.TimedOut {
			entry["timedOut"] = true
		}
		if f.TestWriterFailed {
			entry["testWriterFailed"] = true
		}
		if f.PoolTestUnsound {
			entry["poolTestUnsound"] = true
		}
		files = append(files, entry)
	}

	models := map[string]any{}
	for role, m := range s.ModelsByRole {
		models[role] = m
	}

	gates := map[string]any{}
	if s.MinKillRate != nil {
		gates["minKillRate"] = *s.MinKillRate
	}
	if s.MaxProvenMissed != nil {
		gates["maxProvenMissed"] = *s.MaxProvenMissed
	}

	predicate := map[string]any{
		"auditor": map[string]any{
			"id": "https://corralai.dev/certify",
		},
		// What was measured, per file, with its honesty flags attached.
		"files": files,
		// The denominator and the roster.
		"scope": map[string]any{
			"audited":    s.Audited,
			"candidates": s.Candidates,
		},
		"modelsByRole": models,
		// The thresholds this verdict was judged against. Without them
		// "passed: true" is unreadable — it says nothing about the bar.
		"gates":  gates,
		"passed": s.Passed,
		// ScanID is signed even at 0 — the ledger was not written — because
		// that absence is itself something a verifier needs to see, not a
		// key to omit.
		"scanId": s.ScanID,
	}
	// WarehouseRowsSHA256 is omitted, never signed as "", when the caller
	// has nothing to say — the same rule every other unmeasured field in
	// this predicate follows.
	if s.WarehouseRowsSHA256 != "" {
		predicate["warehouseRowsSha256"] = s.WarehouseRowsSHA256
	}

	return map[string]any{
		"_type": "https://in-toto.io/Statement/v1",
		"subject": []map[string]any{
			{
				"name":   s.Repo,
				"digest": map[string]string{"gitCommit": s.Commit},
			},
		},
		"predicateType": AuditPredicateType,
		"predicate":     predicate,
	}
}

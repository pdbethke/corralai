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
	Path         string  `json:"path"`
	KillRate     float64 `json:"killRate"`
	Survivors    int     `json:"survivors"`
	ProvenMissed int     `json:"provenMissed"`
	// The honesty flags travel WITH the numbers, never separately. A
	// provenMissed of 0 means "nothing was proven" rather than "the suite is
	// clean" whenever one of these is set, and a consumer reading the number
	// without them would draw the opposite conclusion from the right data.
	TimedOut         bool `json:"timedOut,omitempty"`
	TestWriterFailed bool `json:"testWriterFailed,omitempty"`
	PoolTestUnsound  bool `json:"poolTestUnsound,omitempty"`
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
			"killRate":     f.KillRate,
			"survivors":    f.Survivors,
			"provenMissed": f.ProvenMissed,
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

	return map[string]any{
		"_type": "https://in-toto.io/Statement/v1",
		"subject": []map[string]any{
			{
				"name":   s.Repo,
				"digest": map[string]string{"gitCommit": s.Commit},
			},
		},
		"predicateType": AuditPredicateType,
		"predicate": map[string]any{
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
		},
	}
}

// SPDX-License-Identifier: Elastic-2.0

package certify

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/pdbethke/corralai/internal/review"
)

// ReviewPredicateType is the predicate of a `corral review --attest`
// statement. What it attests is the REPRODUCTIONS: for every finding, the
// tier the reviewer declared, the tier the run recorded, the script that
// ran, the hash of what it printed, how it exited, and the verifier's
// refutation on the same terms. The opinion is bound by its hash and not
// carried: a signature over an opinion would be the opinion with a seal on
// it, and this statement signs only what was executed.
const ReviewPredicateType = "https://corralai.dev/review/v1"

// Reproduction is one finding's executed record, as the statement carries
// it. Script and output travel as hashes — the ledger entry holds the
// bytes, and a reader with the entry can check them against these.
type Reproduction struct {
	ID           string `json:"id"`
	Claim        string `json:"claim"`
	DeclaredTier string `json:"declaredTier"`
	Tier         string `json:"tier"`
	File         string `json:"file,omitempty"`
	Line         int    `json:"line,omitempty"`
	Severity     string `json:"severity,omitempty"`
	ScriptSHA256 string `json:"scriptSha256,omitempty"`
	OutputSHA256 string `json:"outputSha256,omitempty"`
	ExitCode     *int   `json:"exitCode,omitempty"`
	Demoted      string `json:"demoted,omitempty"`
	Refutation   *struct {
		Model        string `json:"model"`
		Verdict      string `json:"verdict"`
		DeclaredTier string `json:"declaredTier,omitempty"`
		Tier         string `json:"tier,omitempty"`
		ScriptSHA256 string `json:"scriptSha256,omitempty"`
		OutputSHA256 string `json:"outputSha256,omitempty"`
		ExitCode     *int   `json:"exitCode,omitempty"`
		Demoted      string `json:"demoted,omitempty"`
	} `json:"refutation,omitempty"`
}

func sha256Hex(s string) string {
	if s == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// Reproductions reduces a review's findings to what the statement signs.
func Reproductions(r review.Review) []Reproduction {
	out := make([]Reproduction, 0, len(r.Findings))
	for _, f := range r.Findings {
		p := Reproduction{ID: f.ID, Claim: f.Claim, DeclaredTier: f.Declared, Tier: f.Tier, File: f.File, Line: f.Line, Severity: f.Severity,
			ScriptSHA256: sha256Hex(f.Script), OutputSHA256: sha256Hex(f.Stdout), ExitCode: f.ExitCode, Demoted: f.Demoted}
		if x := f.Refutation; x != nil {
			p.Refutation = &struct {
				Model        string `json:"model"`
				Verdict      string `json:"verdict"`
				DeclaredTier string `json:"declaredTier,omitempty"`
				Tier         string `json:"tier,omitempty"`
				ScriptSHA256 string `json:"scriptSha256,omitempty"`
				OutputSHA256 string `json:"outputSha256,omitempty"`
				ExitCode     *int   `json:"exitCode,omitempty"`
				Demoted      string `json:"demoted,omitempty"`
			}{Model: x.Model, Verdict: x.Verdict, DeclaredTier: x.Declared, Tier: x.Tier, ScriptSHA256: sha256Hex(x.Script), OutputSHA256: sha256Hex(x.Stdout), ExitCode: x.ExitCode, Demoted: x.Demoted}
		}
		out = append(out, p)
	}
	return out
}

// ReproductionsSHA256 is the hash a verifier recomputes from a ledger
// entry's findings and compares to the statement's claim: sha256 over the
// JSON of Reproductions(r), which is deterministic (struct order, no maps).
func ReproductionsSHA256(r review.Review) string {
	b, _ := json.Marshal(Reproductions(r))
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// BuildReviewAttestation is the in-toto statement for one review. The
// subject is the repository at the reviewed commit; the predicate is the
// reproductions and the review's shape. The statement does not name the
// ledger entry — it is written FIRST, and the entry, written after, names
// the statement (Review.StatementSHA256); the statement names the
// reproductions. (An earlier draft of this comment described an entryHash
// parameter that was never added; a sonnet reviewer caught the drift and
// a flash verifier let the finding stand.)
func BuildReviewAttestation(r review.Review) map[string]any {
	rep, cr, hy := r.Counts()
	predicate := map[string]any{
		"scope":               r.Scope,
		"reviewerModel":       r.ReviewerModel,
		"substrate":           r.Substrate,
		"startedAt":           r.StartedAt.UTC().Format("2006-01-02T15:04:05Z"),
		"filesShown":          r.FilesShown,
		"truncated":           r.Truncated,
		"reproduced":          rep,
		"codeRead":            cr,
		"hypothesis":          hy,
		"reproductions":       Reproductions(r),
		"reproductionsSha256": ReproductionsSHA256(r),
		"sound":               r.Sound,
		// The opinion is BOUND, not carried: a reader holding the entry
		// can check the prose is the prose; the signature vouches for no
		// judgment in it.
		"opinionSha256": sha256Hex(r.Opinion),
	}
	if r.VerifierModel != "" {
		predicate["verifierModel"] = r.VerifierModel
		predicate["verifierOpinionSha256"] = sha256Hex(r.VerifierOpinion)
	}
	return map[string]any{
		"_type": "https://in-toto.io/Statement/v1",
		"subject": []map[string]any{
			{"name": r.Repo, "digest": map[string]string{"gitCommit": r.Commit}},
		},
		"predicateType": ReviewPredicateType,
		"predicate":     predicate,
	}
}

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
// ran, the hash of what it printed, how it exited, whether the harness
// could run it at all, and the verifier's refutation on the same terms.
// The opinion is bound by its hash and not carried: a signature over an
// opinion would be the opinion with a seal on it, and this statement signs
// only what was executed.
//
// v2 (2026-09-07): the reproductions carry Unrun (a harness failure is not
// a fall — a statement blind to the marker let it be added or cleared
// under a valid signature: review 167dd45b25cc#R2), and their hash is over
// the canonical JSON tree (sorted keys), not this binary's struct order
// (#R5). The predicate also carries what the entry records about HOW the
// review was made — coverage (#R3), the seats' tools, the language, the
// audited party, the bytes shown (#R4). v1 statements are recomputed under
// the v1 rule by ReproductionsSHA256At.
const (
	ReviewPredicateType   = "https://corralai.dev/review/v2"
	ReviewPredicateTypeV1 = "https://corralai.dev/review/v1"
)

// IsReviewPredicate reports whether pt is a review statement of any version.
func IsReviewPredicate(pt string) bool {
	return pt == ReviewPredicateType || pt == ReviewPredicateTypeV1
}

// Refutation is the verifier's executed record of one finding, as the
// statement carries it.
type Refutation struct {
	Model        string `json:"model"`
	Verdict      string `json:"verdict"`
	DeclaredTier string `json:"declaredTier,omitempty"`
	Tier         string `json:"tier,omitempty"`
	ScriptSHA256 string `json:"scriptSha256,omitempty"`
	OutputSHA256 string `json:"outputSha256,omitempty"`
	ExitCode     *int   `json:"exitCode,omitempty"`
	Demoted      string `json:"demoted,omitempty"`
	// Unrun is LAST and omitempty so the v1 rule — the same struct without
	// it — marshals to the bytes v1 statements were hashed over.
	Unrun string `json:"unrun,omitempty"`
}

// Reproduction is one finding's executed record, as the statement carries
// it. Script and output travel as hashes — the ledger entry holds the
// bytes, and a reader with the entry can check them against these.
type Reproduction struct {
	ID           string      `json:"id"`
	Claim        string      `json:"claim"`
	DeclaredTier string      `json:"declaredTier"`
	Tier         string      `json:"tier"`
	File         string      `json:"file,omitempty"`
	Line         int         `json:"line,omitempty"`
	Severity     string      `json:"severity,omitempty"`
	ScriptSHA256 string      `json:"scriptSha256,omitempty"`
	OutputSHA256 string      `json:"outputSha256,omitempty"`
	ExitCode     *int        `json:"exitCode,omitempty"`
	Demoted      string      `json:"demoted,omitempty"`
	Refutation   *Refutation `json:"refutation,omitempty"`
	// Unrun: see Refutation.Unrun.
	Unrun string `json:"unrun,omitempty"`
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
			ScriptSHA256: sha256Hex(f.Script), OutputSHA256: sha256Hex(f.Stdout), ExitCode: f.ExitCode, Demoted: f.Demoted, Unrun: f.Unrun}
		if x := f.Refutation; x != nil {
			p.Refutation = &Refutation{Model: x.Model, Verdict: x.Verdict, DeclaredTier: x.Declared, Tier: x.Tier,
				ScriptSHA256: sha256Hex(x.Script), OutputSHA256: sha256Hex(x.Stdout), ExitCode: x.ExitCode, Demoted: x.Demoted, Unrun: x.Unrun}
		}
		out = append(out, p)
	}
	return out
}

// jsonTree is v as JSON-native values — map[string]any, []any, string,
// float64, bool, nil — by a marshal/unmarshal round trip. A statement map
// must hold no Go struct (see CanonicalStatement): its canonical bytes
// would then depend on this binary's field order, which is exactly what a
// reader with a different build cannot reproduce.
func jsonTree(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil
	}
	return out
}

// ReproductionsSHA256 is the hash a verifier recomputes from a ledger
// entry's findings and compares to the statement's claim, under the
// CURRENT rule: sha256 over the canonical JSON (sorted keys) of the
// reproductions as JSON-native values — bytes any reader can reproduce.
func ReproductionsSHA256(r review.Review) string {
	return ReproductionsSHA256At(r, ReviewPredicateType)
}

// ReproductionsSHA256At recomputes the hash under the rule the named
// predicate version was written with, so a statement signed last week
// still verifies against its entry. v1: the JSON of the []Reproduction
// struct in declaration order, without Unrun. Unknown versions hash under
// the current rule.
func ReproductionsSHA256At(r review.Review, predicateType string) string {
	reps := Reproductions(r)
	var b []byte
	if predicateType == ReviewPredicateTypeV1 {
		for i := range reps {
			reps[i].Unrun = ""
			if reps[i].Refutation != nil {
				x := *reps[i].Refutation
				x.Unrun = ""
				reps[i].Refutation = &x
			}
		}
		b, _ = json.Marshal(reps)
	} else {
		b, _ = json.Marshal(jsonTree(reps))
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// BuildReviewAttestation is the in-toto statement for one review. The
// subject is the repository at the reviewed commit; the predicate is the
// reproductions and the review's shape — everything the entry records
// about HOW the review was made, so a reader of the statement alone can
// tell an agentic seat that read the whole checkout from an API seat that
// saw a truncated scope, and a review from a blanket approval. The
// statement does not name the ledger entry — it is written FIRST, and the
// entry, written after, names the statement (Review.StatementSHA256); the
// statement names the reproductions. (An earlier draft of this comment
// described an entryHash parameter that was never added; a sonnet reviewer
// caught the drift and a flash verifier let the finding stand.)
func BuildReviewAttestation(r review.Review) map[string]any {
	rep, cr, hy := r.Counts()
	predicate := map[string]any{
		"scope":               r.Scope,
		"reviewerModel":       r.ReviewerModel,
		"substrate":           r.Substrate,
		"startedAt":           r.StartedAt.UTC().Format("2006-01-02T15:04:05Z"),
		"filesShown":          r.FilesShown,
		"bytesShown":          r.BytesShown,
		"truncated":           r.Truncated,
		"reproduced":          rep,
		"codeRead":            cr,
		"hypothesis":          hy,
		"reproductions":       jsonTree(Reproductions(r)),
		"reproductionsSha256": ReproductionsSHA256(r),
		"sound":               r.Sound,
		// The opinion is BOUND, not carried: a reader holding the entry
		// can check the prose is the prose; the signature vouches for no
		// judgment in it.
		"opinionSha256": sha256Hex(r.Opinion),
	}
	// Facts about the review that are present or absent, never signed as
	// "": the run's own coverage note (a blanket approval says so in the
	// statement, not only in the entry), the seats' tools and versions,
	// the language, the audited party.
	for k, v := range map[string]string{
		"coverage":     r.Coverage,
		"reviewerTool": r.ReviewerTool,
		"verifierTool": r.VerifierTool,
		"lang":         r.Lang,
		"author":       r.Author,
		"committer":    r.Committer,
		"coAuthors":    r.CoAuthors,
		"verifierNote": r.VerifierNote,
	} {
		if v != "" {
			predicate[k] = v
		}
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

// SPDX-License-Identifier: Elastic-2.0

package certify

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/review"
)

// The six findings of review 167dd45b25cc (a Claude Code reviewer on
// internal/certify, Gemini verifying, 2026-09-07), all confirmed on
// adjudication. One shape: THE SIGNED STATEMENT DID NOT CARRY WHAT THE
// RECORD SAYS. The three reproductions are kept here, inverted.

// R1 — a duration nobody measured is omitted, never signed as 0. The
// `certify --local` record sets no DurationS while its own ledger step
// omits an unmeasured duration_s on purpose; the statement said 0.
func TestDurationIsSignedOnlyWhenMeasured(t *testing.T) {
	rate := 0.75
	stmt := BuildAttestation(BuildRecord{
		Repo: "r", Commit: "c", Command: "corral/adversarial-pool", OutputDigest: "sha256:deadbeef",
		Scored: &ScoredCertification{KillRate: &rate, MutantsTotal: 40, Survivors: 10, ProvenMissed: 3},
	}, "head")
	if v, present := certificationByproduct(t, stmt)["durationS"]; present {
		t.Fatalf("durationS = %v signed for a record whose caller measured no wall clock", v)
	}
	measured := BuildAttestation(BuildRecord{Repo: "r", Commit: "c", Command: "go test", DurationS: MeasuredSeconds(1500 * time.Millisecond)}, "head")
	if v := certificationByproduct(t, measured)["durationS"]; v != 1.5 {
		t.Fatalf("a measured duration must be signed: %v", v)
	}
	// A submitted document's 0 means "not given", not "took no time".
	if SecondsOrUnmeasured(0) != nil || *SecondsOrUnmeasured(2) != 2 {
		t.Fatal("SecondsOrUnmeasured")
	}
}

// R2 — the harness-failure marker is bound by the statement: an entry whose
// Unrun is added or cleared must not re-hash to the statement's claim.
func TestUnrunIsBoundByTheStatement(t *testing.T) {
	mk := func(unrun string) review.Review {
		return review.Review{Repo: "r", Commit: "c", Findings: []review.Finding{{
			ID: "R1", Claim: "the gate can be defeated", Declared: review.TierReproduced, Tier: review.TierCodeRead,
			Script: "exit 0", Demoted: "not run (harness): the detached worktree was gone", Unrun: unrun,
			Refutation: &review.Refutation{Model: "v", Verdict: review.VerdictStands},
		}}}
	}
	if ReproductionsSHA256(mk("the detached worktree was gone")) == ReproductionsSHA256(mk("")) {
		t.Fatal("ReproductionsSHA256 is blind to Finding.Unrun")
	}
	withRef := mk("")
	withRef.Findings[0].Refutation.Unrun = "sh missing"
	if ReproductionsSHA256(withRef) == ReproductionsSHA256(mk("")) {
		t.Fatal("ReproductionsSHA256 is blind to Refutation.Unrun")
	}
}

// R3 / R4 — the predicate carries the review's own coverage note, the
// seats' tools, the language, the audited party and the bytes shown; a
// blanket approval is attested AS one.
func TestReviewStatementCarriesHowTheReviewWasMade(t *testing.T) {
	r := review.Review{
		Repo: "r", Commit: "c", Scope: "internal/certify", ReviewerModel: "claude-code", ReviewerTool: "2.1.263 (Claude Code)",
		VerifierModel: "codex", VerifierTool: "codex-cli 0.152.0", Lang: "go", Substrate: "workspace",
		Author: "Ada Writer", Committer: "Forge Bot", CoAuthors: "Claude Code", BytesShown: 4096, VerifierNote: "verdict 2 named no finding",
		Sound: []string{"the ledger chain"}, Opinion: "nothing found",
	}
	r.Coverage = review.CoverageNote(r)
	if r.Coverage == "" {
		t.Fatal("precondition: CoverageNote should flag this review")
	}
	stmt := BuildReviewAttestation(r)
	if stmt["predicateType"] != ReviewPredicateType {
		t.Fatalf("predicateType %v", stmt["predicateType"])
	}
	pred := stmt["predicate"].(map[string]any)
	for k, want := range map[string]any{
		"coverage": r.Coverage, "reviewerTool": r.ReviewerTool, "verifierTool": r.VerifierTool, "lang": "go",
		"author": "Ada Writer", "committer": "Forge Bot", "coAuthors": "Claude Code", "bytesShown": 4096, "verifierNote": r.VerifierNote,
	} {
		if pred[k] != want {
			t.Errorf("predicate[%q] = %#v, want %#v", k, pred[k], want)
		}
	}
	// Absent, never "", when the entry has none.
	bare := BuildReviewAttestation(review.Review{Repo: "r", Commit: "c", Scope: "s", ReviewerModel: "m", Findings: []review.Finding{{ID: "R1", Declared: review.TierHypothesis, Tier: review.TierHypothesis}}})["predicate"].(map[string]any)
	for _, k := range []string{"coverage", "reviewerTool", "verifierTool", "lang", "author", "committer", "coAuthors", "verifierNote"} {
		if _, present := bare[k]; present {
			t.Errorf("predicate[%q] signed as %#v on a review that recorded none", k, bare[k])
		}
	}
}

// R5 — the statement map holds JSON-native values only, so its canonical
// bytes are defined by the JSON and not by this binary's struct order.
func TestReviewStatementHoldsNoGoStructs(t *testing.T) {
	code := 0
	r := review.Review{Repo: "r", Commit: "c", Scope: "s", ReviewerModel: "m", Findings: []review.Finding{
		{ID: "R1", Claim: "x", Declared: review.TierReproduced, Tier: review.TierReproduced, Script: "true", ExitCode: &code,
			Refutation: &review.Refutation{Model: "v", Verdict: review.VerdictStands, Script: "false"}},
	}}
	stmt := BuildReviewAttestation(r)
	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, e := range x {
				walk(path+"."+k, e)
			}
		case []any:
			for i, e := range x {
				walk(fmt.Sprintf("%s[%d]", path, i), e)
			}
		case []map[string]any:
			for i, e := range x {
				walk(fmt.Sprintf("%s[%d]", path, i), e)
			}
		case map[string]string, []string, string, float64, int, bool, nil:
		default:
			t.Errorf("%s holds a %T (%v) — a Go value whose canonical bytes depend on this binary", path, v, reflect.TypeOf(v).Kind())
		}
	}
	walk("stmt", stmt)
	// And the reproductions hash is over sorted-key JSON: the same bytes a
	// reader gets from the statement's own "reproductions" value.
	b, _ := json.Marshal(stmt["predicate"].(map[string]any)["reproductions"])
	if !strings.Contains(string(b), `"claim":"x","declaredTier"`) {
		t.Fatalf("reproductions are not sorted-key JSON: %s", b)
	}
	if got := ReproductionsSHA256(r); got != sha256Hex(string(b)) {
		t.Fatalf("the claimed hash %s is not the hash of the reproductions the statement carries (%s)", got, sha256Hex(string(b)))
	}
}

// v1 statements still verify: the v1 rule is the struct in declaration
// order without Unrun, byte for byte what last week's statements hashed.
func TestV1ReproductionsHashIsPreserved(t *testing.T) {
	code := 1
	r := review.Review{Repo: "r", Commit: "c", Findings: []review.Finding{
		{ID: "R1", Claim: "a", Declared: review.TierReproduced, Tier: review.TierCodeRead, File: "f.go", Line: 3, Severity: "high",
			Script: "s", Stdout: "o", ExitCode: &code, Demoted: "exit 1", Unrun: "harness gone",
			Refutation: &review.Refutation{Model: "v", Verdict: review.VerdictRefuted, Declared: review.TierReproduced, Tier: review.TierReproduced, Script: "r", Stdout: "ro", ExitCode: &code, Unrun: "sh missing"}},
	}}
	// The v1 shape, verbatim from the v1 code.
	type v1Ref struct {
		Model        string `json:"model"`
		Verdict      string `json:"verdict"`
		DeclaredTier string `json:"declaredTier,omitempty"`
		Tier         string `json:"tier,omitempty"`
		ScriptSHA256 string `json:"scriptSha256,omitempty"`
		OutputSHA256 string `json:"outputSha256,omitempty"`
		ExitCode     *int   `json:"exitCode,omitempty"`
		Demoted      string `json:"demoted,omitempty"`
	}
	type v1Rep struct {
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
		Refutation   *v1Ref `json:"refutation,omitempty"`
	}
	f := r.Findings[0]
	x := f.Refutation
	want, _ := json.Marshal([]v1Rep{{ID: f.ID, Claim: f.Claim, DeclaredTier: f.Declared, Tier: f.Tier, File: f.File, Line: f.Line, Severity: f.Severity,
		ScriptSHA256: sha256Hex(f.Script), OutputSHA256: sha256Hex(f.Stdout), ExitCode: f.ExitCode, Demoted: f.Demoted,
		Refutation: &v1Ref{Model: x.Model, Verdict: x.Verdict, DeclaredTier: x.Declared, Tier: x.Tier, ScriptSHA256: sha256Hex(x.Script), OutputSHA256: sha256Hex(x.Stdout), ExitCode: x.ExitCode, Demoted: x.Demoted}}})
	if got := ReproductionsSHA256At(r, ReviewPredicateTypeV1); got != sha256Hex(string(want)) {
		t.Fatalf("v1 rule drifted: %s vs %s", got, sha256Hex(string(want)))
	}
	if ReproductionsSHA256At(r, ReviewPredicateTypeV1) == ReproductionsSHA256At(r, ReviewPredicateType) {
		t.Fatal("v1 and v2 must differ on a review that carries Unrun")
	}
	if !IsReviewPredicate(ReviewPredicateTypeV1) || !IsReviewPredicate(ReviewPredicateType) || IsReviewPredicate(AuditPredicateType) {
		t.Fatal("IsReviewPredicate")
	}
}

// R6 — an absent kill rate carries the flag that says which kind of
// unmeasured it was.
func TestAuditedFileSaysWhyARateIsAbsent(t *testing.T) {
	stmt := BuildAuditAttestation(AuditStatement{Repo: "r", Commit: "c", Files: []AuditedFile{{Path: "a.go", NoGradableMutant: true}, {Path: "b.go", Uncovered: true}}})
	b, _ := json.Marshal(stmt)
	for _, want := range []string{`"noGradableMutant":true`, `"uncovered":true`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("statement lacks %s:\n%s", want, b)
		}
	}
	if strings.Count(string(b), "noGradableMutant") != 1 {
		t.Errorf("the flag must be omitted when false:\n%s", b)
	}
}

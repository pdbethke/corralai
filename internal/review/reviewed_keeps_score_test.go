// SPDX-License-Identifier: Elastic-2.0

package review

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The findings of review 61dc210a39fd — the loop reviewing itself
// (Claude Code reviewing, Codex verifying, 2026-09-07), six confirmed on
// adjudication. The four reproductions are kept here inverted; R5 and R7
// get the tests they should have had. Each fails on the code as it stood.

type harnessErrRep struct{}

func (harnessErrRep) Run(context.Context, string) (string, int, error) {
	return "", 0, errors.New("git worktree at 5ea687f3: no such file or directory")
}

// R1 — a reproduction the harness could not run is not a claim that
// fell: nothing ran, so there is no outcome, and nobody is graded on it.
func TestHarnessFailureIsNotAnOutcome(t *testing.T) {
	r := &Review{Findings: []Finding{{ID: "R1", Claim: "the gate is skipped on the push path", Declared: TierReproduced, Tier: TierReproduced, Script: "echo evidence; exit 0"}}}
	Reproduce(context.Background(), harnessErrRep{}, r)
	f := r.Findings[0]
	if f.ExitCode != nil || f.Unrun == "" || !strings.HasPrefix(f.Demoted, "not run (harness):") {
		t.Fatalf("the record must say the harness failed, not that the script disagreed: %+v", f)
	}
	if o := OutcomeOf(f, nil); o.Known {
		t.Fatalf("nothing executed, yet the record has an outcome: %+v", o)
	}
	f.Refutation = &Refutation{Model: "v", Verdict: VerdictRefuted, Tier: TierCodeRead, Argument: "I grepped and found nothing"}
	if g := Grade(f, nil); g != (Graded{}) {
		t.Fatalf("nothing executed, yet a seat is graded: %+v", g)
	}
	// A person's verdict still outranks it.
	if o := OutcomeOf(f, &Adjudicated{Verdict: "confirmed", By: "p"}); !o.Known || !o.Held {
		t.Fatalf("adjudication must still decide: %+v", o)
	}
}

type okRep struct{ ran int }

func (r *okRep) Run(_ context.Context, script string) (string, int, error) {
	r.ran++
	return "the check the finding calls missing fires at internal/x.go:12\n", 0, nil
}

// R2 — a refutation that reproduced is an outcome for a finding of any
// tier: the claim fell, the reviewer is charged, the verifier credited.
func TestReproducedRefutationOfACodeReadClaimIsAnOutcome(t *testing.T) {
	r := &Review{Findings: []Finding{{ID: "R1", Claim: "the check is missing on the push path", Declared: TierCodeRead, Tier: TierCodeRead}}}
	rep := &okRep{}
	refs := map[string]Refutation{"R1": {Model: "verifier-m", Verdict: VerdictRefuted, Declared: TierReproduced, Tier: TierReproduced, Argument: "here is the check, firing", Script: "go test ./... -run TheCheck"}}
	Verify(context.Background(), rep, r, "verifier-m", "the review is wrong", refs)
	f := r.Findings[0]
	if rep.ran != 1 || f.Refutation.ExitCode == nil || *f.Refutation.ExitCode != 0 {
		t.Fatalf("fixture: the refutation did not run cleanly: %+v", f.Refutation)
	}
	if f.Tier != TierCodeRead || !strings.Contains(f.Demoted, "refuted by verifier-m, reproduced") {
		t.Fatalf("the record must say the claim fell: %+v", f)
	}
	o := OutcomeOf(f, nil)
	if !o.Known || o.Held || !strings.HasPrefix(o.By, "execution") {
		t.Fatalf("an execution that disproved the claim is an outcome: %+v", o)
	}
	g := Grade(f, nil)
	if !g.ReviewerChecked || g.ReviewerHeld || !g.VerifierCalled || !g.VerifierCorrect {
		t.Fatalf("the reviewer is charged and the verifier credited: %+v", g)
	}
	// A refutation that did NOT reproduce (exit 1) is still no outcome for
	// a CODE-READ claim.
	r2 := &Review{Findings: []Finding{{ID: "R1", Declared: TierCodeRead, Tier: TierCodeRead}}}
	Verify(context.Background(), scripted{"fails": {code: 1}}, r2, "v", "", map[string]Refutation{"R1": {Model: "v", Verdict: VerdictRefuted, Declared: TierReproduced, Tier: TierReproduced, Script: "fails"}})
	if o := OutcomeOf(r2.Findings[0], nil); o.Known {
		t.Fatalf("a refutation that did not demonstrate itself is no outcome: %+v", o)
	}
}

// R3 — a JSON literal in the prose before the payload is not the review.
func TestParseFindsTheReviewObjectNotTheFirstObject(t *testing.T) {
	prose := "Before writing this I looked for ledger rows shaped like {\"tier\":\"REPRODUCED\"} to see what had held.\n\n"
	payload := `{"opinion":"the router trusts its callers","findings":[{"claim":"the gate is skipped on the push path","tier":"REPRODUCED","file":"x.go","line":3,"severity":"high","script":"exit 0"},{"claim":"the cache key is blind to the model","tier":"CODE-READ","file":"y.go","line":9,"severity":"medium"}],"sound":["the parser","the walker","the tier normaliser"]}`
	opinion, findings, sound, err := Parse(prose + payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 2 || len(sound) != 3 || opinion == "" {
		t.Fatalf("the payload behind the literal was dropped: %d findings, %d sound, opinion %q", len(findings), len(sound), opinion)
	}
	// And a reply with objects but no review-shaped one is an error, not
	// an empty review.
	if _, _, _, err := Parse("I found {\"tier\":\"REPRODUCED\"} and {\"a\":1} but wrote no review."); err == nil {
		t.Fatal("no review object must be an error, never a blanket approval")
	}
	// The verifier's parser, the same way.
	_, refs, err := ParseRefutations("see {\"id\":\"R1\"} above; {\"opinion\":\"wrong\",\"refutations\":[{\"id\":\"R1\",\"verdict\":\"REFUTED\"}]}", "m")
	if err != nil || len(refs) != 1 {
		t.Fatalf("the refutations object must be found past a literal: %v %v", err, refs)
	}
}

// R4 — a scope outside the repository is refused, through .. and
// through a symlink.
func TestLoadScopeStaysInsideTheRepository(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "repo")
	if err := os.MkdirAll(filepath.Join(root, "internal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal", "a.go"), []byte("package a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(secret, []byte("-----BEGIN PRIVATE KEY-----\nNOT-IN-THE-REPOSITORY\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"../id_ed25519", "internal/../../id_ed25519", ".."} {
		if _, err := LoadScope(root, scope, 200000); err == nil || !strings.Contains(err.Error(), "outside the repository") {
			t.Errorf("scope %q must be refused by name: %v", scope, err)
		}
	}
	if err := os.Symlink(secret, filepath.Join(root, "internal", "link")); err == nil {
		if _, err := LoadScope(root, "internal/link", 200000); err == nil || !strings.Contains(err.Error(), "outside the repository") {
			t.Errorf("a symlink out of the checkout must be refused: %v", err)
		}
	}
	// Inside still loads, including through "./".
	sc, err := LoadScope(root, "./internal", 200000)
	if err != nil || len(sc.Files) != 1 {
		t.Fatalf("an inside scope must load: %v %v", err, sc.Files)
	}
}

// R5 — a finding counts toward the scope its file is under, not toward
// every scope the review covers.
func TestPlanAttributesFindingsByTheirFile(t *testing.T) {
	code := 0
	reviews := []Reviewed{{Scope: "internal", Commit: "c1", When: time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC),
		Findings: []Finding{
			{ID: "R1", File: "internal/other/x.go", Declared: TierReproduced, Tier: TierReproduced, ExitCode: &code},
			{ID: "R2", File: "internal/review/y.go", Declared: TierCodeRead, Tier: TierCodeRead},
		}}}
	plan := Plan(map[string]int{"internal/review": 8, "internal/other": 3, "internal": 20}, reviews, func(string, string) int { return 1 })
	by := map[string]ScopeState{}
	for _, s := range plan {
		by[s.Scope] = s
	}
	if r := by["internal/review"]; r.Held != 0 || r.Open != 1 || strings.Contains(r.Reason, "findings that held") {
		t.Errorf("internal/review must carry only its own finding: %+v", r)
	}
	if o := by["internal/other"]; o.Held != 1 || o.Open != 0 {
		t.Errorf("internal/other must carry the held finding: %+v", o)
	}
	if p := by["internal"]; p.Held != 1 || p.Open != 1 {
		t.Errorf("the reviewed scope carries both: %+v", p)
	}
}

// R7 — the verifier's ids are read as the review names them; an unknown
// id and a second verdict are said on the record, never dropped.
func TestVerifierIDsAreCanonicalAndNothingIsDroppedSilently(t *testing.T) {
	_, refs, err := ParseRefutations(`{"opinion":"o","refutations":[
	 {"id":"r1","verdict":"REFUTED","tier":"CODE-READ","argument":"first"},
	 {"id":" 2 ","verdict":"STANDS"},
	 {"id":"R1","verdict":"STANDS","argument":"second, must not overwrite"},
	 {"id":"R9","verdict":"STANDS"}
	]}`, "v")
	if err != nil {
		t.Fatal(err)
	}
	if refs["R1"].Verdict != VerdictRefuted || refs["R2"].Verdict != VerdictStands {
		t.Fatalf("ids not canonical or the first verdict not kept: %+v", refs)
	}
	r := &Review{Findings: []Finding{{ID: "R1", Declared: TierCodeRead, Tier: TierCodeRead}, {ID: "R2", Declared: TierCodeRead, Tier: TierCodeRead}}}
	Verify(context.Background(), scripted{}, r, "v", "o", refs)
	if r.Findings[0].Refutation == nil || r.Findings[0].Refutation.Argument != "first" || r.Findings[1].Refutation == nil {
		t.Fatalf("verdicts under canonical ids must attach: %+v", r.Findings)
	}
	for _, want := range []string{"second verdict on R1", "R9, which is not a finding"} {
		if !strings.Contains(r.VerifierNote, want) {
			t.Errorf("the note lacks %q: %q", want, r.VerifierNote)
		}
	}
}

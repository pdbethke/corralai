// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/modelrank"
	"github.com/pdbethke/corralai/internal/review"
)

// reviewPool names two hosted models; reviewEvidence measures both as
// reviewer and as verifier at the floor, "reviewer-x" being the better
// reviewer and "verifier-y" the better verifier, so an unpinned proposal is
// reviewer-x and verifier-y.
const reviewPool = "anthropic/reviewer-x,anthropic/verifier-y"

func reviewEvidence() rankEvidence {
	var obs []modelrank.Observation
	add := func(model, role string, checked, held, calls, correct int) {
		for _, run := range []string{"r1", "r2", "r3", "r4", "r5", "r6"} {
			obs = append(obs, modelrank.Observation{Model: model, Role: role, Run: run,
				ReviewClaimsChecked: checked, ReviewClaimsHeld: held, VerifierCalls: calls, VerifierCorrect: correct})
		}
	}
	add("reviewer-x", modelrank.SeatReviewer, 2, 2, 0, 0)
	add("verifier-y", modelrank.SeatReviewer, 2, 1, 0, 0)
	add("verifier-y", modelrank.SeatVerifier, 0, 0, 2, 2)
	add("reviewer-x", modelrank.SeatVerifier, 0, 0, 2, 1)
	return rankEvidence{Obs: obs, Source: "test evidence (6 review(s))", Generations: generationRange{Min: "48", Max: "48", Scans: 6}}
}

// stubReviewPool replaces the doors' seams with reviewEvidence and an
// operator who accepts, and reports how often the operator was asked.
func stubReviewPool(t *testing.T) *int {
	t.Helper()
	asked := new(int)
	orig := doorPoolDeps
	t.Cleanup(func() { doorPoolDeps = orig })
	doorPoolDeps = func(stdout, stderr io.Writer) poolDeps {
		return poolDeps{
			load:    func(rankSource) (rankEvidence, error) { return reviewEvidence(), nil },
			confirm: func(string) (bool, bool, error) { *asked++; return true, true, nil },
			stdout:  stdout, stderr: stderr,
		}
	}
	origRun := shadowMemberRunnable
	t.Cleanup(func() { shadowMemberRunnable = origRun })
	shadowMemberRunnable = func(agentbackend.ResolvedSeat) error { return nil }
	return asked
}

// reviewModelsRun runs one review over a tiny committed repo with canned
// seats and returns the single entry it wrote, so the tests read what was
// recorded and not an intermediate.
func reviewModelsRun(t *testing.T, args ...string) (rev review.Review, code int, out, errs string, seen map[string]int) {
	t.Helper()
	requireReviewSandbox(t)
	declareLocalSeats(t, "reviewer-x", "verifier-y")
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	ledger := filepath.Join(t.TempDir(), "machine")
	t.Setenv("CORRALAI_MACHINE_LEDGER", ledger)
	root := t.TempDir()
	gitRun := gitCmd(t, root)
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n\nfunc Add(a, b int) int { return a - b }\n")
	mustWrite(t, filepath.Join(root, "go.mod"), "module x\n\ngo 1.22\n")
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")

	seen = map[string]int{}
	orig := newReviewerBackend
	t.Cleanup(func() { newReviewerBackend = orig })
	newReviewerBackend = func(rs agentbackend.ResolvedSeat, _ agentbackend.SeatOptions) (agentbackend.Backend, error) {
		seen[rs.Spec]++
		if rs.Spec == "verifier-y" {
			return cannedReviewer{reply: `{"opinion":"v","refutations":[]}`}, nil
		}
		return cannedReviewer{reply: `{"opinion":"o","findings":[{"claim":"a caller might overflow","tier":"HYPOTHESIS"}],"sound":[]}`}, nil
	}
	var o, e bytes.Buffer
	code = runReview(append([]string{"--repo", root, "--scope", "pkg"}, args...), &o, &e)
	entries, _ := auditpush.ReadLedgerDir(filepath.Join(root, ".corral", "ledger"))
	if code == 0 {
		if len(entries) != 1 || entries[0].Review == nil {
			t.Fatalf("want one review entry, got %d\n%s%s", len(entries), o.String(), e.String())
		}
		rev = *entries[0].Review
	}
	return rev, code, o.String(), e.String(), seen
}

func TestReviewSeatedByModels(t *testing.T) {
	asked := stubReviewPool(t)
	rev, code, out, errs, _ := reviewModelsRun(t, "--models", reviewPool)
	if code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out, errs)
	}
	if *asked != 1 {
		t.Errorf("asked %d times, want once", *asked)
	}
	if rev.SeatedBy[modelrank.SeatReviewer] != "evidence" || rev.SeatedBy[modelrank.SeatVerifier] != "evidence" || len(rev.SeatedBy) != 2 {
		t.Errorf("seated_by = %v, want both seats by evidence", rev.SeatedBy)
	}
	if !strings.Contains(rev.SeatEvidence, "all languages (a review is not ranked by language)") {
		t.Errorf("seat_evidence = %q", rev.SeatEvidence)
	}
	if rev.ReviewerModel != "reviewer-x" || rev.VerifierModel != "verifier-y" {
		t.Errorf("seats %q / %q, want reviewer-x / verifier-y", rev.ReviewerModel, rev.VerifierModel)
	}
}

// The proposer never seats the reviewer as the verifier, even when it is the
// better verifier too.
func TestReviewModelsNeverSeatsTheReviewerAsVerifier(t *testing.T) {
	stubReviewPool(t)
	ev := reviewEvidence()
	for i := range ev.Obs {
		if ev.Obs[i].Model == "reviewer-x" && ev.Obs[i].Role == modelrank.SeatVerifier {
			ev.Obs[i].VerifierCorrect = 2
		}
	}
	doorPoolDeps = func(stdout, stderr io.Writer) poolDeps {
		return poolDeps{load: func(rankSource) (rankEvidence, error) { return ev, nil },
			confirm: func(string) (bool, bool, error) { return true, true, nil }, stdout: stdout, stderr: stderr}
	}
	rev, code, out, errs, _ := reviewModelsRun(t, "--models", reviewPool, "--reviewer-model", "anthropic/reviewer-x")
	if code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out, errs)
	}
	if rev.VerifierModel == rev.ReviewerModel || rev.ReviewerModel != "reviewer-x" {
		t.Errorf("reviewer %q verifier %q", rev.ReviewerModel, rev.VerifierModel)
	}
	if rev.SeatedBy[modelrank.SeatReviewer] != "operator" {
		t.Errorf("seated_by = %v", rev.SeatedBy)
	}
}

// --verifier-model off is a pin: no verifier is proposed or run, the plan
// names the flag, and the entry carries nothing for a verifier.
func TestReviewModelsWithVerifierOffProposesOnlyTheReviewer(t *testing.T) {
	stubReviewPool(t)
	rev, code, out, errs, seen := reviewModelsRun(t, "--models", reviewPool, "--verifier-model", "off")
	if code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out, errs)
	}
	if got := planRow(out, "verifier"); !strings.Contains(got, "operator") || !strings.Contains(got, "pinned by --verifier-model") {
		t.Errorf("plan verifier row = %q", got)
	}
	if seen["verifier-y"] != 0 || seen["off"] != 0 || rev.VerifierModel != "" || rev.VerifierTool != "" {
		t.Errorf("a verifier ran or was recorded: seen=%v entry verifier=%q", seen, rev.VerifierModel)
	}
	if _, has := rev.SeatedBy[modelrank.SeatVerifier]; has || rev.SeatedBy[modelrank.SeatReviewer] != "evidence" {
		t.Errorf("seated_by = %v, want the reviewer only", rev.SeatedBy)
	}
	if _, has := rev.SeatProviders[modelrank.SeatVerifier]; has {
		t.Errorf("seat_providers = %v", rev.SeatProviders)
	}
}

// Without --models, `off` is the same run as no verifier flag at all, and a
// run without --models records neither field.
func TestReviewVerifierOffIsNoVerifier(t *testing.T) {
	withOff, code, out, errs, seen := reviewModelsRun(t, "--reviewer-model", "reviewer-x", "--verifier-model", "OFF ")
	if code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out, errs)
	}
	without, code, out, errs, _ := reviewModelsRun(t, "--reviewer-model", "reviewer-x")
	if code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out, errs)
	}
	if seen["off"] != 0 || len(seen) != 1 {
		t.Errorf("seats built: %v, want only the reviewer", seen)
	}
	for _, r := range []review.Review{withOff, without} {
		if r.VerifierModel != "" || r.SeatedBy != nil || r.SeatEvidence != "" || len(r.SeatProviders) != 1 {
			t.Errorf("entry not shaped as a no-verifier, no --models run: %+v", r)
		}
	}
}

// Usage refusals that need no seat come before --models is asked about.
func TestReviewModelsRefusesUsageBeforeAskingAnything(t *testing.T) {
	asked := stubReviewPool(t)
	var o, e bytes.Buffer
	if code := runReview([]string{"--models", reviewPool}, &o, &e); code != 2 || *asked != 0 || strings.Contains(o.String(), "seats (from --models") {
		t.Errorf("no --scope: exit %d asked %d\n%s%s", code, *asked, o.String(), e.String())
	}
	if !strings.Contains(e.String(), "--scope is required") {
		t.Errorf("stderr = %q", e.String())
	}
	// A reviewer is required, and --models can fill it; with no pool and no
	// reviewer the refusal stands.
	o.Reset()
	e.Reset()
	root := t.TempDir()
	if code := runReview([]string{"--repo", root, "--scope", "x", "--no-ledger"}, &o, &e); code == 0 {
		t.Errorf("no reviewer was accepted")
	}
}

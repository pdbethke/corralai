// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"errors"
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
	return rankEvidence{Obs: obs, Source: "test evidence (6 review(s))", RecordSource: "test evidence (6 review(s))", Generations: generationRange{Min: "48", Max: "48", Scans: 6}}
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
	r := reviewModelsRunWith(t, reviewRunOpts{verifierReply: `{"opinion":"v","refutations":[]}`}, args...)
	if r.code == 0 {
		if len(r.entries) != 1 || r.entries[0].Review == nil {
			t.Fatalf("want one review entry, got %d\n%s%s", len(r.entries), r.out, r.errs)
		}
		rev = *r.entries[0].Review
	}
	return rev, r.code, r.out, r.errs, r.seen
}

// reviewRunOpts varies the canned review: what the verifier replies.
type reviewRunOpts struct{ verifierReply string }

// reviewRun is everything one canned review left behind: the exit, both
// streams, the seats built (by model), the entries in the repo ledger and the
// machine ledger directory, which a test reads for what was or was not
// written.
type reviewRun struct {
	code          int
	out, errs     string
	seen          map[string]int
	entries       []auditpush.LedgerEntry
	machineLedger string
	stateDir      string
}

func reviewModelsRunWith(t *testing.T, opts reviewRunOpts, args ...string) reviewRun {
	t.Helper()
	requireReviewSandbox(t)
	declareLocalSeats(t, "reviewer-x", "verifier-y")
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	ledger := filepath.Join(t.TempDir(), "machine")
	t.Setenv("CORRALAI_MACHINE_LEDGER", ledger)
	state := t.TempDir()
	t.Setenv("CORRALAI_STATE_DIR", state)
	root := t.TempDir()
	gitRun := gitCmd(t, root)
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n\nfunc Add(a, b int) int { return a - b }\n")
	mustWrite(t, filepath.Join(root, "go.mod"), "module x\n\ngo 1.22\n")
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")

	seen := map[string]int{}
	orig := newReviewerBackend
	t.Cleanup(func() { newReviewerBackend = orig })
	newReviewerBackend = func(rs agentbackend.ResolvedSeat, _ agentbackend.SeatOptions) (agentbackend.Backend, error) {
		seen[rs.Spec]++
		if rs.Spec == "verifier-y" {
			return cannedReviewer{reply: opts.verifierReply}, nil
		}
		return cannedReviewer{reply: `{"opinion":"o","findings":[{"claim":"a caller might overflow","tier":"HYPOTHESIS"}],"sound":[]}`}, nil
	}
	var o, e bytes.Buffer
	code := runReview(append([]string{"--repo", root, "--scope", "pkg"}, args...), &o, &e)
	entries, _ := auditpush.ReadLedgerDir(filepath.Join(root, ".corral", "ledger"))
	return reviewRun{code: code, out: o.String(), errs: e.String(), seen: seen, entries: entries, machineLedger: ledger, stateDir: state}
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
	gitRun := gitCmd(t, root)
	mustWrite(t, filepath.Join(root, "x", "a.go"), "package x\n")
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")
	code := runReview([]string{"--repo", root, "--scope", "x", "--no-ledger"}, &o, &e)
	if code != 2 || *asked != 0 || o.Len() != 0 {
		t.Errorf("no reviewer: exit %d asked %d stdout %q, want 2, nothing asked, nothing printed", code, *asked, o.String())
	}
	if !strings.Contains(e.String(), "a reviewer: --reviewer-model, or --models to have one proposed") {
		t.Errorf("stderr = %q", e.String())
	}
}

// Every seat pinned (the reviewer named, the verifier pinned off): nothing is
// proposed or asked, the pool is still checked, and the entry records the
// reviewer as the operator's and nothing else new (C8).
func TestReviewModelsAllPinned(t *testing.T) {
	asked := stubReviewPool(t)
	rev, code, out, errs, seen := reviewModelsRun(t, "--models", reviewPool, "--reviewer-model", "anthropic/reviewer-x", "--verifier-model", "off")
	if code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out, errs)
	}
	if *asked != 0 || strings.Contains(out, "seats (from --models") {
		t.Errorf("asked %d time(s) or printed a plan with every seat pinned:\n%s", *asked, out)
	}
	if len(rev.SeatedBy) != 1 || rev.SeatedBy[modelrank.SeatReviewer] != "operator" || rev.SeatEvidence != "" {
		t.Errorf("seated_by %v, seat_evidence %q; want the reviewer as the operator's only", rev.SeatedBy, rev.SeatEvidence)
	}
	if rev.VerifierModel != "" || seen["verifier-y"] != 0 {
		t.Errorf("a verifier ran: %q %v", rev.VerifierModel, seen)
	}
	// The pool is still checked: a member that cannot run refuses the run.
	shadowMemberRunnable = func(rs agentbackend.ResolvedSeat) error {
		if rs.Spec == "verifier-y" {
			return errors.New("no key for verifier-y")
		}
		return nil
	}
	_, code, _, errs, _ = reviewModelsRun(t, "--models", reviewPool, "--reviewer-model", "anthropic/reviewer-x", "--verifier-model", "off")
	if code != 2 || !strings.Contains(errs, "no key for verifier-y") {
		t.Errorf("a pool with a member that cannot run: exit %d stderr %q", code, errs)
	}
}

// A review records the evidence line only when a seat on the entry was
// seated by evidence (B12): with the reviewer pinned and the evidence-seated
// verifier recorded unverified (its reply does not parse), no recorded seat
// was chosen by the ranking, so the line would explain nothing on the entry.
func TestReviewRecordsNoEvidenceLineWithoutAnEvidenceSeat(t *testing.T) {
	stubReviewPool(t)
	r := reviewModelsRunWith(t, reviewRunOpts{verifierReply: "not a verdict at all"},
		"--models", reviewPool, "--reviewer-model", "anthropic/reviewer-x")
	if r.code != 0 || len(r.entries) != 1 || r.entries[0].Review == nil {
		t.Fatalf("exit %d, %d entries\n%s%s", r.code, len(r.entries), r.out, r.errs)
	}
	rev := *r.entries[0].Review
	if rev.VerifierModel != "" || rev.SeatedBy[modelrank.SeatReviewer] != "operator" {
		t.Fatalf("fixture did not drop the verifier: verifier %q seated_by %v", rev.VerifierModel, rev.SeatedBy)
	}
	if rev.SeatEvidence != "" {
		t.Errorf("seat_evidence = %q on an entry whose only recorded seat was pinned", rev.SeatEvidence)
	}
}

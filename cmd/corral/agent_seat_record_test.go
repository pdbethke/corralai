// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/bugcatch"
	"github.com/pdbethke/corralai/internal/modelrank"
	"github.com/pdbethke/corralai/internal/reposcan"
	"github.com/pdbethke/corralai/internal/review"
	"github.com/pdbethke/corralai/internal/scanstore"
)

// versionedAgent defines operator agent NAME whose --version prints version.
func versionedAgent(t *testing.T, name, version string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent-"+name)
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo '" + version + "'; exit 0; fi\ncat >/dev/null\necho x\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil { // #nosec G306 -- test fixture must be executable
		t.Fatal(err)
	}
	t.Setenv("CORRALAI_AGENT_"+strings.ToUpper(name), path+" {model:--model}")
}

// --- ruling 1: the record ----------------------------------------------

// certify computes each seat's tool with the same helper review uses, and the
// RunSpec — from which both verdict construction sites take it — carries it.
func TestCertifySeatToolsReachTheRunSpec(t *testing.T) {
	noKeys(t)
	versionedAgent(t, "fakew", "fake-cli 1.0")
	r, err := resolveAuditRoles(localAuditInput{mutantModel: "qwen3.5:9b", writerModel: "fakew:m1", criticModel: "off", shadowModel: "off"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	rs := newAuditRunSpec(localAuditInput{}, r, runSubject{})
	if got := rs.SeatTools[advpool.RoleTestWriter]; got != agentbackend.SeatTool("fakew:m1") || !strings.Contains(got, "fake-cli 1.0") {
		t.Fatalf("writer tool = %q", got)
	}
	if got, ok := rs.SeatTools[advpool.RoleMutantGenerator]; ok {
		t.Fatalf("an API seat carries no tool, got %q", got)
	}
}

// The verdict cache key names the tool version: a CLI upgrade re-measures
// rather than reusing a verdict a different CLI earned. An API-only key is
// byte-identical to what it always was.
func TestTheModelSetKeyCarriesTheAgentVersion(t *testing.T) {
	apiOnly := modelSetKey("w-1", "m-1", "c-1", "off", "")
	if apiOnly != reposcan.CanonicalKV(map[string]string{"test-writer": "w-1", "mutant-generator": "m-1", "critic": "c-1", "shadow": "off"}) {
		t.Fatalf("an API-only key changed: %q", apiOnly)
	}
	versionedAgent(t, "fakew", "fake-cli 1.0")
	k1 := modelSetKey("fakew:m1", "m-1", "c-1", "off", "")
	if !strings.Contains(k1, "fake-cli 1.0") {
		t.Fatalf("the key does not name the tool version: %q", k1)
	}
	versionedAgent(t, "fakew", "fake-cli 2.0")
	if k2 := modelSetKey("fakew:m1", "m-1", "c-1", "off", ""); k2 == k1 {
		t.Fatalf("a CLI upgrade left the key unchanged: %q", k2)
	}
}

// The --repo ledger entry carries the roster's tools beside the roster.
func TestTheRepoBundleCarriesTheSeatTools(t *testing.T) {
	b := buildBundle(scanstore.Scan{Owner: "o", Repo: "r", Commit: "c"}, 0,
		[]scanstore.File{{Path: "a.go", Lang: "go", Disposition: "audited"}}, nil, nil, nil,
		auditpush.Link{}, false, "o/r", "c", "", bundleMeta{ModelsByRole: `{"test-writer":"codex"}`, SeatTools: `{"test-writer":"codex-cli 1"}`})
	if len(b.Files) != 1 || b.Files[0].SeatTools != `{"test-writer":"codex-cli 1"}` {
		t.Fatalf("bundle rows: %+v", b.Files)
	}
}

// models rank and the scorecard leave an unresolved certify seat out — from
// the local bug-catching ledger and from a pushed warehouse alike.
func TestPerModelAnalysisExcludesAnUnpinnedCertifySeat(t *testing.T) {
	store, err := bugcatch.Open(filepath.Join(t.TempDir(), "bc.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ts := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	if err := store.Record(context.Background(), []bugcatch.Observation{
		{TS: ts, RecordID: 1, Model: "codex", Role: modelrank.SeatTestWriter, Catches: 3, Opportunities: 4, Lang: "go", SeatTool: "codex-cli 1", ModelUnresolved: true},
		{TS: ts, RecordID: 1, Model: "codex:gpt-6", Role: modelrank.SeatTestWriter, Catches: 1, Opportunities: 4, Lang: "go", SeatTool: "codex-cli 1"},
	}); err != nil {
		t.Fatal(err)
	}
	ev, err := bugcatchRankEvidence(context.Background(), store, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range ev.Obs {
		if o.Model == "codex" {
			t.Error("models rank counted an unpinned agent seat")
		}
	}
	if len(ev.Obs) != 1 {
		t.Errorf("want the pinned seat's row only, got %+v", ev.Obs)
	}
	cells, err := store.Scorecard(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cells {
		if c.Model == "codex" {
			t.Error("the scorecard counted an unpinned agent seat")
		}
	}
	evid, _, err := store.Evidence(context.Background(), []string{modelrank.SeatTestWriter}, "go")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := evid["codex"]; ok {
		t.Error("the shadow draw's evidence counted an unpinned agent seat")
	}

	// The warehouse: corral_audits carries the roster's tools.
	target := filepath.Join(t.TempDir(), "w.duckdb")
	rows := []auditpush.Row{
		{Repo: "o/r", Commit: "c", Path: "a.go", Lang: "go", Disposition: "audited", ScanID: 1,
			ModelsByRole: `{"test-writer":"codex","mutant-generator":"g1"}`, SeatTools: `{"test-writer":"codex-cli 1"}`,
			Survivors: 4, ProvenMissed: 3, MutantsPlanted: 5, MutantsGraded: 5},
	}
	if _, err := auditpush.PushBundle(target, auditpush.Bundle{Files: rows}); err != nil {
		t.Fatal(err)
	}
	db, err := attachWarehouse(target, true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	wev, err := warehouseRankEvidence(db, target)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range wev.Obs {
		if o.Model == "codex" {
			t.Error("models rank --db counted an unpinned agent seat")
		}
	}
	if len(wev.Obs) != 1 || wev.Obs[0].Model != "g1" {
		t.Errorf("want only the API generator's row, got %+v", wev.Obs)
	}
}

// The same rule closes review: an unpinned agent reviewer is left out of
// models rank; a pinned one and an API verifier are not.
func TestPerModelAnalysisExcludesAnUnpinnedReviewSeat(t *testing.T) {
	t.Setenv("CORRALAI_CERTIFY_KEY_FILE", filepath.Join(t.TempDir(), "certify_key"))
	dir := filepath.Join(t.TempDir(), "ledger")
	writeCacheTestEntry(t, dir, nil)
	signer, _, _ := ledgerSignerFromLocalKey()
	code := 0
	// "my-agent" is an operator agent no environment here defines: only the
	// recorded tool can say it was an agent, not a model.
	for _, reviewer := range []string{"codex", "my-agent", "codex:gpt-6"} {
		r := review.Review{Repo: "acme/r", Commit: "abc", Scope: "pkg", ReviewerModel: reviewer, ReviewerTool: "a-cli 1",
			VerifierModel: "ver-m", Lang: "go",
			Findings: []review.Finding{{ID: "R1", Declared: review.TierReproduced, Tier: review.TierReproduced, ExitCode: &code,
				Refutation: &review.Refutation{Model: "ver-m", Verdict: review.VerdictStands}}}}
		if _, err := auditpush.WriteReview(dir, r, signer); err != nil {
			t.Fatal(err)
		}
	}
	vdb, err := auditpush.LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer vdb.Close()
	obs, err := reviewRankEvidence(vdb)
	if err != nil {
		t.Fatal(err)
	}
	reviewers, verifiers := 0, 0
	for _, o := range obs {
		if o.Role == modelrank.SeatReviewer {
			reviewers++
			if o.Model != "codex:gpt-6" {
				t.Errorf("models rank counted the unpinned agent reviewer %q", o.Model)
			}
		}
		if o.Role == modelrank.SeatVerifier {
			verifiers++
		}
	}
	if reviewers != 1 || verifiers != 3 {
		t.Errorf("want 1 reviewer row (the pinned one) and 3 verifier rows, got %d and %d", reviewers, verifiers)
	}
}

// --- ruling 3: the goal deriver is a seat under the cap -----------------

// The deriver is charged to the run's budget and refused past it, without
// dialling the provider.
func TestTheGoalDeriverIsChargedToTheRunsCap(t *testing.T) {
	fb := &fakeBackend{reply: "a goal", usage: agentbackend.Usage{InputTokens: 600, OutputTokens: 100}}
	budget := agentbackend.NewTokenBudget(1000)
	d := withDeriveBudget(newMeteredDeriver(fb, "m"), budget)
	c := reposcan.Candidate{Path: "pkg/a.go", Lang: "go"}
	for i := 0; i < 2; i++ {
		if _, _, err := d.Derive(context.Background(), c, "package pkg\n"); err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
	}
	if budget.Spent() != 1400 {
		t.Fatalf("spent %d, want 1400 — the deriver's calls must be charged", budget.Spent())
	}
	fb.sent = nil
	if _, _, err := d.Derive(context.Background(), c, "package pkg\n"); !errors.Is(err, agentbackend.ErrTokenBudgetExhausted) {
		t.Fatalf("third call: %v, want the cap", err)
	}
	if fb.sent != nil {
		t.Fatal("the refused call reached the provider")
	}
}

// An unmetered derive seat is refused up front on --repo, like any seat.
func TestCertifyRepoMaxTokensRefusesAnUnmeteredDeriveSeat(t *testing.T) {
	noKeys(t)
	wiredAgent(t, "fakew", "claude-json", claudeJSONReply)
	wiredAgent(t, "fakem", "claude-json", claudeJSONReply)
	wiredAgent(t, "faked", "", "x")
	root := t.TempDir()
	gitRun := gitCmd(t, root)
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n\nfunc A() int { return 1 }\n")
	mustWrite(t, filepath.Join(root, "pkg", "a_test.go"), "package pkg\n")
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")
	derived := 0
	prev := certifyRepoDeriver
	t.Cleanup(func() { certifyRepoDeriver = prev })
	certifyRepoDeriver = func(model, _ string) (reposcan.Deriver, error) {
		derived++
		return newMeteredDeriver(&fakeBackend{reply: "NONE"}, model), nil
	}
	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{
		"--repo", root, "--derive-model", "faked", "--no-goal-cache", "--ledger", filepath.Join(t.TempDir(), "ledger"),
		"--writer-model", "fakew", "--mutant-model", "fakem", "--critic-model", "off",
		"--substrate", substrateWorkspace, "--max-tokens", "1000", "--", "true",
	}, &out, &errb)
	want := "--max-tokens: seat goal-deriver (faked) reports no token usage"
	if code != 2 || !strings.Contains(errb.String(), want) {
		t.Fatalf("exit %d, want 2 naming the derive seat\nstderr:%s", code, errb.String())
	}
	if derived != 0 {
		t.Error("a deriver was built before the refusal")
	}
}

// --- ruling 4: the scan header ------------------------------------------

// A scan none of whose calls was metered writes NULL token totals on its
// header; a mixed scan keeps the metered sum.
func TestTheScanHeaderTokensAreNullWhenNothingWasMetered(t *testing.T) {
	for _, c := range []struct {
		name     string
		rows     []scanstore.ModelCall
		wantNull bool
	}{
		{"all unmetered", []scanstore.ModelCall{{Path: "a.go", Role: "test-writer", Calls: 2, UnmeteredCalls: 2}}, true},
		{"mixed", []scanstore.ModelCall{
			{Path: "a.go", Role: "test-writer", Calls: 2, UnmeteredCalls: 2},
			{Path: "a.go", Role: "mutant-generator", Calls: 1, InputTokens: 50, OutputTokens: 5},
		}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			var calls int64
			var in, out int64
			for _, r := range c.rows {
				calls += int64(r.Calls)
				in, out = in+r.InputTokens, out+r.OutputTokens
			}
			scan := scanstore.Scan{Owner: "o", Repo: "r", Commit: "c", ModelCalls: calls, InputTokens: in, OutputTokens: out}
			b := buildBundle(scan, 0, []scanstore.File{{Path: "a.go", Lang: "go", Disposition: "audited"}}, nil, c.rows, nil,
				auditpush.Link{}, false, "o/r", "c", "", bundleMeta{})
			wh := filepath.Join(t.TempDir(), "w.duckdb")
			if _, err := pushBundle(wh, b); err != nil {
				t.Fatal(err)
			}
			db, err := attachWarehouse(wh, true)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var gin, gout *int64
			var gcalls int64
			if err := db.QueryRow(`SELECT input_tokens, output_tokens, model_calls FROM corral_scans`).Scan(&gin, &gout, &gcalls); err != nil {
				t.Fatal(err)
			}
			if c.wantNull && (gin != nil || gout != nil) {
				t.Fatalf("header tokens = %v/%v, want NULL", gin, gout)
			}
			if !c.wantNull && (gin == nil || *gin != 50 || gout == nil || *gout != 5) {
				t.Fatalf("header tokens = %v/%v, want 50/5", gin, gout)
			}
			if gcalls != calls {
				t.Fatalf("model_calls = %d, want %d", gcalls, calls)
			}
			if _, err := auditpush.ReadBundle(db, "o/r", 0); err != nil {
				t.Fatalf("a NULL header must still read back: %v", err)
			}
		})
	}
}

// The verdict's models line prints an agent seat as review prints its seats.
func TestTheModelsLinePrintsTheAgentTool(t *testing.T) {
	got := formatModels(map[string]string{"test-writer": "codex", "mutant-generator": "gpt-6"}, map[string]string{"test-writer": "codex-cli 1"})
	if want := "mutant-generator=gpt-6  test-writer=codex [codex-cli 1]"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

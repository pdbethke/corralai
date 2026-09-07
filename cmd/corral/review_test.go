// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/certify"
)

type cannedReviewer struct {
	reply string
	saw   *string // the user turn the seat was given, for assertions
}

func (c cannedReviewer) Chat(msgs []agentbackend.Message, _ []any) (agentbackend.Message, error) {
	if c.saw != nil && len(msgs) > 1 {
		*c.saw = msgs[1].Content
	}
	return agentbackend.Message{Role: "assistant", Content: c.reply}, nil
}

// TestReviewRunsReproductionsRecordsTheEntryAndTakesAdjudications is the
// loop end to end, with the model canned: two REPRODUCED claims (one whose
// script holds against the tree, one whose script does not), one
// CODE-READ, one HYPOTHESIS; the entry written; `review show` reading it;
// an adjudication taken and shown; `verify --ledger` naming both.
func TestReviewRunsReproductionsRecordsTheEntryAndTakesAdjudications(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	root := t.TempDir()
	gitRun := gitCmd(t, root)
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n\nfunc Add(a, b int) int { return a - b }\n")
	mustWrite(t, filepath.Join(root, "pkg", "a_test.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "go.mod"), "module x\n\ngo 1.22\n")
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")
	commit := gitRevParseHead(t, root)

	orig := newReviewerBackend
	t.Cleanup(func() { newReviewerBackend = orig })
	newReviewerBackend = func(model, _ string) (agentbackend.Backend, error) {
		if model != "reviewer-x" {
			t.Fatalf("seat resolved to %q", model)
		}
		return cannedReviewer{reply: `{"opinion":"Add subtracts (R1). R2 is wrong on purpose.",
"findings":[
 {"claim":"Add returns a - b","tier":"REPRODUCED","file":"pkg/a.go","line":3,"severity":"high","script":"grep -n 'return a - b' pkg/a.go"},
 {"claim":"go.mod is missing","tier":"REPRODUCED","file":"go.mod","line":1,"severity":"low","script":"test ! -f go.mod"},
 {"claim":"the test file tests nothing","tier":"CODE-READ","file":"pkg/a_test.go","line":1,"severity":"medium"},
 {"claim":"a caller might pass overflow values","tier":"HYPOTHESIS"}
],"sound":["the module declaration"]}`}, nil
	}

	ledger := filepath.Join(t.TempDir(), "ledger")
	wh := filepath.Join(t.TempDir(), "wh.duckdb")
	var out, errb bytes.Buffer
	code := runReview([]string{"--repo", root, "--scope", "pkg", "--reviewer-model", "reviewer-x", "--ledger", ledger, "--push", wh, "--push-source"}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d: stdout=%s stderr=%s", code, out.String(), errb.String())
	}
	if !strings.Contains(out.String(), "pushed 1 review, 4 finding row(s) to "+wh) {
		t.Errorf("the push must be reported:\n%s", out.String())
	}
	s := out.String()
	for _, want := range []string{
		"scope pkg: 2 file(s)",
		"findings: 1 reproduced, 2 code-read, 1 hypothesis",
		"R1  REPRODUCED",
		"R2  CODE-READ (declared REPRODUCED)",
		"R2 — DEMOTED to CODE-READ: the script exited 1",
		"return a - b", // R1's evidence, printed
		"checked and found sound:",
		"· the module declaration",
		"ledger: review entry",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("stdout lacks %q:\n%s", want, s)
		}
	}
	// The operator's checkout was never the subject: no worktree left behind.
	if entries, _ := filepath.Glob(filepath.Join(root, "*")); len(entries) != 3 { // .git, pkg, go.mod
		t.Errorf("the checkout changed: %v", entries)
	}

	entries, err := auditpush.ReadLedgerDir(ledger)
	if err != nil || len(entries) != 1 || entries[0].Kind != auditpush.KindReview {
		t.Fatalf("ledger: %d entries, err %v", len(entries), err)
	}
	rev := entries[0]
	if rev.Review.Commit != commit || rev.Review.Findings[0].ExitCode == nil || *rev.Review.Findings[0].ExitCode != 0 {
		t.Errorf("the entry does not carry the reproduction: %+v", rev.Review.Findings[0])
	}

	out.Reset()
	if code := runReview([]string{"adjudicate", ledger, rev.Hash[:12] + "#R2", "--refute", "--reason", "the module file is right there", "--by", "pdb", "--push", wh}, &out, &errb); code != 0 {
		t.Fatalf("adjudicate: %d %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "pushed the verdict to "+wh) {
		t.Errorf("the adjudication push must be reported:\n%s", out.String())
	}
	// The warehouse holds the review, its findings WITH source (--push-source), and the verdict.
	wdb, werr := attachWarehouse(wh, true)
	if werr != nil {
		t.Fatal(werr)
	}
	var nr, nf, na int
	var script string
	_ = wdb.QueryRow(`SELECT count(*) FROM corral_reviews`).Scan(&nr)
	_ = wdb.QueryRow(`SELECT count(*) FROM corral_findings`).Scan(&nf)
	_ = wdb.QueryRow(`SELECT count(*) FROM corral_adjudications`).Scan(&na)
	_ = wdb.QueryRow(`SELECT script FROM corral_findings WHERE finding_id = 'R1'`).Scan(&script)
	wdb.Close()
	if nr != 1 || nf != 4 || na != 1 || !strings.Contains(script, "grep") {
		t.Errorf("warehouse: %d reviews, %d findings, %d adjudications, R1 script %q", nr, nf, na, script)
	}
	out.Reset()
	if code := runReview([]string{"adjudicate", ledger, rev.Hash[:12] + "#R2", "--confirm", "--refute", "--reason", "x"}, &out, &errb); code != 2 {
		t.Errorf("--confirm and --refute together must be a usage error, got %d", code)
	}
	out.Reset()
	if code := runReview([]string{"show", ledger, rev.Hash[:12]}, &out, &errb); code != 0 {
		t.Fatalf("show: %d %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "refuted by pdb") || !strings.Contains(out.String(), "the module file is right there") {
		t.Errorf("show must apply the adjudication:\n%s", out.String())
	}
	out.Reset()
	if code := runLedger([]string{"verify", ledger}, &out, &errb); code != 0 || !strings.Contains(out.String(), "review of pkg by reviewer-x: 1 reproduced") || !strings.Contains(out.String(), "refuted "+rev.Hash[:12]+"#R2 by pdb") {
		t.Errorf("verify: exit %d\n%s", code, out.String())
	}
}

func TestReviewRefusesWithoutASeatOrAScopeOrACommit(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runReview([]string{"--scope", "x"}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "--reviewer-model") {
		t.Errorf("no seat: %d %s", code, errb.String())
	}
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "a.go"), "package a\n")
	if code := runReview([]string{"--repo", root, "--scope", ".", "--reviewer-model", "m"}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "not a git checkout") {
		t.Errorf("no commit: %d %s", code, errb.String())
	}
}

// TestReviewVerifierSeatRefutesByReproductionAndIsDecorrelated: the
// verifier is handed the review AS RECORDED (the demoted finding says so),
// its REPRODUCED refutation runs in the same worktree and, holding, demotes
// the finding; a refutation whose script fails is itself demoted and moves
// nothing; the verifier's model may never be the reviewer's.
func TestReviewVerifierSeatRefutesByReproductionAndIsDecorrelated(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	root := t.TempDir()
	gitRun := gitCmd(t, root)
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n\nfunc Add(a, b int) int { return a + b }\n")
	mustWrite(t, filepath.Join(root, "go.mod"), "module x\n\ngo 1.22\n")
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")

	var verifierSaw string
	orig := newReviewerBackend
	t.Cleanup(func() { newReviewerBackend = orig })
	newReviewerBackend = func(model, _ string) (agentbackend.Backend, error) {
		switch model {
		case "reviewer-x":
			return cannedReviewer{reply: `{"opinion":"o","findings":[
 {"claim":"Add subtracts","tier":"REPRODUCED","file":"pkg/a.go","line":3,"severity":"high","script":"grep -q 'a + b' pkg/a.go"},
 {"claim":"go.mod is missing","tier":"REPRODUCED","file":"go.mod","line":1,"severity":"low","script":"test ! -f go.mod"},
 {"claim":"Add has no overflow check","tier":"CODE-READ","file":"pkg/a.go","line":3,"severity":"low"}
],"sound":[]}`}, nil
		case "verifier-y":
			return cannedReviewer{saw: &verifierSaw, reply: `{"opinion":"R1 is wrong; R2 the reviewer already lost; R3 stands","refutations":[
 {"id":"R1","verdict":"REFUTED","tier":"REPRODUCED","argument":"Add adds: the source says a + b","script":"grep -q 'return a + b' pkg/a.go"},
 {"id":"R2","verdict":"REFUTED","tier":"REPRODUCED","argument":"go.mod exists","script":"exit 3"},
 {"id":"R3","verdict":"STANDS","argument":"true, there is no check"}
]}`}, nil
		}
		t.Fatalf("unexpected seat %q", model)
		return nil, nil
	}

	var out, errb bytes.Buffer
	if code := runReview([]string{"--repo", root, "--scope", "pkg", "--reviewer-model", "reviewer-x", "--verifier-model", "reviewer-x", "--no-ledger"}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "reviewer's own model") {
		t.Fatalf("a verifier that is the reviewer must be refused before anything is spent: exit %d %s", code, errb.String())
	}
	out.Reset()
	errb.Reset()
	code := runReview([]string{"--repo", root, "--scope", "pkg", "--reviewer-model", "reviewer-x", "--verifier-model", "verifier-y", "--no-ledger"}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d: stdout=%s stderr=%s", code, out.String(), errb.String())
	}
	s := out.String()
	for _, want := range []string{
		"verifier: verifier-y",
		"findings: 0 reproduced, 3 code-read, 0 hypothesis", // R1 demoted by the verifier, R2 by its own script, R3 as declared
		"R1  CODE-READ (declared REPRODUCED)",
		"refuted (reproduced) by verifier-y",
		"R1 — DEMOTED to CODE-READ: refuted by verifier-y, reproduced: Add adds",
		"R2 — refutation DEMOTED to CODE-READ: the script exited 3",
		"R3 — STANDS: true, there is no check",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("stdout lacks %q:\n%s", want, s)
		}
	}
	// The verifier saw the review as recorded: R2's demotion, and R1's script and output.
	if !strings.Contains(verifierSaw, "R2 [CODE-READ, declared REPRODUCED") || !strings.Contains(verifierSaw, "grep -q 'a + b' pkg/a.go") {
		t.Errorf("the verifier must be handed the record, demotions and scripts included:\n%s", verifierSaw)
	}
}

// TestReviewAttestSignsTheReproductionsAndTheEntryNamesTheStatement:
// --attest writes the in-toto statement and its DSSE envelope, the entry
// written after it carries the statement's hash, `corral verify --attest
// … --db <ledger>` finds the entry by that hash and recomputes the
// reproductions' hash — and an entry whose findings were edited after the
// statement was signed is named as such.
func TestReviewAttestSignsTheReproductionsAndTheEntryNamesTheStatement(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	t.Setenv("CORRALAI_CERTIFY_KEY_FILE", filepath.Join(t.TempDir(), "certify_key"))
	root := t.TempDir()
	gitRun := gitCmd(t, root)
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n")
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")

	orig := newReviewerBackend
	t.Cleanup(func() { newReviewerBackend = orig })
	newReviewerBackend = func(string, string) (agentbackend.Backend, error) {
		return cannedReviewer{reply: `{"opinion":"o","findings":[{"claim":"pkg exists","tier":"REPRODUCED","file":"pkg/a.go","line":1,"severity":"low","script":"test -f pkg/a.go"}],"sound":["s"]}`}, nil
	}
	ledger := filepath.Join(t.TempDir(), "ledger")
	stmtPath := filepath.Join(t.TempDir(), "review-statement.json")
	var out, errb bytes.Buffer
	if code := runReview([]string{"--repo", root, "--scope", "pkg", "--reviewer-model", "m", "--ledger", ledger, "--attest", stmtPath}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s%s", code, out.String(), errb.String())
	}
	if !strings.Contains(out.String(), "attestation: "+stmtPath) || !strings.Contains(out.String(), "signed into") {
		t.Errorf("stdout must name the statement and its envelope:\n%s", out.String())
	}
	raw, err := os.ReadFile(stmtPath)
	if err != nil {
		t.Fatal(err)
	}
	var stmt map[string]any
	if err := json.Unmarshal(raw, &stmt); err != nil {
		t.Fatal(err)
	}
	if stmt["predicateType"] != certify.ReviewPredicateType {
		t.Errorf("predicateType = %v", stmt["predicateType"])
	}
	pred := stmt["predicate"].(map[string]any)
	if pred["opinionSha256"] == "" || pred["reproductionsSha256"] == "" || strings.Contains(string(raw), `"opinion":"o"`) {
		t.Errorf("the statement must bind the opinion by hash and never carry it: %s", raw)
	}
	entries, _ := auditpush.ReadLedgerDir(ledger)
	sum := sha256.Sum256(raw)
	if len(entries) != 1 || entries[0].Review.StatementSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("the entry must name the statement it followed: %+v", entries[0].Review)
	}

	out.Reset()
	errb.Reset()
	if code := runVerifyAttest([]string{"--attest", stmtPath, "--db", ledger}, &out, &errb); code != 0 || !strings.Contains(out.String(), "✓ signature") || !strings.Contains(out.String(), "✓ ledger entry") {
		t.Fatalf("verify: exit %d\n%s%s", code, out.String(), errb.String())
	}

	// Tamper: rewrite the entry's finding tier. The chain is broken (the
	// entry's own hash no longer matches) AND the statement's cross-check
	// names the mismatch on its own.
	names, _ := os.ReadDir(filepath.Join(ledger, auditpush.ScansSubdir))
	e := entries[0]
	e.Review.Findings[0].Tier = "HYPOTHESIS"
	b, _ := json.Marshal(e)
	if err := os.WriteFile(filepath.Join(ledger, auditpush.ScansSubdir, names[0].Name()), gzipBytesFor(t, b), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := runVerifyAttest([]string{"--attest", stmtPath, "--db", ledger}, &out, &errb); code != 1 || !strings.Contains(out.String(), "✗ ledger entry") || !strings.Contains(out.String(), "changed after the statement was signed") {
		t.Errorf("a tampered entry must fail the cross-check by name: exit %d\n%s", code, out.String())
	}
}

func gzipBytesFor(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

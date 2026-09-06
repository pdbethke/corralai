// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/auditpush"
)

type cannedReviewer struct{ reply string }

func (c cannedReviewer) Chat(_ []agentbackend.Message, _ []any) (agentbackend.Message, error) {
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
	var out, errb bytes.Buffer
	code := runReview([]string{"--repo", root, "--scope", "pkg", "--reviewer-model", "reviewer-x", "--ledger", ledger}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d: stdout=%s stderr=%s", code, out.String(), errb.String())
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
	if code := runReview([]string{"adjudicate", ledger, rev.Hash[:12] + "#R2", "--refute", "--reason", "the module file is right there", "--by", "pdb"}, &out, &errb); code != 0 {
		t.Fatalf("adjudicate: %d %s", code, errb.String())
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

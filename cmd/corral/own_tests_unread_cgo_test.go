//go:build cgo

// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/reposcan"
)

// setAsideSrc is a Rust source whose line 2 the grammar sets aside (an ERROR
// node between two items): where its own tests are cannot be read.
const setAsideSrc = "pub fn f() -> i32 { 1 }\n@@@\n#[cfg(test)]\nmod tests {\n    #[test]\n    fn t() {}\n}\n"

func setAsideRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "Cargo.toml"), "[package]\nname = \"x\"\nversion = \"0.1.0\"\nedition = \"2021\"\n")
	mustWrite(t, filepath.Join(root, "src", "bad.rs"), setAsideSrc)
	return root
}

// assertOwnTestsRefusal: the refusal carries its own scan reason, names the
// set-aside lines, and reads as nothing the compile gate or a kill rate says.
func assertOwnTestsRefusal(t *testing.T, where string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: a Rust source whose own tests cannot be located was admitted", where)
	}
	rc, ok := err.(reposcan.ReasonCarrier)
	if !ok || rc.ScanReason() != reposcan.ReasonOwnTestsUnreadable {
		t.Fatalf("%s: refusal %v; want scan reason %q", where, err, reposcan.ReasonOwnTestsUnreadable)
	}
	assertNoGradingWords(t, where, err.Error())
	if !strings.Contains(err.Error(), "lines 2-2") || !strings.Contains(err.Error(), "own tests") {
		t.Errorf("%s: refusal does not name the set-aside lines and the tests: %v", where, err)
	}
}

func assertNoGradingWords(t *testing.T, where, text string) {
	t.Helper()
	for _, w := range []string{"compile check", "0.00", "0/0"} {
		if strings.Contains(text, w) {
			t.Errorf("%s says %q, which reads as a graded or compile-rejected file:\n%s", where, w, text)
		}
	}
}

// TestEveryDoorRefusesARustFileWhoseOwnTestsCannotBeLocated (N1): every door
// that can start an audit of such a file refuses it before any model is
// called — the certify --local door (resolveAuditPlugin, which every
// certify --repo job also goes through), a certify --repo job, whose audit
// (the paid step) must never run, doctor's pairing check, and certify
// --local end to end, whose seats are a counting agent that must never be
// called. Before, the file was audited, the mutant generator was paid, every
// mutant was discarded, and --local printed `dev_kill_rate: 0.00` and "failed
// the compile check" while --repo marked it "rejected by the compile check".
func TestEveryDoorRefusesARustFileWhoseOwnTestsCannotBeLocated(t *testing.T) {
	root := setAsideRepo(t)
	stockRustup(t)
	withSystemToolchain(t, true)

	t.Run("certify --local door", func(t *testing.T) {
		_, err := resolveAuditPlugin(localAuditInput{repoDir: root, codePath: "src/bad.rs", goal: "g", checkArgv: []string{"cargo", "test"}, substrate: substrateWorkspace})
		assertOwnTestsRefusal(t, "resolveAuditPlugin", err)
	})

	t.Run("certify --repo job", func(t *testing.T) {
		var progress bytes.Buffer
		ex := newLocalExecutor(root, []string{"cargo", "test"}, substrateWorkspace, 0, &progress)
		audits := 0
		ex.audit = func(context.Context, localAuditInput) (advpool.Verdict, error) {
			audits++
			return advpool.Verdict{}, nil
		}
		_, err := ex.Execute(context.Background(), reposcan.Job{Path: "src/bad.rs", Lang: "rust", Goal: reposcan.Goal{Text: "g"}})
		assertOwnTestsRefusal(t, "Execute", err)
		if audits != 0 {
			t.Errorf("the audit (mutant generation and grading) ran %d time(s) for a refused file", audits)
		}
		assertNoGradingWords(t, "the scan's progress", progress.String())
		if g := reasonGloss(reposcan.ReasonOwnTestsUnreadable); g == "" || !strings.Contains(g, "NOT a test-quality verdict") {
			t.Errorf("gloss = %q", g)
		} else {
			assertNoGradingWords(t, "the gloss", g)
		}
	})

	t.Run("doctor", func(t *testing.T) {
		t.Chdir(root)
		got := checkPairing("src/bad.rs", "")
		if got.ok || !strings.Contains(got.detail, "lines 2-2") {
			t.Fatalf("doctor passed a source whose own tests cannot be located: %+v", got)
		}
	})

	t.Run("certify --local end to end", func(t *testing.T) {
		calls := filepath.Join(t.TempDir(), "calls")
		script := filepath.Join(t.TempDir(), "seat.sh")
		mustWrite(t, script, "#!/bin/sh\necho call >> "+calls+"\necho '[]'\n")
		if err := os.Chmod(script, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("CORRALAI_AGENT_COUNTER", script)
		t.Setenv("CORRALAI_AGENT_COUNTER_WIRE", "text")
		var out, errBuf bytes.Buffer
		rc := runCertifyLocal([]string{"--code", "src/bad.rs", "--repo-dir", root, "--goal", "g",
			"--writer-model", "counter", "--mutant-model", "counter", "--critic-model", "off", "--quiet", "--", "cargo", "test"}, &out, &errBuf)
		if rc != 2 {
			t.Fatalf("exit %d for a file refused before any spend, want 2; stdout:\n%s\nstderr:\n%s", rc, out.String(), errBuf.String())
		}
		all := out.String() + errBuf.String()
		if !strings.Contains(all, "lines 2-2") {
			t.Errorf("the refusal does not name the set-aside lines:\n%s", all)
		}
		assertNoGradingWords(t, "certify --local", all)
		if b, err := os.ReadFile(calls); err == nil && len(b) > 0 {
			t.Errorf("a seat was called %d time(s) before the refusal", strings.Count(string(b), "call"))
		}
	})
}

// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/reposcan"
)

// interruptedRepoRun runs the real `certify --repo` on a one-file repo whose
// goal is derived by a fake, with the given seats, and asserts that an
// operator's interrupt mid-scan records NOTHING: no ledger entry, no push,
// no file labelled executor-error, a non-zero exit saying so.
func interruptedRepoRun(t *testing.T, mutant, writer string) {
	t.Helper()
	t.Cleanup(agentbackend.ResetInterrupted)
	root := t.TempDir()
	gitRun := gitCmd(t, root)
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n\nfunc A(x int) int {\n\tif x > 0 {\n\t\treturn 1\n\t}\n\treturn 2\n}\n")
	mustWrite(t, filepath.Join(root, "pkg", "a_test.go"), "package pkg\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) { _ = A(1) }\n")
	mustWrite(t, filepath.Join(root, "go.mod"), "module x\n\ngo 1.22\n")
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")

	prev := certifyRepoDeriver
	t.Cleanup(func() { certifyRepoDeriver = prev })
	certifyRepoDeriver = func(model, _ string) (reposcan.Deriver, error) {
		return newMeteredDeriver(&fakeBackend{reply: "A returns 1 for positive input"}, model), nil
	}

	ledgerDir := filepath.Join(t.TempDir(), "ledger")
	push := filepath.Join(t.TempDir(), "wh.duckdb")
	mutants := filepath.Join(t.TempDir(), "mutants.json")
	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{
		"--repo", root, "--derive-model", "local-deriver", "--no-goal-cache",
		"--ledger", ledgerDir, "--push", push, "--record-mutants", mutants,
		"--writer-model", writer, "--mutant-model", mutant, "--critic-model", "off",
		"--substrate", substrateWorkspace,
		"--", "go", "test", "./...",
	}, &out, &errb)
	all := out.String() + errb.String()
	t.Logf("exit %d\n%s", code, all)
	if code != exitInterrupted || !strings.Contains(errb.String(), "interrupted — nothing recorded") {
		t.Errorf("want exit %d saying nothing was recorded, got %d", exitInterrupted, code)
	}
	// The Action commits this file back as the next run's prior: an empty
	// or partial set from a stopped run would be read as an exam.
	if _, err := os.Stat(mutants); !os.IsNotExist(err) {
		t.Errorf("an interrupted scan wrote --record-mutants %s", mutants)
	}
	if entries, _ := filepath.Glob(filepath.Join(ledgerDir, "scans", "*")); len(entries) != 0 {
		t.Errorf("an interrupted scan wrote %d ledger entr(ies)", len(entries))
	}
	if _, err := os.Stat(push); !os.IsNotExist(err) {
		t.Errorf("an interrupted scan pushed to %s", push)
	}
	if strings.Contains(all, reposcan.ReasonExecutorError) {
		t.Errorf("an interrupt was labelled %s", reposcan.ReasonExecutorError)
	}
}

// An agent seat interrupted mid-scan: the fake agent sends corral the
// operator's Ctrl-C itself, then waits to be killed.
func TestAnInterruptedRepoScanRecordsNothing_AgentSeat(t *testing.T) {
	noKeys(t)
	path := filepath.Join(t.TempDir(), "agent-intr")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'intr 1'; exit 0; fi\nkill -INT $PPID\nsleep 30\n"), 0o700); err != nil { // #nosec G306 -- test fixture must be executable
		t.Fatal(err)
	}
	t.Setenv("CORRALAI_AGENT_INTR", path+" {model:--model}")
	fakeAgent(t, "quiet", "x")
	interruptedRepoRun(t, "intr", "quiet")
}

// An API seat interrupted mid-scan: no agent is live, so only auditContext
// sees the signal. The rule is the same for every seat type.
func TestAnInterruptedRepoScanRecordsNothing_APISeat(t *testing.T) {
	noKeys(t)
	// Once: a second SIGINT would arrive after auditContext restored the
	// default action and kill the test binary.
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { _ = syscall.Kill(os.Getpid(), syscall.SIGINT) })
		http.Error(w, "stopped", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	t.Setenv("OLLAMA_URL", srv.URL)
	interruptedRepoRun(t, "local-mutant", "local-writer")
}

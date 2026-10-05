// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/reposcan"
)

// TestARepoScanReportsWhatGoalDerivationSpent runs the real `certify --repo`
// with a deriver that answers NONE for every file. Nothing is audited, so no
// verdict carries any model calls, which is exactly the case where the
// deriver's spend used to vanish: it was paid for every candidate and
// recorded nowhere. The scan's cost line must name it.
func TestARepoScanReportsWhatGoalDerivationSpent(t *testing.T) {
	root := t.TempDir()
	gitRun := gitCmd(t, root)
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n\nfunc A() int { return 1 }\n")
	mustWrite(t, filepath.Join(root, "pkg", "a_test.go"), "package pkg\n")
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")

	// The credential preflight only checks that a key is present; nothing in
	// this test reaches a provider (the deriver is a fake and every file is
	// ungoaled, so no seat runs).
	t.Setenv("ANTHROPIC_API_KEY", "test-not-a-real-key")

	prev := certifyRepoDeriver
	t.Cleanup(func() { certifyRepoDeriver = prev })
	certifyRepoDeriver = func(model, _ string) (reposcan.Deriver, error) {
		return newMeteredDeriver(&fakeBackend{reply: "NONE", usage: agentbackend.Usage{InputTokens: 1234, OutputTokens: 3}}, model), nil
	}

	ledgerDir := filepath.Join(t.TempDir(), "ledger")
	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{
		"--repo", root, "--derive-model", testHerdWriter, "--no-goal-cache",
		"--ledger", ledgerDir,
		"--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off",
		"--substrate", substrateWorkspace,
		"--", "true",
	}, &out, &errb)
	t.Logf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out.String(), errb.String())
	if !strings.Contains(out.String(), "cost:") || !strings.Contains(out.String(), roleGoalDeriver) {
		t.Fatalf("the scan's cost line must name the goal-deriver's spend")
	}

	// And the RECORD: the ledger entry's model-call rows are the same rows
	// the cost line was built from, so the deriver's row must be there too,
	// against the file it was asked about.
	entries, err := filepath.Glob(filepath.Join(ledgerDir, "scans", "*.json.gz"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("want one ledger entry, got %v (%v)", entries, err)
	}
	e, err := auditpush.ReadLedgerEntry(entries[0])
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range e.Bundle.Calls {
		if c.Role == roleGoalDeriver && c.Path == "pkg/a.go" && c.InputTokens == 1234 {
			found = true
		}
	}
	if !found {
		t.Fatalf("the signed entry must carry the goal-deriver's row for pkg/a.go: %+v", e.Bundle.Calls)
	}
}

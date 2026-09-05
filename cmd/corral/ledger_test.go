// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/auditpush"
)

// A repo scan writes its entry into <repo>/.corral/ledger by default, and a
// second scan reads the first as its prior by default — disclosed on the
// report. --no-ledger writes and reads nothing. Both scans replay a fixed
// set so no model is called.
func TestCertifyRepoWritesTheLedgerByDefaultAndReadsItAsThePrior(t *testing.T) {
	skipWithoutPythonCoverage(t)
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	t.Setenv("CORRALAI_CERTIFY_KEY_FILE", filepath.Join(t.TempDir(), "certify.key"))
	t.Setenv("CORRALAI_CERTIFY_KEY", "")
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "pyproject.toml"), "[tool.coverage.run]\nsource = [\"mypkg\"]\n")
	mustWrite(t, filepath.Join(root, "mypkg", "__init__.py"), "")
	a := "def a():\n    return 1\n"
	mustWrite(t, filepath.Join(root, "mypkg", "a.py"), a)
	mustWrite(t, filepath.Join(root, "tests", "test_a.py"), "from mypkg.a import a\n\n\ndef test_a():\n    assert a() == 1\n")
	// A ledger entry names the commit it audited, so the fixture is a
	// repository with one.
	for _, argv := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "fixture"}} {
		cmd := exec.Command("git", argv...) // #nosec G204 -- fixed argv
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", argv, err, out)
		}
	}
	setPath := filepath.Join(root, "mutants.json")
	if err := adequacy.WriteMutantSet(setPath, adequacy.MutantSetFile{
		Format: adequacy.MutantSetFormat,
		Files:  map[string]adequacy.MutantSetEntry{"mypkg/a.py": {ParentSHA256: shaOf(a), Mutants: []adequacy.RecordedMutant{{ID: "m1", Replace: "def a():\n    return 2\n"}}}},
	}); err != nil {
		t.Fatal(err)
	}
	run := func(extra ...string) string {
		var out, errb bytes.Buffer
		args := append([]string{
			"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off",
			"--goals", writeGoals(t, root, `{"mypkg/a.py": "a() must return 1"}`),
			"--substrate", substrateWorkspace, "--all", "--mutants", setPath,
		}, extra...)
		if code := runCertifyRepo(args, &out, &errb); code != 0 {
			t.Fatalf("exit=%d\n%s%s", code, out.String(), errb.String())
		}
		return out.String()
	}
	first := run()
	ledger := filepath.Join(root, ".corral", "ledger")
	if !strings.Contains(first, "ledger: entry written to "+ledger) {
		t.Fatalf("first run must write the default ledger:\n%s", first)
	}
	if strings.Contains(first, "prior:") {
		t.Errorf("a first run has no prior to read:\n%s", first)
	}
	entries, _ := auditpush.ReadLedgerDir(ledger)
	if len(entries) != 1 || entries[0].Signature == "" {
		t.Fatalf("want one signed entry, got %d (signed=%v)", len(entries), len(entries) > 0 && entries[0].Signature != "")
	}
	second := run()
	if !strings.Contains(second, "prior: 1 earlier entry in "+ledger) || !strings.Contains(second, "prior: 1 edit(s) earlier runs tried on these bytes") {
		t.Errorf("second run must read the first as its prior, and say so:\n%s", second)
	}
	entries, _ = auditpush.ReadLedgerDir(ledger)
	if len(entries) != 2 || entries[1].Prev != entries[0].Hash {
		t.Fatalf("second entry must link to the first: %d entries, prev=%q hash0=%q", len(entries), entries[1].Prev, entries[0].Hash)
	}
	var out, errb bytes.Buffer
	if code := runVerifyAttest([]string{"--ledger", ledger}, &out, &errb); code != 0 || !strings.Contains(out.String(), "2 entries, chain intact") {
		t.Errorf("chain: exit %d\n%s", code, out.String())
	}
	third := run("--no-ledger")
	if strings.Contains(third, "ledger: entry written") || strings.Contains(third, "prior: 1 edit(s)") {
		t.Errorf("--no-ledger must neither write nor read:\n%s", third)
	}
	if entries, _ = auditpush.ReadLedgerDir(ledger); len(entries) != 2 {
		t.Errorf("--no-ledger wrote an entry: %d", len(entries))
	}
	// --ledger <dir> moves it: a fresh directory gets a genesis, and the
	// default directory is untouched.
	elsewhere := filepath.Join(t.TempDir(), "ledger")
	fourth := run("--ledger", elsewhere)
	if !strings.Contains(fourth, "ledger: entry written to "+elsewhere) {
		t.Errorf("--ledger must move the entry:\n%s", fourth)
	}
	if moved, _ := auditpush.ReadLedgerDir(elsewhere); len(moved) != 1 || moved[0].Prev != "" {
		t.Errorf("the moved ledger must hold one genesis entry: %d", len(moved))
	}
	if entries, _ = auditpush.ReadLedgerDir(ledger); len(entries) != 2 {
		t.Errorf("--ledger elsewhere wrote into the default directory: %d", len(entries))
	}
}

// `corral ledger append`: an entry written elsewhere (a runner's staging
// dir; a laptop behind the branch) is re-linked to the target's CURRENT
// head, re-hashed and re-signed at placement — so a chain whose head moved
// under a writer stays intact. Appending an entry already in the directory
// is refused: that would be editing a placed entry.
func TestLedgerAppendRelinksToTheCurrentHead(t *testing.T) {
	t.Setenv("CORRALAI_CERTIFY_KEY_FILE", filepath.Join(t.TempDir(), "certify.key"))
	t.Setenv("CORRALAI_CERTIFY_KEY", "")
	target := t.TempDir()
	staging := t.TempDir()
	push := func(dir, commit string) {
		if _, err := pushBundle(dir+"/", auditpush.Bundle{Scan: auditpush.ScanRow{Repo: "r", Commit: commit, ScanID: 1}, Files: []auditpush.Row{{Repo: "r", ScanID: 1, Path: "a.py"}}}); err != nil {
			t.Fatal(err)
		}
	}
	push(target, "aaaa1111")  // the branch's head when the other writer started
	push(staging, "bbbb2222") // written elsewhere: a genesis in its own dir, prev ""
	push(target, "cccc3333")  // the branch moved meanwhile
	names, _ := os.ReadDir(filepath.Join(staging, auditpush.ScansSubdir))
	stagedEntry := filepath.Join(staging, auditpush.ScansSubdir, names[0].Name())

	var out, errb bytes.Buffer
	if code := runLedger([]string{"append", stagedEntry, target}, &out, &errb); code != 0 || !strings.Contains(out.String(), "linked to the current head") {
		t.Fatalf("append: exit %d\n%s%s", code, out.String(), errb.String())
	}
	entries, _ := auditpush.ReadLedgerDir(target)
	if len(entries) != 3 {
		t.Fatalf("want 3 entries, got %d", len(entries))
	}
	for i, e := range entries {
		t.Logf("entry %d: commit=%s prev=%.12s hash=%.12s signed=%v pushed=%s", i, e.Bundle.Scan.Commit, e.Prev, e.Hash, e.Signature != "", e.Pushed)
	}
	if entries[2].Prev != entries[1].Hash || entries[2].Bundle.Scan.Commit != "bbbb2222" || entries[2].Signature == "" {
		t.Fatalf("appended entry must sit at the head, linked and signed")
	}
	out.Reset()
	if code := runVerifyAttest([]string{"--ledger", target}, &out, &errb); code != 0 || !strings.Contains(out.String(), "3 entries, chain intact") {
		t.Errorf("chain after append: exit %d\n%s", code, out.String())
	}
	// Refused: an entry already placed in the target.
	tnames, _ := os.ReadDir(filepath.Join(target, auditpush.ScansSubdir))
	placed := filepath.Join(target, auditpush.ScansSubdir, tnames[0].Name())
	if code := runLedger([]string{"append", placed, target}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "never re-linked in place") {
		t.Errorf("re-linking a placed entry: exit %d\n%s", code, errb.String())
	}
}

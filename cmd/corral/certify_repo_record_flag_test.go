// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/auditpush"
)

// recordFlagFixture is a git repository with something to collapse: three
// files in a skipped directory (dist) and two Go test files, plus one
// candidate so the scan has a job to select away.
func recordFlagFixture(t *testing.T, withCollapsible bool) (root, goals string) {
	t.Helper()
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	root = t.TempDir()
	gitRun := gitCmd(t, root)
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "README.md"), "# x\n")
	if withCollapsible {
		mustWrite(t, filepath.Join(root, "pkg", "a_test.go"), "package pkg\n")
		mustWrite(t, filepath.Join(root, "pkg", "b_test.go"), "package pkg\n")
		for _, n := range []string{"x.js", "y.js", "z.js"} {
			mustWrite(t, filepath.Join(root, "dist", n), "module.exports = 1\n")
		}
	}
	gitRun("init", "-q")
	gitRun("add", "-f", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")
	goals = filepath.Join(root, "goals.json")
	mustWrite(t, goals, `{"pkg/a.go": "must not panic"}`)
	return root, goals
}

func recordFlagScan(t *testing.T, root, goals string, extra ...string) (out string, code int, ledgerDir string) {
	t.Helper()
	ledgerDir = filepath.Join(t.TempDir(), "ledger")
	base := gitRevParseHead(t, root)
	var o, e bytes.Buffer
	args := append([]string{
		"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off",
		"--diff-base", base, "--goals", goals, "--substrate", substrateWorkspace, "--ledger", ledgerDir,
	}, extra...)
	code = runCertifyRepo(args, &o, &e)
	if code != 0 {
		t.Fatalf("exit %d: stdout=%s stderr=%s", code, o.String(), e.String())
	}
	return o.String(), code, ledgerDir
}

// ledgerCollapsedCounts is what the written entry holds behind its counted
// rows: files in skipped-dir and gitignored rows, and files in is-test rows.
func ledgerCollapsedCounts(t *testing.T, dir string) (inDirs, testFiles int) {
	t.Helper()
	db, err := auditpush.LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.QueryRow(`SELECT
		COALESCE(sum(CASE WHEN reason IN ('skipped-dir','gitignored') THEN COALESCE(files, 1) END), 0),
		COALESCE(sum(CASE WHEN reason = 'is-test' THEN COALESCE(files, 1) END), 0)
		FROM corral_audits`).Scan(&inDirs, &testFiles); err != nil {
		t.Fatal(err)
	}
	return inDirs, testFiles
}

func ledgerRowCount(t *testing.T, dir string) (rows, files int) {
	t.Helper()
	db, err := auditpush.LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.QueryRow(`SELECT count(*), COALESCE(sum(COALESCE(files, 1)), 0) FROM corral_audits`).Scan(&rows, &files); err != nil {
		t.Fatal(err)
	}
	return rows, files
}

// By default the entry holds counted rows and stdout says so with the right
// counts; with --record-every-file the entry holds one row per file, the
// totals are the same, and stdout carries no record: line.
func TestCertifyRepoCollapsesByDefaultAndListsEveryFileWhenAsked(t *testing.T) {
	root, goals := recordFlagFixture(t, true)
	outDef, _, dirDef := recordFlagScan(t, root, goals)
	outAll, _, dirAll := recordFlagScan(t, root, goals, "--record-every-file")

	re := regexp.MustCompile(`(?m)^  record: (\d+) file\(s\) in skipped or ignored directories and (\d+) test file\(s\) are recorded as counts, not listed; --record-every-file lists them$`)
	m := re.FindStringSubmatch(outDef)
	if m == nil {
		t.Fatalf("default run printed no record: line:\n%s", outDef)
	}
	if m[1] != "3" || m[2] != "2" {
		t.Errorf("record: line counts %s in directories and %s test files, want 3 and 2", m[1], m[2])
	}
	// What the header says must be what the entry holds.
	gotDirs, gotTests := ledgerCollapsedCounts(t, dirDef)
	if fmt.Sprint(gotDirs) != m[1] || fmt.Sprint(gotTests) != m[2] {
		t.Errorf("header says %s and %s, the entry records %d and %d", m[1], m[2], gotDirs, gotTests)
	}
	if strings.Contains(outAll, "record:") && re.MatchString(outAll) {
		t.Errorf("--record-every-file printed the record: line:\n%s", outAll)
	}
	rowsDef, filesDef := ledgerRowCount(t, dirDef)
	rowsAll, filesAll := ledgerRowCount(t, dirAll)
	if rowsDef >= rowsAll {
		t.Errorf("default entry has %d rows, every-file has %d; the default must hold fewer", rowsDef, rowsAll)
	}
	if filesDef != filesAll {
		t.Errorf("total files differ: default %d, every-file %d; the totals must be the same either way", filesDef, filesAll)
	}
	// The entry-written line says both truths: how many files the entry
	// stands for and in how many rows. This fixture walks 8 files and the
	// default entry holds 5 rows; the every-file entry is 8 rows for 8 files.
	if filesDef != 8 || rowsDef != 5 {
		t.Fatalf("fixture changed: default entry is %d file(s) in %d row(s), test expects 8 and 5", filesDef, rowsDef)
	}
	if !strings.Contains(outDef, "(8 file(s) in 5 row(s), 0 mutant(s))") {
		t.Errorf("default entry-written line must say 8 file(s) in 5 row(s):\n%s", outDef)
	}
	if !strings.Contains(outAll, "(8 file(s) in 8 row(s), 0 mutant(s))") {
		t.Errorf("--record-every-file entry-written line must say 8 file(s) in 8 row(s):\n%s", outAll)
	}
}

// A repository with nothing to collapse prints no record: line.
func TestCertifyRepoPrintsNoRecordLineWhenNothingCollapsed(t *testing.T) {
	root, goals := recordFlagFixture(t, false)
	out, _, _ := recordFlagScan(t, root, goals)
	if strings.Contains(out, "  record:") {
		t.Errorf("nothing collapsed, yet a record: line was printed:\n%s", out)
	}
}

// With --no-ledger and no --push nothing is recorded, so the header must not
// say rows "are recorded as counts".
func TestCertifyRepoPrintsNoRecordLineWhenNothingIsRecorded(t *testing.T) {
	root, goals := recordFlagFixture(t, true)
	var o, e bytes.Buffer
	code := runCertifyRepo([]string{
		"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off",
		"--diff-base", gitRevParseHead(t, root), "--goals", goals, "--substrate", substrateWorkspace, "--no-ledger",
	}, &o, &e)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, e.String())
	}
	if strings.Contains(o.String(), "  record:") {
		t.Errorf("--no-ledger records nothing, yet a record: line was printed:\n%s", o.String())
	}
}

// --record-every-file with --no-ledger and no --push changes nothing that is
// recorded, so it is refused rather than silently inert.
func TestRecordEveryFileIsRefusedWhenNothingIsRecorded(t *testing.T) {
	root, goals := recordFlagFixture(t, true)
	var o, e bytes.Buffer
	code := runCertifyRepo([]string{"--repo", root, "--goals", goals, "--record-every-file", "--no-ledger"}, &o, &e)
	if code != 2 {
		t.Fatalf("exit %d, want 2: %s", code, e.String())
	}
	want := "--record-every-file changes what is recorded, and this run records nothing (--no-ledger with no --push)"
	if !strings.Contains(e.String(), want) {
		t.Errorf("stderr %q does not contain %q", e.String(), want)
	}
}

// corral brief names --record-every-file to the operator, so it must be a
// flag certify --repo really has, not one promised for later.
func TestRecordEveryFileIsRegisteredOnCertifyRepo(t *testing.T) {
	var o, e bytes.Buffer
	runCertifyRepo([]string{"-h"}, &o, &e)
	if !strings.Contains(o.String()+e.String(), "-record-every-file") {
		t.Errorf("certify --repo -h does not list --record-every-file")
	}
}

// A dry run writes no entry, so the flag has nothing to change there; it is
// accepted, not refused, and not an unknown flag.
func TestRecordEveryFileIsAcceptedOnADryRun(t *testing.T) {
	root, goals := recordFlagFixture(t, true)
	var o, e bytes.Buffer
	code := runCertifyRepo([]string{
		"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off",
		"--goals", goals, "--dry-run", "--record-every-file", "--ledger", filepath.Join(root, ".corral", "ledger"),
	}, &o, &e)
	if code != 0 {
		t.Fatalf("exit %d, want 0: stderr=%s", code, e.String())
	}
	if strings.Contains(o.String(), "  record:") {
		t.Errorf("a dry run printed a record: line:\n%s", o.String())
	}
	if _, err := os.Stat(filepath.Join(root, ".corral", "ledger")); err == nil {
		t.Errorf("a dry run wrote a ledger entry under %s", root)
	}
}

// The Action must say so when record-every-file is asked for on a run that
// records nothing, rather than ignoring it silently, and must pass the flag
// on a run that does.
func TestActionRecordEveryFileInputIsPassedOrSaid(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "action.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"args+=(--record-every-file)",
		"::notice::record-every-file is set, but this run records nothing",
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("action.yml lacks %q", want)
		}
	}
}

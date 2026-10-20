// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/reposcan"
)

// telemetryRepoFixture is a two-file Go module, committed, with a recorded
// mutant set that each file's own test kills — so a --repo run over it calls
// no model at all and still finishes two real audits. Go rather than the
// Python fixture ledger_test.go uses: the Go toolchain is present wherever
// this package's tests run, so these tests never skip.
func telemetryRepoFixture(t *testing.T) (root, setPath, goals string) {
	t.Helper()
	root = t.TempDir()
	mustWrite(t, filepath.Join(root, "go.mod"), "module example.com/tel\n\ngo 1.21\n")
	a := "package pkg\n\nfunc A() int { return 1 }\n"
	b := "package pkg\n\nfunc B() int { return 2 }\n"
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), a)
	mustWrite(t, filepath.Join(root, "pkg", "b.go"), b)
	mustWrite(t, filepath.Join(root, "pkg", "a_test.go"), "package pkg\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {\n\tif A() != 1 {\n\t\tt.Fatal(\"A\")\n\t}\n}\n")
	mustWrite(t, filepath.Join(root, "pkg", "b_test.go"), "package pkg\n\nimport \"testing\"\n\nfunc TestB(t *testing.T) {\n\tif B() != 2 {\n\t\tt.Fatal(\"B\")\n\t}\n}\n")
	gitRun := gitCmd(t, root)
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "fixture", "--no-gpg-sign")
	setPath = filepath.Join(t.TempDir(), "mutants.json")
	if err := adequacy.WriteMutantSet(setPath, adequacy.MutantSetFile{
		Format: adequacy.MutantSetFormat,
		Files: map[string]adequacy.MutantSetEntry{
			"pkg/a.go": {ParentSHA256: shaOf(a), Mutants: []adequacy.RecordedMutant{{ID: "m1", Replace: "package pkg\n\nfunc A() int { return 0 }\n"}}},
			"pkg/b.go": {ParentSHA256: shaOf(b), Mutants: []adequacy.RecordedMutant{{ID: "m1", Replace: "package pkg\n\nfunc B() int { return 0 }\n"}}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	goals = filepath.Join(t.TempDir(), "goals.json")
	mustWrite(t, goals, `{"pkg/a.go": "A must return 1", "pkg/b.go": "B must return 2"}`)
	return root, setPath, goals
}

// runTelemetryFixture runs certify --repo over the fixture with a signing key
// and a machine ledger, both in temp directories, and returns the exit code
// and both streams.
func runTelemetryFixture(t *testing.T, root, setPath, goals string, extra ...string) (int, string, string) {
	t.Helper()
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	args := append([]string{
		"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off",
		"--goals", goals, "--substrate", substrateWorkspace, "--all", "--mutants", setPath,
	}, extra...)
	var out, errb bytes.Buffer
	code := runCertifyRepo(args, &out, &errb)
	return code, out.String(), errb.String()
}

func withMachineLedger(t *testing.T) string {
	t.Helper()
	t.Setenv("CORRALAI_CERTIFY_KEY_FILE", filepath.Join(t.TempDir(), "certify.key"))
	t.Setenv("CORRALAI_CERTIFY_KEY", "")
	dir := filepath.Join(t.TempDir(), "machine-ledger")
	t.Setenv("CORRALAI_MACHINE_LEDGER", dir)
	return dir
}

func seatPaths(rows []auditpush.SeatRow) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range rows {
		if !seen[r.Path] {
			seen[r.Path] = true
			out = append(out, r.Path)
		}
	}
	sort.Strings(out)
	return out
}

// A two-file repo scan's entry carries seat rows for BOTH files, each
// stamped with the file whose run measured it — and the order is pinned: the
// rows are in the bundle before the statement is written, so the statement
// signs telemetrySha256 and `corral verify` passes on both the statement and
// the ledger. Rows added after the statement would leave it unsigned and fail
// every run's verify.
func TestRepoBundleCarriesSeatRows(t *testing.T) {
	root, setPath, goals := telemetryRepoFixture(t)
	withMachineLedger(t)
	attest := filepath.Join(t.TempDir(), "statement.json")
	code, out, errs := runTelemetryFixture(t, root, setPath, goals, "--attest", attest)
	if code != 0 {
		t.Fatalf("exit=%d\n%s%s", code, out, errs)
	}
	ledger := filepath.Join(root, ".corral", "ledger")
	entries, err := auditpush.ReadLedgerDir(ledger)
	if err != nil || len(entries) != 1 {
		t.Fatalf("want one entry, got %d (%v)", len(entries), err)
	}
	b := entries[0].Bundle
	if got, want := seatPaths(b.Seats), []string{"pkg/a.go", "pkg/b.go"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("seat rows cover %v, want %v: %+v", got, want, b.Seats)
	}
	for _, s := range b.Seats {
		if s.Repo != b.Scan.Repo || s.Commit != b.Scan.Commit || s.Commit == "" {
			t.Errorf("seat row subject %q@%q, want the scan's %q@%q", s.Repo, s.Commit, b.Scan.Repo, b.Scan.Commit)
		}
	}

	raw, err := os.ReadFile(attest) // #nosec G304 -- test temp path
	if err != nil {
		t.Fatal(err)
	}
	var stmt map[string]any
	if err := json.Unmarshal(raw, &stmt); err != nil {
		t.Fatal(err)
	}
	signed, _ := stmt["predicate"].(map[string]any)["telemetrySha256"].(string)
	if signed == "" {
		t.Fatal("the statement signs no telemetrySha256 although the entry carries telemetry rows — the rows were added after the statement was written")
	}
	if r := checkTelemetryDigest(signed, b); !r.checked || !r.ok {
		t.Errorf("the entry's telemetry does not match the statement: %+v", r)
	}
	var vout, verr bytes.Buffer
	if c := runVerifyAttest([]string{"--ledger", ledger}, &vout, &verr); c != 0 {
		t.Errorf("verify --ledger: exit %d\n%s%s", c, vout.String(), verr.String())
	}
	vout.Reset()
	verr.Reset()
	if c := runVerifyAttest([]string{"--attest", attest, "--db", ledger + "/"}, &vout, &verr); c != 0 || !strings.Contains(vout.String(), "hash to exactly the value the statement signs") {
		t.Errorf("verify --attest --db <ledger>: exit %d\n%s%s", c, vout.String(), verr.String())
	}
}

// Concurrent file audits each keep their OWN run's telemetry: the callback
// writes to the job's own result, never to a slice several goroutines share.
// Every audit here waits until all of them are in flight, so they genuinely
// overlap, and each reports a seat whose model names its file.
func TestRepoTelemetryPerFileUnderConcurrency(t *testing.T) {
	const n = 4
	var started sync.WaitGroup
	started.Add(n)
	ex := &localExecutor{
		baselineRuns: 2,
		newBaseline: func(context.Context, localAuditInput) (reposcan.BaselineRunner, func(), error) {
			return &scriptedBaseline{results: []bool{true, true}}, func() {}, nil
		},
		audit: func(_ context.Context, in localAuditInput) (advpool.Verdict, error) {
			started.Done()
			done := make(chan struct{})
			go func() { started.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Error("the audits never ran concurrently")
			}
			if in.telemetry == nil {
				t.Error("the audit input carries no telemetry callback")
				return advpool.Verdict{DevScored: true}, nil
			}
			in.telemetry(advpool.RunTelemetry{
				Seats:          []advpool.BugCatchObservation{{Model: "m-" + in.codePath, Role: advpool.RoleMutantGenerator}},
				MutantAttempts: []advpool.MutantAttempt{{Path: in.codePath, MutantID: "x", Model: "m-" + in.codePath}},
				CriticFindings: []advpool.CriticFindingObservation{{Model: "m-" + in.codePath}},
			})
			return advpool.Verdict{DevScored: true, DevKillRate: 1}, nil
		},
	}
	var jobs []reposcan.Job
	for i := 0; i < n; i++ {
		jobs = append(jobs, reposcan.Job{Path: "pkg/f" + string(rune('a'+i)) + ".go", Lang: "go", Goal: reposcan.Goal{Text: "g"}})
	}
	results := reposcan.Scan(context.Background(), jobs, ex, nil, n)
	for _, r := range results {
		if !r.Gradable {
			t.Fatalf("%s was not audited (%s %s) — the test proves nothing", r.Job.Path, r.Reason, r.Detail)
		}
		if len(r.Telemetry.Seats) != 1 || r.Telemetry.Seats[0].Model != "m-"+r.Job.Path {
			t.Errorf("%s carries another run's telemetry: %+v", r.Job.Path, r.Telemetry)
		}
	}
	seats, attempts, findings := scanTelemetryRows("o/r", "c1", results)
	if len(seats) != n || len(attempts) != n || len(findings) != n {
		t.Fatalf("want %d of each row, got %d/%d/%d", n, len(seats), len(attempts), len(findings))
	}
	for _, s := range seats {
		if s.Model != "m-"+s.Path || s.Repo != "o/r" || s.Commit != "c1" {
			t.Errorf("seat row %+v is stamped with another file or subject", s)
		}
	}
	for _, f := range findings {
		if f.Model != "m-"+f.Path {
			t.Errorf("critic finding %+v is stamped with another file", f)
		}
	}
}

// The scan also keeps a copy in the machine ledger, beside the repo's own
// entry; a machine ledger that cannot be written is printed and never
// changes the exit code; --no-ledger writes neither.
func TestRepoWritesMachineLedger(t *testing.T) {
	root, setPath, goals := telemetryRepoFixture(t)
	machine := withMachineLedger(t)
	code, out, errs := runTelemetryFixture(t, root, setPath, goals)
	if code != 0 {
		t.Fatalf("exit=%d\n%s%s", code, out, errs)
	}
	repoEntries, _ := auditpush.ReadLedgerDir(filepath.Join(root, ".corral", "ledger"))
	machineEntries, err := auditpush.ReadLedgerDir(machine)
	if len(repoEntries) != 1 || len(machineEntries) != 1 || err != nil {
		t.Fatalf("want one entry in each ledger, got repo=%d machine=%d (%v)\n%s%s", len(repoEntries), len(machineEntries), err, out, errs)
	}
	if machineEntries[0].Signature == "" {
		t.Error("the machine-ledger entry is unsigned although a key is configured")
	}
	if got, want := seatPaths(machineEntries[0].Bundle.Seats), seatPaths(repoEntries[0].Bundle.Seats); strings.Join(got, ",") != strings.Join(want, ",") || len(got) == 0 {
		t.Errorf("the machine copy's seats %v differ from the repo entry's %v", got, want)
	}
	if !strings.Contains(out, "machine ledger: entry written to "+machine) {
		t.Errorf("the report does not say where the machine copy went:\n%s", out)
	}
	var vout, verr bytes.Buffer
	if c := runVerifyAttest([]string{"--ledger", machine}, &vout, &verr); c != 0 {
		t.Errorf("verify --ledger <machine>: exit %d\n%s%s", c, vout.String(), verr.String())
	}

	// A machine ledger path that is a FILE cannot be a directory: printed, and
	// the verdict's exit code stands.
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	mustWrite(t, blocker, "x")
	t.Setenv("CORRALAI_MACHINE_LEDGER", blocker)
	code, out, errs = runTelemetryFixture(t, root, setPath, goals)
	if code != 0 {
		t.Fatalf("a machine-ledger failure changed the exit code to %d\n%s%s", code, out, errs)
	}
	if !strings.Contains(errs, "machine ledger: ") {
		t.Errorf("the machine-ledger failure was not printed:\n%s", errs)
	}

	// --no-ledger: neither ledger gains an entry.
	t.Setenv("CORRALAI_MACHINE_LEDGER", machine)
	if code, out, errs = runTelemetryFixture(t, root, setPath, goals, "--no-ledger"); code != 0 {
		t.Fatalf("exit=%d\n%s%s", code, out, errs)
	}
	if m, _ := auditpush.ReadLedgerDir(machine); len(m) != 1 {
		t.Errorf("--no-ledger wrote to the machine ledger: %d entries", len(m))
	}
}

// The executor hands the per-file audit the scan's subject exactly: the
// commit the scan resolved (HEAD, or "" when there is none), never an
// invented "local", and marked as already resolved so the audit does not
// re-derive it or refuse a typed repo paired with no commit.
func TestAuditInputCarriesTheScansCommitNotAnInventedOne(t *testing.T) {
	ex := &localExecutor{}
	for _, commit := range []string{"", "abc123"} {
		in := ex.auditInputFor(reposcan.Job{Path: "a.go", Repo: "acme/r", Commit: commit})
		if in.commit != commit || !in.subjectResolved {
			t.Errorf("commit %q: input carries commit=%q resolved=%v", commit, in.commit, in.subjectResolved)
		}
	}
	repo, commit, err := auditRunSubject(localAuditInput{repo: "acme/r", commit: "", subjectResolved: true})
	if err != nil || repo != "acme/r" || commit != "" {
		t.Errorf("a resolved subject with no commit: %q@%q, %v — want acme/r@\"\" and no refusal", repo, commit, err)
	}
}

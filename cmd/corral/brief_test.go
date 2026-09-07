// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/review"
)

// briefLedger builds a ledger with one scan (a survivor and a proven gap
// with its authored test, on pkg/a.go; an untouched file elsewhere), one
// review (a REPRODUCED finding on pkg/a.go, a refuted one, one with no
// outcome), and an adjudication confirming the reproduced finding.
// Returns the dir and the review hash.
func briefLedger(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	kr := 0.5
	// The local entry carries source (SourcePushed), as certify --repo
	// writes it: the hunk and the authored test are what the report hands
	// back, and a withheld copy has neither.
	if _, err := auditpush.PushBundle(dir+"/", auditpush.Bundle{
		SourcePushed: true,
		Scan:         auditpush.ScanRow{Repo: "o/r", ScanID: 1, Commit: "c1c1c1c1c1c1c1c1", Host: "h", CorralVersion: "v", Substrate: "workspace", SourcePushed: true},
		Files: []auditpush.Row{
			{Repo: "o/r", Commit: "c1c1c1c1c1c1c1c1", ScanID: 1, Path: "pkg/a.go", Disposition: "audited", KillRate: &kr, Survivors: 1, ProvenMissed: 1,
				AuthoredTest: "func TestAddSubtracts(t *testing.T) {\n\tif Add(1, 2) != 3 {\n\t\tt.Fatal()\n\t}\n}\n"},
			{Repo: "o/r", Commit: "c1c1c1c1c1c1c1c1", ScanID: 1, Path: "other/b.go", Disposition: "audited", KillRate: &kr},
		},
		Mutants: []auditpush.MutantRow{
			{Repo: "o/r", ScanID: 1, Path: "pkg/a.go", MutantID: "s0/m1", Outcome: "survived", Proven: true, SpanStart: 3, SpanEnd: 3, Shape: "return-changed", Code: adequacy.EncodeHunk("return a + b", "return a - b")},
			{Repo: "o/r", ScanID: 1, Path: "pkg/a.go", MutantID: "s0/m2", Outcome: "survived", SpanStart: 7, SpanEnd: 9, Shape: "condition-negated"},
			{Repo: "o/r", ScanID: 1, Path: "pkg/a.go", MutantID: "s0/m3", Outcome: "killed", KilledBy: "TestX", SpanStart: 12, SpanEnd: 12},
		},
	}); err != nil {
		t.Fatal(err)
	}
	name, err := auditpush.WriteReview(dir, review.Review{
		Repo: "o/r", Commit: "c1c1c1c1c1c1c1c1", Scope: "pkg", ReviewerModel: "reviewer-x",
		Findings: []review.Finding{
			{ID: "R1", Claim: "Add subtracts", Declared: review.TierReproduced, Tier: review.TierReproduced, File: "pkg/a.go", Line: 3, Severity: "high",
				Refutation: &review.Refutation{Model: "verifier-y", Verdict: review.VerdictStands, Argument: "it does"}},
			{ID: "R2", Claim: "a false claim", Declared: review.TierReproduced, Tier: review.TierCodeRead, File: "pkg/a.go", Line: 20, Demoted: "exit 1"},
			{ID: "R3", Claim: "a hunch", Declared: review.TierHypothesis, Tier: review.TierHypothesis, File: "pkg/a.go"},
		},
		Sound: []string{"go.mod"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	e, err := auditpush.ReadLedgerEntry(filepath.Join(dir, auditpush.ScansSubdir, name))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auditpush.WriteAdjudication(dir, e.Hash+"#R1", auditpush.VerdictConfirmed, "pdbethke", "real, reproduced", nil); err != nil {
		t.Fatal(err)
	}
	return dir, e.Hash
}

// The report says what is open on a path and what closes it: the
// survivor's span, the proven gap's test, the claim that stands with its
// ruling — and counts, never lists, the claims that did not hold.
func TestBriefRendersWhatIsOpenOnAPath(t *testing.T) {
	dir, hash := briefLedger(t)
	var out, errb bytes.Buffer
	if code := runBrief([]string{"--ledger", dir, "--scope", "pkg"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	s := out.String()
	for _, want := range []string{
		"brief — o/r: 1 path(s), from 3 entries",
		"\npkg/a.go\n",
		"kill rate 0.50, 1 survivor(s), 1 proven gap(s)",
		"gap, proven — line 3, return-changed",
		"- return a + b",
		"+ return a - b",
		"the test that closes it:",
		"func TestAddSubtracts",
		"survivor — lines 7-9, condition-negated",
		"claim " + hash[:12] + "#R1 — line 3, high (adjudication by pdbethke): Add subtracts",
		"declared REPRODUCED, recorded REPRODUCED; verifier verifier-y: STANDS",
		"confirmed by pdbethke: real, reproduced",
		"1 claim(s) did not hold, 1 with no outcome yet",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("brief lacks %q:\n%s", want, s)
		}
	}
	for _, never := range []string{"a false claim", "a hunch", "other/b.go", "s0/m3", "TestX"} {
		if strings.Contains(s, never) {
			t.Errorf("brief must not list %q:\n%s", never, s)
		}
	}
}

// --json is the same report as a document an agent can read back.
func TestBriefJSONRoundTrips(t *testing.T) {
	dir, hash := briefLedger(t)
	var out, errb bytes.Buffer
	if code := runBrief([]string{"--ledger", dir, "--scope", "pkg/a.go", "--json"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	var b Brief
	if err := json.Unmarshal(out.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	if len(b.Paths) != 1 || b.Paths[0].Path != "pkg/a.go" || len(b.Paths[0].Survivors) != 1 || len(b.Paths[0].ProvenGaps) != 1 || len(b.Paths[0].Findings) != 1 {
		t.Fatalf("shape: %+v", b.Paths)
	}
	f := b.Paths[0].Findings[0]
	if f.Review != hash || f.Adjudged != "confirmed" || f.Outcome != "adjudication by pdbethke" || b.Paths[0].DidNotHold != 1 || b.Paths[0].Undecided != 1 {
		t.Fatalf("finding: %+v; counts %d/%d", f, b.Paths[0].DidNotHold, b.Paths[0].Undecided)
	}
	if b.Paths[0].ProvenGaps[0].Search != "return a + b" || b.Paths[0].Scan.AuthoredTst == "" {
		t.Fatalf("the gap's hunk and the closing test travel: %+v", b.Paths[0])
	}
}

// A retracted entry is not the record: its rows leave the report, and the
// header says how many were left out.
func TestBriefSkipsRetractedEntries(t *testing.T) {
	dir, hash := briefLedger(t)
	if _, err := auditpush.WriteRetraction(dir, hash, "the review was wrong", nil); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := runBrief([]string{"--ledger", dir, "--scope", "pkg"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	s := out.String()
	if strings.Contains(s, "Add subtracts") || !strings.Contains(s, "retracted, left out") || !strings.Contains(s, "review: none on record") {
		t.Fatalf("a retracted review still reported:\n%s", s)
	}
}

// --changed takes the files from git; a path with nothing on record says so.
func TestBriefChangedSinceARef(t *testing.T) {
	dir, _ := briefLedger(t)
	root := t.TempDir()
	gitRun := gitCmd(t, root)
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "other", "b.go"), "package other\n")
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")
	gitRun("branch", "base")
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n// changed\n")
	mustWrite(t, filepath.Join(root, "pkg", "new.go"), "package pkg\n")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "change", "--no-gpg-sign")
	var out, errb bytes.Buffer
	if code := runBrief([]string{"--repo", root, "--ledger", dir, "--changed", "base"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	s := out.String()
	if !strings.Contains(s, "\npkg/a.go\n") || strings.Contains(s, "other/b.go") {
		t.Fatalf("--changed must name the changed files only:\n%s", s)
	}
	// pkg/new.go changed but nothing is on record for it: not a path in
	// the report (the report is the record), and the count says 1 path.
	if strings.Contains(s, "pkg/new.go") || !strings.Contains(s, "1 path(s)") {
		t.Fatalf("a changed file with no record is not invented:\n%s", s)
	}
	// A ref that looks like an option is refused by name.
	errb.Reset()
	if code := runBrief([]string{"--repo", root, "--ledger", dir, "--changed", "--output=x"}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "looks like a git option") {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
}

// No scope is a refusal, by name; a missing ledger is an empty record.
func TestBriefRefusesWithoutAScope(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runBrief([]string{"--ledger", t.TempDir()}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "--scope") {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	errb.Reset()
	empty := filepath.Join(t.TempDir(), "nowhere")
	if code := runBrief([]string{"--ledger", empty, "--scope", "pkg"}, &out, &errb); code != 0 || !strings.Contains(out.String(), "nothing on record for these paths") {
		t.Fatalf("exit %d: %s %s", code, out.String(), errb.String())
	}
}

// --max-items bounds the report and names where the rest are.
func TestBriefIsBoundedAndNamesTheCut(t *testing.T) {
	dir, _ := briefLedger(t)
	var out, errb bytes.Buffer
	if code := runBrief([]string{"--ledger", dir, "--scope", "pkg", "--max-items", "1"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	s := out.String()
	if !strings.Contains(s, "2 item(s) not listed (--max-items 1), from pkg/a.go on") || strings.Contains(s, "survivor — lines 7-9") {
		t.Fatalf("the cut must be named and honoured:\n%s", s)
	}
}

// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/reposcan"
	"github.com/pdbethke/corralai/internal/scanstore"
)

// TestWeakFileLineDisclosesTheWriterMode: the two modes cost differently and
// attempt differently, so a per-file line that reported a proven count without
// saying which shape earned it would leave two incomparable measurements
// looking identical.
func TestWeakFileLineDisclosesTheWriterMode(t *testing.T) {
	var b bytes.Buffer
	printWeakFile(&b, reposcan.WeakFile{
		Path: "pkg/a.py", KillRate: 0.5, Survivors: 24, ProvenMissed: 3,
		WriterMode: advpool.WriterModePerSurvivor, WriterCalls: 24,
	})
	if !strings.Contains(b.String(), "writer: per-survivor (24 calls)") {
		t.Errorf("per-survivor mode is not disclosed: %q", b.String())
	}

	b.Reset()
	printWeakFile(&b, reposcan.WeakFile{
		Path: "pkg/b.py", KillRate: 0.5, Survivors: 4, ProvenMissed: 1,
		WriterMode: advpool.WriterModeBatched, WriterCalls: 1,
	})
	if !strings.Contains(b.String(), "writer: batched (1 call)") {
		t.Errorf("batched mode is not disclosed: %q", b.String())
	}

	// A verdict earned before the mode existed, or by a caller that named
	// none: NOT RECORDED, and it must not be rendered as either mode.
	b.Reset()
	printWeakFile(&b, reposcan.WeakFile{Path: "pkg/c.py", KillRate: 0.5, Survivors: 2, ProvenMissed: 1})
	if strings.Contains(b.String(), "writer:") {
		t.Errorf("an unrecorded writer mode was rendered anyway: %q", b.String())
	}
}

// TestWriterModeFlagRejectsAnUnknownValue: exit 2 is the usage code, and a
// typo must never silently take the default — the mode changes what the run
// costs and what its verdict claims.
func TestWriterModeFlagRejectsAnUnknownValue(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.py"), []byte("def f():\n    return 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []struct {
		name string
		run  func(args []string, out, errw *bytes.Buffer) int
	}{
		{"--repo", func(args []string, out, errw *bytes.Buffer) int { return runCertifyRepo(args, out, errw) }},
	} {
		var out, errb bytes.Buffer
		code := cmd.run([]string{
			"--repo", root, "--writer-mode", "bogus",
			"--writer-model", testHerdWriter, "--mutant-model", testHerdMutant,
			"--critic-model", "off", "--dry-run",
		}, &out, &errb)
		if code != 2 {
			t.Errorf("%s --writer-mode bogus exited %d, want 2 (usage)", cmd.name, code)
		}
		if !strings.Contains(errb.String(), "writer-mode") {
			t.Errorf("%s: the error does not name the flag: %q", cmd.name, errb.String())
		}
	}
}

// TestWriterModeRoundTripsThroughTheLedger: scan_files.writer_mode is the
// column a later query needs to keep two modes' rows apart. Unset must land
// as SQL NULL, never as one of the two spellings.
func TestWriterModeRoundTripsThroughTheLedger(t *testing.T) {
	st, err := scanstore.Open(filepath.Join(t.TempDir(), "s.duckdb"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	id, err := st.Record(ctx, scanstore.Scan{Owner: "o", Repo: "r", Commit: "c"}, []scanstore.File{
		{Path: "a.py", Disposition: "audited", Evidence: "proven", WriterMode: advpool.WriterModePerSurvivor},
		{Path: "b.py", Disposition: "audited", Evidence: "proven"},
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	files, err := st.FilesForScan(ctx, id)
	if err != nil {
		t.Fatalf("FilesForScan: %v", err)
	}
	got := map[string]string{}
	for _, f := range files {
		got[f.Path] = f.WriterMode
	}
	if got["a.py"] != advpool.WriterModePerSurvivor {
		t.Errorf("a.py writer_mode = %q, want %q", got["a.py"], advpool.WriterModePerSurvivor)
	}
	if got["b.py"] != "" {
		t.Errorf("b.py writer_mode = %q, want empty (SQL NULL — the run named no mode)", got["b.py"])
	}
}

// TestAttestationCarriesTheWriterMode: the statement is the one artifact a
// third party verifies without trusting the run, so it must say which shape
// earned the proven count it signs.
func TestAttestationCarriesTheWriterMode(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "statement.json")
	rep := reposcan.RepoReport{
		Owner: "o", Repo: "r", Commit: "abc123", Audited: 1, Candidates: 1,
		Weakest: []reposcan.WeakFile{{
			Path: "a.py", KillRate: 0.5, Survivors: 2, ProvenMissed: 1,
			WriterMode: advpool.WriterModePerSurvivor, WriterCalls: 2,
		}},
	}
	if _, err := writeAuditStatement(out, dir, rep, map[string]string{"test-writer": "w"}, nil, nil, true, 0, auditpush.Bundle{}); err != nil {
		t.Fatalf("writeAuditStatement: %v", err)
	}
	raw, err := os.ReadFile(out) //nolint:gosec // test-owned temp path
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Predicate struct {
			Files []struct {
				Path       string `json:"path"`
				WriterMode string `json:"writerMode"`
			} `json:"files"`
		} `json:"predicate"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal statement: %v", err)
	}
	if len(doc.Predicate.Files) != 1 {
		t.Fatalf("statement carries %d file(s), want 1", len(doc.Predicate.Files))
	}
	if doc.Predicate.Files[0].WriterMode != advpool.WriterModePerSurvivor {
		t.Errorf("statement writerMode = %q, want %q", doc.Predicate.Files[0].WriterMode, advpool.WriterModePerSurvivor)
	}
}

// TestReportRendersEveryUnmergeableProvenTest is the honesty half of the
// per-survivor fan-out on a language whose parts will not merge. Each part is
// a test corral WROTE, COMPILED and RAN to kill a specific survivor —
// ProvenMissed counts them — so a report that prints only the merged file
// tells a developer that N gaps are provable and hands them fewer than N
// tests.
func TestReportRendersEveryUnmergeableProvenTest(t *testing.T) {
	var b bytes.Buffer
	printWeakFile(&b, reposcan.WeakFile{
		Path: "lib/a.rb", KillRate: 0.4, Survivors: 2, ProvenMissed: 2,
		WriterMode: advpool.WriterModePerSurvivor, WriterCalls: 2,
		AuthoredTest: "require 'minitest/autorun'\n\ndef test_one\n  assert true\nend\n",
		AuthoredExtra: []lang.AuthoredPart{{
			MutantID: "s0/m2",
			Source:   "require 'spec_helper'\n\nRSpec.describe Thing do\nend\n",
			Reason:   "lang: authored parts use different test frameworks",
		}},
	})
	out := b.String()
	if !strings.Contains(out, "def test_one") {
		t.Errorf("the merged authored test is missing:\n%s", out)
	}
	if !strings.Contains(out, "proven test for s0/m2 (separate file") {
		t.Errorf("the unmergeable proven test has no header naming its survivor:\n%s", out)
	}
	if !strings.Contains(out, "RSpec.describe Thing do") {
		t.Errorf("the unmergeable proven test's source was dropped — a proven gap with no test to act on:\n%s", out)
	}
	if !strings.Contains(out, "different test frameworks") {
		t.Errorf("the reason it could not be merged is not shown:\n%s", out)
	}
}

// TestAuthoredRecordJoinsTheUnmergeableParts: the ledger stores ONE authored
// artifact per file, so a run whose parts would not merge must still record
// every proven test — behind a separator that says plainly it is a record and
// not a file to run.
func TestAuthoredRecordJoinsTheUnmergeableParts(t *testing.T) {
	v := advpool.Verdict{
		Lang:         "ruby",
		AuthoredTest: "def test_one\n  assert true\nend\n",
		AuthoredExtra: []lang.AuthoredPart{
			{MutantID: "m2", Source: "it 'works' do\nend\n", Reason: "different frameworks"},
		},
	}
	rec := v.AuthoredRecord()
	if !strings.Contains(rec, "def test_one") || !strings.Contains(rec, "it 'works' do") {
		t.Fatalf("the record lost a proven test:\n%s", rec)
	}
	if !strings.Contains(rec, "# --- corral: separate test file (unmergeable) — m2") {
		t.Errorf("the record does not separate the parts with a comment naming the survivor:\n%s", rec)
	}

	// A file whose parts all merged records exactly the merged file — no
	// separator, nothing added.
	clean := advpool.Verdict{Lang: "ruby", AuthoredTest: "def test_one\nend\n"}
	if clean.AuthoredRecord() != clean.AuthoredTest {
		t.Errorf("a fully merged file was rewritten: %q", clean.AuthoredRecord())
	}
	// And a Go verdict uses Go's comment marker, since the column holds
	// source a reader may well paste somewhere.
	goV := advpool.Verdict{
		Lang: "go", AuthoredTest: "package p\n",
		AuthoredExtra: []lang.AuthoredPart{{MutantID: "m2", Source: "package p\n"}},
	}
	if !strings.Contains(goV.AuthoredRecord(), "// --- corral: separate test file (unmergeable) — m2") {
		t.Errorf("a Go record used a # comment:\n%s", goV.AuthoredRecord())
	}
}

// TestWriterLineDisclosesUngradedSeatsAndSuppressesACachedCount.
//
// Partial failure is the fan-out's common case and neither honesty marker
// covers it: WRITER FAILED means nothing compiled anywhere, TEST UNSOUND means
// nothing graded anywhere, so a file where 3 of 24 seats never graded carries
// neither and its proven count silently reads as a count over all 24.
//
// And on a CACHE HIT the count is another run's. It round-trips through
// verdict_json and comes back fully populated, exactly like Timing — which the
// line above this one suppresses for the same reason. The MODE survives (it is
// a property of the verdict, however it was served); the cost does not.
func TestWriterLineDisclosesUngradedSeatsAndSuppressesACachedCount(t *testing.T) {
	var b bytes.Buffer
	printWeakFile(&b, reposcan.WeakFile{
		Path: "pkg/a.py", KillRate: 0.5, Survivors: 24, ProvenMissed: 5,
		WriterMode: advpool.WriterModePerSurvivor, WriterCalls: 31, WriterSeatsUngraded: 3,
	})
	if !strings.Contains(b.String(), "writer: per-survivor (31 calls, 3 seats ungraded") {
		t.Errorf("the ungraded seats are not disclosed: %q", b.String())
	}

	b.Reset()
	printWeakFile(&b, reposcan.WeakFile{
		Path: "pkg/a.py", KillRate: 0.5, Survivors: 24, ProvenMissed: 5, CacheHit: true,
		WriterMode: advpool.WriterModePerSurvivor, WriterCalls: 31, WriterSeatsUngraded: 3,
	})
	out := b.String()
	if !strings.Contains(out, "writer: per-survivor") {
		t.Errorf("a cached verdict lost its mode, which is true however it was served: %q", out)
	}
	if strings.Contains(out, "31 calls") || strings.Contains(out, "seats ungraded") {
		t.Errorf("a cached verdict printed the EARNING run's cost as this scan's: %q", out)
	}
}

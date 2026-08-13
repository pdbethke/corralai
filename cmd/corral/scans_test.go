// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/scanstore"
)

type fakeScansReader struct {
	scans []scanstore.ScanRow
	files []scanstore.File
	limit int // records what the command asked for
}

func (f *fakeScansReader) Scans(_ context.Context, limit int) ([]scanstore.ScanRow, error) {
	f.limit = limit
	return f.scans, nil
}
func (f *fakeScansReader) FilesForScan(context.Context, int64) ([]scanstore.File, error) {
	return f.files, nil
}
func (f *fakeScansReader) Close() error { return nil }

func openFake(r *fakeScansReader) func(string) (scansReader, error) {
	return func(string) (scansReader, error) { return r, nil }
}

func ptrF(v float64) *float64 { return &v }

// TestScansShow_DistinguishesTheThreeWaysProvenCanBeZero is the reason this
// command exists at all. `proven_missed = 0` is ambiguous: it can mean the
// pool never authored a compiling test, that the test it authored never
// genuinely graded, or that a perfectly sound test ran and proved nothing.
// The ledger already stored flags telling those apart — but nothing could read
// them, so the distinction was written and then lost to whoever needed it.
func TestScansShow_DistinguishesTheThreeWaysProvenCanBeZero(t *testing.T) {
	r := &fakeScansReader{files: []scanstore.File{
		{Path: "writer.py", Disposition: "audited", Survivors: 4, ProvenMissed: 0, TestWriterFailed: true},
		{Path: "unsound.py", Disposition: "audited", Survivors: 5, ProvenMissed: 0, PoolTestUnsound: true},
		{Path: "missed.py", Disposition: "audited", Survivors: 10, ProvenMissed: 0},
		{Path: "proven.py", Disposition: "audited", Survivors: 3, ProvenMissed: 2, ProvenMutantIDs: "m1,m3"},
	}}
	var out, errOut bytes.Buffer
	if code := runScans([]string{"show", "7"}, openFake(r), &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut.String())
	}
	got := out.String()
	for _, want := range []string{
		"WRITER FAILED",
		"TEST UNSOUND",
		"tried and missed",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output must distinguish %q; it does not:\n%s", want, got)
		}
	}
	// A genuinely proven row must NOT be labelled with any of the caveats.
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "proven.py") && strings.Contains(line, "missed") {
			t.Errorf("a row with proven gaps must not carry a caveat: %q", line)
		}
	}
}

// TestScansShow_EvidencePrintsTheAuthoredTestEvenWhenItProvedNothing pins the
// case the evidence columns were added for: the tried-and-missed. If --evidence
// only printed tests that succeeded, the ledger would still be useless for the
// one question that actually needed it.
func TestScansShow_EvidencePrintsTheAuthoredTestEvenWhenItProvedNothing(t *testing.T) {
	const src = "def test_corral_authored():\n    assert thing() == 1\n"
	r := &fakeScansReader{files: []scanstore.File{
		{Path: "missed.py", Disposition: "audited", Survivors: 10, ProvenMissed: 0, AuthoredTest: src},
	}}
	var out, errOut bytes.Buffer
	if code := runScans([]string{"show", "7", "--evidence"}, openFake(r), &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut.String())
	}
	got := out.String()
	if !strings.Contains(got, src) {
		t.Fatalf("--evidence must print the authored test even when it proved nothing:\n%s", got)
	}
	if !strings.Contains(got, "killed: nothing") {
		t.Errorf("a tried-and-missed must say so explicitly, not print a bare empty list:\n%s", got)
	}
}

// TestScansList_NullKillRateIsNotZero pins the NaN/NULL discipline all the way
// out to the terminal: a scan that audited nothing must not render "0.00",
// which reads as "your tests caught nothing" about something never graded.
func TestScansList_NullKillRateIsNotZero(t *testing.T) {
	r := &fakeScansReader{scans: []scanstore.ScanRow{
		{ID: 2, Scan: scanstore.Scan{Repo: "never-graded", Audited: 0, KillRate: nil}},
		{ID: 1, Scan: scanstore.Scan{Repo: "graded", Audited: 1, KillRate: ptrF(0.48)}},
	}}
	var out, errOut bytes.Buffer
	if code := runScans([]string{"list"}, openFake(r), &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut.String())
	}
	got := out.String()
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "2") && strings.Contains(line, "0.00") {
			t.Errorf("a never-graded scan must not render 0.00: %q", line)
		}
	}
	if !strings.Contains(got, "0.48") {
		t.Errorf("a real kill rate must still render: %s", got)
	}
}

func TestScansUsageErrors(t *testing.T) {
	r := &fakeScansReader{}
	for _, c := range []struct {
		name string
		args []string
	}{
		{"no subcommand", nil},
		{"unknown subcommand", []string{"delete"}},
		{"show without an id", []string{"show"}},
		{"show with a non-numeric id", []string{"show", "abc"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if code := runScans(c.args, openFake(r), &out, &errOut); code != 2 {
				t.Fatalf("exit = %d, want 2 (usage); stderr=%s", code, errOut.String())
			}
		})
	}
}

// TestScansList_LimitReachesTheStore guards the flag actually being plumbed,
// not merely accepted — a --limit the store never sees is the same
// silently-discarded-input shape this codebase keeps producing.
func TestScansList_LimitReachesTheStore(t *testing.T) {
	r := &fakeScansReader{}
	var out, errOut bytes.Buffer
	if code := runScans([]string{"list", "--limit", "3"}, openFake(r), &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut.String())
	}
	if r.limit != 3 {
		t.Fatalf("store saw limit=%d, want 3", r.limit)
	}
}

// A scan can be 24 of 25 files reused from three weeks ago, and before this
// the reader showed only a kill rate: cache_hits and cache_hit were both read
// back from SQL and neither was ever printed. That is precisely the
// self-flattering record this tool exists to prevent — the number looks like a
// fresh measurement of today's code.
func TestScansListDisclosesCacheHits(t *testing.T) {
	r := &fakeScansReader{scans: []scanstore.ScanRow{
		{ID: 9, Scan: scanstore.Scan{Repo: "flask", Substrate: "workspace", Audited: 25, Candidates: 25, KillRate: ptrF(0.5), CacheHits: 24}},
	}}
	var out, errOut bytes.Buffer
	if code := runScans([]string{"list"}, openFake(r), &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut.String())
	}
	got := out.String()
	if !strings.Contains(got, "REUSED") {
		t.Errorf("list view has no reuse column:\n%s", got)
	}
	if !strings.Contains(got, "24") {
		t.Errorf("list view never printed the 24 reused verdicts:\n%s", got)
	}
}

func TestScansShowMarksAReusedFile(t *testing.T) {
	r := &fakeScansReader{files: []scanstore.File{
		{Path: "fresh.py", Disposition: "audited", Survivors: 0, KillRate: ptrF(1)},
		{Path: "reused.py", Disposition: "audited", Survivors: 0, KillRate: ptrF(1), CacheHit: true},
	}}
	var out, errOut bytes.Buffer
	if code := runScans([]string{"show", "9"}, openFake(r), &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut.String())
	}
	got := out.String()
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "reused.py") && !strings.Contains(line, "REUSED") {
			t.Errorf("a reused row does not say so:\n%s", got)
		}
		if strings.HasPrefix(line, "fresh.py") && strings.Contains(line, "REUSED") {
			t.Errorf("a freshly earned row was marked reused:\n%s", got)
		}
	}
	if !strings.Contains(got, "REUSED") {
		t.Errorf("no reuse marker at all:\n%s", got)
	}
}

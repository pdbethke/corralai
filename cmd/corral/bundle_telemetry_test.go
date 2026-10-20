// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/certify"
)

// The three parity tests below are the guard on a field-by-field converter.
// Negative control (run when written): deleting `Region: o.Region` from
// telemetryRows makes TestSeatRowCarriesEveryObservationField fail with
// `field Region: sent v-Region, stored `. A producer field with no
// same-named row field fails with "has no field X".

func TestSeatRowCarriesEveryObservationField(t *testing.T) {
	var zero advpool.BugCatchObservation
	for pass := 0; pass < boolPasses(&zero); pass++ {
		t.Run(fmt.Sprintf("pass=%d", pass), func(t *testing.T) {
			parityTestSeatRowCarriesEveryObservationField(t, pass)
		})
	}
}

func parityTestSeatRowCarriesEveryObservationField(t *testing.T, pass int) {
	var o advpool.BugCatchObservation
	fillNonZero(t, &o)
	alternateBools(&o, pass)
	seats, _, _ := telemetryRows("r", "c", "p.go", advpool.RunTelemetry{Seats: []advpool.BugCatchObservation{o}})
	if len(seats) != 1 {
		t.Fatalf("got %d seat rows, want 1", len(seats))
	}
	assertSameNamedFields(t, o, seats[0])
	if s := seats[0]; s.Repo != "r" || s.Commit != "c" || s.Path != "p.go" {
		t.Errorf("subject not stamped: %+v", s)
	}
}

func TestMutantAttemptRowCarriesEveryField(t *testing.T) {
	var zero advpool.MutantAttempt
	for pass := 0; pass < boolPasses(&zero); pass++ {
		t.Run(fmt.Sprintf("pass=%d", pass), func(t *testing.T) {
			parityTestMutantAttemptRowCarriesEveryField(t, pass)
		})
	}
}

func parityTestMutantAttemptRowCarriesEveryField(t *testing.T, pass int) {
	var a advpool.MutantAttempt
	fillNonZero(t, &a)
	alternateBools(&a, pass)
	_, rows, _ := telemetryRows("r", "c", "p.go", advpool.RunTelemetry{MutantAttempts: []advpool.MutantAttempt{a}})
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	assertSameNamedFields(t, a, rows[0])
	if rows[0].Repo != "r" || rows[0].Commit != "c" {
		t.Errorf("subject not stamped: %+v", rows[0])
	}
}

func TestCriticFindingRowCarriesEveryField(t *testing.T) {
	var zero advpool.CriticFindingObservation
	for pass := 0; pass < boolPasses(&zero); pass++ {
		t.Run(fmt.Sprintf("pass=%d", pass), func(t *testing.T) {
			parityTestCriticFindingRowCarriesEveryField(t, pass)
		})
	}
}

func parityTestCriticFindingRowCarriesEveryField(t *testing.T, pass int) {
	var f advpool.CriticFindingObservation
	fillNonZero(t, &f)
	alternateBools(&f, pass)
	_, _, rows := telemetryRows("r", "c", "p.go", advpool.RunTelemetry{CriticFindings: []advpool.CriticFindingObservation{f}})
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	assertSameNamedFields(t, f, rows[0])
	if rows[0].Repo != "r" || rows[0].Commit != "c" || rows[0].Path != "p.go" {
		t.Errorf("subject not stamped: %+v", rows[0])
	}
}

// A run that measured nothing adds nothing: nil, never an empty slice that
// would marshal as a claim of "measured none".
func TestTelemetryRowsEmptyIsNil(t *testing.T) {
	s, a, f := telemetryRows("r", "c", "p", advpool.RunTelemetry{})
	if s != nil || a != nil || f != nil {
		t.Errorf("want all nil, got %v %v %v", s, a, f)
	}
}

// The warehouse-rows hash must not cover rows the warehouse cannot hold.
func TestRowsHashIgnoresTelemetryRows(t *testing.T) {
	base := auditpush.Bundle{Files: []auditpush.Row{{Repo: "r", Path: "a.go"}}}
	with := base
	with.Seats = []auditpush.SeatRow{{Repo: "r", Model: "m"}}
	with.MutantAttempts = []auditpush.MutantAttemptRow{{MutantID: "x"}}
	with.CriticFindings = []auditpush.CriticFindingRow{{Evidence: "e"}}
	h1, err1 := warehouseRowsSHA256(base)
	h2, err2 := warehouseRowsSHA256(with)
	if err1 != nil || err2 != nil || h1 != h2 {
		t.Errorf("hash changed by telemetry rows: %v %v %s %s", err1, err2, h1, h2)
	}
}

func telBundle() auditpush.Bundle {
	return auditpush.Bundle{
		Files:          []auditpush.Row{{Repo: "r", Path: "a.go"}},
		Seats:          []auditpush.SeatRow{{Repo: "r", Model: "m", Catches: 2}},
		MutantAttempts: []auditpush.MutantAttemptRow{{Repo: "r", MutantID: "x", Outcome: "killed"}},
		CriticFindings: []auditpush.CriticFindingRow{{Repo: "r", Evidence: "func TestX() {}"}},
	}
}

func TestStatementCarriesTelemetryDigest(t *testing.T) {
	d, err := telemetrySHA256(telBundle())
	if err != nil || d == "" {
		t.Fatalf("digest %q err %v", d, err)
	}
	stmt := certify.BuildAuditAttestation(certify.AuditStatement{Repo: "r", Commit: "c", TelemetrySHA256: d})
	pred := stmt["predicate"].(map[string]any)
	if pred["telemetrySha256"] != d {
		t.Errorf("predicate telemetrySha256 = %v, want %s", pred["telemetrySha256"], d)
	}
	none := certify.BuildAuditAttestation(certify.AuditStatement{Repo: "r", Commit: "c"})
	if _, has := none["predicate"].(map[string]any)["telemetrySha256"]; has {
		t.Error("a statement with no telemetry must omit the key, not sign a digest of nothing")
	}
	if e, _ := telemetrySHA256(auditpush.Bundle{Files: telBundle().Files}); e != "" {
		t.Errorf("no telemetry must give \"\", got %s", e)
	}
}

func TestTelemetryDigestDetectsTamperingAndCustody(t *testing.T) {
	b := telBundle()
	signed, _ := telemetrySHA256(b)
	if r := checkTelemetryDigest(signed, b); !r.checked || !r.ok {
		t.Fatalf("untouched bundle: %+v", r)
	}
	tampered := telBundle()
	tampered.Seats[0].Catches = 99
	if r := checkTelemetryDigest(signed, tampered); !r.checked || r.ok {
		t.Errorf("tampered seat row must fail: %+v", r)
	}
	// Evidence is never in the digest, whatever the bundle's custody: the
	// statement is computed from the --push bundle (source withheld unless
	// --push-source) while the ledger entry carries source, and one digest
	// has to verify against both.
	other := telBundle()
	other.CriticFindings[0].Evidence = "different"
	if a, _ := telemetrySHA256(other); a != signed {
		t.Error("withheld Evidence leaked into the digest")
	}
	entry := telBundle()
	entry.SourcePushed, entry.Scan.SourcePushed = true, true
	if r := checkTelemetryDigest(signed, entry); !r.checked || !r.ok {
		t.Errorf("a statement signed over the push bundle must verify against the entry, which carries source: %+v", r)
	}
	if x, _ := telemetrySHA256(entry); x != signed {
		t.Error("the entry's custody bit changed the digest")
	}
	if entry.CriticFindings[0].Evidence == "" {
		t.Error("hashing blanked the caller's own Evidence")
	}
	// Rows added to a bundle the statement signed none for, and a bundle with none.
	if r := checkTelemetryDigest("", telBundle()); r.ok {
		t.Errorf("unsigned telemetry rows must fail: %+v", r)
	}
	if r := checkTelemetryDigest("", auditpush.Bundle{}); !r.checked || !r.ok {
		t.Errorf("no telemetry on either side must still verify: %+v", r)
	}
}

func TestVerifyTelemetryAgainstLedgerDirAndWarehouse(t *testing.T) {
	b := telBundle()
	b.Scan = auditpush.ScanRow{Repo: "r", Commit: "c", ScanID: 1, StatementSHA256: "stmtsha"}
	digest, _ := telemetrySHA256(b)
	stmt := map[string]any{"predicate": map[string]any{"telemetrySha256": digest}}

	dir := t.TempDir()
	if _, err := auditpush.PushBundle(dir+"/", b); err != nil {
		t.Fatal(err)
	}
	if r := verifyTelemetry(dir+"/", stmt, "stmtsha"); !r.checked || !r.ok {
		t.Errorf("ledger dir: %+v", r)
	}
	bad := map[string]any{"predicate": map[string]any{"telemetrySha256": "00"}}
	if r := verifyTelemetry(dir+"/", bad, "stmtsha"); !r.checked || r.ok {
		t.Errorf("wrong digest must fail on the path that holds the bundle: %+v", r)
	}
	// A warehouse holds no telemetry tables: said, not guessed.
	var out bytes.Buffer
	printCheckResult(&out, "telemetry", verifyTelemetry(filepath.Join(t.TempDir(), "w.duckdb"), stmt, "stmtsha"))
	if want := "· telemetry: not checked (the warehouse holds no telemetry tables yet)\n"; out.String() != want {
		t.Errorf("got %q want %q", out.String(), want)
	}
}

// The three slices survive a trip through the ledger directory: the only
// sink these rows reach this phase.
func TestLedgerDirRoundTripsTelemetryRows(t *testing.T) {
	for _, pushed := range []bool{true, false} {
		b := telBundle()
		b.Scan = auditpush.ScanRow{Repo: "r", Commit: "c", ScanID: 1}
		b.SourcePushed, b.Scan.SourcePushed = pushed, pushed
		dir := t.TempDir()
		if _, err := auditpush.PushBundle(dir+"/", b); err != nil {
			t.Fatal(err)
		}
		files, err := auditpush.ReadLedgerDir(dir)
		if err != nil || len(files) != 1 {
			t.Fatalf("read: %v, %d entries", err, len(files))
		}
		got := files[0].Bundle
		want := telBundle()
		if !pushed {
			// Evidence quotes code: withheld unless --push-source, and the
			// ONLY telemetry field that is.
			want.CriticFindings[0].Evidence = ""
		}
		if !reflect.DeepEqual(got.Seats, want.Seats) || !reflect.DeepEqual(got.MutantAttempts, want.MutantAttempts) || !reflect.DeepEqual(got.CriticFindings, want.CriticFindings) {
			t.Errorf("pushed=%v: telemetry did not round-trip:\n seats %+v / %+v\n attempts %+v / %+v\n findings %+v / %+v", pushed, got.Seats, want.Seats, got.MutantAttempts, want.MutantAttempts, got.CriticFindings, want.CriticFindings)
		}
	}
}

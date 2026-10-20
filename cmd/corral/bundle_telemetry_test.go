// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"testing"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/auditpush"
)

// The three parity tests below are the guard on a field-by-field converter.
// Negative control (run when written): deleting `Region: o.Region` from
// telemetryRows makes TestSeatRowCarriesEveryObservationField fail with
// `field Region: sent v-Region, stored `. A producer field with no
// same-named row field fails with "has no field X".

func TestSeatRowCarriesEveryObservationField(t *testing.T) {
	var o advpool.BugCatchObservation
	fillNonZero(t, &o)
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
	var a advpool.MutantAttempt
	fillNonZero(t, &a)
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
	var f advpool.CriticFindingObservation
	fillNonZero(t, &f)
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

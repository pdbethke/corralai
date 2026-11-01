// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/auditpush"
)

// fillEveryField sets every exported field of v to a distinct non-zero value,
// so a reader that forgets one copies a zero where the test planted a value.
// Slices and maps are left alone: no reader field in question is one.
func fillEveryField(v reflect.Value, n *int) {
	switch v.Kind() {
	case reflect.Struct:
		if v.Type() == reflect.TypeOf(time.Time{}) {
			v.Set(reflect.ValueOf(time.Date(2026, 10, 31, 12, 0, *n, 0, time.UTC)))
			*n++
			return
		}
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				fillEveryField(v.Field(i), n)
			}
		}
	case reflect.Ptr:
		p := reflect.New(v.Type().Elem())
		fillEveryField(p.Elem(), n)
		v.Set(p)
	case reflect.String:
		v.SetString("v" + string(rune('a'+*n%26)) + string(rune('a'+*n/26%26)))
		*n++
	case reflect.Int, reflect.Int32, reflect.Int64:
		v.SetInt(int64(*n + 1))
		*n++
	case reflect.Float64:
		v.SetFloat(float64(*n) + 0.5)
		*n++
	case reflect.Bool:
		v.SetBool(true)
	}
}

// assertSameNamedFieldsCopied fails on any field of got that also exists, with
// the same type, in want and differs: a reader that copies field by field
// fails open on whichever field nobody remembered to add, so the walk is over
// the types, not a list.
func assertSameNamedFieldsCopied(t *testing.T, what string, got, want reflect.Value) {
	t.Helper()
	assertSameNamedFieldsCopiedAtLeast(t, what, got, want, 20)
}

// assertSameNamedFieldsCopiedAtLeast is assertSameNamedFieldsCopied with the
// floor stated for a smaller row (a model call has a dozen fields, a scan
// eighty).
func assertSameNamedFieldsCopiedAtLeast(t *testing.T, what string, got, want reflect.Value, floor int) {
	t.Helper()
	copied := 0
	for i := 0; i < got.NumField(); i++ {
		name := got.Type().Field(i).Name
		w := want.FieldByName(name)
		if !w.IsValid() || w.Type() != got.Field(i).Type() {
			continue
		}
		copied++
		if !reflect.DeepEqual(got.Field(i).Interface(), w.Interface()) {
			t.Errorf("%s: field %s was dropped or changed by the reader: read %v, entry holds %v", what, name, got.Field(i).Interface(), w.Interface())
		}
	}
	if copied < floor {
		t.Errorf("%s: only %d fields compared, the walk found nothing to hold the reader to", what, copied)
	}
}

// assertEveryFieldHasAHome fails on any exported field of the ledger's row
// type src that the reader's type dst has no field for, unless exempt names
// it with the reason it is not shown. The same-named walk above cannot see a
// field the reader's type never declared: that is how a row's cost was
// dropped at the `corral scans` boundary while every shared field copied.
// An exemption for a field that dst now has, or src no longer has, fails too.
func assertEveryFieldHasAHome(t *testing.T, what string, src, dst reflect.Type, exempt map[string]string) {
	t.Helper()
	for i := 0; i < src.NumField(); i++ {
		f := src.Field(i)
		if !f.IsExported() {
			continue
		}
		_, has := dst.FieldByName(f.Name)
		why, ok := exempt[f.Name]
		switch {
		case !has && !ok:
			t.Errorf("%s: the ledger's %s.%s has no field on %s, so the reader drops it", what, src.Name(), f.Name, dst.Name())
		case has && ok:
			t.Errorf("%s: %s.%s is exempt (%s) but %s now has it: drop the exemption", what, src.Name(), f.Name, why, dst.Name())
		}
	}
	for name := range exempt {
		if _, ok := src.FieldByName(name); !ok {
			t.Errorf("%s: exemption for %s, which %s does not have", what, name, src.Name())
		}
	}
}

// `corral scans` reads an entry back through scanRowFromEntry and fileFromRow,
// which copy field by field. With every field populated, each one that both
// sides name must arrive unchanged, the new inventory, draw, cap and
// per-file count fields among them.
func TestTheScansReaderCopiesEveryFieldItShares(t *testing.T) {
	var n int
	var sc auditpush.ScanRow
	fillEveryField(reflect.ValueOf(&sc).Elem(), &n)
	var r auditpush.Row
	fillEveryField(reflect.ValueOf(&r).Elem(), &n)
	r.Disposition = "audited"

	e := auditpush.LedgerEntry{}
	e.Bundle.Scan = sc
	got := scanRowFromEntry(e, 1)
	assertSameNamedFieldsCopied(t, "scanRowFromEntry", reflect.ValueOf(got.Scan), reflect.ValueOf(sc))

	f := fileFromRow(r)
	assertSameNamedFieldsCopied(t, "fileFromRow", reflect.ValueOf(f), reflect.ValueOf(r))

	// And the fields this change added, named so a rename cannot hide them.
	if got.Inventory == "" || got.ShadowDraw == "" || got.MaxTokensReached == nil {
		t.Errorf("scan reader dropped Inventory/ShadowDraw/MaxTokensReached: %q %q %v", got.Inventory, got.ShadowDraw, got.MaxTokensReached)
	}
	if f.MutantsUnplaced == nil || f.MutantsUnmeasured == nil || f.MutantsDuplicate == nil || f.WriterAttempts == "" {
		t.Errorf("file reader dropped a per-file count: %+v", f)
	}
}

// The scans reader's two other field-by-field converters, the model-call and
// mutant rows: every field both sides name must arrive unchanged, and every
// field the ledger row holds must have somewhere to arrive, or say why not.
// The cost of a model call (CostMicroUSD beside its CostBasis) was dropped
// here while every shared field copied.
func TestTheScansReaderCopiesEveryModelCallAndMutantField(t *testing.T) {
	var n int
	var c auditpush.ModelCallRow
	fillEveryField(reflect.ValueOf(&c).Elem(), &n)
	var m auditpush.MutantRow
	fillEveryField(reflect.ValueOf(&m).Elem(), &n)
	const id = 1
	c.ScanID, m.ScanID = id, id
	e := auditpush.LedgerEntry{}
	e.Bundle.Calls = []auditpush.ModelCallRow{c}
	e.Bundle.Mutants = []auditpush.MutantRow{m}
	l := &ledgerScans{entries: []auditpush.LedgerEntry{e}}
	if !e.IsScan() {
		t.Fatal("fixture: the entry is not a scan, so the readers return nothing")
	}

	// Fields of the ledger row the scans reader does not carry, each with why.
	rowOnly := map[string]string{
		"Repo":            "the scan header carries the repo; every row of an entry shares it",
		"RunURL":          "the scan header's, shared by every row of an entry",
		"StatementSHA256": "the entry's signed statement, read by `ledger verify`, not by `scans`",
	}

	calls, err := l.ModelCallsForScan(context.Background(), id)
	if err != nil || len(calls) != 1 {
		t.Fatalf("ModelCallsForScan = %v, %v", calls, err)
	}
	assertSameNamedFieldsCopiedAtLeast(t, "ModelCallsForScan", reflect.ValueOf(calls[0]), reflect.ValueOf(c), 12)
	assertEveryFieldHasAHome(t, "ModelCallsForScan", reflect.TypeOf(c), reflect.TypeOf(calls[0]), rowOnly)

	mutants, err := l.MutantsForScan(context.Background(), id)
	if err != nil || len(mutants) != 1 {
		t.Fatalf("MutantsForScan = %v, %v", mutants, err)
	}
	assertSameNamedFieldsCopiedAtLeast(t, "MutantsForScan", reflect.ValueOf(mutants[0]), reflect.ValueOf(m), 12)
	mutantOnly := map[string]string{
		"Code": "the audited code itself, written only under --push-source; `scans` shows no source",
		// Pre-existing, from before this branch: `scans show` prints no
		// per-mutant reason yet, so a reader field would be read by nothing.
		// Reported for a follow-up rather than added unread.
		"InvalidReason": "`scans show` prints no per-mutant invalid reason yet (pre-existing; follow-up)",
	}
	for k, v := range rowOnly {
		mutantOnly[k] = v
	}
	assertEveryFieldHasAHome(t, "MutantsForScan", reflect.TypeOf(m), reflect.TypeOf(mutants[0]), mutantOnly)
}

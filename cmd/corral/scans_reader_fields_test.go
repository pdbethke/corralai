// SPDX-License-Identifier: Elastic-2.0

package main

import (
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
	if copied < 20 {
		t.Errorf("%s: only %d fields compared, the walk found nothing to hold the reader to", what, copied)
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

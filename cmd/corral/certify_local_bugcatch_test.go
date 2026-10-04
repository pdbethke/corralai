// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/bugcatch"
)

// fillNonZero sets every exported field of *p to a non-zero value of its
// kind, so a round trip that drops any field is visible.
func fillNonZero(t *testing.T, p any) {
	t.Helper()
	v := reflect.ValueOf(p).Elem()
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		switch f.Kind() {
		case reflect.String:
			f.SetString("v-" + v.Type().Field(i).Name)
		case reflect.Int, reflect.Int64:
			f.SetInt(int64(i + 1))
		case reflect.Bool:
			f.SetBool(true)
		default:
			t.Fatalf("fillNonZero: field %s has kind %s — teach this helper, do not skip it", v.Type().Field(i).Name, f.Kind())
		}
	}
}

func assertSameNamedFields(t *testing.T, src, dst any) {
	t.Helper()
	sv, dv := reflect.ValueOf(src), reflect.ValueOf(dst)
	for i := 0; i < sv.NumField(); i++ {
		name := sv.Type().Field(i).Name
		df := dv.FieldByName(name)
		if !df.IsValid() {
			t.Errorf("bugcatch.Observation has no field %s — the sink cannot carry it", name)
			continue
		}
		if !reflect.DeepEqual(sv.Field(i).Interface(), df.Interface()) {
			t.Errorf("field %s: sent %v, stored %v", name, sv.Field(i).Interface(), df.Interface())
		}
	}
}

// TestLocalBugCatchSinkCarriesEveryField: the sink converts field by field,
// and AGENTS.md names that shape as the one that fails open on the field
// nobody remembered. Every BugCatchObservation field must arrive on the row
// under the same name.
func TestLocalBugCatchSinkCarriesEveryField(t *testing.T) {
	var o advpool.BugCatchObservation
	fillNonZero(t, &o)
	store, err := bugcatch.Open(filepath.Join(t.TempDir(), "bc.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var n int64
	localBugCatchSink{store: store, missionID: 1, repo: "r", commit: "c", shadowRows: &n}.Record(7, "h", []advpool.BugCatchObservation{o})
	var got []bugcatch.Observation
	if err := store.EveryObservation(context.Background(), func(r bugcatch.Observation) error { got = append(got, r); return nil }); err != nil || len(got) != 1 {
		t.Fatalf("read back: %v, %d rows", err, len(got))
	}
	assertSameNamedFields(t, o, got[0])
}

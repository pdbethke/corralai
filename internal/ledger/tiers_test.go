// SPDX-License-Identifier: Elastic-2.0

package ledger_test

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/ledger"
)

// recordedFields walks the entry's types the way the record is built: struct
// fields, then pointer, slice, array and map-value elements, across packages.
// A struct with no exported fields (time.Time) is a leaf, not a node, so the
// walk never descends into a standard-library type's internals (R-pre1). It
// returns "Type.Field" for every exported leaf, and fails if two distinct
// types share a name, because tier rows key on the name alone (R-pre2).
func recordedFields(t *testing.T) map[string]bool {
	t.Helper()
	found := map[string]bool{}
	byName := map[string]reflect.Type{}
	seen := map[reflect.Type]bool{}
	hasExported := func(ty reflect.Type) bool {
		for i := 0; i < ty.NumField(); i++ {
			if ty.Field(i).IsExported() {
				return true
			}
		}
		return false
	}
	var walk func(reflect.Type)
	walk = func(ty reflect.Type) {
		for {
			switch ty.Kind() {
			case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
				ty = ty.Elem()
				continue
			}
			break
		}
		if ty.Kind() != reflect.Struct || !hasExported(ty) || seen[ty] {
			return
		}
		seen[ty] = true
		if prev, clash := byName[ty.Name()]; clash {
			t.Errorf("two distinct types are named %q (%s and %s); tier rows key on the type name, so one must be renamed",
				ty.Name(), prev.PkgPath(), ty.PkgPath())
		}
		byName[ty.Name()] = ty
		for i := 0; i < ty.NumField(); i++ {
			f := ty.Field(i)
			if !f.IsExported() {
				continue
			}
			ft := f.Type
			if et, ok := reflect.Zero(ft).Interface().(interface{ EncodedType() reflect.Type }); ok {
				// A JSON-text field records nothing itself; its tiers live on
				// the fields of the type it encodes.
				walk(et.EncodedType())
				continue
			}
			for ft.Kind() == reflect.Pointer || ft.Kind() == reflect.Slice || ft.Kind() == reflect.Array || ft.Kind() == reflect.Map {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct && hasExported(ft) {
				walk(ft)
				continue
			}
			found[ty.Name()+"."+f.Name] = true
		}
	}
	walk(reflect.TypeOf(ledger.Entry{}))
	if len(found) < 100 {
		t.Fatalf("the walk found only %d fields; it is not reaching the record", len(found))
	}
	return found
}

func TestEveryRecordedFieldHasATier(t *testing.T) {
	var missing []string
	for k := range recordedFields(t) {
		row, field, _ := strings.Cut(k, ".")
		if _, ok := ledger.TierOf(row, field); !ok {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("recorded fields with no tier; add a row for each to internal/ledger/tiers.go:\n  %s", strings.Join(missing, "\n  "))
	}
}

func TestTheTierTableHasNoStaleRows(t *testing.T) {
	found := recordedFields(t)
	var stale []string
	for _, ft := range ledger.FieldTiers() {
		if !found[ft.Row+"."+ft.Field] {
			stale = append(stale, ft.Row+"."+ft.Field)
		}
	}
	if len(stale) > 0 {
		t.Fatalf("tier rows naming no recorded field; delete them from internal/ledger/tiers.go:\n  %s", strings.Join(stale, "\n  "))
	}
}

func TestDeclaredAndMetaFieldsCarryANote(t *testing.T) {
	for _, ft := range ledger.FieldTiers() {
		if (ft.Tier == ledger.TierDeclared || ft.Tier == ledger.TierMeta) && ft.Note == "" {
			t.Errorf("%s.%s is %s but has no Note; views print it", ft.Row, ft.Field, ft.Tier)
		}
	}
}

func TestFieldTiersAreSortedAndUseKnownTiers(t *testing.T) {
	rows := ledger.FieldTiers()
	for i, ft := range rows {
		switch ft.Tier {
		case ledger.TierMeasured, ledger.TierDeclared, ledger.TierMeta, ledger.TierKey:
		default:
			t.Errorf("%s.%s has unknown tier %q", ft.Row, ft.Field, ft.Tier)
		}
		if i > 0 {
			p := rows[i-1]
			if p.Row > ft.Row || (p.Row == ft.Row && p.Field >= ft.Field) {
				t.Errorf("FieldTiers not sorted at %s.%s", ft.Row, ft.Field)
			}
		}
	}
}

func TestPinnedTiers(t *testing.T) {
	for _, c := range []struct {
		row, field string
		want       ledger.Tier
	}{
		{"Identity", "GeneratedWith", ledger.TierDeclared},
		{"Identity", "Author", ledger.TierDeclared},
		{"EventRow", "Detail", ledger.TierMeta},
		{"Row", "KillRate", ledger.TierMeasured},
		{"ScanRow", "EntryHash", ledger.TierKey},
		// The source of a test that executed: the content ran.
		{"Row", "AuthoredTest", ledger.TierMeasured},
	} {
		got, ok := ledger.TierOf(c.row, c.field)
		if !ok || got != c.want {
			t.Errorf("TierOf(%s,%s) = %q,%v; want %q", c.row, c.field, got, ok, c.want)
		}
	}
	if _, ok := ledger.TierOf("Row", "NoSuchField"); ok {
		t.Error("an unknown field must answer ok=false, never a default tier")
	}
}

func TestRecordedFieldsWalkIntoJSONText(t *testing.T) {
	got := recordedFields(t)
	for _, want := range []string{"ToolMutantCounts.Path", "ToolMutantCounts.DroppedBy", "ToolMutantCounts.Refused"} {
		if !got[want] {
			t.Errorf("recordedFields lacks %q: the gate does not walk inside JSON-text fields", want)
		}
	}
	if got["ScanRow.ToolMutants"] {
		t.Errorf("ScanRow.ToolMutants is a leaf; its tier should live on the encoded type's fields")
	}
}

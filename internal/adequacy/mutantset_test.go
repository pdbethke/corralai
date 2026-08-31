// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
)

func sha(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func TestMutantSetRoundTripsAndRefusesStaleSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mutants.json")
	set := MutantSetFile{Format: MutantSetFormat, Files: map[string]MutantSetEntry{
		"pkg/a.py": {ParentSHA256: sha("x = 1\n"), Mutants: []RecordedMutant{
			{ID: "m1", Search: "x = 1", Replace: "x = 2", Span: lang.LineRange{Start: 1, End: 1}},
		}},
	}}
	if err := WriteMutantSet(path, set); err != nil {
		t.Fatal(err)
	}
	got, err := ReadMutantSet(path)
	if err != nil {
		t.Fatal(err)
	}
	ms, err := got.MutantsFor("pkg/a.py", "x = 1\n")
	if err != nil || len(ms) != 1 || ms[0].ID != "m1" || ms[0].Span.Start != 1 || ms[0].ParentSHA256 != sha("x = 1\n") {
		t.Fatalf("replayed mutants = %+v, err=%v", ms, err)
	}
	code, aerr := ms[0].Apply("x = 1\n")
	if aerr != nil || code != "x = 2\n" {
		t.Fatalf("replayed mutant applies to %q (err=%v), want %q", code, aerr, "x = 2\n")
	}
	if _, err := got.MutantsFor("pkg/a.py", "x = 1 # changed\n"); err == nil {
		t.Error("a mutant set derived from other bytes must be refused, never re-applied")
	}
	if _, err := got.MutantsFor("pkg/b.py", "y\n"); err == nil {
		t.Error("a file absent from the set must be refused")
	}
}

// TestMutantSetV2RoundTrip pins the hunk-native document: what is written is
// what comes back, field for field. The set is the audit's exam; a lossy
// round-trip would replay a different one under the same name.
func TestMutantSetV2RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.json")
	set := MutantSetFile{Format: MutantSetFormat, Files: map[string]MutantSetEntry{
		"pkg/a.go": {ParentSHA256: sha("package p\n\nfunc F() int { return 1 }\n"), Mutants: []RecordedMutant{
			{ID: "m1", Search: "return 1", Replace: "return 2", Span: lang.LineRange{Start: 3, End: 3}},
			{ID: "m2", Search: "func F() int", Replace: "func F() int64", Span: lang.LineRange{Start: 3, End: 3}},
		}},
	}}
	if err := WriteMutantSet(path, set); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- test tempdir
	if err != nil {
		t.Fatal(err)
	}
	var probe struct {
		Format string `json:"format"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatal(err)
	}
	if probe.Format != "corral-mutants-2" {
		t.Fatalf("on-disk format = %q, want corral-mutants-2", probe.Format)
	}
	got, err := ReadMutantSet(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, set) {
		t.Fatalf("round trip changed the document:\n got %+v\nwant %+v", got, set)
	}
}

// TestMutantSetV1StillReplays is the promise to every set already recorded:
// a corral-mutants-1 document still reads, as WHOLE-FILE mutants whose Apply
// hands back exactly the bytes v1 stored. Nothing measured may move.
func TestMutantSetV1StillReplays(t *testing.T) {
	original := "def f():\n    return 1\n"
	mutated := "def f():\n    return 99\n"
	v1 := map[string]any{
		"format": "corral-mutants-1",
		"files": map[string]any{
			"a.py": map[string]any{
				"parent_sha256": sha(original),
				"mutants": []any{map[string]any{
					"id":   "m1",
					"code": mutated,
					"span": map[string]int{"start": 2, "end": 2},
				}},
			},
		},
	}
	raw, err := json.MarshalIndent(v1, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "v1.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	set, err := ReadMutantSet(path)
	if err != nil {
		t.Fatalf("a corral-mutants-1 document must still READ: %v", err)
	}
	ms, err := set.MutantsFor("a.py", original)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || !ms[0].IsWholeFile() {
		t.Fatalf("a v1 entry must convert to a whole-file mutant, got %+v", ms)
	}
	if ms[0].Span != (lang.LineRange{Start: 2, End: 2}) {
		t.Errorf("v1 span must survive: got %v", ms[0].Span)
	}
	code, aerr := ms[0].Apply(original)
	if aerr != nil || code != mutated {
		t.Fatalf("v1 replay applied to %q (err=%v), want the stored file %q", code, aerr, mutated)
	}
	// The parent-sha guard is unchanged for v1 documents.
	if _, err := set.MutantsFor("a.py", original+"# moved\n"); err == nil {
		t.Error("a v1 set must still refuse source that has changed")
	}
}

func TestReadMutantSetRefusesAnotherFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.json")
	if err := WriteMutantSet(path, MutantSetFile{Format: "corral-mutants-0", Files: map[string]MutantSetEntry{}}); err == nil {
		t.Error("writing an unknown format must be refused")
	}
	// v1 is READ-only: this build never writes one again.
	if err := WriteMutantSet(path, MutantSetFile{Format: "corral-mutants-1", Files: map[string]MutantSetEntry{}}); err == nil {
		t.Error("writing corral-mutants-1 must be refused — this build writes v2 only")
	}
	if err := os.WriteFile(path, []byte(`{"format":"corral-mutants-3","files":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadMutantSet(path); err == nil {
		t.Error("reading an unknown format must be refused")
	}
}

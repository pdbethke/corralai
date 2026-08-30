// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
)

func sha(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func TestMutantSetRoundTripsAndRefusesStaleSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mutants.json")
	set := MutantSetFile{Format: "corral-mutants-1", Files: map[string]MutantSetEntry{
		"pkg/a.py": {ParentSHA256: sha("x = 1\n"), Mutants: []RecordedMutant{{ID: "m1", Code: "x = 2\n", Span: lang.LineRange{Start: 1, End: 1}}}},
	}}
	if err := WriteMutantSet(path, set); err != nil {
		t.Fatal(err)
	}
	got, err := ReadMutantSet(path)
	if err != nil {
		t.Fatal(err)
	}
	ms, err := got.MutantsFor("pkg/a.py", "x = 1\n")
	if err != nil || len(ms) != 1 || ms[0].ID != "m1" || ms[0].Code != "x = 2\n" || ms[0].Span.Start != 1 || ms[0].ParentSHA256 != sha("x = 1\n") {
		t.Fatalf("replayed mutants = %+v, err=%v", ms, err)
	}
	if _, err := got.MutantsFor("pkg/a.py", "x = 1 # changed\n"); err == nil {
		t.Error("a mutant set derived from other bytes must be refused, never re-applied")
	}
	if _, err := got.MutantsFor("pkg/b.py", "y\n"); err == nil {
		t.Error("a file absent from the set must be refused")
	}
}

func TestReadMutantSetRefusesAnotherFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.json")
	if err := WriteMutantSet(path, MutantSetFile{Format: "corral-mutants-0", Files: map[string]MutantSetEntry{}}); err == nil {
		t.Error("writing an unknown format must be refused")
	}
}

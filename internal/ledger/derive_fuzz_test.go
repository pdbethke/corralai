// SPDX-License-Identifier: Elastic-2.0

package ledger_test

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/ledger"
)

// FuzzDerive hands Derive up to four ledger files of attacker-chosen bytes.
// It must not panic, and when it reports a Problem it must serve no rows at
// all: rows come only from one intact chain, and a half-trusted ledger that
// leaks rows is the failure the rule exists to prevent.
func FuzzDerive(f *testing.F) {
	hexPub, err := os.ReadFile(filepath.Join("testdata", "two-entries", "rootpub.hex"))
	if err != nil {
		f.Fatal(err)
	}
	pub, err := hex.DecodeString(strings.TrimSpace(string(hexPub)))
	if err != nil {
		f.Fatal(err)
	}
	files := twoEntryFiles(f)
	f.Add(files[0], files[1], []byte{}, []byte{}, uint8(2))
	f.Add(files[1], files[0], []byte{}, []byte{}, uint8(2))
	f.Add(files[0], gunzip(f, files[1]), []byte{}, []byte{}, uint8(2))
	f.Add(files[0], files[0][:len(files[0])/2], []byte("{}"), []byte{}, uint8(3))
	f.Add([]byte("{}"), []byte("[]"), []byte(""), []byte("x"), uint8(4))
	f.Fuzz(func(t *testing.T, a, b, c, d []byte, n uint8) {
		all := [][]byte{a, b, c, d}
		var raw []ledger.RawEntry
		for i := 0; i < int(n%4)+1; i++ {
			raw = append(raw, ledger.RawEntry{Name: "f" + string(rune('0'+i)), Bytes: all[i]})
		}
		got, err := ledger.Derive(raw, ledger.Trust{RootPub: pub, Repo: "o/r"})
		if err != nil {
			return
		}
		if got.Problem != "" && (len(got.Scans) > 0 || len(got.Files) > 0 || len(got.Mutants) > 0 ||
			len(got.Calls) > 0 || len(got.Adjudications) > 0 || got.Reviews > 0) {
			t.Fatalf("Problem %q set but rows were served: %+v", got.Problem, got)
		}
	})
}

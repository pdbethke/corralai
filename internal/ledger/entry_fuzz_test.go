// SPDX-License-Identifier: Elastic-2.0

package ledger_test

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/pdbethke/corralai/internal/ledger"
)

// twoEntryFiles returns the gzipped bytes of the two-entries fixture.
func twoEntryFiles(tb testing.TB) [][]byte {
	tb.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "two-entries", "scans", "*.json.gz"))
	if err != nil || len(paths) == 0 {
		tb.Fatalf("two-entries fixture missing: %v", err)
	}
	var out [][]byte
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			tb.Fatal(err)
		}
		out = append(out, b)
	}
	return out
}

func gunzip(tb testing.TB, b []byte) []byte {
	tb.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		tb.Fatal(err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		tb.Fatal(err)
	}
	return out
}

// FuzzParseEntry feeds ParseEntry the bytes a browser fetches from any
// GitHub repository, gzipped or not. Whatever they are it must not panic,
// and an entry it accepts must hash the same way every time it is asked:
// the hash is what the chain links on, so a nondeterministic one would let
// two readers disagree about one file.
func FuzzParseEntry(f *testing.F) {
	for _, gz := range twoEntryFiles(f) {
		plain := gunzip(f, gz)
		f.Add(gz)
		f.Add(plain)
		f.Add(gz[:len(gz)/2])
		f.Add(plain[:len(plain)/2])
		f.Add(plain[:len(plain)-1])
	}
	f.Add([]byte{0x1f, 0x8b})
	f.Add([]byte(`{"format":"corral-ledger-6"}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		e, err := ledger.ParseEntry("fuzz.json.gz", raw)
		if err != nil {
			return
		}
		h1, err1 := ledger.EntryHash(e)
		h2, err2 := ledger.EntryHash(e)
		if (err1 == nil) != (err2 == nil) || h1 != h2 {
			t.Fatalf("EntryHash is not deterministic: %q,%v then %q,%v", h1, err1, h2, err2)
		}
	})
}

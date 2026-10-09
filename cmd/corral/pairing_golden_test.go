// SPDX-License-Identifier: Elastic-2.0
package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/pairing"
)

// TestMightBeTestGolden pins the diff bound's generous "could this be a test?"
// over every corpus path, language or not. Generated from looksLikeATestPath
// before Round B1 replaced it; must stay byte-identical through B1.
func TestMightBeTestGolden(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "testdata", "pairing-corpus.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var b bytes.Buffer
	n := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		repo, path, ok := strings.Cut(sc.Text(), "\t")
		if !ok {
			continue
		}
		n++
		fmt.Fprintf(&b, "%s\t%s\t%v\n", repo, path, mightBeTest(path))
	}
	if n < 1000 {
		t.Fatalf("corpus has %d rows", n)
	}
	golden := filepath.Join("testdata", "might-be-test.golden.tsv")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(golden, b.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden) // #nosec G304 -- fixed testdata path
	if err != nil {
		t.Fatalf("golden missing (UPDATE_GOLDEN=1): %v", err)
	}
	if !bytes.Equal(b.Bytes(), want) {
		t.Fatal("might-be-test answers changed; diff testdata/might-be-test.golden.tsv")
	}
}

// mightBeTest is the one seam Task 4 re-points.
func mightBeTest(path string) bool { return pairing.MightBeTest(lang.AllTestRules(), path) }

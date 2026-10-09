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
// before Round B1 replaced it; must stay byte-identical through B1. It asks
// through mightBeTestPredicate, the exact function production calls.
func TestMightBeTestGolden(t *testing.T) {
	mightBeTest := mightBeTestPredicate()
	var b bytes.Buffer
	for _, r := range mightBeTestCorpus(t) {
		fmt.Fprintf(&b, "%s\t%s\t%v\n", r.repo, r.path, mightBeTest(r.path))
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

// TestIsTestImpliesMightBeTest is the subset relation the two questions are
// built on (pairing.MightBeTest's doc): over every real corpus path, a file
// some registered language calls a test must also be a possible test. If it
// ever failed, the diff bound would wave through a change to a file the audit
// itself treats as a test — the false green MightBeTest exists to prevent.
func TestIsTestImpliesMightBeTest(t *testing.T) {
	mightBeTest := mightBeTestPredicate()
	all := lang.AllTestRules()
	tests := 0
	for _, r := range mightBeTestCorpus(t) {
		for _, rules := range all {
			if !pairing.IsTest(rules, r.path) {
				continue
			}
			tests++
			if !mightBeTest(r.path) {
				t.Errorf("%s %s: IsTest under a registered language, but MightBeTest says no", r.repo, r.path)
			}
			break
		}
	}
	if tests == 0 {
		t.Fatal("no corpus path is a test under any language — the implication was never exercised")
	}
}

type mightBeTestRow struct{ repo, path string }

// mightBeTestCorpus reads the shared pairing corpus (every row, language or
// not), failing on a read error or a near-empty file so neither test above
// can pass vacuously.
func mightBeTestCorpus(t *testing.T) []mightBeTestRow {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", "testdata", "pairing-corpus.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var rows []mightBeTestRow
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		repo, path, ok := strings.Cut(sc.Text(), "\t")
		if ok {
			rows = append(rows, mightBeTestRow{repo, path})
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(rows) < 1000 {
		t.Fatalf("corpus has %d rows", len(rows))
	}
	return rows
}

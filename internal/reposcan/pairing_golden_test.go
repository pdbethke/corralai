// SPDX-License-Identifier: Elastic-2.0
package reposcan

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

// TestPairingGolden is Round B1's contract: for every real path in the corpus
// that a plugin detects, the candidates (path:rank, in order), the search roots
// and the is-test answer. It was generated from the code B1 replaces, BEFORE the
// replacement, so a byte-identical golden after B1 means the shared library
// pairs exactly as the six TestPaths did. A diff here is the refactor being
// wrong, never the golden being stale.
func TestPairingGolden(t *testing.T) {
	var b bytes.Buffer
	for _, r := range pairingCorpus(t) {
		p, ok := lang.Detect(r.path)
		if !ok {
			continue
		}
		var cands []string
		for _, c := range pairingCandidates(p, r.path) {
			cands = append(cands, fmt.Sprintf("%s:%d", filepath.ToSlash(c.Path), c.Rank))
		}
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%s\t%v\n", r.repo, r.path, p.Name(),
			strings.Join(cands, "|"), strings.Join(pairingRoots(p), ","), pairingIsTest(p, r.path))
	}
	golden := filepath.Join("testdata", "pairing.golden.tsv")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(golden, b.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden) // #nosec G304 -- fixed testdata path
	if err != nil {
		t.Fatalf("golden missing (generate deliberately with UPDATE_GOLDEN=1): %v", err)
	}
	if !bytes.Equal(b.Bytes(), want) {
		t.Fatalf("pairing changed — %s", firstGoldenDiff(want, b.Bytes()))
	}
}

// The three seams the golden reads through. Each is ONE call, and Task 3/4
// re-point exactly these lines at internal/pairing; the loop above never changes.
func pairingCandidates(p lang.Plugin, path string) []pairing.Candidate {
	return pairing.Candidates(p.TestRules(), path)
}
func pairingRoots(p lang.Plugin) []string           { return pairing.Roots(p.TestRules()) }
func pairingIsTest(p lang.Plugin, path string) bool { return pairing.IsTest(p.TestRules(), path) }

type corpusRow struct{ repo, path string }

func pairingCorpus(t *testing.T) []corpusRow {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", "testdata", "pairing-corpus.tsv"))
	if err != nil {
		t.Fatalf("corpus missing (scripts/gen-pairing-corpus.sh): %v", err)
	}
	defer f.Close()
	var rows []corpusRow
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		repo, path, ok := strings.Cut(sc.Text(), "\t")
		if ok {
			rows = append(rows, corpusRow{repo, path})
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(rows) < 1000 {
		t.Fatalf("corpus has %d rows — a near-empty corpus would make this golden pass vacuously", len(rows))
	}
	return rows
}

// firstGoldenDiff names the first differing line, so a failure says WHICH path
// paired differently instead of dumping two large files.
func firstGoldenDiff(want, got []byte) string {
	w, g := strings.Split(string(want), "\n"), strings.Split(string(got), "\n")
	for i := 0; i < len(w) || i < len(g); i++ {
		var wl, gl string
		if i < len(w) {
			wl = w[i]
		}
		if i < len(g) {
			gl = g[i]
		}
		if wl != gl {
			return fmt.Sprintf("line %d:\n  want %q\n  got  %q", i+1, wl, gl)
		}
	}
	return "same lines, different bytes"
}

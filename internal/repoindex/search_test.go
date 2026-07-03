// SPDX-License-Identifier: Elastic-2.0

package repoindex

import (
	"path/filepath"
	"testing"
)

func seedSearch(t *testing.T, withEmbed bool) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "rc.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if withEmbed {
		s.SetEmbedder(fakeEmbedServer(t))
	}
	// auth chunk: no lexical "login"; parse chunk: lexical "ParseURL"
	s.IndexFiles(1, []FileInput{
		{Path: "auth.go", Text: "func Authenticate(token string) bool { return verify(token) }\n"},
		{Path: "url.go", Text: "func ParseURL(s string) (string, error) { return s, nil }\n"},
	})
	return s
}

func TestSearchSemanticNoLexicalOverlap(t *testing.T) {
	s := seedSearch(t, true)
	// "login security" shares no token with auth.go, but maps to the same fake topic vector
	hits, err := s.Search(1, "login security", 5)
	if err != nil || len(hits) == 0 {
		t.Fatalf("hits=%v err=%v", hits, err)
	}
	if hits[0].Path != "auth.go" {
		t.Fatalf("semantic arm should rank auth.go first, got %s", hits[0].Path)
	}
	if hits[0].Via != "semantic" && hits[0].Via != "both" {
		t.Fatalf("expected semantic/both Via, got %s", hits[0].Via)
	}
}

func TestSearchExactToken(t *testing.T) {
	s := seedSearch(t, true)
	hits, err := s.Search(1, "ParseURL", 5)
	if err != nil || len(hits) == 0 {
		t.Fatalf("hits=%v err=%v", hits, err)
	}
	if hits[0].Path != "url.go" {
		t.Fatalf("keyword arm should rank url.go first, got %s", hits[0].Path)
	}
	// "ParseURL" matches url.go both lexically (BM25) and semantically (fake topic
	// vector dim1) → spec-mandated tag is keyword or, when both arms surface it, both.
	if hits[0].Via != "keyword" && hits[0].Via != "both" {
		t.Fatalf("via=%s", hits[0].Via)
	}
}

func TestSearchKeywordFloorNoEmbedder(t *testing.T) {
	s := seedSearch(t, false) // nil embedder
	hits, err := s.Search(1, "Authenticate", 5)
	if err != nil {
		t.Fatalf("nil embedder must not error: %v", err)
	}
	if len(hits) == 0 || hits[0].Path != "auth.go" {
		t.Fatalf("keyword floor should find auth.go, got %v", hits)
	}
	for _, h := range hits {
		if h.Via == "semantic" {
			t.Fatal("no semantic hits without an embedder")
		}
	}
}

// TestSearchHitCarriesLang verifies that after indexing a .go file, every Hit
// returned by Search has Lang="go". With tree-sitter active (Task 2+), symbol
// chunks will also carry non-empty Symbol/Kind for captured definitions.
func TestSearchHitCarriesLang(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "rc.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	if err := s.IndexFiles(42, []FileInput{
		{Path: "auth.go", Text: "func Authenticate(token string) bool { return verify(token) }\n"},
	}); err != nil {
		t.Fatal(err)
	}
	hits, err := s.Search(42, "Authenticate", 5)
	if err != nil {
		t.Fatalf("search error: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected at least one hit")
	}
	for _, h := range hits {
		if h.Lang != "go" {
			t.Errorf("hit Lang=%q want go", h.Lang)
		}
	}
}

// SPDX-License-Identifier: Elastic-2.0

package repoindex

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/pdbethke/corralai/internal/embed"
)

// fakeEmbedServer returns a deterministic 3-dim vector per input: a coarse "topic"
// one-hot — dim0 if the text mentions auth/login/security, dim1 if it mentions
// parse/url, else dim2. This lets a semantic query match a non-lexically-overlapping
// chunk in the search tests.
func fakeEmbedServer(t *testing.T) *embed.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Input []string `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		type emb struct {
			Embedding []float32 `json:"embedding"`
		}
		out := struct {
			Data []emb `json:"data"`
		}{}
		for _, s := range in.Input {
			ls := strings.ToLower(s)
			v := []float32{0, 0, 1}
			if strings.Contains(ls, "auth") || strings.Contains(ls, "login") || strings.Contains(ls, "security") {
				v = []float32{1, 0, 0}
			} else if strings.Contains(ls, "parse") || strings.Contains(ls, "url") {
				v = []float32{0, 1, 0}
			}
			out.Data = append(out.Data, emb{Embedding: v})
		}
		json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	return embed.NewFor(srv.URL, "fake", "")
}

// fakeCountingEmbedServer is the fakeEmbedServer body with a request counter
// threaded through the httptest handler, so callers can assert on the number
// of embed HTTP round-trips made by a single IndexFiles call (audit N+1 fix).
func fakeCountingEmbedServer(t *testing.T, calls *int32) *embed.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		var in struct {
			Input []string `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		type emb struct {
			Embedding []float32 `json:"embedding"`
		}
		out := struct {
			Data []emb `json:"data"`
		}{}
		for _, s := range in.Input {
			ls := strings.ToLower(s)
			v := []float32{0, 0, 1}
			if strings.Contains(ls, "auth") || strings.Contains(ls, "login") || strings.Contains(ls, "security") {
				v = []float32{1, 0, 0}
			} else if strings.Contains(ls, "parse") || strings.Contains(ls, "url") {
				v = []float32{0, 1, 0}
			}
			out.Data = append(out.Data, emb{Embedding: v})
		}
		json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	return embed.NewFor(srv.URL, "fake", "")
}

// TestIndexFilesBatchesEmbedCalls verifies the audit N+1 fix: IndexFiles must
// make exactly one embed HTTP call for a multi-file batch, not one per file.
func TestIndexFilesBatchesEmbedCalls(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "rc.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	var calls int32
	s.SetEmbedder(fakeCountingEmbedServer(t, &calls))

	files := []FileInput{
		{Path: "a.go", Text: "package a\nfunc A(){}"},
		{Path: "b.go", Text: "package b\nfunc B(){}"},
		{Path: "c.go", Text: "package c\nfunc C(){}"},
	}
	if err := s.IndexFiles(1, files); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("embed HTTP calls = %d for 3 files; want 1 (batched)", got)
	}
	if n := s.countRows(1); n == 0 {
		t.Fatal("expected chunks stored for all 3 files")
	}
}

func TestIndexAndIdempotentAndDrop(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "rc.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	s.SetEmbedder(fakeEmbedServer(t))

	if err := s.IndexFiles(7, []FileInput{{Path: "auth.go", Text: "func Authenticate() {}\n"}}); err != nil {
		t.Fatal(err)
	}
	if n := s.countRows(7); n == 0 { // test-only helper (add an unexported countRows in store.go)
		t.Fatal("expected rows for mission 7")
	}
	// re-index same path with identical content → replaces, does not duplicate
	before := s.countRows(7)
	if err := s.IndexFiles(7, []FileInput{{Path: "auth.go", Text: "func Authenticate() {}\n"}}); err != nil {
		t.Fatal(err)
	}
	if s.countRows(7) != before { // same content → same chunk count, no duplication
		t.Fatalf("idempotent upsert duplicated rows: %d → %d", before, s.countRows(7))
	}
	// a different mission is isolated
	if err := s.IndexFiles(8, []FileInput{{Path: "x.go", Text: "package x\n"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.DropMission(7); err != nil {
		t.Fatal(err)
	}
	if s.countRows(7) != 0 {
		t.Fatal("DropMission(7) left rows")
	}
	if s.countRows(8) == 0 {
		t.Fatal("DropMission(7) wrongly removed mission 8")
	}
}

// TestIndexPathsDeletesChunksForMissingFile verifies Fix 1: when IndexPaths is
// called with a path that no longer exists on disk, any previously-indexed chunks
// for that path are removed so search cannot return a stale path:line reference.
func TestIndexPathsDeletesChunksForMissingFile(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "rc.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	// Write and index a real file under a fake "repo root" directory.
	repoRoot := t.TempDir()
	filePath := filepath.Join(repoRoot, "auth.go")
	if err := os.WriteFile(filePath, []byte("func Authenticate() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.IndexPaths(9, repoRoot, []string{"auth.go"}); err != nil {
		t.Fatal(err)
	}
	if n := s.countRows(9); n == 0 {
		t.Fatal("expected chunks after initial index")
	}

	// Delete the file from disk, then re-run IndexPaths with the same path.
	// IndexPaths must drop the old chunks (deletion is a valid index update).
	if err := os.Remove(filePath); err != nil {
		t.Fatal(err)
	}
	if err := s.IndexPaths(9, repoRoot, []string{"auth.go"}); err != nil {
		t.Fatal(err)
	}
	if n := s.countRows(9); n != 0 {
		t.Fatalf("expected 0 chunks after deleted file re-indexed, got %d", n)
	}
}

// TestIndexPathsSkipsBinaryAndOversizeFiles verifies Fix 3a: binary content
// (NUL byte in first 8 KiB) and files over 512 KiB are not embedded, and any
// previously-indexed chunks for those paths are dropped.
func TestIndexPathsSkipsBinaryAndOversizeFiles(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "rc.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	repoRoot := t.TempDir()

	// Write both files with normal text first, index them so they have chunks.
	binaryPath := filepath.Join(repoRoot, "binary.bin")
	bigPath := filepath.Join(repoRoot, "big.go")
	if err := os.WriteFile(binaryPath, []byte("normal text\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bigPath, []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.IndexPaths(10, repoRoot, []string{"binary.bin", "big.go"}); err != nil {
		t.Fatal(err)
	}
	if n := s.countRows(10); n == 0 {
		t.Fatal("expected chunks from initial index of normal files")
	}

	// Now overwrite with binary content and oversized content.
	binaryContent := append([]byte("prefix"), 0, 0, 0) // NUL bytes
	binaryContent = append(binaryContent, []byte("suffix")...)
	if err := os.WriteFile(binaryPath, binaryContent, 0o644); err != nil {
		t.Fatal(err)
	}
	oversized := make([]byte, 513*1024) // > 512 KiB
	for i := range oversized {
		oversized[i] = 'x'
	}
	if err := os.WriteFile(bigPath, oversized, 0o644); err != nil {
		t.Fatal(err)
	}

	// Re-index: both files must be dropped (chunks deleted, nothing added).
	if err := s.IndexPaths(10, repoRoot, []string{"binary.bin", "big.go"}); err != nil {
		t.Fatal(err)
	}
	if n := s.countRows(10); n != 0 {
		t.Fatalf("expected 0 chunks after binary/oversized files re-indexed, got %d", n)
	}
}

// TestIndexNoEmbedder is the resiliency floor: with no embedder configured,
// IndexFiles must still chunk and store rows (NULL embeddings), not error.
func TestIndexNoEmbedder(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "rc.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	// deliberately NOT calling SetEmbedder

	if err := s.IndexFiles(3, []FileInput{{Path: "auth.go", Text: "func Authenticate() {}\n"}}); err != nil {
		t.Fatal(err)
	}
	if n := s.countRows(3); n == 0 {
		t.Fatal("expected chunks stored with NULL embedding when no embedder is set")
	}
}

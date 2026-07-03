// SPDX-License-Identifier: Elastic-2.0

package reference

import (
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/annindex"
	"github.com/pdbethke/corralai/internal/embed"
)

func openStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "ref.duckdb"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// Three sources on an orthonormal basis make cosine ranking deterministic.
func seed(t *testing.T, s *Store) {
	t.Helper()
	s.Replace("x-doc", "text", []Chunk{{Seq: 0, Text: "about x", Embedding: []float32{1, 0, 0}}})
	s.Replace("y-doc", "text", []Chunk{{Seq: 0, Text: "about y", Embedding: []float32{0, 1, 0}}})
	s.Replace("z-doc", "url", []Chunk{{Seq: 0, Text: "about z", Embedding: []float32{0, 0, 1}}})
}

func TestSearchRanksByCosine(t *testing.T) {
	s := openStore(t)
	seed(t, s)
	hits, err := s.Search([]float32{0.9, 0.1, 0}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 3 {
		t.Fatalf("want 3 hits, got %d", len(hits))
	}
	if hits[0].Source != "x-doc" {
		t.Fatalf("nearest to [0.9,0.1,0] should be x-doc, got %q (%.3f)", hits[0].Source, hits[0].Score)
	}
	if hits[0].Score <= hits[1].Score {
		t.Fatalf("scores should be descending: %.3f then %.3f", hits[0].Score, hits[1].Score)
	}
	if hits[2].Source != "z-doc" { // orthogonal to the query → last
		t.Fatalf("furthest should be z-doc, got %q", hits[2].Source)
	}
}

func TestReplaceIsIdempotent(t *testing.T) {
	s := openStore(t)
	s.Replace("doc", "text", []Chunk{
		{Seq: 0, Text: "v1 a", Embedding: []float32{1, 0}},
		{Seq: 1, Text: "v1 b", Embedding: []float32{0, 1}},
	})
	// Re-ingest the same source with fewer chunks → old ones are gone.
	s.Replace("doc", "text", []Chunk{{Seq: 0, Text: "v2", Embedding: []float32{1, 1}}})
	srcs, _ := s.Sources()
	if len(srcs) != 1 || srcs[0].Chunks != 1 {
		t.Fatalf("re-ingest should replace, got %+v", srcs)
	}
}

func TestSourcesAndRemove(t *testing.T) {
	s := openStore(t)
	seed(t, s)
	srcs, _ := s.Sources()
	if len(srcs) != 3 {
		t.Fatalf("want 3 sources, got %d", len(srcs))
	}
	n, _ := s.Remove("y-doc")
	if n != 1 {
		t.Fatalf("remove should drop 1 chunk, got %d", n)
	}
	if srcs, _ := s.Sources(); len(srcs) != 2 {
		t.Fatalf("after remove want 2 sources, got %d", len(srcs))
	}
}

func TestVettedTiering(t *testing.T) {
	s := openStore(t)

	// Ingest a chunk — vetted must default to false.
	if err := s.Replace("vetted-doc", "text", []Chunk{{Seq: 0, Text: "content", Embedding: []float32{1, 0, 0}}}); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	hits, err := s.Search([]float32{1, 0, 0}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("no hits after Replace")
	}
	if hits[0].Vetted {
		t.Fatalf("want Vetted==false by default after ingest, got true")
	}

	// After SetVetted the same source's hits should carry Vetted==true.
	if err := s.SetVetted("vetted-doc"); err != nil {
		t.Fatalf("SetVetted: %v", err)
	}
	hits2, err := s.Search([]float32{1, 0, 0}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits2) == 0 {
		t.Fatal("no hits after SetVetted")
	}
	if !hits2[0].Vetted {
		t.Fatalf("want Vetted==true after SetVetted, got false")
	}

	// Re-ingesting the same source resets vetted back to false (Replace clears +
	// re-inserts, and new rows are unvetted).
	if err := s.Replace("vetted-doc", "text", []Chunk{{Seq: 0, Text: "updated content", Embedding: []float32{1, 0, 0}}}); err != nil {
		t.Fatalf("Replace after SetVetted: %v", err)
	}
	hits3, err := s.Search([]float32{1, 0, 0}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits3) == 0 {
		t.Fatal("no hits after second Replace")
	}
	if hits3[0].Vetted {
		t.Fatalf("want Vetted==false after re-ingest, got true")
	}
}

// TestSetVetted_NotFound asserts that SetVetted returns a non-nil error
// containing "no reference source" when the source does not exist.
func TestSetVetted_NotFound(t *testing.T) {
	s := openStore(t)
	err := s.SetVetted("nonexistent")
	if err == nil {
		t.Fatal("expected non-nil error for nonexistent source, got nil")
	}
	if !strings.Contains(err.Error(), "no reference source") {
		t.Errorf("error should contain 'no reference source', got: %v", err)
	}
}

// TestSetVetted_Exists asserts that SetVetted returns nil and flips vetted=true
// when chunks for the source are present.
func TestSetVetted_Exists(t *testing.T) {
	s := openStore(t)
	if err := s.Replace("my-doc", "text", []Chunk{{Seq: 0, Text: "content", Embedding: []float32{1, 0, 0}}}); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if err := s.SetVetted("my-doc"); err != nil {
		t.Fatalf("SetVetted: unexpected error: %v", err)
	}
	hits, err := s.Search([]float32{1, 0, 0}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("no hits after SetVetted")
	}
	if !hits[0].Vetted {
		t.Fatalf("want Vetted==true after SetVetted, got false")
	}
}

// normalize returns a unit-length copy of v.
func normalize(v []float32) []float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	mag := math.Sqrt(sum)
	if mag == 0 {
		return v
	}
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = float32(float64(x) / mag)
	}
	return out
}

// hitKey is a stable identity key for a search Hit for set comparisons.
func hitKey(h Hit) string { return h.Source + "\x00" + h.Text }

// annSeedChunks builds n three-dimensional normalized-vector chunks spread
// across the unit sphere so cosine ranking is non-trivial.
func annSeedChunks(n int) []Chunk {
	chunks := make([]Chunk, n)
	for i := range chunks {
		fi := float32(i + 1)
		fn := float32(n + 1)
		v := normalize([]float32{fi / fn, (fn - fi) / fn, float32(math.Sin(float64(fi)))})
		chunks[i] = Chunk{Seq: i, Text: fmt.Sprintf("chunk-%d", i), Embedding: v}
	}
	return chunks
}

// bruteForceTopK runs the reference brute-force query directly on the DB and
// returns the top-k Source\x00Text key set. ORDER BY array_cosine_distance ASC
// (== cosine similarity DESC) using the migrated FLOAT[3] column.
func bruteForceTopK(t *testing.T, s *Store, qv []float32, k int) map[string]bool {
	t.Helper()
	lit := embed.VecLiteral(qv)
	rows, err := s.db.Query(
		`SELECT source, text FROM chunks
		 ORDER BY array_cosine_distance(embedding, `+lit+`::FLOAT[3]) ASC
		 LIMIT ?`, k)
	if err != nil {
		t.Fatalf("brute-force reference query: %v", err)
	}
	defer rows.Close()
	keys := map[string]bool{}
	for rows.Next() {
		var src, txt string
		if err := rows.Scan(&src, &txt); err != nil {
			t.Fatalf("brute-force scan: %v", err)
		}
		keys[src+"\x00"+txt] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("brute-force rows: %v", err)
	}
	return keys
}

// TestReferenceANNAgreesWithBruteforce seeds > injected-low-threshold chunks with
// consistent dim, triggers HNSW via Replace, then asserts that Search (HNSW path)
// returns the SAME top-k Source/Text set as a direct brute-force query over the
// same rows. Also verifies the ref_hnsw index is present in duckdb_indexes.
func TestReferenceANNAgreesWithBruteforce(t *testing.T) {
	s := openStore(t)
	// Inject a low threshold so the test DB (60 rows) triggers HNSW.
	s.annCfg = annindex.Config{Threshold: 50}

	// Seed 60 three-dimensional unit-vector chunks from one source. Vectors are
	// spread across the unit sphere so cosine ranking is non-trivial.
	const N = 60
	chunks := make([]Chunk, N)
	for i := range chunks {
		fi := float32(i + 1)
		fn := float32(N + 1)
		v := normalize([]float32{fi / fn, (fn - fi) / fn, float32(math.Sin(float64(fi)))})
		chunks[i] = Chunk{Seq: i, Text: fmt.Sprintf("chunk-%d", i), Embedding: v}
	}
	if err := s.Replace("big-doc", "text", chunks); err != nil {
		t.Fatalf("Replace: %v", err)
	}

	// After Replace the HNSW index should be active (N > threshold=50).
	if !s.annActive {
		t.Fatal("expected HNSW to be active above threshold; vss may not be available in this environment — skip if CORRALAI_ANN_DISABLE=1")
	}
	if s.annDim != 3 {
		t.Fatalf("expected annDim=3, got %d", s.annDim)
	}

	// Verify the index is visible in duckdb_indexes.
	var idxCnt int
	if err := s.db.QueryRow(
		`SELECT count(*) FROM duckdb_indexes WHERE index_name = 'ref_hnsw' AND table_name = 'chunks'`,
	).Scan(&idxCnt); err != nil {
		t.Fatalf("index check: %v", err)
	}
	if idxCnt == 0 {
		t.Fatal("HNSW index ref_hnsw not found in duckdb_indexes after Replace")
	}

	// ANN search (goes through HNSW + exact re-rank).
	qv := normalize([]float32{0.6, 0.7, 0.3})
	k := 5
	annHits, err := s.Search(qv, k)
	if err != nil {
		t.Fatalf("Search (HNSW): %v", err)
	}
	if len(annHits) != k {
		t.Fatalf("HNSW Search: want %d hits, got %d", k, len(annHits))
	}

	// Reference brute-force query: ORDER BY array_cosine_distance ASC (= cosine
	// similarity DESC) using the fixed-array cast, consistent with the migrated
	// FLOAT[3] column.
	lit := embed.VecLiteral(qv)
	bfRows, err := s.db.Query(
		`SELECT source, text FROM chunks
		 ORDER BY array_cosine_distance(embedding, `+lit+`::FLOAT[3]) ASC
		 LIMIT ?`, k)
	if err != nil {
		t.Fatalf("brute-force reference query: %v", err)
	}
	defer bfRows.Close()
	bfKeys := map[string]bool{}
	for bfRows.Next() {
		var src, txt string
		if err := bfRows.Scan(&src, &txt); err != nil {
			t.Fatalf("brute-force scan: %v", err)
		}
		bfKeys[src+"\x00"+txt] = true
	}
	if err := bfRows.Err(); err != nil {
		t.Fatalf("brute-force rows: %v", err)
	}
	if len(bfKeys) != k {
		t.Fatalf("brute-force reference: want %d rows, got %d", k, len(bfKeys))
	}

	// ANN result set.
	annKeys := map[string]bool{}
	for _, h := range annHits {
		annKeys[hitKey(h)] = true
	}

	// The HNSW + exact re-rank must produce the same top-k set as brute-force.
	var missing []string
	for key := range bfKeys {
		if !annKeys[key] {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("ANN top-k differs from brute-force; missing keys: %v", missing)
	}
}

// TestReferenceBelowThresholdBruteforce seeds fewer chunks than the injected
// threshold, verifies that annActive stays false (no HNSW index built), and
// confirms Search still returns correct results via the brute-force path.
func TestReferenceBelowThresholdBruteforce(t *testing.T) {
	s := openStore(t)
	// Inject a very high threshold so the test DB never triggers HNSW.
	s.annCfg = annindex.Config{Threshold: 10000}

	// Seed 3 chunks — well below the threshold.
	seed(t, s)

	// HNSW must NOT be active.
	if s.annActive {
		t.Fatal("expected HNSW inactive below threshold")
	}

	// Search must still work and return correct rankings via brute-force.
	hits, err := s.Search([]float32{0.9, 0.1, 0}, 3)
	if err != nil {
		t.Fatalf("brute-force Search: %v", err)
	}
	if len(hits) != 3 {
		t.Fatalf("want 3 hits, got %d", len(hits))
	}
	if hits[0].Source != "x-doc" {
		t.Fatalf("nearest to [0.9,0.1,0] should be x-doc, got %q (score %.3f)", hits[0].Source, hits[0].Score)
	}
}

// TestReferenceOpenActivatesExistingIndex proves the Open()-time ensureANN call:
// seed an above-threshold store (building the persisted HNSW index), CLOSE it,
// then RE-OPEN the same file-backed DB. Search must use the HNSW path immediately
// (annActive true) WITHOUT any intervening Replace/Remove, and still return the
// same top-k as brute-force. Threshold is driven through the real env path so the
// reopened Open()'s ConfigFromEnv() picks it up.
func TestReferenceOpenActivatesExistingIndex(t *testing.T) {
	t.Setenv("CORRALAI_ANN_THRESHOLD", "50")

	// File-backed path so the persisted data + index survive Close/Open.
	path := filepath.Join(t.TempDir(), "reopen.duckdb")

	const N = 60
	qv := normalize([]float32{0.6, 0.7, 0.3})
	k := 5

	// Phase 1: create, seed above threshold, verify index built, then close.
	{
		s, err := Open(path)
		if err != nil {
			t.Fatalf("open (1): %v", err)
		}
		if err := s.Replace("big-doc", "text", annSeedChunks(N)); err != nil {
			s.Close()
			t.Fatalf("Replace: %v", err)
		}
		if !s.annActive {
			s.Close()
			t.Fatal("expected HNSW active after seeding above threshold; vss may be unavailable in this environment")
		}
		if err := s.Close(); err != nil {
			t.Fatalf("close (1): %v", err)
		}
	}

	// Phase 2: reopen the SAME file. Open() must activate HNSW with no write.
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open (2): %v", err)
	}
	t.Cleanup(func() { s.Close() })

	if !s.annActive {
		t.Fatal("reopened above-threshold store: Open() must activate HNSW (annActive) without an intervening write")
	}
	if s.annDim != 3 {
		t.Fatalf("reopened store: expected annDim=3, got %d", s.annDim)
	}

	// The persisted index must be visible.
	var idxCnt int
	if err := s.db.QueryRow(
		`SELECT count(*) FROM duckdb_indexes WHERE index_name = 'ref_hnsw' AND table_name = 'chunks'`,
	).Scan(&idxCnt); err != nil {
		t.Fatalf("index check: %v", err)
	}
	if idxCnt == 0 {
		t.Fatal("reopened store: ref_hnsw index not found in duckdb_indexes")
	}

	// Search (HNSW path) must agree with brute-force on the reopened store.
	annHits, err := s.Search(qv, k)
	if err != nil {
		t.Fatalf("Search (reopened HNSW): %v", err)
	}
	if len(annHits) != k {
		t.Fatalf("reopened HNSW Search: want %d hits, got %d", k, len(annHits))
	}
	bfKeys := bruteForceTopK(t, s, qv, k)
	if len(bfKeys) != k {
		t.Fatalf("brute-force reference: want %d rows, got %d", k, len(bfKeys))
	}
	annKeys := map[string]bool{}
	for _, h := range annHits {
		annKeys[hitKey(h)] = true
	}
	var missing []string
	for key := range bfKeys {
		if !annKeys[key] {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("reopened ANN top-k differs from brute-force; missing keys: %v", missing)
	}
}

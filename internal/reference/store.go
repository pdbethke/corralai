// SPDX-License-Identifier: Elastic-2.0

// Package reference is corralai's bring-your-own reference corpus: external
// material (URLs, docs, PDFs, design references) chunked, embedded, and stored so
// the swarm — especially the researcher — can semantically search it for
// grounding. Storage is embedded DuckDB (no server, no Postgres); cosine search
// runs natively via list_cosine_similarity. Embeddings come from a configurable
// remote endpoint (see embed.go), so nothing is tied to one workstation.
package reference

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"

	"github.com/pdbethke/corralai/internal/annindex"
	"github.com/pdbethke/corralai/internal/embed"
)

var now = func() float64 { return float64(time.Now().UnixNano()) / 1e9 }

// Chunk is one embedded slice of a source document.
type Chunk struct {
	Seq       int
	Text      string
	Embedding []float32
}

// Hit is a search result.
type Hit struct {
	Source string  `json:"source"`
	Kind   string  `json:"kind"`
	Vetted bool    `json:"vetted"`
	Text   string  `json:"text"`
	Score  float64 `json:"score"`
}

// SourceInfo summarizes one ingested source.
type SourceInfo struct {
	Source string `json:"source"`
	Kind   string `json:"kind"`
	Chunks int    `json:"chunks"`
}

// Store holds the DuckDB connection and caches ANN index state.
type Store struct {
	db        *sql.DB
	vss       bool            // vss extension loaded successfully at Open
	annCfg    annindex.Config // injectable for tests; defaults to ConfigFromEnv
	annActive bool            // true when the HNSW index is above threshold and ready
	annDim    int             // cached embedding dimension when annActive==true
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("duckdb", path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS chunks (
		id BIGINT PRIMARY KEY,
		source VARCHAR NOT NULL,
		kind VARCHAR NOT NULL,
		seq INTEGER NOT NULL,
		text VARCHAR NOT NULL,
		embedding FLOAT[] NOT NULL,
		vetted BOOLEAN NOT NULL DEFAULT FALSE,
		created_ts DOUBLE NOT NULL)`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(`CREATE SEQUENCE IF NOT EXISTS chunk_id START 1`); err != nil {
		_ = db.Close()
		return nil, err
	}
	// Migration for DBs created before vetted existed: add the column with a
	// default (DuckDB does not support NOT NULL in ALTER TABLE ADD COLUMN, but
	// all existing rows get DEFAULT FALSE, matching the intended semantics).
	// DuckDB reports "already exists" when the column is already present — that
	// is the idempotence signal (same pattern as coord store's migrations which
	// catch "duplicate column" from SQLite).
	if _, err := db.Exec(`ALTER TABLE chunks ADD COLUMN vetted BOOLEAN DEFAULT FALSE`); err != nil {
		if !strings.Contains(err.Error(), "already exists") {
			_ = db.Close()
			return nil, err
		}
	}

	// Load vss extension for HNSW acceleration. annindex.Loaded is resilient:
	// it returns false on any failure so we degrade gracefully to brute-force.
	vss := annindex.Loaded(db)

	s := &Store{
		db:     db,
		vss:    vss,
		annCfg: annindex.ConfigFromEnv(),
	}
	// Activate HNSW immediately if a reopened, already-populated store is above
	// threshold — otherwise Search would run brute-force for the whole process
	// lifetime, ignoring the persisted index, until the next Replace/Remove.
	// Safe for a fresh empty store: below threshold → active=false, no harm.
	s.ensureANN()
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// ensureANN refreshes the HNSW index state after a write. active is the sole
// brute-force signal — we never return an error from this path.
func (s *Store) ensureANN() {
	if !s.vss {
		return
	}
	active, dim, _ := annindex.Ensure(s.db, "chunks", "embedding", "ref_hnsw", s.annCfg)
	s.annActive = active
	s.annDim = dim
}

// Replace sets a source's chunks: the source's existing chunks are deleted and
// the new ones inserted, so re-ingesting a document is idempotent.
func (s *Store) Replace(source, kind string, chunks []Chunk) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM chunks WHERE source = ?`, source); err != nil {
		return err
	}
	for _, c := range chunks {
		if strings.TrimSpace(c.Text) == "" || len(c.Embedding) == 0 {
			continue
		}
		// The embedding is interpolated as a numeric literal (floats only — no
		// injection surface); go-duckdb's param binding for FLOAT[] is finicky.
		if _, err := tx.Exec(
			`INSERT INTO chunks (id, source, kind, seq, text, embedding, vetted, created_ts)
			 VALUES (nextval('chunk_id'), ?, ?, ?, ?, `+embed.VecLiteral(c.Embedding)+`::FLOAT[], false, ?)`, // #nosec G202 -- not injectable: vector literal is numeric-only (embed.VecLiteral); all other values use ? placeholders
			source, kind, c.Seq, c.Text, now(),
		); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	// After each write, update HNSW state. This is idempotent and cheap below
	// threshold (just a count query + no-op).
	s.ensureANN()
	return nil
}

// Search returns the k chunks most cosine-similar to queryVec.
// When the HNSW index is active and the query vector dimension matches, it uses
// the accelerated candidate-gen + exact re-rank path (overfetch × k candidates
// via array_cosine_distance HNSW scan, then list_cosine_similarity re-rank).
// Otherwise it falls back to the unchanged brute-force list_cosine_similarity scan.
func (s *Store) Search(queryVec []float32, k int) ([]Hit, error) {
	if k <= 0 {
		k = 5
	}
	if len(queryVec) == 0 {
		return []Hit{}, nil
	}

	lit := embed.VecLiteral(queryVec)

	// HNSW path: active, and the query vector's dimension matches the index.
	if s.annActive && len(queryVec) == s.annDim {
		return s.searchHNSW(lit, k)
	}

	// Brute-force path: unchanged.
	return s.searchBruteForce(lit, k)
}

// searchHNSW runs the HNSW candidate-gen + exact re-rank query.
// Inner ORDER BY array_cosine_distance triggers the HNSW index scan.
// Outer ORDER BY list_cosine_similarity provides exact cosine scores.
func (s *Store) searchHNSW(lit string, k int) ([]Hit, error) {
	const overfetch = 4
	dimStr := strconv.Itoa(s.annDim)
	// Wrap: inner fetches k*overfetch candidates via the HNSW index, outer
	// re-ranks exactly and returns the true top-k.
	sql := `SELECT source, kind, vetted, text,` + // #nosec G202 -- not injectable: vector literals are numeric-only (embed.VecLiteral); dimStr is a server-computed integer; all user-supplied values use ? placeholders
		`               list_cosine_similarity(embedding, ` + lit + `::FLOAT[]) AS score
	        FROM (
	            SELECT source, kind, vetted, text, embedding
	            FROM chunks
	            ORDER BY array_cosine_distance(embedding, ` + lit + `::FLOAT[` + dimStr + `])
	            LIMIT ?
	        )
	        ORDER BY score DESC
	        LIMIT ?`
	rows, err := s.db.Query(sql, k*overfetch, k)
	if err != nil {
		// Degrade to brute-force on any SQL error so Search never breaks.
		return s.searchBruteForce(lit, k)
	}
	defer rows.Close()
	out := []Hit{}
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.Source, &h.Kind, &h.Vetted, &h.Text, &h.Score); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// searchBruteForce is the original full-scan cosine similarity query.
func (s *Store) searchBruteForce(lit string, k int) ([]Hit, error) {
	rows, err := s.db.Query(
		`SELECT source, kind, vetted, text, list_cosine_similarity(embedding, `+lit+`::FLOAT[]) AS score
		 FROM chunks ORDER BY score DESC LIMIT ?`, k) // #nosec G202 -- not injectable: vector literal is numeric-only (embed.VecLiteral); k uses ? placeholder
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Hit{}
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.Source, &h.Kind, &h.Vetted, &h.Text, &h.Score); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// Sources lists the ingested sources with their chunk counts.
func (s *Store) Sources() ([]SourceInfo, error) {
	rows, err := s.db.Query(`SELECT source, any_value(kind), count(*) FROM chunks GROUP BY source ORDER BY source`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SourceInfo{}
	for rows.Next() {
		var si SourceInfo
		if err := rows.Scan(&si.Source, &si.Kind, &si.Chunks); err != nil {
			return nil, err
		}
		out = append(out, si)
	}
	return out, rows.Err()
}

// Remove deletes a source's chunks. Returns how many were removed.
// After deletion, the HNSW index is refreshed (Ensure to check threshold,
// Rebuild to evict stale graph edges — reference deletes are infrequent so the
// cost is acceptable).
func (s *Store) Remove(source string) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM chunks WHERE source = ?`, source)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}

	// Refresh HNSW state: Ensure re-evaluates the threshold; Rebuild purges stale
	// graph edges left by the deletion.
	s.ensureANN()
	if s.annActive {
		_ = annindex.Rebuild(s.db, "chunks", "embedding", "ref_hnsw", s.annDim)
	}

	return n, nil
}

// SetVetted promotes all chunks belonging to source to vetted=true. An admin
// must call this after reviewing the source's content (see promote_reference
// MCP tool). Re-ingesting the same source via Replace resets vetted to false.
// Returns an error if no chunks for source exist (not-found guard).
func (s *Store) SetVetted(source string) error {
	res, err := s.db.Exec(`UPDATE chunks SET vetted = true WHERE source = ?`, source)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("no reference source %q", source)
	}
	return nil
}

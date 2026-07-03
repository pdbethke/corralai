// SPDX-License-Identifier: Elastic-2.0

package repoindex

import (
	"bytes"
	"database/sql"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"
	"github.com/pdbethke/corralai/internal/embed"
)

type Store struct {
	db       *sql.DB
	embedder *embed.Client
	fts      bool
}

type FileInput struct {
	Path string
	Text string
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("duckdb", path)
	if err != nil {
		return nil, err
	}
	// DuckDB does not support concurrent writers; a single connection serializes
	// the engine Tick and the create_mission goroutine without races.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS chunks (
		id BIGINT PRIMARY KEY,
		mission_id BIGINT NOT NULL,
		path VARCHAR NOT NULL,
		seq INTEGER NOT NULL,
		start_line INTEGER NOT NULL,
		end_line INTEGER NOT NULL,
		text VARCHAR NOT NULL,
		embedding FLOAT[],
		ts DOUBLE NOT NULL)`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(`CREATE SEQUENCE IF NOT EXISTS repocode_id START 1`); err != nil {
		_ = db.Close()
		return nil, err
	}
	// Idempotent migrations: add symbol/kind/lang columns introduced in Task 1.
	// DuckDB reports "already exists" when the column is already present — that
	// is the idempotence signal (mirrors reference/store.go vetted migration pattern).
	for _, col := range []string{"symbol", "kind", "lang"} {
		if _, err := db.Exec(`ALTER TABLE chunks ADD COLUMN IF NOT EXISTS ` + col + ` VARCHAR`); err != nil {
			if !strings.Contains(err.Error(), "already exists") {
				_ = db.Close()
				return nil, err
			}
		}
	}
	s := &Store{db: db}
	if _, err := db.Exec(`INSTALL fts; LOAD fts;`); err == nil {
		s.fts = true
	} else {
		log.Printf("repoindex: FTS unavailable, keyword search degrades to LIKE: %v", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) SetEmbedder(e *embed.Client) { s.embedder = e }

func (s *Store) countRows(missionID int64) int {
	var n int
	_ = s.db.QueryRow(`SELECT count(*) FROM chunks WHERE mission_id=?`, missionID).Scan(&n)
	return n
}

// IndexFiles re-indexes each file idempotently by (mission_id, path).
// Each file's DELETE + INSERT set is wrapped in a transaction so a mid-file
// INSERT error leaves the old rows intact rather than leaving the file partially
// updated. The FTS rebuild runs once after the loop (outside any transaction).
func (s *Store) IndexFiles(missionID int64, files []FileInput) error {
	for _, f := range files {
		chunks := chunkFile(f.Path, f.Text)
		var vecs [][]float32
		if s.embedder != nil && len(chunks) > 0 {
			texts := make([]string, len(chunks))
			for i, c := range chunks {
				texts[i] = c.Text
			}
			if v, err := s.embedder.Embed(texts); err == nil {
				vecs = v
			} else {
				log.Printf("repoindex: embed %s: %v (stored without vectors)", f.Path, err)
			}
		}
		tx, err := s.db.Begin()
		if err != nil {
			log.Printf("repoindex: begin tx for %s: %v (skipping)", f.Path, err)
			continue
		}
		if _, err := tx.Exec(`DELETE FROM chunks WHERE mission_id=? AND path=?`, missionID, f.Path); err != nil {
			_ = tx.Rollback()
			log.Printf("repoindex: delete %s: %v (skipping)", f.Path, err)
			continue
		}
		txOK := true
		for i, c := range chunks {
			embCol := "NULL"
			if i < len(vecs) && len(vecs[i]) > 0 {
				embCol = embed.VecLiteral(vecs[i]) + "::FLOAT[]"
			}
			q := `INSERT INTO chunks (id, mission_id, path, seq, start_line, end_line, text, embedding, ts, symbol, kind, lang)
				VALUES (nextval('repocode_id'), ?, ?, ?, ?, ?, ?, ` + embCol + `, ?, ?, ?, ?)`
			if _, err := tx.Exec(q, missionID, f.Path, c.Seq, c.StartLine, c.EndLine, c.Text, nowUnix(), c.Symbol, c.Kind, c.Lang); err != nil {
				_ = tx.Rollback()
				log.Printf("repoindex: insert chunk %s seq %d: %v (rolled back)", f.Path, c.Seq, err)
				txOK = false
				break
			}
		}
		if txOK {
			if err := tx.Commit(); err != nil {
				log.Printf("repoindex: commit %s: %v", f.Path, err)
			}
		}
	}
	if s.fts {
		// rebuild the BM25 index over the whole table (idempotent, like memory.Build)
		if _, err := s.db.Exec(`PRAGMA create_fts_index('chunks','id','text',overwrite=1)`); err != nil {
			log.Printf("repoindex: create_fts_index: %v", err)
		}
	}
	return nil
}

// dropChunksForPath removes all indexed chunks for a single (mission, path) pair.
// Used by IndexPaths when a file is deleted, unreadable, binary, or oversized so
// that stale search results cannot reference a path:line that no longer exists.
func (s *Store) dropChunksForPath(missionID int64, p string) {
	if _, err := s.db.Exec(`DELETE FROM chunks WHERE mission_id=? AND path=?`, missionID, p); err != nil {
		log.Printf("repoindex: drop chunks for %s: %v", p, err)
	}
}

// IndexPaths reads each path under dir and indexes it.
// Deleted or unreadable files have their chunks dropped (Fix 1: stale results
// must not survive). Binary content (NUL in first 8 KiB) and files over 512 KiB
// are also dropped and not re-indexed (Fix 3a: no junk embeddings).
func (s *Store) IndexPaths(missionID int64, dir string, paths []string) error {
	files := make([]FileInput, 0, len(paths))
	for _, p := range paths {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p))) // #nosec G304 -- path is a server-configured location (db/config/own file), not attacker input
		if err != nil {
			// File deleted or unreadable: remove any stale chunks so search cannot
			// return a path:line that no longer exists. A deletion is a valid update.
			s.dropChunksForPath(missionID, p)
			continue
		}
		// Skip binary (NUL byte in first 8 KiB) and oversized (>512 KiB) files.
		// Drop any previously-indexed chunks for this path — treat like a delete.
		if len(b) > 512*1024 || bytes.IndexByte(b[:min(len(b), 8192)], 0) >= 0 {
			s.dropChunksForPath(missionID, p)
			continue
		}
		files = append(files, FileInput{Path: p, Text: string(b)})
	}
	return s.IndexFiles(missionID, files)
}

func (s *Store) DropMission(missionID int64) error {
	_, err := s.db.Exec(`DELETE FROM chunks WHERE mission_id=?`, missionID)
	if err == nil && s.fts {
		if _, ferr := s.db.Exec(`PRAGMA create_fts_index('chunks','id','text',overwrite=1)`); ferr != nil {
			log.Printf("repoindex: create_fts_index after drop: %v", ferr)
		}
	}
	return err
}

// nowUnix mirrors the other stores' timestamp helper.
func nowUnix() float64 { return float64(timeNow().UnixNano()) / 1e9 }

func timeNow() time.Time { return time.Now() }

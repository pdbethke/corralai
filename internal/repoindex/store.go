// SPDX-License-Identifier: Elastic-2.0

package repoindex

import (
	"bytes"
	"database/sql"
	"log"
	"os"
	"path/filepath"
	"time"

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

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) SetEmbedder(e *embed.Client) { s.embedder = e }

func (s *Store) countRows(missionID int64) int {
	var n int
	_ = s.db.QueryRow(`SELECT count(*) FROM chunks WHERE mission_id=?`, missionID).Scan(&n)
	return n
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

// nowUnix mirrors the other stores' timestamp helper.
func nowUnix() float64 { return float64(timeNow().UnixNano()) / 1e9 }

func timeNow() time.Time { return time.Now() }

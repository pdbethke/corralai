//go:build cgo

package repoindex

import (
	"database/sql"
	"log"
	"strings"

	_ "github.com/marcboeker/go-duckdb/v2"
	"github.com/pdbethke/corralai/internal/embed"
)

func Open(path string) (*Store, error) {
	db, err := sql.Open("duckdb", path)
	if err != nil {
		return nil, err
	}
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

// staged holds one file's chunks plus its slice offset/length into the single
// batched embedding call's result, so vectors can be re-associated per file.
type staged struct {
	f      FileInput
	chunks []LineChunk
	off, n int
}

func (s *Store) IndexFiles(missionID int64, files []FileInput) error {
	var items []staged
	var allTexts []string
	for _, f := range files {
		cs := chunkFile(f.Path, f.Text)
		off := len(allTexts)
		for _, c := range cs {
			allTexts = append(allTexts, c.Text)
		}
		items = append(items, staged{f: f, chunks: cs, off: off, n: len(cs)})
	}
	var vecs [][]float32
	if s.embedder != nil && len(allTexts) > 0 {
		if v, err := s.embedder.Embed(allTexts); err == nil {
			vecs = v
		} else {
			log.Printf("repoindex: embed %d chunks: %v", len(allTexts), err)
		}
	}
	for _, it := range items {
		f := it.f
		chunks := it.chunks
		var fileVecs [][]float32
		if vecs != nil && it.off+it.n <= len(vecs) {
			fileVecs = vecs[it.off : it.off+it.n]
		}
		tx, err := s.db.Begin()
		if err != nil {
			log.Printf("repoindex: begin tx for %s: %v", f.Path, err)
			continue
		}
		if _, err := tx.Exec(`DELETE FROM chunks WHERE mission_id=? AND path=?`, missionID, f.Path); err != nil {
			_ = tx.Rollback()
			log.Printf("repoindex: delete %s: %v", f.Path, err)
			continue
		}
		txOK := true
		for i, c := range chunks {
			embCol := "NULL"
			if i < len(fileVecs) && len(fileVecs[i]) > 0 {
				embCol = embed.VecLiteral(fileVecs[i]) + "::FLOAT[]"
			}
			q := `INSERT INTO chunks (id, mission_id, path, seq, start_line, end_line, text, embedding, ts, symbol, kind, lang)
				VALUES (nextval('repocode_id'), ?, ?, ?, ?, ?, ?, ` + embCol + `, ?, ?, ?, ?)`
			if _, err := tx.Exec(q, missionID, f.Path, c.Seq, c.StartLine, c.EndLine, c.Text, nowUnix(), c.Symbol, c.Kind, c.Lang); err != nil {
				_ = tx.Rollback()
				log.Printf("repoindex: insert chunk %s seq %d: %v", f.Path, c.Seq, err)
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
		if _, err := s.db.Exec(`PRAGMA create_fts_index('chunks','id','text',overwrite=1)`); err != nil {
			log.Printf("repoindex: create_fts_index: %v", err)
		}
	}
	return nil
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

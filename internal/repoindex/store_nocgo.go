//go:build !cgo

package repoindex

import (
	"database/sql"
	"log"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS chunks (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		mission_id BIGINT NOT NULL,
		path TEXT NOT NULL,
		seq INTEGER NOT NULL,
		start_line INTEGER NOT NULL,
		end_line INTEGER NOT NULL,
		text TEXT NOT NULL,
		embedding TEXT,
		ts DOUBLE NOT NULL,
		symbol TEXT,
		kind TEXT,
		lang TEXT)`); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

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
				log.Printf("repoindex: embed %s: %v", f.Path, err)
			}
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
			var embStr sql.NullString
			if i < len(vecs) && len(vecs[i]) > 0 {
				embStr.String = serializeVector(vecs[i])
				embStr.Valid = true
			}
			q := `INSERT INTO chunks (mission_id, path, seq, start_line, end_line, text, embedding, ts, symbol, kind, lang)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
			if _, err := tx.Exec(q, missionID, f.Path, c.Seq, c.StartLine, c.EndLine, c.Text, embStr, nowUnix(), c.Symbol, c.Kind, c.Lang); err != nil {
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
	return nil
}

func (s *Store) DropMission(missionID int64) error {
	_, err := s.db.Exec(`DELETE FROM chunks WHERE mission_id=?`, missionID)
	return err
}

func serializeVector(v []float32) string {
	var sb strings.Builder
	for i, f := range v {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(strconv.FormatFloat(float64(f), 'f', -1, 32))
	}
	return sb.String()
}

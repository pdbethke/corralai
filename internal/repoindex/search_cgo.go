//go:build cgo

package repoindex

import (
	"database/sql"
	"strings"

	"github.com/pdbethke/corralai/internal/embed"
)

func (s *Store) searchKeyword(missionID int64, query string, k int) ([]Hit, error) {
	var rows *sql.Rows
	var err error
	if s.fts {
		rows, err = s.db.Query(`
			SELECT path, start_line, end_line, text,
				fts_main_chunks.match_bm25(id, ?) AS score,
				COALESCE(symbol,'') AS symbol, COALESCE(kind,'') AS kind, COALESCE(lang,'') AS lang
			FROM chunks WHERE mission_id=? AND score IS NOT NULL
			ORDER BY score DESC LIMIT ?`, query, missionID, k)
	} else {
		escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(query)
		like := "%" + escaped + "%"
		rows, err = s.db.Query(`
			SELECT path, start_line, end_line, text, 1.0 AS score,
				COALESCE(symbol,'') AS symbol, COALESCE(kind,'') AS kind, COALESCE(lang,'') AS lang
			FROM chunks WHERE mission_id=? AND text ILIKE ? ESCAPE '\'
			LIMIT ?`, missionID, like, k)
	}
	if err != nil {
		return nil, err
	}
	return scanHits(rows, "keyword")
}

func (s *Store) searchSemantic(missionID int64, qvec []float32, k int) ([]Hit, error) {
	rows, err := s.db.Query(`SELECT path, start_line, end_line, text,`+
		`	list_cosine_similarity(embedding, `+embed.VecLiteral(qvec)+`::FLOAT[]) AS score,
			COALESCE(symbol,'') AS symbol, COALESCE(kind,'') AS kind, COALESCE(lang,'') AS lang
		FROM chunks WHERE mission_id=? AND embedding IS NOT NULL
		ORDER BY score DESC LIMIT ?`, missionID, k)
	if err != nil {
		return nil, err
	}
	return scanHits(rows, "semantic")
}

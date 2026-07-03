// SPDX-License-Identifier: Elastic-2.0

package repoindex

import (
	"database/sql"
	"sort"
	"strconv"
	"strings"

	"github.com/pdbethke/corralai/internal/embed"
)

// Hit is a single search result from the repoindex Store.
type Hit struct {
	Path      string
	StartLine int
	EndLine   int
	Snippet   string
	Score     float64
	Via       string // "keyword", "semantic", or "both"
	Symbol    string // populated by language-aware chunker (Task 2+); empty in fallback
	Kind      string // e.g. "function", "type"; empty in fallback
	Lang      string // e.g. "go", "python"; tagged from file extension
}

// Search runs a hybrid BM25 + cosine search scoped to missionID.
// If s.embedder is nil, the keyword arm alone is returned (the floor).
// k defaults to 8 when <= 0.
func (s *Store) Search(missionID int64, query string, k int) ([]Hit, error) {
	if k <= 0 {
		k = 8
	}
	kw, err := s.searchKeyword(missionID, query, k)
	if err != nil {
		return nil, err
	}
	if s.embedder == nil {
		return kw, nil
	}
	vecs, err := s.embedder.Embed([]string{query})
	if err != nil || len(vecs) == 0 {
		return kw, nil // semantic unavailable → keyword floor
	}
	sem, err := s.searchSemantic(missionID, vecs[0], k)
	if err != nil {
		return kw, nil
	}
	return mergeHits(kw, sem, k), nil
}

func (s *Store) searchKeyword(missionID int64, query string, k int) ([]Hit, error) {
	var rows *sql.Rows
	var err error
	if s.fts {
		// NOTE: `WHERE score IS NOT NULL` references the SELECT alias `score` — a
		// DuckDB extension (standard SQL forbids alias refs in WHERE). It filters
		// non-matching rows; if the backing DB is ever swapped, this filter would
		// silently stop working (rows would no longer be filtered), so keep it.
		rows, err = s.db.Query(`
			SELECT path, start_line, end_line, text,
				fts_main_chunks.match_bm25(id, ?) AS score,
				COALESCE(symbol,'') AS symbol, COALESCE(kind,'') AS kind, COALESCE(lang,'') AS lang
			FROM chunks WHERE mission_id=? AND score IS NOT NULL
			ORDER BY score DESC LIMIT ?`, query, missionID, k)
	} else {
		// Escape ILIKE metacharacters so a `%`/`_`/`\` in the agent's query is a
		// literal, not a wildcard (otherwise "a%b" silently over-matches).
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
	rows, err := s.db.Query(`SELECT path, start_line, end_line, text,`+ // #nosec G202 -- not injectable: vector literal is numeric-only (embed.VecLiteral); missionID and k use ? placeholders
		`	list_cosine_similarity(embedding, `+embed.VecLiteral(qvec)+`::FLOAT[]) AS score,
			COALESCE(symbol,'') AS symbol, COALESCE(kind,'') AS kind, COALESCE(lang,'') AS lang
		FROM chunks WHERE mission_id=? AND embedding IS NOT NULL
		ORDER BY score DESC LIMIT ?`, missionID, k)
	if err != nil {
		return nil, err
	}
	return scanHits(rows, "semantic")
}

func scanHits(rows *sql.Rows, via string) ([]Hit, error) {
	defer rows.Close()
	var out []Hit
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.Path, &h.StartLine, &h.EndLine, &h.Snippet, &h.Score, &h.Symbol, &h.Kind, &h.Lang); err != nil {
			return nil, err
		}
		h.Via = via
		out = append(out, h)
	}
	return out, rows.Err()
}

// mergeHits max-normalizes each arm to [0,1], unions by path:start_line,
// keeps the higher score when both arms surface the same chunk,
// tags shared hits "both", and returns the top-k.
// Mirrors internal/memory/store.go mergeHits.
func mergeHits(kw, sem []Hit, k int) []Hit {
	norm := func(hs []Hit) {
		var max float64
		for _, h := range hs {
			if h.Score > max {
				max = h.Score
			}
		}
		if max <= 0 {
			return
		}
		for i := range hs {
			hs[i].Score /= max
		}
	}
	norm(kw)
	norm(sem)

	key := func(h Hit) string { return h.Path + ":" + strconv.Itoa(h.StartLine) }
	idx := map[string]int{}
	var out []Hit

	add := func(h Hit) {
		if j, ok := idx[key(h)]; ok {
			if h.Score > out[j].Score {
				out[j].Score = h.Score
			}
			out[j].Via = "both"
			return
		}
		idx[key(h)] = len(out)
		out = append(out, h)
	}
	for _, h := range kw {
		add(h)
	}
	for _, h := range sem {
		add(h)
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > k {
		out = out[:k]
	}
	return out
}

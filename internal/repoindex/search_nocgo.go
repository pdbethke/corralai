//go:build !cgo

package repoindex

import (
	"math"
	"sort"
	"strconv"
	"strings"
)

func (s *Store) searchKeyword(missionID int64, query string, k int) ([]Hit, error) {
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(query)
	like := "%" + escaped + "%"
	rows, err := s.db.Query(`
		SELECT path, start_line, end_line, text, 1.0 AS score,
			COALESCE(symbol,'') AS symbol, COALESCE(kind,'') AS kind, COALESCE(lang,'') AS lang
		FROM chunks WHERE mission_id=? AND text LIKE ? ESCAPE '\'
		LIMIT ?`, missionID, like, k)
	if err != nil {
		return nil, err
	}
	return scanHits(rows, "keyword")
}

func (s *Store) searchSemantic(missionID int64, qvec []float32, k int) ([]Hit, error) {
	rows, err := s.db.Query(`SELECT path, start_line, end_line, text, embedding,
			COALESCE(symbol,'') AS symbol, COALESCE(kind,'') AS kind, COALESCE(lang,'') AS lang
		FROM chunks WHERE mission_id=? AND embedding IS NOT NULL`, missionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var candidates []Hit
	for rows.Next() {
		var h Hit
		var strEmb string
		if err := rows.Scan(&h.Path, &h.StartLine, &h.EndLine, &h.Snippet, &strEmb, &h.Symbol, &h.Kind, &h.Lang); err != nil {
			return nil, err
		}
		emb := deserializeVector(strEmb)
		h.Score = cosineSimilarity(qvec, emb)
		h.Via = "semantic"
		candidates = append(candidates, h)
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Score > candidates[j].Score
	})

	if len(candidates) > k {
		candidates = candidates[:k]
	}
	return candidates, nil
}

func deserializeVector(s string) []float32 {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	res := make([]float32, len(parts))
	for i, p := range parts {
		f, _ := strconv.ParseFloat(p, 32)
		res[i] = float32(f)
	}
	return res
}

func cosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0.0
	}
	var dot, normA, normB float64
	for i := range a {
		valA := float64(a[i])
		valB := float64(b[i])
		dot += valA * valB
		normA += valA * valA
		normB += valB * valB
	}
	if normA == 0.0 || normB == 0.0 {
		return 0.0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

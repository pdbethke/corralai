//go:build !cgo

package memory

import (
	"database/sql"
	"log"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

func Open(path string) (*Store, error) {
	// sqlite (modernc.org/sqlite) is the pure-Go driver
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)

	s := &Store{db: db}
	// Try creating FTS5 virtual table or fall back to standard SQL queries.
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS mem (
		path TEXT PRIMARY KEY, slug TEXT, name TEXT, project TEXT,
		type TEXT, description TEXT, title TEXT, body TEXT, shared BOOLEAN DEFAULT FALSE,
		author TEXT DEFAULT '', embedding TEXT)`)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) ensureANN() {}

func (s *Store) count() int {
	var n int
	_ = s.db.QueryRow("SELECT count(*) FROM mem").Scan(&n)
	return n
}

func (s *Store) EnsureBuilt() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.count() == 0 {
		_, err := s.buildLocked(nil)
		return err
	}
	return nil
}

// Build reindexes dirs; it is the public, locking entry point.
func (s *Store) Build(dirs []string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buildLocked(dirs)
}

// buildLocked is Build's body with no locking of its own — callers must
// already hold s.mu (EnsureBuilt/Add/SetShared use this to avoid re-lock deadlock).
func (s *Store) buildLocked(dirs []string) (int, error) {
	if dirs == nil {
		if d := strings.TrimSpace(os.Getenv("CORRALAI_MEMORY_DIR")); d != "" {
			dirs = []string{d}
		} else {
			dirs = []string{}
		}
	}
	s.lastDirs = dirs
	rows := iterEntries(dirs, s.tiers)

	// Snapshot old embeddings so we don't re-embed unchanged entries
	type prev struct{ text, emb string }
	old := map[string]prev{}
	if rs, err := s.db.Query("SELECT path, name||' '||description||' '||body, COALESCE(embedding, '') FROM mem"); err == nil {
		for rs.Next() {
			var p, t, em string
			if rs.Scan(&p, &t, &em) == nil {
				old[p] = prev{text: t, emb: em}
			}
		}
		_ = rs.Close()
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec("DELETE FROM mem"); err != nil {
		_ = tx.Rollback()
		return 0, err
	}

	inserted := 0
	for _, e := range rows {
		embExpr := "NULL"
		if pv, ok := old[e.path]; ok && pv.text == e.name+" "+e.description+" "+e.body && pv.emb != "" {
			embExpr = lit(pv.emb)
		}
		stmt := "INSERT INTO mem (path,slug,name,project,type,description,title,body,shared,author,embedding) VALUES (" +
			lit(e.path) + "," + lit(e.slug) + "," + lit(e.name) + "," + lit(e.project) + "," +
			lit(e.typ) + "," + lit(e.description) + "," + lit(e.title) + "," + lit(e.body) + "," +
			bsql(e.shared) + "," + lit(e.author) + "," + embExpr + ")"
		if _, err := tx.Exec(stmt); err != nil {
			continue
		}
		inserted++
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}

	if s.embedder != nil {
		var paths, texts []string
		rs, err := s.db.Query("SELECT path, name||' '||description||' '||body FROM mem WHERE embedding IS NULL")
		if err == nil {
			for rs.Next() {
				var p, txt string
				if rs.Scan(&p, &txt) == nil {
					paths = append(paths, p)
					texts = append(texts, txt)
				}
			}
			_ = rs.Close()
		}
		if len(texts) > 0 {
			if vecs, err := s.embedder.Embed(texts); err != nil {
				log.Printf("memory: embed %d entries: %v", len(texts), err)
			} else {
				for i, p := range paths {
					if i < len(vecs) {
						strVec := serializeVector(vecs[i])
						if _, err := s.db.Exec("UPDATE mem SET embedding = ? WHERE path = ?", strVec, p); err != nil {
							log.Printf("memory: embedding UPDATE for %s: %v", p, err)
						}
					}
				}
			}
		}
	}
	return inserted, nil
}

func (s *Store) Search(query, scope, typ string, limit int, sharedOnly bool) ([]Hit, error) {
	kw, err := s.searchKeyword(query, scope, typ, limit, sharedOnly)
	if err != nil {
		return nil, err
	}
	if s.embedder == nil {
		return kw, nil
	}
	vecs, err := s.embedder.Embed([]string{query})
	if err != nil || len(vecs) == 0 {
		return kw, nil
	}
	sem, err := s.searchSemantic(vecs[0], scope, typ, limit, sharedOnly)
	if err != nil {
		return kw, nil
	}
	return mergeHits(kw, sem, limit), nil
}

func (s *Store) searchKeyword(query, scope, typ string, limit int, sharedOnly bool) ([]Hit, error) {
	if limit <= 0 {
		limit = 8
	}
	if strings.TrimSpace(query) == "" {
		return []Hit{}, nil
	}
	var where []string
	var args []any

	// Fall back to pure-Go SQL standard LIKE queries
	var ors []string
	for _, t := range strings.Fields(strings.ToLower(query)) {
		ors = append(ors, "lower(name||' '||description||' '||body) LIKE ?")
		args = append(args, "%"+t+"%")
	}
	if len(ors) > 0 {
		where = append(where, "("+strings.Join(ors, " OR ")+")")
	}

	if scope != "" && scope != "*" {
		where = append(where, "project = ?")
		args = append(args, scope)
	}
	if typ != "" {
		where = append(where, "type = ?")
		args = append(args, typ)
	}
	if sharedOnly {
		where = append(where, "shared = 1")
	}

	q := "SELECT slug, name, project, type, description, shared, author, 0.0 AS score FROM mem"
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " LIMIT ?"
	args = append(args, limit)
	hits, err := s.scanHits(q, args)
	if err != nil {
		return nil, err
	}
	for i := range hits {
		hits[i].Via = "keyword"
	}
	return hits, nil
}

func (s *Store) searchSemantic(qv []float32, scope, typ string, limit int, sharedOnly bool) ([]Hit, error) {
	where := "embedding IS NOT NULL"
	args := []any{}
	if sharedOnly {
		where += " AND shared = 1"
	}
	if scope != "" && scope != "*" {
		where += " AND project = ?"
		args = append(args, scope)
	}
	if typ != "" {
		where += " AND type = ?"
		args = append(args, typ)
	}

	q := "SELECT slug, name, project, type, description, shared, author, embedding FROM mem WHERE " + where
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var candidates []Hit
	for rows.Next() {
		var h Hit
		var strEmb string
		if err := rows.Scan(&h.Slug, &h.Name, &h.Project, &h.Type, &h.Description, &h.Shared, &h.Author, &strEmb); err != nil {
			return nil, err
		}
		emb := deserializeVector(strEmb)
		h.Score = cosineSimilarity(qv, emb)
		h.Via = "semantic"
		candidates = append(candidates, h)
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Score > candidates[j].Score
	})

	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return candidates, nil
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

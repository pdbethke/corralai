//go:build cgo

package memory

import (
	"database/sql"
	"log"
	"os"
	"strconv"
	"strings"

	_ "github.com/marcboeker/go-duckdb/v2"

	"github.com/pdbethke/corralai/internal/annindex"
	"github.com/pdbethke/corralai/internal/embed"
)

func Open(path string) (*Store, error) {
	db, err := sql.Open("duckdb", path)
	if err != nil {
		return nil, err
	}
	vss := annindex.Loaded(db)

	s := &Store{db: db, vss: vss, annCfg: annindex.ConfigFromEnv()}
	if _, err := db.Exec("INSTALL fts; LOAD fts;"); err == nil {
		s.fts = true
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS mem (
		path VARCHAR PRIMARY KEY, slug VARCHAR, name VARCHAR, project VARCHAR,
		type VARCHAR, description VARCHAR, title VARCHAR, body VARCHAR, shared BOOLEAN DEFAULT FALSE)`)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	_, _ = db.Exec("ALTER TABLE mem ADD COLUMN shared BOOLEAN DEFAULT FALSE")
	_, _ = db.Exec("ALTER TABLE mem ADD COLUMN author VARCHAR DEFAULT ''")
	_, _ = db.Exec("ALTER TABLE mem ADD COLUMN embedding FLOAT[]")
	s.ensureANN()
	return s, nil
}

func (s *Store) ensureANN() {
	if !s.vss {
		return
	}
	active, dim, _ := annindex.Ensure(s.db, "mem", "embedding", "mem_hnsw", s.annCfg)
	s.annActive = active
	s.annDim = dim
}

func (s *Store) count() int {
	var n int
	_ = s.db.QueryRow("SELECT count(*) FROM mem").Scan(&n)
	return n
}

func (s *Store) EnsureBuilt() error {
	if s.count() == 0 {
		_, err := s.Build(nil)
		return err
	}
	return nil
}

func (s *Store) Build(dirs []string) (int, error) {
	if dirs == nil {
		if d := strings.TrimSpace(os.Getenv("CORRALAI_MEMORY_DIR")); d != "" {
			dirs = []string{d}
		} else {
			dirs = []string{}
		}
	}
	s.lastDirs = dirs
	rows := iterEntries(dirs, s.tiers)

	type prev struct{ text, emb string }
	old := map[string]prev{}
	if rs, err := s.db.Query("SELECT path, name||' '||description||' '||body, CASE WHEN embedding IS NULL THEN '' ELSE embedding::VARCHAR END FROM mem"); err == nil {
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
			embExpr = pv.emb + "::FLOAT[]"
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
	if s.fts {
		if _, err := s.db.Exec("PRAGMA create_fts_index('mem','path','name','title','description','body',overwrite=1)"); err != nil {
			return inserted, err
		}
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
						if _, err := s.db.Exec("UPDATE mem SET embedding = "+embed.VecLiteral(vecs[i])+"::FLOAT[] WHERE path = ?", p); err != nil {
							log.Printf("memory: embedding UPDATE for %s: %v", p, err)
						}
					}
				}
			}
		}
	}
	s.ensureANN()
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
	sel := "SELECT slug, name, project, type, description, shared, author, 0.0 AS score FROM mem"
	if s.fts {
		sel = "SELECT slug, name, project, type, description, shared, author, fts_main_mem.match_bm25(path, ?) AS score FROM mem"
		args = append(args, query)
		where = append(where, "score IS NOT NULL")
	} else {
		var ors []string
		for _, t := range strings.Fields(strings.ToLower(query)) {
			ors = append(ors, "lower(name||' '||description||' '||body) LIKE ?")
			args = append(args, "%"+t+"%")
		}
		if len(ors) > 0 {
			where = append(where, "("+strings.Join(ors, " OR ")+")")
		}
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
		where = append(where, "shared = TRUE")
	}
	q := sel
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	if s.fts {
		q += " ORDER BY score DESC"
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
	vecLit := embed.VecLiteral(qv)
	if s.annActive && len(qv) == s.annDim {
		return s.searchHNSWFiltered(vecLit, scope, typ, limit, sharedOnly)
	}
	return s.searchBruteForce(vecLit, scope, typ, limit, sharedOnly)
}

func (s *Store) searchHNSWFiltered(lit, scope, typ string, k int, sharedOnly bool) ([]Hit, error) {
	const overfetch = 8
	dimStr := strconv.Itoa(s.annDim)

	innerWhere := "embedding IS NOT NULL"
	var args []any
	if sharedOnly {
		innerWhere += " AND shared = TRUE"
	}
	if scope != "" && scope != "*" {
		innerWhere += " AND project = ?"
		args = append(args, scope)
	}
	if typ != "" {
		innerWhere += " AND type = ?"
		args = append(args, typ)
	}
	args = append(args, k*overfetch, k)

	q := `SELECT slug, name, project, type, description, shared, author,` +
		`             list_cosine_similarity(embedding, ` + lit + `::FLOAT[]) AS score
	      FROM (
	          SELECT slug, name, project, type, description, shared, author, embedding
	          FROM mem
	          WHERE ` + innerWhere + `
	          ORDER BY array_cosine_distance(embedding, ` + lit + `::FLOAT[` + dimStr + `])
	          LIMIT ?
	      )
	      ORDER BY score DESC
	      LIMIT ?`

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return s.searchBruteForce(lit, scope, typ, k, sharedOnly)
	}
	defer rows.Close()
	out := []Hit{}
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.Slug, &h.Name, &h.Project, &h.Type, &h.Description, &h.Shared, &h.Author, &h.Score); err != nil {
			return nil, err
		}
		h.Via = "semantic"
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *Store) searchBruteForce(lit, scope, typ string, k int, sharedOnly bool) ([]Hit, error) {
	where := "embedding IS NOT NULL"
	args := []any{}
	if sharedOnly {
		where += " AND shared = TRUE"
	}
	if scope != "" && scope != "*" {
		where += " AND project = ?"
		args = append(args, scope)
	}
	if typ != "" {
		where += " AND type = ?"
		args = append(args, typ)
	}
	q := "SELECT slug, name, project, type, description, shared, author," +
		" list_cosine_similarity(embedding, " + lit + "::FLOAT[]) AS score" +
		" FROM mem WHERE " + where + " ORDER BY score DESC LIMIT ?"
	args = append(args, k)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Hit{}
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.Slug, &h.Name, &h.Project, &h.Type, &h.Description, &h.Shared, &h.Author, &h.Score); err != nil {
			return nil, err
		}
		h.Via = "semantic"
		out = append(out, h)
	}
	return out, rows.Err()
}

// SPDX-License-Identifier: Elastic-2.0

// Package memory is CorralAI's memory half: a multi-tier, searchable knowledge
// corpus backed by DuckDB FTS (BM25). Source of truth stays the per-project
// markdown files under ~/.claude/projects/*/memory/; this is a derived,
// rebuildable index. DuckDB (not SQLite) so the semantic upgrade — vectors via
// the VSS extension — and MotherDuck sync are a natural next step.
package memory

import (
	"database/sql"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	_ "github.com/marcboeker/go-duckdb/v2"
	"gopkg.in/yaml.v3"

	"github.com/pdbethke/corralai/internal/annindex"
	"github.com/pdbethke/corralai/internal/embed"
)

var (
	home      = mustHome()
	DefaultDB = filepath.Join(home, ".claude", "corralai_memory.duckdb")
	dirGlob   = filepath.Join(home, ".claude", "projects", "*", "memory")
	skipNames = map[string]bool{"MEMORY.md": true, "ARCHIVE.md": true}
	fmRe      = regexp.MustCompile(`(?s)^---\n(.*?)\n---\n?(.*)$`)
)

func mustHome() string { h, _ := os.UserHomeDir(); return h }

// memoryDir is where new entries are written when no target dir is given.
// Override with CORRALAI_MEMORY_DIR; otherwise a neutral per-tool location.
// Resolution is lazy — read on every call rather than cached at package init
// — so tests can set CORRALAI_MEMORY_DIR (via t.Setenv, per-test) before
// their first Add and have it take effect. A package-level var resolved at
// init time would already have captured the unset env var by the time any
// test runs; a cached-after-first-call resolution (e.g. sync.Once) would
// still pin every later test in the same process to whichever value the
// first caller saw, defeating per-test isolation. The lookup is a single
// cheap os.Getenv, so re-reading it on each Add is not a meaningful cost.
func memoryDir() string {
	if d := strings.TrimSpace(os.Getenv("CORRALAI_MEMORY_DIR")); d != "" {
		return d
	}
	return filepath.Join(home, ".claude", "projects", "default", "memory")
}

// lit renders a Go string as a DuckDB string literal (quotes doubled, invalid UTF-8 stripped).
func lit(s string) string {
	return "'" + strings.ReplaceAll(strings.ToValidUTF8(s, ""), "'", "''") + "'"
}

type Store struct {
	db        *sql.DB
	fts       bool
	lastDirs  []string        // dirs of the last Build, so Add/SetShared reindex the right corpus
	tiers     []tierRule      // optional substring->tier rules (CORRALAI_PROJECT_TIERS)
	embedder  *embed.Client   // optional; nil => keyword-only
	vss       bool            // vss extension loaded at Open
	annCfg    annindex.Config // injectable for tests; defaults to ConfigFromEnv
	annActive bool            // true when HNSW index is ready
	annDim    int             // cached embedding dimension when annActive==true
}

// tierRule maps a path/name substring to a project tier label.
type tierRule struct{ substr, tier string }

// SetEmbedder enables semantic indexing/search. nil keeps the store keyword-only.
func (s *Store) SetEmbedder(e *embed.Client) { s.embedder = e }

// SetProjectTiers configures path-based project-tier classification from a spec
// like "substr=tier,substr=tier" (e.g. CORRALAI_PROJECT_TIERS). Rules are tried
// in order; an entry's own front-matter project:/scope: always wins over them.
// Empty spec => entries fall back to the "default" tier unless front-matter says
// otherwise. Call before Build.
func (s *Store) SetProjectTiers(spec string) { s.tiers = parseTierRules(spec) }

func parseTierRules(spec string) []tierRule {
	var rules []tierRule
	for _, item := range strings.Split(spec, ",") {
		kv := strings.SplitN(strings.TrimSpace(item), "=", 2)
		if len(kv) != 2 {
			continue
		}
		sub := strings.ToLower(strings.TrimSpace(kv[0]))
		tier := strings.TrimSpace(kv[1])
		if sub == "" || tier == "" {
			continue
		}
		rules = append(rules, tierRule{sub, tier})
	}
	return rules
}

type Hit struct {
	Slug        string  `json:"slug"`
	Name        string  `json:"name"`
	Project     string  `json:"project"`
	Type        string  `json:"type"`
	Description string  `json:"description"`
	Shared      bool    `json:"shared"`
	Author      string  `json:"author,omitempty"`
	Score       float64 `json:"score,omitempty"`
	Via         string  `json:"via,omitempty"`
}

type Entry struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Project     string `json:"project"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Shared      bool   `json:"shared"`
	Author      string `json:"author,omitempty"`
	Body        string `json:"body"`
	Path        string `json:"path"`
}

type entry struct {
	slug, name, project, typ, description, title, body, path, author string
	shared                                                           bool
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("duckdb", path)
	if err != nil {
		return nil, err
	}
	// Load vss for HNSW acceleration. annindex.Loaded is resilient: it returns
	// false on any failure so we degrade gracefully to brute-force.
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
	// Migrate pre-existing indexes that lack the column (error ignored if present).
	_, _ = db.Exec("ALTER TABLE mem ADD COLUMN shared BOOLEAN DEFAULT FALSE")
	_, _ = db.Exec("ALTER TABLE mem ADD COLUMN author VARCHAR DEFAULT ''")
	_, _ = db.Exec("ALTER TABLE mem ADD COLUMN embedding FLOAT[]")
	// Activate HNSW immediately if a reopened, already-populated store is above
	// threshold — same reopen fix applied to reference in Task 2.
	s.ensureANN()
	return s, nil
}

// ensureANN refreshes the HNSW index state after a write. active is the sole
// brute-force signal — we never return an error from this path.
func (s *Store) ensureANN() {
	if !s.vss {
		return
	}
	active, dim, _ := annindex.Ensure(s.db, "mem", "embedding", "mem_hnsw", s.annCfg)
	s.annActive = active
	s.annDim = dim
}

func bsql(b bool) string {
	if b {
		return "TRUE"
	}
	return "FALSE"
}

func (s *Store) Close() error { return s.db.Close() }
func (s *Store) FTS() bool    { return s.fts }

// projectFor decides an entry's tier: its own front-matter project:/scope: wins;
// otherwise the first configured rule whose substring appears in the entry's dir
// or name; otherwise "default".
func projectFor(encodedDir, name string, fm map[string]any, rules []tierRule) string {
	if p := strings.ToLower(strings.TrimSpace(str(fm["project"]) + str(fm["scope"]))); p != "" {
		return p
	}
	hay := strings.ToLower(encodedDir + " " + name)
	for _, r := range rules {
		if strings.Contains(hay, r.substr) {
			return r.tier
		}
	}
	return "default"
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func entryType(fm map[string]any, filename string) string {
	if md, ok := fm["metadata"].(map[string]any); ok {
		if t := str(md["type"]); t != "" {
			return strings.ToLower(t)
		}
	}
	if t := str(fm["type"]); t != "" {
		return strings.ToLower(t)
	}
	for _, p := range []string{"feedback", "reference", "project", "user"} {
		if strings.HasPrefix(filename, p) {
			return p
		}
	}
	return "reference"
}

func parseEntry(path, encodedDir string, rules []tierRule) entry {
	raw, _ := os.ReadFile(path) // #nosec G703,G304 -- path is a server-configured location (db/config/own file), not attacker input
	text := string(raw)
	fm := map[string]any{}
	body := strings.TrimSpace(text)
	if m := fmRe.FindStringSubmatch(text); m != nil {
		_ = yaml.Unmarshal([]byte(m[1]), &fm)
		body = strings.TrimSpace(m[2])
	}
	stem := strings.TrimSuffix(filepath.Base(path), ".md")
	name := str(fm["name"])
	if name == "" {
		name = stem
	}
	title := name
	if name == stem && body != "" {
		title = strings.TrimLeft(strings.SplitN(body, "\n", 2)[0], "# ")
	}
	if len(title) > 200 {
		title = title[:200]
	}
	shared, _ := fm["shared"].(bool)
	return entry{slug: stem, name: name, project: projectFor(encodedDir, stem, fm, rules),
		typ: entryType(fm, stem), description: str(fm["description"]), title: title, body: body, path: path, shared: shared,
		author: str(fm["author"])}
}

func iterEntries(dirs []string, rules []tierRule) []entry {
	var out []entry
	for _, d := range dirs {
		encoded := filepath.Base(filepath.Dir(d))
		paths, _ := filepath.Glob(filepath.Join(d, "*.md"))
		for _, p := range paths {
			if skipNames[filepath.Base(p)] {
				continue
			}
			out = append(out, parseEntry(p, encoded, rules))
		}
	}
	return out
}

// Build re-indexes the corpus. nil dirs indexes only explicitly configured dirs:
// CORRALAI_MEMORY_DIR (single dir) when set, otherwise nothing.
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

	// Snapshot existing embed-text+embedding BEFORE DELETE so we can carry vectors
	// forward for unchanged entries (avoiding O(n²) re-embedding on every Add).
	// The comparison text must match the post-insert embed SELECT exactly:
	// name||' '||description||' '||body — so name or description edits also trigger
	// a fresh embed rather than carrying a stale vector.
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
	// go-duckdb's parameter binding rejects very large string params ("could not
	// bind parameter"), so build escaped literal INSERTs instead: data is local and
	// trusted, single quotes are doubled, and invalid UTF-8 is stripped.
	inserted := 0
	for _, e := range rows {
		embExpr := "NULL"
		if pv, ok := old[e.path]; ok && pv.text == e.name+" "+e.description+" "+e.body && pv.emb != "" {
			embExpr = pv.emb + "::FLOAT[]"
		}
		stmt := "INSERT INTO mem (path,slug,name,project,type,description,title,body,shared,author,embedding) VALUES (" + // #nosec G202 -- not injectable: string values are escaped via lit() (standard SQL literal: single-quotes doubled, invalid UTF-8 stripped) and vector literals are numeric-only (embed.VecLiteral); user-supplied scalars use ? placeholders
			lit(e.path) + "," + lit(e.slug) + "," + lit(e.name) + "," + lit(e.project) + "," +
			lit(e.typ) + "," + lit(e.description) + "," + lit(e.title) + "," + lit(e.body) + "," +
			bsql(e.shared) + "," + lit(e.author) + "," + embExpr + ")"
		if _, err := tx.Exec(stmt); err != nil {
			continue // skip a malformed row rather than failing the whole index
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
	// Embed rows whose embedding is still NULL (new/changed entries) in one batch.
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
				log.Printf("memory: embed %d entries: %v (keyword search still works)", len(texts), err)
			} else {
				for i, p := range paths {
					if i < len(vecs) {
						if _, err := s.db.Exec("UPDATE mem SET embedding = "+embed.VecLiteral(vecs[i])+"::FLOAT[] WHERE path = ?", p); err != nil { // #nosec G202 -- not injectable: vector literal is numeric-only (embed.VecLiteral); path uses ? placeholder
							log.Printf("memory: embedding UPDATE for %s: %v", p, err)
						}
					}
				}
			}
		}
	}
	// After a full wipe+reinsert, (re)establish the HNSW index. Ensure is idempotent:
	// it CREATES the index if the store just crossed the threshold, and is a no-op if the
	// index already exists (DuckDB maintains the HNSW index incrementally across the
	// DELETE+reinsert, so recall stays correct without an explicit rebuild here).
	s.ensureANN()
	return inserted, nil
}

func (s *Store) count() int {
	var n int
	_ = s.db.QueryRow("SELECT count(*) FROM mem").Scan(&n)
	return n
}

// EnsureBuilt builds the index on first use if empty.
func (s *Store) EnsureBuilt() error {
	if s.count() == 0 {
		_, err := s.Build(nil)
		return err
	}
	return nil
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
		return kw, nil // semantic unavailable this call → keyword floor
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
		// LIKE fallback (no FTS extension): match any term in name/description/body.
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

// searchSemantic dispatches to the HNSW-accelerated path when the index is active
// and the query vector's dimension matches, otherwise falls back to brute-force.
func (s *Store) searchSemantic(qv []float32, scope, typ string, limit int, sharedOnly bool) ([]Hit, error) {
	vecLit := embed.VecLiteral(qv) // vector literal; not the package-level lit() string escaper
	if s.annActive && len(qv) == s.annDim {
		return s.searchHNSWFiltered(vecLit, scope, typ, limit, sharedOnly)
	}
	return s.searchBruteForce(vecLit, scope, typ, limit, sharedOnly)
}

// searchHNSWFiltered runs the HNSW candidate-gen + exact re-rank query with the
// shared/project/type filters applied. Verified via EXPLAIN: DuckDB uses HNSW_INDEX_SCAN
// and applies the filters as a FILTER node ON TOP of the index scan — it does NOT filter
// inside the graph traversal. Placing the filters in the inner subquery therefore does not
// change the plan vs. filtering only on the outer, BUT it makes the LIMIT count FILTERED
// candidates, so up to k×overfetch matching rows reach the exact re-rank even under a
// highly selective filter — which is what guarantees filtered ANN == filtered brute-force.
// Over-fetch by 8× (more generous than reference's 4× because filters reduce the pool).
// On any SQL error it degrades to brute-force — Search never breaks.
func (s *Store) searchHNSWFiltered(lit, scope, typ string, k int, sharedOnly bool) ([]Hit, error) {
	const overfetch = 8
	dimStr := strconv.Itoa(s.annDim)

	// Inner (candidate-gen) filters — see the function doc: DuckDB applies these as a FILTER
	// on top of HNSW_INDEX_SCAN, and the LIMIT counts filtered rows so the re-rank sees up to
	// k×overfetch matching candidates.
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
	// Args order: inner filter args, inner LIMIT (k*overfetch), outer LIMIT (k).
	args = append(args, k*overfetch, k)

	q := `SELECT slug, name, project, type, description, shared, author,` + // #nosec G202 -- not injectable: vector literals are numeric-only (embed.VecLiteral); innerWhere uses only constant predicates + ? placeholders; dimStr is a server-computed integer
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
		// Degrade to brute-force on any SQL error so Search never breaks.
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

// searchBruteForce is the original full-scan cosine similarity query. Unchanged
// from the pre-HNSW implementation; used below threshold, on dim mismatch, when
// vss is unavailable, or when the Config is disabled.
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
	q := "SELECT slug, name, project, type, description, shared, author," + // #nosec G202 -- not injectable: vector literals are numeric-only (embed.VecLiteral); where clauses use ? placeholders for all user-supplied scalars
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

func mergeHits(kw, sem []Hit, limit int) []Hit {
	norm := func(hs []Hit) {
		if len(hs) == 0 {
			return
		}
		max := hs[0].Score
		for _, h := range hs {
			if h.Score > max {
				max = h.Score
			}
		}
		if max <= 0 {
			return
		}
		for i := range hs {
			hs[i].Score = hs[i].Score / max
		}
	}
	cp := func(hs []Hit) []Hit { c := make([]Hit, len(hs)); copy(c, hs); return c }
	k, m := cp(kw), cp(sem)
	norm(k)
	norm(m)
	by := map[string]Hit{}
	add := func(h Hit) {
		if e, ok := by[h.Slug]; ok {
			if h.Score > e.Score {
				e.Score = h.Score
			}
			e.Via = "both"
			by[h.Slug] = e
		} else {
			by[h.Slug] = h
		}
	}
	for _, h := range k {
		add(h)
	}
	for _, h := range m {
		add(h)
	}
	out := make([]Hit, 0, len(by))
	for _, h := range by {
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// RecallLessons returns the lessons (type=lesson) most relevant to query — the
// recall side of the learning loop. Lessons are team knowledge, so results are
// not viewer-scoped. Builds the index on first use.
func (s *Store) RecallLessons(query string, k int) ([]Hit, error) {
	if k <= 0 {
		k = 5
	}
	if err := s.EnsureBuilt(); err != nil {
		return nil, err
	}
	return s.Search(query, "", "lesson", k, true) // sharedOnly=true: only vetted lessons auto-recall
}

// LessonForLearning is a local, learn-package-free projection of a lesson
// entry — name/body/author — for the sweep ticker to convert into a
// learn.LessonDoc at the call site. This package must never import learn (the
// dependency runs the other way: main wires memory's output into learn's
// input), so the shape lives here instead of importing learn's type.
type LessonForLearning struct {
	Name, Body, Author string
}

// LessonsForLearning returns up to limit type=lesson entries (name/body/author)
// for the learn sweep ticker to feed into learn.Sweep. Unlike RecallLessons
// (query-relevance ranked) this is an unfiltered recency-ish listing — the
// sweep wants the whole corpus of lessons, not a query match. limit<=0 => 200.
func (s *Store) LessonsForLearning(limit int) ([]LessonForLearning, error) {
	if limit <= 0 {
		limit = 200
	}
	// TODO: ORDER BY name is an alphabetical proxy for recency — the mem table
	// has no timestamp column. Real fix: add a timestamp column and order by it.
	rows, err := s.db.Query(
		"SELECT name, body, author FROM mem WHERE type = 'lesson' ORDER BY name LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LessonForLearning{}
	for rows.Next() {
		var d LessonForLearning
		if err := rows.Scan(&d.Name, &d.Body, &d.Author); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) List(scope, typ string, limit int, sharedOnly bool) ([]Hit, error) {
	if limit <= 0 {
		limit = 100
	}
	var where []string
	var args []any
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
	q := "SELECT slug, name, project, type, description, shared, author, 0.0 FROM mem"
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY name LIMIT ?"
	args = append(args, limit)
	return s.scanHits(q, args)
}

func (s *Store) scanHits(q string, args []any) ([]Hit, error) {
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
		out = append(out, h)
	}
	return out, rows.Err()
}

// Get returns one entry. When sharedOnly, a non-shared entry is treated as absent
// (so a non-owner can't read it even by exact name).
func (s *Store) Get(nameOrSlug string, sharedOnly bool) (*Entry, error) {
	var e Entry
	err := s.db.QueryRow(
		"SELECT slug, name, project, type, description, shared, author, body, path FROM mem WHERE slug=? OR name=? LIMIT 1",
		nameOrSlug, nameOrSlug).Scan(&e.Slug, &e.Name, &e.Project, &e.Type, &e.Description, &e.Shared, &e.Author, &e.Body, &e.Path)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if sharedOnly && !e.Shared {
		return nil, nil
	}
	return &e, nil
}

var slugRe = regexp.MustCompile(`[^a-z0-9_-]+`)

// Add writes a markdown entry (source of truth) to the default memory dir and
// reindexes. shared=true marks it team-visible (the shared knowledge base).
// author, when non-empty, is written into front-matter so it survives re-index.
func (s *Store) Add(name, body, description, typ, project, targetDir string, shared bool, author string) (slug, path, status string, err error) {
	if targetDir == "" {
		targetDir = memoryDir()
	}
	if typ == "" {
		typ = "reference"
	}
	if project == "" {
		project = "default"
	}
	slug = strings.Trim(slugRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-"), "-")
	if slug == "" {
		slug = "untitled"
	}
	path = filepath.Join(targetDir, slug+".md")
	status = "created"
	if _, statErr := os.Stat(path); statErr == nil {
		status = "updated"
	}
	front := map[string]any{"name": name, "description": description, "project": project,
		"metadata": map[string]any{"type": typ}}
	if shared {
		front["shared"] = true
	}
	if author != "" {
		front["author"] = author
	}
	fmBytes, _ := yaml.Marshal(front)
	if err = os.MkdirAll(targetDir, 0o700); err != nil {
		return
	}
	content := "---\n" + strings.TrimSpace(string(fmBytes)) + "\n---\n\n" + strings.TrimSpace(body) + "\n"
	if err = os.WriteFile(path, []byte(content), 0o600); err != nil {
		return
	}
	buildDirs := s.lastDirs
	if len(buildDirs) == 0 {
		buildDirs = []string{targetDir}
		s.lastDirs = buildDirs
	}
	_, err = s.Build(buildDirs)
	return
}

// SetShared flips an existing entry's shared flag (admin promote/unshare): it edits
// the entry's markdown frontmatter (source of truth) and reindexes.
func (s *Store) SetShared(nameOrSlug string, shared bool) (bool, error) {
	e, err := s.Get(nameOrSlug, false)
	if err != nil || e == nil {
		return false, err
	}
	raw, err := os.ReadFile(e.Path)
	if err != nil {
		return false, err
	}
	text := string(raw)
	fm := map[string]any{}
	body := strings.TrimSpace(text)
	if m := fmRe.FindStringSubmatch(text); m != nil {
		_ = yaml.Unmarshal([]byte(m[1]), &fm)
		body = strings.TrimSpace(m[2])
	}
	if shared {
		fm["shared"] = true
	} else {
		delete(fm, "shared")
	}
	fmBytes, _ := yaml.Marshal(fm)
	content := "---\n" + strings.TrimSpace(string(fmBytes)) + "\n---\n\n" + body + "\n"
	if err := os.WriteFile(e.Path, []byte(content), 0o600); err != nil { // #nosec G703 -- targetDir is server-set (add_memory passes ""→memoryDir()) and the filename is a sanitized slug ([^a-z0-9_-]); not agent-controllable
		return false, err
	}
	_, err = s.Build(s.lastDirs)
	return true, err
}

type Stats struct {
	Total     int            `json:"total"`
	ByProject map[string]int `json:"by_project"`
}

func (s *Store) Stats(sharedOnly bool) (*Stats, error) {
	st := &Stats{ByProject: map[string]int{}}
	filter := ""
	if sharedOnly {
		filter = " WHERE shared = TRUE"
	}
	_ = s.db.QueryRow("SELECT count(*) FROM mem" + filter).Scan(&st.Total)
	rows, err := s.db.Query("SELECT project, count(*) FROM mem" + filter + " GROUP BY project ORDER BY count(*) DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		var c int
		if err := rows.Scan(&p, &c); err != nil {
			return nil, err
		}
		st.ByProject[p] = c
	}
	return st, rows.Err()
}

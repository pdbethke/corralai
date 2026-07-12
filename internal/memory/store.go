// SPDX-License-Identifier: Elastic-2.0

// Package memory is CorralAI's memory half: a multi-tier, searchable knowledge
// corpus backed by DuckDB FTS (BM25). Source of truth stays the per-project
// markdown files under ~/.claude/projects/*/memory/; this is a derived,
// rebuildable index. DuckDB (not SQLite) so the semantic upgrade — vectors via
// the VSS extension — and MotherDuck sync are a natural next step.
package memory

import (
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

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
	mu        sync.RWMutex // guards db mutation/build state below (lastDirs + the FTS/embedding rebuild, plus the ANN index fields below); writers (Build/EnsureBuilt/Add/SetShared) take Lock, Search takes RLock so concurrent searches don't race a concurrent build's write to annActive/annDim/the HNSW index. Open pins SetMaxOpenConns(1) alongside this
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
	s.mu.Lock()
	defer s.mu.Unlock()
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
	_, err = s.buildLocked(buildDirs)
	return
}

// SetShared flips an existing entry's shared flag (admin promote/unshare): it edits
// the entry's markdown frontmatter (source of truth) and reindexes.
func (s *Store) SetShared(nameOrSlug string, shared bool) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	_, err = s.buildLocked(s.lastDirs)
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

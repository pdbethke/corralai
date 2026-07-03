# Repo Code Index (DuckDB semantic/hybrid search) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Index a mission's working copy into DuckDB (FTS + embeddings) and serve hybrid/semantic code search over MCP via `repo_search`, so bees find code by meaning — per mission, dropped on completion.

**Architecture:** A new `internal/repoindex` engine that mirrors the memory engine's recipe exactly (`INSTALL fts` + `create_fts_index` + `match_bm25` ⊕ `list_cosine_similarity` + max-normalize `mergeHits`), over its own `corralai_repocode.duckdb`, partitioned by `mission_id`. A line-window chunker. Indexing is triggered at the per-commit seam (incremental) and at clone (full); `DropMission` runs on completion.

**Tech Stack:** Go 1.26; `github.com/marcboeker/go-duckdb/v2` (CGO); the shared `internal/embed` client; the `internal/repo` engine + mission engine from #15.

## Global Constraints

- **Builds on #15.** Reuses the mission engine's per-commit seam (`commitDonePhases`), `mission.MissionDir`, the brain's claimed-mission resolution, and `Options.Repo`/`Options.Workspace`. Do #15 first. (#16 is independent of this plan.)
- **Reuse the memory recipe verbatim** — `INSTALL fts; LOAD fts;` → `create_fts_index(table, idcol, textcol, overwrite=1)` → `fts_main_<table>.match_bm25(idcol, ?)`; `list_cosine_similarity(embedding, [..]::FLOAT[])`; inline vectors via `embed.VecLiteral(v)+"::FLOAT[]"` (the go-duckdb FLOAT[] param workaround). Max-normalize each arm to [0,1], union by `path:start_line`, keep the higher score, tag `Via ∈ {keyword,semantic,both}`.
- **Graceful degradation:** no embedder (`embed.New()` returns nil) → BM25 keyword floor, never a failure. Embed failure on a file → store chunks with NULL embedding (BM25 still finds them), log.
- **Per-mission, never cross-mission.** Every row carries `mission_id`; `Search` filters by it; `DropMission` removes a mission's rows on completion. There is no shared cross-mission corpus.
- **One DB file per engine** — `corralai_repocode.duckdb`, single `chunks` table (the established convention).
- **Search is an aid, not a gate** — an indexing error logs and the mission proceeds; search returns the prior state.
- This engine is CGO (DuckDB); it lives brain-side only. `go build ./...` stays clean each task.

---

## File Structure

- `internal/repoindex/chunk.go` (create) — `chunkLines` + `LineChunk`.
- `internal/repoindex/chunk_test.go` (create) — window/overlap/line-number tests.
- `internal/repoindex/store.go` (create) — `Open`/`SetEmbedder`/`IndexFiles`/`IndexPaths`/`DropMission` + schema/FTS.
- `internal/repoindex/search.go` (create) — `Search` (hybrid) + `mergeHits`.
- `internal/repoindex/store_test.go` (create) — index/idempotent/drop + a shared fake embed server.
- `internal/repoindex/search_test.go` (create) — semantic/keyword/both/nil-embedder.
- `internal/repo/changed.go` (create) — `ChangedFiles` git op.
- `internal/repo/changed_test.go` (create) — changed-files test.
- `internal/mission/engine.go` (modify) — `Indexer` interface + `Index` field; index at the commit seam; `DropMission` on done.
- `internal/mission/engine_test.go` (modify) — fake `Indexer` spy.
- `internal/brain/reposearch.go` (create) — `repo_search` tool.
- `internal/brain/missions.go` (modify) — initial full index after provisioning.
- `internal/brain/identity.go` (modify) — `Options.Index`.
- `internal/brain/server.go` (modify) — register `repo_search`.
- `cmd/corral/main.go` (modify) — construct `repoindex`, wire engine + brain.

---

## Task 1: line-window chunker

**Files:** Create `internal/repoindex/chunk.go`, `internal/repoindex/chunk_test.go`

**Interfaces:**
- Produces: `type LineChunk struct{ Seq, StartLine, EndLine int; Text string }`;
  `func chunkLines(text string, window, overlap int) []LineChunk` (1-based lines; default window 60, overlap 15).

- [ ] **Step 1: Write the failing test**

```go
// internal/repoindex/chunk_test.go
package repoindex

import (
	"strings"
	"testing"
)

func TestChunkLines(t *testing.T) {
	// 100 numbered lines
	var sb strings.Builder
	for i := 1; i <= 100; i++ {
		sb.WriteString("line")
		sb.WriteByte('\n')
	}
	cs := chunkLines(sb.String(), 60, 15) // step = 45
	if len(cs) != 2 {
		t.Fatalf("want 2 chunks, got %d", len(cs))
	}
	if cs[0].StartLine != 1 || cs[0].EndLine != 60 {
		t.Fatalf("chunk0 lines = %d-%d", cs[0].StartLine, cs[0].EndLine)
	}
	if cs[1].StartLine != 46 || cs[1].EndLine != 100 {
		t.Fatalf("chunk1 lines = %d-%d", cs[1].StartLine, cs[1].EndLine)
	}
	if cs[1].Seq != 1 {
		t.Fatalf("chunk1 seq = %d", cs[1].Seq)
	}
}

func TestChunkLinesShortAndEmpty(t *testing.T) {
	cs := chunkLines("a\nb\nc\n", 60, 15)
	if len(cs) != 1 || cs[0].StartLine != 1 || cs[0].EndLine != 3 || cs[0].Text != "a\nb\nc" {
		t.Fatalf("short file: %+v", cs)
	}
	if len(chunkLines("", 60, 15)) != 0 {
		t.Fatal("empty text should produce no chunks")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/repoindex/ -run TestChunk`
Expected: FAIL — package/`chunkLines` undefined.

- [ ] **Step 3: Implement `internal/repoindex/chunk.go`**

```go
package repoindex

import "strings"

type LineChunk struct {
	Seq       int
	StartLine int
	EndLine   int
	Text      string
}

// chunkLines splits text into overlapping windows of `window` lines stepping by
// (window-overlap). Lines are 1-based; the final short window is included.
func chunkLines(text string, window, overlap int) []LineChunk {
	if window <= 0 {
		window = 60
	}
	if overlap < 0 || overlap >= window {
		overlap = window / 4
	}
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1] // drop the empty tail from a trailing newline
	}
	if len(lines) == 0 {
		return nil
	}
	step := window - overlap
	var out []LineChunk
	seq := 0
	for start := 0; start < len(lines); start += step {
		end := start + window
		if end > len(lines) {
			end = len(lines)
		}
		out = append(out, LineChunk{Seq: seq, StartLine: start + 1, EndLine: end, Text: strings.Join(lines[start:end], "\n")})
		seq++
		if end == len(lines) {
			break
		}
	}
	return out
}
```

- [ ] **Step 4: Run tests + commit**

Run: `go test ./internal/repoindex/`
Expected: PASS.

```bash
git add internal/repoindex/chunk.go internal/repoindex/chunk_test.go
git commit -m "feat(repoindex): line-window code chunker"
```

---

## Task 2: repoindex store — Open, IndexFiles, IndexPaths, DropMission

**Files:** Create `internal/repoindex/store.go`, `internal/repoindex/store_test.go`

**Interfaces:**
- Consumes: `chunkLines` (Task 1); `embed.Client.{Embed,Model}`, `embed.VecLiteral` (existing); `embed.NewFor` (tests).
- Produces:
  - `type FileInput struct{ Path, Text string }`
  - `func Open(path string) (*Store, error)`; `func (s *Store) Close() error`
  - `func (s *Store) SetEmbedder(e *embed.Client)`
  - `func (s *Store) IndexFiles(missionID int64, files []FileInput) error`
  - `func (s *Store) IndexPaths(missionID int64, dir string, paths []string) error` (reads each path under dir → `IndexFiles`)
  - `func (s *Store) DropMission(missionID int64) error`

- [ ] **Step 1: Write the failing test**

```go
// internal/repoindex/store_test.go
package repoindex

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/embed"
)

// fakeEmbedServer returns a deterministic 3-dim vector per input: a coarse "topic"
// one-hot — dim0 if the text mentions auth/login/security, dim1 if it mentions
// parse/url, else dim2. This lets a semantic query match a non-lexically-overlapping
// chunk in the search tests.
func fakeEmbedServer(t *testing.T) *embed.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Input []string `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		type emb struct {
			Embedding []float32 `json:"embedding"`
		}
		out := struct {
			Data []emb `json:"data"`
		}{}
		for _, s := range in.Input {
			ls := strings.ToLower(s)
			v := []float32{0, 0, 1}
			if strings.Contains(ls, "auth") || strings.Contains(ls, "login") || strings.Contains(ls, "security") {
				v = []float32{1, 0, 0}
			} else if strings.Contains(ls, "parse") || strings.Contains(ls, "url") {
				v = []float32{0, 1, 0}
			}
			out.Data = append(out.Data, emb{Embedding: v})
		}
		json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	return embed.NewFor(srv.URL, "fake", "")
}

func TestIndexAndIdempotentAndDrop(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "rc.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	s.SetEmbedder(fakeEmbedServer(t))

	if err := s.IndexFiles(7, []FileInput{{Path: "auth.go", Text: "func Authenticate() {}\n"}}); err != nil {
		t.Fatal(err)
	}
	if n := s.countRows(7); n == 0 { // test-only helper (add an unexported countRows in store.go)
		t.Fatal("expected rows for mission 7")
	}
	// re-index same path → replaces, does not duplicate
	before := s.countRows(7)
	if err := s.IndexFiles(7, []FileInput{{Path: "auth.go", Text: "func Authenticate() {}\nfunc Logout() {}\n"}}); err != nil {
		t.Fatal(err)
	}
	if s.countRows(7) != before { // 1-chunk file both times → same row count, content replaced
		t.Fatalf("idempotent upsert duplicated rows: %d → %d", before, s.countRows(7))
	}
	// a different mission is isolated
	s.IndexFiles(8, []FileInput{{Path: "x.go", Text: "package x\n"}})
	if err := s.DropMission(7); err != nil {
		t.Fatal(err)
	}
	if s.countRows(7) != 0 {
		t.Fatal("DropMission(7) left rows")
	}
	if s.countRows(8) == 0 {
		t.Fatal("DropMission(7) wrongly removed mission 8")
	}
}
```

> NOTE: add an unexported `func (s *Store) countRows(missionID int64) int` to `store.go` for the tests (a `SELECT count(*) FROM chunks WHERE mission_id=?`). `IndexPaths` is exercised in the engine test (#17 Task 4) and the brain test (Task 5); its read-the-files behavior is trivial over `IndexFiles`.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/repoindex/ -run TestIndex`
Expected: FAIL — `Open`/`IndexFiles` undefined.

- [ ] **Step 3: Implement `internal/repoindex/store.go`**

```go
package repoindex

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/marcboeker/go-duckdb/v2"
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

func Open(path string) (*Store, error) {
	db, err := sql.Open("duckdb", path)
	if err != nil {
		return nil, err
	}
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
		db.Close()
		return nil, err
	}
	db.Exec(`CREATE SEQUENCE IF NOT EXISTS repocode_id START 1`)
	s := &Store{db: db}
	if _, err := db.Exec(`INSTALL fts; LOAD fts;`); err == nil {
		s.fts = true
	} else {
		log.Printf("repoindex: FTS unavailable, keyword search degrades to LIKE: %v", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) SetEmbedder(e *embed.Client) { s.embedder = e }

func (s *Store) countRows(missionID int64) int {
	var n int
	s.db.QueryRow(`SELECT count(*) FROM chunks WHERE mission_id=?`, missionID).Scan(&n)
	return n
}

// IndexFiles re-indexes each file idempotently by (mission_id, path).
func (s *Store) IndexFiles(missionID int64, files []FileInput) error {
	for _, f := range files {
		chunks := chunkLines(f.Text, 60, 15)
		var vecs [][]float32
		if s.embedder != nil && len(chunks) > 0 {
			texts := make([]string, len(chunks))
			for i, c := range chunks {
				texts[i] = c.Text
			}
			if v, err := s.embedder.Embed(texts); err == nil {
				vecs = v
			} else {
				log.Printf("repoindex: embed %s: %v (stored without vectors)", f.Path, err)
			}
		}
		if _, err := s.db.Exec(`DELETE FROM chunks WHERE mission_id=? AND path=?`, missionID, f.Path); err != nil {
			return err
		}
		for i, c := range chunks {
			embCol := "NULL"
			if i < len(vecs) && len(vecs[i]) > 0 {
				embCol = embed.VecLiteral(vecs[i]) + "::FLOAT[]"
			}
			q := `INSERT INTO chunks (id, mission_id, path, seq, start_line, end_line, text, embedding, ts)
				VALUES (nextval('repocode_id'), ?, ?, ?, ?, ?, ?, ` + embCol + `, ?)`
			if _, err := s.db.Exec(q, missionID, f.Path, c.Seq, c.StartLine, c.EndLine, c.Text, nowUnix()); err != nil {
				return err
			}
		}
	}
	if s.fts {
		// rebuild the BM25 index over the whole table (idempotent, like memory.Build)
		if _, err := s.db.Exec(`PRAGMA create_fts_index('chunks','id','text',overwrite=1)`); err != nil {
			log.Printf("repoindex: create_fts_index: %v", err)
		}
	}
	return nil
}

// IndexPaths reads each path under dir and indexes it.
func (s *Store) IndexPaths(missionID int64, dir string, paths []string) error {
	files := make([]FileInput, 0, len(paths))
	for _, p := range paths {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
		if err != nil {
			continue // deleted/binary/unreadable — skip
		}
		files = append(files, FileInput{Path: p, Text: string(b)})
	}
	return s.IndexFiles(missionID, files)
}

func (s *Store) DropMission(missionID int64) error {
	_, err := s.db.Exec(`DELETE FROM chunks WHERE mission_id=?`, missionID)
	if err == nil && s.fts {
		s.db.Exec(`PRAGMA create_fts_index('chunks','id','text',overwrite=1)`)
	}
	return err
}

// nowUnix mirrors the other stores' timestamp helper.
func nowUnix() float64 { return float64(timeNow().UnixNano()) / 1e9 }
```

> NOTE: `timeNow` — if `internal/repoindex` should avoid `time.Now()` directly, follow whatever the memory/reference stores do for timestamps; otherwise `func timeNow() time.Time { return time.Now() }` with `import "time"` is fine (this is brain-side production code, not a workflow script). The `fmt`/`strings` imports are used by `search.go` (Task 3) in the same package — if unused here, drop them from this file.

- [ ] **Step 4: Run tests + commit**

Run: `go test ./internal/repoindex/`
Expected: PASS — index creates rows; re-indexing a path replaces (no dup); `DropMission` is per-mission.

```bash
git add internal/repoindex/store.go internal/repoindex/store_test.go
git commit -m "feat(repoindex): DuckDB store — IndexFiles/IndexPaths/DropMission (FTS + embeddings)"
```

---

## Task 3: repoindex Search — hybrid BM25 ⊕ cosine

**Files:** Create `internal/repoindex/search.go`, `internal/repoindex/search_test.go`

**Interfaces:**
- Consumes: `Store`, `s.embedder`, `s.fts`, `s.db` (Task 2); `embed.VecLiteral`.
- Produces: `type Hit struct{ Path string; StartLine, EndLine int; Snippet string; Score float64; Via string }`;
  `func (s *Store) Search(missionID int64, query string, k int) ([]Hit, error)`.

- [ ] **Step 1: Write the failing test**

```go
// internal/repoindex/search_test.go
package repoindex

import (
	"path/filepath"
	"testing"
)

func seedSearch(t *testing.T, withEmbed bool) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "rc.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if withEmbed {
		s.SetEmbedder(fakeEmbedServer(t))
	}
	// auth chunk: no lexical "login"; parse chunk: lexical "ParseURL"
	s.IndexFiles(1, []FileInput{
		{Path: "auth.go", Text: "func Authenticate(token string) bool { return verify(token) }\n"},
		{Path: "url.go", Text: "func ParseURL(s string) (string, error) { return s, nil }\n"},
	})
	return s
}

func TestSearchSemanticNoLexicalOverlap(t *testing.T) {
	s := seedSearch(t, true)
	// "login security" shares no token with auth.go, but maps to the same fake topic vector
	hits, err := s.Search(1, "login security", 5)
	if err != nil || len(hits) == 0 {
		t.Fatalf("hits=%v err=%v", hits, err)
	}
	if hits[0].Path != "auth.go" {
		t.Fatalf("semantic arm should rank auth.go first, got %s", hits[0].Path)
	}
	if hits[0].Via != "semantic" && hits[0].Via != "both" {
		t.Fatalf("expected semantic/both Via, got %s", hits[0].Via)
	}
}

func TestSearchExactToken(t *testing.T) {
	s := seedSearch(t, true)
	hits, err := s.Search(1, "ParseURL", 5)
	if err != nil || len(hits) == 0 {
		t.Fatalf("hits=%v err=%v", hits, err)
	}
	if hits[0].Path != "url.go" {
		t.Fatalf("keyword arm should rank url.go first, got %s", hits[0].Path)
	}
}

func TestSearchKeywordFloorNoEmbedder(t *testing.T) {
	s := seedSearch(t, false) // nil embedder
	hits, err := s.Search(1, "Authenticate", 5)
	if err != nil {
		t.Fatalf("nil embedder must not error: %v", err)
	}
	if len(hits) == 0 || hits[0].Path != "auth.go" {
		t.Fatalf("keyword floor should find auth.go, got %v", hits)
	}
	for _, h := range hits {
		if h.Via == "semantic" {
			t.Fatal("no semantic hits without an embedder")
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/repoindex/ -run TestSearch`
Expected: FAIL — `Search` undefined.

- [ ] **Step 3: Implement `internal/repoindex/search.go`**

```go
package repoindex

import (
	"sort"

	"github.com/pdbethke/corralai/internal/embed"
)

type Hit struct {
	Path      string
	StartLine int
	EndLine   int
	Snippet   string
	Score     float64
	Via       string
}

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
	var rows interface {
		Next() bool
		Scan(...any) error
		Close() error
		Err() error
	}
	var err error
	if s.fts {
		rows, err = s.db.Query(`SELECT path, start_line, end_line, text,
			fts_main_chunks.match_bm25(id, ?) AS score
			FROM chunks WHERE mission_id=? AND score IS NOT NULL
			ORDER BY score DESC LIMIT ?`, query, missionID, k)
	} else {
		like := "%" + query + "%"
		rows, err = s.db.Query(`SELECT path, start_line, end_line, text, 1.0 AS score
			FROM chunks WHERE mission_id=? AND text ILIKE ?
			LIMIT ?`, missionID, like, k)
	}
	if err != nil {
		return nil, err
	}
	return scanHits(rows, "keyword")
}

func (s *Store) searchSemantic(missionID int64, qvec []float32, k int) ([]Hit, error) {
	rows, err := s.db.Query(`SELECT path, start_line, end_line, text,
		list_cosine_similarity(embedding, `+embed.VecLiteral(qvec)+`::FLOAT[]) AS score
		FROM chunks WHERE mission_id=? AND embedding IS NOT NULL
		ORDER BY score DESC LIMIT ?`, missionID, k)
	if err != nil {
		return nil, err
	}
	return scanHits(rows, "semantic")
}

func scanHits(rows interface {
	Next() bool
	Scan(...any) error
	Close() error
	Err() error
}, via string) ([]Hit, error) {
	defer rows.Close()
	var out []Hit
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.Path, &h.StartLine, &h.EndLine, &h.Snippet, &h.Score); err != nil {
			return nil, err
		}
		h.Via = via
		out = append(out, h)
	}
	return out, rows.Err()
}

// mergeHits max-normalizes each arm to [0,1], unions by path:line, keeps the higher
// score, tags shared hits "both", and returns the top-k. Mirrors memory.mergeHits.
func mergeHits(kw, sem []Hit, k int) []Hit {
	norm := func(hs []Hit) {
		var max float64
		for _, h := range hs {
			if h.Score > max {
				max = h.Score
			}
		}
		if max > 0 {
			for i := range hs {
				hs[i].Score /= max
			}
		}
	}
	norm(kw)
	norm(sem)
	key := func(h Hit) string { return h.Path + ":" + itoa(h.StartLine) }
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

func itoa(n int) string {
	// small local helper; if the package already imports strconv, use it directly.
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
```

> NOTE: the `interface{ Next; Scan; Close; Err }` shape lets `scanHits` accept `*sql.Rows` without importing the concrete type awkwardly — but simpler is to just type these as `*sql.Rows` and `import "database/sql"`. Prefer `*sql.Rows` if the reviewer finds the structural interface noisy; it's a style call. `itoa` duplicates `strconv.Itoa` — replace the body with `strconv.Itoa(n)` and `import "strconv"` (shown long-hand only to keep the file self-contained).

- [ ] **Step 4: Run tests + commit**

Run: `go test ./internal/repoindex/`
Expected: PASS — semantic surfaces `auth.go` for "login security"; keyword surfaces `url.go` for "ParseURL"; nil embedder is keyword-only, no error.

```bash
git add internal/repoindex/search.go internal/repoindex/search_test.go
git commit -m "feat(repoindex): hybrid Search (BM25 + cosine) with merge/normalize + Via"
```

---

## Task 4: repo.ChangedFiles + engine indexing trigger

**Files:** Create `internal/repo/changed.go`, `internal/repo/changed_test.go`; Modify `internal/mission/engine.go`, `internal/mission/engine_test.go`

**Interfaces:**
- Consumes: `repo.Engine.git` (#15); `repoindex.Store.{IndexPaths,DropMission}` (Tasks 2); the engine's `commitDonePhases`/`finishRepoMission`/`workdir` (#15 Task 4).
- Produces:
  - `func (e *Engine) ChangedFiles(ctx context.Context, dir string) ([]string, error)` (repo) — files in `HEAD` (`git show --name-only --pretty=format: HEAD`).
  - mission `type Indexer interface { IndexPaths(missionID int64, dir string, paths []string) error; DropMission(missionID int64) error }`; `Engine.Index Indexer`; `RepoOps` gains `ChangedFiles(ctx, dir) ([]string, error)`.

- [ ] **Step 1: Write the failing tests**

```go
// internal/repo/changed_test.go
package repo

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestChangedFiles(t *testing.T) {
	bare := makeBareRepoWithCommit(t) // #15 Task 1 helper
	dest := filepath.Join(t.TempDir(), "w")
	e := New("", "")
	ctx := context.Background()
	if err := e.Clone(ctx, bare, "main", dest); err != nil {
		t.Fatal(err)
	}
	e.Checkout(ctx, dest, "feature")
	os.WriteFile(filepath.Join(dest, "new.go"), []byte("package x\n"), 0o644)
	if _, err := e.Commit(ctx, dest, "add new"); err != nil {
		t.Fatal(err)
	}
	changed, err := e.ChangedFiles(ctx, dest)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range changed {
		if f == "new.go" {
			found = true
		}
	}
	if !found {
		t.Fatalf("ChangedFiles missing new.go: %v", changed)
	}
}
```

```go
// internal/mission/engine_test.go — add
type fakeIndexer struct {
	indexed map[int64][]string
	dropped map[int64]bool
}
func newFakeIndexer() *fakeIndexer { return &fakeIndexer{indexed: map[int64][]string{}, dropped: map[int64]bool{}} }
func (f *fakeIndexer) IndexPaths(missionID int64, dir string, paths []string) error {
	f.indexed[missionID] = append(f.indexed[missionID], paths...)
	return nil
}
func (f *fakeIndexer) DropMission(missionID int64) error { f.dropped[missionID] = true; return nil }

// Extend the #15 repo-mission engine test: set e.Index = newFakeIndexer(), give the
// fakeRepo a ChangedFiles returning ["calc.go"], and assert that after a gate-passed
// commit the indexer recorded "calc.go" for the mission, and after mission-done the
// indexer dropped the mission.
```

> Implementer: extend the existing `TestEnginePhaseCommitAndPRForRepoMission` (#15 Task 4) — add `ChangedFiles` to `fakeRepo` (returns `["calc.go"], nil`), set `e.Index = newFakeIndexer()`, and after driving `Tick` assert `idx.indexed[id]` contains `"calc.go"` and `idx.dropped[id]` is true once the mission completes.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/repo/ -run TestChangedFiles` and `go test ./internal/mission/ -run TestEnginePhase`
Expected: FAIL — `ChangedFiles`/`Engine.Index` undefined.

- [ ] **Step 3: Implement**

`internal/repo/changed.go`:
```go
package repo

import (
	"context"
	"strings"
)

// ChangedFiles lists the files touched by the HEAD commit (added/modified).
func (e *Engine) ChangedFiles(ctx context.Context, dir string) ([]string, error) {
	out, err := e.git(ctx, dir, "show", "--name-only", "--pretty=format:", "HEAD")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if l := strings.TrimSpace(line); l != "" {
			files = append(files, l)
		}
	}
	return files, nil
}
```

`internal/mission/engine.go`:
- Add to the `RepoOps` interface: `ChangedFiles(ctx context.Context, dir string) ([]string, error)`.
- Add the indexer interface + field:
```go
type Indexer interface {
	IndexPaths(missionID int64, dir string, paths []string) error
	DropMission(missionID int64) error
}
```
Add `Index Indexer` to `Engine`.
- In `commitDonePhases`, right after a commit succeeds with `ok == true`, index the changed files:
```go
		} else if ok {
			log.Printf("mission %d: committed phase %s", m.ID, p.Name)
			if e.Index != nil {
				if changed, cerr := e.Repo.ChangedFiles(context.Background(), e.workdir(m)); cerr == nil && len(changed) > 0 {
					if ierr := e.Index.IndexPaths(m.ID, e.workdir(m), changed); ierr != nil {
						log.Printf("mission %d: index phase %s: %v", m.ID, p.Name, ierr)
					}
				}
			}
		}
```
- In `finishRepoMission`, after the PR step (or right before returning), drop the index:
```go
	if e.Index != nil {
		_ = e.Index.DropMission(id)
	}
```

- [ ] **Step 4: Run tests + commit**

Run: `go test ./internal/repo/ ./internal/mission/ && go build ./...`
Expected: PASS; build OK. (`fakeRepo` in the mission test now needs the `ChangedFiles` method — add it.)

```bash
git add internal/repo/changed.go internal/repo/changed_test.go internal/mission/engine.go internal/mission/engine_test.go
git commit -m "feat(mission): index gate-passed commits + drop on done (Indexer); repo.ChangedFiles"
```

---

## Task 5: brain repo_search + initial full index + cmd wiring

**Files:** Create `internal/brain/reposearch.go`; Modify `internal/brain/missions.go`, `internal/brain/identity.go`, `internal/brain/server.go`, `cmd/corral/main.go`

**Interfaces:**
- Consumes: `repoindex.Store.{Search,IndexPaths}` (Tasks 2–3); `mission.MissionDir`, the claimed-mission resolution (`repoMissionDir` from #16, or the read-tool resolver from #15 — reuse whichever exists); `Options` (#15).
- Produces: `Options.Index *repoindex.Store`; tool `repo_search{name, query, k}`; full index at provisioning.

- [ ] **Step 1: Write the failing test**

```go
// internal/brain/reposearch_test.go
package brain

// Build a brain with Options{Queue, Missions, Repo:repo.New("",""), Workspace:ws,
// Index: <a repoindex.Store with a fake embedder>}. Seed a repo mission whose
// MissionDir holds auth.go; index it (Options.Index.IndexPaths(id, dir, ["auth.go"]));
// a bee claims the task. Then repo_search{name:bee, query:"Authenticate"} returns a hit
// for auth.go. A caller with no claimed repo mission → "not on a repo mission" error.
//
// Implementer: mirror reposync_test.go / repofiles_test.go harness. Construct the
// repoindex store with internal/repoindex.Open(t.TempDir()+"/rc.duckdb") and a fake
// embed server (or nil embedder — keyword floor still returns the hit for an exact token).
}
```

> Implementer: complete using the in-package MCP harness. A nil embedder is fine for this test (exact token "Authenticate" hits via the keyword floor), which avoids standing up an embed server in the brain package.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/brain/ -run TestRepoSearch`
Expected: FAIL — tool not registered.

- [ ] **Step 3: Implement `internal/brain/reposearch.go`**

```go
package brain

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pdbethke/corralai/internal/repoindex"
)

type repoSearchIn struct {
	Name  string `json:"name"`
	Query string `json:"query"`
	K     int    `json:"k,omitempty"`
}
type repoSearchOut struct {
	Hits []repoindex.Hit `json:"hits"`
}

func registerRepoSearch(s *mcp.Server, opts Options) {
	mcp.AddTool(s, &mcp.Tool{Name: "repo_search",
		Description: "Semantic/hybrid code search over your mission's working copy. Returns path:line ranges ranked by meaning (and keyword). Use it to find where something is handled before editing."},
		func(ctx context.Context, req *mcp.CallToolRequest, in repoSearchIn) (*mcp.CallToolResult, repoSearchOut, error) {
			mid, _ := opts.Queue.ClaimedMission(identity(req, in.Name))
			if mid == 0 {
				return nil, repoSearchOut{}, errNotRepoMission()
			}
			mi, err := opts.Missions.Mission(mid)
			if err != nil || mi == nil || mi.Repo == "" {
				return nil, repoSearchOut{}, errNotRepoMission()
			}
			hits, err := opts.Index.Search(mid, in.Query, in.K)
			if err != nil {
				return nil, repoSearchOut{}, err
			}
			return nil, repoSearchOut{Hits: hits}, nil
		})
}

func errNotRepoMission() error { return fmt.Errorf("not on a repo mission") }
```
(Add `"fmt"` to imports; or reuse the existing not-on-mission error helper from #15/#16 if one exists — DRY, don't define a second.)

- [ ] **Step 4: Options + provisioning + registration**

(a) `internal/brain/identity.go` — add to `Options`:
```go
	// Index, when set, enables repo_search and per-mission code indexing.
	Index *repoindex.Store
```
(import `github.com/pdbethke/corralai/internal/repoindex`).

(b) `internal/brain/missions.go` — after the successful clone+checkout+`SetRepo` in `create_mission` (the #15 provisioning block), do an initial full index so brownfield code is searchable immediately:
```go
				if opts.Index != nil {
					var all []string
					filepath.WalkDir(dest, func(p string, d fs.DirEntry, err error) error {
						if err != nil { return nil }
						if d.IsDir() {
							if d.Name() == ".git" || d.Name() == "node_modules" || d.Name() == "vendor" { return filepath.SkipDir }
							return nil
						}
						rel, _ := filepath.Rel(dest, p)
						all = append(all, filepath.ToSlash(rel))
						return nil
					})
					if err := opts.Index.IndexPaths(id, dest, all); err != nil {
						log.Printf("mission %d: initial index: %v", id, err)
					}
				}
```
(add imports `io/fs`, `path/filepath`, `log` if missing; reuse `dest` from the provisioning block.)

(c) `internal/brain/server.go` — register when the index is configured:
```go
	if opts.Index != nil && opts.Queue != nil && opts.Missions != nil {
		registerRepoSearch(s, opts)
	}
```

- [ ] **Step 5: Wire `cmd/corral/main.go`**

Next to the repo engine construction (#15 Task 6):
```go
	var repoIdx *repoindex.Store
	if repoEng != nil { // repo work enabled ⇒ enable the code index too
		idxDB := env("CORRALAI_REPOCODE_DB", filepath.Join(homeDir(), ".claude", "corralai_repocode.duckdb"))
		if ri, err := repoindex.Open(idxDB); err != nil {
			log.Printf("repo code index disabled: %v", err)
		} else {
			ri.SetEmbedder(embedder) // same shared vector space as memory/reference
			repoIdx = ri
			engine.Index = repoIdx
			log.Printf("repo code index enabled (%s)", idxDB)
		}
	}
```
Add `Index: repoIdx` to the `brain.Options{...}` literal and `github.com/pdbethke/corralai/internal/repoindex` to imports. (`homeDir()`/`env()` are the existing helpers used for the other DB paths — match the memory/reference construction at the lines the explore noted.)

- [ ] **Step 6: Build + commit**

Run: `go build ./... && go test ./internal/brain/ ./internal/repoindex/ ./internal/mission/`
Expected: build OK; tests PASS.

```bash
git add internal/brain/reposearch.go internal/brain/reposearch_test.go internal/brain/missions.go internal/brain/identity.go internal/brain/server.go cmd/corral/main.go
git commit -m "feat(brain): repo_search tool + initial full index at provisioning; wire repoindex in corral"
```

---

## Final verification

- [ ] `go build ./...` — OK
- [ ] `go test ./internal/repoindex/ ./internal/repo/ ./internal/mission/ ./internal/brain/` — all PASS
- [ ] Per-mission isolation: `Search`/`IndexFiles`/`DropMission` all key on `mission_id`; a `DropMission` on one mission leaves others intact (Task 2 test) — no cross-mission corpus.
- [ ] Graceful: nil embedder → keyword floor, no error (Task 3 test); FTS-unavailable → LIKE fallback.
- [ ] One DB file: `corralai_repocode.duckdb`, single `chunks` table — matches the one-file-per-engine convention.

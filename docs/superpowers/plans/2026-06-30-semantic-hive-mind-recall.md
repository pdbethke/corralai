# Semantic Hive-Mind Recall Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give the shared memory corpus hybrid semantic+keyword recall with per-agent attribution, and wire it into the ask-a-bee narrator, so a heterogeneous swarm shares one queryable meaning-space ("hey Hawk, have you seen this vuln?").

**Architecture:** Extract the reference engine's `Embedder` into a shared `internal/embed`. Add `author` (markdown front-matter, survives re-index) and `embedding FLOAT[]` (index-only, preserved across re-index) to the memory store; embed on (re)build; add a hybrid `Search` that merges BM25 and DuckDB-native `list_cosine_similarity`. The brain stamps the authoritative author on `add_memory`; the narrator calls the now-hybrid `mem.Search` keyed on the question and flags each hit as the asked agent's own vs the hive's. Graceful degradation: no embedder → keyword-only, nothing fails.

**Tech Stack:** Go 1.26; DuckDB (CGO, `internal/memory` + `internal/reference`) with `list_cosine_similarity` + FTS/BM25; OpenAI-compatible `/v1/embeddings` (demo: `gemini-embedding-001`); MCP via go-sdk.

## Global Constraints

- **Graceful degradation, never a hard dependency:** no `CORRALAI_EMBED_URL` → `embed.New()` returns nil → semantic skipped everywhere, BM25 floor, no error. Mirrors `internal/reference` today.
- **`internal/reference` public API stays identical** — keep `reference.Embedder`, `reference.NewEmbedder`, `reference.NewEmbedderFor` working (via alias + delegation) so its callers/tests are untouched.
- **No `memory.Open` signature change** — wire the embedder via a `SetEmbedder(*embed.Client)` setter (avoid churning the many `Open(path)` call sites/tests).
- **The markdown `.md` is the source of truth.** `author` lives in front-matter; the `embedding` vector lives ONLY in the derived DuckDB index (never in markdown).
- **Embeddings are preserved across re-index.** `Build` does `DELETE FROM mem` + re-INSERT from markdown on every `Add`; it MUST carry forward existing vectors for unchanged entries and embed only new/changed/missing ones — otherwise every write re-embeds the whole corpus.
- **Centralized embedder:** one `*embed.Client` constructed in `cmd/corral`, shared by `reference` and `memory`, so the whole heterogeneous swarm shares ONE vector space.
- The brain NEVER runs a model (embeddings go to a configured endpoint); the agent stays CGO-free (`CGO_ENABLED=0 go build ./cmd/corral-agent`) — this plan does not touch the agent binary's build.
- Demo embedder (verified): `CORRALAI_EMBED_MODEL=gemini-embedding-001`, endpoint `https://generativelanguage.googleapis.com/v1beta/openai/embeddings`, reuse the demo key.

---

## File Structure

- `internal/embed/embed.go` (create) — `Client` + `New`/`NewFor` + `Embed`; moved verbatim from reference.
- `internal/embed/embed_test.go` (create) — stub-server embed; nil-on-unset.
- `internal/reference/embed.go` (modify) — replace impl with `type Embedder = embed.Client` + delegating constructors.
- `internal/memory/store.go` (modify) — `author` + `embedding` columns/migrations; `entry.author`; `parseEntry`; `Build` (author + embed-preserving); `Add(author)`; `SetEmbedder`; `Hit.Author`/`Hit.Via`/`Entry.Author`; hybrid `Search`; `vecLiteral`.
- `internal/memory/store_test.go` (modify) — author, embedding-preservation, hybrid search, graceful degradation.
- `internal/brain/memory.go` (modify) — `add_memory` stamps `author = identity(req, in.Name)`.
- `internal/brain/memory_test.go` (modify) — author stamped + surfaced.
- `cmd/corral/main.go` (modify) — one `*embed.Client`, `memStore.SetEmbedder`, share with reference.
- `internal/ui/ask.go` (modify) — semantic memory recall in `buildTrail`, attributed.
- `deploy/demo/docker-compose.yml` (modify) — `CORRALAI_EMBED_*` on the brain.

---

## Task 1: Extract `internal/embed` (shared embedder)

**Files:**
- Create: `internal/embed/embed.go`, `internal/embed/embed_test.go`
- Modify: `internal/reference/embed.go`

**Interfaces:**
- Produces: `embed.Client` (opaque struct); `embed.New() *Client` (env: `CORRALAI_EMBED_URL`, `CORRALAI_EMBED_MODEL` default `text-embedding-3-small`, `CORRALAI_EMBED_KEY`; nil if URL unset); `embed.NewFor(url, model, key string) *Client` (nil if url==""); `(*Client).Embed(texts []string) ([][]float32, error)`.
- `internal/reference` re-exports: `type Embedder = embed.Client`, `NewEmbedder() *Embedder { return embed.New() }`, `NewEmbedderFor(url,model,key string) *Embedder { return embed.NewFor(url,model,key) }`.

- [ ] **Step 1: Write the failing test**

```go
// internal/embed/embed_test.go
package embed

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewForNilWhenNoURL(t *testing.T) {
	if NewFor("", "", "") != nil {
		t.Fatal("NewFor with empty url must return nil (graceful-degradation contract)")
	}
}

func TestEmbedRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Input []string `json:"input"` }
		json.NewDecoder(r.Body).Decode(&in)
		out := map[string]any{"data": []map[string]any{}}
		data := []map[string]any{}
		for range in.Input {
			data = append(data, map[string]any{"embedding": []float64{0.1, 0.2, 0.3}})
		}
		out["data"] = data
		json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()
	c := NewFor(srv.URL, "m", "")
	vecs, err := c.Embed([]string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 2 || len(vecs[0]) != 3 || vecs[0][0] != float32(0.1) {
		t.Fatalf("unexpected vecs: %v", vecs)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/embed/`
Expected: FAIL — package/`NewFor` undefined.

- [ ] **Step 3: Create `internal/embed/embed.go`**

Move the current `reference.Embedder` implementation verbatim, renamed `Client`. Copy the exact body of `Embedder`/`NewEmbedder`/`NewEmbedderFor`/`Embed` from `internal/reference/embed.go` (read that file), renaming the type to `Client` and the constructors to `New`/`NewFor`:

```go
// Package embed turns text into vectors via a configurable, OpenAI-compatible
// /v1/embeddings endpoint — shared by the reference (RAG) and memory (hive-mind)
// engines so the whole swarm embeds into ONE vector space. nil Client => disabled
// (no hard dependency); callers fall back to keyword search.
package embed

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type Client struct {
	url   string
	model string
	key   string
	httpc *http.Client
}

func New() *Client {
	return NewFor(os.Getenv("CORRALAI_EMBED_URL"), os.Getenv("CORRALAI_EMBED_MODEL"), os.Getenv("CORRALAI_EMBED_KEY"))
}

func NewFor(url, model, key string) *Client {
	if url == "" {
		return nil
	}
	if model == "" {
		model = "text-embedding-3-small"
	}
	return &Client{url: url, model: model, key: key, httpc: &http.Client{Timeout: 60 * time.Second}}
}

// Embed returns one vector per input text (order-preserving).
func (c *Client) Embed(texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	body, _ := json.Marshal(map[string]any{"model": c.model, "input": texts})
	req, err := http.NewRequest(http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 400))
		return nil, fmt.Errorf("embeddings endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var out struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode embeddings: %w", err)
	}
	if len(out.Data) != len(texts) {
		return nil, fmt.Errorf("expected %d embeddings, got %d", len(texts), len(out.Data))
	}
	vecs := make([][]float32, len(out.Data))
	for i, d := range out.Data {
		v := make([]float32, len(d.Embedding))
		for j, x := range d.Embedding {
			v[j] = float32(x)
		}
		vecs[i] = v
	}
	return vecs, nil
}
```

> Read `internal/reference/embed.go` first and match its exact `Embed` body (the conversion from `[]float64` to `[]float32` above mirrors it). If the original differs in any detail, prefer the original's behavior.

- [ ] **Step 4: Replace `internal/reference/embed.go` impl with the alias**

Delete the moved `Embedder` struct + `NewEmbedder`/`NewEmbedderFor`/`Embed` from `internal/reference/embed.go` and replace with:

```go
package reference

import "github.com/pdbethke/corralai/internal/embed"

// Embedder is the shared embed client; kept as an alias so existing reference
// callers/tests are unchanged after the extract to internal/embed.
type Embedder = embed.Client

func NewEmbedder() *Embedder { return embed.New() }

func NewEmbedderFor(url, model, key string) *Embedder { return embed.NewFor(url, model, key) }
```

Remove now-unused imports from `internal/reference/embed.go`. If `reference` code calls `embedder.Embed(...)`, it still works (alias).

- [ ] **Step 5: Run tests**

Run: `go test ./internal/embed/ ./internal/reference/ && go build ./...`
Expected: embed PASS; reference PASS (unchanged behavior); build OK.

- [ ] **Step 6: Commit**

```bash
git add internal/embed/ internal/reference/embed.go
git commit -m "refactor(embed): extract shared embed.Client from reference (alias preserved)"
```

---

## Task 2: memory — per-agent author attribution

**Files:**
- Modify: `internal/memory/store.go`
- Test: `internal/memory/store_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `entry.author`; `Hit.Author`, `Entry.Author`; `Add(name, body, description, typ, project, targetDir string, shared bool, author string)` (author is the NEW last param); author persisted to markdown front-matter and the `mem.author` column.

- [ ] **Step 1: Write the failing test**

```go
// internal/memory/store_test.go — add (uses the existing test store helper in this file)
func TestMemoryAuthorAttribution(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "m.duckdb"))
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { s.Close() })
	if _, _, _, err := s.Add("sql-injection-eval", "eval() on unsanitized input", "a vuln", "lesson", "default", filepath.Join(dir, "mem"), true, "Hawk"); err != nil {
		t.Fatal(err)
	}
	hits, err := s.Search("eval unsanitized", "", "", 10, false)
	if err != nil { t.Fatal(err) }
	if len(hits) == 0 || hits[0].Author != "Hawk" {
		t.Fatalf("want author Hawk on the hit, got %+v", hits)
	}
	e, err := s.Get("sql-injection-eval", false)
	if err != nil || e == nil || e.Author != "Hawk" {
		t.Fatalf("Get should carry author Hawk, got %+v (err %v)", e, err)
	}
}
```

> NOTE: read `internal/memory/store_test.go` for the exact temp-store construction the existing tests use; reuse it rather than duplicating. The `Add` call here uses the NEW 8-arg signature.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/memory/ -run TestMemoryAuthorAttribution`
Expected: FAIL — `Add` arg count / `Hit.Author` undefined.

- [ ] **Step 3: Add the column + struct fields + parsing + Build + Add**

In `internal/memory/store.go`:

(a) After the existing `shared`-column migration (`ALTER TABLE mem ADD COLUMN shared ...`), add:
```go
	_, _ = db.Exec("ALTER TABLE mem ADD COLUMN author VARCHAR DEFAULT ''")
```

(b) Add `author` to the `entry` struct:
```go
type entry struct {
	slug, name, project, typ, description, title, body, path, author string
	shared                                                           bool
}
```

(c) Add `Author` to `Hit` and `Entry` (both structs):
```go
	Author string `json:"author,omitempty"`
```

(d) In `parseEntry`, set author from front-matter (it's a plain string field, like name):
```go
	... author: str(fm["author"]), ...
```
(add `author: str(fm["author"])` to the returned `entry{...}` literal.)

(e) In `Build`, add the `author` column to the INSERT (it currently lists `(path,slug,name,project,type,description,title,body,shared)`):
```go
		stmt := "INSERT INTO mem (path,slug,name,project,type,description,title,body,shared,author) VALUES (" +
			lit(e.path) + "," + lit(e.slug) + "," + lit(e.name) + "," + lit(e.project) + "," +
			lit(e.typ) + "," + lit(e.description) + "," + lit(e.title) + "," + lit(e.body) + "," + bsql(e.shared) + "," + lit(e.author) + ")"
```

(f) In `Search` and `Get`, add `author` to the SELECT column list and scan it into `Hit.Author`/`Entry.Author`. (Both BM25 branches in `Search`, and the non-FTS fallback, must select `author`; scan an extra column.)

(g) `Add` gains the `author` param and writes it to front-matter when non-empty:
```go
func (s *Store) Add(name, body, description, typ, project, targetDir string, shared bool, author string) (slug, path, status string, err error) {
	...
	front := map[string]any{"name": name, "description": description, "project": project,
		"metadata": map[string]any{"type": typ}}
	if shared {
		front["shared"] = true
	}
	if author != "" {
		front["author"] = author
	}
	...
}
```

- [ ] **Step 4: Update existing `Add` callers in the memory package/tests**

Any existing call to `s.Add(...)` in `internal/memory/store_test.go` needs the new trailing `author` arg (pass `""` where attribution doesn't matter). Grep: `grep -rn '\.Add(' internal/memory/`.

- [ ] **Step 5: Run tests**

Run: `go test ./internal/memory/`
Expected: PASS (new test + existing, with the migration idempotent on re-open).

- [ ] **Step 6: Commit**

```bash
git add internal/memory/store.go internal/memory/store_test.go
git commit -m "feat(memory): per-agent author attribution (front-matter + column, survives re-index)"
```

---

## Task 3: memory — embedding column + embed-preserving Build + SetEmbedder

**Files:**
- Modify: `internal/memory/store.go`
- Test: `internal/memory/store_test.go`

**Interfaces:**
- Consumes: `embed.Client` (Task 1) — import `github.com/pdbethke/corralai/internal/embed`.
- Produces: `(*Store).SetEmbedder(*embed.Client)`; an `embedding FLOAT[]` column; `Build` carries forward existing vectors for unchanged entries and embeds new/changed/missing ones (only when an embedder is set); `vecLiteral([]float32) string` helper.

- [ ] **Step 1: Write the failing test (with a fake embedder via httptest)**

```go
// internal/memory/store_test.go — add
func TestMemoryEmbedOnBuildAndPreserve(t *testing.T) {
	embeds := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Input []string `json:"input"` }
		json.NewDecoder(r.Body).Decode(&in)
		embeds += len(in.Input)
		data := []map[string]any{}
		for range in.Input {
			data = append(data, map[string]any{"embedding": []float64{1, 0, 0}})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer srv.Close()

	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "m.duckdb"))
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { s.Close() })
	s.SetEmbedder(embed.NewFor(srv.URL, "m", ""))

	md := filepath.Join(dir, "mem")
	s.Add("one", "first body", "d", "note", "default", md, true, "Bob") // embeds 1
	first := embeds
	if first < 1 {
		t.Fatalf("expected an embed call on Add, got %d", embeds)
	}
	// Adding a SECOND entry triggers a full Build; the unchanged "one" must NOT be re-embedded.
	s.Add("two", "second body", "d", "note", "default", md, true, "Bob") // should embed only "two"
	if embeds != first+1 {
		t.Fatalf("re-index re-embedded unchanged entries: embeds went %d -> %d (want +1)", first, embeds)
	}
}

func TestMemoryNilEmbedderNoError(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "m.duckdb"))
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { s.Close() })
	// no SetEmbedder => embedder nil
	if _, _, _, err := s.Add("x", "body", "d", "note", "default", filepath.Join(dir, "mem"), true, ""); err != nil {
		t.Fatalf("Add with nil embedder must not error: %v", err)
	}
}
```

Add imports to the test file as needed: `encoding/json`, `net/http`, `net/http/httptest`, and `github.com/pdbethke/corralai/internal/embed`.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/memory/ -run 'TestMemoryEmbed|TestMemoryNilEmbedder'`
Expected: FAIL — `SetEmbedder` undefined.

- [ ] **Step 3: Implement column, setter, vecLiteral, and embed-preserving Build**

In `internal/memory/store.go`:

(a) Migration after the `author` one:
```go
	_, _ = db.Exec("ALTER TABLE mem ADD COLUMN embedding FLOAT[]")
```

(b) Add an embedder field + setter (add `embedder *embed.Client` to the `Store` struct):
```go
// SetEmbedder enables semantic indexing/search. nil keeps the store keyword-only.
func (s *Store) SetEmbedder(e *embed.Client) { s.embedder = e }
```

(c) Add the `vecLiteral` helper (copy from `internal/reference/store.go`):
```go
func vecLiteral(v []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, x := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(x), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}
```
(ensure `strconv` is imported.)

(d) Make `Build` preserve vectors and embed only new/changed entries. Replace the `Build` body's DELETE+INSERT region so that:
  1. BEFORE `DELETE FROM mem`, snapshot existing bodies+vectors:
```go
	type prev struct{ body, emb string }
	old := map[string]prev{}
	if rs, err := s.db.Query("SELECT path, body, CASE WHEN embedding IS NULL THEN '' ELSE embedding::VARCHAR END FROM mem"); err == nil {
		for rs.Next() {
			var p, b, em string
			if rs.Scan(&p, &b, &em) == nil {
				old[p] = prev{body: b, emb: em}
			}
		}
		rs.Close()
	}
```
  2. In the INSERT, include the `embedding` column, carrying forward the old vector when the body is unchanged (else NULL):
```go
		embExpr := "NULL"
		if pv, ok := old[e.path]; ok && pv.body == e.body && pv.emb != "" {
			embExpr = pv.emb + "::FLOAT[]"
		}
		stmt := "INSERT INTO mem (path,slug,name,project,type,description,title,body,shared,author,embedding) VALUES (" +
			lit(e.path) + "," + lit(e.slug) + "," + lit(e.name) + "," + lit(e.project) + "," +
			lit(e.typ) + "," + lit(e.description) + "," + lit(e.title) + "," + lit(e.body) + "," +
			bsql(e.shared) + "," + lit(e.author) + "," + embExpr + ")"
```
  3. AFTER the commit + FTS reindex, if `s.embedder != nil`, embed rows whose embedding is still NULL (new/changed) in one batch and UPDATE them:
```go
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
			rs.Close()
		}
		if len(texts) > 0 {
			if vecs, err := s.embedder.Embed(texts); err != nil {
				log.Printf("memory: embed %d entries: %v (keyword search still works)", len(texts), err)
			} else {
				for i, p := range paths {
					if i < len(vecs) {
						s.db.Exec("UPDATE mem SET embedding = "+vecLiteral(vecs[i])+"::FLOAT[] WHERE path = ?", p)
					}
				}
			}
		}
	}
```
(ensure `log` is imported.)

- [ ] **Step 4: Run tests**

Run: `go test ./internal/memory/`
Expected: PASS — embed-on-Add fires; the second Add does NOT re-embed the unchanged first entry (preservation); nil embedder errors nowhere.

- [ ] **Step 5: Commit**

```bash
git add internal/memory/store.go internal/memory/store_test.go
git commit -m "feat(memory): embedding column + embed-preserving Build + SetEmbedder"
```

---

## Task 4: memory — hybrid Search (BM25 ⊕ cosine)

**Files:**
- Modify: `internal/memory/store.go`
- Test: `internal/memory/store_test.go`

**Interfaces:**
- Consumes: `embedder` field + `vecLiteral` (Task 3); BM25 `Search` (existing).
- Produces: `Hit.Via string` ("semantic"|"keyword"|"both"); `Search` returns a merged, deduped, ranked list when an embedder is set, else the BM25 list unchanged.

- [ ] **Step 1: Write the failing test**

```go
// internal/memory/store_test.go — add
func TestMemoryHybridSemantic(t *testing.T) {
	// fake embedder: "vuln" query and the eval() entry get the SAME vector (semantic
	// match) though they share NO keywords; an unrelated entry gets an orthogonal vector.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Input []string `json:"input"` }
		json.NewDecoder(r.Body).Decode(&in)
		data := []map[string]any{}
		for _, s := range in.Input {
			v := []float64{0, 1} // default orthogonal
			if strings.Contains(s, "eval") || strings.Contains(s, "injection risk") {
				v = []float64{1, 0} // the vuln cluster
			}
			data = append(data, map[string]any{"embedding": v})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer srv.Close()
	dir := t.TempDir()
	s, _ := Open(filepath.Join(dir, "m.duckdb"))
	t.Cleanup(func() { s.Close() })
	s.SetEmbedder(embed.NewFor(srv.URL, "m", ""))
	md := filepath.Join(dir, "mem")
	s.Add("eval-danger", "calling eval() on user input", "d", "lesson", "default", md, true, "Hawk")
	s.Add("color-prefs", "the UI uses a warm palette", "d", "note", "default", md, true, "Iris")

	// Query shares NO keywords with the eval entry, but is semantically the vuln cluster.
	hits, err := s.Search("injection risk", "", "", 5, false)
	if err != nil { t.Fatal(err) }
	if len(hits) == 0 || hits[0].Slug != "eval-danger" {
		t.Fatalf("semantic arm should surface eval-danger first, got %+v", hits)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/memory/ -run TestMemoryHybridSemantic`
Expected: FAIL — BM25-only `Search` won't rank `eval-danger` for the keyword-disjoint query (or `Via` undefined).

- [ ] **Step 3: Implement hybrid Search**

In `internal/memory/store.go`:
(a) Add `Via string` to `Hit`.
(b) Refactor `Search` so the existing BM25 logic becomes an internal helper returning `[]Hit` (each tagged `Via:"keyword"`). Then:
```go
func (s *Store) Search(query, scope, typ string, limit int, sharedOnly bool) ([]Hit, error) {
	kw, err := s.searchKeyword(query, scope, typ, limit, sharedOnly) // existing BM25 body, Via="keyword"
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
	sem, err := s.searchSemantic(vecs[0], scope, typ, limit, sharedOnly) // cosine, Via="semantic"
	if err != nil {
		return kw, nil
	}
	return mergeHits(kw, sem, limit), nil
}
```
(c) `searchSemantic` runs cosine over rows that have a vector:
```go
func (s *Store) searchSemantic(qv []float32, scope, typ string, limit int, sharedOnly bool) ([]Hit, error) {
	where := "embedding IS NOT NULL"
	args := []any{}
	if sharedOnly { where += " AND shared = TRUE" }
	if scope != "" { where += " AND project = ?"; args = append(args, scope) }
	if typ != "" { where += " AND type = ?"; args = append(args, typ) }
	q := "SELECT slug,name,project,type,description,shared,author, list_cosine_similarity(embedding, " +
		vecLiteral(qv) + "::FLOAT[]) AS score FROM mem WHERE " + where + " ORDER BY score DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil { return nil, err }
	defer rows.Close()
	out := []Hit{}
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.Slug,&h.Name,&h.Project,&h.Type,&h.Description,&h.Shared,&h.Author,&h.Score); err != nil {
			return nil, err
		}
		h.Via = "semantic"
		out = append(out, h)
	}
	return out, rows.Err()
}
```
(d) `mergeHits` normalizes each list's scores to [0,1], unions by slug taking the max normalized score, tags `both` when a slug appears in both, sorts desc, caps at limit:
```go
func mergeHits(kw, sem []Hit, limit int) []Hit {
	norm := func(hs []Hit) {
		if len(hs) == 0 { return }
		max := hs[0].Score
		for _, h := range hs { if h.Score > max { max = h.Score } }
		if max <= 0 { return }
		for i := range hs { hs[i].Score = hs[i].Score / max }
	}
	cp := func(hs []Hit) []Hit { c := make([]Hit, len(hs)); copy(c, hs); return c }
	k, m := cp(kw), cp(sem)
	norm(k); norm(m)
	by := map[string]Hit{}
	add := func(h Hit) {
		if e, ok := by[h.Slug]; ok {
			if h.Score > e.Score { e.Score = h.Score }
			e.Via = "both"
			by[h.Slug] = e
		} else {
			by[h.Slug] = h
		}
	}
	for _, h := range k { add(h) }
	for _, h := range m { add(h) }
	out := make([]Hit, 0, len(by))
	for _, h := range by { out = append(out, h) }
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if limit > 0 && len(out) > limit { out = out[:limit] }
	return out
}
```
(ensure `sort` is imported.)

> The existing BM25 body in `Search` moves verbatim into `searchKeyword` with `Via:"keyword"` set on each hit and `author` added to its SELECT/scan (from Task 2).

- [ ] **Step 4: Run tests**

Run: `go test ./internal/memory/`
Expected: PASS — semantic surfaces the keyword-disjoint match; existing keyword tests still pass; nil-embedder path returns BM25 unchanged.

- [ ] **Step 5: Commit**

```bash
git add internal/memory/store.go internal/memory/store_test.go
git commit -m "feat(memory): hybrid BM25+cosine Search with merge/dedupe and Via tags"
```

---

## Task 5: brain `add_memory` stamps author + agent carries it + `cmd/corral` wires the shared embedder + demo env

**Files:**
- Modify: `internal/brain/memory.go`, `internal/brain/memory_test.go`, `cmd/corral-agent/main.go`, `cmd/corral/main.go`, `deploy/demo/docker-compose.yml`

**Interfaces:**
- Consumes: `memory.Add(...author)` (Task 2); `memory.SetEmbedder` (Task 3); `embed.New()` (Task 1); `identity(req, fallback)`.
- Key point: for `add_memory`, `in.Name` is the entry SLUG, not the agent. The agent's `brain()` closure (which already skips stamping `name` for `add_memory`) gains a special case that stamps `args["author"] = <agent name>`. The brain handler then uses `author := identity(req, in.Author)` — authoritative principal/subagent in auth mode, the agent-supplied name in dev. `addIn` gains an `Author` field.

- [ ] **Step 1: Write the failing test (concrete)**

Append to `internal/brain/memory_test.go` (it already imports `context`, `encoding/json`, `path/filepath`, `testing`, `mcp`, `coord`, `memory`; add `strings`):

```go
func TestAddMemoryStampsAuthor(t *testing.T) {
	root := t.TempDir()
	cstore, err := coord.Open(filepath.Join(root, "c.sqlite3"))
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { cstore.Close() })
	mstore, err := memory.Open(filepath.Join(root, "m.duckdb"))
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { mstore.Close() })

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() { _ = NewServer(cstore, mstore, Options{}).Run(ctx, serverT) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "smoke", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil { t.Fatal(err) }
	defer sess.Close()

	// The agent's brain() closure stamps "author"=<agent name> for add_memory; here
	// we pass it directly. "name" is the entry SLUG (not the agent).
	if _, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "add_memory", Arguments: map[string]any{
		"name": "eval-vuln", "author": "Hawk", "body": "eval() on unsanitized parser input",
		"description": "a vuln", "type": "lesson", "shared": true,
	}}); err != nil { t.Fatal(err) }

	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "search_memory", Arguments: map[string]any{"query": "eval unsanitized"}})
	if err != nil { t.Fatal(err) }
	b, _ := json.Marshal(res.StructuredContent)
	if !strings.Contains(string(b), `"author":"Hawk"`) {
		t.Fatalf("search_memory hit should carry author Hawk: %s", string(b))
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/brain/ -run TestAddMemoryStampsAuthor`
Expected: FAIL — author empty (not yet stamped).

- [ ] **Step 3: Add the `Author` field + stamp it in the handler**

In `internal/brain/memory.go`, add to the `addIn` struct:
```go
	Author string `json:"author,omitempty" jsonschema:"the agent recording this (stamped by your client); the brain takes the authoritative identity when authenticated"`
```
And change the `add_memory` handler call from
`mem.Add(in.Name, in.Body, in.Description, in.Type, in.Project, "", in.Shared)` to:
```go
			author := identity(req, in.Author) // authoritative in auth mode; agent-supplied name in dev
			slug, path, status, err := mem.Add(in.Name, in.Body, in.Description, in.Type, in.Project, "", in.Shared, author)
```

- [ ] **Step 4: Make the agent carry its name on add_memory**

In `cmd/corral-agent/main.go`, the `brain()` closure currently does:
```go
		if tool != "add_memory" && tool != "spawn_subagent" {
			args["name"] = name
		}
```
Add, right after that block, a special case so add_memory carries the author (its
`name` stays the entry slug the model chose):
```go
		if tool == "add_memory" {
			args["author"] = name // attribute the entry to this agent (the hive-mind "who")
		}
```

- [ ] **Step 5: Run tests + CGO-free check (the agent was touched)**

Run: `go test ./internal/brain/ && CGO_ENABLED=0 go build ./cmd/corral-agent`
Expected: PASS; agent builds CGO-free.

- [ ] **Step 6: Wire one shared embedder in cmd/corral**

In `cmd/corral/main.go`, where `embedder := reference.NewEmbedder()` exists (~line 337), construct the shared client once and give it to the memory store too. `reference.Embedder` is now an alias for `embed.Client`, so one value serves both:
```go
	embedder := embed.New() // one client → one vector space for reference AND memory
	memStore.SetEmbedder(embedder)
```
- Replace the `reference.NewEmbedder()` call with `embed.New()` and pass `embedder` wherever `Options.Embedder` is set (its type `*reference.Embedder` == `*embed.Client`, so it assigns directly).
- Add the import `github.com/pdbethke/corralai/internal/embed`.
- `memStore.SetEmbedder(embedder)` must be called AFTER `memStore` is opened and BEFORE the startup `memStore.Build(nil)` (so the initial index embeds the seeded corpus).

- [ ] **Step 7: Demo env**

In `deploy/demo/docker-compose.yml`, on the `brain` service `environment:` block, add:
```yaml
      CORRALAI_EMBED_URL: ${CORRALAI_EMBED_URL:-https://generativelanguage.googleapis.com/v1beta/openai/embeddings}
      CORRALAI_EMBED_MODEL: ${CORRALAI_EMBED_MODEL:-gemini-embedding-001}
      CORRALAI_EMBED_KEY: ${CORRALAI_EMBED_KEY:-${OPENAI_API_KEY:-}}
```

- [ ] **Step 8: Build + commit**

Run: `go build ./... && go test ./internal/brain/ ./internal/memory/ && CGO_ENABLED=0 go build ./cmd/corral-agent`
Expected: build OK; tests PASS; agent CGO-free.

```bash
git add internal/brain/memory.go internal/brain/memory_test.go cmd/corral-agent/main.go cmd/corral/main.go deploy/demo/docker-compose.yml
git commit -m "feat(brain): stamp add_memory author (agent carries it); share one embedder; demo embed env"
```

---

## Task 6: narrator — semantic memory recall in the ask-a-bee debrief

**Files:**
- Modify: `internal/ui/ask.go`
- Test: `internal/ui/ask_test.go` (create) OR add to an existing ui test file

**Interfaces:**
- Consumes: `s.mem` (the UI server already holds `*memory.Store`) whose `Search` is now hybrid (Tasks 3–5); `s.narrator`/`buildTrail` (existing).

- [ ] **Step 1: Write the failing test**

```go
// internal/ui/ask_test.go
package ui

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/memory"
)

func TestBuildTrailIncludesAttributedMemories(t *testing.T) {
	dir := t.TempDir()
	mem, err := memory.Open(filepath.Join(dir, "m.duckdb"))
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { mem.Close() })
	// No SetEmbedder => keyword path (graceful degradation); the slug still matches.
	mem.Add("eval-vuln", "eval() on unsanitized parser input", "a vuln", "lesson", "default", filepath.Join(dir, "mem"), true, "Hawk")

	s := &Server{mem: mem}
	trail := s.buildTrail("Hawk", "pentester", "eval unsanitized parser")
	if !strings.Contains(trail, "eval-vuln") || !strings.Contains(trail, "your own") {
		t.Fatalf("trail should include Hawk's own memory flagged 'your own':\n%s", trail)
	}
}
```

> `buildTrail` currently has the signature `func (s *Server) buildTrail(agent, role string) string` and is called at `internal/ui/ask.go:39` as `trail := s.buildTrail(agent, role)`. Change the signature to `buildTrail(agent, role, question string)` and update that one call site to `s.buildTrail(agent, role, body.Question)` (the `ask` handler already has `body.Question`).

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/ui/ -run TestBuildTrailIncludesAttributedMemories`
Expected: FAIL — memories not in the trail.

- [ ] **Step 3: Add the memory recall to the trail**

In `internal/ui/ask.go`, change `buildTrail`'s signature to `func (s *Server) buildTrail(agent, role, question string) string`, update the call site at line 39 to `s.buildTrail(agent, role, body.Question)`, and append a memory section after the existing trail sections:
```go
	// Hive-mind recall: semantically (hybrid) search the WHOLE corpus for the
	// question, flag the asked agent's own notes vs the hive's. mem.Search is hybrid
	// when an embedder is configured, keyword otherwise (graceful).
	if s.mem != nil && strings.TrimSpace(question) != "" {
		if hits, err := s.mem.Search(question, "", "", 5, false); err == nil && len(hits) > 0 {
			var lines []string
			for _, h := range hits {
				who := "hive: " + h.Author
				if h.Author == agent {
					who = "your own"
				} else if h.Author == "" {
					who = "hive"
				}
				lines = append(lines, "  "+h.Slug+" ("+who+") — "+oneLine(h.Description))
			}
			b.WriteString("RELEVANT MEMORIES (hive-mind recall):\n" + strings.Join(lines, "\n") + "\n")
		}
	}
```
Thread `question` into the trail builder and pass it from `ask` (which has `body.Question`). Keep `agent` = the asked agent name (so `h.Author == agent` flags "your own").

- [ ] **Step 4: Run tests**

Run: `go test ./internal/ui/`
Expected: PASS — Hawk's own memory appears in the grounding, flagged "your own".

- [ ] **Step 5: Commit**

```bash
git add internal/ui/ask.go internal/ui/ask_test.go
git commit -m "feat(ask): narrator recalls the hive-mind — semantic memory in the debrief, attributed"
```

---

## Final verification

- [ ] `go build ./...` — OK
- [ ] `go test ./...` — all PASS
- [ ] `CGO_ENABLED=0 go build ./cmd/corral-agent` — agent stays CGO-free (this plan didn't touch its build, but confirm)
- [ ] Graceful degradation: unset `CORRALAI_EMBED_URL` and confirm `memory` + brain tests still pass (keyword-only path).

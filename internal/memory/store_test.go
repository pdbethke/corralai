// SPDX-License-Identifier: Elastic-2.0

package memory

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/annindex"
	"github.com/pdbethke/corralai/internal/embed"
)

func TestMemoryAuthorAttribution(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "m.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, _, _, err := s.Add("sql-injection-eval", "eval() on unsanitized input", "a vuln", "lesson", "default", filepath.Join(dir, "mem"), true, "Hawk"); err != nil {
		t.Fatal(err)
	}
	hits, err := s.Search("eval unsanitized", "", "", 10, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].Author != "Hawk" {
		t.Fatalf("want author Hawk on the hit, got %+v", hits)
	}
	e, err := s.Get("sql-injection-eval", false)
	if err != nil || e == nil || e.Author != "Hawk" {
		t.Fatalf("Get should carry author Hawk, got %+v (err %v)", e, err)
	}
}

// writeEntry creates a markdown memory file with frontmatter.
func writeEntry(t *testing.T, dir, slug, desc, project, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	fm := "---\nname: " + slug + "\ndescription: \"" + desc + "\"\n"
	if project != "" {
		fm += "project: " + project + "\n"
	}
	fm += "metadata:\n  type: reference\n---\n\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, slug+".md"), []byte(fm), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryBuildSearchScope(t *testing.T) {
	root := t.TempDir()
	// encoded dir contains "alpha" => mapped to the "alpha" tier by an env rule.
	alphaMem := filepath.Join(root, "x-alpha", "memory")
	writeEntry(t, alphaMem, "feedback_widget_thing", "the widget renders a sprocket", "", "The widget draws a sprocket using the gizmo pipeline.")
	// front-matter project: always wins over the path rule.
	writeEntry(t, alphaMem, "beta-combat-lore", "combat resolves via the engine", "beta", "Beta combat uses resolve_attack.")

	s, err := Open(filepath.Join(root, "m.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.SetProjectTiers("alpha=alpha,gamma=gamma")
	n, err := s.Build([]string{alphaMem})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if n != 2 {
		t.Fatalf("want 2 entries, got %d", n)
	}
	t.Logf("FTS available: %v", s.FTS())

	// search within the alpha tier finds the widget (classified by path rule)
	hits, err := s.Search("sprocket gizmo", "alpha", "", 5, false)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) == 0 || hits[0].Slug != "feedback_widget_thing" {
		t.Fatalf("expected feedback_widget_thing top hit, got %+v", hits)
	}

	// tier isolation: same query scoped to beta returns nothing
	bh, err := s.Search("sprocket gizmo", "beta", "", 5, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(bh) != 0 {
		t.Fatalf("beta scope should not see the alpha entry: %+v", bh)
	}

	// front-matter project: beta wins over the alpha path rule
	bl, err := s.List("beta", "", 50, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(bl) != 1 || bl[0].Slug != "beta-combat-lore" {
		t.Fatalf("beta list should be [beta-combat-lore], got %+v", bl)
	}

	// get returns the full body
	e, err := s.Get("feedback_widget_thing", false)
	if err != nil || e == nil {
		t.Fatalf("get: %v %v", e, err)
	}
	if e.Project != "alpha" || e.Body == "" {
		t.Fatalf("unexpected entry: %+v", e)
	}

	st, _ := s.Stats(false)
	if st.Total != 2 || st.ByProject["alpha"] != 1 || st.ByProject["beta"] != 1 {
		t.Fatalf("stats: %+v", st)
	}
}

// TestProjectTierFallback: with no rules and no front-matter, entries land in "default".
func TestProjectTierFallback(t *testing.T) {
	root := t.TempDir()
	mem := filepath.Join(root, "x-whatever", "memory")
	writeEntry(t, mem, "note_one", "a plain note", "", "Just a note.")
	s, err := Open(filepath.Join(root, "m.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Build([]string{mem}); err != nil {
		t.Fatal(err)
	}
	e, err := s.Get("note_one", false)
	if err != nil || e == nil {
		t.Fatalf("get: %v %v", e, err)
	}
	if e.Project != "default" {
		t.Fatalf("want default tier, got %q", e.Project)
	}
}

func TestMemoryEmbedOnBuildAndPreserve(t *testing.T) {
	embeds := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Input []string `json:"input"`
		}
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
	if err != nil {
		t.Fatal(err)
	}
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
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	// no SetEmbedder => embedder nil
	if _, _, _, err := s.Add("x", "body", "d", "note", "default", filepath.Join(dir, "mem"), true, ""); err != nil {
		t.Fatalf("Add with nil embedder must not error: %v", err)
	}
}

func TestMemoryHybridSemantic(t *testing.T) {
	// fake embedder: "vuln" query and the eval() entry get the SAME vector (semantic
	// match) though they share NO keywords; an unrelated entry gets an orthogonal vector.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Input []string `json:"input"`
		}
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
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].Slug != "eval-danger" {
		t.Fatalf("semantic arm should surface eval-danger first, got %+v", hits)
	}
}

func TestSharedVisibility(t *testing.T) {
	root := t.TempDir()
	mem := filepath.Join(root, "x-alpha", "memory")
	if err := os.MkdirAll(mem, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(mem, "team-guide.md"), []byte("---\nname: team-guide\nshared: true\n---\nhow we deploy"), 0o644)
	os.WriteFile(filepath.Join(mem, "private-note.md"), []byte("---\nname: private-note\n---\nmy secret"), 0o644)
	s, err := Open(filepath.Join(root, "m.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Build([]string{mem}); err != nil {
		t.Fatal(err)
	}
	all, _ := s.List("", "", 100, false)   // owner sees all
	shared, _ := s.List("", "", 100, true) // teammate sees shared only
	if len(all) != 2 {
		t.Fatalf("owner must see 2, got %d", len(all))
	}
	if len(shared) != 1 || !shared[0].Shared || shared[0].Slug != "team-guide" {
		t.Fatalf("teammate must see only the shared entry, got %v", shared)
	}
	if e, _ := s.Get("private-note", true); e != nil {
		t.Error("teammate must not read a private entry even by exact name")
	}
	if e, _ := s.Get("private-note", false); e == nil {
		t.Error("owner must read private entries")
	}
	if ok, err := s.SetShared("private-note", true); !ok || err != nil {
		t.Fatalf("promote failed: ok=%v err=%v", ok, err)
	}
	if shared2, _ := s.List("", "", 100, true); len(shared2) != 2 {
		t.Fatalf("after promote teammate must see 2, got %d", len(shared2))
	}
	s.SetShared("team-guide", false) // unshare
	if shared3, _ := s.List("", "", 100, true); len(shared3) != 1 {
		t.Fatalf("after unshare teammate must see 1, got %d", len(shared3))
	}
}

// writeANNEntry writes a memory markdown file with project, type, shared and body
// fields — used by the ANN filter tests that need full frontmatter control.
func writeANNEntry(t *testing.T, dir, slug, project, typ string, shared bool, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	fm := "---\nname: " + slug + "\n"
	if project != "" {
		fm += "project: " + project + "\n"
	}
	fm += "metadata:\n  type: " + typ + "\n"
	if shared {
		fm += "shared: true\n"
	}
	fm += "---\n\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, slug+".md"), []byte(fm), 0o644); err != nil {
		t.Fatal(err)
	}
}

// annVec returns a deterministic 4-dim embedding vector based on keywords in text.
// The vectors are designed so that, when filtered to project=alpha / type=reference /
// sharedOnly=true, the ranking is: alpha-top > alpha-mid > alpha-low.
// Entries that would rank higher WITHOUT the filter (beta-top, alpha-private,
// alpha-note) have vectors that are very close to the query, which proves the
// filter is being enforced and not just luck of the overfetch.
func annVec(text string) []float32 {
	switch {
	// Query sentinel checked before "alpha-top" since it contains that substring.
	case strings.Contains(text, "queryfor-alpha-top"):
		return []float32{1, 0, 0, 0}
	// High-similarity entries that must be excluded by the filter.
	case strings.Contains(text, "beta-top"):
		return []float32{0.999, 0.01, 0, 0} // wrong project
	case strings.Contains(text, "alpha-private"):
		return []float32{0.98, 0.02, 0, 0} // shared=false
	case strings.Contains(text, "alpha-note"):
		return []float32{0.94, 0.05, 0, 0} // wrong type
	// Entries that pass the filter, ordered by closeness to the query.
	case strings.Contains(text, "alpha-top"):
		return []float32{0.95, 0.1, 0, 0}
	case strings.Contains(text, "alpha-mid"):
		return []float32{0.7, 0.5, 0, 0}
	case strings.Contains(text, "alpha-low"):
		return []float32{0.5, 0.7, 0, 0}
	default:
		return []float32{0, 0, 1, 0} // orthogonal to query
	}
}

// TestMemoryANNFilteredAgreesWithBruteforce seeds > threshold entries with a mix
// of shared/private, projects, and types. After Build it asserts that a filtered
// searchSemantic call returns the SAME results via the HNSW path as via the
// brute-force path — including exclusion of entries that would rank higher without
// the filter.
func TestMemoryANNFilteredAgreesWithBruteforce(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Input []string `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		data := []map[string]any{}
		for _, text := range in.Input {
			v := annVec(text)
			fv := make([]float64, len(v))
			for i, x := range v {
				fv[i] = float64(x)
			}
			data = append(data, map[string]any{"embedding": fv})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer srv.Close()

	root := t.TempDir()
	dbPath := filepath.Join(root, "ann.duckdb")
	memDir := filepath.Join(root, "mem")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	// Inject a low threshold so our 10-entry corpus triggers HNSW.
	s.annCfg = annindex.Config{Threshold: 3}
	s.SetEmbedder(embed.NewFor(srv.URL, "fake", ""))

	// Entries that pass filter (alpha, reference, shared=true) — expected top-3.
	writeANNEntry(t, memDir, "alpha-top", "alpha", "reference", true, "body alpha-top")
	writeANNEntry(t, memDir, "alpha-mid", "alpha", "reference", true, "body alpha-mid")
	writeANNEntry(t, memDir, "alpha-low", "alpha", "reference", true, "body alpha-low")
	// Entries that should be excluded by the filter but rank higher without it.
	writeANNEntry(t, memDir, "beta-top", "beta", "reference", true, "body beta-top")
	writeANNEntry(t, memDir, "alpha-private", "alpha", "reference", false, "body alpha-private")
	writeANNEntry(t, memDir, "alpha-note", "alpha", "note", true, "body alpha-note")
	// Filler to reach above threshold (already at 6, but add a few more for clarity).
	for i := 0; i < 4; i++ {
		slug := "other-" + strings.Repeat("x", i+1)
		writeANNEntry(t, memDir, slug, "other", "reference", false, "body "+slug)
	}

	n, err := s.Build([]string{memDir})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if n != 10 {
		t.Fatalf("expected 10 entries, got %d", n)
	}

	if !s.annActive {
		t.Skip("vss extension not available in this environment — HNSW test requires vss")
	}

	// Assert the HNSW index exists on the mem table.
	var idxCnt int
	if err := s.db.QueryRow(
		`SELECT count(*) FROM duckdb_indexes WHERE index_name='mem_hnsw' AND table_name='mem'`,
	).Scan(&idxCnt); err != nil {
		t.Fatalf("duckdb_indexes query: %v", err)
	}
	if idxCnt == 0 {
		t.Fatal("expected mem_hnsw HNSW index to exist after Build above threshold")
	}

	// Embed the query vector once and share it between both paths.
	qvecs, err := s.embedder.Embed([]string{"queryfor-alpha-top"})
	if err != nil || len(qvecs) == 0 {
		t.Fatalf("embed query: %v", err)
	}
	qv := qvecs[0]

	// ANN path (annActive=true — already set from Build).
	annHits, err := s.searchSemantic(qv, "alpha", "reference", 3, true)
	if err != nil {
		t.Fatalf("HNSW searchSemantic: %v", err)
	}

	// Brute-force path: temporarily disable HNSW and run the same query.
	s.annActive = false
	bfHits, err := s.searchSemantic(qv, "alpha", "reference", 3, true)
	s.annActive = true // restore
	if err != nil {
		t.Fatalf("brute-force searchSemantic: %v", err)
	}

	// Results must agree: same count, same slugs in the same rank order.
	if len(annHits) != len(bfHits) {
		t.Fatalf("ANN returned %d hits, brute-force returned %d", len(annHits), len(bfHits))
	}
	if len(annHits) != 3 {
		t.Fatalf("expected 3 filtered hits, got %d (ann) / %d (bf)", len(annHits), len(bfHits))
	}
	for i := range annHits {
		if annHits[i].Slug != bfHits[i].Slug {
			t.Errorf("rank %d: ANN slug=%q, brute-force slug=%q", i, annHits[i].Slug, bfHits[i].Slug)
		}
	}
	// Confirm the top hit is alpha-top and no excluded entries leaked through.
	if annHits[0].Slug != "alpha-top" {
		t.Errorf("expected alpha-top as top hit, got %q", annHits[0].Slug)
	}
	excluded := map[string]bool{"beta-top": true, "alpha-private": true, "alpha-note": true}
	for _, h := range annHits {
		if excluded[h.Slug] {
			t.Errorf("filter violation: excluded entry %q appeared in ANN results", h.Slug)
		}
	}
}

// TestMemoryDisableSwitch verifies that Config{Disabled:true} forces brute-force
// even when the corpus is above threshold — annActive stays false after Build,
// and search still returns correct results.
func TestMemoryDisableSwitch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Input []string `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		data := []map[string]any{}
		for _, text := range in.Input {
			v := annVec(text)
			fv := make([]float64, len(v))
			for i, x := range v {
				fv[i] = float64(x)
			}
			data = append(data, map[string]any{"embedding": fv})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer srv.Close()

	root := t.TempDir()
	dbPath := filepath.Join(root, "disabled.duckdb")
	memDir := filepath.Join(root, "mem")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	// Disabled=true forces brute-force regardless of row count.
	s.annCfg = annindex.Config{Threshold: 3, Disabled: true}
	s.SetEmbedder(embed.NewFor(srv.URL, "fake", ""))

	writeANNEntry(t, memDir, "alpha-top", "alpha", "reference", true, "body alpha-top")
	writeANNEntry(t, memDir, "alpha-mid", "alpha", "reference", true, "body alpha-mid")
	writeANNEntry(t, memDir, "alpha-low", "alpha", "reference", true, "body alpha-low")
	writeANNEntry(t, memDir, "beta-top", "beta", "reference", true, "body beta-top")
	writeANNEntry(t, memDir, "alpha-private", "alpha", "reference", false, "body alpha-private")

	if _, err := s.Build([]string{memDir}); err != nil {
		t.Fatalf("Build: %v", err)
	}

	// With Disabled=true, HNSW must NOT be activated even though corpus > threshold.
	if s.annActive {
		t.Error("expected annActive=false when Config.Disabled=true, but got true")
	}

	// Search must still work correctly via brute-force.
	hits, err := s.Search("queryfor-alpha-top", "alpha", "reference", 3, true)
	if err != nil {
		t.Fatalf("Search with disabled ANN: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected hits from brute-force search with disabled ANN")
	}
	// alpha-top should be the top semantic hit (it has the closest vector to the query).
	found := false
	for _, h := range hits {
		if h.Slug == "alpha-top" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected alpha-top in results, got %+v", hits)
	}
}

// TestMemoryDirEnvOverrideAfterProcessStart proves the default-target-dir
// resolution used by Add(targetDir="") is lazy: CORRALAI_MEMORY_DIR set via
// t.Setenv (i.e. AFTER process/package init has already run) still redirects
// writes. This is the seam that broke real ~/.claude/projects/default/memory
// isolation: a package-level var resolved at init time captures the env var
// before any test's t.Setenv takes effect, so tests calling Add with
// targetDir="" silently wrote into the developer's real memory dir. Must be
// the first caller of memoryDir() in this test binary — every other test in
// this package passes an explicit targetDir to Add.
func TestMemoryDirEnvOverrideAfterProcessStart(t *testing.T) {
	root := t.TempDir()
	want := filepath.Join(root, "redirected-mem")
	t.Setenv("CORRALAI_MEMORY_DIR", want)

	got := memoryDir()
	if got != want {
		t.Fatalf("memoryDir() = %q, want %q (CORRALAI_MEMORY_DIR set after process start must still be honored)", got, want)
	}

	dbPath := filepath.Join(root, "m.duckdb")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	if _, path, _, err := s.Add("env-redirect-check", "body", "d", "note", "default", "", true, "tester"); err != nil {
		t.Fatal(err)
	} else if !strings.HasPrefix(path, want) {
		t.Fatalf("Add wrote to %q, want under %q", path, want)
	}

	if _, err := os.Stat(filepath.Join(want, "env-redirect-check.md")); err != nil {
		t.Fatalf("expected entry file under redirected dir: %v", err)
	}
}

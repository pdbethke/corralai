// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/coord"
	"github.com/pdbethke/corralai/internal/principals"
	"github.com/pdbethke/corralai/internal/reference"
)

// fakeEmbed serves a constant unit vector per input — enough to exercise the
// add → store → search plumbing (ranking correctness is tested in the reference
// package itself).
func fakeEmbed(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		var out struct {
			Data []struct {
				Embedding []float64 `json:"embedding"`
			} `json:"data"`
		}
		for range req.Input {
			out.Data = append(out.Data, struct {
				Embedding []float64 `json:"embedding"`
			}{Embedding: []float64{1, 0, 0}})
		}
		json.NewEncoder(w).Encode(out)
	}))
}

func TestReferenceToolsOverMCP(t *testing.T) {
	dir := t.TempDir()
	cstore, _ := coord.Open(filepath.Join(dir, "c.sqlite3"))
	t.Cleanup(func() { cstore.Close() })
	refStore, err := reference.Open(filepath.Join(dir, "ref.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { refStore.Close() })
	es := fakeEmbed(t)
	defer es.Close()
	embedder := reference.NewEmbedderFor(es.URL, "m", "")

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() { _ = NewServer(cstore, nil, Options{Reference: refStore, Embedder: embedder}).Run(ctx, serverT) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sess.Close()

	// add_reference (text) → chunks stored.
	var added addReferenceOut
	callTask(t, sess, "add_reference", map[string]any{"source": "spec.md", "text": "the scores come from the live football data API"}, &added)
	if added.Chunks < 1 || added.Source != "spec.md" {
		t.Fatalf("add_reference returned %+v", added)
	}

	// list_references → the source shows up.
	var listed listReferencesOut
	callTask(t, sess, "list_references", map[string]any{}, &listed)
	if len(listed.Sources) != 1 || listed.Sources[0].Source != "spec.md" {
		t.Fatalf("list_references = %+v", listed.Sources)
	}

	// search_reference → returns the ingested chunk.
	var found searchReferenceOut
	callTask(t, sess, "search_reference", map[string]any{"query": "where do scores come from", "k": 3}, &found)
	if len(found.Hits) < 1 || found.Hits[0].Source != "spec.md" {
		t.Fatalf("search_reference = %+v", found.Hits)
	}
}

// TestSearchReferenceIsFenced asserts that search_reference wraps every hit's
// text in an UNTRUSTED fence (provenance-tagged) and surfaces the vetted flag.
func TestSearchReferenceIsFenced(t *testing.T) {
	dir := t.TempDir()
	cstore, _ := coord.Open(filepath.Join(dir, "c.sqlite3"))
	t.Cleanup(func() { cstore.Close() })
	refStore, err := reference.Open(filepath.Join(dir, "ref.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { refStore.Close() })
	es := fakeEmbed(t)
	defer es.Close()
	embedder := reference.NewEmbedderFor(es.URL, "m", "")

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() { _ = NewServer(cstore, nil, Options{Reference: refStore, Embedder: embedder}).Run(ctx, serverT) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sess.Close()

	// Ingest a chunk.
	var added addReferenceOut
	callTask(t, sess, "add_reference", map[string]any{"source": "design.md", "text": "the architecture uses event sourcing"}, &added)
	if added.Chunks < 1 {
		t.Fatalf("add_reference returned %+v", added)
	}

	// search_reference must return fenced text, NOT the raw chunk text.
	var found searchReferenceOut
	callTask(t, sess, "search_reference", map[string]any{"query": "architecture", "k": 1}, &found)
	if len(found.Hits) < 1 {
		t.Fatal("no hits returned")
	}
	h := found.Hits[0]

	// The hit must surface the source and vetted flag (false by default).
	if h.Source != "design.md" {
		t.Errorf("Source = %q, want design.md", h.Source)
	}
	if h.Vetted {
		t.Error("want Vetted==false by default, got true")
	}

	// The text must be wrapped in an UNTRUSTED fence and must NOT be raw.
	if !strings.Contains(h.Text, "UNTRUSTED") {
		t.Errorf("fenced text should contain UNTRUSTED; got: %q", h.Text)
	}
	if !strings.Contains(h.Text, "reference:design.md") {
		t.Errorf("fenced text should reference the source; got: %q", h.Text)
	}
	if !strings.Contains(h.Text, "vetted=false") {
		t.Errorf("fenced provenance should include vetted=false; got: %q", h.Text)
	}
	// The raw text content should be present inside the fence (not stripped).
	if !strings.Contains(h.Text, "event sourcing") {
		t.Errorf("raw content should appear inside the fence; got: %q", h.Text)
	}
}

// TestPromoteReferenceAdminGate asserts that promote_reference is admin-only and
// that it flips the vetted flag when called by an admin.
func TestPromoteReferenceAdminGate(t *testing.T) {
	dir := t.TempDir()
	cstore, _ := coord.Open(filepath.Join(dir, "c.sqlite3"))
	t.Cleanup(func() { cstore.Close() })
	refStore, err := reference.Open(filepath.Join(dir, "ref.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { refStore.Close() })
	es := fakeEmbed(t)
	defer es.Close()
	embedder := reference.NewEmbedderFor(es.URL, "m", "")

	// A principals store with at least one superuser means: unauthenticated
	// requests (no bearer token → p="") are NOT admins (IsSuperuser("") is
	// false once count > 0). This is how the production "admin gate closed"
	// state works — mirroring Django's post-createsuperuser behaviour.
	pstore, err := principals.Open(filepath.Join(dir, "p.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pstore.Close() })
	// Seed one real superuser so the store is no longer "open" (count==0 means
	// open-to-all; count>0 enforces the allowlist).
	if err := pstore.CreateSuperuser("real-admin@example.com", "test"); err != nil {
		t.Fatalf("CreateSuperuser: %v", err)
	}

	ctx := context.Background()

	// --- non-admin server (principals non-nil, no token = not admin) ---
	clientT1, serverT1 := mcp.NewInMemoryTransports()
	go func() {
		_ = NewServer(cstore, nil, Options{Reference: refStore, Embedder: embedder, Principals: pstore}).Run(ctx, serverT1)
	}()
	client1 := mcp.NewClient(&mcp.Implementation{Name: "t1", Version: "0"}, nil)
	sess1, err := client1.Connect(ctx, clientT1, nil)
	if err != nil {
		t.Fatalf("connect non-admin: %v", err)
	}
	defer sess1.Close()

	// Ingest so there is something to promote.
	var added addReferenceOut
	callTask(t, sess1, "add_reference", map[string]any{"source": "sec.md", "text": "security design notes"}, &added)
	if added.Chunks < 1 {
		t.Fatalf("add_reference returned %+v", added)
	}

	// promote_reference by a non-admin must be refused.
	res, err := sess1.CallTool(ctx, &mcp.CallToolParams{Name: "promote_reference", Arguments: map[string]any{"source": "sec.md"}})
	if err != nil {
		t.Fatalf("promote_reference non-admin call: %v", err)
	}
	if !res.IsError {
		t.Fatal("want tool error for non-admin promote_reference, got success")
	}

	// --- admin server (principals nil → unauthenticated = admin) ---
	clientT2, serverT2 := mcp.NewInMemoryTransports()
	go func() {
		_ = NewServer(cstore, nil, Options{Reference: refStore, Embedder: embedder}).Run(ctx, serverT2)
	}()
	client2 := mcp.NewClient(&mcp.Implementation{Name: "t2", Version: "0"}, nil)
	sess2, err := client2.Connect(ctx, clientT2, nil)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer sess2.Close()

	// promote_reference by an admin must succeed and flip vetted.
	var promoted okMsg
	callTask(t, sess2, "promote_reference", map[string]any{"source": "sec.md"}, &promoted)
	if !promoted.OK {
		t.Fatalf("promote_reference by admin: got OK=false msg=%q", promoted.Message)
	}

	// Verify search_reference now returns Vetted==true for that source.
	var found searchReferenceOut
	callTask(t, sess2, "search_reference", map[string]any{"query": "security", "k": 1}, &found)
	if len(found.Hits) < 1 {
		t.Fatal("no hits after promote")
	}
	if !found.Hits[0].Vetted {
		t.Fatalf("want Vetted==true after promote_reference, hit: %+v", found.Hits[0])
	}
	// Fenced text must now contain vetted=true.
	if !strings.Contains(found.Hits[0].Text, "vetted=true") {
		t.Errorf("fenced text after promote should include vetted=true; got: %q", found.Hits[0].Text)
	}
}

// SPDX-License-Identifier: Elastic-2.0

package brain

// Tests that add_reference, promote_reference, add_memory, and promote_memory
// each write an audit row to the coord store (Task 5 — knowledge-poisoning hardening).

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/coord"
	"github.com/pdbethke/corralai/internal/memory"
	"github.com/pdbethke/corralai/internal/reference"
)

// wantAuditIdentity is the identity the unauthenticated test client produces:
// no bearer token → actor() returns ("","") → identity(req, "agent") = "agent".
const wantAuditIdentity = "agent"

// findAuditRow returns the first audit activity whose Action matches action, or nil.
func findAuditRow(t *testing.T, cs *coord.Store, action string) *coord.Activity {
	t.Helper()
	rows, err := cs.RecentActivityAll(50)
	if err != nil {
		t.Fatalf("RecentActivityAll: %v", err)
	}
	for i := range rows {
		if rows[i].Action == action {
			return &rows[i]
		}
	}
	return nil
}

// TestAuditAddReference asserts that a successful add_reference call writes an
// audit row with action="add_reference" and a non-empty agent name.
func TestAuditAddReference(t *testing.T) {
	dir := t.TempDir()
	cs, _ := coord.Open(filepath.Join(dir, "c.sqlite3"))
	t.Cleanup(func() { cs.Close() })
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
	go func() {
		_ = NewServer(cs, nil, Options{
			Reference: refStore,
			Embedder:  embedder,
			Coord:     cs,
		}).Run(ctx, serverT)
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-audit", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sess.Close()

	var addedRef addReferenceOut
	callTask(t, sess, "add_reference", map[string]any{
		"source": "spec.md",
		"text":   "the audit test grounding text",
	}, &addedRef)

	row := findAuditRow(t, cs, "add_reference")
	if row == nil {
		t.Fatal("expected audit row with action=add_reference, got none")
	}
	if row.Agent != wantAuditIdentity {
		t.Errorf("audit row Agent = %q, want %q", row.Agent, wantAuditIdentity)
	}
}

// TestAuditPromoteReference asserts that a successful promote_reference call
// writes an audit row with action="promote_reference".
func TestAuditPromoteReference(t *testing.T) {
	dir := t.TempDir()
	cs, _ := coord.Open(filepath.Join(dir, "c.sqlite3"))
	t.Cleanup(func() { cs.Close() })
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
	go func() {
		_ = NewServer(cs, nil, Options{
			Reference: refStore,
			Embedder:  embedder,
			Coord:     cs,
		}).Run(ctx, serverT)
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-audit", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sess.Close()

	// Ingest a source first so promote has something to promote.
	var addedRef2 addReferenceOut
	callTask(t, sess, "add_reference", map[string]any{"source": "guide.md", "text": "guide text"}, &addedRef2)

	var promotedRef okMsg
	callTask(t, sess, "promote_reference", map[string]any{"source": "guide.md"}, &promotedRef)

	row := findAuditRow(t, cs, "promote_reference")
	if row == nil {
		t.Fatal("expected audit row with action=promote_reference, got none")
	}
	if row.Agent != wantAuditIdentity {
		t.Errorf("audit row Agent = %q, want %q", row.Agent, wantAuditIdentity)
	}
}

// TestAuditAddMemory asserts that a successful add_memory call writes an audit
// row with action="add_memory".
func TestAuditAddMemory(t *testing.T) {
	dir := t.TempDir()
	cs, _ := coord.Open(filepath.Join(dir, "c.sqlite3"))
	t.Cleanup(func() { cs.Close() })
	mstore, err := memory.Open(filepath.Join(dir, "m.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mstore.Close() })

	mem := filepath.Join(dir, "mem")
	if err := os.MkdirAll(mem, 0o755); err != nil {
		t.Fatal(err)
	}
	mstore.Build([]string{mem}) //nolint:errcheck

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() {
		_ = NewServer(cs, mstore, Options{Coord: cs}).Run(ctx, serverT)
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-audit", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sess.Close()

	var addedMem addOut
	callTask(t, sess, "add_memory", map[string]any{
		"name":        "audit-lesson",
		"body":        "audit every knowledge ingest",
		"description": "audit trail lesson",
		"type":        "lesson",
		"shared":      true,
	}, &addedMem)

	row := findAuditRow(t, cs, "add_memory")
	if row == nil {
		t.Fatal("expected audit row with action=add_memory, got none")
	}
	if row.Agent != wantAuditIdentity {
		t.Errorf("audit row Agent = %q, want %q", row.Agent, wantAuditIdentity)
	}
}

// TestAuditPromoteMemory asserts that a successful promote_memory call writes
// an audit row with action="promote_memory".
func TestAuditPromoteMemory(t *testing.T) {
	dir := t.TempDir()
	cs, _ := coord.Open(filepath.Join(dir, "c.sqlite3"))
	t.Cleanup(func() { cs.Close() })
	mstore, err := memory.Open(filepath.Join(dir, "m.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mstore.Close() })

	// Write a private entry directly into the temp dir and index it so
	// promote_memory can find it without writing to the real defaultMemoryDir.
	memDir := filepath.Join(dir, "mem")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatal(err)
	}
	privEntry := "---\nname: priv-lesson\ndescription: private lesson\nmetadata:\n  type: lesson\n---\n\nprivate lesson body\n"
	if err := os.WriteFile(filepath.Join(memDir, "priv-lesson.md"), []byte(privEntry), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := mstore.Build([]string{memDir}); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() {
		_ = NewServer(cs, mstore, Options{Coord: cs}).Run(ctx, serverT)
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-audit", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sess.Close()

	var promotedMem okMsg
	callTask(t, sess, "promote_memory", map[string]any{
		"name": "priv-lesson", "shared": true,
	}, &promotedMem)
	if !promotedMem.OK {
		t.Fatalf("promote_memory returned ok=false: %s", promotedMem.Message)
	}

	row := findAuditRow(t, cs, "promote_memory")
	if row == nil {
		t.Fatal("expected audit row with action=promote_memory, got none")
	}
	if row.Agent != wantAuditIdentity {
		t.Errorf("audit row Agent = %q, want %q", row.Agent, wantAuditIdentity)
	}
}

// TestAuditAddReferenceURL exercises the add_reference URL branch and asserts
// that it writes an audit row with the correct action and identity. A local
// httptest server stands in for the remote URL so the test runs hermetically
// without network access (no Egress guard → http.DefaultClient reaches localhost).
func TestAuditAddReferenceURL(t *testing.T) {
	// Serve a minimal page the fetcher can retrieve.
	fakePage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("reference content served over HTTP for audit test"))
	}))
	defer fakePage.Close()

	dir := t.TempDir()
	cs, _ := coord.Open(filepath.Join(dir, "c.sqlite3"))
	t.Cleanup(func() { cs.Close() })
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
	go func() {
		_ = NewServer(cs, nil, Options{
			Reference: refStore,
			Embedder:  embedder,
			Coord:     cs,
			// No Egress guard: http.DefaultClient is used, so localhost is reachable.
		}).Run(ctx, serverT)
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-audit", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sess.Close()

	var addedRef addReferenceOut
	callTask(t, sess, "add_reference", map[string]any{
		"url": fakePage.URL + "/doc",
	}, &addedRef)
	if addedRef.Chunks < 1 {
		t.Fatalf("add_reference via URL returned 0 chunks: %+v", addedRef)
	}

	row := findAuditRow(t, cs, "add_reference")
	if row == nil {
		t.Fatal("expected audit row with action=add_reference (URL branch), got none")
	}
	if row.Agent != wantAuditIdentity {
		t.Errorf("audit row Agent = %q, want %q", row.Agent, wantAuditIdentity)
	}
}

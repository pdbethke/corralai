// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/coord"
	"github.com/pdbethke/corralai/internal/memory"
	"github.com/pdbethke/corralai/internal/telemetry"
)

func openTel(t *testing.T) *telemetry.Store {
	t.Helper()
	s, err := telemetry.Open(filepath.Join(t.TempDir(), "t.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func kindCount(t *testing.T, tel *telemetry.Store, kind string) int {
	t.Helper()
	rep, err := tel.RunReport("kinds")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rep.Rows {
		if row[0] == kind {
			return int(row[1].(int64))
		}
	}
	return 0
}

func TestClaimAndReleaseEmitTelemetry(t *testing.T) {
	cs, err := coord.Open(filepath.Join(t.TempDir(), "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	tel := openTel(t)
	r, err := cs.ClaimPaths("bee1", []string{"a.go", "b.go"}, 3600, true, "build")
	if err != nil {
		t.Fatal(err)
	}
	recordClaimMade(tel, "bee1", r)
	if n := kindCount(t, tel, "claim_made"); n != len(r.Granted) {
		t.Fatalf("claim_made count = %d, want %d", n, len(r.Granted))
	}
	recordClaimReleased(tel, "bee1", []string{"a.go", "b.go"})
	if n := kindCount(t, tel, "claim_released"); n != 2 {
		t.Fatalf("claim_released count = %d, want 2", n)
	}
}

func TestHostSeenOnlyOnFirstOrMaterialChange(t *testing.T) {
	book := NewHostBook()
	tel := openTel(t)
	h1 := Host{Agent: "bee1", Model: "qwen2.5-coder:7b", Backend: "ollama", Jail: "bwrap"}
	recordHostSeen(tel, book, h1)
	if n := kindCount(t, tel, "host_seen"); n != 1 {
		t.Fatalf("first sighting: host_seen = %d, want 1", n)
	}
	recordHostSeen(tel, book, h1) // identical re-announce
	if n := kindCount(t, tel, "host_seen"); n != 1 {
		t.Fatalf("unchanged re-announce must not re-emit: host_seen = %d, want 1", n)
	}
	h2 := h1
	h2.Model = "qwen2.5-coder:14b"
	recordHostSeen(tel, book, h2)
	if n := kindCount(t, tel, "host_seen"); n != 2 {
		t.Fatalf("material change (model) must emit: host_seen = %d, want 2", n)
	}
}

func TestMemoryWrittenNeverCarriesBody(t *testing.T) {
	dir := t.TempDir()
	// mem.Add below uses targetDir="" (the default dir, CORRALAI_MEMORY_DIR).
	// Give this test its own dir so its "go-mod-init" entry can't leak into
	// another test's default-dir corpus within the same package test run.
	t.Setenv("CORRALAI_MEMORY_DIR", filepath.Join(dir, "default-mem"))
	mem, err := memory.Open(filepath.Join(dir, "mem.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer mem.Close()
	tel := openTel(t)
	slug, _, _, err := mem.Add("go-mod-init", "SECRET-LOOKING-BODY-TEXT run go mod init first", "how to init", "lesson", "default", "", true, "bee1")
	if err != nil {
		t.Fatal(err)
	}
	recordMemoryWritten(tel, "bee1", slug, "lesson", true)
	rep, err := tel.Query(`SELECT detail FROM events WHERE kind='memory_written'`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Rows) != 1 {
		t.Fatalf("expected 1 memory_written event, got %d", len(rep.Rows))
	}
	detail := rep.Rows[0][0].(string)
	if strings.Contains(detail, "SECRET-LOOKING-BODY-TEXT") {
		t.Fatalf("memory_written detail must never carry body text: %s", detail)
	}
}

// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/reposcan"
	"github.com/pdbethke/corralai/internal/shadowpool"
)

// signedFileEntries runs verdicts through the real hops (Aggregate ->
// WeakFile -> writeAuditStatement) and returns the signed per-file entries.
func signedFileEntries(t *testing.T, results []reposcan.FileResult) map[string]map[string]any {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "statement.json")
	rep := reposcan.Aggregate("o", "r", "abc123", len(results), len(results), results, nil)
	if _, err := writeAuditStatement(out, dir, rep, map[string]string{"test-writer": "w"}, nil, nil, true, 0, auditpush.Bundle{}); err != nil {
		t.Fatalf("writeAuditStatement: %v", err)
	}
	b, err := os.ReadFile(out) // #nosec G304 -- test-local path
	if err != nil {
		t.Fatalf("read statement: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("decode statement: %v", err)
	}
	got := map[string]map[string]any{}
	files, _ := decoded["predicate"].(map[string]any)["files"].([]any)
	for _, raw := range files {
		f := raw.(map[string]any)
		got[f["path"].(string)] = f
	}
	return got
}

// A drawn challenger must survive Verdict -> WeakFile -> AuditedFile -> the
// signed entry, and an undrawn file must carry no key at all.
func TestStatementCarriesTheShadowSelection(t *testing.T) {
	sel := []shadowpool.Selection{{Role: "mutant", Lang: "go", Chosen: "c", Seed: "s"}}
	entries := signedFileEntries(t, []reposcan.FileResult{
		{Job: reposcan.Job{Path: "drawn.go", Lang: "go"}, Gradable: true,
			Verdict: advpool.Verdict{DevKillRate: 0.5, MutantsTotal: 2, ShadowSelection: sel}},
		{Job: reposcan.Job{Path: "named.go", Lang: "go"}, Gradable: true,
			Verdict: advpool.Verdict{DevKillRate: 0.5, MutantsTotal: 2}},
	})
	raw, ok := entries["drawn.go"]["shadowSelection"]
	if !ok {
		t.Fatalf("drawn.go entry has no shadowSelection: %+v", entries["drawn.go"])
	}
	b, _ := json.Marshal(raw)
	if !strings.Contains(string(b), `"c"`) {
		t.Errorf("shadowSelection = %s, want Chosen c", b)
	}
	if _, ok := entries["named.go"]["shadowSelection"]; ok {
		t.Errorf("an undrawn file signed a shadowSelection: %+v", entries["named.go"])
	}
}

// A file reused from the verdict cache was MEASURED by an earlier run; its
// signed entry must carry that run's selection, not this invocation's draw.
func TestStatementReusedFileKeepsMeasuredSelection(t *testing.T) {
	earlier := []shadowpool.Selection{{Role: "mutant", Lang: "go", Chosen: "earlier"}}
	entries := signedFileEntries(t, []reposcan.FileResult{
		{Job: reposcan.Job{Path: "reused.go", Lang: "go"}, Gradable: true, CacheHit: true,
			Verdict: advpool.Verdict{DevKillRate: 0.9, MutantsTotal: 4, ShadowSelection: earlier}},
	})
	b, _ := json.Marshal(entries["reused.go"]["shadowSelection"])
	if !strings.Contains(string(b), "earlier") || strings.Contains(string(b), "now") {
		t.Errorf("reused file shadowSelection = %s, want the earlier run's", b)
	}
}

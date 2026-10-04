// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/reposcan"
	"github.com/pdbethke/corralai/internal/scanstore"
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

// decodeSignedSelection reads a signed entry's shadowSelection back into the
// type it was signed from, so a test asserts fields rather than substrings.
func decodeSignedSelection(t *testing.T, entry map[string]any) []shadowpool.Selection {
	t.Helper()
	raw, ok := entry["shadowSelection"]
	if !ok {
		t.Fatalf("entry has no shadowSelection: %+v", entry)
	}
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	var got []shadowpool.Selection
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("decode shadowSelection %s: %v", b, err)
	}
	return got
}

// A drawn challenger must survive Verdict -> WeakFile -> AuditedFile -> the
// signed entry, and an undrawn file must carry no key at all.
func TestStatementCarriesTheShadowSelection(t *testing.T) {
	sel := []shadowpool.Selection{{Role: advpool.RoleMutantGeneratorShadow, Lang: "go", Chosen: "c", Seed: "0x000000000000002a",
		History: shadowpool.HistoryRead, HistoryRows: 3,
		Members: []shadowpool.Member{{Model: "c", Alpha: 2, Beta: 3, Sample: 0.4}, {Model: "d", Alpha: 1, Beta: 1, Sample: 0.2}}}}
	entries := signedFileEntries(t, []reposcan.FileResult{
		{Job: reposcan.Job{Path: "drawn.go", Lang: "go"}, Gradable: true,
			Verdict: advpool.Verdict{DevKillRate: 0.5, MutantsTotal: 2, ShadowSelection: sel}},
		{Job: reposcan.Job{Path: "named.go", Lang: "go"}, Gradable: true,
			Verdict: advpool.Verdict{DevKillRate: 0.5, MutantsTotal: 2}},
	})
	got := decodeSignedSelection(t, entries["drawn.go"])
	if len(got) != 1 || got[0].Chosen != "c" || got[0].Seed != "0x000000000000002a" || got[0].Role != advpool.RoleMutantGeneratorShadow {
		t.Errorf("signed shadowSelection = %+v, want Chosen c, Seed 0x…2a, Role %s", got, advpool.RoleMutantGeneratorShadow)
	}
	if !reflect.DeepEqual(got, sel) {
		t.Errorf("signed shadowSelection = %+v, want exactly %+v", got, sel)
	}
	if _, ok := entries["named.go"]["shadowSelection"]; ok {
		t.Errorf("an undrawn file signed a shadowSelection: %+v", entries["named.go"])
	}
}

// A file reused from the verdict cache was MEASURED by an earlier run; its
// signed entry must carry that run's selection. The real round trip: the
// earlier run's verdict is encoded the one way the record carries it
// (marshalVerdict), written as a ledger entry, served by the ledger cache,
// and signed again as a reused file — the selection must come out of every
// hop exactly as it went in.
func TestStatementReusedFileKeepsMeasuredSelection(t *testing.T) {
	earlier := []shadowpool.Selection{{Role: advpool.RoleTestWriterShadow, Lang: shadowpool.LangAny, Chosen: "earlier", Seed: "0x0000000000000007",
		History: shadowpool.HistoryRead, HistoryRows: 5,
		Members: []shadowpool.Member{{Model: "earlier", Concrete: "earlier-1", Alpha: 4, Beta: 2, Sample: 0.7}, {Model: "other", Alpha: 1, Beta: 1, Sample: 0.3}}}}
	vj, err := marshalVerdict(advpool.Verdict{DevKillRate: 0.9, MutantsTotal: 4, ShadowSelection: earlier})
	if err != nil {
		t.Fatal(err)
	}
	dir := cacheTestLedger(t, []scanstore.File{{
		Path: "reused.go", Disposition: "audited", Gradable: true,
		CacheKey: "K", VerdictJSON: vj, ComputedAt: time.Now().UTC(),
	}})
	served, ok := newLedgerCache(dir, io.Discard).Get("acme", "K")
	if !ok {
		t.Fatal("the ledger cache did not serve the recorded verdict")
	}
	if !reflect.DeepEqual(served.Verdict.ShadowSelection, earlier) {
		t.Fatalf("served ShadowSelection = %+v, want the recorded %+v", served.Verdict.ShadowSelection, earlier)
	}
	served.Job = reposcan.Job{Path: "reused.go", Lang: "go"}
	served.CacheHit = true
	entries := signedFileEntries(t, []reposcan.FileResult{served})
	if got := decodeSignedSelection(t, entries["reused.go"]); !reflect.DeepEqual(got, earlier) {
		t.Errorf("reused file signed shadowSelection = %+v, want the earlier run's %+v", got, earlier)
	}
}

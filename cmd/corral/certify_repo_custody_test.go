// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/scanstore"
)

// eventSourceSentinels are the substrings that would prove the audited
// file's bytes (or its dev suite's bytes) escaped into an EVENT row. They
// are the marker comments driveEventsRun's RunSpec carries — see
// eventsRunCode / eventsRunDevTestCode.
var eventSourceSentinels = []string{"SENTINEL-AUDITED-SOURCE-8f21", "SENTINEL-DEV-TEST-SOURCE-3c40"}

// TestEventsNeverCarrySourceBytes is the custody rule at the EVENT grain.
//
// The driver's pool_subject beat carries `code` and `dev_test_code` — the
// whole audited file and the whole dev suite — and the sink JSON-encodes a
// beat's entire detail map into one column. That column is written to the
// local ledger unconditionally and inserted into corral_events with no
// --push-source guard anywhere on the path, so before this test every
// audited file's source landed in the operator's warehouse regardless of
// the custody flag.
//
// Redaction happens at the SINK, so BOTH stores are covered by one rule:
// this asserts the audited bytes appear nowhere in scan_events (local) or
// corral_events (warehouse) — with SourcePushed false AND true, because
// --push-source opts a run into shipping the AUTHORED TEST and the verdict
// JSON, never into shipping the audited file through the tape.
func TestEventsNeverCarrySourceBytes(t *testing.T) {
	for _, sourcePushed := range []bool{false, true} {
		name := "default"
		if sourcePushed {
			name = "--push-source"
		}
		t.Run(name, func(t *testing.T) {
			sink := newScanEventSink(nil)
			clk := &eventsFakeClock{t: time.Date(2026, 8, 30, 9, 0, 0, 0, time.UTC)}
			if v := driveEventsRun(t, sink, "target.go", clk); v == nil {
				t.Fatal("expected a converged verdict")
			}
			events := sink.drain()

			// The beat must still HAPPEN — redaction is not deletion. Its
			// paths are the point: a reader learns which file was audited,
			// never what was in it.
			var subject *scanstore.Event
			for i := range events {
				if events[i].Kind == "pool_subject" {
					subject = &events[i]
				}
			}
			if subject == nil {
				t.Fatal("the pool_subject beat vanished — redaction must drop the source keys, not the event")
			}
			for _, want := range []string{"code_path", "dev_test_path"} {
				if !strings.Contains(subject.Detail, want) {
					t.Errorf("pool_subject detail lost %q: %s", want, subject.Detail)
				}
			}
			// The length of what was withheld is disclosed, so a reader can
			// tell "no source" from "an empty file".
			for _, want := range []string{"code_bytes", "dev_test_code_bytes", "goal_bytes"} {
				if !strings.Contains(subject.Detail, want) {
					t.Errorf("pool_subject detail does not disclose %q (the withheld length): %s", want, subject.Detail)
				}
			}
			// THE GOAL IS NOT A NUMBER. It is prose an operator wrote (or a
			// deriver wrote from the source), it is carried verbatim on the
			// pool_subject beat, and it is the one remaining free-text field
			// on the tape. The warehouse holds numbers, hashes, reasons and
			// model names; it does not hold what a repo is trying to do. Its
			// LENGTH still ships, for the same reason every other withheld
			// field's does: "no goal" and "a 60-character goal" are different
			// answers.
			if strings.Contains(subject.Detail, "passwords >= 12 chars") {
				t.Errorf("the goal text reached the tape verbatim: %s", subject.Detail)
			}

			// 1. In memory, straight off the sink.
			for _, e := range events {
				assertNoSentinel(t, "sink event "+e.Kind, e.Detail)
			}

			// 2. The LOCAL ledger.
			ledger := filepath.Join(t.TempDir(), "scans.duckdb")
			st, err := scanstore.Open(ledger)
			if err != nil {
				t.Fatalf("scanstore.Open: %v", err)
			}
			scanID, err := st.Record(context.Background(), scanstore.Scan{Owner: "o", Repo: "o/r"}, nil)
			if err != nil {
				t.Fatalf("Record: %v", err)
			}
			for i := range events {
				events[i].ScanID = scanID
			}
			if err := st.RecordEvents(context.Background(), events); err != nil {
				t.Fatalf("RecordEvents: %v", err)
			}
			back, err := st.EventsForScan(context.Background(), scanID)
			if err != nil {
				t.Fatalf("EventsForScan: %v", err)
			}
			if len(back) != len(events) {
				t.Fatalf("read back %d event(s), wrote %d", len(back), len(events))
			}
			for _, e := range back {
				assertNoSentinel(t, "scan_events "+e.Kind, e.Detail)
			}
			_ = st.Close()

			// 3. The WAREHOUSE.
			b := buildBundle(scanstore.Scan{Repo: "o/r", Commit: "deadbeef"}, scanID,
				nil, nil, nil, events, auditpush.Link{}, sourcePushed,
				"o/r", "deadbeef", "", bundleMeta{})
			target := filepath.Join(t.TempDir(), "w.duckdb")
			if _, err := pushBundle(target, b); err != nil {
				t.Fatalf("pushBundle: %v", err)
			}
			rows := queryRows(t, target, `SELECT kind, COALESCE(detail, '') FROM corral_events`)
			if len(rows) != len(events) {
				t.Fatalf("corral_events holds %d row(s), pushed %d", len(rows), len(events))
			}
			for _, r := range rows {
				kind, _ := r[0].(string)
				detail, _ := r[1].(string)
				assertNoSentinel(t, "corral_events "+kind, detail)
			}
		})
	}
}

func assertNoSentinel(t *testing.T, where, detail string) {
	t.Helper()
	for _, s := range eventSourceSentinels {
		if strings.Contains(detail, s) {
			t.Errorf("%s carries audited source (%s): %s", where, s, detail)
		}
	}
}

// TestWarehouseRowsSHA256UsesTheOneCustodyRule pins the hasher to the same
// function the WRITER uses (auditpush.BlankUnpushedSource) instead of its own
// second copy of the list.
//
// Two copies is one copy that gets a field added to it and one that does not,
// and this one's divergence is silent: the statement would carry a
// warehouseRowsSha256 taken over bytes the warehouse never received, so the
// third-party cross-check the statement exists for could never check out.
//
// The oracle IS BlankUnpushedSource: hashing a bundle must equal hashing the
// same bundle with the writer's own withholding already applied. A field the
// hasher stopped blanking would break that equality; and the second arm —
// a change to a field OUTSIDE the custody set must change the hash — keeps
// the first from being satisfied by a hasher that blanks everything.
func TestWarehouseRowsSHA256UsesTheOneCustodyRule(t *testing.T) {
	base := auditpush.Bundle{
		Scan:    auditpush.ScanRow{Repo: "o/r", Commit: "deadbeef"},
		Files:   []auditpush.Row{{Repo: "o/r", Path: "a.go", Detail: "a note", AuthoredTest: "AUTHORED", VerdictJSON: `{"v":1}`}},
		Mutants: []auditpush.MutantRow{{Repo: "o/r", Path: "a.go", MutantID: "m1", Outcome: "killed", Code: "MUTANT"}},
	}

	raw, err := warehouseRowsSHA256(base)
	if err != nil {
		t.Fatalf("warehouseRowsSHA256: %v", err)
	}

	withheld := base
	auditpush.BlankUnpushedSource(&withheld)
	blanked, err := warehouseRowsSHA256(withheld)
	if err != nil {
		t.Fatalf("warehouseRowsSHA256 (pre-withheld): %v", err)
	}
	if raw != blanked {
		t.Errorf("the hasher and the writer disagree about the custody set:\n hashed as built   = %s\n hashed as WRITTEN = %s", raw, blanked)
	}

	// ...but it must not be blanking everything: a field outside the custody
	// set still has to reach the hash.
	other := base
	other.Files = []auditpush.Row{base.Files[0]}
	other.Files[0].Detail = "a different note"
	changed, err := warehouseRowsSHA256(other)
	if err != nil {
		t.Fatalf("warehouseRowsSHA256 (changed): %v", err)
	}
	if changed == raw {
		t.Error("a non-source field changed and the hash did not — the hasher is blanking more than the custody set")
	}

	// With --push-source the source IS what the warehouse receives, so it
	// must be in the hash.
	pushed := base
	pushed.SourcePushed = true
	pushedSHA, err := warehouseRowsSHA256(pushed)
	if err != nil {
		t.Fatalf("warehouseRowsSHA256 (--push-source): %v", err)
	}
	if pushedSHA == raw {
		t.Error("--push-source hashed the same as withheld — the source it actually ships is not covered by the statement")
	}
}

// TestRedactSourceDetailReachesNestedAndOddlySpelledKeys: the guard used
// to look at the top level of the detail map only, by exact key. A beat
// that nested its payload (`shards: [{code: …}]`) or spelled a key the way
// a person reads it ("Code", "dev-test-code", "STDOUT") shipped source
// under a name that, to a reader of the warehouse, IS the guarded name.
// No emit does this today; the point is that the next one cannot.
func TestRedactSourceDetailReachesNestedAndOddlySpelledKeys(t *testing.T) {
	const src = "SENTINEL-NESTED-SOURCE-77aa"
	in := map[string]any{
		"path":   "pkg/a.go",
		"Code":   src,
		"STDOUT": src,
		"shards": []any{
			map[string]any{"id": 1, "dev-test-code": src},
			map[string]any{"id": 2, "nested": map[string]any{"authored_test": src}},
		},
		"regions": []map[string]any{{"mutant_code": src}},
	}
	out := redactSourceDetail(in)
	js, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(js), src) {
		t.Fatalf("source escaped the redaction: %s", js)
	}
	for _, want := range []string{`"Code_bytes":27`, `"STDOUT_bytes":27`, `"dev-test-code_bytes":27`, `"authored_test_bytes":27`, `"mutant_code_bytes":27`, `"path":"pkg/a.go"`, `"id":1`} {
		if !strings.Contains(string(js), want) {
			t.Errorf("redacted detail lacks %s: %s", want, js)
		}
	}
	// Copy-on-write, at every level: the caller's map is untouched.
	if in["Code"] != src || in["shards"].([]any)[0].(map[string]any)["dev-test-code"] != src {
		t.Error("the caller's detail map was mutated in place")
	}
}

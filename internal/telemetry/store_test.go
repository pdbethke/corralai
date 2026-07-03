// SPDX-License-Identifier: Elastic-2.0

package telemetry

import (
	"path/filepath"
	"testing"
)

func openT(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "tel.duckdb"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestCountKind(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "t.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 3; i++ {
		if err := s.Record(Event{MissionID: 7, Kind: "agent_activity", Actor: "bee1"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Record(Event{MissionID: 8, Kind: "agent_activity", Actor: "bee2"}); err != nil {
		t.Fatal(err)
	}
	n, err := s.CountKind(7, "agent_activity")
	if err != nil || n != 3 {
		t.Fatalf("CountKind(7): n=%d err=%v, want 3", n, err)
	}
	if n, _ := s.CountKind(8, "agent_activity"); n != 1 {
		t.Fatalf("CountKind(8): got %d, want 1", n)
	}
	if n, _ := s.CountKind(9, "agent_activity"); n != 0 {
		t.Fatalf("CountKind(9): got %d, want 0", n)
	}
}

func TestRecordAndReports(t *testing.T) {
	s := openT(t)
	s.Record(Event{MissionID: 1, Kind: "mission_created", Actor: "operator"})
	s.Record(Event{MissionID: 1, Kind: "task_completed", Actor: "Ada", Subject: "build#1"})
	s.Record(Event{MissionID: 1, Kind: "task_completed", Actor: "Ada", Subject: "fix-f2"})
	s.Record(Event{MissionID: 1, Kind: "task_completed", Actor: "Tess", Subject: "test#1"})
	s.Record(Event{MissionID: 1, Kind: "finding_reported", Actor: "Hawk", Subject: "score-API", Detail: map[string]any{"severity": "high"}})
	s.Record(Event{MissionID: 1, Kind: "finding_recurring", Actor: "Hawk", Subject: "score-API"})

	// agents: Ada=2, Tess=1, ordered desc.
	rep, err := s.RunReport("agents")
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Rows) != 2 {
		t.Fatalf("agents: want 2 rows, got %d (%+v)", len(rep.Rows), rep.Rows)
	}
	if got := rep.Rows[0][0]; got != "Ada" {
		t.Fatalf("top agent = %v, want Ada", got)
	}

	// findings: two finding-kind rows.
	fr, _ := s.RunReport("findings")
	if len(fr.Rows) != 2 {
		t.Fatalf("findings: want 2 kinds, got %d", len(fr.Rows))
	}

	// ad-hoc read-only query works; a write is rejected.
	q, err := s.Query("SELECT count(*) FROM events")
	if err != nil || len(q.Rows) != 1 {
		t.Fatalf("count query: %v %+v", err, q.Rows)
	}
	if _, err := s.Query("DELETE FROM events"); err == nil {
		t.Fatal("a non-SELECT query must be rejected")
	}
	if _, err := s.Query("SELECT 1; DROP TABLE events"); err == nil {
		t.Fatal("a multi-statement query must be rejected")
	}
}

func TestUnknownReport(t *testing.T) {
	s := openT(t)
	if _, err := s.RunReport("nope"); err == nil {
		t.Fatal("unknown report should error")
	}
}

func TestModelComparisonReport(t *testing.T) {
	s := openT(t)

	// Model A: 3 finding_reported (high/high/low), 2 finding_resolved (both addressed).
	// → findings=3, high=2, low=1, addressed=2, dismissed=0, open=1, confirm=100.0
	mustRecord := func(e Event) {
		t.Helper()
		if err := s.Record(e); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	mustRecord(Event{Kind: "finding_reported", Model: "A", Detail: map[string]any{"severity": "high", "finding_id": "a1"}})
	mustRecord(Event{Kind: "finding_reported", Model: "A", Detail: map[string]any{"severity": "high", "finding_id": "a2"}})
	mustRecord(Event{Kind: "finding_reported", Model: "A", Detail: map[string]any{"severity": "low", "finding_id": "a3"}})
	mustRecord(Event{Kind: "finding_resolved", Model: "A", Detail: map[string]any{"outcome": "addressed", "finding_id": "a1"}})
	mustRecord(Event{Kind: "finding_resolved", Model: "A", Detail: map[string]any{"outcome": "addressed", "finding_id": "a2"}})
	// a3 intentionally left open

	// Model B: 2 finding_reported (critical/medium).
	// Double-resolve on finding_id "bX": dismissed (earlier ts) then addressed (later ts) → latest-wins = addressed.
	// Plus a separate finding "bY" resolved as dismissed.
	// → findings=2, critical=1, medium=1, addressed=1, dismissed=1, open=0, confirm=50.0
	mustRecord(Event{Kind: "finding_reported", Model: "B", Detail: map[string]any{"severity": "critical", "finding_id": "bX"}})
	mustRecord(Event{Kind: "finding_reported", Model: "B", Detail: map[string]any{"severity": "medium", "finding_id": "bY"}})
	mustRecord(Event{Kind: "finding_resolved", Model: "B", Detail: map[string]any{"outcome": "dismissed", "finding_id": "bX"}})
	mustRecord(Event{Kind: "finding_resolved", Model: "B", Detail: map[string]any{"outcome": "addressed", "finding_id": "bX"}}) // later ts wins
	mustRecord(Event{Kind: "finding_resolved", Model: "B", Detail: map[string]any{"outcome": "dismissed", "finding_id": "bY"}})

	// Model C: 1 finding_reported, 0 resolved → confirm_pct must be NULL.
	mustRecord(Event{Kind: "finding_reported", Model: "C", Detail: map[string]any{"severity": "medium", "finding_id": "c1"}})

	rep, err := s.RunReport("model_comparison")
	if err != nil {
		t.Fatalf("model_comparison report: %v", err)
	}
	// Expect 3 rows (A, B, C) ordered by findings desc: A=3, B=2, C=1.
	if len(rep.Rows) != 3 {
		t.Fatalf("want 3 rows, got %d: %+v", len(rep.Rows), rep.Rows)
	}

	type row struct {
		model      string
		findings   int64
		critical   int64
		high       int64
		medium     int64
		low        int64
		addressed  int64
		dismissed  int64
		open       int64
		confirmPct *float64
	}
	parse := func(r []any) row {
		t.Helper()
		toI := func(v any) int64 {
			switch x := v.(type) {
			case int64:
				return x
			case int32:
				return int64(x)
			case float64:
				return int64(x)
			default:
				t.Fatalf("unexpected int type %T: %v", v, v)
				return 0
			}
		}
		toF := func(v any) *float64 {
			if v == nil {
				return nil
			}
			switch x := v.(type) {
			case float64:
				return &x
			case float32:
				f := float64(x)
				return &f
			default:
				t.Fatalf("unexpected float type %T: %v", v, v)
				return nil
			}
		}
		return row{
			model:      r[0].(string),
			findings:   toI(r[1]),
			critical:   toI(r[2]),
			high:       toI(r[3]),
			medium:     toI(r[4]),
			low:        toI(r[5]),
			addressed:  toI(r[6]),
			dismissed:  toI(r[7]),
			open:       toI(r[8]),
			confirmPct: toF(r[9]),
		}
	}

	a := parse(rep.Rows[0])
	if a.model != "A" {
		t.Errorf("row0 model = %q, want A", a.model)
	}
	if a.findings != 3 {
		t.Errorf("A findings = %d, want 3", a.findings)
	}
	if a.high != 2 {
		t.Errorf("A high = %d, want 2", a.high)
	}
	if a.low != 1 {
		t.Errorf("A low = %d, want 1", a.low)
	}
	if a.addressed != 2 {
		t.Errorf("A addressed = %d, want 2", a.addressed)
	}
	if a.dismissed != 0 {
		t.Errorf("A dismissed = %d, want 0", a.dismissed)
	}
	if a.open != 1 {
		t.Errorf("A open = %d, want 1", a.open)
	}
	if a.confirmPct == nil || *a.confirmPct != 100.0 {
		t.Errorf("A confirm_pct = %v, want 100.0", a.confirmPct)
	}

	b := parse(rep.Rows[1])
	if b.model != "B" {
		t.Errorf("row1 model = %q, want B", b.model)
	}
	if b.findings != 2 {
		t.Errorf("B findings = %d, want 2", b.findings)
	}
	if b.critical != 1 {
		t.Errorf("B critical = %d, want 1", b.critical)
	}
	if b.medium != 1 {
		t.Errorf("B medium = %d, want 1", b.medium)
	}
	if b.addressed != 1 {
		t.Errorf("B addressed = %d, want 1 (latest-wins dedupe)", b.addressed)
	}
	if b.dismissed != 1 {
		t.Errorf("B dismissed = %d, want 1", b.dismissed)
	}
	if b.open != 0 {
		t.Errorf("B open = %d, want 0", b.open)
	}
	if b.confirmPct == nil || *b.confirmPct != 50.0 {
		t.Errorf("B confirm_pct = %v, want 50.0", b.confirmPct)
	}

	c := parse(rep.Rows[2])
	if c.model != "C" {
		t.Errorf("row2 model = %q, want C", c.model)
	}
	if c.findings != 1 {
		t.Errorf("C findings = %d, want 1", c.findings)
	}
	if c.confirmPct != nil {
		t.Errorf("C confirm_pct = %v, want NULL (no resolutions)", c.confirmPct)
	}
}

// SPDX-License-Identifier: Elastic-2.0

package queue

import "testing"

func TestAddFindingCarriesModel(t *testing.T) {
	s := open(t)

	// Finding WITH model fields → they round-trip through the DB.
	id, err := s.AddFinding(Finding{
		MissionID: 1, Reporter: "Hawk", Type: "vuln", Severity: "high",
		ReporterModel: "claude-opus", ReporterBackend: "anthropic",
	})
	if err != nil || id == 0 {
		t.Fatalf("AddFinding with model fields rejected: id=%d err=%v", id, err)
	}

	// Finding WITHOUT model fields → reads back as "" (back-compat).
	id2, err := s.AddFinding(Finding{
		MissionID: 1, Reporter: "Tess", Type: "bug", Severity: "low",
	})
	if err != nil || id2 == 0 {
		t.Fatalf("AddFinding without model fields rejected: id=%d err=%v", id2, err)
	}

	fs, err := s.Findings(1, "")
	if err != nil {
		t.Fatalf("Findings: %v", err)
	}
	if len(fs) != 2 {
		t.Fatalf("want 2 findings, got %d", len(fs))
	}

	// Newest-first; id is the second-inserted (lower id) item last.
	byID := map[int64]Finding{}
	for _, f := range fs {
		byID[f.ID] = f
	}

	if byID[id].ReporterModel != "claude-opus" {
		t.Errorf("ReporterModel: got %q, want %q", byID[id].ReporterModel, "claude-opus")
	}
	if byID[id].ReporterBackend != "anthropic" {
		t.Errorf("ReporterBackend: got %q, want %q", byID[id].ReporterBackend, "anthropic")
	}
	if byID[id2].ReporterModel != "" {
		t.Errorf("back-compat: ReporterModel should be empty for old row, got %q", byID[id2].ReporterModel)
	}
	if byID[id2].ReporterBackend != "" {
		t.Errorf("back-compat: ReporterBackend should be empty for old row, got %q", byID[id2].ReporterBackend)
	}
}

// TestAllFindingsSpansMissions verifies AllFindings returns findings across
// every mission (unlike Findings, which is mission-scoped) — the learn sweep
// ticker needs the fleet-wide view to detect cross-mission recurrence.
func TestAllFindingsSpansMissions(t *testing.T) {
	s := open(t)

	if _, err := s.AddFinding(Finding{
		MissionID: 1, Reporter: "Hawk", Type: "vuln", Severity: "high", Target: "authz",
	}); err != nil {
		t.Fatalf("AddFinding mission 1: %v", err)
	}
	if _, err := s.AddFinding(Finding{
		MissionID: 2, Reporter: "Tess", Type: "bug", Severity: "low", Target: "parser",
	}); err != nil {
		t.Fatalf("AddFinding mission 2: %v", err)
	}

	fs, err := s.AllFindings()
	if err != nil {
		t.Fatalf("AllFindings: %v", err)
	}
	if len(fs) != 2 {
		t.Fatalf("want 2 findings across missions, got %d", len(fs))
	}
	byMission := map[int64]bool{}
	for _, f := range fs {
		byMission[f.MissionID] = true
	}
	if !byMission[1] || !byMission[2] {
		t.Fatalf("expected findings from missions 1 and 2, got %+v", fs)
	}
}

// TestAllFindingsUnboundedReturnsEveryRow verifies the sweep-facing accessor
// has NO row cap. The learn sweep re-feeds ALL findings every cycle —
// sub-threshold signature groups persist nothing between sweeps — so a finding
// pushed past a row limit would silently stop counting toward the recurrence
// threshold. Contrast: AllFindings (the live-UI accessor) caps at 200 rows.
func TestAllFindingsUnboundedReturnsEveryRow(t *testing.T) {
	s := open(t)

	const total = 205
	for i := 0; i < total; i++ {
		mission := int64(1 + i%2) // spread across two missions
		if _, err := s.AddFinding(Finding{
			MissionID: mission, Reporter: "Hawk", Type: "bug", Severity: "low", Target: "parser",
		}); err != nil {
			t.Fatalf("AddFinding #%d: %v", i, err)
		}
	}

	fs, err := s.AllFindingsUnbounded()
	if err != nil {
		t.Fatalf("AllFindingsUnbounded: %v", err)
	}
	if len(fs) != total {
		t.Fatalf("want all %d findings (no row cap), got %d", total, len(fs))
	}

	// Sanity-check the contrast: the UI accessor stays capped at 200.
	capped, err := s.AllFindings()
	if err != nil {
		t.Fatalf("AllFindings: %v", err)
	}
	if len(capped) != 200 {
		t.Fatalf("AllFindings cap changed: want 200 rows, got %d", len(capped))
	}
}

func TestAddFindingValidates(t *testing.T) {
	s := open(t)
	good := Finding{MissionID: 1, Reporter: "Hawk", Type: "vuln", Severity: "high",
		Target: "src/api.go", Evidence: "SQLi", SuggestedAction: "parameterize"}
	id, err := s.AddFinding(good)
	if err != nil || id == 0 {
		t.Fatalf("valid finding rejected: id=%d err=%v", id, err)
	}

	for _, bad := range []Finding{
		{MissionID: 1, Reporter: "Hawk", Type: "nonsense", Severity: "high"},
		{MissionID: 1, Reporter: "Hawk", Type: "vuln", Severity: "spicy"},
		{MissionID: 1, Type: "vuln", Severity: "high"},     // no reporter
		{Reporter: "Hawk", Type: "vuln", Severity: "high"}, // no mission
	} {
		if _, err := s.AddFinding(bad); err == nil {
			t.Fatalf("invalid finding accepted: %+v", bad)
		}
	}

	// Stored open, with fields intact.
	fs, _ := s.Findings(1, "")
	if len(fs) != 1 {
		t.Fatalf("want 1 finding, got %d", len(fs))
	}
	if f := fs[0]; f.Status != FindingOpen || f.Target != "src/api.go" || f.SuggestedAction != "parameterize" {
		t.Fatalf("finding not stored intact: %+v", f)
	}
}

func TestFindingsFilterByStatus(t *testing.T) {
	s := open(t)
	a, _ := s.AddFinding(Finding{MissionID: 1, Reporter: "Hawk", Type: "vuln", Severity: "critical"})
	s.AddFinding(Finding{MissionID: 1, Reporter: "Tess", Type: "bug", Severity: "low"})
	s.AddFinding(Finding{MissionID: 2, Reporter: "Hawk", Type: "note", Severity: "low"}) // other mission

	if all, _ := s.Findings(1, ""); len(all) != 2 {
		t.Fatalf("mission 1 has %d findings, want 2", len(all))
	}
	if open, _ := s.Findings(1, FindingOpen); len(open) != 2 {
		t.Fatalf("mission 1 open = %d, want 2", len(open))
	}

	// Resolve one → it leaves the open set.
	if ok, _ := s.SetFindingStatus(a, FindingAddressed); !ok {
		t.Fatal("SetFindingStatus did not transition the finding")
	}
	if open, _ := s.Findings(1, FindingOpen); len(open) != 1 {
		t.Fatalf("after resolve, open = %d, want 1", len(open))
	}
}

func TestSetFindingStatusValidationAndMiss(t *testing.T) {
	s := open(t)
	id, _ := s.AddFinding(Finding{MissionID: 1, Reporter: "Hawk", Type: "vuln", Severity: "high"})
	if _, err := s.SetFindingStatus(id, "bogus"); err == nil {
		t.Fatal("invalid status accepted")
	}
	if ok, _ := s.SetFindingStatus(99999, FindingDismissed); ok {
		t.Fatal("transitioned a non-existent finding")
	}
}

func TestRecurrenceDetection(t *testing.T) {
	s := open(t)
	// First sighting of a (type,target) is not recurring.
	s.AddFinding(Finding{MissionID: 1, Reporter: "Hawk", Type: "vuln", Severity: "high", Target: "score-API"})
	// Same type+target again (even a later mission) => recurring: the fix/lesson didn't hold.
	s.AddFinding(Finding{MissionID: 2, Reporter: "Hawk", Type: "vuln", Severity: "high", Target: "score-API"})
	// Different target => not recurring.
	s.AddFinding(Finding{MissionID: 2, Reporter: "Hawk", Type: "vuln", Severity: "high", Target: "auth"})

	rec := map[string]bool{} // target -> any recurring
	all1, _ := s.Findings(1, "")
	all2, _ := s.Findings(2, "")
	for _, f := range append(all1, all2...) {
		if f.Recurring {
			rec[f.Target] = true
		}
	}
	if !rec["score-API"] {
		t.Fatal("the repeated score-API vuln should be flagged recurring")
	}
	if rec["auth"] {
		t.Fatal("a first-time finding must not be flagged recurring")
	}
}

func TestFindingByID(t *testing.T) {
	s := open(t)

	// Round-trip: all fields including ReporterModel/ReporterBackend come back intact.
	id, err := s.AddFinding(Finding{
		MissionID: 7, Reporter: "Hawk", Type: "vuln", Severity: "high",
		Target: "auth", ReporterModel: "gemini-3", ReporterBackend: "gemini",
	})
	if err != nil || id == 0 {
		t.Fatalf("AddFinding: id=%d err=%v", id, err)
	}

	f, ok, err := s.FindingByID(id)
	if err != nil {
		t.Fatalf("FindingByID: %v", err)
	}
	if !ok {
		t.Fatal("FindingByID: want ok=true, got false")
	}
	if f.MissionID != 7 {
		t.Errorf("MissionID: got %d, want 7", f.MissionID)
	}
	if f.Target != "auth" {
		t.Errorf("Target: got %q, want %q", f.Target, "auth")
	}
	if f.ReporterModel != "gemini-3" {
		t.Errorf("ReporterModel: got %q, want %q", f.ReporterModel, "gemini-3")
	}
	if f.ReporterBackend != "gemini" {
		t.Errorf("ReporterBackend: got %q, want %q", f.ReporterBackend, "gemini")
	}

	// Missing id → ok=false, no error.
	_, ok2, err2 := s.FindingByID(99999)
	if err2 != nil {
		t.Fatalf("FindingByID missing: unexpected error: %v", err2)
	}
	if ok2 {
		t.Fatal("FindingByID missing: want ok=false, got true")
	}
}

// TestSetFindingStatusStampsResolvedTS verifies resolved_ts stays 0 until the
// finding first leaves "open", then gets stamped >= created_ts.
func TestSetFindingStatusStampsResolvedTS(t *testing.T) {
	s := open(t)
	id, err := s.AddFinding(Finding{MissionID: 1, Reporter: "bee1", Type: "bug", Severity: "high", Target: "x.go"})
	if err != nil {
		t.Fatal(err)
	}
	f, ok, _ := s.FindingByID(id)
	if !ok || f.ResolvedTS != 0 {
		t.Fatalf("freshly-opened finding must have resolved_ts=0, got %v", f.ResolvedTS)
	}
	if ok, err := s.SetFindingStatus(id, FindingAddressed); err != nil || !ok {
		t.Fatalf("SetFindingStatus: ok=%v err=%v", ok, err)
	}
	f2, _, _ := s.FindingByID(id)
	if f2.ResolvedTS == 0 {
		t.Fatal("resolved finding must have a non-zero resolved_ts")
	}
	if f2.ResolvedTS < f.CreatedTS {
		t.Fatalf("resolved_ts %v must be >= created_ts %v", f2.ResolvedTS, f.CreatedTS)
	}
}

func TestSeverityRankOrders(t *testing.T) {
	if !(SeverityRank("low") < SeverityRank("medium") &&
		SeverityRank("medium") < SeverityRank("high") &&
		SeverityRank("high") < SeverityRank("critical")) {
		t.Fatal("severity ranking is not strictly increasing")
	}
}

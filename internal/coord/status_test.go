// SPDX-License-Identifier: Elastic-2.0

package coord

import "testing"

func TestSetStatusStampsSinceOnChangeOnly(t *testing.T) {
	clock := 500.0
	withClock(t, func() float64 { return clock })
	s := openTmp(t)
	if err := s.Register("alice", "", "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	clock = 600
	if err := s.SetStatus("alice", "awaiting_approval"); err != nil {
		t.Fatal(err)
	}
	clock = 700
	if err := s.SetStatus("alice", "awaiting_approval"); err != nil { // unchanged
		t.Fatal(err)
	}
	agents, err := s.ListActive(100000)
	if err != nil {
		t.Fatal(err)
	}
	var a Agent
	for _, x := range agents {
		if x.Name == "alice" {
			a = x
		}
	}
	if a.Status != "awaiting_approval" {
		t.Fatalf("status = %q", a.Status)
	}
	if a.StatusSince != 600 {
		t.Fatalf("status_since should stamp once on change, got %v (want 600)", a.StatusSince)
	}
}

func TestParkedExclusiveDowngradesToAdvisory(t *testing.T) {
	clock := 1000.0
	withClock(t, func() float64 { return clock })
	t.Setenv("CORRALAI_PARKED_GRACE_SECONDS", "20")
	s := openTmp(t)
	s.Register("owner", "", "", "", "", "")
	s.Register("peer", "", "", "", "", "")
	if _, err := s.ClaimPaths("owner", []string{"src/app.go"}, 3600, true, "editing"); err != nil {
		t.Fatal(err)
	}

	// Owner parks at t=1010.
	clock = 1010
	if err := s.SetStatus("owner", "awaiting_approval"); err != nil {
		t.Fatal(err)
	}

	// Within the grace window (t=1020, parked 10s < 20): peer is BLOCKED.
	clock = 1020
	r1, _ := s.ClaimPaths("peer", []string{"src/app.go"}, 3600, true, "")
	if len(r1.Granted) != 0 || len(r1.Conflicts) != 1 {
		t.Fatalf("within grace peer must be blocked, got granted=%v conflicts=%v", r1.Granted, r1.Conflicts)
	}

	// Past the grace window (t=1040, parked 30s > 20): peer is GRANTED with a surfaced conflict.
	clock = 1040
	r2, _ := s.ClaimPaths("peer", []string{"src/app.go"}, 3600, true, "")
	if len(r2.Granted) != 1 {
		t.Fatalf("past grace peer must be granted, got %v", r2.Granted)
	}
	if len(r2.Conflicts) != 1 || r2.Conflicts[0].HeldBy != "owner" {
		t.Fatalf("past grace peer must still SEE the conflict, got %+v", r2.Conflicts)
	}
}

func TestResumeFlagsContestedClaims(t *testing.T) {
	clock := 2000.0
	withClock(t, func() float64 { return clock })
	t.Setenv("CORRALAI_PARKED_GRACE_SECONDS", "20")
	s := openTmp(t)
	s.Register("owner", "", "", "", "", "")
	s.Register("peer", "", "", "", "", "")
	s.ClaimPaths("owner", []string{"src/app.go"}, 3600, true, "")

	clock = 2010
	s.SetStatus("owner", "awaiting_approval")
	clock = 2040 // past grace: peer can take it
	if _, err := s.ClaimPaths("peer", []string{"src/app.go"}, 3600, true, ""); err != nil {
		t.Fatal(err)
	}

	// Owner returns and re-bootstraps: its lease is flagged contested.
	b, err := s.BootstrapSession("owner", "", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Contested) != 1 || b.Contested[0].Path != "src/app.go" || b.Contested[0].HeldBy != "peer" {
		t.Fatalf("owner must be told src/app.go is contested by peer, got %+v", b.Contested)
	}
}

func TestCoordinationStatusExposesClockAndGrace(t *testing.T) {
	clock := 4242.0
	withClock(t, func() float64 { return clock })
	t.Setenv("CORRALAI_PARKED_GRACE_SECONDS", "20")
	s := openTmp(t)
	s.Register("alice", "", "", "", "", "")
	st, err := s.CoordinationStatus(100000)
	if err != nil {
		t.Fatal(err)
	}
	if st.ServerNow != 4242 {
		t.Fatalf("server_now should be the seam clock, got %v", st.ServerNow)
	}
	if st.ParkedGraceSeconds != 20 {
		t.Fatalf("parked_grace_seconds should reflect env, got %v", st.ParkedGraceSeconds)
	}
}

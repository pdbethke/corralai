// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"bytes"
	"log"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/pdbethke/corralai/internal/telemetry"
)

func TestRecordActivityCapCacheSkipsCount(t *testing.T) {
	ring := NewActivityRing()
	capped := &capCache{}
	tel, err := telemetry.Open(filepath.Join(t.TempDir(), "t.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer tel.Close()
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)
	for i := 0; i < agentActivityCap+5; i++ {
		recordActivity(tel, ring, capped, 42, Activity{Agent: "bee1", Role: "builder", Tool: "run_command", Detail: "go build"})
	}
	if !capped.capped(42) {
		t.Fatal("mission 42 must be cached as capped once the cap is crossed")
	}
	if got := strings.Count(buf.String(), "agent_activity cap"); got != 1 {
		t.Fatalf("loud cap log must fire exactly once, fired %d times:\n%s", got, buf.String())
	}
	if n, err := tel.CountKind(42, "agent_activity"); err != nil || n != agentActivityCap {
		t.Fatalf("count after cap: n=%d err=%v, want %d", n, err, agentActivityCap)
	}
	// Post-cap reports must skip the COUNT entirely: with the store closed, a
	// query would log an error — a cache hit stays silent and still no-ops.
	tel.Close()
	buf.Reset()
	recordActivity(tel, ring, capped, 42, Activity{Agent: "bee1", Tool: "run_command", Detail: "go test"})
	if buf.Len() != 0 {
		t.Fatalf("post-cap report must not touch the store, but logged:\n%s", buf.String())
	}
	// The ring still receives every report, capped or not.
	if got := ring.Recent()[0].Detail; got != "go test" {
		t.Fatalf("ring must still receive post-cap activity, top detail = %q", got)
	}
}

func TestRecordActivityTruncatesDurableDetail(t *testing.T) {
	ring := NewActivityRing()
	tel, err := telemetry.Open(filepath.Join(t.TempDir(), "t.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer tel.Close()
	long := strings.Repeat("x", 4096)
	recordActivity(tel, ring, &capCache{}, 43, Activity{Agent: "bee1", Role: "builder", Tool: "run_command", Detail: long})
	// The live ring keeps the full line — only the durable copy is bounded.
	if got := ring.Recent()[0].Detail; got != long {
		t.Fatalf("ring detail must be untruncated: got %d chars, want %d", len(got), len(long))
	}
	rep, err := tel.Query(`SELECT json_extract_string(detail,'$.detail') FROM events WHERE mission_id=43 AND kind='agent_activity'`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Rows) != 1 {
		t.Fatalf("want 1 durable event, got %d", len(rep.Rows))
	}
	stored, _ := rep.Rows[0][0].(string)
	if n := utf8.RuneCountInString(stored); n != activityDetailMax {
		t.Fatalf("durable detail must be exactly %d runes, got %d", activityDetailMax, n)
	}
	if !strings.HasSuffix(stored, "…") {
		t.Fatalf("truncated detail must end with the … marker, got tail %q", stored[len(stored)-8:])
	}
	// A short line passes through untouched.
	recordActivity(tel, ring, &capCache{}, 44, Activity{Agent: "bee1", Detail: "go build"})
	rep, err = tel.Query(`SELECT json_extract_string(detail,'$.detail') FROM events WHERE mission_id=44 AND kind='agent_activity'`)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := rep.Rows[0][0].(string); got != "go build" {
		t.Fatalf("short detail must be stored verbatim, got %q", got)
	}
}

func TestReportActivityRecordsAndCaps(t *testing.T) {
	ring := NewActivityRing()
	tel, err := telemetry.Open(filepath.Join(t.TempDir(), "t.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer tel.Close()
	// Directly exercise the recording helper this task adds (not the MCP
	// round-trip — that's covered by the wire suite): recordActivity is the
	// function registerActivity's handler calls after ring.Add.
	capped := &capCache{}
	for i := 0; i < agentActivityCap+5; i++ {
		recordActivity(tel, ring, capped, 42, Activity{Agent: "bee1", Role: "builder", Tool: "run_command", Detail: "go build"})
	}
	n, err := tel.CountKind(42, "agent_activity")
	if err != nil {
		t.Fatal(err)
	}
	if n != agentActivityCap {
		t.Fatalf("agent_activity must be capped at %d, got %d", agentActivityCap, n)
	}
}

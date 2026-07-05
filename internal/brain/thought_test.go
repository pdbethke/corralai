// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/mission"
	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/telemetry"

	_ "modernc.org/sqlite"
)

// openThoughtFixtures opens a fresh mission store, queue, and telemetry store
// under a temp dir, and creates one mission — returning everything a
// report_thought test needs.
func openThoughtFixtures(t *testing.T, recordStory bool) (*mission.Store, *telemetry.Store, int64, string) {
	t.Helper()
	dir := t.TempDir()
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.Close() })
	missionDBPath := filepath.Join(dir, "m.sqlite3")
	m, err := mission.Open(missionDBPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	tel, err := telemetry.Open(filepath.Join(dir, "t.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tel.Close() })

	mid, err := mission.CreateMission(m, q, "tell a story", []mission.PhaseSpec{{Name: "build", Instruction: "build it"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if recordStory {
		if err := m.SetRecordStory(mid, true); err != nil {
			t.Fatal(err)
		}
	}
	return m, tel, mid, missionDBPath
}

func TestRecordThoughtVerbatimUnderCap(t *testing.T) {
	m, tel, mid, _ := openThoughtFixtures(t, true)
	text := "I should check the schema before writing the migration."
	recordThought(tel, m, Thought{Agent: "bee1", Role: "builder", MissionID: mid, Text: text})

	evs, err := tel.EventsForMission(mid)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Fatalf("want 1 telemetry event, got %d", len(evs))
	}
	if evs[0].Kind != "thought" {
		t.Fatalf("kind = %q, want thought", evs[0].Kind)
	}
	if evs[0].Actor != "bee1" {
		t.Fatalf("actor = %q, want bee1", evs[0].Actor)
	}
	got, _ := evs[0].Detail["text"].(string)
	if got != text {
		t.Fatalf("stored text = %q, want verbatim %q", got, text)
	}
	if role, _ := evs[0].Detail["role"].(string); role != "builder" {
		t.Fatalf("role = %q, want builder", role)
	}
}

func TestRecordThoughtTruncatesOverCapPreservingPrefix(t *testing.T) {
	m, tel, mid, _ := openThoughtFixtures(t, true)
	long := strings.Repeat("reasoning ", 100) // far over 600 chars
	recordThought(tel, m, Thought{Agent: "bee1", Role: "builder", MissionID: mid, Text: long})

	evs, err := tel.EventsForMission(mid)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Fatalf("want 1 telemetry event, got %d", len(evs))
	}
	stored, _ := evs[0].Detail["text"].(string)
	if !strings.HasSuffix(stored, "...") {
		t.Fatalf("truncated thought must end with ... marker, got tail %q", stored[len(stored)-10:])
	}
	prefix := strings.TrimSuffix(stored, "...")
	if !strings.HasPrefix(long, prefix) {
		t.Fatalf("truncated prefix must be an exact prefix of the original — no synthesis; prefix=%q", prefix)
	}
	if len(stored) >= len(long) {
		t.Fatalf("stored thought (%d chars) must be shorter than the original (%d chars)", len(stored), len(long))
	}
}

func TestRecordThoughtNoOpWhenRecordStoryOff(t *testing.T) {
	m, tel, mid, _ := openThoughtFixtures(t, false) // default: opt-in off
	recordThought(tel, m, Thought{Agent: "bee1", Role: "builder", MissionID: mid, Text: "a thought"})

	evs, err := tel.EventsForMission(mid)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 0 {
		t.Fatalf("record_story=false must record nothing, got %d events: %+v", len(evs), evs)
	}
}

func TestRecordThoughtRecordsWhenRecordStoryOn(t *testing.T) {
	m, tel, mid, _ := openThoughtFixtures(t, true)
	recordThought(tel, m, Thought{Agent: "bee1", Role: "builder", MissionID: mid, Text: "a thought"})

	evs, err := tel.EventsForMission(mid)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Fatalf("record_story=true must record the thought, got %d events", len(evs))
	}
}

// TestRecordThoughtRoutesToTelemetryNotCoordination confirms thought data
// lands ONLY in the DuckDB telemetry/analytics store, never in the
// coordination SQLite mission database — introspecting the raw sqlite file
// for any "thought" table or column, and confirming the missions/phases
// tables carry no thought text.
func TestRecordThoughtRoutesToTelemetryNotCoordination(t *testing.T) {
	m, tel, mid, missionDBPath := openThoughtFixtures(t, true)
	text := "coordination store must never see this text"
	recordThought(tel, m, Thought{Agent: "bee1", Role: "builder", MissionID: mid, Text: text})

	// Telemetry (DuckDB) got it.
	evs, err := tel.EventsForMission(mid)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Kind != "thought" {
		t.Fatalf("telemetry must hold the thought event, got %+v", evs)
	}

	// Coordination SQLite must not: no table named/containing "thought", and
	// no row in missions/phases contains the reported text.
	db, err := sql.Open("sqlite", missionDBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.ToLower(name), "thought") {
			t.Fatalf("coordination SQLite must have no thought-related table, found %q", name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM missions WHERE directive LIKE ? OR instr(directive, ?) > 0`, "%"+text+"%", text).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("coordination SQLite missions table must not carry thought text, found %d matching rows", n)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM phases WHERE instr(instruction, ?) > 0`, text).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("coordination SQLite phases table must not carry thought text, found %d matching rows", n)
	}
}

func TestTruncateThoughtExactCapAndMarker(t *testing.T) {
	short := "short and sweet"
	if got := truncateThought(short); got != short {
		t.Fatalf("under-cap text must pass through untouched, got %q", got)
	}
	long := strings.Repeat("x", thoughtTextMax+50)
	got := truncateThought(long)
	if !strings.HasSuffix(got, thoughtEllipsis) {
		t.Fatalf("truncated text must end with %q, got tail %q", thoughtEllipsis, got[len(got)-10:])
	}
	if len([]rune(got)) != thoughtTextMax {
		t.Fatalf("truncated text must be exactly %d runes, got %d", thoughtTextMax, len([]rune(got)))
	}
	wantPrefix := strings.Repeat("x", thoughtTextMax-len(thoughtEllipsis))
	if !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("truncated text must preserve the original prefix exactly")
	}
}

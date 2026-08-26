// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestRecordSinkStreamsEventsAsTheyHappen pins the property a live cockpit
// needs: an event must be readable WHILE the run is going, not only after it
// ends.
//
// --record writes {"events":[…]} once, at the end, via writeTape. So a
// three-hour audit produced nothing watchable until it finished, and the
// cockpit could only ever replay history. The stream emits each event as
// newline-delimited JSON the instant it is recorded, so a watcher can tail the
// same beats that later become the tape.
func TestRecordSinkStreamsEventsAsTheyHappen(t *testing.T) {
	var stream bytes.Buffer
	r := &recordSink{stream: &stream}

	r.add("pool_subject", "qwen/mutant-generator", "admission.go", map[string]any{"n": 1})
	firstLen := stream.Len()
	if firstLen == 0 {
		t.Fatal("no bytes streamed after the first event — nothing is watchable mid-run")
	}
	r.add("pool_verdict", "gemini/test-writer", "admission.go", map[string]any{"n": 2})

	lines := strings.Split(strings.TrimSpace(stream.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("streamed %d line(s), want 2 (one JSON object per event)", len(lines))
	}
	if stream.Len() <= firstLen {
		t.Fatal("second event did not append — the stream is not incremental")
	}

	var ev recEvent
	if err := json.Unmarshal([]byte(lines[0]), &ev); err != nil {
		t.Fatalf("streamed line is not valid JSON: %v", err)
	}
	if ev.Kind != "pool_subject" || ev.Actor != "qwen/mutant-generator" {
		t.Fatalf("streamed event = %+v, want the recorded beat", ev)
	}
	if ev.Ts != 1 {
		t.Errorf("streamed ts = %d, want the same monotonic ts the tape uses", ev.Ts)
	}
}

// The stream must carry exactly what the tape carries — a live view that
// diverges from the artifact it previews is worse than no live view.
func TestStreamedEventsMatchTheTape(t *testing.T) {
	var stream bytes.Buffer
	r := &recordSink{stream: &stream}
	r.add("task_started", "a", "s", map[string]any{"k": "v"})
	r.add("task_done", "b", "s", nil)

	path := t.TempDir() + "/tape.json"
	if err := r.writeTape(path); err != nil {
		t.Fatal(err)
	}
	var tape struct {
		Events []recEvent `json:"events"`
	}
	raw, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if err := json.Unmarshal(raw, &tape); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(stream.String()), "\n")
	if len(lines) != len(tape.Events) {
		t.Fatalf("stream has %d event(s), tape has %d", len(lines), len(tape.Events))
	}
	for i, ln := range lines {
		var ev recEvent
		if err := json.Unmarshal([]byte(ln), &ev); err != nil {
			t.Fatalf("line %d: %v", i, err)
		}
		if ev.Kind != tape.Events[i].Kind || ev.Ts != tape.Events[i].Ts || ev.Actor != tape.Events[i].Actor {
			t.Fatalf("event %d diverges: stream %+v vs tape %+v", i, ev, tape.Events[i])
		}
	}
}

// A nil stream is the ordinary case and must stay free.
func TestRecordSinkWithoutStreamIsUnaffected(t *testing.T) {
	r := &recordSink{}
	r.add("k", "a", "s", nil)
	if len(r.events) != 1 {
		t.Fatalf("events = %d, want 1", len(r.events))
	}
}

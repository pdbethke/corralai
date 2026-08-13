// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"strings"
	"testing"
)

// The beats always existed — they were captured for REPLAY and never shown, so
// a run printed a few lines and then went silent for minutes while eight seats
// worked. That reads as a hang, not as work, and it is the first impression a
// newcomer forms.
func TestLiveEchoRendersTheBeatsWorthWatching(t *testing.T) {
	var buf bytes.Buffer
	r := &recordSink{live: &buf}

	r.add("pool_subject", "corral-advpool", "kirby/characters/xp_service.py", nil)
	r.add("task_claimed", "mutant-generator/3", "seat 3", nil)
	r.add("task_done", "mutant-generator/3", "seat 3", map[string]any{"mutants": 5})
	r.add("pool_dev_adequacy", "corral-advpool", "x", map[string]any{"killed": 22, "mutants_total": 39})
	r.add("pool_verdict", "corral-advpool", "x", nil)

	got := buf.String()
	for _, want := range []string{
		"auditing kirby/characters/xp_service.py",
		"mutant-generator/3 started",
		"produced 5 mutant(s)",
		"killed 22 of 39",
		"verdict signed",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("live echo missing %q; got:\n%s", want, got)
		}
	}

	// A genuinely long path is shortened to its base name so the echo stays
	// readable in a narrow terminal; a short one is shown whole, because the
	// directory is information when it fits.
	var long bytes.Buffer
	(&recordSink{live: &long}).add("pool_subject", "corral-advpool",
		"packages/service/internal/domain/pricing/calculator_service.py", nil)
	if !strings.Contains(long.String(), "auditing calculator_service.py") {
		t.Errorf("a long path should render as its base name; got: %s", long.String())
	}

	// Every beat is still recorded for the tape — the echo is an observer, not
	// a filter. A --record tape that lost events because they were unprintable
	// would be a replay that disagrees with the run.
	if len(r.events) != 5 {
		t.Fatalf("recorded %d events, want all 5 — the live echo must not drop any", len(r.events))
	}
}

// A firehose tells a newcomer less than silence did, so beats that only matter
// to a replay stay off the terminal.
func TestLiveEchoIsSelective(t *testing.T) {
	var buf bytes.Buffer
	r := &recordSink{live: &buf}
	r.add("pool_matrix", "corral-advpool", "x", map[string]any{"tests_total": 9})
	r.add("some_future_kind", "x", "y", nil)
	if buf.Len() != 0 {
		t.Fatalf("replay-only beats must not reach the terminal; got:\n%s", buf.String())
	}
	if len(r.events) != 2 {
		t.Fatalf("they must still be RECORDED: got %d events, want 2", len(r.events))
	}
}

// --quiet turns the echo off without touching the tape.
func TestNilLiveWriterIsSilentButStillRecords(t *testing.T) {
	r := &recordSink{}
	r.add("pool_verdict", "corral-advpool", "x", nil)
	if len(r.events) != 1 {
		t.Fatalf("got %d events, want 1", len(r.events))
	}
}

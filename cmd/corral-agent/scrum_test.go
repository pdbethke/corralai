// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"strings"
	"testing"
)

func TestScrumFactsStallAndNudge(t *testing.T) {
	now := 10_000.0
	tasks := []scrumTask{
		{ID: 1, Role: "builder", Title: "build", Status: "claimed", ClaimedBy: "Bob", ClaimedTS: now - 600},
		{ID: 2, Role: "tester", Title: "test", Status: "pending"},
		{ID: 3, Role: "writer", Title: "docs", Status: "done"},
	}
	standup, nudges := scrumFacts(tasks, nil, now, 240)
	if !strings.Contains(standup, "1/3 done") {
		t.Fatalf("standup should report progress, got %q", standup)
	}
	if !strings.Contains(standup, "Bob has held task #1") {
		t.Fatalf("standup should name the slacker, got %q", standup)
	}
	if len(nudges) != 1 || nudges[0].Holder != "Bob" || nudges[0].TaskID != 1 {
		t.Fatalf("expected one nudge at Bob for task 1, got %v", nudges)
	}
	if !strings.Contains(nudges[0].Text, "10min") {
		t.Fatalf("nudge should say how long, got %q", nudges[0].Text)
	}
}

func TestScrumFactsFreshClaimIsNotStalled(t *testing.T) {
	now := 10_000.0
	tasks := []scrumTask{
		{ID: 1, Role: "builder", Title: "build", Status: "claimed", ClaimedBy: "Bob", ClaimedTS: now - 60},
	}
	standup, nudges := scrumFacts(tasks, nil, now, 240)
	if len(nudges) != 0 {
		t.Fatalf("a 1-minute-old claim is not a stall: %v", nudges)
	}
	if strings.Contains(standup, "held task") {
		t.Fatalf("standup should not call out a fresh claim, got %q", standup)
	}
}

func TestScrumFactsStarvation(t *testing.T) {
	now := 10_000.0
	tasks := []scrumTask{
		{ID: 1, Role: "builder", Title: "build", Status: "ready"},
		{ID: 2, Role: "builder", Title: "fix", Status: "ready"},
	}
	agents := []scrumAgent{{Name: "Bob", Role: "builder", Status: "idle"}}
	standup, _ := scrumFacts(tasks, agents, now, 240)
	if !strings.Contains(standup, "2 builder task(s) ready with idle builders") {
		t.Fatalf("standup should flag starvation, got %q", standup)
	}
}

func TestScrumFactsQuietWhenEmpty(t *testing.T) {
	standup, nudges := scrumFacts(nil, nil, 10_000, 240)
	if standup != "" || len(nudges) != 0 {
		t.Fatalf("no tasks → no standup, got %q %v", standup, nudges)
	}
}

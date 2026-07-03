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
	standup, nudges := scrumFacts(tasks, nil, now, 240, 0)
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
	standup, nudges := scrumFacts(tasks, nil, now, 240, 0)
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
	standup, _ := scrumFacts(tasks, agents, now, 240, 0)
	if !strings.Contains(standup, "2 builder task(s) ready with idle builders") {
		t.Fatalf("standup should flag starvation, got %q", standup)
	}
}

func TestScrumFactsQuietWhenEmpty(t *testing.T) {
	standup, nudges := scrumFacts(nil, nil, 10_000, 240, 0)
	if standup != "" || len(nudges) != 0 {
		t.Fatalf("no tasks → no standup, got %q %v", standup, nudges)
	}
}

// TestScrumFactsAnnouncesPendingProposals: when the learning loop has
// proposals awaiting the operator's approve/reject decision, Shep's standup
// names the count — the human gate of the learning loop shouldn't go unnoticed
// just because the queue itself is quiet.
func TestScrumFactsAnnouncesPendingProposals(t *testing.T) {
	now := 10_000.0
	tasks := []scrumTask{
		{ID: 1, Role: "builder", Title: "build", Status: "done"},
	}
	standup, _ := scrumFacts(tasks, nil, now, 240, 3)
	if !strings.Contains(standup, "3 skill proposal(s) awaiting the operator") {
		t.Fatalf("standup should announce pending proposals, got %q", standup)
	}
}

// TestScrumFactsAnnouncesProposalsWhenQueueEmpty: proposals pending while the
// queue is quiescent is the learning loop's natural steady state — precisely
// when the operator isn't watching the UI and needs the nudge. An empty task
// list must NOT silence the proposal announcement; only true idle (no tasks
// AND no proposals — TestScrumFactsQuietWhenEmpty above) stays quiet.
func TestScrumFactsAnnouncesProposalsWhenQueueEmpty(t *testing.T) {
	standup, nudges := scrumFacts(nil, nil, 10_000, 240, 2)
	if standup == "" {
		t.Fatal("2 pending proposals with an empty queue should still produce a standup")
	}
	if !strings.Contains(standup, "2 skill proposal(s) awaiting the operator") {
		t.Fatalf("proposals-only standup should announce the count, got %q", standup)
	}
	if len(nudges) != 0 {
		t.Fatalf("no tasks → no nudges, got %v", nudges)
	}
}

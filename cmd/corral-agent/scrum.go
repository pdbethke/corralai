// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// The scrum master is the standup tier: a bee that watches the queue and the
// hive and SAYS what it sees — progress, stalls, starvation — in the live
// console, and nudges slackers via send_instruction. Detection is
// deterministic Go (no model in the loop): the brain's reclaim rules are the
// enforcement floor; the scrum bee is the visible layer that narrates and
// escalates before the floor has to act.

// scrumTask is the slice of list_tasks output the scrum master reasons over.
type scrumTask struct {
	ID        int64   `json:"id"`
	MissionID int64   `json:"mission_id"`
	Role      string  `json:"role"`
	Title     string  `json:"title"`
	Status    string  `json:"status"`
	ClaimedBy string  `json:"claimed_by"`
	ClaimedTS float64 `json:"claimed_ts"`
}

// scrumAgent is the slice of coordination_status the scrum master reasons over.
type scrumAgent struct {
	Name   string `json:"name"`
	Role   string `json:"role"`
	Status string `json:"status"`
}

// scrumNudge is one slacker call-out: who, about which task, and the message.
type scrumNudge struct {
	Holder string
	TaskID int64
	Text   string
}

// scrumFacts distills the standup from raw queue + presence state. Pure —
// covered by unit tests; the loop just delivers what this returns.
// stallAfter is how long a claim may sit before its holder gets named.
// pendingProposals is the count of learning-loop proposals awaiting the
// operator's approve/reject decision (from list_proposals status=pending);
// when >0 it's appended to the standup so the human gate doesn't go unnoticed.
func scrumFacts(tasks []scrumTask, agents []scrumAgent, nowTS float64, stallAfter float64, pendingProposals int) (standup string, nudges []scrumNudge) {
	var done, total int
	var ready []scrumTask
	var stalled []scrumTask
	for _, t := range tasks {
		total++
		switch t.Status {
		case "done":
			done++
		case "ready":
			ready = append(ready, t)
		case "claimed":
			if t.ClaimedTS > 0 && nowTS-t.ClaimedTS > stallAfter {
				stalled = append(stalled, t)
			}
		}
	}
	if total == 0 {
		return "", nil
	}

	idleByRole := map[string][]string{}
	for _, a := range agents {
		if a.Status == "idle" {
			idleByRole[a.Role] = append(idleByRole[a.Role], a.Name)
		}
	}

	parts := []string{fmt.Sprintf("standup: %d/%d done", done, total)}
	for _, t := range stalled {
		mins := int((nowTS - t.ClaimedTS) / 60)
		parts = append(parts, fmt.Sprintf("⚠ %s has held task #%d (%s) for %dmin", t.ClaimedBy, t.ID, t.Title, mins))
		nudges = append(nudges, scrumNudge{
			Holder: t.ClaimedBy, TaskID: t.ID,
			Text: fmt.Sprintf("scrum check-in: you have held task #%d (%s) for %dmin — finish it, release it, or report what's blocking you", t.ID, t.Title, mins),
		})
	}
	// Starvation: ready work whose role has idle hands.
	starving := map[string]int{}
	for _, t := range ready {
		if len(idleByRole[t.Role]) > 0 || len(idleByRole[""]) > 0 {
			starving[t.Role]++
		}
	}
	if len(starving) > 0 {
		roles := make([]string, 0, len(starving))
		for r := range starving {
			roles = append(roles, r)
		}
		sort.Strings(roles)
		for _, r := range roles {
			parts = append(parts, fmt.Sprintf("%d %s task(s) ready with idle %ss", starving[r], r, r))
		}
	}
	if len(parts) == 1 && done == total {
		parts = append(parts, "queue drained — nothing outstanding")
	}
	if pendingProposals > 0 {
		parts = append(parts, fmt.Sprintf("%d skill proposal(s) awaiting the operator", pendingProposals))
	}
	return strings.Join(parts, " · "), nudges
}

// runScrumLoop polls the queue + presence, posts a standup line to the live
// console whenever the picture changes, and nudges the holder of any stalled
// claim (at most once per stall window per task).
func runScrumLoop(name string, brain func(string, map[string]any) string) {
	poll := 20 * time.Second
	if v := os.Getenv("SCRUM_POLL_SECONDS"); v != "" {
		var n int
		if _, _ = fmt.Sscanf(v, "%d", &n); n > 0 {
			poll = time.Duration(n) * time.Second
		}
	}
	stallAfter := 240.0
	if v := os.Getenv("SCRUM_STALL_SECONDS"); v != "" {
		var n int
		if _, _ = fmt.Sscanf(v, "%d", &n); n > 0 {
			stallAfter = float64(n)
		}
	}
	fmt.Printf("[%s/scrum] standup tier online — poll %s, stall threshold %.0fs\n", name, poll, stallAfter)

	lastStandup := ""
	lastNudge := map[int64]time.Time{} // task id → last nudge, throttled to the stall window
	for {
		var tl struct {
			Tasks []scrumTask `json:"tasks"`
		}
		_ = json.Unmarshal([]byte(brain("list_tasks", nil)), &tl)
		var st struct {
			ActiveAgents []scrumAgent `json:"active_agents"`
		}
		_ = json.Unmarshal([]byte(brain("coordination_status", nil)), &st)
		var lp struct {
			Proposals []struct {
				ID int64 `json:"id"`
			} `json:"proposals"`
		}
		_ = json.Unmarshal([]byte(brain("list_proposals", map[string]any{"status": "pending"})), &lp)

		standup, nudges := scrumFacts(tl.Tasks, st.ActiveAgents, float64(time.Now().Unix()), stallAfter, len(lp.Proposals))
		if standup != "" && standup != lastStandup {
			fmt.Printf("[%s/scrum] %s\n", name, standup)
			brain("report_activity", map[string]any{"role": "scrum", "tool": "standup", "detail": standup})
			lastStandup = standup
		}
		for _, n := range nudges {
			if n.Holder == "" || time.Since(lastNudge[n.TaskID]) < time.Duration(stallAfter)*time.Second {
				continue
			}
			lastNudge[n.TaskID] = time.Now()
			fmt.Printf("[%s/scrum] nudging %s about task #%d\n", name, n.Holder, n.TaskID)
			brain("send_instruction", map[string]any{"target": n.Holder, "text": n.Text})
			brain("report_activity", map[string]any{"role": "scrum", "tool": "nudge", "detail": n.Holder + ": " + n.Text})
		}
		brain("heartbeat", map[string]any{"status": "working"})
		time.Sleep(poll)
	}
}

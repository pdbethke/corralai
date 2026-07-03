// SPDX-License-Identifier: Elastic-2.0

package ui

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// chatter is the swarm canvas's in-character speech: one short line, generated
// by the narrator model AS the agent's skin persona ("you are Tess, a sheep in
// the flock"), grounded in the agent's real recorded trail — so the critters
// talk about what they are ACTUALLY doing instead of playing canned lines.
// Read-only observability, same trust boundary as /api/ask, and deliberately
// cheap: per-agent server cache with a TTL, one generation in flight at a time,
// tiny token budget. The UI falls back to canned lines on any non-200.

const chatterTTL = 45 * time.Second

// chatterPersonas is a fixed server-side map — the skin name is the only thing
// the client chooses, so a viewer can't inject an arbitrary persona prompt.
var chatterPersonas = map[string]map[string]string{
	"ranch":  {"": "a hard-working pony", "scrum": "the cattle dog keeping the herd honest", "lead": "the trail boss", "client": "the ranch owner"},
	"flock":  {"": "a diligent sheep", "scrum": "the sheepdog keeping the flock honest", "lead": "the head shepherd", "client": "the farm owner"},
	"matrix": {"": "an Agent inside the construct", "scrum": "the operator watching the feeds", "lead": "the architect's deputy", "client": "the oracle"},
	"hive":   {"": "a busy worker bee", "scrum": "the drill-sergeant bee", "lead": "the lead bee", "client": "the queen's client"},
}

type chatterEntry struct {
	line string
	ts   time.Time
}

var (
	chatterMu    sync.Mutex
	chatterCache = map[string]chatterEntry{}
	chatterBusy  bool
)

func (s *Server) chatter(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if s.narrator == nil || !s.narrator.Available() {
		http.Error(w, "narrator unavailable", http.StatusServiceUnavailable)
		return
	}
	agent := strings.TrimSpace(r.URL.Query().Get("agent"))
	skinName := r.URL.Query().Get("skin")
	personas, ok := chatterPersonas[skinName]
	if agent == "" || !ok {
		http.Error(w, "agent and a known skin required", http.StatusBadRequest)
		return
	}
	key := agent + "|" + skinName

	chatterMu.Lock()
	if e, hit := chatterCache[key]; hit && time.Since(e.ts) < chatterTTL {
		chatterMu.Unlock()
		writeJSON(w, map[string]string{"line": e.line})
		return
	}
	if chatterBusy { // one generation at a time — viewers can't stampede the model
		chatterMu.Unlock()
		http.Error(w, "busy", http.StatusServiceUnavailable)
		return
	}
	chatterBusy = true
	chatterMu.Unlock()
	defer func() { chatterMu.Lock(); chatterBusy = false; chatterMu.Unlock() }()

	role := s.roleOf(agent)
	persona := personas[role]
	if persona == "" {
		persona = personas[""]
	}
	trail := s.buildTrail(agent, role, "")
	if len(trail) > 1200 {
		trail = trail[len(trail)-1200:]
	}

	system := fmt.Sprintf(`You are %q — %s — the %s in a corral of coding agents.
Say ONE short line (8 words or fewer), in character, about what you are doing RIGHT NOW, grounded strictly in your recorded trail below. First person. No quotes, no emoji, no preamble.`, agent, persona, role)
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	line, err := s.narrator.Ask(ctx, system, "YOUR RECORDED TRAIL:\n"+trail)
	if err != nil {
		http.Error(w, "narrator error", http.StatusBadGateway)
		return
	}
	line = strings.Trim(strings.TrimSpace(line), `"'“”`)
	if i := strings.IndexByte(line, '\n'); i > 0 {
		line = line[:i]
	}
	if len(line) > 80 {
		line = line[:79] + "…"
	}

	chatterMu.Lock()
	chatterCache[key] = chatterEntry{line: line, ts: time.Now()}
	chatterMu.Unlock()
	writeJSON(w, map[string]string{"line": line})
}

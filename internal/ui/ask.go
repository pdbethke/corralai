// SPDX-License-Identifier: Elastic-2.0

package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/auth"
	"github.com/pdbethke/corralai/internal/coord"
)

// ask is the read-only "talk to a bee" debrief. It is NOT a control channel: it
// gathers the agent's RECORDED trail (tool-calls, commands, findings, completions
// — all append-only observability the brain already holds) and asks the narrator
// model to answer AS that bee, grounded only in what's on record. It never touches
// the task queue, claims, or the bee's own work loop, so it cannot derail a build.
func (s *Server) ask(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	// A read-only observer may VIEW the swarm but never act; invoking the
	// narrator model is an action (cost + a model call the observer's MCP
	// ask_fleet equivalent is already denied). Gate before touching the model.
	if auth.ReadOnly(r) {
		http.Error(w, "forbidden: read-only observer token cannot act", http.StatusForbidden)
		return
	}
	if s.narrator == nil || !s.narrator.Available() {
		http.Error(w, "narrator unavailable (no model backend configured for the brain)", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Agent    string `json:"agent"`
		Question string `json:"question"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Agent) == "" || strings.TrimSpace(body.Question) == "" {
		http.Error(w, "agent and question required", http.StatusBadRequest)
		return
	}

	agent := body.Agent
	role := s.roleOf(agent)
	trail := s.buildTrail(agent, role, body.Question)

	// Skin-aware persona, same server-fixed pattern as /api/chatter (a viewer
	// picks a KNOWN skin name, never an arbitrary persona string). Unknown or
	// missing ?skin= falls back to "ranch" — the corral is the default voice.
	persona, group, _ := resolveSkinPersona(r.URL.Query().Get("skin"), role)
	system := buildAskPrompt(agent, persona, group)

	user := body.Question + "\n\nYOUR RECORDED TRAIL:\n" + trail

	ctx, cancel := context.WithTimeout(r.Context(), 55*time.Second)
	defer cancel()
	answer, err := s.narrator.Ask(ctx, system, user)
	if err != nil {
		http.Error(w, "narrator error: "+err.Error(), http.StatusBadGateway)
		return
	}
	if answer == "" {
		answer = "(no answer)"
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"agent": agent, "answer": answer})
}

// buildAskPrompt is the ask/debrief system prompt, factored pure for tests.
func buildAskPrompt(agent, persona, group string) string {
	return fmt.Sprintf(`You ARE %q — %s — in the %s, giving a brief, read-only debrief about your OWN work.
Answer the user's question ONLY from YOUR RECORDED TRAIL below — the real tool-calls, commands, findings, and completions on record for you.
Speak in the first person ("I ran…", "I'm waiting on…"), concretely and concisely (2–5 sentences).
If the trail does not contain the answer, say so plainly — NEVER invent actions you have no record of.
VOICE: stay inside YOUR persona's universe even if the trail text below uses other metaphors (bee/hive/herd/flock/construct/etc.) — translate, don't import them. If you riff on your name or role, do it in your persona's universe.
You are a debrief, not a controller: you cannot take new actions or change the mission.`, agent, persona, group)
}

// roleOf resolves an agent's role from the host book (preferred) or live presence.
func (s *Server) roleOf(agent string) string {
	if s.hosts != nil {
		for _, h := range s.hosts.List() {
			if h.Agent == agent && h.Role != "" {
				return h.Role
			}
		}
	}
	if s.coord != nil {
		if st, err := s.coord.CoordinationStatus(coord.PresenceWindow); err == nil && st != nil {
			for _, a := range st.ActiveAgents {
				if a.Name == agent {
					return a.Role
				}
			}
		}
	}
	return "agent"
}

// buildTrail assembles the agent's recorded history into a compact grounding block.
// Every section is capped so the prompt stays bounded; all data is read-only.
func (s *Server) buildTrail(agent, role, question string) string {
	var b strings.Builder
	who := "WHO: " + agent + " · role " + role
	if s.hosts != nil {
		for _, h := range s.hosts.List() {
			if h.Agent == agent {
				who += fmt.Sprintf(" · model %s:%s · host %s · jail %s", h.Backend, h.Model, h.Host, h.Jail)
				break
			}
		}
	}
	b.WriteString(who + "\n")

	// Current / recent tasks owned by this bee.
	if s.queue != nil {
		if tasks, err := s.queue.Active(); err == nil {
			cur := []string{}
			for _, t := range tasks {
				if t.ClaimedBy == agent {
					cur = append(cur, fmt.Sprintf("%s %q [%s]", t.Key, t.Title, t.Status))
				}
			}
			if len(cur) > 0 {
				b.WriteString("CURRENT TASK(S): " + strings.Join(cur, "; ") + "\n")
			} else {
				b.WriteString("CURRENT TASK(S): none claimed right now\n")
			}
		}
	}

	// DURABLE command history (newest first) — the full record from the executions
	// table, NOT the capped in-memory ring, so every command of a long build survives
	// for the post-mortem.
	if s.queue != nil {
		if exs, err := s.queue.ExecutionsByAgent(agent, 30); err == nil && len(exs) > 0 {
			lines := make([]string, 0, len(exs))
			for _, e := range exs {
				status := "exit " + itoa(e.ExitCode)
				if e.OK {
					status = "ok"
				}
				lines = append(lines, "  "+oneLine(e.Command)+"  -> "+status)
			}
			b.WriteString("COMMANDS I RAN (durable, newest first):\n" + strings.Join(lines, "\n") + "\n")
		}
	}

	// DURABLE event timeline — claims, completions, findings, re-plans across the
	// WHOLE build (survives ring eviction; the backbone of a long-build post-mortem).
	if s.tel != nil {
		if tl, err := s.tel.AgentTimeline(agent, 30); err == nil && len(tl) > 0 {
			lines := make([]string, 0, len(tl))
			for _, e := range tl {
				ln := "  " + e.Kind
				if e.Subject != "" {
					ln += " " + e.Subject
				}
				if e.Detail != "" && e.Detail != "{}" {
					ln += " " + oneLine(e.Detail)
				}
				lines = append(lines, ln)
			}
			b.WriteString("MY TIMELINE (durable, newest first):\n" + strings.Join(lines, "\n") + "\n")
		}
	}

	// Most-recent tool-calls from the live ring (search_memory, write_file, …). These
	// aren't durable — they show only what's still in the window — so they supplement
	// the durable history above with the freshest detail.
	if s.acts != nil {
		lines := []string{}
		for _, a := range s.acts.Recent() {
			if a.Agent != agent {
				continue
			}
			lines = append(lines, "  "+a.Tool+" "+oneLine(a.Detail))
			if len(lines) >= 15 {
				break
			}
		}
		if len(lines) > 0 {
			b.WriteString("RECENT TOOL-CALLS (live, newest first):\n" + strings.Join(lines, "\n") + "\n")
		}
	}

	// Findings this bee raised.
	if s.queue != nil {
		if fs, err := s.queue.AllFindings(); err == nil {
			lines := []string{}
			for _, f := range fs {
				if f.Reporter != agent {
					continue
				}
				lines = append(lines, fmt.Sprintf("  %s %s on %s — %s", f.Severity, f.Type, f.Target, oneLine(f.Evidence)))
				if len(lines) >= 8 {
					break
				}
			}
			if len(lines) > 0 {
				b.WriteString("FINDINGS I RAISED:\n" + strings.Join(lines, "\n") + "\n")
			}
		}
	}

	// Things this bee completed (from coordination's recent-completed window).
	if s.coord != nil {
		if st, err := s.coord.CoordinationStatus(coord.PresenceWindow); err == nil && st != nil {
			lines := []string{}
			for _, c := range st.RecentCompleted {
				if c.AgentName != agent {
					continue
				}
				ln := "  " + oneLine(c.Summary)
				if len(c.Paths) > 0 {
					ln += " [" + strings.Join(c.Paths, ", ") + "]"
				}
				lines = append(lines, ln)
				if len(lines) >= 8 {
					break
				}
			}
			if len(lines) > 0 {
				b.WriteString("RECENTLY COMPLETED:\n" + strings.Join(lines, "\n") + "\n")
			}
		}
	}

	if b.Len() == len(who)+1 {
		b.WriteString("(no recorded activity yet)\n")
	}

	// Hive-mind recall: semantically (hybrid) search the WHOLE corpus for the
	// question, flag the asked agent's own notes vs the hive's. mem.Search is hybrid
	// when an embedder is configured, keyword otherwise (graceful).
	if s.mem != nil && strings.TrimSpace(question) != "" {
		if hits, err := s.mem.Search(question, "", "", 5, false); err == nil && len(hits) > 0 {
			var lines []string
			for _, h := range hits {
				attrib := "hive: " + h.Author
				if h.Author == agent {
					attrib = "your own"
				} else if h.Author == "" {
					attrib = "hive"
				}
				lines = append(lines, "  "+h.Slug+" ("+attrib+") — "+oneLine(h.Description))
			}
			b.WriteString("RELEVANT MEMORIES (hive-mind recall):\n" + strings.Join(lines, "\n") + "\n")
		}
	}

	return b.String()
}

func oneLine(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\t", " ")
	if len(s) > 140 {
		s = s[:139] + "…"
	}
	return strings.TrimSpace(s)
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }

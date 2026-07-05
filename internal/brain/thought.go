// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pdbethke/corralai/internal/mission"
	"github.com/pdbethke/corralai/internal/telemetry"
)

// Thought is one beat of an agent's own reasoning, reported for the story
// engine — the future console's "watch the agents reason" stream.
//
// INVARIANT: thought text is stored VERBATIM from the agent, never rewritten,
// summarized, or synthesized by the brain — the only transformation ever
// applied is the length cap in truncateThought. This stream's whole value is
// that it's the agent's REAL words; anything else would be theatre wearing
// the agent's name.
type Thought struct {
	Agent     string `json:"agent"`
	Role      string `json:"role"`
	MissionID int64  `json:"mission_id"`
	Text      string `json:"text"`
	TS        int64  `json:"ts"` // Unix seconds
}

// thoughtTextMax bounds a recorded thought's text, in runes. Agents can think
// at length; the cap protects the durable log the same way agentActivityCap
// protects it from volume — this protects it from a single oversized entry.
const thoughtTextMax = 600

// thoughtEllipsis marks a truncated thought so replay never reads a cut
// thought as one that ended there naturally.
const thoughtEllipsis = "..."

// truncateThought caps s to thoughtTextMax runes, appending thoughtEllipsis
// when cut. The surviving prefix is untouched — copied rune-for-rune from s —
// so truncation never doubles as a rewrite: it only shortens.
func truncateThought(s string) string {
	if utf8.RuneCountInString(s) <= thoughtTextMax {
		return s
	}
	cut := thoughtTextMax - utf8.RuneCountInString(thoughtEllipsis)
	if cut < 0 {
		cut = 0
	}
	r := []rune(s)
	return string(r[:cut]) + thoughtEllipsis
}

// recordThought durably records a thought beat, gated by the mission's
// record_story opt-in (default false — see mission.Mission.RecordStory).
// No-op when tel or missions is nil, when missionID is unset, or when the
// mission can't be resolved or hasn't opted in: a normal mission pays zero
// storage cost for thought beats. Routing: this ONLY ever writes to tel (the
// DuckDB analytics/telemetry store) via the shared rec() helper — it never
// touches the coordination SQLite hot path (missions is consulted read-only,
// solely to check the opt-in flag).
func recordThought(tel *telemetry.Store, missions *mission.Store, t Thought) {
	if tel == nil || missions == nil || t.MissionID == 0 {
		return
	}
	m, err := missions.Mission(t.MissionID)
	if err != nil || m == nil || !m.RecordStory {
		return
	}
	rec(tel, t.MissionID, "thought", t.Agent, "", map[string]any{
		"role": t.Role,
		"text": truncateThought(t.Text),
	})
}

// registerThought registers the report_thought MCP tool against s. When
// missions is nil (mission orchestration disabled on this brain) the
// function is a no-op — there is no mission to opt a thought into.
func registerThought(s *mcp.Server, missions *mission.Store, opts Options) {
	if missions == nil {
		return
	}
	type reportThoughtIn struct {
		Name      string `json:"name"`
		Role      string `json:"role,omitempty"`
		Text      string `json:"text" jsonschema:"a substantive piece of your OWN reasoning, in your own words — reported verbatim, never rewritten or summarized by the brain (only length-capped)"`
		MissionID int64  `json:"mission_id" jsonschema:"the mission this thought belongs to; recorded only if that mission opted in via record_story (default off)"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "report_thought",
		Description: "Report a piece of your own reasoning, verbatim, for the story engine. Opt-in per mission (record_story, default off) — on a mission that hasn't opted in this is a silent no-op, so normal missions pay no cost. The brain NEVER rewrites, summarizes, or synthesizes what you send; it is only length-bounded. Best-effort: never blocks or alters your work.",
	}, func(_ context.Context, req *mcp.CallToolRequest, in reportThoughtIn) (*mcp.CallToolResult, okOut, error) {
		recordThought(opts.Telemetry, missions, Thought{
			Agent:     identity(req, in.Name),
			Role:      in.Role,
			MissionID: in.MissionID,
			Text:      in.Text,
			TS:        time.Now().Unix(),
		})
		return nil, okOut{OK: true}, nil
	})
}

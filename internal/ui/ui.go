// SPDX-License-Identifier: Elastic-2.0

// Package ui serves the live "swarm" view: agents as nodes, claims as edges,
// presence as pulse. State is pushed to the browser over Server-Sent Events
// (stdlib, one-way — exactly what a live diagram needs), and the page is embedded
// in the binary via go:embed (the UI ships inside the single binary).
package ui

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/artifacts"
	"github.com/pdbethke/corralai/internal/auth"
	"github.com/pdbethke/corralai/internal/brain"
	"github.com/pdbethke/corralai/internal/coord"
	"github.com/pdbethke/corralai/internal/gateway"
	"github.com/pdbethke/corralai/internal/learn"
	"github.com/pdbethke/corralai/internal/llm"
	"github.com/pdbethke/corralai/internal/memory"
	"github.com/pdbethke/corralai/internal/mission"
	"github.com/pdbethke/corralai/internal/oracle"
	"github.com/pdbethke/corralai/internal/principals"
	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/rolemodel"
	"github.com/pdbethke/corralai/internal/telemetry"
)

//go:embed web
var webFS embed.FS

type Server struct {
	coord           *coord.Store
	mem             *memory.Store
	gw              *gateway.Store
	bus             *coord.Bus
	memOwners       map[string]bool
	roles           *principals.Store
	queue           *queue.Store
	missions        *mission.Store
	execs           *brain.ExecRing
	acts            *brain.ActivityRing
	hosts           *brain.HostBook
	health          *brain.HealthBook
	narrator        *llm.Client
	tel             *telemetry.Store
	oracle          *oracle.Client
	roleModels      rolemodel.Policy
	learn           *learn.Store
	promote         func(id int64, actor string) error
	reject          func(id int64, reason string) error
	historyFn       func() ([]brain.MissionSummary, error)
	historyDetailFn func(id int64) (*brain.MissionDetail, error)
	replayFn        func(missionID int64) ([]brain.ReplayEvent, error)
	artifacts       *artifacts.Store
}

// Handler returns the UI routes: / (the page), /api/me (the viewer's identity +
// role), /api/state (snapshot JSON), /events (SSE stream), the read-only memory
// browser (/api/memory/*), and the agent's-eye view (/api/agent). All are bearer-
// gated upstream (the `corral-observe` proxy carries the token); memory/agent detail is
// additionally capped by the viewer's own visibility.
// Deps are the stores and config the swarm UI reads from.
type Deps struct {
	Coord     *coord.Store
	Mem       *memory.Store
	Gateway   *gateway.Store
	Bus       *coord.Bus
	MemOwners map[string]bool
	Roles     *principals.Store
	Queue     *queue.Store
	Missions  *mission.Store
	// Executions is the brain's ring of recent real command runs; the UI renders it
	// as the live execution feed. nil => the feed stays empty.
	Executions *brain.ExecRing
	// Activity is the brain's ring of recent bee tool-calls; the UI streams it into
	// the live console so every phase shows motion. nil => no activity stream.
	Activity *brain.ActivityRing
	// Hosts is the brain's per-agent runtime facts; the UI renders it as the
	// topology view (where each bee runs). nil => topology empty.
	Hosts *brain.HostBook
	// Health is the brain's per-agent inferred-health tracker (working|idle|
	// failing — see internal/brain/health.go, #72); /api/state surfaces it
	// per active agent. nil => every agent reports "idle" (degrade-never-block).
	Health *brain.HealthBook
	// Narrator is the brain's read-only LLM used to debrief a bee from its recorded
	// trail (the "ask an agent" feature). nil => the ask endpoint is disabled.
	Narrator *llm.Client
	// Telemetry is the durable event store (DuckDB); the narrator reads an agent's
	// full timeline from it for long-build post-mortems. nil => fall back to rings.
	Telemetry *telemetry.Store
	// Oracle is the fleet oracle (NL→SQL→narrate over MotherDuck). nil or
	// !Enabled() => the /api/ask_fleet endpoint and UI panel return 503 / show
	// a "not configured" message. Mirror of Narrator for the fleet-level query.
	Oracle *oracle.Client
	// RoleModels is the declared role-to-model policy; the UI topology annotates
	// each host with expected model + drift when this is non-nil. nil => no drift
	// annotations (degrade-never-block).
	RoleModels rolemodel.Policy
	// Learn is the learning-loop proposals store; /api/state surfaces pending
	// proposals from it for the proposals card. nil => the card stays empty.
	Learn *learn.Store
	// Promote fans a proposal's guidance/skill out into standing memory + the
	// fleet artifact store (the same fan-out the approve_proposal MCP tool
	// runs — see brain.ApproveProposal). actor is the verified principal
	// (auth on) or "operator" (dev-mode fallback — see proposalApprove).
	// Wired in cmd/corral/main.go. nil => the approve endpoint returns 404.
	Promote func(id int64, actor string) error
	// Reject dismisses a pending proposal, recording the reason. Wired in
	// cmd/corral/main.go. nil => the reject endpoint returns 404.
	Reject func(id int64, reason string) error
	// History lists past (non-running) missions for the Completed tab. nil =>
	// the tab renders empty (feature disabled, never a 500).
	History func() ([]brain.MissionSummary, error)
	// HistoryDetail drills into one mission's phases/tasks/findings/executions.
	// Returns (nil, nil) for an unknown id — the handler turns that into 404.
	HistoryDetail func(id int64) (*brain.MissionDetail, error)
	// Replay reconstructs a mission's whole build from durable rows for
	// playback on the canvas. nil => /api/replay is disabled (404).
	Replay func(missionID int64) ([]brain.ReplayEvent, error)
	// Artifacts is the fleet's shared skill/hook store — the same one `corral
	// sync` reads/writes. The UI only ever READS it (/api/skills, the Skills
	// tab, the agent inspector's fleet-skills section) — publishing stays an
	// MCP-only, isHumanAdmin-gated action (see internal/brain/artifacts.go).
	// nil => the Skills tab and inspector section render empty, never a 500.
	Artifacts *artifacts.Store
}

func Handler(d Deps) http.Handler {
	s := &Server{coord: d.Coord, mem: d.Mem, gw: d.Gateway, bus: d.Bus, memOwners: d.MemOwners, roles: d.Roles, queue: d.Queue, missions: d.Missions, execs: d.Executions, acts: d.Activity, hosts: d.Hosts, health: d.Health, narrator: d.Narrator, tel: d.Telemetry, oracle: d.Oracle, roleModels: d.RoleModels, learn: d.Learn, promote: d.Promote, reject: d.Reject, historyFn: d.History, historyDetailFn: d.HistoryDetail, replayFn: d.Replay, artifacts: d.Artifacts}
	mux := http.NewServeMux()
	sub, _ := fs.Sub(webFS, "web")
	mux.Handle("/", http.FileServer(http.FS(sub)))
	mux.HandleFunc("/api/me", s.me)
	mux.HandleFunc("/api/state", s.state)
	mux.HandleFunc("/events", s.events)
	mux.HandleFunc("/api/memory/stats", s.memStats)
	mux.HandleFunc("/api/memory/search", s.memSearch)
	mux.HandleFunc("/api/memory/get", s.memGet)
	mux.HandleFunc("/api/agent", s.agentDetail)
	mux.HandleFunc("/api/skills", s.skills)
	mux.HandleFunc("/api/instruct", s.instruct)
	mux.HandleFunc("/api/ask", s.ask)
	mux.HandleFunc("/api/ask_fleet", s.askFleet)
	mux.HandleFunc("/api/chatter", s.chatter)
	mux.HandleFunc("/api/review", s.review)
	mux.HandleFunc("/api/proposal/approve", s.proposalApprove)
	mux.HandleFunc("/api/proposal/reject", s.proposalReject)
	mux.HandleFunc("/api/history", s.history)
	mux.HandleFunc("/api/history/", s.historyDetail)
	mux.HandleFunc("/api/replay", s.replay)
	mux.HandleFunc("/api/leaderboard", s.leaderboard)
	mux.HandleFunc("/api/mission/footprint", s.footprint)
	mux.HandleFunc("/api/mission/prune", s.prune)
	return mux
}

// isSuperuser mirrors me()'s permissive-dev-mode rule: a nil roles store
// (dev, no Principals configured) is wide open; otherwise the verified
// principal must be a seeded superuser. Used to gate the proposal
// approve/reject endpoints the same way the approve_proposal/reject_proposal
// MCP tools gate on opts.isAdmin — approving/rejecting from the browser must
// require exactly what approving over MCP requires, not just "not an
// observer" (any non-observer bearer, including agent delegation tokens,
// would otherwise promote guidance fleet-wide).
func (s *Server) isSuperuser(r *http.Request) bool {
	return (s.roles == nil || s.roles.IsSuperuser(auth.Principal(r.Context()))) && !auth.Subagent(r.Context())
}

// proposalApprove promotes a pending learning-loop proposal — the UI's
// approve button. It calls the Promote callback (wired in cmd/corral/main.go
// to brain.ApproveProposal), the same fan-out the approve_proposal MCP tool
// runs, so approving from the browser and approving over MCP behave
// identically. Read-only observers can't act; neither can a non-superuser.
func (s *Server) proposalApprove(w http.ResponseWriter, r *http.Request) {
	if auth.ReadOnly(r) {
		http.Error(w, "forbidden: read-only observer token cannot act", http.StatusForbidden)
		return
	}
	if !s.isSuperuser(r) {
		http.Error(w, "forbidden: superuser only (approval shapes fleet-wide behavior)", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if s.promote == nil {
		http.Error(w, "proposals unavailable", http.StatusNotFound)
		return
	}
	var body struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if body.ID == 0 {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	actor := auth.Principal(r.Context())
	if actor == "" {
		actor = "operator"
	}
	if err := s.promote(body.ID, actor); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// proposalReject dismisses a pending learning-loop proposal — the UI's
// reject button. A reason is accepted but not required (the server stores
// whatever the client sends, including empty). Read-only observers can't
// act; neither can a non-superuser (mirrors reject_proposal's MCP gate).
// Deliberate scope boundary: unlike approve, reject has no actor sink yet —
// brain.RejectProposal stamps no telemetry/log actor, so there's nothing to
// pass the verified principal into. Thread an actor through here (and into
// Reject/brain.RejectProposal) when reject telemetry is added.
func (s *Server) proposalReject(w http.ResponseWriter, r *http.Request) {
	if auth.ReadOnly(r) {
		http.Error(w, "forbidden: read-only observer token cannot act", http.StatusForbidden)
		return
	}
	if !s.isSuperuser(r) {
		http.Error(w, "forbidden: superuser only", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if s.reject == nil {
		http.Error(w, "proposals unavailable", http.StatusNotFound)
		return
	}
	var body struct {
		ID     int64  `json:"id"`
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if body.ID == 0 {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	if err := s.reject(body.ID, body.Reason); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// history lists past (non-running) missions for the Completed tab — a plain
// read, so (unlike approve/reject) no ReadOnly/superuser gate: observers may
// view finished missions same as any other GET (mirrors memStats/agentDetail).
// leaderboard returns the model×role performance matrix (#52): per-cell task
// completions, average duration, verify-gate pass rate, findings raised/
// resolved, rework count, and a sample count so a thin cell isn't mistaken
// for a confident verdict. Read-only, observer-safe like /api/state; a nil
// queue store (dev mode without one configured) renders an empty matrix
// rather than erroring.
func (s *Server) leaderboard(w http.ResponseWriter, r *http.Request) {
	lb, err := brain.BuildLeaderboard(s.queue, s.hosts, s.tel)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, lb)
}

func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	if s.historyFn == nil {
		writeJSON(w, map[string]any{"missions": []any{}})
		return
	}
	ms, err := s.historyFn()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"missions": ms})
}

// historyDetail drills into one mission's phases/tasks/findings/executions —
// the Completed tab's detail pane. Read-only, same as history.
func (s *Server) historyDetail(w http.ResponseWriter, r *http.Request) {
	if s.historyDetailFn == nil {
		http.NotFound(w, r)
		return
	}
	idStr := strings.TrimPrefix(r.URL.Path, "/api/history/")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "bad mission id", http.StatusBadRequest)
		return
	}
	d, err := s.historyDetailFn(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if d == nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, map[string]any{"mission": d})
}

// replay reconstructs one mission's whole build for client-side playback —
// the Completed tab's ▶ replay button. Read-only, same gating as
// history/historyDetail (a finished mission's recorded beats are no more
// sensitive than its summary/detail view already exposed): a missing/
// non-numeric mission param 400s, a store error 500s, nil Deps.Replay 404s
// (feature disabled).
func (s *Server) replay(w http.ResponseWriter, r *http.Request) {
	if s.replayFn == nil {
		http.NotFound(w, r)
		return
	}
	idStr := r.URL.Query().Get("mission")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if idStr == "" || err != nil {
		http.Error(w, "mission query param required", http.StatusBadRequest)
		return
	}
	events, err := s.replayFn(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"events": events})
}

// footprint reports a mission's storage footprint — coordination rows
// (tasks/findings/executions, from the queue store) plus telemetry events
// (including thought beats) — the DB relief valve's read-only view (#66).
// Enough for an operator to see what a mission is costing before deciding to
// export its story (GET /api/replay?mission=N) and prune it. Omit ?mission=
// for the footprint across every mission. Read-only, same trust tier as
// /api/history and /api/replay — available to any bearer, including a
// read-only observer.
func (s *Server) footprint(w http.ResponseWriter, r *http.Request) {
	if s.queue == nil {
		http.NotFound(w, r)
		return
	}
	idStr := r.URL.Query().Get("mission")
	if idStr == "" {
		if s.missions == nil {
			http.NotFound(w, r)
			return
		}
		all, err := brain.MissionFootprintAll(s.missions, s.queue, s.tel)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"missions": all})
		return
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "bad mission id", http.StatusBadRequest)
		return
	}
	fp, err := brain.MissionFootprintOf(s.queue, s.tel, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, fp)
}

// prune DESTRUCTIVELY deletes a mission's records from BOTH the coordination
// store (tasks/findings/executions) and telemetry (events) — reclaiming the
// storage the footprint endpoint reports. The DB relief valve's write half
// (#66): export the mission's tape first (GET /api/replay?mission=N,
// archived to a static file), THEN prune — the lifecycle export -> prune ->
// reclaim. Human-gated exactly like proposalApprove/proposalReject: a
// read-only observer token, or a delegation/worker token (a subagent riding a
// superuser's rolled-up authorization), is refused — only a verified human
// superuser may prune. It never touches a published static recording (those
// are separate files under site/src/data/recordings/*.json, outside either
// live store).
func (s *Server) prune(w http.ResponseWriter, r *http.Request) {
	if auth.ReadOnly(r) {
		http.Error(w, "forbidden: read-only observer token cannot act", http.StatusForbidden)
		return
	}
	if !s.isSuperuser(r) {
		http.Error(w, "forbidden: superuser only (prune is destructive and irreversible)", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if s.queue == nil {
		http.NotFound(w, r)
		return
	}
	var body struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if body.ID == 0 {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	pruned, err := brain.PruneMission(s.queue, s.tel, body.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "pruned": pruned})
}

// me reports the viewer's verified identity and role so the UI can show who's
// signed in and reveal superuser-only controls.
func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	p := auth.Principal(r.Context())
	superuser := s.roles == nil || s.roles.IsSuperuser(p) // nil store (dev) => open
	writeJSON(w, map[string]any{"principal": p, "is_superuser": superuser, "readonly": auth.ReadOnly(r)})
}

// instruct queues an instruction for an agent (issued by the logged-in operator).
func (s *Server) instruct(w http.ResponseWriter, r *http.Request) {
	if auth.ReadOnly(r) {
		http.Error(w, "forbidden: read-only observer token cannot act", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var body struct{ Target, Text string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if body.Target == "" || strings.TrimSpace(body.Text) == "" {
		http.Error(w, "target and text required", http.StatusBadRequest)
		return
	}
	issuer := auth.Principal(r.Context())
	if issuer == "" {
		issuer = "operator"
	}
	id, err := s.coord.SendInstruction(issuer, body.Target, body.Text)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"id": id, "ok": true})
}

// review is the human client's verdict on a mission awaiting review: accept it
// or request changes (which opens the next sprint). Read-only observers can't.
func (s *Server) review(w http.ResponseWriter, r *http.Request) {
	if auth.ReadOnly(r) {
		http.Error(w, "forbidden: read-only observer cannot review", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if s.missions == nil {
		http.Error(w, "missions unavailable", http.StatusNotFound)
		return
	}
	var body struct {
		ID       int64  `json:"id"`
		Accept   bool   `json:"accept"`
		Feedback string `json:"feedback"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	reporter := auth.Principal(r.Context())
	if reporter == "" {
		reporter = "operator"
	}
	mv, err := mission.SubmitReview(s.missions, s.queue, body.ID, body.Accept, body.Feedback, reporter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, mv)
}

// agentDetail returns what one agent sees: its recent activity (live work
// stream), the memory it can recall, and the MCP endpoints it can use — each
// capped by the VIEWER's own visibility so inspecting an agent can't leak a
// private entry/endpoint the viewer isn't entitled to.
func (s *Server) agentDetail(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	out := map[string]any{}
	if s.coord != nil {
		act, _ := s.coord.RecentActivity(name, 25)
		out["activity"] = act
		ins, _ := s.coord.RecentInstructions(name, 6)
		out["instructions"] = ins
	}
	if s.mem != nil {
		_ = s.mem.EnsureBuilt()
		// More restrictive of viewer vs agent visibility.
		agentShared := len(s.memOwners) != 0 && !s.memOwners[name]
		sharedOnly := s.memSharedOnly(r) || agentShared
		sample, _ := s.mem.List("", "", 8, sharedOnly)
		st, _ := s.mem.Stats(sharedOnly)
		out["memory"] = map[string]any{"total": st.Total, "shared_only": sharedOnly, "sample": sample}
	}
	if s.gw != nil {
		viewer := auth.Principal(r.Context())
		agentEps, _ := s.gw.Usable(name)
		viewerEps, _ := s.gw.Usable(viewer)
		vset := map[string]bool{}
		for _, e := range viewerEps {
			vset[e.Name] = true
		}
		caps := []gateway.Endpoint{}
		for _, e := range agentEps {
			if e.Public || vset[e.Name] { // don't leak the agent's private endpoint to a viewer who can't see it
				caps = append(caps, e)
			}
		}
		out["capabilities"] = caps
	}
	// HONEST LIMIT: the brain does not track which skills a given agent has
	// actually equipped/synced — that dedup/equip state lives CLIENT-side
	// (see internal/artifacts/store.go's package doc: sync is bidirectional,
	// but conflict/equip bookkeeping is the client's, not the brain's). So
	// this can only report the FLEET's canonical skill set — what every
	// synced member CAN use, not what this one agent HAS pulled. The UI
	// labels it accordingly ("fleet skills — synced to every member")
	// rather than implying per-agent knowledge the brain doesn't have.
	if s.artifacts != nil {
		if arts, err := s.artifacts.ListKind("skill"); err == nil {
			out["skills"] = toSkillViews(arts)
		}
	}
	writeJSON(w, out)
}

// memSharedOnly: a memory OWNER sees everything (private + shared); any other
// authorized UI session sees only the shared team knowledge base. Empty owners
// (dev/open) => owner-level visibility.
func (s *Server) memSharedOnly(r *http.Request) bool {
	if len(s.memOwners) == 0 {
		return false
	}
	return !s.memOwners[auth.Principal(r.Context())]
}

func (s *Server) memStats(w http.ResponseWriter, r *http.Request) {
	if s.mem == nil {
		http.Error(w, "memory unavailable", http.StatusNotFound)
		return
	}
	st, err := s.mem.Stats(s.memSharedOnly(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, st)
}

func (s *Server) memSearch(w http.ResponseWriter, r *http.Request) {
	if s.mem == nil {
		http.Error(w, "memory unavailable", http.StatusNotFound)
		return
	}
	_ = s.mem.EnsureBuilt()
	sharedOnly := s.memSharedOnly(r)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	scope := r.URL.Query().Get("scope")
	typ := r.URL.Query().Get("type")
	limit := 40
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 {
		limit = n
	}
	var (
		hits []memory.Hit
		err  error
	)
	if q == "" {
		hits, err = s.mem.List(scope, typ, limit, sharedOnly) // browse mode
	} else {
		hits, err = s.mem.Search(q, scope, typ, limit, sharedOnly)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"hits": hits})
}

func (s *Server) memGet(w http.ResponseWriter, r *http.Request) {
	if s.mem == nil {
		http.Error(w, "memory unavailable", http.StatusNotFound)
		return
	}
	e, err := s.mem.Get(r.URL.Query().Get("name"), s.memSharedOnly(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if e == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, e)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// stateView is the live snapshot the UI consumes: the coordination status (its
// fields promoted to the top level) plus the task queue, so the page can render
// the live task list and which bee is assigned to each task.
type stateView struct {
	*coord.Status
	Tasks           []queue.Task          `json:"tasks"`
	Findings        []queue.Finding       `json:"findings"`
	Missions        []mission.Mission     `json:"missions"`
	Executions      []brain.Execution     `json:"recent_executions"`          // newest-first, capped at 40 by the ring
	Activity        []brain.Activity      `json:"recent_activity"`            // newest-first tool-calls across all phases
	Topology        []brain.AnnotatedHost `json:"topology"`                   // per-agent runtime facts (where each bee runs) + drift annotation
	Health          []brain.HealthAgent   `json:"health"`                     // per-agent inferred health (working|idle|failing, #72)
	ModelComparison *telemetry.Report     `json:"model_comparison,omitempty"` // per-model finding volume + confirmation rate
	Proposals       []proposalView        `json:"proposals"`                  // pending learning-loop proposals awaiting the operator
}

// proposalView is the /api/state shape of a pending learning-loop proposal —
// just enough for the card to render (signature, count badge, guidance,
// skill-name chip, status) without leaking the full evidence/rejection
// bookkeeping the internal learn.Proposal carries.
type proposalView struct {
	ID        int64  `json:"id"`
	Signature string `json:"signature"`
	Count     int    `json:"count"`
	Guidance  string `json:"guidance"`
	SkillName string `json:"skill_name"`
	Status    string `json:"status"`
}

func (s *Server) snapshot() stateView {
	st, err := s.coord.CoordinationStatus(coord.PresenceWindow)
	if err != nil || st == nil {
		st = &coord.Status{ActiveAgents: []coord.Agent{}, LiveClaims: []coord.LiveClaim{}, RecentCompleted: []coord.Completed{}}
	}
	tasks := []queue.Task{}
	findings := []queue.Finding{}
	if s.queue != nil {
		if ts, err := s.queue.Active(); err == nil && ts != nil {
			tasks = ts
		}
		if fs, err := s.queue.AllFindings(); err == nil && fs != nil {
			findings = fs
		}
	}
	missions := []mission.Mission{}
	if s.missions != nil {
		if ms, err := s.missions.ListMissions(); err == nil && ms != nil {
			missions = ms
		}
	}
	executions := []brain.Execution{}
	if s.execs != nil {
		if es := s.execs.Recent(); es != nil {
			executions = es
		}
	}
	activity := []brain.Activity{}
	if s.acts != nil {
		if as := s.acts.Recent(); as != nil {
			activity = as
		}
	}
	topology := []brain.AnnotatedHost{}
	if s.hosts != nil {
		if hs := s.hosts.List(); hs != nil {
			topology = brain.AnnotateHosts(hs, s.roleModels)
		}
	}
	health := []brain.HealthAgent{}
	if s.health != nil {
		for _, a := range st.ActiveAgents {
			health = append(health, s.health.Health(a.Name))
		}
	}
	var modelComparison *telemetry.Report
	if s.tel != nil {
		if mc, err := s.tel.RunReport("model_comparison"); err == nil {
			modelComparison = &mc
		}
	}
	proposals := []proposalView{}
	if s.learn != nil {
		if ps, err := s.learn.List(learn.StatusPending); err == nil {
			for _, p := range ps {
				proposals = append(proposals, proposalView{
					ID: p.ID, Signature: p.Signature, Count: p.Count,
					Guidance: p.Guidance, SkillName: p.SkillName, Status: p.Status,
				})
			}
		}
	}
	return stateView{Status: st, Tasks: tasks, Findings: findings, Missions: missions, Executions: executions, Activity: activity, Topology: topology, Health: health, ModelComparison: modelComparison, Proposals: proposals}
}

func (s *Server) state(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.snapshot())
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	f, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	push := func() {
		b, _ := json.Marshal(s.snapshot())
		fmt.Fprintf(w, "data: %s\n\n", b)
		f.Flush()
	}
	// Wake immediately on any coordination action (instant push), plus a slow
	// heartbeat tick so presence aging / claim expiry still refresh.
	var sub <-chan struct{}
	if s.bus != nil {
		var cancel func()
		sub, cancel = s.bus.Subscribe()
		defer cancel()
	}
	push()
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-t.C:
			push()
		case <-sub:
			push()
		}
	}
}

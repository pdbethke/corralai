// SPDX-License-Identifier: Elastic-2.0

// Package ui serves the live "swarm" view: agents as nodes, claims as edges,
// presence as pulse. State is pushed to the browser over Server-Sent Events
// (stdlib, one-way — exactly what a live diagram needs), and the page is embedded
// in the binary via go:embed (the UI ships inside the single binary).
package ui

import (
	"crypto/ed25519"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/artifacts"
	"github.com/pdbethke/corralai/internal/auth"
	"github.com/pdbethke/corralai/internal/brain"
	"github.com/pdbethke/corralai/internal/bugcatch"
	"github.com/pdbethke/corralai/internal/buildstore"
	"github.com/pdbethke/corralai/internal/certverify"
	"github.com/pdbethke/corralai/internal/coord"
	"github.com/pdbethke/corralai/internal/criticscore"
	"github.com/pdbethke/corralai/internal/gateway"
	"github.com/pdbethke/corralai/internal/learn"
	"github.com/pdbethke/corralai/internal/llm"
	"github.com/pdbethke/corralai/internal/matrixstore"
	"github.com/pdbethke/corralai/internal/memory"
	"github.com/pdbethke/corralai/internal/mission"
	"github.com/pdbethke/corralai/internal/oracle"
	"github.com/pdbethke/corralai/internal/principals"
	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/rolemodel"
	"github.com/pdbethke/corralai/internal/taskartifacts"
	"github.com/pdbethke/corralai/internal/telemetry"
	"github.com/pdbethke/corralai/internal/transparency"
	"golang.org/x/net/websocket"
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
	roleModels      *rolemodel.Policy
	staffing        *mission.StaffingManager
	learn           *learn.Store
	promote         func(id int64, actor string) error
	reject          func(id int64, reason string) error
	historyFn       func() ([]brain.MissionSummary, error)
	historyDetailFn func(id int64) (*brain.MissionDetail, error)
	replayFn        func(missionID int64) ([]brain.ReplayEvent, error)
	artifacts       *artifacts.Store
	taskArtifacts   *taskartifacts.Store
	buildStore      *buildstore.Store
	bugCatch        *bugcatch.Store
	criticScore     *criticscore.Store
	matrixStore     *matrixstore.Store
	certifyPub      ed25519.PublicKey
	witness         transparency.Witness

	// consoleSub is the same fs.Sub(webFS,"web") the FileServer at "/"
	// serves from — the /console/asset/{path...} endpoint reads from this
	// SAME tree so it can never drift from what "/" serves.
	consoleSub fs.FS
	// consoleManifestJSON is the cached JSON bytes of this Server's
	// BundleManifest, built ONCE at Handler construction (never
	// recomputed per-request — see buildManifest's doc comment).
	consoleManifestJSON []byte
	// consoleSig is the detached signature bytes /console/manifest.sig
	// serves, copied from the package-level embedded consoleManifestSig at
	// Handler construction. Kept as a Server field (rather than reading
	// the package var directly in the handler) so a Server built without
	// Handler() — e.g. a zero-value &Server{} in a test — can exercise the
	// "no signature configured" 404 path without mutating global state.
	consoleSig []byte
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
	RoleModels *rolemodel.Policy
	Staffing   *mission.StaffingManager
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

	// TaskArtifacts is the dedicated database store for task execution and UI artifacts.
	TaskArtifacts *taskartifacts.Store

	// BuildStore is corral certify's signed build-record ledger (Task 3);
	// /api/builds and /api/builds/{id} read from it. nil => /api/builds
	// returns an empty list and /api/builds/{id} 404s (feature disabled,
	// never a 500).
	BuildStore *buildstore.Store
	// BugCatch is the bug-catching scorecard store (internal/bugcatch);
	// /api/bugcatch reads from it, marking thin (Runs < 3) cells provisional
	// (see brain.BuildBugCatchScorecard). nil => /api/bugcatch returns an
	// empty scorecard, never a 500 (degrade-never-block).
	BugCatch *bugcatch.Store
	// CriticScore is the critic-accuracy store (internal/criticscore);
	// /api/bugcatch joins its per-model precision onto the test-critic
	// cells, and /api/criticscore serves its pending-adjudication list to
	// `corral criticscore list`. nil => /api/bugcatch shows no
	// critic-precision column and /api/criticscore returns an empty list,
	// never a 500 (degrade-never-block, same as BugCatch above).
	CriticScore *criticscore.Store
	// MatrixStore is the tests×mutants matrix store (internal/matrixstore,
	// swarm slice 5); /api/matrix reads its List + DeleteCandidates for
	// `corral matrix list`. nil => /api/matrix returns an empty matrix,
	// never a 500 (degrade-never-block, same as BugCatch/CriticScore above).
	MatrixStore *matrixstore.Store
	// CertifyPub is the published Ed25519 public key /api/builds/{id} uses
	// to re-verify a record's signature — the same external trust anchor
	// `corral certify verify` uses, never derived from the record itself.
	// nil => the signature check fails closed rather than trusting an
	// embedded key.
	CertifyPub ed25519.PublicKey
	// Witness is the transparency witness /api/builds/{id} uses to confirm
	// an anchored record's Rekor inclusion proof. nil => the rekor check
	// fails with "no transparency witness configured" for any anchored
	// record (an unanchored record never needs it).
	Witness transparency.Witness

	// Version is the daemon's own build version (cmd/corral's `version`
	// var). Stamped into the /console/manifest.json bundle manifest — see
	// console_bundle.go. Empty => the manifest's Version field is "".
	Version string
}

func Handler(d Deps) http.Handler {
	s := &Server{coord: d.Coord, mem: d.Mem, gw: d.Gateway, bus: d.Bus, memOwners: d.MemOwners, roles: d.Roles, queue: d.Queue, missions: d.Missions, execs: d.Executions, acts: d.Activity, hosts: d.Hosts, health: d.Health, narrator: d.Narrator, tel: d.Telemetry, oracle: d.Oracle, roleModels: d.RoleModels, staffing: d.Staffing, learn: d.Learn, promote: d.Promote, reject: d.Reject, historyFn: d.History, historyDetailFn: d.HistoryDetail, replayFn: d.Replay, artifacts: d.Artifacts, taskArtifacts: d.TaskArtifacts, buildStore: d.BuildStore, bugCatch: d.BugCatch, criticScore: d.CriticScore, matrixStore: d.MatrixStore, certifyPub: d.CertifyPub, witness: d.Witness}
	mux := http.NewServeMux()
	sub, _ := fs.Sub(webFS, "web")
	// "/" no longer serves the SPA (Task 3 of the daemon/client refactor):
	// the daemon is headless. The SPA is reached only via the signed
	// /console/* bundle (Task 1), hosted locally by a client (Task 2). This
	// catch-all just identifies the daemon for anyone who lands on it
	// directly, for any path the more-specific routes below don't claim.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, "corral daemon — headless. Connect a client: corral-observe (read-only), corral-admin, or corral-desktop.")
	})
	s.consoleSub = sub

	// /console/*: the versioned, signed bundle resource thin clients fetch
	// instead of "/" (Task 1 of the daemon/client refactor — additive,
	// "/" above is untouched). Built ONCE here and cached on s, not
	// recomputed per request.
	if manifest, err := buildManifest(sub, d.Version); err != nil {
		log.Printf("console bundle: buildManifest: %v", err)
	} else if b, err := json.Marshal(manifest); err != nil {
		log.Printf("console bundle: marshal manifest: %v", err)
	} else {
		s.consoleManifestJSON = b
	}
	s.consoleSig = consoleManifestSig
	mux.HandleFunc("/console/manifest.json", s.consoleManifestHandler)
	mux.HandleFunc("/console/manifest.sig", s.consoleManifestSigHandler)
	mux.HandleFunc("GET /console/asset/{path...}", s.consoleAsset)
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
	mux.HandleFunc("/api/proposal/approve", s.proposalApprove)
	mux.HandleFunc("/api/proposal/reject", s.proposalReject)
	mux.HandleFunc("/api/history", s.history)
	mux.HandleFunc("/api/history/", s.historyDetail)
	mux.HandleFunc("/api/replay", s.replay)
	mux.HandleFunc("/api/leaderboard", s.leaderboard)
	mux.HandleFunc("/api/bugcatch", s.bugcatch)
	mux.HandleFunc("/api/criticscore", s.criticScorePending)
	mux.HandleFunc("/api/matrix", s.matrix)
	mux.HandleFunc("/api/mission/footprint", s.footprint)
	mux.HandleFunc("/api/mission/prune", s.prune)
	mux.HandleFunc("/api/mission/pause", s.steer(brain.SteerPause))
	mux.HandleFunc("/api/mission/resume", s.steer(brain.SteerResume))
	mux.HandleFunc("/api/mission/cancel", s.steer(brain.SteerCancel))
	mux.HandleFunc("/api/mission/intercept", s.intercept)
	mux.HandleFunc("/api/mission/propose_staffing", s.proposeStaffing)
	mux.HandleFunc("/api/mission/compose-options", s.composeOptions)
	mux.Handle("/api/terminal/ws", s.guardTerminalWS(websocket.Handler(Registry.ServeWS)))

	// Records dashboard: the certify build ledger (Task 3/4).
	mux.HandleFunc("/api/builds", s.builds)
	mux.HandleFunc("/api/builds/", s.buildDetail)

	// Swarm Design Lookbook API routes
	mux.HandleFunc("/api/lookbook", s.lookbookList)
	mux.HandleFunc("/api/lookbook/upload", s.lookbookUpload)
	mux.HandleFunc("/api/lookbook/delete", s.lookbookDelete)
	mux.HandleFunc("/api/lookbook/image", s.lookbookImage)

	return mux
}

// composeOptions feeds the Mission Composer's endpoint + lookbook pickers: the
// endpoints the caller may consume and the lookbook items available to attach.
func (s *Server) composeOptions(w http.ResponseWriter, r *http.Request) {
	type epView struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	type lbView struct {
		ID          int64  `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	eps := []epView{}
	if s.gw != nil {
		if usable, err := s.gw.Usable(auth.Principal(r.Context())); err == nil {
			for _, e := range usable {
				eps = append(eps, epView{Name: e.Name, Description: e.Description})
			}
		} else {
			log.Printf("compose-options: endpoints: %v", err)
		}
	}
	lbs := []lbView{}
	if s.taskArtifacts != nil {
		if metas, err := s.taskArtifacts.GetLookbookItemsMeta(); err == nil {
			for _, mta := range metas {
				lbs = append(lbs, lbView{ID: mta.ID, Name: mta.Name, Description: mta.Description})
			}
		} else {
			log.Printf("compose-options: lookbook: %v", err)
		}
	}
	writeJSON(w, map[string]any{"endpoints": eps, "lookbook": lbs})
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

// guardTerminalWS authorizes the /api/terminal/ws upgrade BEFORE it hands the
// connection to the intercept Registry. The "operator" side is a human taking
// interactive control of an agent's stdin — as consequential as any mutating
// action — so it is gated to a superuser with a non-read-only token, exactly
// like proposalApprove / steer / prune. Without this a read-only observer (or
// any authenticated non-superuser) could open role=operator and type commands
// straight into an agent's shell. The "agent" side is the corral-agent process
// streaming its OWN terminal (authenticated as a worker, never a superuser), so
// it passes through — reaching the mux at all already required a valid bearer.
func (s *Server) guardTerminalWS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("role") == "operator" {
			if auth.ReadOnly(r) {
				http.Error(w, "forbidden: read-only observer cannot take control of an agent", http.StatusForbidden)
				return
			}
			if !s.isSuperuser(r) {
				http.Error(w, "forbidden: superuser only (interactive agent control)", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
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

// bugcatch serves the read-only bug-catching scorecard (Task 4): the same
// authz-free, read-only pattern as leaderboard — reaching the mux at all
// already required a valid bearer (see the daemon's authz wrapper in
// cmd/corral/main.go), and this endpoint mutates nothing.
func (s *Server) bugcatch(w http.ResponseWriter, r *http.Request) {
	sc, err := brain.BuildBugCatchScorecard(s.bugCatch, s.criticScore)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, sc)
}

// criticScorePending serves the critic-accuracy findings still awaiting
// human adjudication (internal/criticscore.ListPending) — the same
// read-only, authz-free-past-the-mux pattern as bugcatch above. `corral
// criticscore list` reads this instead of opening the criticscore DuckDB
// file directly, for the same single-process reason `corral scorecard`
// reads /api/bugcatch instead of the bugcatch file (see
// cmd/corral/scorecard.go's httpScorecardReader doc comment). A nil
// criticScore store (feature disabled) degrades to an empty list.
func (s *Server) criticScorePending(w http.ResponseWriter, r *http.Request) {
	if s.criticScore == nil {
		writeJSON(w, pendingCriticFindingsResponse{Findings: []criticscore.Finding{}})
		return
	}
	fs, err := s.criticScore.ListPending(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, pendingCriticFindingsResponse{Findings: fs})
}

// matrixResponse is /api/matrix's response shape: every scored row (List)
// plus the delete-candidate subset (DeleteCandidates), pre-split so `corral
// matrix list` doesn't have to re-filter Rows client-side to render its
// safe-to-delete section.
type matrixResponse struct {
	Rows             []matrixstore.Row `json:"rows"`
	DeleteCandidates []matrixstore.Row `json:"delete_candidates"`
}

// matrix serves the read-only tests×mutants matrix (swarm slice 5): the same
// authz-free-past-the-mux, read-only pattern as bugcatch/criticScorePending
// above — reaching the mux at all already required a valid bearer, and this
// endpoint mutates nothing. A nil matrixStore (feature disabled, or no
// --matrix run has ever recorded anything) degrades to an empty matrix,
// never a 500.
func (s *Server) matrix(w http.ResponseWriter, r *http.Request) {
	if s.matrixStore == nil {
		writeJSON(w, matrixResponse{Rows: []matrixstore.Row{}, DeleteCandidates: []matrixstore.Row{}})
		return
	}
	rows, err := s.matrixStore.List(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	candidates, err := s.matrixStore.DeleteCandidates(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if rows == nil {
		rows = []matrixstore.Row{}
	}
	if candidates == nil {
		candidates = []matrixstore.Row{}
	}
	writeJSON(w, matrixResponse{Rows: rows, DeleteCandidates: candidates})
}

// pendingCriticFindingsResponse is /api/criticscore's response shape —
// mirrors brain.pendingCriticFindingsOut's {"findings": [...]}  so the
// MCP tool (list_pending_critic_findings) and the HTTP surface agree.
type pendingCriticFindingsResponse struct {
	Findings []criticscore.Finding `json:"findings"`
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

// builds lists build_records rows for the records dashboard, narrowed by
// query params repo/actor/status/anchored/limit/offset — a thin translation
// of the query string into buildstore.ListFilter. A malformed anchored=
// value is ignored (treated as "no filter") rather than erroring, matching
// the read-only, never-500-on-bad-input style of the other GET endpoints
// here. A nil BuildStore (feature not configured) renders an empty list,
// never a panic.
func (s *Server) builds(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if s.buildStore == nil {
		writeJSON(w, []buildstore.Summary{})
		return
	}
	q := r.URL.Query()
	f := buildstore.ListFilter{
		Repo:   q.Get("repo"),
		Actor:  q.Get("actor"),
		Status: q.Get("status"),
	}
	if v := q.Get("anchored"); v == "true" || v == "false" {
		b := v == "true"
		f.Anchored = &b
	}
	if v, err := strconv.Atoi(q.Get("limit")); err == nil {
		f.Limit = v
	}
	if v, err := strconv.Atoi(q.Get("offset")); err == nil {
		f.Offset = v
	}
	summaries, err := s.buildStore.List(f)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, summaries)
}

// buildDetail is the server-VERIFIED (not independently verified — see the
// package doc note below) detail view for a single build_records row: the
// brain re-runs certverify.VerifyRecord against its own stored record, the
// same four checks `corral certify verify` runs offline. This is an
// operator convenience, not a substitute for independent verification —
// verify_command in the response always points a third party at the
// independent path (the CLI against the published key + Rekor, not this
// dashboard's word for it).
func (s *Server) buildDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	idStr := strings.TrimPrefix(r.URL.Path, "/api/builds/")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if idStr == "" || err != nil {
		http.Error(w, "bad build id", http.StatusBadRequest)
		return
	}
	if s.buildStore == nil {
		http.NotFound(w, r)
		return
	}
	m, ok, err := s.buildStore.Get(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}

	repo, commit, branch, actor := buildDefinitionFields(m)
	head, _ := statementHead(m)
	pass, _ := m["pass"].(bool)
	sig, _ := m["signature"].(string)
	anchored, _ := m["anchored"].(bool)
	rekor, _ := m["rekor"].(string)
	var steps []map[string]any
	if raw, ok := m["steps"].([]any); ok {
		for _, s := range raw {
			if step, ok := s.(map[string]any); ok {
				steps = append(steps, step)
			}
		}
	}
	rec := certverify.Record{Statement: m, Signature: sig, Steps: steps, Head: head, Rekor: rekor, Anchored: anchored}

	checks, allOK := certverify.VerifyRecord(rec, s.certifyPub, func() (transparency.Witness, error) {
		if s.witness == nil {
			return nil, fmt.Errorf("no transparency witness configured")
		}
		return s.witness, nil
	}, false)

	writeJSON(w, map[string]any{
		"id":               id,
		"repo":             repo,
		"commit":           commit,
		"branch":           branch,
		"actor":            actor,
		"head":             head,
		"pass":             pass,
		"anchored":         anchored,
		"produced_by":      producedBy(m),
		"commit_message":   m["commit_message"],
		"commit_author":    m["commit_author"],
		"commit_date":      m["commit_date"],
		"commit_signature": m["commit_signature"],
		"checks":           checks,
		"all_ok":           allOK,
		"verify_command":   fmt.Sprintf("corral certify verify <record.json> --brain %s", brainBaseURL(r)),
	})
}

// buildDefinitionFields pulls repo/commit/branch/actor out of a Get() map's
// merged-in statement — predicate.buildDefinition.externalParameters for the
// first three, predicate.buildDefinition.internalParameters.actor for the
// last (the shape certify.BuildAttestation writes). Missing/malformed
// pieces come back as "" rather than erroring — this is a best-effort
// display projection, not a verification step (VerifyRecord below is the
// verification step).
func buildDefinitionFields(m map[string]any) (repo, commit, branch, actor string) {
	predicate, _ := m["predicate"].(map[string]any)
	buildDef, _ := predicate["buildDefinition"].(map[string]any)
	ext, _ := buildDef["externalParameters"].(map[string]any)
	repo, _ = ext["repo"].(string)
	commit, _ = ext["commit"].(string)
	branch, _ = ext["branch"].(string)
	internal, _ := buildDef["internalParameters"].(map[string]any)
	actor, _ = internal["actor"].(string)
	return repo, commit, branch, actor
}

// statementHead pulls the ledger head out of a Get() map's merged-in
// statement — subject[0].digest.sha256, the same field
// certverify.VerifyRecord's subject check reads.
func statementHead(m map[string]any) (string, bool) {
	subjects, ok := m["subject"].([]any)
	if !ok || len(subjects) == 0 {
		return "", false
	}
	subj, ok := subjects[0].(map[string]any)
	if !ok {
		return "", false
	}
	digest, ok := subj["digest"].(map[string]any)
	if !ok {
		return "", false
	}
	sha, ok := digest["sha256"].(string)
	return sha, ok
}

// producedBy pulls the model URIs out of a Get() map's merged-in statement —
// predicate.buildDefinition.resolvedDependencies, "model:" prefix stripped.
// Mirrors buildstore's own producedByFromStatement (unexported there); kept
// duplicated rather than exported across a package boundary for one helper.
func producedBy(m map[string]any) []string {
	predicate, _ := m["predicate"].(map[string]any)
	buildDef, _ := predicate["buildDefinition"].(map[string]any)
	deps, _ := buildDef["resolvedDependencies"].([]any)
	out := []string{}
	for _, d := range deps {
		dep, ok := d.(map[string]any)
		if !ok {
			continue
		}
		uri, _ := dep["uri"].(string)
		if uri == "" {
			continue
		}
		out = append(out, strings.TrimPrefix(uri, "model:"))
	}
	return out
}

// brainBaseURL best-effort reconstructs this brain's own base URL from the
// incoming request, for embedding in verify_command so a third party knows
// which brain (and thus which published certify key) to fetch the record
// from independently. Falls back to a placeholder when scheme can't be
// inferred (e.g. behind a proxy that doesn't set anything usable).
func brainBaseURL(r *http.Request) string {
	if r.Host == "" {
		return "<brain-url>"
	}
	scheme := "https"
	if r.TLS == nil {
		scheme = "http"
	}
	return scheme + "://" + r.Host
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

// steer returns the handler for one of #58's mid-mission human-steering
// verbs (pause/resume/cancel) — today's human gate was TERMINAL only
// (proposalApprove/proposalReject) and mission creation was PRE-launch
// only; there was no way to intervene on a RUNNING mission. Each
// verb is gated exactly like prune: a read-only observer token, or a
// delegation/worker token, is refused — only a verified human superuser may
// steer a mission. The action is recorded (attributed to the verified
// principal) in telemetry by brain.SteerMission so it surfaces in
// history/replay.
func (s *Server) steer(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if auth.ReadOnly(r) {
			http.Error(w, "forbidden: read-only observer token cannot act", http.StatusForbidden)
			return
		}
		if !s.isSuperuser(r) {
			http.Error(w, "forbidden: superuser only (mission steering is an operator action)", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		if s.missions == nil || s.queue == nil {
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
		actor := auth.Principal(r.Context())
		if actor == "" {
			actor = "operator"
		}
		mi, err := brain.SteerMission(s.missions, s.queue, s.tel, body.ID, action, actor)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "mission": mi})
	}
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
	if !s.isSuperuser(r) {
		http.Error(w, "forbidden: superuser only (instructing an agent is an operator action)", http.StatusForbidden)
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

// intercept is the HTTP twin of guardTerminalWS's operator-role gate: taking
// interactive control of (or tearing down) a live agent session is as
// consequential as typing into its shell, so it requires the same
// non-read-only, superuser bearer — never a bare authenticated request. Before
// this gate existed, ANY authenticated caller (including a read-only observer)
// could stall or kill any agent's session.
func (s *Server) intercept(w http.ResponseWriter, r *http.Request) {
	if auth.ReadOnly(r) {
		http.Error(w, "forbidden: read-only observer cannot take control of an agent", http.StatusForbidden)
		return
	}
	if !s.isSuperuser(r) {
		http.Error(w, "forbidden: superuser only (interactive agent control)", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	agent := r.URL.Query().Get("agent")
	enable := r.URL.Query().Get("enable") == "true"
	if agent == "" {
		http.Error(w, "agent required", 400)
		return
	}
	if s.hosts != nil {
		s.hosts.SetInterceptPending(agent, enable)
	}
	if !enable {
		Registry.mu.Lock()
		if sess, ok := Registry.sessions[agent]; ok {
			select {
			case <-sess.Done:
			default:
				close(sess.Done)
			}
			delete(Registry.sessions, agent)
		}
		Registry.mu.Unlock()
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func (s *Server) proposeStaffing(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	directive := r.URL.Query().Get("directive")
	if directive == "" {
		http.Error(w, "directive required", http.StatusBadRequest)
		return
	}

	thoroughness, _ := strconv.Atoi(r.URL.Query().Get("thoroughness"))
	if thoroughness <= 0 {
		thoroughness = 3
	}
	footprint, _ := strconv.Atoi(r.URL.Query().Get("footprint"))
	if footprint <= 0 {
		footprint = 3
	}

	if s.staffing == nil || s.staffing.LLM == nil || !s.staffing.LLM.Available() {
		writeJSON(w, map[string]any{
			"ok": true,
			"assignments": map[string]string{
				"security-breaker":     "qwen2.5-coder:7b",
				"correctness-reviewer": "qwen2.5-coder:7b",
				"exploit-attempter":    "qwen2.5-coder:7b",
				"edge-hunter":          "llama3.2:3b",
			},
		})
		return
	}

	resources := s.staffing.Sense()
	stats := s.staffing.Perf.GetRoleModelStats()
	assignments, _, err := s.staffing.Judge(r.Context(), directive, resources, stats, thoroughness, footprint)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	clamped := s.staffing.Clamp(assignments, resources)
	writeJSON(w, map[string]any{
		"ok":          true,
		"assignments": clamped,
	})
}

func (s *Server) lookbookList(w http.ResponseWriter, r *http.Request) {
	if s.taskArtifacts == nil {
		http.Error(w, "Lookbook database not configured", http.StatusServiceUnavailable)
		return
	}
	items, err := s.taskArtifacts.GetLookbookItemsMeta()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, items)
}

func (s *Server) lookbookUpload(w http.ResponseWriter, r *http.Request) {
	if s.taskArtifacts == nil {
		http.Error(w, "Lookbook database not configured", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	// A read-only observer token may view the swarm but never act — the UI mux
	// (unlike /mcp) isn't wrapped in denyReadOnly, so writes guard themselves.
	if auth.ReadOnly(r) {
		http.Error(w, "forbidden: read-only observer token cannot act", http.StatusForbidden)
		return
	}
	// Adding a fleet-shared design directive is an operator action, mirroring
	// the isHumanAdmin gate on memory/reference promotion — any authenticated
	// member being able to add one would let a non-admin steer every agent's
	// design guidance.
	if !s.isSuperuser(r) {
		http.Error(w, "forbidden: superuser only (lookbook is a fleet-shared design directive)", http.StatusForbidden)
		return
	}
	// Cap the request body BEFORE reading/decoding — the 5MB check below runs after
	// a full read + base64 decode, so without this a huge upload OOMs the brain.
	// 8MiB leaves room for a 5MB image's base64 (~6.7MB) plus the JSON envelope.
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)

	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Data        string `json:"data"` // base64 encoded
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	dataBytes, err := base64.StdEncoding.DecodeString(body.Data)
	if err != nil {
		http.Error(w, "invalid base64 payload: "+err.Error(), http.StatusBadRequest)
		return
	}

	// 5MB limit
	if len(dataBytes) > 5*1024*1024 {
		http.Error(w, "payload exceeds 5MB size limit", http.StatusBadRequest)
		return
	}

	// Validate by the actual bytes, and store the DETECTED type — never a
	// client-supplied mime_type. Serving a client-controlled Content-Type back on
	// the brain's own origin is a stored-XSS vector (see lookbookImage).
	detected := http.DetectContentType(dataBytes)
	allowed := false
	for _, prefix := range []string{"image/png", "image/jpeg", "image/gif", "image/webp"} {
		if strings.HasPrefix(detected, prefix) {
			allowed = true
			break
		}
	}
	if !allowed {
		http.Error(w, "unsupported image type: "+detected, http.StatusBadRequest)
		return
	}

	id, err := s.taskArtifacts.SaveLookbookItem(body.Name, body.Description, detected, dataBytes)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]any{"id": id, "ok": true})
}

func (s *Server) lookbookDelete(w http.ResponseWriter, r *http.Request) {
	if s.taskArtifacts == nil {
		http.Error(w, "Lookbook database not configured", http.StatusServiceUnavailable)
		return
	}
	if auth.ReadOnly(r) {
		http.Error(w, "forbidden: read-only observer token cannot act", http.StatusForbidden)
		return
	}
	// Removing a fleet-shared design directive is an operator action — same
	// gate as the upload side.
	if !s.isSuperuser(r) {
		http.Error(w, "forbidden: superuser only (lookbook is a fleet-shared design directive)", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	idStr := r.URL.Query().Get("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	if err := s.taskArtifacts.DeleteLookbookItem(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) lookbookImage(w http.ResponseWriter, r *http.Request) {
	if s.taskArtifacts == nil {
		http.Error(w, "Lookbook database not configured", http.StatusServiceUnavailable)
		return
	}
	idStr := r.URL.Query().Get("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	found, err := s.taskArtifacts.GetLookbookItem(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if found == nil {
		http.Error(w, "image not found", http.StatusNotFound)
		return
	}

	// MimeType is the server-detected type (see lookbookUpload). nosniff stops the
	// browser MIME-sniffing the blob into active content, and an inline
	// disposition keeps it a passive image — belt-and-suspenders against a
	// stored-XSS via a crafted image/HTML polyglot.
	w.Header().Set("Content-Type", found.MimeType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", "inline")
	w.Header().Set("Content-Length", strconv.Itoa(len(found.Data)))
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(found.Data)
}

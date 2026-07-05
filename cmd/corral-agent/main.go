// SPDX-License-Identifier: Elastic-2.0

// Command corral-agent is a reference agent for the demo: a real LLM (local Ollama)
// drives a coding-work loop and coordinates through the corral brain over MCP —
// claim before editing, mark done, release. The model picks the tools; the agent
// executes them against the brain and the shared workspace.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/admission"
	"github.com/pdbethke/corralai/internal/agentrole"
	"github.com/pdbethke/corralai/internal/sandbox"
)

// makeBrainCall wraps the agent's brain closure (which returns raw JSON strings)
// into a brainCall (returns parsed map or error). The mirror helpers use this shape.
func makeBrainCall(brain func(string, map[string]any) string) brainCall {
	return func(tool string, args map[string]any) (map[string]any, error) {
		raw := brain(tool, args)
		var m map[string]any
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			return nil, err
		}
		if errMsg, ok := m["error"].(string); ok {
			return nil, errors.New(errMsg)
		}
		return m, nil
	}
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func usageText() string {
	return `corral-agent — reference LLM-driven agent for the demo (local Ollama by default)

Usage:
  corral-agent            connect to the brain and work the queue
  corral-agent --version  print the build version and exit
  corral-agent -h         print this help and exit

Env:
  CORRAL_BRAIN       brain URL (default http://127.0.0.1:9019/mcp/)
  AGENT_ROLE         role(s) to serve (default builder): a single role
                     (builder | tester | pentester | reviewer | ...), a
                     comma-separated list (e.g. "researcher,designer,tester")
                     to claim any ready task in that set, or "any"/"*"/empty
                     to claim ANY ready task as a pure generalist
  AGENT_NAME         display name in the swarm UI (default same as AGENT_ROLE,
                     e.g. "researcher+designer" or "generalist")
  AGENT_WORKSPACE    working directory for edits (default $TMPDIR/corral-demo-ws)
  MODEL_BACKEND      ollama (default) | openai (Gemini/OpenRouter/local, any OpenAI-compatible endpoint)
  AGENT_MODEL        model name passed to the backend (default qwen2.5-coder:7b)
  CLOBBER            set "1" to ignore coordination conflicts and edit anyway (demo of what NOT coordinating looks like)
`
}

// ---- Ollama (function-calling chat) ----

type omsg struct {
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	ToolCalls []otoolcal `json:"tool_calls,omitempty"`
}
type otoolcal struct {
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

// extractCall returns the tool name + raw args the model wants, handling BOTH the
// structured tool_calls field AND the common Ollama fallback where the model emits
// the call as JSON in content (optionally fenced).
var fence = regexp.MustCompile("(?s)```(?:json)?\\s*(\\{.*?\\})\\s*```")

func extractCall(m omsg) (name string, args map[string]any, ok bool) {
	if len(m.ToolCalls) > 0 {
		tc := m.ToolCalls[0]
		if json.Unmarshal(tc.Function.Arguments, &args) != nil || args == nil {
			// OpenAI-compatible backends return arguments as a JSON-encoded STRING,
			// not an object (Ollama returns the object). Handle both.
			var s string
			if json.Unmarshal(tc.Function.Arguments, &s) == nil {
				_ = json.Unmarshal([]byte(s), &args)
			}
		}
		return tc.Function.Name, args, true
	}
	raw := strings.TrimSpace(m.Content)
	if mt := fence.FindStringSubmatch(raw); mt != nil {
		raw = mt[1]
	}
	if i := strings.IndexByte(raw, '{'); i >= 0 {
		raw = raw[i:]
		if j := strings.LastIndexByte(raw, '}'); j >= 0 {
			raw = raw[:j+1]
		}
	}
	var probe struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
		Params    map[string]any `json:"parameters"`
	}
	if err := json.Unmarshal([]byte(raw), &probe); err == nil && probe.Name != "" {
		a := probe.Arguments
		if a == nil {
			a = probe.Params
		}
		return probe.Name, a, true
	}
	// Last resort: a textual `tool_name({...})` call. Some models (observed:
	// gemini-flash) drop out of native tool-calling and write the call as prose
	// — e.g. mimicking the transcript's action echoes. Executing the stated
	// intent beats discarding it as a final answer.
	if mt := callish.FindStringSubmatch(m.Content); mt != nil {
		var a map[string]any
		if json.Unmarshal([]byte(mt[2]), &a) == nil {
			return mt[1], a, true
		}
	}
	return "", nil, false
}

// callish matches a textual tool invocation: name({json-args}).
var callish = regexp.MustCompile(`(?s)\b([a-zA-Z_][a-zA-Z0-9_]*)\s*\(\s*(\{.*\})\s*\)`)

// ---- the agent ----

type ticket struct{ id, title, hint string }

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	for _, a := range os.Args[1:] {
		if a == "--version" || a == "-version" || a == "version" || a == "-v" {
			fmt.Println("corral-agent", version)
			return
		}
		if a == "-h" || a == "--help" || a == "help" {
			fmt.Print(usageText())
			return
		}
	}
	var (
		brainURL = env("CORRAL_BRAIN", "http://127.0.0.1:9019/mcp/")
		// rs is the parsed AGENT_ROLE: a single role, a comma-list, or a
		// generalist ("any"/"*"/empty). role is its display form — unchanged
		// for a single role, "+"-joined for a list, "generalist" for any —
		// used everywhere the old single-role string was (bootstrap, logs,
		// report_host, AGENT_NAME's default). Only claim_task's actual
		// filter (rs.ClaimArg(), wired in runQueueLoop) needs the real set:
		// a small herd of multi-role bees can then cover every role a
		// mission plans instead of deadlocking on the first one nobody
		// staffs (#23/#39).
		rs   = agentrole.Parse(env("AGENT_ROLE", "builder"))
		role = rs.Display()
		name = env("AGENT_NAME", role)
		ws   = env("AGENT_WORKSPACE", filepath.Join(os.TempDir(), "corral-demo-ws"))
	)
	backend := newBackend() // MODEL_BACKEND: ollama (default) | openai (Gemini/OpenRouter/local) — NOT hard-wired
	modelDesc := env("MODEL_BACKEND", "ollama") + ":" + env("AGENT_MODEL", "qwen2.5-coder:7b")
	// clobber: the agent STILL connects to the brain (so it's visible in the swarm
	// UI), but its prompt tells it to IGNORE conflicts and edit anyway — so you watch
	// the agents pile onto the same files (red conflicts) and trample each other's
	// work. The coordinated agents back off on conflict instead.
	clobber := os.Getenv("CLOBBER") == "1"
	ctx := context.Background()
	if err := os.MkdirAll(ws, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "agent: create workspace %s: %v\n", ws, err)
		os.Exit(1)
	}
	setupExec()

	cl := mcp.NewClient(&mcp.Implementation{Name: "corral-agent", Version: "0"}, nil)
	sess, err := cl.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: brainURL}, nil)
	if err != nil {
		fmt.Println("connect brain:", err)
		os.Exit(1)
	}
	defer sess.Close()

	// Discover, from the brain's OWN tool schemas, which tools declare a "name"
	// input — those get the caller identity stamped. Guessing this list by hand
	// broke every bee's status/list/resolve call for weeks: the strict schemas
	// REJECT unexpected properties, and the failures were silent until brain-call
	// errors were logged. Schema-driven means new tools just work.
	nameAware := map[string]bool{}
	schemaDiscovered := false
	if lt, lterr := sess.ListTools(ctx, nil); lterr == nil {
		schemaDiscovered = true
		for _, t := range lt.Tools {
			sb, _ := json.Marshal(t.InputSchema)
			var sch struct {
				Properties map[string]json.RawMessage `json:"properties"`
			}
			_ = json.Unmarshal(sb, &sch)
			if _, ok := sch.Properties["name"]; ok {
				nameAware[t.Name] = true
			}
		}
	} else {
		fmt.Printf("[%s] ⚠ tools/list failed (%v) — falling back to stamping name everywhere\n", name, lterr)
	}

	brain := func(tool string, args map[string]any) string {
		if args == nil {
			args = map[string]any{}
		}
		switch tool {
		case "add_memory", "spawn_subagent", "get_memory", "despawn_subagent":
			// "name" here is NEVER the caller: entry slug (memory), child name /
			// subagent id (spawn/despawn). Identity rides other fields or the token.
		default:
			if nameAware[tool] || !schemaDiscovered {
				args["name"] = name
			}
		}
		if tool == "add_memory" {
			args["author"] = name // attribute the entry to this agent (the hive-mind "who")
		}
		res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
		if err != nil {
			// Every failed brain call is visible in the container log — a silent
			// transport error here is how a claim once got orphaned into a
			// mission-wide deadlock. Callers still receive the error-shaped JSON.
			fmt.Printf("[%s] ⚠ brain call %s failed: %v\n", name, tool, err)
			return fmt.Sprintf(`{"error":%q}`, err.Error())
		}
		if res.IsError {
			if b, _ := json.Marshal(res.Content); len(b) > 0 {
				fmt.Printf("[%s] ⚠ brain tool %s returned error: %.300s\n", name, tool, string(b))
			}
		}
		b, _ := json.Marshal(res.StructuredContent)
		return string(b)
	}
	mode := "coordinating"
	if clobber {
		mode = "CLOBBER (ignores conflicts)"
	}
	fmt.Printf("[%s] registering as a %s [%s] with the brain at %s\n", name, role, mode, brainURL)
	brain("bootstrap", map[string]any{"task": role + " on the demo codebase", "role": role, "program": "corral-agent (" + modelDesc + ")"})

	// Announce this bee's runtime facts so the brain's topology view can map WHERE
	// every agent runs. Re-announce on a timer so the picture survives a brain
	// restart (bees are long-lived; they don't restart when the brain does).
	go func() {
		for {
			host, _ := os.Hostname()
			jail := "none"
			if execRuntime.enabled {
				jail = execRuntime.backend.Name()
			}
			brain("report_host", map[string]any{
				"role": role, "host": host,
				"model": env("AGENT_MODEL", "qwen2.5-coder:7b"), "backend": env("MODEL_BACKEND", "ollama"),
				"jail": jail, "net": execRuntime.network,
				"os": runtime.GOOS + "/" + runtime.GOARCH, "pid": os.Getpid(),
			})
			time.Sleep(20 * time.Second)
		}
	}()

	ctrl := admission.FromEnv()
	spawnConfiguredChildren(ctrl, brain, name, brainURL)

	// The simulated edit_file only ships to the ticket-demo modes; queue-mode
	// mission bees get write_file only (see agentTools).
	tools := agentTools(env("AGENT_MODE", "queue") != "queue")

	// queue mode (default): pull tasks and execute them. lead mode: the judgment
	// tier — read open findings + the plan and re-architect via the mutation tools.
	// demo mode: self-assigned role work (the existing demo profiles).
	switch env("AGENT_MODE", "queue") {
	case "queue":
		runQueueLoop(ctx, backend, name, role, rs, ws, brain, tools)
		return
	case "lead":
		runLeadLoop(ctx, backend, name, brain)
		return
	case "client":
		runClientLoop(ctx, backend, name, brain)
		return
	case "scrum":
		// The standup tier: deterministic watcher — narrates progress, names
		// stalls, nudges slackers. No model in the loop.
		runScrumLoop(name, brain)
		return
	}

	job, backlog := roleWork(role)
	rounds := 1
	if v := os.Getenv("AGENT_ROUNDS"); v != "" {
		_, _ = fmt.Sscanf(v, "%d", &rounds) // 0 = forever (the demo keeps the swarm alive)
	}
	for round := 1; rounds == 0 || round <= rounds; round++ {
		for _, t := range backlog {
			fmt.Printf("\n[%s/%s] r%d ── %s: %s\n", name, role, round, t.id, t.title)
			runTicket(ctx, backend, name, role, job, ws, brain, tools, t, clobber)
		}
		brain("release_claims", map[string]any{})
		if rounds == 0 {
			brain("heartbeat", map[string]any{"status": "idle"})
			time.Sleep(4 * time.Second)
		}
	}
	fmt.Printf("\n[%s/%s] done.\n", name, role)
}

// runQueueLoop is the bee's pull loop: claim the next ready task for this
// worker's role set, execute it, complete it, repeat; idle-poll
// (heart-beating) when the queue is empty. Runs until the process is
// stopped — a worker, not a one-shot. role is the display string (for logs
// and reporting); rs.ClaimArg() is the actual claim_task filter — a list for
// multi-role, or empty for a generalist (claims any ready task).
func runQueueLoop(ctx context.Context, backend Backend, name, role string, rs agentrole.Set, ws string, brain func(string, map[string]any) string, tools []any) {
	poll := 3 * time.Second
	if v := os.Getenv("AGENT_POLL_SECONDS"); v != "" {
		var n int
		if _, _ = fmt.Sscanf(v, "%d", &n); n > 0 {
			poll = time.Duration(n) * time.Second
		}
	}
	modelDesc := env("MODEL_BACKEND", "ollama") + ":" + env("AGENT_MODEL", "qwen2.5-coder:7b")
	fmt.Printf("[%s/%s] queue mode — pulling tasks\n", name, role)
	// One mirror per agent worker: tracks per-mission snapshot dirs + written paths.
	mir := newMirror(ws)
	bc := makeBrainCall(brain)
	// Instance identity (hostname) rides on every claim so the brain can tell
	// "this bee lost the reply to its own claim — re-issue it" apart from "a
	// same-named --scale replica is asking" (which must wait for lease expiry).
	instance, _ := os.Hostname()
	// Refusal bookkeeping per task: the gate's message is fed into the next
	// attempt, and past refusalCap the bee reports a finding + backs off hard
	// instead of burning model quota on an unwinnable loop.
	refusals := map[int64]int{}
	refusalMsg := map[int64]string{}
	for {
		var out struct {
			Task *struct {
				ID          int64  `json:"id"`
				MissionID   int64  `json:"mission_id"`
				Title       string `json:"title"`
				Instruction string `json:"instruction"`
				Reissued    bool   `json:"reissued"`
			} `json:"task"`
			Error string `json:"error"`
		}
		raw := brain("claim_task", map[string]any{"roles": rs.ClaimArg(), "instance": instance})
		// NEVER silently equate a failed claim with an empty queue: a lost reply
		// here is how a claim gets orphaned while the bee heartbeats idle.
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			fmt.Printf("[%s/%s] ⚠ claim_task reply unparseable: %v — raw: %.200s\n", name, role, err, raw)
		} else if out.Error != "" {
			fmt.Printf("[%s/%s] ⚠ claim_task failed: %s\n", name, role, out.Error)
		}
		if out.Task == nil {
			brain("heartbeat", map[string]any{"status": "idle"})
			time.Sleep(poll)
			continue
		}
		if out.Task.Reissued {
			fmt.Printf("[%s/%s] ↻ brain re-issued task #%d (%s) — an earlier claim of mine was orphaned\n", name, role, out.Task.ID, out.Task.Title)
		}
		fmt.Printf("\n[%s/%s] ▶ claimed task #%d: %s\n", name, role, out.Task.ID, out.Task.Title)
		// Close the feedback loop on a refused completion: without this, a model
		// that doesn't spontaneously exec loops forever against the verify gate —
		// each fresh attempt starts a conversation that never hears WHY the last
		// one was refused (observed live: 41 refusal cycles on gemini-flash).
		instruction := withRefusalFeedback(out.Task.Instruction, refusalMsg[out.Task.ID])
		taskStart := time.Now()
		summary, taskErr := runTask(ctx, backend, name, role, ws, brain, tools, out.Task.MissionID, out.Task.ID, out.Task.Title, instruction, mir, bc)
		fmt.Printf("[%s/%s] task #%d ran %s\n", name, role, out.Task.ID, time.Since(taskStart).Round(time.Second))
		if handleTaskError(out.Task.ID, out.Task.MissionID, modelDesc, taskErr, brain) {
			fmt.Printf("[%s/%s] ⨯ model unreachable on task #%d — released claim; reaper will reassign\n",
				name, role, out.Task.ID)
			time.Sleep(5 * time.Second) // brief backoff: avoid re-claiming immediately
			continue
		}
		// For repo missions: push all bee-written files BEFORE completing so the brain
		// can apply them before the task is closed. Retry up to 3 times for transient
		// failures. If all attempts fail, do NOT complete the task as a clean success —
		// the verify gate ran in the bee's mirror, and committing a phase that never
		// reached the brain's working copy would produce a confidently-wrong PR branch.
		// Instead, surface the failure via report_finding and leave the task's lease to
		// expire so the Reap mechanism re-queues it for another bee.
		const pushMaxAttempts = 3
		pushFailed := false // #nosec G118 -- the agent's queue/lead poll loops run for the process lifetime by design (AGENT_ROUNDS=0 = forever); not a runaway; graceful ctx shutdown is a robustness nicety, not a vuln
		var pushErr error
		if mir.isRepo[out.Task.MissionID] {
			for attempt := 1; attempt <= pushMaxAttempts; attempt++ {
				var applied []string
				applied, pushErr = mir.push(bc, out.Task.MissionID)
				if pushErr == nil {
					fmt.Printf("[%s/%s] repo_push applied %d file(s) for mission #%d\n", name, role, len(applied), out.Task.MissionID)
					break
				}
				if attempt < pushMaxAttempts {
					fmt.Printf("[%s/%s] repo_push attempt %d/%d failed for mission #%d: %v — retrying\n",
						name, role, attempt, pushMaxAttempts, out.Task.MissionID, pushErr)
					time.Sleep(time.Duration(attempt) * time.Second)
				} else {
					pushFailed = true
				}
			}
		}
		if pushFailed {
			// Report the infrastructure failure as a finding so the lead can observe it
			// and the swarm knows why this phase didn't land.
			brain("report_finding", map[string]any{
				"mission_id":       out.Task.MissionID,
				"task_id":          out.Task.ID,
				"type":             "regression",
				"severity":         "high",
				"target":           fmt.Sprintf("task#%d repo_push", out.Task.ID),
				"evidence":         fmt.Sprintf("repo_push failed after %d attempts: %v", pushMaxAttempts, pushErr),
				"suggested_action": "investigate brain/workspace connectivity; task will be re-queued when its lease expires",
			})
			// Mirror the complete_task refusal contract (ok:false + message) without
			// calling the brain — the task stays claimed and the Reap mechanism will
			// re-queue it once the lease expires (~5 min), so another bee can retry.
			fmt.Printf("[%s/%s] ⨯ refused task #%d: repo_push failed: %v (lease will expire; task re-queued by reaper)\n",
				name, role, out.Task.ID, pushErr)
			continue
		}
		// complete_task can REFUSE: the verify gate returns {ok:false,message:…}
		// when no successful run of the task's verify command is on record (it also
		// returns ok:false if you weren't the claimer / it was already done). Honour
		// the verdict — narrating "✓ completed" on a refusal is exactly the lie the
		// gate exists to stop, so report what actually happened.
		var ct struct {
			OK      bool   `json:"ok"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal([]byte(brain("complete_task", map[string]any{"id": out.Task.ID, "result": summary})), &ct)
		if ct.OK {
			fmt.Printf("[%s/%s] ✓ completed task #%d: %s\n", name, role, out.Task.ID, oneline(summary))
			delete(refusals, out.Task.ID)
			delete(refusalMsg, out.Task.ID)
		} else {
			why := ct.Message
			if why == "" {
				why = "not the claimer, or it was already done"
			}
			fmt.Printf("[%s/%s] ⨯ refused task #%d: %s\n", name, role, out.Task.ID, oneline(why))
			refusals[out.Task.ID]++
			refusalMsg[out.Task.ID] = why
			if refusals[out.Task.ID] == refusalCap {
				// Make the stall visible (the lead / a human can supersede) and
				// stop hammering the model at full speed.
				brain("report_finding", map[string]any{
					"mission_id": out.Task.MissionID, "task_id": out.Task.ID,
					"type": "note", "severity": "high", "target": out.Task.Title,
					"evidence":         fmt.Sprintf("%s cannot satisfy the completion gate after %d attempts: %s", name, refusalCap, why),
					"suggested_action": "supersede the task, adjust its verify command, or hand it to a stronger model",
				})
			}
			if refusals[out.Task.ID] >= refusalCap {
				fmt.Printf("[%s/%s] task #%d refused %d times — backing off %s between attempts\n",
					name, role, out.Task.ID, refusals[out.Task.ID], refusalBackoff)
				time.Sleep(refusalBackoff)
			}
		}
	}
}

// refusalCap is how many gate refusals of one task a bee absorbs at full speed
// before it reports a finding and throttles; refusalBackoff is that throttle.
const (
	refusalCap     = 5
	refusalBackoff = 60 * time.Second
)

// assistantEcho is what goes into the transcript as the assistant's turn after
// a tool call. With native tool-calling the model's content is typically EMPTY
// (the action lives in tool_calls) — echoing that empty string erases the
// model's own action history, and an amnesiac model repeats the same call
// forever (observed live: gemini-flash issuing search_memory 8× per attempt,
// never progressing to write_file/run_command). Synthesize a description when
// content is empty so every backend sees what it just did.
func assistantEcho(content, callName string, args map[string]any) string {
	if strings.TrimSpace(content) != "" {
		return content
	}
	// Past tense, and shaped like a report rather than an invocation — models
	// mimic transcript patterns, and a mimicked "I called X" is at worst a lie
	// about history, while a mimicked call-shaped line used to become a lost
	// final answer (now extractCall's textual fallback would execute it anyway).
	return fmt.Sprintf("I called %s(%s) and I am using its result.", callName, oneline(jsons(args)))
}

// withRefusalFeedback appends the completion gate's refusal to the task
// instruction so the NEXT attempt's conversation knows what blocked the last
// one — the gate's message is trusted brain output, not agent content.
func withRefusalFeedback(instruction, refusal string) string {
	if refusal == "" {
		return instruction
	}
	return instruction +
		"\n\nYOUR PREVIOUS ATTEMPT WAS REFUSED by the completion gate: " + refusal +
		"\nDo not try to complete again until you have satisfied it — actually run the required command with run_command, read its output, and fix what fails first."
}

// handleTaskError is called when runTask returns a non-nil error. Returns true when
// the caller should skip complete_task and continue the loop (claim was released).
// Extracted for testability — the queue loop calls this immediately after runTask.
//
// Currently only ErrModelUnreachable triggers a release+report; all other errors
// leave the caller to handle (or ignore, preserving the pre-existing behaviour).
func handleTaskError(taskID, missionID int64, modelDesc string, err error, brain func(string, map[string]any) string) bool {
	if !errors.Is(err, ErrModelUnreachable) {
		return false
	}
	brain("release_claims", map[string]any{})
	brain("report_finding", map[string]any{
		"mission_id":       missionID,
		"task_id":          taskID,
		"type":             "note",
		"severity":         "high",
		"target":           fmt.Sprintf("task#%d model", taskID),
		"evidence":         fmt.Sprintf("model unreachable (%s); claim released for reaper re-assignment", modelDesc),
		"suggested_action": "another agent with a reachable model will reclaim once the lease expires",
	})
	return true
}

// runTask drives the LLM to carry out one queued task, coordinating file edits
// through the brain and reporting structured findings. It heartbeats each step so
// a long task isn't reaped.
// mir and bc wire the repo-mirror: for repo missions, runTask works in the
// per-mission snapshot directory instead of the global ws, and tracks every
// write_file/edit_file so push() can send exactly those files back.
//
// Returns (summary, ErrModelUnreachable) when the backend 404s or is
// connection-refused on the very first Chat call — the queue loop uses this to
// release the claim instead of completing with an error string.
func runTask(ctx context.Context, backend Backend, name, role, ws string, brain func(string, map[string]any) string, tools []any, missionID, taskID int64, title, instruction string, mir *mirror, bc brainCall) (string, error) {
	sys := fmt.Sprintf(`You are %q, the %s in a swarm of coding agents sharing ONE codebase, working a task from the mission queue.
Coordinate so you never clobber a peer:
- call claim_paths to lease a file BEFORE you write it.
- if claim_paths reports a conflict, back off that file.
- write REAL files with write_file (the actual artifact), and VERIFY with
  run_command (build it, run the tests, execute it) — don't assume it works.
If you discover a vulnerability, bug, design flaw, or missing requirement, call
report_finding with a type and a severity (low|medium|high|critical) — this is how
the swarm learns and re-plans, so report it rather than only mentioning it.
As you work, call report_thought a few times with a short, SUBSTANTIVE sentence of
your ACTUAL reasoning — what you're examining, deciding, or finding (e.g. "the
ratelimit test fails because the bucket refills too slowly; checking the interval").
Report your real reasoning in your own words, never a fabricated or performative
narration — it's recorded verbatim for the story engine, so its only value is
being true. Skip status filler ("working on it") and skip it entirely if a step
has nothing substantive to say.
Lean on the hive-mind: search_memory FIRST — across EVERY agent and past mission,
someone may have already solved this or hit it before ("have I seen this vuln /
did I patch it?"). And add_memory LIBERALLY as you go — decisions, dead-ends,
gotchas, and lessons (type 'lesson'). Storage is unlimited and the whole swarm
searches what you write, so err on the side of recording over rationing.
Take minimal real steps for THIS task, then stop.
Task: %s
%s`, name, role, title, instruction)
	// Determine the effective working directory for this task. For repo missions,
	// mir.ensure pulls a .git-free snapshot into <ws>/m<id> on the first task and
	// returns that directory; subsequent tasks on the same mission reuse it. For
	// plain missions (non-repo) or if ensure fails, fall back to the global ws.
	wd := ws
	if mir != nil {
		taskWD, isRepo, err := mir.ensure(bc, missionID)
		if err != nil {
			fmt.Printf("[%s/%s] mirror ensure failed for mission #%d: %v — using global ws\n", name, role, missionID, err)
		} else if isRepo {
			wd = taskWD
			fmt.Printf("[%s/%s] repo mission #%d: working in mirror %s\n", name, role, missionID, wd)
		}
	}

	messages := []omsg{{Role: "system", Content: sys}, {Role: "user", Content: "Begin. Take the first step."}}
	last := "completed"
	for step := 0; step < 8; step++ {
		brain("heartbeat", map[string]any{"status": "working"}) // stay present during long work
		m, err := backend.Chat(messages, tools)
		if err != nil {
			if errors.Is(err, ErrModelUnreachable) {
				// Propagate up so runQueueLoop can release the claim and let the
				// reaper reassign the task to an agent with a reachable model.
				return "", ErrModelUnreachable
			}
			fmt.Printf("   ! model: %v\n", err)
			return "error: " + err.Error(), nil
		}
		callName, args, ok := extractCall(m)
		if callName == "report_finding" { // the harness scopes the finding; the model only describes it
			if args == nil {
				args = map[string]any{}
			}
			args["mission_id"] = missionID
			args["task_id"] = taskID
		}
		if callName == "report_thought" { // the harness scopes the thought; the model only supplies the text
			if args == nil {
				args = map[string]any{}
			}
			args["mission_id"] = missionID
			args["role"] = role
			args["name"] = name
		}
		if !ok {
			if c := oneline(m.Content); c != "" {
				last = c
			}
			fmt.Printf("   · %s considers the task done: %s\n", name, last)
			return last, nil
		}
		// wd (not ws) routes write_file/edit_file/run_command to the per-mission
		// mirror when this is a repo task; otherwise wd == ws and behaviour is unchanged.
		result := dispatch(name, role, wd, brain, callName, args)
		fmt.Printf("   → %s(%s)  ⇒  %s\n", callName, oneline(jsons(args)), oneline(result))
		// Track bee-written files so push() sends ONLY these paths back, never build
		// artifacts. writeSucceeded parses the result JSON (robust to formatting) so a
		// brain-side format change can't silently stop tracking and lose the bee's work.
		if mir != nil && (callName == "write_file" || callName == "edit_file") {
			if writeSucceeded(result) {
				if p, ok2 := args["path"].(string); ok2 && p != "" {
					mir.track(missionID, p)
				}
			}
		}
		// Stream this tool-call to the swarm UI console so EVERY phase shows motion,
		// not just the exec phases. run_command already reports via report_execution
		// (it carries the exit code and feeds the verification gate) — and the report_*
		// tools are themselves observability, so skip them to avoid a feedback loop.
		if callName != "run_command" && callName != "report_execution" && callName != "report_activity" && callName != "report_thought" && callName != "heartbeat" {
			brain("report_activity", map[string]any{"role": role, "tool": callName, "detail": oneline(jsons(args))})
		}
		messages = append(messages,
			omsg{Role: "assistant", Content: assistantEcho(m.Content, callName, args)},
			omsg{Role: "user", Content: fmt.Sprintf("[result of %s] %s\nContinue with the next step, or stop if the task is complete.", callName, result)})
		time.Sleep(300 * time.Millisecond)
	}
	return last, nil
}

// roleWork returns the role's job description and its backlog over the shared
// feature set. builder/pentester/reviewer all target the SOURCE files (so they
// contend); tester targets the matching test files.
func roleWork(role string) (job string, tickets []ticket) {
	feats := []struct{ name, src, test string }{
		{"auth", "src/auth/token.go", "tests/auth_test.go"},
		{"api", "src/api/handlers.go", "tests/api_test.go"},
		{"db", "internal/store/db.go", "tests/db_test.go"},
	}
	switch role {
	case "tester":
		job = "You WRITE TESTS for features your peers have built. Check coordination_status to see what's completed, then add a test with edit_file."
		for _, f := range feats {
			tickets = append(tickets, ticket{"TEST-" + f.name, "Write tests for the " + f.name + " feature", f.test})
		}
	case "pentester":
		job = "You are a SECURITY AUDITOR. Inspect a source file for vulnerabilities and record a `// SECURITY:` finding with edit_file."
		for _, f := range feats {
			tickets = append(tickets, ticket{"SEC-" + f.name, "Audit the " + f.name + " code for security issues", f.src})
		}
	case "reviewer":
		job = "You REVIEW code for quality and record a `// REVIEW:` note with edit_file."
		for _, f := range feats {
			tickets = append(tickets, ticket{"REV-" + f.name, "Review the " + f.name + " code", f.src})
		}
	default: // builder
		job = "You IMPLEMENT features by making the change with edit_file."
		for _, f := range feats {
			tickets = append(tickets, ticket{"BUILD-" + f.name, "Implement the " + f.name + " feature", f.src})
		}
	}
	return job, tickets
}

func runTicket(ctx context.Context, backend Backend, name, role, job, ws string, brain func(string, map[string]any) string, tools []any, t ticket, clobber bool) {
	coord := `Coordinate through the broker so you never clobber a peer's work:
- ALWAYS call claim_paths to lease a file BEFORE you edit_file it.
- If claim_paths returns a conflict, DO NOT edit; back off and move on.
- When the change is finished: call mark_done, then release_claims.`
	if clobber {
		coord = `You do NOT coordinate. Call claim_paths, then edit_file the file REGARDLESS of
the result — even if claim_paths reports a conflict, edit it anyway; you never wait
for or defer to peers. Then mark_done.`
	}
	sys := fmt.Sprintf(`You are %q, the %s in a swarm of coding agents sharing ONE codebase.
%s
%s
Work ONLY this ticket, take minimal real steps, then stop.
Ticket %s: %s (file: %s).`, name, role, job, coord, t.id, t.title, t.hint)

	messages := []omsg{{Role: "system", Content: sys}, {Role: "user", Content: "Begin. Take the first step."}}
	for step := 0; step < 8; step++ {
		m, err := backend.Chat(messages, tools)
		if err != nil {
			fmt.Printf("   ! ollama: %v\n", err)
			return
		}
		callName, args, ok := extractCall(m)
		if !ok {
			fmt.Printf("   · %s considers it done: %s\n", name, oneline(m.Content))
			return
		}
		result := dispatch(name, role, ws, brain, callName, args)
		fmt.Printf("   → %s(%s)  ⇒  %s\n", callName, oneline(jsons(args)), oneline(result))
		// Stream this tool-call to the swarm UI console so EVERY phase shows motion,
		// not just the exec phases. run_command already reports via report_execution
		// (it carries the exit code and feeds the verification gate) — and the report_*
		// tools are themselves observability, so skip them to avoid a feedback loop.
		if callName != "run_command" && callName != "report_execution" && callName != "report_activity" && callName != "report_thought" && callName != "heartbeat" {
			brain("report_activity", map[string]any{"role": role, "tool": callName, "detail": oneline(jsons(args))})
		}
		messages = append(messages,
			omsg{Role: "assistant", Content: assistantEcho(m.Content, callName, args)},
			omsg{Role: "user", Content: fmt.Sprintf("[result of %s] %s\nContinue with the next step, or stop if the ticket is complete.", callName, result)})
		time.Sleep(300 * time.Millisecond)
	}
}

// runLeadLoop is the judgment tier: per running mission, read the open findings
// (those the reflex layer left — design flaws, root causes) and the current plan,
// and drive the LLM to re-architect via the mutation tools. Findings it reviews
// are marked addressed so the loop doesn't reprocess them.
func runLeadLoop(ctx context.Context, backend Backend, name string, brain func(string, map[string]any) string) {
	poll := 3 * time.Second
	if v := os.Getenv("AGENT_POLL_SECONDS"); v != "" {
		var n int
		if _, _ = fmt.Sscanf(v, "%d", &n); n > 0 {
			poll = time.Duration(n) * time.Second
		}
	}
	tools := leadTools()
	fmt.Printf("[%s/lead] watching for findings to re-plan around\n", name)
	for {
		var ml struct {
			Missions []struct {
				ID     int64  `json:"id"`
				Status string `json:"status"`
			} `json:"missions"`
		}
		_ = json.Unmarshal([]byte(brain("list_missions", nil)), &ml)
		acted := false
		for _, m := range ml.Missions {
			if m.Status != "running" {
				continue
			}
			var lf struct {
				Findings []struct {
					ID int64 `json:"id"`
				} `json:"findings"`
			}
			findingsJSON := brain("list_findings", map[string]any{"mission_id": m.ID, "status": "open"})
			_ = json.Unmarshal([]byte(findingsJSON), &lf)
			if len(lf.Findings) == 0 {
				continue
			}
			acted = true
			brain("heartbeat", map[string]any{"status": "working"})
			tasksJSON := brain("list_tasks", map[string]any{"mission_id": m.ID})
			fmt.Printf("\n[%s/lead] mission #%d: %d open finding(s) — re-planning\n", name, m.ID, len(lf.Findings))
			runLead(ctx, backend, name, brain, tools, m.ID, findingsJSON, tasksJSON)
			// mark the findings we reviewed addressed so the loop converges
			for _, f := range lf.Findings {
				brain("resolve_finding", map[string]any{"id": f.ID, "status": "addressed"})
			}
		}
		if !acted {
			brain("heartbeat", map[string]any{"status": "idle"}) // #nosec G118 -- the agent's queue/lead poll loops run for the process lifetime by design (AGENT_ROUNDS=0 = forever); not a runaway; graceful ctx shutdown is a robustness nicety, not a vuln
			time.Sleep(poll)
		}
	}
}

// runLead drives one LLM re-planning pass over a mission's open findings + plan.
func runLead(ctx context.Context, backend Backend, name string, brain func(string, map[string]any) string, tools []any, missionID int64, findingsJSON, tasksJSON string) {
	sys := `You are the LEAD of a swarm of coding agents. The reflex layer already handles
straightforward fixes; YOU handle judgment: root causes, design flaws, and rework
that spans the plan. Given the open findings and the current task plan, decide what
to rework — be surgical:
- supersede_task(old_id, key, role, title, instruction): replace a stale task with a reworked one (pending dependents follow automatically).
- cancel_task(id): abandon work no longer needed.
- reopen_task(id): re-do finished work whose foundation changed.
- enqueue_task(key, role, title, instruction): add new rework.
Make ONLY the changes the findings justify, then stop.`
	user := "Open findings (JSON):\n" + findingsJSON + "\n\nCurrent tasks (JSON — note id, key, status):\n" + tasksJSON + "\n\nDecide and apply the rework."
	messages := []omsg{{Role: "system", Content: sys}, {Role: "user", Content: user}}
	for step := 0; step < 8; step++ {
		m, err := backend.Chat(messages, tools)
		if err != nil {
			fmt.Printf("   ! lead model: %v\n", err)
			return
		}
		callName, args, ok := extractCall(m)
		if callName == "enqueue_task" { // the harness scopes new tasks to this mission
			if args == nil {
				args = map[string]any{}
			}
			args["mission_id"] = missionID
		}
		if !ok {
			fmt.Printf("   · lead is satisfied: %s\n", oneline(m.Content))
			return
		}
		result := dispatch(name, "lead", "", brain, callName, args)
		fmt.Printf("   ⚙ %s(%s)  ⇒  %s\n", callName, oneline(jsons(args)), oneline(result))
		messages = append(messages,
			omsg{Role: "assistant", Content: assistantEcho(m.Content, callName, args)},
			omsg{Role: "user", Content: fmt.Sprintf("[result of %s] %s\nContinue, or stop if the rework is complete.", callName, result)})
		time.Sleep(300 * time.Millisecond)
	}
}

// leadTools is the re-planning surface the lead model drives.
func leadTools() []any {
	fn := func(name, desc string, props map[string]any, req ...string) any {
		if req == nil {
			req = []string{} // a no-arg tool must still send required:[] (a list), not null —
			//                  Gemini's OpenAI-compat layer rejects null with "must be a list".
		}
		return map[string]any{"type": "function", "function": map[string]any{
			"name": name, "description": desc,
			"parameters": map[string]any{"type": "object", "properties": props, "required": req},
		}}
	}
	str := map[string]any{"type": "string"}
	intg := map[string]any{"type": "integer"}
	strArr := map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	return []any{
		fn("supersede_task", "Replace a stale task with a reworked one (old → superseded; dependents follow).",
			map[string]any{"old_id": intg, "key": str, "role": str, "title": str, "instruction": str, "depends_on": strArr},
			"old_id", "key", "title", "instruction"),
		fn("cancel_task", "Abandon a task nothing depends on. REFUSED if live tasks depend on it — supersede or retarget instead, or pass cascade:true to take the whole subtree down.",
			map[string]any{"id": intg, "cascade": map[string]any{"type": "boolean"}}, "id"),
		fn("retarget_dependencies", "Re-point every live task that waits on from_key to wait on to_key instead — the recovery when a dependency is dead but another task covers it.",
			map[string]any{"mission_id": intg, "from_key": str, "to_key": str}, "mission_id", "from_key", "to_key"),
		fn("reopen_task", "Re-do a finished task whose foundation changed.", map[string]any{"id": intg}, "id"),
		fn("enqueue_task", "Add new rework to the mission.",
			map[string]any{"key": str, "role": str, "title": str, "instruction": str, "depends_on": strArr},
			"key", "title", "instruction"),
	}
}

// runClientLoop is the stakeholder: per mission awaiting review, it reviews the
// deliverable against the directive and decides — accept (done) or request
// changes (next sprint) — via review_mission. The modeled product owner.
func runClientLoop(ctx context.Context, backend Backend, name string, brain func(string, map[string]any) string) {
	poll := 3 * time.Second
	if v := os.Getenv("AGENT_POLL_SECONDS"); v != "" {
		var n int
		if _, _ = fmt.Sscanf(v, "%d", &n); n > 0 {
			poll = time.Duration(n) * time.Second
		}
	}
	tools := clientTools()
	fmt.Printf("[%s/client] watching for deliverables to review\n", name)
	for {
		var ml struct {
			Missions []struct {
				ID        int64  `json:"id"`
				Status    string `json:"status"`
				Directive string `json:"directive"`
			} `json:"missions"`
		}
		_ = json.Unmarshal([]byte(brain("list_missions", nil)), &ml)
		acted := false
		for _, m := range ml.Missions {
			if m.Status != "awaiting_review" {
				continue
			}
			acted = true
			brain("heartbeat", map[string]any{"status": "working"})
			statusJSON := brain("mission_status", map[string]any{"id": m.ID})
			fmt.Printf("\n[%s/client] reviewing mission #%d: %s\n", name, m.ID, m.Directive)
			runClientReview(ctx, backend, name, brain, tools, m.ID, m.Directive, statusJSON)
		}
		if !acted {
			brain("heartbeat", map[string]any{"status": "idle"}) // #nosec G118 -- the agent's queue/lead poll loops run for the process lifetime by design (AGENT_ROUNDS=0 = forever); not a runaway; graceful ctx shutdown is a robustness nicety, not a vuln
			time.Sleep(poll)
		}
	}
}

// runClientReview drives one LLM verdict on a deliverable.
func runClientReview(ctx context.Context, backend Backend, name string, brain func(string, map[string]any) string, tools []any, missionID int64, directive, statusJSON string) {
	sys := `You are the CLIENT for a swarm of coding agents — the stakeholder who asked for this. You are reviewing the deliverable. Be reasonable but demanding: if it meets the ask, call review_mission with accept=true. If something important is missing or wrong, call review_mission with accept=false and a SPECIFIC, actionable change request. Make exactly one decision, then stop.`
	user := "What you asked for: " + directive + "\n\nWhat the team produced (mission status):\n" + statusJSON + "\n\nReview it now."
	messages := []omsg{{Role: "system", Content: sys}, {Role: "user", Content: user}}
	for step := 0; step < 4; step++ {
		m, err := backend.Chat(messages, tools)
		if err != nil {
			fmt.Printf("   ! client model: %v\n", err)
			return
		}
		callName, args, ok := extractCall(m)
		if callName == "review_mission" { // the harness scopes the verdict to this mission
			if args == nil {
				args = map[string]any{}
			}
			args["id"] = missionID
		}
		if !ok {
			fmt.Printf("   · client deliberating: %s\n", oneline(m.Content))
			messages = append(messages, omsg{Role: "assistant", Content: m.Content},
				omsg{Role: "user", Content: "Decide now: call review_mission to accept or request changes."})
			continue
		}
		result := dispatch(name, "client", "", brain, callName, args)
		fmt.Printf("   ⚖ %s(%s)  ⇒  %s\n", callName, oneline(jsons(args)), oneline(result))
		return // one verdict per review
	}
}

// clientTools is the single decision the client makes.
func clientTools() []any {
	return []any{
		map[string]any{"type": "function", "function": map[string]any{
			"name": "review_mission", "description": "Accept the deliverable (mission done) or request changes with specific feedback (opens the next sprint).",
			"parameters": map[string]any{"type": "object", "properties": map[string]any{
				"accept":   map[string]any{"type": "boolean", "description": "true to accept, false to request changes"},
				"feedback": map[string]any{"type": "string", "description": "the specific change request when accept is false"},
			}, "required": []string{"accept"}},
		}},
	}
}

// execState is resolved once at startup; run_command consults it. The bee itself
// is never jailed — this governs only the subprocess run_command spawns.
type execState struct {
	enabled bool
	backend sandbox.Isolator
	network bool
	reason  string // why exec is unavailable, surfaced to the model
}

var execRuntime execState

// setupExec resolves + preflights the isolation backend once. Default-deny: if
// exec is requested but can't be isolated, it stays disabled with a loud reason.
func setupExec() {
	if os.Getenv("AGENT_ALLOW_EXEC") != "1" {
		execRuntime = execState{reason: "execution disabled — set AGENT_ALLOW_EXEC=1 to enable run_command"}
		return
	}
	iso, err := sandbox.Resolve(sandbox.Config{
		Backend:    os.Getenv("AGENT_EXEC_BACKEND"),
		UnsafeHost: os.Getenv("AGENT_EXEC_UNSAFE_HOST") == "1",
	})
	if err != nil {
		execRuntime = execState{reason: "execution unavailable: " + err.Error() +
			" — install bubblewrap, or set AGENT_EXEC_BACKEND=container, or run in a disposable container with AGENT_EXEC_BACKEND=none AGENT_EXEC_UNSAFE_HOST=1. Refusing to run untrusted commands unprotected."}
		fmt.Printf("[exec] DISABLED: %s\n", execRuntime.reason)
		return
	}
	execRuntime = execState{enabled: true, backend: iso, network: os.Getenv("AGENT_EXEC_NET") == "1"}
	fmt.Printf("[exec] enabled: backend=%s network=%v\n", iso.Name(), execRuntime.network)
}

// dispatch routes a model tool-call: coordination tools go to the brain; edit_file
// mutates the shared workspace (the "real work"). role is the reporting bee's role,
// stamped onto the live execution feed.
func dispatch(name, role, ws string, brain func(string, map[string]any) string, tool string, args map[string]any) string {
	switch tool {
	case "claim_paths", "mark_done", "release_claims", "list_active", "check_instructions", "coordination_status", "report_finding",
		"cancel_task", "reopen_task", "supersede_task", "enqueue_task", "resolve_finding", "review_mission", "search_reference",
		"search_memory", "add_memory", "report_thought":
		// set status working on claim so the swarm shows it live
		if tool == "claim_paths" {
			brain("heartbeat", map[string]any{"status": "working"})
		}
		return brain(tool, args)
	case "edit_file":
		p, _ := args["path"].(string)
		change, _ := args["change"].(string)
		if p == "" {
			return `{"error":"path required"}`
		}
		full := filepath.Join(ws, filepath.Clean("/"+p))
		_ = os.MkdirAll(filepath.Dir(full), 0o700)
		// Read-modify-write: read the CURRENT file, append this agent's edit, write it
		// all back. With coordination, an exclusive claim serializes this so every
		// edit lands. WITHOUT it (the clobber demo), two agents both read the same old
		// content and the later writer overwrites the earlier one's edit — lost work.
		// The work-pause widens that window so the race is reliably visible.
		cur, _ := os.ReadFile(full) // #nosec G304 -- path confined under the agent's own workspace via filepath.Join(ws, filepath.Clean("/"+p)), which collapses traversal; ws is agent-owned
		time.Sleep(1200 * time.Millisecond)
		next := append(cur, []byte(fmt.Sprintf("// [%s] %s\n", name, oneline(change)))...)
		if err := os.WriteFile(full, next, 0o600); err != nil { // #nosec G703 -- path confined under the agent's own workspace via filepath.Join(ws, filepath.Clean("/"+p)), which collapses traversal; ws is agent-owned
			return fmt.Sprintf(`{"error":%q}`, err.Error())
		}
		return fmt.Sprintf(`{"ok":true,"edited":%q}`, p)
	case "write_file":
		// A real file write (the actual artifact), confined to the workspace.
		p, _ := args["path"].(string)
		content, _ := args["content"].(string)
		if p == "" {
			return `{"error":"path required"}`
		}
		full := filepath.Join(ws, filepath.Clean("/"+p))
		_ = os.MkdirAll(filepath.Dir(full), 0o700)
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			return fmt.Sprintf(`{"error":%q}`, err.Error())
		}
		return fmt.Sprintf(`{"ok":true,"path":%q,"bytes":%d}`, p, len(content))
	case "run_command":
		// Real execution — only via the backend resolved at startup. Default-deny:
		// if no backend could isolate, refuse with the reason. The jail wraps this
		// subprocess, never the agent.
		if !execRuntime.enabled {
			return fmt.Sprintf(`{"error":%q}`, execRuntime.reason)
		}
		command, _ := args["command"].(string)
		if command == "" {
			return `{"error":"command required"}`
		}
		res := sandbox.Run(context.Background(), command, sandbox.Options{
			Workspace: ws, Timeout: 120 * time.Second,
			Backend: execRuntime.backend, Network: execRuntime.network,
		})
		// Report the run to the brain so the swarm UI's live execution feed can show
		// it. Best-effort: this is observability, never blocks or alters the result.
		if brain != nil {
			brain("report_execution", map[string]any{
				"role":      role,
				"command":   command,
				"exit_code": res.ExitCode,
				"ok":        res.ExitCode == 0 && !res.TimedOut,
				"timed_out": res.TimedOut,
				"summary":   execSummary(res),
			})
		}
		b, _ := json.Marshal(res)
		return string(b)
	default:
		return fmt.Sprintf(`{"error":"unknown tool %q"}`, tool)
	}
}

// agentTools builds the model-facing tool list. includeSimEdit gates the
// SIMULATED edit_file (dispatch appends "// [name] <one-line summary>" to the
// target file — the coordinated/clobber demos' visible-trample mechanism):
// ticket mode needs it, but a queue-mode mission bee that picks it over
// write_file corrupts its own artifact with squashed comment lines, and a
// verify-gated task ("go build") then livelocks — the gate can never pass.
// Mission bees write real files with write_file only.
func agentTools(includeSimEdit bool) []any {
	fn := func(name, desc string, props map[string]any, req ...string) any {
		if req == nil {
			req = []string{} // a no-arg tool must still send required:[] (a list), not null —
			//                  Gemini's OpenAI-compat layer rejects null with "must be a list".
		}
		return map[string]any{"type": "function", "function": map[string]any{
			"name": name, "description": desc,
			"parameters": map[string]any{"type": "object", "properties": props, "required": req},
		}}
	}
	strArr := map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	str := map[string]any{"type": "string"}
	tools := []any{
		fn("write_file", "Write the FULL content of a file in the workspace — a real file, the actual artifact (not a note). Use this to build.",
			map[string]any{"path": str, "content": str}, "path", "content"),
		fn("run_command", "Run a shell command in the workspace (build, run the tests, execute the program) and get its exit code + output. VERIFY your work with real execution rather than assuming it works.",
			map[string]any{"command": str}, "command"),
		fn("claim_paths", "Lease files/dirs before editing so peers don't collide. Returns {granted, conflicts}.",
			map[string]any{"paths": strArr, "reason": map[string]any{"type": "string"}}, "paths"),
	}
	if includeSimEdit {
		tools = append(tools, fn("edit_file", "Record a change note on a file (appends an attributed one-line comment — the demo's visible edit marker, NOT a code editor).",
			map[string]any{"path": map[string]any{"type": "string"}, "change": map[string]any{"type": "string", "description": "what you changed"}}, "path", "change"))
	}
	tools = append(tools,
		fn("mark_done", "Record that you finished work on some paths so peers don't redo it.",
			map[string]any{"summary": map[string]any{"type": "string"}, "paths": strArr}, "summary"),
		fn("release_claims", "Release your leases when done so peers can take the files.",
			map[string]any{"paths": strArr}, "paths"),
		fn("list_active", "See which peer agents are active and what they're doing.", map[string]any{}),
		fn("coordination_status", "See active peers, their live claims, and recently-completed work (so you can build on what's done).", map[string]any{}),
		fn("search_reference", "Search the reference corpus (docs/URLs/specs the user brought in) for grounding — use this to ground requirements and design in real material.",
			map[string]any{"query": map[string]any{"type": "string"}, "k": map[string]any{"type": "integer"}}, "query"),
		fn("search_memory", "Search the shared memory corpus for prior decisions, conventions, and LESSONS from past missions — consult it to avoid repeating mistakes.",
			map[string]any{"query": map[string]any{"type": "string"}, "type": map[string]any{"type": "string", "description": "filter, e.g. 'lesson'"}}, "query"),
		fn("add_memory", "Add to the swarm's shared hive-mind — write LIBERALLY (decisions, dead-ends, gotchas, lessons). Storage is unlimited and EVERY agent + future mission searches what you record, so err on the side of writing rather than rationing. Use type 'lesson' for what broke + the corrective guidance.",
			map[string]any{
				"name":        map[string]any{"type": "string", "description": "short kebab-case title (the slug)"},
				"body":        map[string]any{"type": "string", "description": "the fact / lesson (markdown)"},
				"description": map[string]any{"type": "string", "description": "one-line summary for recall"},
				"type":        map[string]any{"type": "string", "description": "lesson | reference | project | feedback"},
				"shared":      map[string]any{"type": "boolean", "description": "team-visible (set true for lessons)"},
			}, "name", "body"),
		fn("report_finding", "Report a vulnerability, bug, design flaw, missing requirement, or regression you discovered, with a severity. The swarm records it and re-plans around it.",
			map[string]any{
				"type":             map[string]any{"type": "string", "description": "vuln|bug|design-flaw|missing-req|regression|note"},
				"severity":         map[string]any{"type": "string", "description": "low|medium|high|critical"},
				"target":           map[string]any{"type": "string", "description": "the file or area affected"},
				"evidence":         map[string]any{"type": "string", "description": "what you observed"},
				"suggested_action": map[string]any{"type": "string", "description": "how to fix it"},
			}, "type", "severity"),
		fn("report_thought", "Report a short, substantive piece of YOUR OWN reasoning — what you're examining, deciding, or finding right now, in your own words. This feeds the swarm's story engine (a scrubbable debugger of how the work actually happened), so it must be your REAL reasoning, never a fabricated or dramatized narration. It's a silent no-op unless the mission opted in, so call it freely at genuine decision/finding points — not on every step, and not status filler like 'working on it'.",
			map[string]any{"text": str}, "text"),
	)
	return tools
}

func jsons(v any) string { b, _ := json.Marshal(v); return string(b) }
func oneline(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 110 {
		s = s[:110] + "…"
	}
	return s
}

// execSummary is the one-line gist of a command run for the live feed: the last
// non-empty output line (where build/test results land), trimmed to ~120 chars;
// or the error when the command produced no output (e.g. a timeout or spawn fail).
func execSummary(res sandbox.Result) string {
	var tail []string // the last few non-empty lines, oldest→newest
	lines := strings.Split(res.Output, "\n")
	for i := len(lines) - 1; i >= 0 && len(tail) < 6; i-- {
		if s := strings.TrimSpace(lines[i]); s != "" {
			if len(s) > 160 {
				s = s[:160] + "…"
			}
			tail = append([]string{s}, tail...)
		}
	}
	out := strings.Join(tail, "\n")
	if out == "" {
		out = strings.TrimSpace(res.Err)
	}
	if len(out) > 360 {
		out = out[:360] + "…"
	}
	return out
}

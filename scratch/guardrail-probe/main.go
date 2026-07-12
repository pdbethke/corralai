// SPDX-License-Identifier: Elastic-2.0

// Command guardrail-probe drives a LIVE local brain over MCP and deliberately
// trips each of the six reliability guardrails shipped in PRs #12–#15 and #17,
// asserting the guarded behaviour end-to-end against the real deployed binary.
// It is a throwaway operator tool (scratch/), not part of the build.
//
// Two MCP sessions: `op` (operator/lead — never bootstraps, so it stays a
// non-worker session and may pause/resume) and `wk` (workers — bootstrap,
// heartbeat, claim, complete). Run against a dev-mode brain (auth off) launched
// with CORRALAI_TASK_LEASE_SECONDS=1 so the self-heal reclaim path fires.
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var ctx = context.Background()
var httpBase string

type result struct {
	name   string
	pass   bool
	detail string
}

func main() {
	endpoint := os.Getenv("PROBE_BRAIN")
	if endpoint == "" {
		endpoint = "http://127.0.0.1:9119/mcp/"
	}
	op := connect(endpoint, "probe-operator")
	wk := connect(endpoint, "probe-worker")
	defer op.Close()
	defer wk.Close()

	httpBase = strings.TrimSuffix(strings.TrimSuffix(endpoint, "/"), "/mcp")

	var results []result
	results = append(results, probeDepKey(op, wk))
	results = append(results, probeStaffing(op, wk))
	results = append(results, probeFindingsGate(op, wk))
	results = append(results, probeLeakyPause(op, wk))
	results = append(results, probeSelfHeal(op, wk))
	results = append(results, probeCompose(op, wk))

	fmt.Println("\n================  GUARDRAIL PROBE RESULTS  ================")
	allPass := true
	for _, r := range results {
		mark := "PASS"
		if !r.pass {
			mark = "FAIL"
			allPass = false
		}
		fmt.Printf("[%s] %-28s %s\n", mark, r.name, r.detail)
	}
	fmt.Println("==========================================================")
	if !allPass {
		os.Exit(1)
	}
	fmt.Println("all guardrails held ✓")
}

// ---- MCP helpers ----

func connect(endpoint, name string) *mcp.ClientSession {
	cl := mcp.NewClient(&mcp.Implementation{Name: name, Version: "0"}, nil)
	sess, err := cl.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint}, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect %s: %v\n", name, err)
		os.Exit(2)
	}
	return sess
}

// call returns (isError, text, structuredJSON). It never fails the process on a
// tool error — probes assert on isError themselves.
func call(sess *mcp.ClientSession, name string, args map[string]any) (bool, string, []byte) {
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return true, "transport: " + err.Error(), nil
	}
	txt := ""
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			txt += tc.Text
		}
	}
	b, _ := json.Marshal(res.StructuredContent)
	return res.IsError, txt, b
}

// mustCall aborts the probe run on an unexpected tool error (setup steps).
func mustCall(sess *mcp.ClientSession, name string, args map[string]any) []byte {
	isErr, txt, b := call(sess, name, args)
	if isErr {
		fmt.Fprintf(os.Stderr, "setup %s failed: %s\n", name, txt)
		os.Exit(2)
	}
	return b
}

type missionView struct {
	ID     int64  `json:"id"`
	Status string `json:"status"`
}

type taskT struct {
	ID       int64  `json:"id"`
	Key      string `json:"key"`
	Reissued bool   `json:"reissued"`
}
type claimOut struct {
	Task *taskT `json:"task"`
}

func createMission(op *mcp.ClientSession, directive string, plan []map[string]any) int64 {
	b := mustCall(op, "create_mission", map[string]any{"directive": directive, "plan": plan})
	var mv missionView
	_ = json.Unmarshal(b, &mv)
	if mv.ID == 0 {
		fmt.Fprintf(os.Stderr, "create_mission returned no id: %s\n", b)
		os.Exit(2)
	}
	return mv.ID
}

func status(op *mcp.ClientSession, mid int64) string {
	b := mustCall(op, "mission_status", map[string]any{"id": mid})
	var mv missionView
	_ = json.Unmarshal(b, &mv)
	return mv.Status
}

// claimWithRetry polls claim_task until it returns a task or the deadline passes
// (the engine promotes pending→ready on its ~1s tick).
func claimWithRetry(wk *mcp.ClientSession, name, instance string, roles []string, within time.Duration) *taskT {
	deadline := time.Now().Add(within)
	for {
		args := map[string]any{"name": name, "roles": roles}
		if instance != "" {
			args["instance"] = instance
		}
		b := mustCall(wk, "claim_task", args)
		var co claimOut
		_ = json.Unmarshal(b, &co)
		if co.Task != nil {
			return co.Task
		}
		if time.Now().After(deadline) {
			return nil
		}
		time.Sleep(400 * time.Millisecond)
	}
}

func oneClaim(wk *mcp.ClientSession, name, instance string, roles []string) *taskT {
	args := map[string]any{"name": name, "roles": roles}
	if instance != "" {
		args["instance"] = instance
	}
	b := mustCall(wk, "claim_task", args)
	var co claimOut
	_ = json.Unmarshal(b, &co)
	return co.Task
}

func phase(name, role, instr string) map[string]any {
	return map[string]any{"name": name, "role": role, "instruction": instr}
}

// ---- Probes ----

// #14 — enqueue_task must refuse a dependency key naming no task in the mission.
func probeDepKey(op, wk *mcp.ClientSession) result {
	// A unique role (with a bootstrapped agent so the staffing guard passes) keeps
	// these seed tasks out of every other probe's global claim pool. NB: a role-""
	// task would be claimable by ANY role-typed worker (role IN (roles…, '')).
	const role = "r14only"
	mustCall(wk, "bootstrap", map[string]any{"name": "R14", "role": role, "program": "probe", "task": "seed"})
	mustCall(wk, "heartbeat", map[string]any{"name": "R14", "status": "working"})
	mid := createMission(op, "probe: dep-key validation", []map[string]any{phase("seed", role, "seed")})
	// Seed a task with a KNOWN verbatim key (enqueue_task keys are not suffixed).
	if isErr, txt, _ := call(op, "enqueue_task", map[string]any{
		"mission_id": mid, "key": "probe-base", "role": role, "title": "b", "instruction": "b"}); isErr {
		return result{"#14 dep-key validation", false, "seeding probe-base failed: " + txt}
	}
	// Orphan dep → refused, naming the missing key.
	isErr, txt, _ := call(op, "enqueue_task", map[string]any{
		"mission_id": mid, "key": "orphan", "role": role, "title": "o", "instruction": "o",
		"depends_on": []string{"ghost-key"}})
	if !isErr {
		return result{"#14 dep-key validation", false, "orphan dep was ACCEPTED (want refusal)"}
	}
	if !contains(txt, "ghost-key") {
		return result{"#14 dep-key validation", false, "refused but message didn't name the missing key: " + txt}
	}
	// A dep on the existing probe-base task must be accepted.
	isErr2, txt2, _ := call(op, "enqueue_task", map[string]any{
		"mission_id": mid, "key": "child", "role": role, "title": "c", "instruction": "c",
		"depends_on": []string{"probe-base"}})
	if isErr2 {
		return result{"#14 dep-key validation", false, "valid dep on 'probe-base' was refused: " + txt2}
	}
	return result{"#14 dep-key validation", true, "orphan dep refused (named 'ghost-key'); valid dep accepted"}
}

// #12 — a generalist worker's coverage must be honoured: enqueue for a role only
// a generalist covers is accepted, while the same role is refused with only a
// non-covering agent present.
func probeStaffing(op, wk *mcp.ClientSession) result {
	mid := createMission(op, "probe: staffing false-alarm", []map[string]any{phase("b", "builder", "build")})

	// A builder-only agent present: "perf" is not covered → refuse.
	mustCall(wk, "bootstrap", map[string]any{"name": "Bob", "role": "builder", "program": "probe", "task": "build"})
	mustCall(wk, "heartbeat", map[string]any{"name": "Bob", "status": "working"})
	isErr, _, _ := call(op, "enqueue_task", map[string]any{
		"mission_id": mid, "key": "perf-1", "role": "perf", "title": "p", "instruction": "p"})
	if !isErr {
		return result{"#12 staffing false-alarm", false, "control failed: 'perf' accepted with only a builder present (guard inactive?)"}
	}

	// Add a generalist: "perf" is now covered → accept (this is the fix).
	mustCall(wk, "bootstrap", map[string]any{"name": "Gigi", "role": "generalist", "program": "probe", "task": "cover all"})
	mustCall(wk, "heartbeat", map[string]any{"name": "Gigi", "status": "working"})
	isErr2, txt2, _ := call(op, "enqueue_task", map[string]any{
		"mission_id": mid, "key": "perf-2", "role": "perf", "title": "p", "instruction": "p"})
	if isErr2 {
		return result{"#12 staffing false-alarm", false, "generalist present but 'perf' STILL refused: " + txt2}
	}
	return result{"#12 staffing false-alarm", true, "'perf' refused with builder-only, accepted once a generalist joined"}
}

// #13 — a drained mission holding an open critical finding routes to needs-review,
// not done; resolve_review certifies it once the finding is cleared.
func probeFindingsGate(op, wk *mcp.ClientSession) result {
	// A unique role isolates this mission's task from the global claim pool, and
	// bootstrapping a worker for it keeps the stall watchdog from injecting its
	// own high finding.
	const role = "r13builder"
	mustCall(wk, "bootstrap", map[string]any{"name": "Wilma", "role": role, "program": "probe", "task": "work"})
	mustCall(wk, "heartbeat", map[string]any{"name": "Wilma", "status": "working"})
	mid := createMission(op, "probe: findings gate", []map[string]any{phase("work", role, "do the work")})

	// A non-actionable critical finding (design-flaw) that never becomes a task.
	mustCall(op, "report_finding", map[string]any{
		"name": "reviewer", "mission_id": mid, "type": "design-flaw", "severity": "critical",
		"target": "architecture", "evidence": "the model is unsound"})

	// Drive the single task to done.
	t := claimWithRetry(wk, "Wilma", "host-w", []string{role}, 8*time.Second)
	if t == nil {
		return result{"#13 findings gate", false, "could not claim the work task (not promoted?)"}
	}
	if isErr, txt, _ := call(wk, "complete_task", map[string]any{"id": t.ID, "name": "Wilma", "result": "done"}); isErr {
		return result{"#13 findings gate", false, "complete_task refused: " + txt}
	}

	// Poll for convergence: it must land on needs-review, never done.
	st := waitStatus(op, mid, func(s string) bool { return s != "running" }, 12*time.Second)
	if st == "done" {
		return result{"#13 findings gate", false, "mission certified DONE despite an open critical finding"}
	}
	if st != "needs-review" {
		return result{"#13 findings gate", false, "expected needs-review, got " + st}
	}

	// Resolution: dismiss the finding, then resolve_review → done.
	fid := firstOpenFindingID(op, mid)
	if fid == 0 {
		return result{"#13 findings gate", false, "needs-review reached, but no open finding to resolve"}
	}
	mustCall(op, "resolve_finding", map[string]any{"id": fid, "status": "dismissed"})
	if isErr, txt, _ := call(op, "resolve_review", map[string]any{"id": mid}); isErr {
		return result{"#13 findings gate", false, "resolve_review refused after clearing finding: " + txt}
	}
	if s := status(op, mid); s != "done" {
		return result{"#13 findings gate", false, "after resolve_review, status=" + s + " (want done)"}
	}
	return result{"#13 findings gate", true, "open critical → needs-review (not done); resolve_review → done"}
}

// #17 — a paused mission must not re-issue an orphaned claim; resume restores it.
func probeLeakyPause(op, wk *mcp.ClientSession) result {
	const role = "r17builder"
	mustCall(wk, "bootstrap", map[string]any{"name": "Bam", "role": role, "program": "probe", "task": "x"})
	mustCall(wk, "heartbeat", map[string]any{"name": "Bam", "status": "working"})
	mid := createMission(op, "probe: leaky pause", []map[string]any{phase("halt-work", role, "x")})
	t := claimWithRetry(wk, "Bam", "host-b", []string{role}, 8*time.Second)
	if t == nil {
		return result{"#17 leaky-pause", false, "could not claim the task to hold across the pause"}
	}
	if isErr, txt, _ := call(op, "pause_mission", map[string]any{"id": mid}); isErr {
		return result{"#17 leaky-pause", false, "pause_mission refused (session marked worker?): " + txt}
	}
	// Same name + instance re-poll: the re-issue path must be blocked by the halt.
	if again := oneClaim(wk, "Bam", "host-b", []string{role}); again != nil {
		return result{"#17 leaky-pause", false, fmt.Sprintf("paused mission RE-ISSUED task %q (reissued=%v)", again.Key, again.Reissued)}
	}
	// Resume → the bee gets its own claim re-issued.
	if isErr, txt, _ := call(op, "resume_mission", map[string]any{"id": mid}); isErr {
		return result{"#17 leaky-pause", false, "resume_mission refused: " + txt}
	}
	got := claimWithRetry(wk, "Bam", "host-b", []string{role}, 5*time.Second)
	if got == nil || got.ID != t.ID {
		return result{"#17 leaky-pause", false, "after resume the bee did not get its own claim back"}
	}
	return result{"#17 leaky-pause", true, "no re-issue while paused; re-issued on resume"}
}

// #15 — a force-reclaimed worker is throttled on its next claim; a healthy peer
// still claims the freed task. Needs CORRALAI_TASK_LEASE_SECONDS=1 on the brain.
func probeSelfHeal(op, wk *mcp.ClientSession) result {
	const role = "r15builder"
	_ = createMission(op, "probe: self-heal backoff", []map[string]any{phase("sh", role, "x")})
	mustCall(wk, "bootstrap", map[string]any{"name": "Flaky", "role": role, "program": "probe", "task": "x"})
	mustCall(wk, "heartbeat", map[string]any{"name": "Flaky", "status": "working"})
	t := claimWithRetry(wk, "Flaky", "host-f", []string{role}, 8*time.Second)
	if t == nil {
		return result{"#15 self-heal backoff", false, "could not claim the task to orphan"}
	}
	// Let the 1s lease + 30s idle-reclaim grace elapse, staying present.
	fmt.Println("   … self-heal probe waiting 33s for lease+grace to elapse")
	time.Sleep(33 * time.Second)
	// Idle heartbeat with an expired lease → the brain force-reclaims → Flaky failing.
	mustCall(wk, "heartbeat", map[string]any{"name": "Flaky", "status": "idle"})

	// Flaky's next claim must be throttled (task:null) during the backoff window.
	if again := oneClaim(wk, "Flaky", "host-f", []string{role}); again != nil {
		return result{"#15 self-heal backoff", false, fmt.Sprintf("reclaimed worker was NOT throttled — claimed %q", again.Key)}
	}
	// A healthy peer still claims the freed task.
	peer := claimWithRetry(wk, "Ada", "host-a", []string{role}, 5*time.Second)
	if peer == nil {
		return result{"#15 self-heal backoff", false, "healthy peer could not claim the freed task"}
	}
	return result{"#15 self-heal backoff", true, "reclaimed worker throttled; healthy peer claimed the freed task"}
}

// probeCompose — a mission created with mcp_endpoints + lookbook_ids over the
// HTTP composer must persist its herd and inject the endpoint note + lookbook
// guideline into the herd's task instructions (per-mission herd composer,
// PRs #1-#6 on feat/per-mission-herd-composer). Mixes MCP (register_endpoint,
// list_tasks) with the /api/mission/* HTTP surface.
func probeCompose(op, wk *mcp.ClientSession) result {
	const name = "compose"

	// The HTTP /api/mission/create composer enforces a single running-mission-
	// per-workspace constraint (internal/ui/ui.go createMission). The earlier
	// guardrail probes above leave their own missions running (they never drive
	// them to a terminal state), so clear those out first — otherwise compose
	// creation 409s on a stale mission from a DIFFERENT probe, not a compose bug.
	if b := mustCall(op, "list_missions", map[string]any{}); true {
		var lm struct {
			Missions []struct {
				ID     int64  `json:"id"`
				Status string `json:"status"`
			} `json:"missions"`
		}
		_ = json.Unmarshal(b, &lm)
		for _, m := range lm.Missions {
			if m.Status == "running" {
				_, txt, _ := call(op, "cancel_mission", map[string]any{"id": m.ID})
				_ = txt // best-effort cleanup; a failed cancel surfaces via the create 409 below
			}
		}
	}

	// 1. Register a gateway endpoint (dev owner = "").
	if isErr, txt, _ := call(op, "register_endpoint", map[string]any{
		"name": "compose-probe-ep", "endpoint": "http://127.0.0.1:1/mcp/", "description": "probe"}); isErr {
		return result{name, false, "register_endpoint failed: " + txt}
	}

	// 2. Upload a lookbook item over HTTP. lookbookUpload (internal/ui/ui.go)
	// takes a JSON body {name, description, data: base64 image bytes}, NOT
	// multipart — despite the task brief's assumption, that's what the code
	// does. A 1x1 PNG satisfies the http.DetectContentType("image/png") check.
	onePxPNG := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
		0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89, 0x00, 0x00, 0x00,
		0x0a, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
		0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00, 0x00, 0x00, 0x49,
		0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
	}
	const lbName = "compose-probe-lookbook"
	const lbDesc = "compose-probe-guideline: use a dark theme"
	uploadBody, _ := json.Marshal(map[string]any{
		"name": lbName, "description": lbDesc, "data": base64.StdEncoding.EncodeToString(onePxPNG),
	})
	var uploadOut struct {
		ID int64 `json:"id"`
		OK bool  `json:"ok"`
	}
	status, respBody, err := httpJSON("POST", "/api/lookbook/upload", uploadBody, &uploadOut)
	if err != nil || status != 200 {
		return result{name, false, fmt.Sprintf("lookbook upload failed: status=%d err=%v body=%s", status, err, respBody)}
	}
	if uploadOut.ID == 0 {
		return result{name, false, "lookbook upload returned no id: " + string(respBody)}
	}

	// 3. compose-options must surface both the endpoint and the lookbook item.
	var opts struct {
		Endpoints []struct{ Name string } `json:"endpoints"`
		Lookbook  []struct {
			ID   int64
			Name string
		} `json:"lookbook"`
	}
	status, respBody, err = httpJSON("GET", "/api/mission/compose-options", nil, &opts)
	if err != nil || status != 200 {
		return result{name, false, fmt.Sprintf("compose-options failed: status=%d err=%v body=%s", status, err, respBody)}
	}
	epFound := false
	for _, e := range opts.Endpoints {
		if e.Name == "compose-probe-ep" {
			epFound = true
		}
	}
	if !epFound {
		return result{name, false, "compose-options endpoints[] missing 'compose-probe-ep': " + string(respBody)}
	}
	lbFound := false
	for _, l := range opts.Lookbook {
		if l.ID == uploadOut.ID {
			lbFound = true
		}
	}
	if !lbFound {
		return result{name, false, "compose-options lookbook[] missing uploaded item: " + string(respBody)}
	}

	// 4. Create a mission with mcp_endpoints + lookbook_ids.
	createBody, _ := json.Marshal(map[string]any{
		"directive": "build a dashboard",
		"role_models": map[string]any{
			"builder": map[string]any{"backend": "anthropic", "model": "claude-opus"},
		},
		"mcp_endpoints": []string{"compose-probe-ep"},
		"lookbook_ids":  []int64{uploadOut.ID},
	})
	var createOut struct {
		ID int64 `json:"id"`
	}
	status, respBody, err = httpJSON("POST", "/api/mission/create", createBody, &createOut)
	if err != nil || status != 200 {
		return result{name, false, fmt.Sprintf("mission create failed: status=%d err=%v body=%s", status, err, respBody)}
	}
	if createOut.ID == 0 {
		return result{name, false, "mission create returned no id: " + string(respBody)}
	}

	// 5. The herd context must be injected into the builder task's instruction.
	b := mustCall(op, "list_tasks", map[string]any{"mission_id": createOut.ID})
	var lt struct {
		Tasks []queueTaskT `json:"tasks"`
	}
	_ = json.Unmarshal(b, &lt)
	var builderInstr string
	found := false
	for _, t := range lt.Tasks {
		if t.Role == "builder" {
			builderInstr = t.Instruction
			found = true
			break
		}
	}
	if !found {
		return result{name, false, fmt.Sprintf("no builder task found in mission %d: %s", createOut.ID, b)}
	}
	if !contains(builderInstr, "compose-probe-ep") {
		return result{name, false, "builder instruction missing endpoint note 'compose-probe-ep': " + builderInstr}
	}
	if !contains(builderInstr, lbName) && !contains(builderInstr, lbDesc) {
		return result{name, false, "builder instruction missing injected lookbook guideline: " + builderInstr}
	}
	return result{name, true, fmt.Sprintf("mission %d: herd persisted, builder instruction carries endpoint note + lookbook guideline", createOut.ID)}
}

type queueTaskT struct {
	ID          int64  `json:"id"`
	Role        string `json:"role"`
	Instruction string `json:"instruction"`
}

// ---- small HTTP helpers (the composer surface is HTTP, not MCP) ----

func httpJSON(method, path string, body []byte, out any) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, httpBase+path, reader)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if out != nil && len(respBody) > 0 {
		_ = json.Unmarshal(respBody, out)
	}
	return resp.StatusCode, respBody, nil
}

// ---- small utilities ----

func waitStatus(op *mcp.ClientSession, mid int64, ok func(string) bool, within time.Duration) string {
	deadline := time.Now().Add(within)
	last := ""
	for {
		last = status(op, mid)
		if ok(last) {
			return last
		}
		if time.Now().After(deadline) {
			return last
		}
		time.Sleep(400 * time.Millisecond)
	}
}

func firstOpenFindingID(op *mcp.ClientSession, mid int64) int64 {
	b := mustCall(op, "list_findings", map[string]any{"mission_id": mid, "status": "open"})
	var out struct {
		Findings []struct {
			ID int64 `json:"id"`
		} `json:"findings"`
	}
	_ = json.Unmarshal(b, &out)
	if len(out.Findings) == 0 {
		return 0
	}
	return out.Findings[0].ID
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

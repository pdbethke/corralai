// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/coord"
	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/telemetry"
)

func TestFindingToolsOverMCP(t *testing.T) {
	dir := t.TempDir()
	cstore, _ := coord.Open(filepath.Join(dir, "c.sqlite3"))
	t.Cleanup(func() { cstore.Close() })
	q, _ := queue.Open(filepath.Join(dir, "q.sqlite3"))
	t.Cleanup(func() { q.Close() })
	// A claimed task to complete-with-finding.
	q.Enqueue(7, []queue.TaskSpec{{Key: "pentest#1", Role: "pentester", Title: "pentest", Instruction: "attack it"}})
	q.PromoteReady(7)
	claimedTask, _ := q.ClaimNext("Hawk", []string{"pentester"}, 300)

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() { _ = NewServer(cstore, nil, Options{Queue: q}).Run(ctx, serverT) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "bee", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sess.Close()

	// report_finding (standalone) → returns an id.
	var rf findingOut
	callTask(t, sess, "report_finding", map[string]any{
		"name": "Hawk", "mission_id": 7, "type": "vuln", "severity": "high",
		"target": "score-API", "evidence": "SQLi", "suggested_action": "parameterize",
	}, &rf)
	if rf.ID == 0 {
		t.Fatal("report_finding did not return an id")
	}

	// complete_task carrying a finding → task done AND finding recorded (critical).
	var ct completeTaskOut
	callTask(t, sess, "complete_task", map[string]any{
		"id": claimedTask.ID, "name": "Hawk", "result": "attacked",
		"findings": []map[string]any{{"type": "vuln", "severity": "critical", "target": "auth", "evidence": "bypass"}},
	}, &ct)
	if !ct.OK {
		t.Fatal("complete_task with findings failed")
	}

	// list_findings (mission 7) → both findings, the critical one present.
	var lf listFindingsOut
	callTask(t, sess, "list_findings", map[string]any{"mission_id": 7}, &lf)
	if len(lf.Findings) != 2 {
		t.Fatalf("want 2 findings for mission 7, got %d", len(lf.Findings))
	}
	var sawCritical bool
	for _, f := range lf.Findings {
		if f.Severity == "critical" && f.TaskID == claimedTask.ID {
			sawCritical = true
		}
	}
	if !sawCritical {
		t.Fatal("the finding attached at complete_task is not scoped to its task")
	}

	// Invalid severity → the tool reports an error (junk is rejected).
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "report_finding",
		Arguments: map[string]any{"name": "Hawk", "mission_id": 7, "type": "vuln", "severity": "spicy"}})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !res.IsError {
		t.Fatal("invalid severity was accepted; want a tool error")
	}
}

func TestReplanMutationToolsOverMCP(t *testing.T) {
	dir := t.TempDir()
	cstore, _ := coord.Open(filepath.Join(dir, "c.sqlite3"))
	t.Cleanup(func() { cstore.Close() })
	q, _ := queue.Open(filepath.Join(dir, "q.sqlite3"))
	t.Cleanup(func() { q.Close() })
	q.Enqueue(3, []queue.TaskSpec{
		{Key: "build", Role: "builder", Title: "build", Instruction: "old"},
		{Key: "extra", Role: "builder", Title: "extra", Instruction: "x"},
	})
	q.PromoteReady(3)
	tasks, _ := q.List(3)
	id := map[string]int64{}
	for _, tk := range tasks {
		id[tk.Key] = tk.ID
	}

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() { _ = NewServer(cstore, nil, Options{Queue: q}).Run(ctx, serverT) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "lead", Version: "0"}, nil)
	sess, _ := client.Connect(ctx, clientT, nil)
	defer sess.Close()

	// enqueue_task adds rework to the live mission.
	var enq okOut
	callTask(t, sess, "enqueue_task", map[string]any{"mission_id": 3, "key": "rework", "role": "builder", "title": "rework", "instruction": "do it"}, &enq)
	if !enq.OK {
		t.Fatal("enqueue_task failed")
	}

	// cancel_task abandons the extra task.
	var canc okOut
	callTask(t, sess, "cancel_task", map[string]any{"id": id["extra"]}, &canc)
	if !canc.OK {
		t.Fatal("cancel_task failed")
	}

	// supersede_task replaces build with a reworked version (lineage).
	var sup supersedeOut
	callTask(t, sess, "supersede_task", map[string]any{"old_id": id["build"], "key": "build-v2", "role": "builder", "title": "build", "instruction": "rebuilt"}, &sup)
	if !sup.OK || sup.NewID == 0 {
		t.Fatalf("supersede_task failed: %+v", sup)
	}

	// Verify state: extra cancelled, build superseded, build-v2 carries lineage.
	final, _ := q.List(3)
	st := map[string]string{}
	var v2Supersedes int64
	for _, tk := range final {
		st[tk.Key] = tk.Status
		if tk.Key == "build-v2" {
			v2Supersedes = tk.Supersedes
		}
	}
	if st["extra"] != queue.StatusCancelled {
		t.Fatalf("extra = %q, want cancelled", st["extra"])
	}
	if st["build"] != queue.StatusSuperseded {
		t.Fatalf("build = %q, want superseded", st["build"])
	}
	if v2Supersedes != id["build"] {
		t.Fatalf("build-v2.supersedes = %d, want %d (lineage)", v2Supersedes, id["build"])
	}
}

// TestReportFindingStampsModelFromHostBook verifies that report_finding stamps
// ReporterModel/ReporterBackend from the HostBook, and that a reporter with no
// HostBook entry degrades to "" without error (finding still files).
func TestReportFindingStampsModelFromHostBook(t *testing.T) {
	dir := t.TempDir()
	cstore, _ := coord.Open(filepath.Join(dir, "c.sqlite3"))
	t.Cleanup(func() { cstore.Close() })
	q, _ := queue.Open(filepath.Join(dir, "q.sqlite3"))
	t.Cleanup(func() { q.Close() })
	tel, err := telemetry.Open(filepath.Join(dir, "tel.duckdb"))
	if err != nil {
		t.Fatalf("telemetry.Open: %v", err)
	}
	t.Cleanup(func() { tel.Close() })

	// Build a HostBook with Hawk registered.
	book := NewHostBook()
	book.Set(Host{Agent: "Hawk", Model: "gemini-3", Backend: "gemini", TS: 9_999_999_999})

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() {
		_ = NewServer(cstore, nil, Options{Queue: q, HostBook: book, Telemetry: tel}).Run(ctx, serverT)
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "bee", Version: "0"}, nil)
	sess, connErr := client.Connect(ctx, clientT, nil)
	if connErr != nil {
		t.Fatalf("connect: %v", connErr)
	}
	defer sess.Close()

	// Hawk (in HostBook) → model must be stamped on the stored finding.
	var rf findingOut
	callTask(t, sess, "report_finding", map[string]any{
		"name": "Hawk", "mission_id": 1, "type": "vuln", "severity": "high", "target": "auth",
	}, &rf)
	if rf.ID == 0 {
		t.Fatal("report_finding returned id=0")
	}

	fs, err := q.Findings(1, "")
	if err != nil || len(fs) == 0 {
		t.Fatalf("Findings: err=%v len=%d", err, len(fs))
	}
	f := fs[0]
	if f.ReporterModel != "gemini-3" {
		t.Errorf("ReporterModel: got %q, want %q", f.ReporterModel, "gemini-3")
	}
	if f.ReporterBackend != "gemini" {
		t.Errorf("ReporterBackend: got %q, want %q", f.ReporterBackend, "gemini")
	}

	// Orphan (no HostBook entry) → model="" but finding still files successfully.
	var rf2 findingOut
	callTask(t, sess, "report_finding", map[string]any{
		"name": "Orphan", "mission_id": 1, "type": "note", "severity": "low", "target": "readme",
	}, &rf2)
	if rf2.ID == 0 {
		t.Fatal("orphan report_finding returned id=0 — must still file without a HostBook entry")
	}
	all, _ := q.Findings(1, "")
	var orphanF *queue.Finding
	for i := range all {
		if all[i].Reporter == "Orphan" {
			orphanF = &all[i]
			break
		}
	}
	if orphanF == nil {
		t.Fatal("orphan finding not stored")
	}
	if orphanF.ReporterModel != "" {
		t.Errorf("orphan ReporterModel: got %q, want empty", orphanF.ReporterModel)
	}

	// Telemetry event for the first finding must carry the model.
	rpt, err := tel.Query(`SELECT model FROM events WHERE kind='finding_reported' ORDER BY id LIMIT 1`)
	if err != nil {
		t.Fatalf("telemetry query: %v", err)
	}
	if len(rpt.Rows) == 0 {
		t.Fatal("no finding_reported event in telemetry")
	}
	if modelStr := varcharStr(rpt.Rows[0][0]); modelStr != "gemini-3" {
		t.Errorf("telemetry finding_reported model: got %q, want %q", modelStr, "gemini-3")
	}
}

// TestCompleteTaskInlineFindingStampsModel proves the OTHER finding path — the
// findings attached inline to complete_task — also stamps the model on BOTH the
// finding row and the finding_reported telemetry event.
func TestCompleteTaskInlineFindingStampsModel(t *testing.T) {
	dir := t.TempDir()
	cstore, _ := coord.Open(filepath.Join(dir, "c.sqlite3"))
	t.Cleanup(func() { cstore.Close() })
	q, _ := queue.Open(filepath.Join(dir, "q.sqlite3"))
	t.Cleanup(func() { q.Close() })
	tel, err := telemetry.Open(filepath.Join(dir, "tel.duckdb"))
	if err != nil {
		t.Fatalf("telemetry.Open: %v", err)
	}
	t.Cleanup(func() { tel.Close() })

	// A claimed task Hawk can complete-with-finding.
	q.Enqueue(7, []queue.TaskSpec{{Key: "pentest#1", Role: "pentester", Title: "pentest", Instruction: "attack it"}})
	q.PromoteReady(7)
	claimedTask, _ := q.ClaimNext("Hawk", []string{"pentester"}, 300)

	// HostBook with Hawk registered.
	book := NewHostBook()
	book.Set(Host{Agent: "Hawk", Model: "claude-opus", Backend: "anthropic", TS: 9_999_999_999})

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() {
		_ = NewServer(cstore, nil, Options{Queue: q, HostBook: book, Telemetry: tel}).Run(ctx, serverT)
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "bee", Version: "0"}, nil)
	sess, connErr := client.Connect(ctx, clientT, nil)
	if connErr != nil {
		t.Fatalf("connect: %v", connErr)
	}
	defer sess.Close()

	// complete_task carrying an inline finding → model must thread to the row.
	var ct completeTaskOut
	callTask(t, sess, "complete_task", map[string]any{
		"id": claimedTask.ID, "name": "Hawk", "result": "attacked",
		"findings": []map[string]any{{"type": "vuln", "severity": "critical", "target": "auth", "evidence": "bypass"}},
	}, &ct)
	if !ct.OK {
		t.Fatal("complete_task with inline findings failed")
	}

	fs, err := q.Findings(7, "")
	if err != nil || len(fs) == 0 {
		t.Fatalf("Findings: err=%v len=%d", err, len(fs))
	}
	f := fs[0]
	if f.ReporterModel != "claude-opus" {
		t.Errorf("inline finding ReporterModel: got %q, want %q", f.ReporterModel, "claude-opus")
	}
	if f.ReporterBackend != "anthropic" {
		t.Errorf("inline finding ReporterBackend: got %q, want %q", f.ReporterBackend, "anthropic")
	}

	// Telemetry finding_reported event must carry the model too.
	rpt, err := tel.Query(`SELECT model FROM events WHERE kind='finding_reported' ORDER BY id LIMIT 1`)
	if err != nil {
		t.Fatalf("telemetry query: %v", err)
	}
	if len(rpt.Rows) == 0 {
		t.Fatal("no finding_reported event in telemetry for the inline finding")
	}
	if modelStr := varcharStr(rpt.Rows[0][0]); modelStr != "claude-opus" {
		t.Errorf("telemetry inline finding_reported model: got %q, want %q", modelStr, "claude-opus")
	}
}

// varcharStr coerces a DuckDB VARCHAR cell to string — the go-duckdb driver may
// return it as string or []byte depending on version.
func varcharStr(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

// TestResolveFindingEmitsFindingResolved verifies that resolve_finding emits a
// finding_resolved telemetry event carrying the model + outcome, and that a
// finding with ReporterModel=="" still resolves successfully with Model=="".
func TestResolveFindingEmitsFindingResolved(t *testing.T) {
	dir := t.TempDir()
	cstore, _ := coord.Open(filepath.Join(dir, "c.sqlite3"))
	t.Cleanup(func() { cstore.Close() })
	q, _ := queue.Open(filepath.Join(dir, "q.sqlite3"))
	t.Cleanup(func() { q.Close() })
	tel, err := telemetry.Open(filepath.Join(dir, "tel.duckdb"))
	if err != nil {
		t.Fatalf("telemetry.Open: %v", err)
	}
	t.Cleanup(func() { tel.Close() })

	// Pre-seed a finding with a known model so we can resolve it over MCP.
	fid, err := q.AddFinding(queue.Finding{
		MissionID: 7, Reporter: "Hawk", Type: "vuln", Severity: "high",
		Target: "auth", ReporterModel: "gemini-3", ReporterBackend: "gemini",
	})
	if err != nil {
		t.Fatalf("AddFinding: %v", err)
	}

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() {
		_ = NewServer(cstore, nil, Options{Queue: q, Telemetry: tel}).Run(ctx, serverT)
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "lead", Version: "0"}, nil)
	sess, connErr := client.Connect(ctx, clientT, nil)
	if connErr != nil {
		t.Fatalf("connect: %v", connErr)
	}
	defer sess.Close()

	// Resolve the finding → must succeed.
	var out okOut
	callTask(t, sess, "resolve_finding", map[string]any{"id": fid, "status": "dismissed"}, &out)
	if !out.OK {
		t.Fatal("resolve_finding returned ok=false")
	}

	// finding_resolved telemetry event must be emitted with model + mission + detail.
	rpt, err := tel.Query(`SELECT model, mission_id, detail FROM events WHERE kind='finding_resolved' ORDER BY id LIMIT 1`)
	if err != nil {
		t.Fatalf("telemetry query: %v", err)
	}
	if len(rpt.Rows) == 0 {
		t.Fatal("no finding_resolved event in telemetry")
	}
	row := rpt.Rows[0]
	if modelStr := varcharStr(row[0]); modelStr != "gemini-3" {
		t.Errorf("finding_resolved model: got %q, want %q", modelStr, "gemini-3")
	}
	if midVal := row[1]; fmt.Sprintf("%v", midVal) != "7" {
		t.Errorf("finding_resolved mission_id: got %v, want 7", midVal)
	}
	detailStr := varcharStr(row[2])
	if detailStr == "" || detailStr == "null" {
		t.Errorf("finding_resolved detail is empty: %q", detailStr)
	}
	// detail must contain outcome=dismissed and finding_id.
	if !jsonContains(detailStr, "outcome", "dismissed") {
		t.Errorf("finding_resolved detail missing outcome=dismissed: %s", detailStr)
	}

	// ReporterModel=="" → event still emitted with Model=="", resolve still succeeds.
	fid2, _ := q.AddFinding(queue.Finding{
		MissionID: 7, Reporter: "Orphan", Type: "note", Severity: "low", Target: "readme",
	})
	var out2 okOut
	callTask(t, sess, "resolve_finding", map[string]any{"id": fid2, "status": "addressed"}, &out2)
	if !out2.OK {
		t.Fatal("resolve_finding for empty-model finding returned ok=false")
	}
	rpt2, err := tel.Query(`SELECT model FROM events WHERE kind='finding_resolved' ORDER BY id DESC LIMIT 1`)
	if err != nil {
		t.Fatalf("telemetry query (orphan): %v", err)
	}
	if len(rpt2.Rows) == 0 {
		t.Fatal("no finding_resolved event for empty-model finding")
	}
	if modelStr := varcharStr(rpt2.Rows[0][0]); modelStr != "" {
		t.Errorf("empty-model finding_resolved: got model %q, want \"\"", modelStr)
	}
}

// jsonContains is a quick-and-dirty check that a JSON string contains a key:value pair.
func jsonContains(s, key, val string) bool {
	return strings.Contains(s, fmt.Sprintf("%q", key)) && strings.Contains(s, fmt.Sprintf("%q", val))
}

func TestListFindingsByModel(t *testing.T) {
	dir := t.TempDir()
	cstore, _ := coord.Open(filepath.Join(dir, "c.sqlite3"))
	t.Cleanup(func() { cstore.Close() })
	q, _ := queue.Open(filepath.Join(dir, "q.sqlite3"))
	t.Cleanup(func() { q.Close() })

	book := NewHostBook()
	book.Set(Host{Agent: "A", Model: "claude-opus", Backend: "anthropic", TS: 9_999_999_999})
	book.Set(Host{Agent: "B", Model: "gemini-3", Backend: "gemini", TS: 9_999_999_999})

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() { _ = NewServer(cstore, nil, Options{Queue: q, HostBook: book}).Run(ctx, serverT) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "bee", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sess.Close()

	// File three findings: two from A (claude-opus), one from B (gemini-3).
	for i := 0; i < 2; i++ {
		var rf findingOut
		callTask(t, sess, "report_finding", map[string]any{
			"name": "A", "mission_id": int64(1), "type": "vuln", "severity": "high", "target": fmt.Sprintf("t%d", i),
		}, &rf)
	}
	var rf findingOut
	callTask(t, sess, "report_finding", map[string]any{
		"name": "B", "mission_id": int64(1), "type": "note", "severity": "low", "target": "b-target",
	}, &rf)

	// by_model:"gemini-3" → exactly 1 finding.
	var lf listFindingsOut
	callTask(t, sess, "list_findings", map[string]any{"by_model": "gemini-3"}, &lf)
	if len(lf.Findings) != 1 {
		t.Fatalf("by_model=gemini-3: want 1, got %d", len(lf.Findings))
	}
	if lf.Findings[0].ReporterModel != "gemini-3" {
		t.Errorf("wrong model: %q", lf.Findings[0].ReporterModel)
	}

	// empty by_model → all 3.
	var lfall listFindingsOut
	callTask(t, sess, "list_findings", map[string]any{}, &lfall)
	if len(lfall.Findings) != 3 {
		t.Fatalf("no by_model: want 3, got %d", len(lfall.Findings))
	}
}

// Bug #23 (role deadlock): the lead's re-planning LLM sometimes enqueues a
// task for a role name it invented ("performance" instead of "perf"). No
// agent serves that role, the task can never be claimed, MissionDone requires
// zero open tasks — so one typo'd role deadlocks the whole mission. The brain
// must refuse a role that is not already part of the mission's own task plan,
// loudly enough that the lead can self-correct with a valid role.
func TestEnqueueTaskRefusesUnstaffedRole(t *testing.T) {
	dir := t.TempDir()
	cstore, _ := coord.Open(filepath.Join(dir, "c.sqlite3"))
	t.Cleanup(func() { cstore.Close() })
	q, _ := queue.Open(filepath.Join(dir, "q.sqlite3"))
	t.Cleanup(func() { q.Close() })
	q.Enqueue(9, []queue.TaskSpec{
		{Key: "build", Role: "builder", Title: "build", Instruction: "b"},
		{Key: "perf", Role: "perf", Title: "perf", Instruction: "p"},
	})

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() { _ = NewServer(cstore, nil, Options{Queue: q}).Run(ctx, serverT) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "lead", Version: "0"}, nil)
	sess, _ := client.Connect(ctx, clientT, nil)
	defer sess.Close()

	// An invented role is refused with the valid roles named in the error.
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "enqueue_task",
		Arguments: map[string]any{"mission_id": 9, "key": "perf-2", "role": "performance", "title": "perf", "instruction": "again"}})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !res.IsError {
		t.Fatal("an unstaffed role was accepted; want a tool error")
	}
	text := ""
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text += tc.Text
		}
	}
	if !strings.Contains(text, "perf") || !strings.Contains(text, "performance") {
		t.Fatalf("refusal must name the bad role and the valid ones, got: %s", text)
	}

	// A role the mission already staffs is still accepted.
	var enq okOut
	callTask(t, sess, "enqueue_task", map[string]any{"mission_id": 9, "key": "perf-3", "role": "perf", "title": "perf", "instruction": "again"}, &enq)
	if !enq.OK {
		t.Fatal("a staffed role must still be accepted")
	}
}

// If active agent roles are known, enqueue/supersede validation should use the
// live role set (present agents), not just historical mission-plan roles.
func TestEnqueueTaskUsesActiveRolesWhenPresent(t *testing.T) {
	dir := t.TempDir()
	cstore, _ := coord.Open(filepath.Join(dir, "c.sqlite3"))
	t.Cleanup(func() { cstore.Close() })
	q, _ := queue.Open(filepath.Join(dir, "q.sqlite3"))
	t.Cleanup(func() { q.Close() })
	// Mission plan includes "perf", but no perf agent is currently present.
	q.Enqueue(7, []queue.TaskSpec{
		{Key: "build", Role: "builder", Title: "build", Instruction: "b"},
		{Key: "perf", Role: "perf", Title: "perf", Instruction: "p"},
	})
	if err := cstore.Register("Ada", "test", "", "", "", "builder"); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() { _ = NewServer(cstore, nil, Options{Queue: q}).Run(ctx, serverT) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "lead", Version: "0"}, nil)
	sess, _ := client.Connect(ctx, clientT, nil)
	defer sess.Close()

	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "enqueue_task",
		Arguments: map[string]any{"mission_id": 7, "key": "perf-2", "role": "perf", "title": "perf", "instruction": "again"}})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !res.IsError {
		t.Fatal("enqueue_task accepted role with no active agents; want refusal")
	}
	text := ""
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text += tc.Text
		}
	}
	if !strings.Contains(text, "no active") || !strings.Contains(text, "builder") {
		t.Fatalf("refusal should mention active roles and correction path, got: %s", text)
	}
}

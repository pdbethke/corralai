// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/coord"
	"github.com/pdbethke/corralai/internal/queue"
)

func TestReportExecutionPersists(t *testing.T) {
	dir := t.TempDir()
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.Close() })

	if err := q.Enqueue(5, []queue.TaskSpec{{Key: "build#1", Role: "builder", Title: "build", Instruction: "build", Verify: "go build"}}); err != nil {
		t.Fatal(err)
	}
	q.PromoteReady(5)
	q.ClaimNext("Bob", []string{"builder"}, 300)

	cstore, err := coord.Open(filepath.Join(dir, "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cstore.Close() })

	ring := NewExecRing()
	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() { _ = NewServer(cstore, nil, Options{Queue: q, ExecRing: ring}).Run(ctx, serverT) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "bee", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sess.Close()

	res, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name: "report_execution",
		Arguments: map[string]any{
			"name": "Bob", "role": "builder", "command": "go build ./...",
			"exit_code": 0, "ok": true, "timed_out": false, "summary": "ok",
		},
	})
	if err != nil {
		t.Fatalf("report_execution: %v", err)
	}
	if res.IsError {
		t.Fatalf("report_execution tool error: %v", res.Content)
	}

	pass, err := q.MissionPassedVerify(5, "go build")
	if err != nil {
		t.Fatal(err)
	}
	if !pass {
		t.Fatal("report_execution did not persist Bob's passing build to the durable table")
	}
}

func TestExecRingCapsAt40(t *testing.T) {
	r := NewExecRing()
	for i := 0; i < 45; i++ {
		r.Add(Execution{Command: fmt.Sprintf("cmd-%d", i)})
	}
	got := r.Recent()
	if len(got) != 40 {
		t.Fatalf("want 40 items, got %d", len(got))
	}
	// newest item was added last (i=44), so it must be first
	if got[0].Command != "cmd-44" {
		t.Fatalf("want newest first (cmd-44), got %q", got[0].Command)
	}
}

func TestExecRingOkField(t *testing.T) {
	r := NewExecRing()

	r.Add(Execution{Command: "ok-cmd", ExitCode: 0, Ok: true})
	got := r.Recent()
	if !got[0].Ok {
		t.Fatalf("expected Ok=true for exit-code-0 execution, got false")
	}

	r.Add(Execution{Command: "fail-cmd", ExitCode: 1, Ok: false})
	got = r.Recent()
	if got[0].Ok {
		t.Fatalf("expected Ok=false for exit-code-1 execution, got true")
	}
}

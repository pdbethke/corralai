// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/coord"
	"github.com/pdbethke/corralai/internal/queue"
)

// Bug #40, part 1: a done bee must not keep its path leases. Twice observed
// live (runs A-2 and C-1): an agent finished its task but held an exclusive
// 3600s lease on the artifact, so every peer that needed the file backed off
// until the mission livelocked. Completing a task now releases the completer's
// claims — the same cleanup despawn already does.
func TestCompleteTaskReleasesClaims(t *testing.T) {
	dir := t.TempDir()
	cstore, _ := coord.Open(filepath.Join(dir, "c.sqlite3"))
	t.Cleanup(func() { cstore.Close() })
	q, _ := queue.Open(filepath.Join(dir, "q.sqlite3"))
	t.Cleanup(func() { q.Close() })
	q.Enqueue(4, []queue.TaskSpec{{Key: "build", Role: "builder", Title: "b", Instruction: "b"}})
	q.PromoteReady(4)
	task, _ := q.ClaimNext("Bob", []string{"builder"}, 300)
	// Whois answers nil (not empty) for unregistered agents — register so the
	// post-completion claim assertions are real, not vacuous.
	if err := cstore.Register("Bob", "test", "", "", "", "builder"); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() { _ = NewServer(cstore, nil, Options{Queue: q}).Run(ctx, serverT) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "bee", Version: "0"}, nil)
	sess, _ := client.Connect(ctx, clientT, nil)
	defer sess.Close()

	var cr coord.ClaimResult
	callTask(t, sess, "claim_paths", map[string]any{"name": "Bob", "paths": []string{"stack.go", "stack_test.go"}}, &cr)
	if len(cr.Granted) != 2 {
		t.Fatalf("setup: want both paths granted, got %+v", cr)
	}
	if _, claims, _ := cstore.Whois("Bob"); len(claims) != 2 {
		t.Fatalf("setup: Whois must see Bob's 2 live claims, got %+v", claims)
	}

	var done completeTaskOut
	callTask(t, sess, "complete_task", map[string]any{"name": "Bob", "id": task.ID, "result": "built"}, &done)
	if !done.OK {
		t.Fatalf("complete refused: %+v", done)
	}

	_, claims, err := cstore.Whois("Bob")
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 0 {
		t.Fatalf("Bob completed his task but still holds %d claim(s): %+v — the #40 dead-lease", len(claims), claims)
	}
}

// Bug #40, part 2: the refusal-loop escalation. When the verify gate refuses
// the same task from the same bee escalationRefusals times, the brain must
// (a) force-release path leases held by agents with no claimed task — the
// stale leases that starve remediation — and (b) reset the counter so the
// escalation can fire again rather than only once. C-1 died with 35 silent
// refusals while a long-done researcher held the artifact's lease.
func TestVerifyRefusalEscalationReleasesStaleClaims(t *testing.T) {
	dir := t.TempDir()
	cstore, _ := coord.Open(filepath.Join(dir, "c.sqlite3"))
	t.Cleanup(func() { cstore.Close() })
	q, _ := queue.Open(filepath.Join(dir, "q.sqlite3"))
	t.Cleanup(func() { q.Close() })
	q.Enqueue(9, []queue.TaskSpec{{Key: "test#1", Role: "tester", Title: "t", Instruction: "t", Verify: "node --test"}})
	q.PromoteReady(9)
	task, _ := q.ClaimNext("Tess", []string{"tester"}, 300)
	// Whois answers nil (not empty) for unregistered agents — register both so
	// every claim assertion below is real, not vacuous.
	for _, a := range []struct{ name, role string }{{"Sage", "researcher"}, {"Tess", "tester"}} {
		if err := cstore.Register(a.name, "test", "", "", "", a.role); err != nil {
			t.Fatal(err)
		}
	}

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() { _ = NewServer(cstore, nil, Options{Queue: q}).Run(ctx, serverT) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "bee", Version: "0"}, nil)
	sess, _ := client.Connect(ctx, clientT, nil)
	defer sess.Close()

	// Sage finished research long ago (no claimed task) but still leases the
	// artifact — the C-1 wedge. Tess also leases her own test file; hers must
	// survive the escalation (she holds a claimed task, she is not stale).
	var cr coord.ClaimResult
	callTask(t, sess, "claim_paths", map[string]any{"name": "Sage", "paths": []string{"lru.js"}}, &cr)
	callTask(t, sess, "claim_paths", map[string]any{"name": "Tess", "paths": []string{"test/lru.test.js"}}, &cr)

	// No passing "node --test" execution recorded => every completion refused.
	for i := 0; i < escalationRefusals; i++ {
		var out completeTaskOut
		callTask(t, sess, "complete_task", map[string]any{"name": "Tess", "id": task.ID, "result": "looks done"}, &out)
		if out.OK {
			t.Fatalf("refusal %d: gate accepted an unverified completion", i+1)
		}
	}

	_, sageClaims, err := cstore.Whois("Sage")
	if err != nil {
		t.Fatal(err)
	}
	if len(sageClaims) != 0 {
		t.Fatalf("after %d refusals Sage (no claimed task) still holds %+v — escalation did not fire", escalationRefusals, sageClaims)
	}
	_, tessClaims, err := cstore.Whois("Tess")
	if err != nil {
		t.Fatal(err)
	}
	if len(tessClaims) != 1 {
		t.Fatalf("escalation must spare the active claimer's own leases; Tess has %+v", tessClaims)
	}

	// Counter reset: the next escalationRefusals refusals fire again (Sage
	// re-leases in between — e.g. a lead redispatch — and is re-released).
	callTask(t, sess, "claim_paths", map[string]any{"name": "Sage", "paths": []string{"lru.js"}}, &cr)
	for i := 0; i < escalationRefusals; i++ {
		var out completeTaskOut
		callTask(t, sess, "complete_task", map[string]any{"name": "Tess", "id": task.ID, "result": "still not verified"}, &out)
	}
	_, sageClaims, _ = cstore.Whois("Sage")
	if len(sageClaims) != 0 {
		t.Fatalf("second escalation window did not fire (counter not reset): Sage holds %+v", sageClaims)
	}
}

// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/coord"
	"github.com/pdbethke/corralai/internal/memory"
	"github.com/pdbethke/corralai/internal/mission"
	"github.com/pdbethke/corralai/internal/principals"
	"github.com/pdbethke/corralai/internal/queue"
)

// TestLearningLoopInjectsLessons proves the loop end to end at the brain layer: a
// lesson in memory is recalled and injected into a new mission's task
// instructions by create_mission.
func TestLearningLoopInjectsLessons(t *testing.T) {
	dir := t.TempDir()
	memDir := filepath.Join(dir, "memory")
	os.MkdirAll(memDir, 0o755)
	// shared: true is required — RecallLessons now enforces vetted-only recall.
	lesson := "---\nname: lesson-scores\ndescription: \"scores dashboard: parameterize all score-API queries\"\nshared: true\nmetadata:\n  type: lesson\n---\n\nPast mission: SQL injection in the score API. Always parameterize queries.\n"
	os.WriteFile(filepath.Join(memDir, "lesson-scores.md"), []byte(lesson), 0o644)

	mem, err := memory.Open(filepath.Join(dir, "m.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer mem.Close()
	if _, err := mem.Build([]string{memDir}); err != nil {
		t.Fatal(err)
	}
	cstore, _ := coord.Open(filepath.Join(dir, "c.sqlite3"))
	defer cstore.Close()
	q, _ := queue.Open(filepath.Join(dir, "q.duckdb.sqlite3"))
	defer q.Close()
	mstore, _ := mission.Open(filepath.Join(dir, "mi.sqlite3"))
	defer mstore.Close()

	// Principals must be non-nil: the fail-closed guard refuses to inject
	// lessons when no real role authority is present (dev permissiveness is
	// not sufficient to trust "shared" entries).
	pstore, err := principals.Open(filepath.Join(dir, "p.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer pstore.Close()

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() {
		_ = NewServer(cstore, mem, Options{Missions: mstore, Queue: q, Principals: pstore}).Run(ctx, serverT)
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sess.Close()

	var mv mission.MissionView
	callTask(t, sess, "create_mission", map[string]any{"directive": "build a scores dashboard"}, &mv)
	if mv.ID == 0 {
		t.Fatal("no mission id")
	}

	tasks, err := q.List(mv.ID)
	if err != nil || len(tasks) == 0 {
		t.Fatalf("no tasks enqueued: %v", err)
	}
	// Lessons are now injected inside an UNTRUSTED fence (not raw-prepended).
	var injected bool
	for _, tk := range tasks {
		if strings.Contains(tk.Instruction, "UNTRUSTED") &&
			strings.Contains(tk.Instruction, "parameterize all score-API queries") {
			injected = true
			break
		}
	}
	if !injected {
		t.Fatalf("the recalled lesson was not injected into the mission's task instructions; sample: %q", tasks[0].Instruction)
	}
}

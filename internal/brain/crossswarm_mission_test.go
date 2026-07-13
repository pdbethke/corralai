// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/attest"
	"github.com/pdbethke/corralai/internal/coord"
	"github.com/pdbethke/corralai/internal/fleet"
	"github.com/pdbethke/corralai/internal/mission"
	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/repo"
)

// initGitRepo makes dir a committed git repo so repo.Engine.Clone/Checkout succeed.
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "-C", dir, "init", "-b", "main"},
		{"git", "-C", dir, "config", "user.email", "test@example.com"},
		{"git", "-C", dir, "config", "user.name", "Test"},
		{"git", "-C", dir, "add", "."},
		{"git", "-C", dir, "commit", "-m", "init"},
	} {
		if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
			t.Fatalf("git setup %v: %v\n%s", args, err, out)
		}
	}
}

// newCrossSwarmMissionBrain boots a brain configured for cross-swarm publishing
// (CrossSwarm on, keypair set) with a real repo engine + queue + mission store.
// Returns the connected session and the remote coordination target path.
func newCrossSwarmMissionBrain(t *testing.T, remote, brainID string, kp attest.KeyPair) *mcp.ClientSession {
	t.Helper()
	root := t.TempDir()

	cstore, err := coord.Open(filepath.Join(root, "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cstore.Close() })

	q, err := queue.Open(filepath.Join(root, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.Close() })

	mstore, err := mission.Open(filepath.Join(root, "m.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mstore.Close() })

	ws := filepath.Join(root, "ws")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}

	opts := Options{
		Queue:         q,
		Missions:      mstore,
		Repo:          repo.New("", ""),
		Workspace:     ws,
		CrossSwarm:    true,
		CrossSwarmKey: kp,
		FleetTarget:   remote,
		FleetBrainID:  brainID,
	}
	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() { _ = NewServer(cstore, nil, opts).Run(ctx, serverT) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "xswarm-mission-test", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

// TestCreateMission_PublishesCrossSwarmClaim verifies that creating a repo-work
// mission with cross-swarm coordination enabled PUBLISHES a signed claim on the
// repo that a DIFFERENT brain then observes via fleet.ActiveClaims (the same read
// fleet_claims wraps).
func TestCreateMission_PublishesCrossSwarmClaim(t *testing.T) {
	remote := newFleetClaimsRemote(t)
	kpA, pubA := mkFleetBrain(t)

	// brainA must be registered so its published claim verifies (ActiveClaims JOINs
	// fleet_brains for the pubkey). cmd/corral does this at startup; the test does it here.
	mustRegisterPeer(t, remote, "brainA", pubA)

	srcDir := filepath.Join(t.TempDir(), "src")
	initGitRepo(t, srcDir)

	sess := newCrossSwarmMissionBrain(t, remote, "brainA", kpA)

	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "create_mission",
		Arguments: map[string]any{"directive": "add a feature", "repo": srcDir, "plan": []map[string]any{{"name": "build", "role": "builder", "instruction": "add a feature"}}},
	})
	if err != nil {
		t.Fatalf("create_mission: %v", err)
	}
	if res.IsError {
		t.Fatalf("create_mission errored: %s", contentText(res))
	}
	// Structured MissionView must still be present (publishing doesn't clobber it).
	var mv mission.MissionView
	b, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(b, &mv); err != nil {
		t.Fatalf("decode MissionView: %v", err)
	}
	if mv.ID == 0 {
		t.Fatal("expected a valid mission ID")
	}

	// A DIFFERENT brain (brainB) must now observe brainA's published claim on srcDir.
	claims, err := fleet.ActiveClaims(remote, srcDir, "brainB", time.Now())
	if err != nil {
		t.Fatalf("ActiveClaims: %v", err)
	}
	if len(claims) != 1 {
		t.Fatalf("expected brainA's published claim to be observable by brainB, got %d claims", len(claims))
	}
	if claims[0].BrainID != "brainA" || claims[0].Subject != srcDir {
		t.Fatalf("unexpected claim: brain=%s subject=%s", claims[0].BrainID, claims[0].Subject)
	}
}

// TestCreateMission_AdvisorySurfacesPeerClaimWithoutBlocking verifies that when a
// peer brain already holds a verified claim on the target repo, create_mission
// SURFACES it as an advisory note in the response WITHOUT blocking (mission still
// created; not a tool error).
func TestCreateMission_AdvisorySurfacesPeerClaimWithoutBlocking(t *testing.T) {
	remote := newFleetClaimsRemote(t)
	kpA, pubA := mkFleetBrain(t) // this brain
	kpB, pubB := mkFleetBrain(t) // the peer that already claimed the repo

	mustRegisterPeer(t, remote, "brainA", pubA)
	mustRegisterPeer(t, remote, "brainB", pubB)

	srcDir := filepath.Join(t.TempDir(), "src")
	initGitRepo(t, srcDir)

	// Peer brainB publishes a live claim on srcDir BEFORE brainA's mission.
	if err := fleet.PublishIntent(kpB, remote, "brainB", "claim", srcDir, time.Hour, time.Now()); err != nil {
		t.Fatalf("peer PublishIntent: %v", err)
	}

	sess := newCrossSwarmMissionBrain(t, remote, "brainA", kpA)

	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "create_mission",
		Arguments: map[string]any{"directive": "add a feature", "repo": srcDir, "plan": []map[string]any{{"name": "build", "role": "builder", "instruction": "add a feature"}}},
	})
	if err != nil {
		t.Fatalf("create_mission: %v", err)
	}
	// Advisory must NOT block: the mission is still created (not a tool error).
	if res.IsError {
		t.Fatalf("advisory peer claim must NOT block mission creation; got error: %s", contentText(res))
	}
	// The mission must still be valid in StructuredContent.
	var mv mission.MissionView
	b, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(b, &mv); err != nil {
		t.Fatalf("decode MissionView: %v", err)
	}
	if mv.ID == 0 {
		t.Fatal("expected a valid mission ID even with a peer claim present")
	}
	// The advisory note must be surfaced in the response content, naming the peer.
	note := contentText(res)
	if !strings.Contains(note, "brainB") {
		t.Fatalf("expected advisory note naming peer brainB, got content: %q", note)
	}
	if !strings.Contains(strings.ToLower(note), "advisory") {
		t.Errorf("advisory note should signal it is advisory, got: %q", note)
	}
}

// TestCreateMission_NoCrossSwarmWhenDisabled verifies that with CrossSwarm off,
// create_mission publishes nothing (no claim appears in the coordination plane).
func TestCreateMission_NoCrossSwarmWhenDisabled(t *testing.T) {
	remote := newFleetClaimsRemote(t)
	// Register a brain so the tables exist and any (absent) claim would be joinable.
	_, pubA := mkFleetBrain(t)
	mustRegisterPeer(t, remote, "brainA", pubA)

	srcDir := filepath.Join(t.TempDir(), "src")
	initGitRepo(t, srcDir)

	// Boot a brain WITHOUT cross-swarm (Repo engine on, but CrossSwarm false).
	root := t.TempDir()
	cstore, err := coord.Open(filepath.Join(root, "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cstore.Close() })
	q, err := queue.Open(filepath.Join(root, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.Close() })
	mstore, err := mission.Open(filepath.Join(root, "m.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mstore.Close() })
	ws := filepath.Join(root, "ws")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() {
		_ = NewServer(cstore, nil, Options{
			Queue: q, Missions: mstore, Repo: repo.New("", ""), Workspace: ws,
			// CrossSwarm intentionally false.
		}).Run(ctx, serverT)
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "no-xswarm-mission", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { sess.Close() })

	res, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name:      "create_mission",
		Arguments: map[string]any{"directive": "add a feature", "repo": srcDir, "plan": []map[string]any{{"name": "build", "role": "builder", "instruction": "add a feature"}}},
	})
	if err != nil {
		t.Fatalf("create_mission: %v", err)
	}
	if res.IsError {
		t.Fatalf("create_mission errored: %s", contentText(res))
	}

	// No claim must have been published on srcDir.
	claims, err := fleet.ActiveClaims(remote, srcDir, "brainB", time.Now())
	if err != nil {
		t.Fatalf("ActiveClaims: %v", err)
	}
	if len(claims) != 0 {
		t.Fatalf("CrossSwarm disabled: expected 0 published claims, got %d", len(claims))
	}
}

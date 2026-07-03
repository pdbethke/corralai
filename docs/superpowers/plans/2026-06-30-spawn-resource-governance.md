# Spawn Resource Governance Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make spawn-and-launch bounded by construction — a brain-side per-principal/depth/breadth budget and a pluggable host-side admission controller in a reference launcher — so corralai cannot be used as a DoS vector.

**Architecture:** Two enforcers across the trust boundary. The brain refuses over-budget `spawn_subagent` calls before registering a child or minting a token (per-principal live count, spawn depth, children-per-parent). A new `internal/admission` package gates the reference launcher in `corral-agent`: a child process starts only after a non-blocking `Acquire` grants a lease (concurrency semaphore + load gate). The brain never executes compute.

**Tech Stack:** Go 1.26; `modernc.org/sqlite` (coord store, pure-Go); existing `internal/telemetry` (DuckDB) for `spawn_refused` events; `internal/sandbox` jail unchanged; MCP via `github.com/modelcontextprotocol/go-sdk/mcp`.

## Global Constraints

- `corral-agent` MUST stay CGO-free — verify each task with `CGO_ENABLED=0 go build ./cmd/corral-agent`. The `internal/admission` package MUST be pure Go (no cgo).
- Budget/limit config values of `0` mean "use the default", NEVER "unlimited" — caps can never be accidentally disabled.
- The brain NEVER execs agent compute. Process launching lives ONLY in `cmd/corral-agent`.
- Spawning in the reference is deterministic/operator-triggered (`AGENT_SPAWN_CHILDREN`), NEVER an LLM-callable tool.
- `Acquire` is NON-BLOCKING — it returns a refusal error rather than waiting, so callers back off instead of piling up goroutines.
- Follow existing patterns: pluggable interface mirrors `sandbox.Isolator` (`internal/sandbox/sandbox.go`); telemetry recording mirrors `rec(...)` in `internal/brain/telemetry.go`.
- Defaults: `MaxAgentsPerPrincipal=64`, `MaxSpawnDepth=4`, `MaxChildrenPerParent=8`, `MaxConcurrent=2×NumCPU`, `LoadFactor=2.0`.
- "Live" agent = `last_active_ts >= now − coord.PresenceWindow` (300s).

---

## File Structure

- `internal/admission/admission.go` (create) — `Controller`/`Lease` interfaces + `Local` reference impl + `FromEnv`.
- `internal/admission/admission_test.go` (create) — semaphore, load gate, release, swappable interface.
- `internal/coord/store.go` (modify) — `principal` column migration; `RecordPrincipal`; `CountLiveByPrincipal`.
- `internal/coord/store_test.go` (modify) — principal recording + live-count tests.
- `internal/brain/identity.go` (modify) — `SpawnBudget` struct + field on `Options`.
- `internal/brain/subagents.go` (modify) — budget enforcement in `spawn_subagent` + `spawn_refused` telemetry; record principal.
- `internal/brain/server.go` (modify) — record principal on `bootstrap`.
- `internal/brain/subagents_test.go` (modify — file EXISTS with `TestSubagentsOverMCP`; append new tests, keep the existing one passing) — over/under each cap.
- `internal/brain/spawn_integration_test.go` (create) — refusal recorded in telemetry end-to-end.
- `cmd/corral/main.go` (modify) — read budget env → `Options.SpawnBudget`.
- `cmd/corral-agent/launcher.go` (create) — admission-gated `launchChild` + `spawnConfiguredChildren`.
- `cmd/corral-agent/launcher_test.go` (create) — Acquire→launch→Release; refused→no launch; env carries token/name/role.
- `cmd/corral-agent/main.go` (modify) — construct admission controller; call `spawnConfiguredChildren` after registration.
- `deploy/demo/docker-compose.yml` (modify) — admission env defaults + optional `AGENT_SPAWN_CHILDREN`.

---

## Task 1: `internal/admission` package (Controller + Local reference impl)

**Files:**
- Create: `internal/admission/admission.go`
- Test: `internal/admission/admission_test.go`

**Interfaces:**
- Consumes: nothing (leaf package, stdlib only).
- Produces:
  - `type Lease interface { Release() }`
  - `type Controller interface { Acquire(role string) (Lease, error); Name() string }`
  - `func NewLocal(maxConcurrent int, loadFactor float64, load func() float64) *Local`
  - `func FromEnv() *Local` — reads `CORRAL_MAX_CONCURRENT_CHILDREN` (default `2*runtime.NumCPU()`), `CORRAL_LOAD_FACTOR` (default `2.0`); `load` defaults to `readLoadAvg`.
  - `Local.Acquire(role string) (Lease, error)` / `Local.Name() string` (returns `"local"`).

- [ ] **Step 1: Write the failing test**

```go
// internal/admission/admission_test.go
package admission

import "testing"

func TestLocalSemaphoreRefusesAtCap(t *testing.T) {
	c := NewLocal(2, 100, func() float64 { return 0 }) // high loadFactor => load gate never trips
	l1, err := c.Acquire("builder")
	if err != nil {
		t.Fatalf("acquire 1: %v", err)
	}
	if _, err := c.Acquire("builder"); err != nil {
		t.Fatalf("acquire 2: %v", err)
	}
	if _, err := c.Acquire("builder"); err == nil {
		t.Fatal("acquire 3 should be refused at cap 2")
	}
	l1.Release()
	if _, err := c.Acquire("builder"); err != nil {
		t.Fatalf("acquire after release should succeed: %v", err)
	}
}

func TestLocalLoadGateRefuses(t *testing.T) {
	// loadFactor 2.0, NumCPU≥1 => threshold ≥2.0; injected load 999 must refuse.
	c := NewLocal(100, 2.0, func() float64 { return 999 })
	if _, err := c.Acquire("tester"); err == nil {
		t.Fatal("acquire should be refused when load exceeds threshold")
	}
}

func TestLocalReleaseIsIdempotentlySafe(t *testing.T) {
	c := NewLocal(1, 100, func() float64 { return 0 })
	l, err := c.Acquire("x")
	if err != nil {
		t.Fatal(err)
	}
	l.Release()
	l.Release() // double release must not free an extra slot
	if _, err := c.Acquire("x"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Acquire("x"); err == nil {
		t.Fatal("double-release must not have freed two slots")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/admission/`
Expected: FAIL — `undefined: NewLocal`.

- [ ] **Step 3: Write minimal implementation**

```go
// internal/admission/admission.go

// Package admission is host-side spawn governance: before the reference launcher
// starts a child agent process, it must Acquire a lease. The Local impl caps both
// concurrency (a semaphore) and host load (a 1-min loadavg gate), refusing rather
// than blocking so a coordinator can never conscript a host beyond its capacity.
// Controller is an interface (like sandbox.Isolator) so cgroups/container impls can
// be dropped in without touching the launcher.
package admission

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// Lease is a held admission slot; Release returns it. Release is safe to call once.
type Lease interface{ Release() }

// Controller grants (or refuses) permission to launch a child on this host.
type Controller interface {
	// Acquire is NON-BLOCKING: it returns a lease, or an error explaining why the
	// host won't run another child right now (at capacity, or load too high).
	Acquire(role string) (Lease, error)
	Name() string
}

// Local is the reference Controller: a concurrency semaphore + a host-load gate.
type Local struct {
	mu         sync.Mutex
	inUse      int
	max        int
	loadFactor float64
	load       func() float64
}

// NewLocal builds a Local. maxConcurrent<=0 falls back to 2*NumCPU; loadFactor<=0
// falls back to 2.0. load returns the current 1-minute load average.
func NewLocal(maxConcurrent int, loadFactor float64, load func() float64) *Local {
	if maxConcurrent <= 0 {
		maxConcurrent = 2 * runtime.NumCPU()
	}
	if loadFactor <= 0 {
		loadFactor = 2.0
	}
	if load == nil {
		load = readLoadAvg
	}
	return &Local{max: maxConcurrent, loadFactor: loadFactor, load: load}
}

// FromEnv builds a Local from CORRAL_MAX_CONCURRENT_CHILDREN and CORRAL_LOAD_FACTOR.
func FromEnv() *Local {
	max := 0
	if v, err := strconv.Atoi(os.Getenv("CORRAL_MAX_CONCURRENT_CHILDREN")); err == nil {
		max = v
	}
	lf := 0.0
	if v, err := strconv.ParseFloat(os.Getenv("CORRAL_LOAD_FACTOR"), 64); err == nil {
		lf = v
	}
	return NewLocal(max, lf, nil)
}

func (l *Local) Name() string { return "local" }

type localLease struct {
	l    *Local
	once sync.Once
}

func (ll *localLease) Release() {
	ll.once.Do(func() {
		ll.l.mu.Lock()
		if ll.l.inUse > 0 {
			ll.l.inUse--
		}
		ll.l.mu.Unlock()
	})
}

// Acquire grants a slot unless the host is at the concurrency cap or its load
// exceeds loadFactor*NumCPU.
func (l *Local) Acquire(role string) (Lease, error) {
	if threshold := l.loadFactor * float64(runtime.NumCPU()); l.load() > threshold {
		return nil, fmt.Errorf("admission refused: host load %.2f exceeds %.2f", l.load(), threshold)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inUse >= l.max {
		return nil, fmt.Errorf("admission refused: host at capacity (%d concurrent children)", l.max)
	}
	l.inUse++
	return &localLease{l: l}, nil
}

// readLoadAvg returns the 1-minute load average from /proc/loadavg, or 0 if it
// can't be read (non-Linux) — the load gate is then a no-op and the semaphore alone
// governs. Pure Go: no cgo, no build tags.
func readLoadAvg() float64 {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return 0
	}
	return v
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/admission/ && CGO_ENABLED=0 go build ./internal/admission/`
Expected: PASS; build succeeds (pure Go).

- [ ] **Step 5: Commit**

```bash
git add internal/admission/
git commit -m "feat(admission): pluggable host admission controller (concurrency + load gate)"
```

---

## Task 2: coord — owning principal + live-count query

**Files:**
- Modify: `internal/coord/store.go` (schema block near line 23; add methods after `Subagents`)
- Test: `internal/coord/store_test.go`

**Interfaces:**
- Consumes: existing `coord.Store`, `now()`, `PresenceWindow` (300.0).
- Produces:
  - `func (s *Store) RecordPrincipal(name, principal string) error` — sets the owning principal on an agent (only when currently empty, so it's not clobbered by re-registration).
  - `func (s *Store) CountLiveByPrincipal(principal string) (int, error)` — count of agents with that principal whose `last_active_ts >= now − PresenceWindow`.

- [ ] **Step 1: Write the failing test**

```go
// internal/coord/store_test.go — add to the existing file
func TestPrincipalCountLive(t *testing.T) {
	s := open(t) // existing test helper in internal/coord/store_test.go
	// two agents owned by "alice", one by "bob"
	s.Register("a1", "", "", "", "", "")
	s.Register("a2", "", "", "", "", "")
	s.Register("b1", "", "", "", "", "")
	if err := s.RecordPrincipal("a1", "alice"); err != nil {
		t.Fatal(err)
	}
	s.RecordPrincipal("a2", "alice")
	s.RecordPrincipal("b1", "bob")
	n, err := s.CountLiveByPrincipal("alice")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("alice live count = %d, want 2", n)
	}
	if n, _ := s.CountLiveByPrincipal("bob"); n != 1 {
		t.Fatalf("bob live count = %d, want 1", n)
	}
}
```

> The helper `open(t)` already exists in `internal/coord/store_test.go` (`func open(t *testing.T) *Store`) — use it; do not add a duplicate.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/coord/ -run TestPrincipalCountLive`
Expected: FAIL — `s.RecordPrincipal undefined`.

- [ ] **Step 3: Write minimal implementation**

In the schema string (the `CREATE TABLE IF NOT EXISTS agents (...)` block), the column is added by migration rather than editing the CREATE (existing DBs must upgrade). Immediately after the `Open`/schema-exec succeeds, add an idempotent migration. Find where the agents table is created and add, after it:

```go
// principal: the authenticated owner of this agent (''=dev/unattributed). Added by
// migration so existing stores upgrade. Used by the brain's per-principal spawn budget.
s.db.Exec(`ALTER TABLE agents ADD COLUMN principal TEXT NOT NULL DEFAULT ''`)
```

> The `ALTER TABLE ... ADD COLUMN` errors harmlessly if the column already exists; ignore its error (same idempotent-migration pattern as the tasks `verify` column in `internal/queue/store.go`). If this `Open` function returns early on exec errors, wrap this one to ignore "duplicate column".

Then add the two methods after `Subagents`:

```go
// RecordPrincipal sets the owning principal on an agent, but only when it is not yet
// set — so a heartbeat / re-register never clobbers established ownership.
func (s *Store) RecordPrincipal(name, principal string) error {
	if principal == "" {
		return nil
	}
	_, err := s.db.Exec(`UPDATE agents SET principal=? WHERE name=? AND principal=''`, principal, name)
	return err
}

// CountLiveByPrincipal returns how many of a principal's agents are currently live
// (heartbeat within PresenceWindow). The brain's MaxAgentsPerPrincipal budget reads
// this; a crashed-but-not-despawned agent ages out automatically.
func (s *Store) CountLiveByPrincipal(principal string) (int, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM agents WHERE principal=? AND last_active_ts >= ?`,
		principal, now()-PresenceWindow,
	).Scan(&n)
	return n, err
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/coord/ -run TestPrincipalCountLive`
Expected: PASS.

- [ ] **Step 5: Run the full coord suite (the migration must not break existing tests)**

Run: `go test ./internal/coord/`
Expected: PASS (ok).

- [ ] **Step 6: Commit**

```bash
git add internal/coord/store.go internal/coord/store_test.go
git commit -m "feat(coord): record owning principal + CountLiveByPrincipal for spawn budget"
```

---

## Task 3: brain spawn budget — enforce in spawn_subagent

**Files:**
- Modify: `internal/brain/identity.go` (the `Options` struct + a new `SpawnBudget` type)
- Modify: `internal/brain/subagents.go` (`spawn_subagent` handler)
- Modify: `internal/brain/server.go` (record principal on `bootstrap`)
- Modify: `cmd/corral/main.go` (env → `Options.SpawnBudget`)
- Test: `internal/brain/subagents_test.go` (MODIFY — already exists with `TestSubagentsOverMCP`; the budget defaults 64/4/8 and the dev principal `""` skip mean that test keeps passing. Append the new tests; reuse its inline `call`/session pattern.)

**Interfaces:**
- Consumes: `coord.CountLiveByPrincipal`, `coord.RecordPrincipal`, `coord.Subagents` (Task 2); `actor(req)` → `(principal, tenant string)`; `identity(req, fallback)`; `rec(tel, missionID, kind, actor, subject, detail)` from `internal/brain/telemetry.go`; `opts.Telemetry`.
- Produces:
  - `type SpawnBudget struct { MaxAgentsPerPrincipal, MaxSpawnDepth, MaxChildrenPerParent int }`
  - `Options.SpawnBudget SpawnBudget`
  - `func (b SpawnBudget) withDefaults() SpawnBudget` — replaces any `0` with the default (64/4/8).

- [ ] **Step 1: Write the failing test**

```go
// internal/brain/subagents_test.go
package brain

import (
	"strings"
	"testing"
)

func TestSpawnBudgetDefaults(t *testing.T) {
	got := SpawnBudget{}.withDefaults()
	if got.MaxAgentsPerPrincipal != 64 || got.MaxSpawnDepth != 4 || got.MaxChildrenPerParent != 8 {
		t.Fatalf("defaults wrong: %+v", got)
	}
	// 0 means default, never unlimited:
	got = SpawnBudget{MaxSpawnDepth: 2}.withDefaults()
	if got.MaxSpawnDepth != 2 || got.MaxAgentsPerPrincipal != 64 {
		t.Fatalf("partial override wrong: %+v", got)
	}
}

func TestSpawnDepthOf(t *testing.T) {
	cases := map[string]int{"Bob": 1, "Bob/t1": 2, "a/b/c/d": 4}
	for name, want := range cases {
		if d := spawnDepthOf(name); d != want {
			t.Fatalf("depth(%q)=%d want %d", name, d, want)
		}
	}
}

func TestBudgetDecisionRefusals(t *testing.T) {
	b := SpawnBudget{MaxAgentsPerPrincipal: 2, MaxSpawnDepth: 3, MaxChildrenPerParent: 2}.withDefaults()
	// over depth (full name has 4 segments, cap 3)
	if err := budgetDecision(b, "a/b/c", "a", 0, 0); err == nil || !strings.Contains(err.Error(), "depth") {
		t.Fatalf("want depth refusal, got %v", err)
	}
	// over breadth (parent already has 2 children, cap 2)
	if err := budgetDecision(b, "a", "root", 2, 0); err == nil || !strings.Contains(err.Error(), "children") {
		t.Fatalf("want breadth refusal, got %v", err)
	}
	// over principal total (2 live, cap 2)
	if err := budgetDecision(b, "a", "root", 0, 2); err == nil || !strings.Contains(err.Error(), "principal") {
		t.Fatalf("want principal refusal, got %v", err)
	}
	// under all caps
	if err := budgetDecision(b, "a/b", "a", 1, 1); err != nil {
		t.Fatalf("under caps should pass, got %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/brain/ -run 'TestSpawnBudget|TestSpawnDepthOf|TestBudgetDecision'`
Expected: FAIL — `undefined: SpawnBudget`.

- [ ] **Step 3a: Add the SpawnBudget type + Options field**

In `internal/brain/identity.go`, add to the `Options` struct (next to `ExecRing`/`HostBook`):

```go
	// SpawnBudget bounds spawn_subagent: standing live agents per principal, spawn
	// tree depth, and children per parent. Zero fields take the defaults (64/4/8);
	// zero NEVER means unlimited.
	SpawnBudget SpawnBudget
```

And define the type (in `identity.go`, after the `Options` struct):

```go
// SpawnBudget is the brain-side request-side DoS bound for spawning.
type SpawnBudget struct {
	MaxAgentsPerPrincipal int
	MaxSpawnDepth         int
	MaxChildrenPerParent  int
}

// withDefaults replaces any unset (0) field with its default. Zero is "default",
// never "unlimited".
func (b SpawnBudget) withDefaults() SpawnBudget {
	if b.MaxAgentsPerPrincipal == 0 {
		b.MaxAgentsPerPrincipal = 64
	}
	if b.MaxSpawnDepth == 0 {
		b.MaxSpawnDepth = 4
	}
	if b.MaxChildrenPerParent == 0 {
		b.MaxChildrenPerParent = 8
	}
	return b
}
```

- [ ] **Step 3b: Add the pure decision helpers in subagents.go**

In `internal/brain/subagents.go`, add (above `registerSubagents`):

```go
// spawnDepthOf is the number of segments in a hierarchical agent name
// ("a/b/c" => 3). The spawn budget caps this to stop recursive fork-bombs.
func spawnDepthOf(fullName string) int {
	if fullName == "" {
		return 0
	}
	return strings.Count(fullName, "/") + 1
}

// budgetDecision returns a refusal error if spawning a child named fullName under
// parent would breach the budget, given the parent's current child count and the
// principal's current live-agent count. nil => allowed. Pure (no I/O) for testing.
func budgetDecision(b SpawnBudget, fullName, parent string, parentChildren, principalLive int) error {
	if d := spawnDepthOf(fullName); d > b.MaxSpawnDepth {
		return fmt.Errorf("spawn refused: depth %d exceeds max spawn depth %d", d, b.MaxSpawnDepth)
	}
	if parentChildren >= b.MaxChildrenPerParent {
		return fmt.Errorf("spawn refused: parent %q already has %d children (max %d)", parent, parentChildren, b.MaxChildrenPerParent)
	}
	if principalLive >= b.MaxAgentsPerPrincipal {
		return fmt.Errorf("spawn refused: principal at %d live agents (max %d)", principalLive, b.MaxAgentsPerPrincipal)
	}
	return nil
}
```

Add `"strings"` to the `subagents.go` imports (it currently imports `context`, `fmt`, `time`, the mcp pkg, and coord).

- [ ] **Step 4: Run the unit tests (helpers) to verify they pass**

Run: `go test ./internal/brain/ -run 'TestSpawnBudget|TestSpawnDepthOf|TestBudgetDecision'`
Expected: PASS.

- [ ] **Step 5: Wire the decision into the spawn_subagent handler**

In `internal/brain/subagents.go`, in the `spawn_subagent` handler, AFTER computing `parent` and the would-be `full` name (`full := parent + "/" + in.Name`) but BEFORE `store.Spawn`, enforce the budget. The handler currently calls `store.Spawn` first; restructure so the name is computed, the budget is checked, then `store.Spawn` runs. Replace the body up to the Spawn call with:

```go
			parent := identity(req, in.Parent) // you, or a parent within your namespace
			if parent == "" {
				parent = "agent" // dev (no auth)
			}
			full := parent + "/" + in.Name
			principal, _ := actor(req)
			budget := opts.SpawnBudget.withDefaults()

			// Gather the two counts the decision needs (read-only).
			kids, _ := store.Subagents(parent)
			liveForPrincipal := 0
			if principal != "" {
				liveForPrincipal, _ = store.CountLiveByPrincipal(principal)
			}
			if err := budgetDecision(budget, full, parent, len(kids), liveForPrincipal); err != nil {
				rec(opts.Telemetry, 0, "spawn_refused", actorOr(principal, parent), full,
					map[string]any{"reason": err.Error()})
				return nil, spawnSubagentOut{}, err
			}

			if _, err := store.Spawn(parent, in.Name, in.Role, in.Program, in.Model, in.Task); err != nil {
				return nil, spawnSubagentOut{}, err
			}
			_ = store.RecordPrincipal(full, principal)
```

Then the existing `out := spawnSubagentOut{...}` and out-of-process token block follow unchanged (note: `store.Spawn` returns the full name; we already have `full`, so either keep the returned value or use `full` — use `full` for the output `Name`).

Add this tiny helper near `budgetDecision` (avoids logging an empty actor in dev):

```go
func actorOr(principal, fallback string) string {
	if principal != "" {
		return principal
	}
	return fallback
}
```

- [ ] **Step 6: Record principal on bootstrap (so top-level agents count toward their owner)**

In `internal/brain/server.go`, find the `bootstrap` tool handler (it calls into coord registration). After the agent is registered, record its principal. Locate the bootstrap handler and add, after the registration call succeeds:

```go
			if pr, _ := actor(req); pr != "" {
				_ = store.RecordPrincipal(identity(req, in.Name), pr)
			}
```

> If `bootstrap` registers via a coord method other than the agent name in `in.Name`, use the same name the registration used. The goal: the row created by bootstrap gets its `principal` set to the authenticated owner.

- [ ] **Step 7: Write the handler-level test (over-breadth spawn is refused by the tool)**

Append to `internal/brain/subagents_test.go` (it already imports `context`, `encoding/json`, `path/filepath`, `testing`, `time`, `mcp`, `coord`). This test asserts the SECOND spawn under a parent is refused when `MaxChildrenPerParent=1` — proving the budget is wired into the live tool (the unit test only covered the pure decision):

```go
func TestSpawnSubagentRefusedOverBreadth(t *testing.T) {
	store, err := coord.Open(filepath.Join(t.TempDir(), "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	opts := Options{SpawnBudget: SpawnBudget{MaxChildrenPerParent: 1}}
	go func() { _ = NewServer(store, nil, opts).Run(ctx, serverT) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "smoke", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sess.Close()

	spawn := func(name string) *mcp.CallToolResult {
		res, err := sess.CallTool(ctx, &mcp.CallToolParams{
			Name:      "spawn_subagent",
			Arguments: map[string]any{"name": name, "role": "tester"},
		})
		if err != nil {
			t.Fatalf("spawn %s transport error: %v", name, err)
		}
		return res
	}
	if res := spawn("t1"); res.IsError {
		t.Fatalf("first spawn should succeed: %+v", res.Content)
	}
	if res := spawn("t2"); !res.IsError {
		t.Fatal("second spawn should be refused (breadth cap 1)")
	}
}
```

- [ ] **Step 8: Run the brain suite**

Run: `go test ./internal/brain/`
Expected: PASS.

- [ ] **Step 9: Wire env → budget in cmd/corral**

In `cmd/corral/main.go`, in the `brain.Options{...}` literal (near `ExecRing`/`ActivityRing`/`HostBook`), add:

```go
		SpawnBudget: brain.SpawnBudget{
			MaxAgentsPerPrincipal: envInt("CORRALAI_MAX_AGENTS_PER_PRINCIPAL", 0),
			MaxSpawnDepth:         envInt("CORRALAI_MAX_SPAWN_DEPTH", 0),
			MaxChildrenPerParent:  envInt("CORRALAI_MAX_CHILDREN_PER_PARENT", 0),
		},
```

(`envInt` already exists in `cmd/corral/main.go`; `0` → `withDefaults()` applies 64/4/8.)

- [ ] **Step 10: Build + commit**

Run: `go build ./... && go test ./internal/brain/ ./internal/coord/`
Expected: build OK; tests PASS.

```bash
git add internal/brain/identity.go internal/brain/subagents.go internal/brain/server.go internal/brain/subagents_test.go cmd/corral/main.go
git commit -m "feat(brain): per-principal/depth/breadth spawn budget with spawn_refused telemetry"
```

---

## Task 4: reference launcher in corral-agent (admission-gated local child)

**Files:**
- Create: `cmd/corral-agent/launcher.go`
- Create: `cmd/corral-agent/launcher_test.go`
- Modify: `cmd/corral-agent/main.go` (construct controller; call `spawnConfiguredChildren` after registration)
- Modify: `deploy/demo/docker-compose.yml` (env defaults + optional `AGENT_SPAWN_CHILDREN`)

**Interfaces:**
- Consumes: `admission.Controller`/`admission.FromEnv` (Task 1); the agent's `brain(tool, args)` closure (calls `spawn_subagent`); `env(k, d string)` helper in `main.go`.
- Produces:
  - `type childSpec struct { Role string; N int }`
  - `func parseSpawnSpec(s string) []childSpec` — parses `AGENT_SPAWN_CHILDREN` (`"tester:2,builder:1"`).
  - `func spawnConfiguredChildren(ctrl admission.Controller, brain func(string, map[string]any) string, parent, brainURL string, launch func(env []string) error)` — for each requested child: call `spawn_subagent(out_of_process=true)`, then `ctrl.Acquire(role)`; on grant call `launch(childEnv)`, on refusal log + skip; release on launch error.
  - `func launchProcess(env []string) error` — the real launcher: `exec.Command(os.Args[0])` with the child env, `Start()`, and a goroutine that waits then releases (release is wired by the caller via a closure — see below).

- [ ] **Step 1: Write the failing test**

```go
// cmd/corral-agent/launcher_test.go
package main

import (
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/admission"
)

func TestParseSpawnSpec(t *testing.T) {
	got := parseSpawnSpec("tester:2, builder:1 , junk, perf:0")
	// tester:2, builder:1 ; "junk" (no count) and "perf:0" are dropped.
	if len(got) != 2 || got[0].Role != "tester" || got[0].N != 2 || got[1].Role != "builder" || got[1].N != 1 {
		t.Fatalf("parsed wrong: %+v", got)
	}
}

func TestSpawnConfiguredChildrenRespectsAdmission(t *testing.T) {
	// Controller that allows exactly 1, then refuses.
	ctrl := admission.NewLocal(1, 100, func() float64 { return 0 })
	var launched int
	launch := func(env []string) error {
		// must carry token + name + role
		joined := strings.Join(env, " ")
		if !strings.Contains(joined, "CORRAL_TOKEN=") || !strings.Contains(joined, "AGENT_NAME=") || !strings.Contains(joined, "AGENT_ROLE=") {
			t.Fatalf("child env missing required vars: %v", env)
		}
		launched++
		return nil
	}
	// fake brain: spawn_subagent returns a token; everything else returns "{}"
	brain := func(tool string, args map[string]any) string {
		if tool == "spawn_subagent" {
			return `{"name":"P/c","parent":"P","token":"tok-123"}`
		}
		return "{}"
	}
	// request 3 testers but the controller only admits 1 (it never releases here)
	spawnConfiguredChildrenN(ctrl, brain, "P", "http://brain", launch, []childSpec{{Role: "tester", N: 3}})
	if launched != 1 {
		t.Fatalf("admission cap=1 should launch 1 child, launched %d", launched)
	}
}
```

> NOTE: the test calls `spawnConfiguredChildrenN` (the spec-list form). `spawnConfiguredChildren` is a thin wrapper that reads `AGENT_SPAWN_CHILDREN` via `parseSpawnSpec` and delegates to `spawnConfiguredChildrenN` — split so the list form is testable without env.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/corral-agent/ -run 'TestParseSpawnSpec|TestSpawnConfiguredChildren'`
Expected: FAIL — `undefined: parseSpawnSpec`.

- [ ] **Step 3: Write the launcher**

```go
// cmd/corral-agent/launcher.go
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/pdbethke/corralai/internal/admission"
)

type childSpec struct {
	Role string
	N    int
}

// parseSpawnSpec parses AGENT_SPAWN_CHILDREN: "tester:2,builder:1". Entries without
// a positive count are ignored. Spawning is deterministic + operator-configured —
// never an LLM-callable tool.
func parseSpawnSpec(s string) []childSpec {
	var out []childSpec
	for _, part := range strings.Split(s, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), ":", 2)
		if len(kv) != 2 {
			continue
		}
		role := strings.TrimSpace(kv[0])
		n, err := strconv.Atoi(strings.TrimSpace(kv[1]))
		if role == "" || err != nil || n <= 0 {
			continue
		}
		out = append(out, childSpec{Role: role, N: n})
	}
	return out
}

// spawnConfiguredChildren reads AGENT_SPAWN_CHILDREN and launches the requested
// children, each gated by the host admission controller. No-op when unset.
func spawnConfiguredChildren(ctrl admission.Controller, brain func(string, map[string]any) string, parent, brainURL string) {
	specs := parseSpawnSpec(os.Getenv("AGENT_SPAWN_CHILDREN"))
	if len(specs) == 0 {
		return
	}
	spawnConfiguredChildrenN(ctrl, brain, parent, brainURL, launchProcess, specs)
}

// spawnConfiguredChildrenN is the testable core: for each requested child, ask the
// brain to register it + mint a token (brain enforces the budget), then Acquire a
// host slot (host enforces capacity/load); only on BOTH grants does it launch.
func spawnConfiguredChildrenN(ctrl admission.Controller, brain func(string, map[string]any) string, parent, brainURL string, launch func(env []string) error, specs []childSpec) {
	for _, spec := range specs {
		for i := 0; i < spec.N; i++ {
			name := fmt.Sprintf("%s-%d", spec.Role, i+1)
			raw := brain("spawn_subagent", map[string]any{
				"name": name, "role": spec.Role, "out_of_process": true,
			})
			var resp struct {
				Name  string `json:"name"`
				Token string `json:"token"`
				Error string `json:"error"`
			}
			_ = json.Unmarshal([]byte(raw), &resp)
			if resp.Error != "" || resp.Token == "" {
				fmt.Printf("[launcher] brain refused %s/%s: %s\n", parent, name, oneline(raw))
				continue // brain budget refusal — do not launch
			}
			lease, err := ctrl.Acquire(spec.Role)
			if err != nil {
				fmt.Printf("[launcher] %s\n", err.Error()) // host admission refusal — do not launch
				continue
			}
			childEnv := append(os.Environ(),
				"CORRAL_TOKEN="+resp.Token,
				"AGENT_NAME="+resp.Name,
				"AGENT_ROLE="+spec.Role,
				"AGENT_MODE=queue",
				"CORRAL_BRAIN="+brainURL,
				"AGENT_SPAWN_CHILDREN=", // children never recursively spawn (defense in depth)
			)
			if err := launch(childEnv); err != nil {
				fmt.Printf("[launcher] launch %s failed: %v\n", resp.Name, err)
				lease.Release()
				continue
			}
			fmt.Printf("[launcher] launched %s (role %s)\n", resp.Name, spec.Role)
			// lease is released when the child process exits (launchProcess wires the wait).
			_ = lease
		}
	}
}

// launchProcess starts a child corral-agent with the given env and returns once it
// has started (not waited). NOTE: lease release on child exit is wired in main.go's
// real path; in tests launch is a stub so no process is started.
func launchProcess(env []string) error {
	cmd := exec.Command(os.Args[0])
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Start()
}
```

> Implementer note on lease lifetime: in `spawnConfiguredChildrenN` the lease is intentionally held (not released) when launch succeeds, because the child is now running. The real release-on-exit is wired in main.go (Step 5) by passing a `launch` closure that starts the process AND spawns a goroutine to `cmd.Wait()` then `lease.Release()`. To keep the core testable and the lease accounting correct, change the signature so the launch closure receives the lease: see Step 4 refinement.

- [ ] **Step 4: Refine so the launch closure owns release-on-exit, then make the test pass**

Adjust `spawnConfiguredChildrenN` to hand the lease to the launch closure (so production can release on `Wait`, tests can ignore it):

```go
// launch now takes the lease; it is responsible for releasing it when the child
// ends. Tests pass a stub that drops the lease (cap stays consumed, which is what
// "launched and still running" means).
func spawnConfiguredChildrenN(ctrl admission.Controller, brain func(string, map[string]any) string, parent, brainURL string, launch func(env []string, lease admission.Lease) error, specs []childSpec) {
	// ...identical until the launch call...
			if err := launch(childEnv, lease); err != nil {
				fmt.Printf("[launcher] launch %s failed: %v\n", resp.Name, err)
				lease.Release()
				continue
			}
	// ...
}
```

Update the test's `launch` stub signature to `func(env []string, _ admission.Lease) error`. Update `launchProcess` to match and wire release-on-exit:

```go
func launchProcess(env []string, lease admission.Lease) error {
	cmd := exec.Command(os.Args[0])
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait(); lease.Release() }() // free the host slot when the child exits
	return nil
}
```

And `spawnConfiguredChildren` passes `launchProcess` as the closure.

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./cmd/corral-agent/ -run 'TestParseSpawnSpec|TestSpawnConfiguredChildren'`
Expected: PASS.

- [ ] **Step 6: Wire into main.go**

In `cmd/corral-agent/main.go`, after the agent registers (after the `report_host` goroutine is started, before/around entering its mode loop), construct the controller and spawn configured children:

```go
	ctrl := admission.FromEnv()
	spawnConfiguredChildren(ctrl, brain, name, env("CORRAL_BRAIN", "http://127.0.0.1:9019/mcp/"))
```

Add `"github.com/pdbethke/corralai/internal/admission"` to the imports. Use the same brain-URL env the agent already reads (match the existing `brainURL` variable if present — reuse it rather than re-reading).

- [ ] **Step 7: Build (incl. CGO-free) + commit**

Run: `go build ./... && CGO_ENABLED=0 go build ./cmd/corral-agent && go test ./cmd/corral-agent/`
Expected: build OK; CGO-free OK; tests PASS.

```bash
git add cmd/corral-agent/launcher.go cmd/corral-agent/launcher_test.go cmd/corral-agent/main.go
git commit -m "feat(agent): admission-gated reference launcher for out-of-process children"
```

- [ ] **Step 8: Demo wiring (compose env)**

In `deploy/demo/docker-compose.yml`, add to the shared `x-agent-env` anchor (so every bee inherits sane caps), after the exec env vars:

```yaml
  # Host admission control for the reference launcher (caps concurrent children +
  # refuses under load). AGENT_SPAWN_CHILDREN is OFF by default; set e.g.
  # "tester:2" on ONE service to demo bounded spawning.
  CORRAL_MAX_CONCURRENT_CHILDREN: ${CORRAL_MAX_CONCURRENT_CHILDREN:-4}
  CORRAL_LOAD_FACTOR: ${CORRAL_LOAD_FACTOR:-2.0}
  AGENT_SPAWN_CHILDREN: ${AGENT_SPAWN_CHILDREN:-}
```

And document the brain-side budget env on the `brain` service `environment:` block (defaults apply if unset, so this is documentation + override surface):

```yaml
      CORRALAI_MAX_AGENTS_PER_PRINCIPAL: ${CORRALAI_MAX_AGENTS_PER_PRINCIPAL:-64}
      CORRALAI_MAX_SPAWN_DEPTH: ${CORRALAI_MAX_SPAWN_DEPTH:-4}
      CORRALAI_MAX_CHILDREN_PER_PARENT: ${CORRALAI_MAX_CHILDREN_PER_PARENT:-8}
```

- [ ] **Step 9: Commit the demo wiring**

```bash
git add deploy/demo/docker-compose.yml
git commit -m "feat(demo): admission + spawn-budget env on the demo stack"
```

---

## Task 5: integration test — a budget breach is refused AND recorded

**Files:**
- Create: `internal/brain/spawn_integration_test.go`

**Interfaces:**
- Consumes: `coord.Open`, `telemetry.Open`, `telemetry.AgentTimeline(actor, limit)`, the MCP in-memory transport, `SpawnBudget`. (`rec(...)` is synchronous — it calls `tel.Record` directly — so no sleep is needed before asserting.)

> Note: the `brain` package already links `internal/telemetry` (DuckDB/CGO) via `Options.Telemetry`, so this test needs CGO available — same as the rest of the `brain` suite, which already builds with it. The `spawn_refused` event's actor is `"agent"` in dev (no auth → `actorOr("", "agent")`).

- [ ] **Step 1: Write the test**

```go
// internal/brain/spawn_integration_test.go
package brain

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/coord"
	"github.com/pdbethke/corralai/internal/telemetry"
)

func TestSpawnRefusalRecorded(t *testing.T) {
	dir := t.TempDir()
	store, err := coord.Open(filepath.Join(dir, "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	tel, err := telemetry.Open(filepath.Join(dir, "t.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tel.Close() })

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	opts := Options{Telemetry: tel, SpawnBudget: SpawnBudget{MaxChildrenPerParent: 1}}
	go func() { _ = NewServer(store, nil, opts).Run(ctx, serverT) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "smoke", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sess.Close()

	mk := func(name string) {
		_, _ = sess.CallTool(ctx, &mcp.CallToolParams{
			Name:      "spawn_subagent",
			Arguments: map[string]any{"name": name, "role": "tester"},
		})
	}
	mk("t1") // ok (first child)
	mk("t2") // refused: breadth cap 1 → records a spawn_refused event

	tl, err := tel.AgentTimeline("agent", 20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range tl {
		if e.Kind == "spawn_refused" {
			found = true
		}
	}
	if !found {
		t.Fatal("a spawn_refused telemetry event should have been recorded")
	}
}
```

- [ ] **Step 2: Run + commit**

Run: `go test ./internal/brain/`
Expected: PASS.

```bash
git add internal/brain/spawn_integration_test.go
git commit -m "test(brain): spawn budget refusal is enforced + recorded end-to-end"
```

---

## Final verification

- [ ] `go build ./...` — OK
- [ ] `go test ./...` — all PASS
- [ ] `CGO_ENABLED=0 go build ./cmd/corral-agent` — CGO-free OK
- [ ] Grep check: no agent-process launching outside `cmd/corral-agent` (`grep -rn 'exec.Command' internal/ cmd/corral/` shows only sandbox/console, not the brain).

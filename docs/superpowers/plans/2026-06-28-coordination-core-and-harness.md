# Coordination-Core + CI Harness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make corralai's exclusive claims trustworthy and permission-aware, and prove it with a deterministic in-process harness driving real MCP clients over real HTTP.

**Architecture:** All changes live in `internal/coord` (the SQLite-backed store) and `internal/brain` (the MCP tool layer). A package-level clock seam makes time-dependent behavior testable. Exclusive `ClaimPaths` becomes transactional (atomic check+insert) so a race yields exactly one zero-conflict winner. Agents carry a `status`; a parked (`awaiting_approval`) exclusive lease is derived-downgraded to advisory after a grace window, computed inside the same transaction — nothing is mutated or stolen. A new HTTP harness boots the real brain on `httptest` and connects N real MCP clients to assert all of this.

**Tech Stack:** Go 1.26, `modernc.org/sqlite` (pure-Go, `MaxOpenConns(1)`), `github.com/modelcontextprotocol/go-sdk` v1.6.1 (`mcp`), `net/http/httptest`.

## Global Constraints

- Module path: `github.com/pdbethke/corralai`. Go 1.26.
- The store uses `db.SetMaxOpenConns(1)` — a single connection serializes statements. Atomicity across multiple statements still requires an explicit transaction (`db.Begin`).
- No new third-party dependencies.
- Time MUST flow through the package-level `now` seam (Task 1) — never call `time.Now()` directly in `internal/coord` after Task 1.
- `CORRALAI_PARKED_GRACE_SECONDS` (float seconds, default `300`) is the parked-lease grace window.
- Tests run under `go test -race ./...` and must be deterministic (no `time.Sleep` for correctness; advance the clock seam instead).
- Commit after each task with a `feat(coord):` / `feat(brain):` / `test:` prefix.

## File Structure

- `internal/coord/store.go` — MODIFY: clock seam; `status`/`status_since` schema + `SetStatus`; `Agent.Status`/`StatusSince`; transactional atomic `ClaimPaths`; `activeClaim` named type + `activeClaimsTx`; `parkedGraceSeconds`; `ContestedClaims`; `Bootstrap.Contested`.
- `internal/coord/clock_test.go` — CREATE: `withClock` helper + TTL-expiry test.
- `internal/coord/status_test.go` — CREATE: status + parked-lease + resume tests.
- `internal/coord/claim_test.go` — CREATE: atomic-race + sequential + overlap + release tests.
- `internal/brain/server.go` — MODIFY: `heartbeat` tool carries optional `status`.
- `internal/brain/harness_test.go` — CREATE: HTTP cohort fixture (boot brain on httptest + N clients).
- `internal/brain/harness_coord_test.go` — CREATE: over-the-wire concurrency/presence/inbox tests.

---

### Task 1: Clock seam + deterministic TTL expiry

**Files:**
- Modify: `internal/coord/store.go:99` (`func now` → `var now`)
- Test: `internal/coord/clock_test.go` (create)

**Interfaces:**
- Produces: `var now = func() float64` (package `coord`); test helper `withClock(t *testing.T, f func() float64)`.

- [ ] **Step 1: Write the failing test**

Create `internal/coord/clock_test.go`:

```go
package coord

import (
	"path/filepath"
	"testing"
)

// withClock overrides the package clock for the duration of a test.
func withClock(t *testing.T, f func() float64) {
	t.Helper()
	orig := now
	now = f
	t.Cleanup(func() { now = orig })
}

func openTmp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestClaimExpiresWithClock(t *testing.T) {
	clock := 1000.0
	withClock(t, func() float64 { return clock })
	s := openTmp(t)
	if err := s.Register("alice", "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimPaths("alice", []string{"src/app.go"}, 10, true, ""); err != nil {
		t.Fatal(err)
	}
	// Before expiry: bob sees a conflict.
	clock = 1005
	r, err := s.ClaimPaths("bob", []string{"src/app.go"}, 10, true, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Conflicts) != 1 {
		t.Fatalf("before expiry want 1 conflict, got %d", len(r.Conflicts))
	}
	// After alice's lease expires: a fresh claimant sees none.
	clock = 1100
	r2, err := s.ClaimPaths("carol", []string{"src/app.go"}, 10, true, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(r2.Conflicts) != 0 {
		t.Fatalf("after expiry want 0 conflicts, got %d", len(r2.Conflicts))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/coord/ -run TestClaimExpiresWithClock`
Expected: FAIL to compile — `cannot assign to now (neither addressable nor a map index expression)` (because `now` is a `func`, not a `var`).

- [ ] **Step 3: Make `now` an overridable var**

In `internal/coord/store.go`, change line 99 from:

```go
func now() float64 { return float64(time.Now().UnixNano()) / 1e9 }
```

to:

```go
var now = func() float64 { return float64(time.Now().UnixNano()) / 1e9 }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/coord/ -run TestClaimExpiresWithClock -race`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/coord/store.go internal/coord/clock_test.go
git commit -m "feat(coord): overridable now seam for deterministic time tests"
```

---

### Task 2: Agent `status` / `status_since`

**Files:**
- Modify: `internal/coord/store.go` (schema migrations ~line 117; `Agent` struct ~line 71; `ListActive` query+scan ~line 258; add `SetStatus`)
- Test: `internal/coord/status_test.go` (create)

**Interfaces:**
- Consumes: `openTmp`, `withClock` (Task 1).
- Produces: `Agent.Status string`, `Agent.StatusSince float64`; `(s *Store) SetStatus(name, status string) error`. New `agents` columns `status TEXT NOT NULL DEFAULT 'working'`, `status_since REAL NOT NULL DEFAULT 0`.

- [ ] **Step 1: Write the failing test**

Create `internal/coord/status_test.go`:

```go
package coord

import "testing"

func TestSetStatusStampsSinceOnChangeOnly(t *testing.T) {
	clock := 500.0
	withClock(t, func() float64 { return clock })
	s := openTmp(t)
	if err := s.Register("alice", "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	clock = 600
	if err := s.SetStatus("alice", "awaiting_approval"); err != nil {
		t.Fatal(err)
	}
	clock = 700
	if err := s.SetStatus("alice", "awaiting_approval"); err != nil { // unchanged
		t.Fatal(err)
	}
	agents, err := s.ListActive(100000)
	if err != nil {
		t.Fatal(err)
	}
	var a Agent
	for _, x := range agents {
		if x.Name == "alice" {
			a = x
		}
	}
	if a.Status != "awaiting_approval" {
		t.Fatalf("status = %q", a.Status)
	}
	if a.StatusSince != 600 {
		t.Fatalf("status_since should stamp once on change, got %v (want 600)", a.StatusSince)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/coord/ -run TestSetStatusStampsSinceOnChangeOnly`
Expected: FAIL to compile — `s.SetStatus undefined` and `a.Status undefined`.

- [ ] **Step 3: Add columns, struct fields, scan, and SetStatus**

In `internal/coord/store.go`, add to the migration loop in `Open` (after the `role` ALTER):

```go
		`ALTER TABLE agents ADD COLUMN status TEXT NOT NULL DEFAULT 'working'`,
		`ALTER TABLE agents ADD COLUMN status_since REAL NOT NULL DEFAULT 0`,
```

Add two fields to the `Agent` struct (after `Role`):

```go
	Status       string  `json:"status,omitempty"`
	StatusSince  float64 `json:"status_since,omitempty"`
```

Change the `ListActive` query to select the two columns and its scan to read them:

```go
	rows, err := s.db.Query(
		"SELECT name, program, model, task, parent, role, status, status_since, last_active_ts, registered_ts FROM agents WHERE last_active_ts > ? ORDER BY last_active_ts DESC",
		now()-window)
	...
		if err := rows.Scan(&a.Name, &a.Program, &a.Model, &a.Task, &a.Parent, &a.Role, &a.Status, &a.StatusSince, &a.LastActiveTS, &a.RegisteredTS); err != nil {
```

Add the method (near `Heartbeat`):

```go
// SetStatus records an agent's coordination posture (working | awaiting_approval |
// idle). status_since is stamped only when the value actually changes, so callers
// can measure how long an agent has been parked.
func (s *Store) SetStatus(name, status string) error {
	n := now()
	_, err := s.db.Exec(
		`UPDATE agents SET status=?, status_since=CASE WHEN status<>? THEN ? ELSE status_since END, last_active_ts=? WHERE name=?`,
		status, status, n, n, name)
	return err
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/coord/ -run TestSetStatusStampsSinceOnChangeOnly -race`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/coord/store.go internal/coord/status_test.go
git commit -m "feat(coord): agent status + status_since presence fields"
```

---

### Task 3: Atomic exclusive `ClaimPaths`

**Files:**
- Modify: `internal/coord/store.go` (`activeClaims` → named `activeClaim` type + `activeClaimsTx`; rewrite `ClaimPaths` to be transactional and enforcing for exclusive)
- Test: `internal/coord/claim_test.go` (create)

**Interfaces:**
- Consumes: `openTmp` (Task 1).
- Produces: `type activeClaim struct { Agent, Path, Reason, Status string; Exclusive bool; StatusSince float64 }`; `func activeClaimsTx(tx *sql.Tx, n float64) ([]activeClaim, error)`. `ClaimPaths` keeps its signature `(name string, paths []string, ttlSeconds float64, exclusive bool, reason string) (*ClaimResult, error)` but: an exclusive claim that overlaps an *enforcing* exclusive holder is **not granted** (`Granted` omits it) and a conflict is reported.

- [ ] **Step 1: Write the failing tests**

Create `internal/coord/claim_test.go`:

```go
package coord

import (
	"sync"
	"testing"
)

func TestExclusiveRaceExactlyOneWinner(t *testing.T) {
	s := openTmp(t)
	const n = 6
	names := []string{"a", "b", "c", "d", "e", "f"}
	for _, nm := range names {
		if err := s.Register(nm, "", "", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	results := make([]*ClaimResult, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			r, err := s.ClaimPaths(names[i], []string{"src/app.go"}, 3600, true, "")
			if err != nil {
				t.Errorf("claim: %v", err)
				return
			}
			results[i] = r
		}(i)
	}
	close(start)
	wg.Wait()

	winners := 0
	for _, r := range results {
		if r == nil {
			continue
		}
		if len(r.Granted) == 1 && len(r.Conflicts) == 0 {
			winners++
		} else if len(r.Granted) != 0 {
			t.Fatalf("a losing exclusive claim must NOT be granted, got granted=%v conflicts=%v", r.Granted, r.Conflicts)
		}
	}
	if winners != 1 {
		t.Fatalf("want exactly one winner, got %d", winners)
	}
}

func TestSequentialConflictReporting(t *testing.T) {
	s := openTmp(t)
	s.Register("a", "", "", "", "")
	s.Register("b", "", "", "", "")
	if _, err := s.ClaimPaths("a", []string{"src/app.go"}, 3600, true, "edit"); err != nil {
		t.Fatal(err)
	}
	r, err := s.ClaimPaths("b", []string{"src/app.go"}, 3600, true, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Granted) != 0 {
		t.Fatalf("b must not be granted, got %v", r.Granted)
	}
	if len(r.Conflicts) != 1 || r.Conflicts[0].HeldBy != "a" {
		t.Fatalf("b must see conflict held_by a, got %+v", r.Conflicts)
	}
}

func TestOverlapDirVsFile(t *testing.T) {
	s := openTmp(t)
	s.Register("a", "", "", "", "")
	s.Register("b", "", "", "", "")
	s.ClaimPaths("a", []string{"src"}, 3600, true, "")
	r, _ := s.ClaimPaths("b", []string{"src/app.go"}, 3600, true, "")
	if len(r.Conflicts) != 1 {
		t.Fatalf("nested path must conflict, got %+v", r.Conflicts)
	}
}

func TestReleaseFreesPath(t *testing.T) {
	s := openTmp(t)
	s.Register("a", "", "", "", "")
	s.Register("b", "", "", "", "")
	s.ClaimPaths("a", []string{"src/app.go"}, 3600, true, "")
	if _, err := s.ReleaseClaims("a", []string{"src/app.go"}); err != nil {
		t.Fatal(err)
	}
	r, _ := s.ClaimPaths("b", []string{"src/app.go"}, 3600, true, "")
	if len(r.Conflicts) != 0 || len(r.Granted) != 1 {
		t.Fatalf("after release b should claim cleanly, got granted=%v conflicts=%v", r.Granted, r.Conflicts)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/coord/ -run 'TestExclusiveRace|TestSequentialConflict' -race`
Expected: FAIL — `TestExclusiveRaceExactlyOneWinner` fails (today every claimant is granted, so winners != 1 and losers have non-empty Granted).

- [ ] **Step 3: Replace `activeClaims` with a named type + tx variant, and make `ClaimPaths` transactional**

In `internal/coord/store.go`, replace the anonymous-struct `activeClaims` method with:

```go
type activeClaim struct {
	Agent, Path, Reason, Status string
	Exclusive                   bool
	StatusSince                 float64
}

func scanActiveClaims(rows *sql.Rows) ([]activeClaim, error) {
	defer rows.Close()
	var out []activeClaim
	for rows.Next() {
		var c activeClaim
		var excl int
		if err := rows.Scan(&c.Agent, &c.Path, &excl, &c.Reason, &c.Status, &c.StatusSince); err != nil {
			return nil, err
		}
		c.Exclusive = excl != 0
		out = append(out, c)
	}
	return out, rows.Err()
}

const activeClaimsSQL = `
	SELECT c.agent_name, c.path, c.exclusive, c.reason,
	       COALESCE(a.status,'working'), COALESCE(a.status_since,0)
	FROM claims c LEFT JOIN agents a ON a.name = c.agent_name
	WHERE c.released_ts IS NULL AND c.expires_ts > ? ORDER BY c.created_ts`

func (s *Store) activeClaims() ([]activeClaim, error) {
	rows, err := s.db.Query(activeClaimsSQL, now())
	if err != nil {
		return nil, err
	}
	return scanActiveClaims(rows)
}

func activeClaimsTx(tx *sql.Tx, n float64) ([]activeClaim, error) {
	rows, err := tx.Query(activeClaimsSQL, n)
	if err != nil {
		return nil, err
	}
	return scanActiveClaims(rows)
}
```

Then update any callers of the old `activeClaims()` that used `.Agent/.Path/.Exclusive/.Reason` — the field names are unchanged, so they still compile.

Rewrite `ClaimPaths` to be transactional and enforcing for exclusive claims:

```go
// ClaimPaths leases paths. Exclusive claims are ENFORCED: if an enforcing exclusive
// holder overlaps, the path is NOT granted and the conflict is reported. The whole
// check+insert runs in one transaction so a race yields exactly one winner.
func (s *Store) ClaimPaths(name string, paths []string, ttlSeconds float64, exclusive bool, reason string) (*ClaimResult, error) {
	n := now()
	expires := n + ttlSeconds
	res := &ClaimResult{Granted: []string{}, Conflicts: []Conflict{}, ExpiresTS: expires, Advisory: !exclusive}
	exclInt := 0
	if exclusive {
		exclInt = 1
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	active, err := activeClaimsTx(tx, n)
	if err != nil {
		return nil, err
	}
	for _, p := range paths {
		blocked := false
		for _, c := range active {
			if c.Agent == name || !c.Exclusive || !pathsOverlap(p, c.Path) {
				continue
			}
			res.Conflicts = append(res.Conflicts, Conflict{Path: p, HeldBy: c.Agent, TheirPath: c.Path, Reason: c.Reason})
			if exclusive && enforcing(c, n) {
				blocked = true
			}
		}
		if blocked {
			continue // exclusive claim loses to an enforcing holder: not granted
		}
		if _, err := tx.Exec(
			"INSERT INTO claims (agent_name, path, exclusive, reason, created_ts, expires_ts) VALUES (?,?,?,?,?,?)",
			name, p, exclInt, reason, n, expires); err != nil {
			return nil, err
		}
		res.Granted = append(res.Granted, p)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	_ = s.Heartbeat(name)
	s.audit(name, "claim", map[string]any{"paths": paths, "ttl": ttlSeconds, "conflicts": len(res.Conflicts)})
	return res, nil
}

// enforcing reports whether an exclusive holder still blocks. Task 4 makes a
// long-parked holder non-enforcing; until then every exclusive holder enforces.
func enforcing(c activeClaim, n float64) bool { return true }
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/coord/ -race`
Expected: PASS (all claim tests, plus Task 1/2 still green).

- [ ] **Step 5: Commit**

```bash
git add internal/coord/store.go internal/coord/claim_test.go
git commit -m "feat(coord): atomic enforcing exclusive claims (one winner per race)"
```

---

### Task 4: Derived parked-lease downgrade

**Files:**
- Modify: `internal/coord/store.go` (`enforcing` body; add `parkedGraceSeconds`; import `os`, `strconv`)
- Test: `internal/coord/status_test.go` (add)

**Interfaces:**
- Consumes: `activeClaim`, `enforcing` (Task 3); `SetStatus` (Task 2); `withClock` (Task 1).
- Produces: `func parkedGraceSeconds() float64` (env `CORRALAI_PARKED_GRACE_SECONDS`, default 300). `enforcing` returns false for an `awaiting_approval` holder parked longer than the grace window.

- [ ] **Step 1: Write the failing test**

Append to `internal/coord/status_test.go`:

```go
func TestParkedExclusiveDowngradesToAdvisory(t *testing.T) {
	clock := 1000.0
	withClock(t, func() float64 { return clock })
	t.Setenv("CORRALAI_PARKED_GRACE_SECONDS", "20")
	s := openTmp(t)
	s.Register("owner", "", "", "", "")
	s.Register("peer", "", "", "", "")
	if _, err := s.ClaimPaths("owner", []string{"src/app.go"}, 3600, true, "editing"); err != nil {
		t.Fatal(err)
	}

	// Owner parks at t=1010.
	clock = 1010
	if err := s.SetStatus("owner", "awaiting_approval"); err != nil {
		t.Fatal(err)
	}

	// Within the grace window (t=1020, parked 10s < 20): peer is BLOCKED.
	clock = 1020
	r1, _ := s.ClaimPaths("peer", []string{"src/app.go"}, 3600, true, "")
	if len(r1.Granted) != 0 || len(r1.Conflicts) != 1 {
		t.Fatalf("within grace peer must be blocked, got granted=%v conflicts=%v", r1.Granted, r1.Conflicts)
	}

	// Past the grace window (t=1040, parked 30s > 20): peer is GRANTED with a surfaced conflict.
	clock = 1040
	r2, _ := s.ClaimPaths("peer", []string{"src/app.go"}, 3600, true, "")
	if len(r2.Granted) != 1 {
		t.Fatalf("past grace peer must be granted, got %v", r2.Granted)
	}
	if len(r2.Conflicts) != 1 || r2.Conflicts[0].HeldBy != "owner" {
		t.Fatalf("past grace peer must still SEE the conflict, got %+v", r2.Conflicts)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/coord/ -run TestParkedExclusiveDowngrades -race`
Expected: FAIL — past the grace window the peer is still blocked (`enforcing` always returns true), so `r2.Granted` is empty.

- [ ] **Step 3: Implement the grace window in `enforcing`**

In `internal/coord/store.go`, add to the imports `"os"` and `"strconv"`, then replace the placeholder `enforcing` with:

```go
// parkedGraceSeconds is how long an awaiting_approval holder keeps enforcing its
// exclusive lease before it derives down to advisory. Demo sets this low (~20).
func parkedGraceSeconds() float64 {
	if v := os.Getenv("CORRALAI_PARKED_GRACE_SECONDS"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
			return f
		}
	}
	return 300
}

// enforcing reports whether an exclusive holder still blocks peers. A holder that
// has been awaiting_approval longer than the grace window is treated as advisory
// (non-blocking) — derived here, never mutated, so it reverts the moment the
// holder un-parks.
func enforcing(c activeClaim, n float64) bool {
	if c.Status == "awaiting_approval" && n-c.StatusSince > parkedGraceSeconds() {
		return false
	}
	return true
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/coord/ -race`
Expected: PASS (all coord tests).

- [ ] **Step 5: Commit**

```bash
git add internal/coord/store.go internal/coord/status_test.go
git commit -m "feat(coord): parked exclusive leases derive down to advisory after grace"
```

---

### Task 5: Resume re-validation (`Bootstrap.Contested`)

**Files:**
- Modify: `internal/coord/store.go` (`Bootstrap` struct ~line 95; add `ContestedClaims`; wire into `BootstrapSession` ~line 426)
- Test: `internal/coord/status_test.go` (add)

**Interfaces:**
- Consumes: `activeClaims`, `pathsOverlap`, `Conflict` (existing); `SetStatus`, parked downgrade (Tasks 2/4).
- Produces: `Bootstrap.Contested []Conflict`; `func (s *Store) ContestedClaims(name string) ([]Conflict, error)` — the agent's own still-held paths that overlap another agent's enforcing-or-not exclusive claim.

- [ ] **Step 1: Write the failing test**

Append to `internal/coord/status_test.go`:

```go
func TestResumeFlagsContestedClaims(t *testing.T) {
	clock := 2000.0
	withClock(t, func() float64 { return clock })
	t.Setenv("CORRALAI_PARKED_GRACE_SECONDS", "20")
	s := openTmp(t)
	s.Register("owner", "", "", "", "")
	s.Register("peer", "", "", "", "")
	s.ClaimPaths("owner", []string{"src/app.go"}, 3600, true, "")

	clock = 2010
	s.SetStatus("owner", "awaiting_approval")
	clock = 2040 // past grace: peer can take it
	if _, err := s.ClaimPaths("peer", []string{"src/app.go"}, 3600, true, ""); err != nil {
		t.Fatal(err)
	}

	// Owner returns and re-bootstraps: its lease is flagged contested.
	b, err := s.BootstrapSession("owner", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Contested) != 1 || b.Contested[0].Path != "src/app.go" || b.Contested[0].HeldBy != "peer" {
		t.Fatalf("owner must be told src/app.go is contested by peer, got %+v", b.Contested)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/coord/ -run TestResumeFlagsContested`
Expected: FAIL to compile — `b.Contested undefined`.

- [ ] **Step 3: Add the field, the helper, and wire it into BootstrapSession**

In `internal/coord/store.go`, add a field to the `Bootstrap` struct (after `RecentCompleted`):

```go
	Contested       []Conflict        `json:"contested,omitempty"`
```

Add the helper:

```go
// ContestedClaims returns the agent's own still-held paths that overlap ANOTHER
// agent's active exclusive claim — i.e. work it must re-validate before touching
// (a peer may have proceeded while it was parked).
func (s *Store) ContestedClaims(name string) ([]Conflict, error) {
	n := now()
	rows, err := s.db.Query(
		"SELECT path FROM claims WHERE agent_name=? AND released_ts IS NULL AND expires_ts > ?", name, n)
	if err != nil {
		return nil, err
	}
	var mine []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return nil, err
		}
		mine = append(mine, p)
	}
	rows.Close()
	active, err := s.activeClaims()
	if err != nil {
		return nil, err
	}
	out := []Conflict{}
	for _, p := range mine {
		for _, c := range active {
			if c.Agent != name && c.Exclusive && pathsOverlap(p, c.Path) {
				out = append(out, Conflict{Path: p, HeldBy: c.Agent, TheirPath: c.Path, Reason: c.Reason})
			}
		}
	}
	return out, nil
}
```

In `BootstrapSession`, before the final `return`, compute it and add it to the returned literal:

```go
	contested, err := s.ContestedClaims(name)
	if err != nil {
		return nil, err
	}
	return &Bootstrap{
		You:             map[string]string{"name": name, "task": task},
		ActivePeers:     peers,
		YourClaims:      claims,
		RecentCompleted: done,
		Contested:       contested,
		Hint:            "Check active_peers' claims and recent_completed before claiming work.",
	}, nil
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/coord/ -race`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/coord/store.go internal/coord/status_test.go
git commit -m "feat(coord): flag contested claims on bootstrap for resume re-validation"
```

---

### Task 6: Brain `heartbeat` carries `status`

**Files:**
- Modify: `internal/brain/server.go` (`heartbeatIn` + the `heartbeat` tool handler)
- Test: `internal/brain/harness_coord_test.go` covers this over the wire in Task 8; add a focused in-memory check here.

**Interfaces:**
- Consumes: `coord.SetStatus` (Task 2).
- Produces: `heartbeat` tool accepts optional `status`; when non-empty it calls `store.SetStatus`.

- [ ] **Step 1: Write the failing test**

Create `internal/brain/status_wire_test.go`:

```go
package brain

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/coord"
)

func TestHeartbeatSetsStatus(t *testing.T) {
	dir := t.TempDir()
	cs, err := coord.Open(filepath.Join(dir, "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	go func() { _ = NewServer(cs, nil, Options{}).Run(ctx, st) }()
	cl := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	sess, err := cl.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	if _, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name: "bootstrap", Arguments: map[string]any{"name": "alice"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name: "heartbeat", Arguments: map[string]any{"name": "alice", "status": "awaiting_approval"}}); err != nil {
		t.Fatal(err)
	}
	agents, _ := cs.ListActive(100000)
	if len(agents) != 1 || agents[0].Status != "awaiting_approval" {
		t.Fatalf("status not recorded via heartbeat: %+v", agents)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/brain/ -run TestHeartbeatSetsStatus`
Expected: FAIL — status stays `working` (the `status` arg is ignored / `heartbeatIn` has no such field).

- [ ] **Step 3: Add `status` to the heartbeat tool**

In `internal/brain/server.go`, extend `heartbeatIn`:

```go
type heartbeatIn struct {
	Name   string `json:"name"`
	Status string `json:"status,omitempty" jsonschema:"working | awaiting_approval | idle"`
}
```

Update the `heartbeat` tool handler to set status when provided:

```go
	mcp.AddTool(s, &mcp.Tool{Name: "heartbeat", Description: "Refresh your presence so peers know you're alive. Optionally report status (working|awaiting_approval|idle)."},
		func(_ context.Context, req *mcp.CallToolRequest, in heartbeatIn) (*mcp.CallToolResult, okOut, error) {
			actor := identity(req, in.Name)
			if in.Status != "" {
				if err := store.SetStatus(actor, in.Status); err != nil {
					return nil, okOut{}, err
				}
			}
			return nil, okOut{OK: true}, store.Heartbeat(actor)
		})
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/brain/ -run TestHeartbeatSetsStatus -race`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/brain/server.go internal/brain/status_wire_test.go
git commit -m "feat(brain): heartbeat tool carries agent status"
```

---

### Task 7: HTTP cohort harness fixture

**Files:**
- Create: `internal/brain/harness_test.go`

**Interfaces:**
- Consumes: `NewServer` (existing); `mcp.NewStreamableHTTPHandler`, `mcp.StreamableClientTransport` (go-sdk).
- Produces: `newCohort(t, n int) *cohort`; `(*cohort).call(i int, tool string, args map[string]any, out any)`; `cohort.sessions []*mcp.ClientSession`. The brain runs on a real `httptest` server; each of the n sessions is an independent MCP client over real HTTP.

- [ ] **Step 1: Write the fixture + a smoke test that uses it**

Create `internal/brain/harness_test.go`:

```go
package brain

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/coord"
)

type cohort struct {
	t        *testing.T
	store    *coord.Store
	ts       *httptest.Server
	sessions []*mcp.ClientSession
}

// newCohort boots the real brain on an httptest server and connects n independent
// MCP clients to it over real streamable-HTTP (auth off). Each session behaves like
// a separate machine.
func newCohort(t *testing.T, n int) *cohort {
	t.Helper()
	ctx := context.Background()
	cs, err := coord.Open(filepath.Join(t.TempDir(), "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(cs, nil, Options{})
	handler := mcp.NewStreamableHTTPHandler(
		func(*mcp.Request) *mcp.Server { return srv }, // see note below
		&mcp.StreamableHTTPOptions{DisableLocalhostProtection: true},
	)
	ts := httptest.NewServer(handler)
	c := &cohort{t: t, store: cs, ts: ts}
	for i := 0; i < n; i++ {
		cl := mcp.NewClient(&mcp.Implementation{Name: "harness-client", Version: "0"}, nil)
		sess, err := cl.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL}, nil)
		if err != nil {
			t.Fatalf("client %d connect: %v", i, err)
		}
		c.sessions = append(c.sessions, sess)
	}
	t.Cleanup(func() {
		for _, s := range c.sessions {
			s.Close()
		}
		ts.Close()
		cs.Close()
	})
	return c
}

// call invokes a tool on session i and unmarshals the structured result into out
// (pass nil to ignore the result).
func (c *cohort) call(i int, tool string, args map[string]any, out any) {
	c.t.Helper()
	res, err := c.sessions[i].CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		c.t.Fatalf("call %s: %v", tool, err)
	}
	if res.IsError {
		c.t.Fatalf("tool %s errored: %+v", tool, res.Content)
	}
	if out != nil {
		b, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(b, out); err != nil {
			c.t.Fatalf("decode %s result: %v", tool, err)
		}
	}
}

func TestHarnessSmoke(t *testing.T) {
	c := newCohort(t, 2)
	c.call(0, "bootstrap", map[string]any{"name": "alice"}, nil)
	c.call(1, "bootstrap", map[string]any{"name": "bob"}, nil)
	var out struct {
		Agents []coord.Agent `json:"agents"`
	}
	c.call(0, "list_active", map[string]any{}, &out)
	if len(out.Agents) != 2 {
		t.Fatalf("want 2 active agents over the wire, got %d", len(out.Agents))
	}
}
```

> Note on the handler signature: `mcp.NewStreamableHTTPHandler`'s first argument is `func(*http.Request) *mcp.Server` in `cmd/corral/main.go`. Use the exact same parameter type the SDK expects (copy it verbatim from `main.go:340-343`). If the SDK version differs, match `main.go` — it is the source of truth for this codebase.

- [ ] **Step 2: Run the smoke test to verify the fixture works**

Run: `go test ./internal/brain/ -run TestHarnessSmoke -race`
Expected: PASS. If it fails on the handler func signature, align it with `cmd/corral/main.go:340-343` (verbatim) and re-run.

- [ ] **Step 3: (no separate impl — the fixture IS the deliverable)**

- [ ] **Step 4: Confirm the package still builds and all tests pass**

Run: `go test ./internal/brain/ -race`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/brain/harness_test.go
git commit -m "test(brain): HTTP cohort harness — real brain on httptest + N MCP clients"
```

---

### Task 8: Over-the-wire concurrency, presence, and inbox assertions

**Files:**
- Create: `internal/brain/harness_coord_test.go`

**Interfaces:**
- Consumes: `newCohort`, `(*cohort).call` (Task 7); tools `bootstrap`, `claim_paths`, `heartbeat`, `list_active`, `send_instruction`, `check_instructions`, `ack_instruction`.

- [ ] **Step 1: Write the failing/asserting tests**

Create `internal/brain/harness_coord_test.go`:

```go
package brain

import (
	"context"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/coord"
)

// Over real HTTP, N independent clients race to claim the same path; exactly one
// must win cleanly (relies on the atomic exclusive claim from Task 3).
func TestWireExclusiveRaceOneWinner(t *testing.T) {
	const n = 5
	c := newCohort(t, n)
	for i := 0; i < n; i++ {
		c.call(i, "bootstrap", map[string]any{"name": agentName(i)}, nil)
	}
	results := make([]coord.ClaimResult, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			res, err := c.sessions[i].CallTool(context.Background(), &mcp.CallToolParams{
				Name: "claim_paths",
				Arguments: map[string]any{"name": agentName(i), "paths": []string{"src/app.go"}, "exclusive": true},
			})
			if err != nil || res.IsError {
				t.Errorf("claim over wire: %v %+v", err, res)
				return
			}
			decode(t, res, &results[i])
		}(i)
	}
	close(start)
	wg.Wait()

	winners := 0
	for _, r := range results {
		if len(r.Granted) == 1 && len(r.Conflicts) == 0 {
			winners++
		} else if len(r.Granted) != 0 {
			t.Fatalf("loser must not be granted: %+v", r)
		}
	}
	if winners != 1 {
		t.Fatalf("want exactly one winner over the wire, got %d", winners)
	}
}

func TestWirePresenceReflectsStatus(t *testing.T) {
	c := newCohort(t, 1)
	c.call(0, "bootstrap", map[string]any{"name": "alice"}, nil)
	c.call(0, "heartbeat", map[string]any{"name": "alice", "status": "awaiting_approval"}, nil)
	var out struct {
		Agents []coord.Agent `json:"agents"`
	}
	c.call(0, "list_active", map[string]any{}, &out)
	if len(out.Agents) != 1 || out.Agents[0].Status != "awaiting_approval" {
		t.Fatalf("presence must reflect status over the wire, got %+v", out.Agents)
	}
}

func TestWireInstructionRoundTrip(t *testing.T) {
	c := newCohort(t, 2)
	c.call(0, "bootstrap", map[string]any{"name": "boss"}, nil)
	c.call(1, "bootstrap", map[string]any{"name": "worker"}, nil)
	c.call(0, "send_instruction", map[string]any{"target": "worker", "text": "claim src/ and refactor"}, nil)

	var inbox struct {
		Instructions []struct {
			ID   int64  `json:"id"`
			Text string `json:"text"`
		} `json:"instructions"`
	}
	c.call(1, "check_instructions", map[string]any{"name": "worker"}, &inbox)
	if len(inbox.Instructions) != 1 || inbox.Instructions[0].Text == "" {
		t.Fatalf("worker should have 1 instruction, got %+v", inbox.Instructions)
	}
	c.call(1, "ack_instruction", map[string]any{"name": "worker", "id": inbox.Instructions[0].ID, "result": "done"}, nil)
	var inbox2 struct {
		Instructions []any `json:"instructions"`
	}
	c.call(1, "check_instructions", map[string]any{"name": "worker"}, &inbox2)
	if len(inbox2.Instructions) != 0 {
		t.Fatalf("acked instruction should clear from pending inbox, got %+v", inbox2.Instructions)
	}
}

func agentName(i int) string { return string(rune('a' + i)) }

func decode(t *testing.T, res *mcp.CallToolResult, out any) {
	t.Helper()
	b, _ := jsonMarshal(res.StructuredContent)
	if err := jsonUnmarshal(b, out); err != nil {
		t.Fatalf("decode: %v", err)
	}
}
```

> Before running: confirm the exact field names/shapes of `check_instructions` / `ack_instruction` / `send_instruction` in `internal/brain/inbox.go` and adjust the `inbox` struct tags + argument keys to match (e.g. the id/text/target field names). The harness `call`/`decode` plumbing does not change — only the literal arg keys and result struct tags. Replace `jsonMarshal`/`jsonUnmarshal` with `encoding/json`'s `Marshal`/`Unmarshal` (import it; the placeholder names just flag that this file needs the `encoding/json` import).

- [ ] **Step 2: Reconcile inbox tool shapes, then run**

Open `internal/brain/inbox.go`, confirm the `send_instruction` / `check_instructions` / `ack_instruction` input and output structs, and fix the test's arg keys + result tags to match. Replace `jsonMarshal`/`jsonUnmarshal` with `json.Marshal`/`json.Unmarshal` and add the `encoding/json` import.

Run: `go test ./internal/brain/ -run TestWire -race`
Expected: `TestWireExclusiveRaceOneWinner` and `TestWirePresenceReflectsStatus` PASS immediately (they rest on Tasks 3/6). Fix any inbox shape mismatches until `TestWireInstructionRoundTrip` passes.

- [ ] **Step 3: (no separate impl — these are assertion-only tests on existing behavior)**

- [ ] **Step 4: Full suite green under race**

Run: `go test ./... -race`
Expected: PASS (whole module).

- [ ] **Step 5: Commit**

```bash
git add internal/brain/harness_coord_test.go
git commit -m "test(brain): over-the-wire race/presence/inbox coordination assertions"
```

---

## Self-Review

- **Spec coverage:** Coordination-Core #1 (atomic exclusive) → Task 3; #2 (status) → Tasks 2, 6; #3 (parked downgrade) → Task 4; #4 (resume re-validation) → Task 5; #5 (clock seam) → Task 1. Deliverable 1 harness (real HTTP, N clients, barrier race, presence, inbox, TTL, parked, resume) → Tasks 7–8 (over-wire) + Tasks 1–5 (coord-level deterministic). UI (#2 visuals) and the demo env are explicitly **out of scope** for this plan (Plans 2 and 3).
- **Placeholder scan:** the only deferred specifics are in Task 8 Step 2 (inbox tool field names) and Task 7's handler-func-signature note — both point the implementer at the exact source file to copy from (`internal/brain/inbox.go`, `cmd/corral/main.go:340-343`) rather than leaving a blank. Acceptable because the surrounding test logic is complete and the unknown is a verbatim copy from a named location.
- **Type consistency:** `activeClaim` (Task 3) is consumed by `enforcing` (Tasks 3/4); `ClaimResult`/`Conflict` field names (`Granted`, `Conflicts`, `HeldBy`, `Path`, `TheirPath`) match the existing `coord` types; `Agent.Status`/`StatusSince` (Task 2) are read in Tasks 4/6/8; `Bootstrap.Contested` (Task 5) matches the harness/coord tests. `now` is a `var` from Task 1 onward.

## Out of Scope (later plans)

- Swarm UI rendering of status/parked leases (Plan 2).
- The docker demo env, `corral-agent`, profiles, BYO docs (Plan 3).
- Mission-advancement MCP-level assertions (existing `internal/mission/engine_test.go` covers the mechanics).

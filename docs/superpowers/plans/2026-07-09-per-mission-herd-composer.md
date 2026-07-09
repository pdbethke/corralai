# Per-Mission Herd Composer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let an operator compose a mission's herd at launch — assign agents to roles (exists), select MCP endpoints the herd may consume (new), attach lookbook design directives (new) — persisted per-mission and injected into the herd's task instructions.

**Architecture:** A new `mission_herds` side table records each mission's herd config. The existing `/api/mission/create` handler (and the `create_mission` MCP tool) resolve lookbook ids → guideline text and validate endpoint names, inject that context into the plan's phase instructions via a pure `InjectHerdContext` helper, create the mission, then persist the herd row. v1 keeps one active mission at a time and applies `role_models` to the run at launch exactly as today; concurrency is out of scope.

**Tech Stack:** Go 1.26, modernc SQLite (queue/mission stores), the go-sdk MCP server, vanilla JS cockpit (`internal/ui/web/index.html`).

## Global Constraints

- SPDX header `// SPDX-License-Identifier: Elastic-2.0` on every new Go file.
- TDD: failing test first, watch it fail, minimal code, watch it pass, commit.
- `go vet ./...` clean; full suite green before each commit.
- Corral metaphor in any user-facing copy — herd/corral/wrangle, never bee/hive/swarm.
- Branch: `feat/per-mission-herd-composer` (already created; the spec is committed there).
- Degrade-never-block: a missing/empty herd row means "no overrides" and the run behaves byte-identically to today.
- Injected operator free-text (lookbook guidelines) MUST be wrapped in `fence.Untrusted` (same pattern as `internal/mission/replan.go`).

## File Structure

- Create `internal/mission/herd.go` — `Herd` type + `SaveHerd`/`Herd` store methods; adds the `mission_herds` table to the schema.
- Create `internal/mission/herd_test.go` — store round-trip tests.
- Create `internal/mission/inject.go` — pure `InjectHerdContext(plan, guidelines, endpoints) []PhaseSpec`.
- Create `internal/mission/inject_test.go` — injection tests.
- Modify `internal/ui/ui.go` — `createMission` handler: decode + validate + resolve + inject + persist; add `composeOptions` handler.
- Create `internal/ui/compose_test.go` — httptest for the handler.
- Modify `internal/ui/web/index.html` — composer: backend fix + endpoints + lookbook sections.
- Modify `internal/brain/missions.go` — `create_mission` MCP tool parity (optional, Task 6).
- Modify `scratch/guardrail-probe/main.go` — live compose→launch→verify probe (Task 7).

---

### Task 1: `mission_herds` store

**Files:**
- Create: `internal/mission/herd.go`
- Modify: `internal/mission/store.go` (add table to the `const schema` block, ~line 27)
- Test: `internal/mission/herd_test.go`

**Interfaces:**
- Produces: `type Herd struct { RoleModels map[string]rolemodel.ModelRef; Endpoints []string; LookbookIDs []int64 }`; `func (s *Store) SaveHerd(missionID int64, h Herd) error`; `func (s *Store) Herd(missionID int64) (*Herd, bool, error)`.

- [ ] **Step 1: Add the table to the schema.** In `internal/mission/store.go`, inside the `const schema = \`` block (after the `phases` table), add:

```sql
CREATE TABLE IF NOT EXISTS mission_herds (
  mission_id   INTEGER PRIMARY KEY,
  role_models  TEXT NOT NULL DEFAULT '{}',
  endpoints    TEXT NOT NULL DEFAULT '[]',
  lookbook_ids TEXT NOT NULL DEFAULT '[]',
  created_ts   REAL NOT NULL
);
```

- [ ] **Step 2: Write the failing test** in `internal/mission/herd_test.go`:

```go
// SPDX-License-Identifier: Elastic-2.0

package mission

import (
	"path/filepath"
	"testing"

	"github.com/pdbethke/corralai/internal/rolemodel"
)

func TestHerdRoundTrip(t *testing.T) {
	m, err := Open(filepath.Join(t.TempDir(), "m.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	// Absent herd → not found, no error.
	if _, ok, err := m.Herd(7); err != nil || ok {
		t.Fatalf("absent herd: ok=%v err=%v, want false/nil", ok, err)
	}

	want := Herd{
		RoleModels:  map[string]rolemodel.ModelRef{"builder": {Backend: "anthropic", Model: "claude-opus"}},
		Endpoints:   []string{"prod-db"},
		LookbookIDs: []int64{3, 9},
	}
	if err := m.SaveHerd(7, want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := m.Herd(7)
	if err != nil || !ok {
		t.Fatalf("Herd(7): ok=%v err=%v", ok, err)
	}
	if got.RoleModels["builder"].Backend != "anthropic" || got.RoleModels["builder"].Model != "claude-opus" {
		t.Fatalf("role_models round-trip wrong: %+v", got.RoleModels)
	}
	if len(got.Endpoints) != 1 || got.Endpoints[0] != "prod-db" {
		t.Fatalf("endpoints round-trip wrong: %+v", got.Endpoints)
	}
	if len(got.LookbookIDs) != 2 || got.LookbookIDs[0] != 3 || got.LookbookIDs[1] != 9 {
		t.Fatalf("lookbook_ids round-trip wrong: %+v", got.LookbookIDs)
	}

	// Save again (upsert) → overwrites, still one row.
	if err := m.SaveHerd(7, Herd{Endpoints: []string{"other"}}); err != nil {
		t.Fatal(err)
	}
	got2, _, _ := m.Herd(7)
	if len(got2.Endpoints) != 1 || got2.Endpoints[0] != "other" || len(got2.LookbookIDs) != 0 {
		t.Fatalf("upsert did not overwrite: %+v", got2)
	}
}
```

- [ ] **Step 3: Run it, verify it fails.** Run: `go test ./internal/mission/ -run TestHerdRoundTrip -v` → FAIL (`SaveHerd`/`Herd` undefined).

- [ ] **Step 4: Implement `internal/mission/herd.go`:**

```go
// SPDX-License-Identifier: Elastic-2.0

package mission

import (
	"database/sql"
	"encoding/json"

	"github.com/pdbethke/corralai/internal/rolemodel"
)

// Herd is a mission's composed team: which model serves each role, which MCP
// gateway endpoints its agents may consume, and which lookbook items it attaches
// as design directives. Persisted per-mission in mission_herds so a mission owns
// its herd (the "compose now, concurrency later" plumbing) — v1 also applies
// RoleModels to the run at launch via the existing global policy.
type Herd struct {
	RoleModels  map[string]rolemodel.ModelRef `json:"role_models"`
	Endpoints   []string                      `json:"endpoints"`
	LookbookIDs []int64                       `json:"lookbook_ids"`
}

// SaveHerd upserts a mission's herd config. Idempotent per mission_id.
func (s *Store) SaveHerd(missionID int64, h Herd) error {
	rm, err := json.Marshal(h.RoleModels)
	if err != nil {
		return err
	}
	ep, err := json.Marshal(h.Endpoints)
	if err != nil {
		return err
	}
	lb, err := json.Marshal(h.LookbookIDs)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO mission_herds(mission_id,role_models,endpoints,lookbook_ids,created_ts)
		 VALUES(?,?,?,?,?)
		 ON CONFLICT(mission_id) DO UPDATE SET
		   role_models=excluded.role_models, endpoints=excluded.endpoints, lookbook_ids=excluded.lookbook_ids`,
		missionID, string(rm), string(ep), string(lb), now())
	return err
}

// Herd returns a mission's herd config, or (nil, false, nil) when none was saved.
func (s *Store) Herd(missionID int64) (*Herd, bool, error) {
	var rm, ep, lb string
	err := s.db.QueryRow(`SELECT role_models,endpoints,lookbook_ids FROM mission_herds WHERE mission_id=?`, missionID).
		Scan(&rm, &ep, &lb)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var h Herd
	_ = json.Unmarshal([]byte(rm), &h.RoleModels)
	_ = json.Unmarshal([]byte(ep), &h.Endpoints)
	_ = json.Unmarshal([]byte(lb), &h.LookbookIDs)
	return &h, true, nil
}
```

- [ ] **Step 5: Run it, verify it passes.** Run: `go test ./internal/mission/ -run TestHerdRoundTrip -v` → PASS. Then `go test ./internal/mission/` → all green.

- [ ] **Step 6: Commit.**

```bash
git add internal/mission/herd.go internal/mission/herd_test.go internal/mission/store.go
git commit -m "feat(mission): mission_herds store — a mission owns its herd config"
```

---

### Task 2: `InjectHerdContext` helper

**Files:**
- Create: `internal/mission/inject.go`
- Test: `internal/mission/inject_test.go`

**Interfaces:**
- Consumes: `PhaseSpec` (existing, `store.go:83`), `fence.Untrusted` (existing).
- Produces: `func InjectHerdContext(plan []PhaseSpec, lookbookGuidelines []string, endpointNames []string) []PhaseSpec`.

- [ ] **Step 1: Write the failing test** in `internal/mission/inject_test.go`:

```go
// SPDX-License-Identifier: Elastic-2.0

package mission

import (
	"strings"
	"testing"
)

func TestInjectHerdContext(t *testing.T) {
	plan := []PhaseSpec{
		{Name: "design", Role: "designer", Instruction: "design the UI"},
		{Name: "build", Role: "builder", Instruction: "build it"},
		{Name: "scan", Role: "pentester", Instruction: "attack it"},
	}
	out := InjectHerdContext(plan, []string{"Emulate the neon dashboard mock."}, []string{"prod-db", "search"})

	// Endpoint note goes to EVERY role.
	for _, p := range out {
		if !strings.Contains(p.Instruction, "prod-db") || !strings.Contains(p.Instruction, "search") {
			t.Fatalf("%s missing endpoint note: %q", p.Role, p.Instruction)
		}
	}
	// Lookbook goes to designer/builder/reviewer, NOT pentester.
	if !strings.Contains(out[0].Instruction, "neon dashboard") {
		t.Fatalf("designer missing lookbook: %q", out[0].Instruction)
	}
	if !strings.Contains(out[1].Instruction, "neon dashboard") {
		t.Fatalf("builder missing lookbook: %q", out[1].Instruction)
	}
	if strings.Contains(out[2].Instruction, "neon dashboard") {
		t.Fatalf("pentester should NOT get the lookbook directive: %q", out[2].Instruction)
	}
	// Original instruction is preserved (prepended, not replaced).
	if !strings.Contains(out[1].Instruction, "build it") {
		t.Fatalf("original instruction lost: %q", out[1].Instruction)
	}
}

func TestInjectHerdContextNoOp(t *testing.T) {
	plan := []PhaseSpec{{Name: "build", Role: "builder", Instruction: "build it"}}
	out := InjectHerdContext(plan, nil, nil)
	if out[0].Instruction != "build it" {
		t.Fatalf("no herd context must leave instructions untouched, got %q", out[0].Instruction)
	}
}
```

- [ ] **Step 2: Run it, verify it fails.** Run: `go test ./internal/mission/ -run TestInjectHerdContext -v` → FAIL (`InjectHerdContext` undefined).

- [ ] **Step 3: Implement `internal/mission/inject.go`:**

```go
// SPDX-License-Identifier: Elastic-2.0

package mission

import (
	"strings"

	"github.com/pdbethke/corralai/internal/fence"
)

// lookbookRoles are the roles whose work is shaped by a visual design directive.
// Pentester/perf/writer/researcher don't consume a UI style guide.
var lookbookRoles = map[string]bool{"designer": true, "builder": true, "reviewer": true}

// InjectHerdContext prepends a mission's herd context to its phase instructions:
// an "available MCP capabilities" note to EVERY role, and the lookbook design
// directive to the design-shaping roles only. Operator-authored guideline text is
// fenced as untrusted (an operator paste must not smuggle instructions to an
// agent). Empty inputs return the plan unchanged (degrade-never-block).
func InjectHerdContext(plan []PhaseSpec, lookbookGuidelines []string, endpointNames []string) []PhaseSpec {
	if len(lookbookGuidelines) == 0 && len(endpointNames) == 0 {
		return plan
	}
	endpointBlock := ""
	if len(endpointNames) > 0 {
		endpointBlock = "Available MCP capabilities you may use (call via call_capability): " +
			strings.Join(endpointNames, ", ") + "."
	}
	lookbookBlock := ""
	if len(lookbookGuidelines) > 0 {
		lookbookBlock = fence.Untrusted("lookbook design directive", "operator",
			strings.Join(lookbookGuidelines, "\n\n"))
	}
	out := make([]PhaseSpec, len(plan))
	for i, p := range plan {
		var pre []string
		if endpointBlock != "" {
			pre = append(pre, endpointBlock)
		}
		if lookbookBlock != "" && lookbookRoles[p.Role] {
			pre = append(pre, lookbookBlock)
		}
		if len(pre) > 0 {
			p.Instruction = strings.Join(pre, "\n\n") + "\n\n" + p.Instruction
		}
		out[i] = p
	}
	return out
}
```

- [ ] **Step 4: Run it, verify it passes.** Run: `go test ./internal/mission/ -run TestInjectHerdContext -v` → PASS.

- [ ] **Step 5: Commit.**

```bash
git add internal/mission/inject.go internal/mission/inject_test.go
git commit -m "feat(mission): InjectHerdContext — herd endpoints + lookbook into task instructions"
```

---

### Task 3: Wire the herd into `/api/mission/create`

**Files:**
- Modify: `internal/ui/ui.go` (`createMission`, ~line 902)
- Test: `internal/ui/compose_test.go`

**Interfaces:**
- Consumes: `mission.CreateMission`, `mission.ScaledPlan`, `mission.InjectHerdContext`, `(*mission.Store).SaveHerd`, `(*gateway.Store).Usable`, `(*taskartifacts.Store).GetLookbookItem`, `auth.Principal`.
- Produces: `/api/mission/create` accepting `mcp_endpoints []string` and `lookbook_ids []int64` in addition to `role_models`.

- [ ] **Step 1: Write the failing test** in `internal/ui/compose_test.go`:

```go
// SPDX-License-Identifier: Elastic-2.0

package ui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/gateway"
	"github.com/pdbethke/corralai/internal/mission"
	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/rolemodel"
	"github.com/pdbethke/corralai/internal/taskartifacts"
)

func TestCreateMissionPersistsAndInjectsHerd(t *testing.T) {
	dir := t.TempDir()
	m, _ := mission.Open(filepath.Join(dir, "m.sqlite3"))
	q, _ := queue.Open(filepath.Join(dir, "q.sqlite3"))
	gw, _ := gateway.Open(filepath.Join(dir, "g.sqlite3"))
	ta, _ := taskartifacts.Open(filepath.Join(dir, "ta.sqlite3"))
	// An endpoint usable by the anonymous (dev) principal "".
	if err := gw.Register(gateway.Endpoint{Name: "prod-db", Transport: "stdio", Endpoint: "x", Enabled: true}, gateway.Auth{}, ""); err != nil {
		t.Fatal(err)
	}
	lbID, err := ta.SaveLookbookItem("neon", "Emulate the neon dashboard mock.", "image/png", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}

	s := NewServer(Deps{Missions: m, Queue: q, Gateway: gw, TaskArtifacts: ta, RoleModels: rolemodel.New()})

	body, _ := json.Marshal(map[string]any{
		"directive":     "build a dashboard",
		"role_models":   map[string]rolemodel.ModelRef{"builder": {Backend: "anthropic", Model: "claude-opus"}},
		"mcp_endpoints": []string{"prod-db"},
		"lookbook_ids":  []int64{lbID},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/mission/create", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.createMission(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		ID int64 `json:"id"`
		OK bool  `json:"ok"`
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	if !out.OK || out.ID == 0 {
		t.Fatalf("bad response: %s", rec.Body.String())
	}

	// Herd persisted.
	h, ok, _ := m.Herd(out.ID)
	if !ok || len(h.Endpoints) != 1 || h.Endpoints[0] != "prod-db" || len(h.LookbookIDs) != 1 {
		t.Fatalf("herd not persisted: %+v ok=%v", h, ok)
	}
	// Context injected into a builder task instruction.
	tasks, _ := q.List(out.ID)
	sawEndpoint, sawLookbook := false, false
	for _, tk := range tasks {
		if strings.Contains(tk.Instruction, "prod-db") {
			sawEndpoint = true
		}
		if tk.Role == "builder" && strings.Contains(tk.Instruction, "neon dashboard") {
			sawLookbook = true
		}
	}
	if !sawEndpoint || !sawLookbook {
		t.Fatalf("injection missing: endpoint=%v lookbook=%v", sawEndpoint, sawLookbook)
	}
}

func TestCreateMissionRejectsUnknownEndpoint(t *testing.T) {
	dir := t.TempDir()
	m, _ := mission.Open(filepath.Join(dir, "m.sqlite3"))
	q, _ := queue.Open(filepath.Join(dir, "q.sqlite3"))
	gw, _ := gateway.Open(filepath.Join(dir, "g.sqlite3"))
	ta, _ := taskartifacts.Open(filepath.Join(dir, "ta.sqlite3"))
	s := NewServer(Deps{Missions: m, Queue: q, Gateway: gw, TaskArtifacts: ta, RoleModels: rolemodel.New()})

	body, _ := json.Marshal(map[string]any{"directive": "x", "mcp_endpoints": []string{"nope"}})
	req := httptest.NewRequest(http.MethodPost, "/api/mission/create", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.createMission(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown endpoint must be rejected, got %d: %s", rec.Code, rec.Body.String())
	}
}
```

- [ ] **Step 2: Run it, verify it fails.** Run: `go test ./internal/ui/ -run TestCreateMission -v` → FAIL (handler ignores the new fields; herd not persisted / no injection).

- [ ] **Step 3: Implement.** In `internal/ui/ui.go` `createMission`, extend the decoded `body` struct and add resolution/injection/persistence. Replace the body struct and the `plan := ...; CreateMission(...)` tail:

```go
	var body struct {
		Directive      string                        `json:"directive"`
		RequiresReview bool                          `json:"requires_review"`
		RoleModels     map[string]rolemodel.ModelRef `json:"role_models"`
		MCPEndpoints   []string                      `json:"mcp_endpoints"`
		LookbookIDs    []int64                       `json:"lookbook_ids"`
	}
```

Then, after the running-mission conflict guard and the existing `role_models` apply block, replace `plan := mission.ScaledPlan(...)` / `CreateMission` with:

```go
	// Resolve + validate the herd's endpoints and lookbook directives.
	principal := auth.Principal(r.Context())
	var endpointNames []string
	if len(body.MCPEndpoints) > 0 && s.gw != nil {
		usable, _ := s.gw.Usable(principal)
		ok := map[string]bool{}
		for _, e := range usable {
			ok[e.Name] = true
		}
		for _, name := range body.MCPEndpoints {
			if !ok[name] {
				http.Error(w, fmt.Sprintf("unknown or inaccessible MCP endpoint %q", name), http.StatusBadRequest)
				return
			}
			endpointNames = append(endpointNames, name)
		}
	}
	var guidelines []string
	if len(body.LookbookIDs) > 0 && s.taskArtifacts != nil {
		for _, id := range body.LookbookIDs {
			item, err := s.taskArtifacts.GetLookbookItem(id)
			if err != nil || item == nil {
				http.Error(w, fmt.Sprintf("unknown lookbook item %d", id), http.StatusBadRequest)
				return
			}
			guidelines = append(guidelines, item.Name+": "+item.Description)
		}
	}

	plan := mission.InjectHerdContext(mission.ScaledPlan(body.Directive), guidelines, endpointNames)
	id, err := mission.CreateMission(s.missions, s.queue, body.Directive, plan, body.RequiresReview)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.missions.SaveHerd(id, mission.Herd{
		RoleModels: body.RoleModels, Endpoints: endpointNames, LookbookIDs: body.LookbookIDs,
	}); err != nil {
		log.Printf("mission %d: SaveHerd: %v", id, err) // non-fatal: the run proceeds
	}
	writeJSON(w, map[string]any{"id": id, "ok": true})
```

Ensure `fmt` and `log` are imported (they already are in ui.go).

- [ ] **Step 4: Run it, verify it passes.** Run: `go test ./internal/ui/ -run TestCreateMission -v` → PASS. Then `go test ./internal/ui/` → green.

- [ ] **Step 5: Commit.**

```bash
git add internal/ui/ui.go internal/ui/compose_test.go
git commit -m "feat(ui): /api/mission/create persists + injects the per-mission herd"
```

---

### Task 4: Composer options endpoint

**Files:**
- Modify: `internal/ui/ui.go` (add `composeOptions` handler + route registration next to the other `/api/...` routes, ~line 175)
- Test: `internal/ui/compose_test.go` (append)

**Interfaces:**
- Produces: `GET /api/mission/compose-options` → `{"endpoints": [{"name","description"}...], "lookbook": [{"id","name","description"}...]}` for the composer's pickers.

- [ ] **Step 1: Write the failing test** (append to `internal/ui/compose_test.go`):

```go
func TestComposeOptions(t *testing.T) {
	dir := t.TempDir()
	gw, _ := gateway.Open(filepath.Join(dir, "g.sqlite3"))
	ta, _ := taskartifacts.Open(filepath.Join(dir, "ta.sqlite3"))
	gw.Register(gateway.Endpoint{Name: "prod-db", Transport: "stdio", Endpoint: "x", Enabled: true}, gateway.Auth{}, "")
	ta.SaveLookbookItem("neon", "neon mock", "image/png", []byte("x"))
	s := NewServer(Deps{Gateway: gw, TaskArtifacts: ta})

	req := httptest.NewRequest(http.MethodGet, "/api/mission/compose-options", nil)
	rec := httptest.NewRecorder()
	s.composeOptions(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var out struct {
		Endpoints []struct{ Name string `json:"name"` } `json:"endpoints"`
		Lookbook  []struct{ Name string `json:"name"` } `json:"lookbook"`
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Endpoints) != 1 || out.Endpoints[0].Name != "prod-db" {
		t.Fatalf("endpoints wrong: %+v", out.Endpoints)
	}
	if len(out.Lookbook) != 1 || out.Lookbook[0].Name != "neon" {
		t.Fatalf("lookbook wrong: %+v", out.Lookbook)
	}
}
```

- [ ] **Step 2: Run it, verify it fails.** Run: `go test ./internal/ui/ -run TestComposeOptions -v` → FAIL (`composeOptions` undefined).

- [ ] **Step 3: Implement the handler** in `internal/ui/ui.go`:

```go
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
		}
	}
	lbs := []lbView{}
	if s.taskArtifacts != nil {
		if metas, err := s.taskArtifacts.GetLookbookItemsMeta(); err == nil {
			for _, mta := range metas {
				lbs = append(lbs, lbView{ID: mta.ID, Name: mta.Name, Description: mta.Description})
			}
		}
	}
	writeJSON(w, map[string]any{"endpoints": eps, "lookbook": lbs})
}
```

Register the route beside the other `/api/mission/*` mux entries (~line 175):

```go
	mux.HandleFunc("/api/mission/compose-options", s.composeOptions)
```

Note: confirm `LookbookItemMeta` exposes `ID`, `Name`, `Description` (`internal/taskartifacts/store.go:180`); adjust field names if they differ.

- [ ] **Step 4: Run it, verify it passes.** Run: `go test ./internal/ui/ -run TestComposeOptions -v` → PASS.

- [ ] **Step 5: Commit.**

```bash
git add internal/ui/ui.go internal/ui/compose_test.go
git commit -m "feat(ui): /api/mission/compose-options for the composer pickers"
```

---

### Task 5: Composer UI — backend fix + endpoints + lookbook sections

**Files:**
- Modify: `internal/ui/web/index.html` (`renderComposer` ~2138, `submitMission` ~2217)

**Interfaces:**
- Consumes: `GET /api/mission/compose-options`; `POST /api/mission/create` with `mcp_endpoints[]`, `lookbook_ids[]`.

No Go unit harness exists for the cockpit JS; this task is verified by the live probe in Task 7 and a manual smoke. Keep the changes minimal and mechanical.

- [ ] **Step 1: Fix the backend tag.** In `submitMission` (~2230), replace the hardcoded backend. The agent chips must carry a backend. Change `getLocalAgents`/`renderComposer` so each chip's `data-agent` stays the label but add `data-backend`, and in `mcDrop` store `{agent, backend}` in `mcAssignments[r]`. Then:

```js
  const roleModels = {};
  for (const [r, a] of Object.entries(mcAssignments)) {
    if (a && a.agent) {
      roleModels[r] = { backend: a.backend || 'ollama', model: a.agent };
    }
  }
```

Cloud agents (claude/gemini/codex/copilot) map to their backend (anthropic/google/openai/…); local Ollama models keep `ollama`. Derive the backend from the topology entry that produced the agent, defaulting to `ollama` only for a genuinely local model.

- [ ] **Step 2: Load compose options.** In the composer's init/render, fetch once:

```js
let composeOpts = { endpoints: [], lookbook: [] };
function loadComposeOptions() {
  fetch('/api/mission/compose-options').then(r => r.json()).then(d => {
    composeOpts = d || { endpoints: [], lookbook: [] };
    renderCockpit(); // re-render the composer with the pickers populated
  }).catch(() => {});
}
```

Call `loadComposeOptions()` when the composer tab is shown.

- [ ] **Step 3: Add the two sections** to `renderComposer`'s right column, after "Roles Setup":

```js
  const endpointsHtml = composeOpts.endpoints.map(e => `
    <label class="mc-check"><input type="checkbox" class="mc-endpoint" value="${esc(e.name)}"> ${esc(e.name)}</label>
  `).join('') || '<span class="mc-empty">no MCP endpoints available</span>';

  const lookbookHtml = composeOpts.lookbook.map(l => `
    <label class="mc-check"><input type="checkbox" class="mc-lookbook" value="${l.id}"> ${esc(l.name)}</label>
  `).join('') || '<span class="mc-empty">no lookbook items</span>';
```

Insert into the returned markup:

```html
          <div class="mc-sec-title">MCP Endpoints (the herd may consume)</div>
          <div class="mc-endpoints">${endpointsHtml}</div>
          <div class="mc-sec-title">Lookbook Directives</div>
          <div class="mc-lookbooks">${lookbookHtml}</div>
```

- [ ] **Step 4: Post the selections.** In `submitMission`, gather the checked boxes and add to the POST body:

```js
  const mcpEndpoints = [...document.querySelectorAll('.mc-endpoint:checked')].map(c => c.value);
  const lookbookIds = [...document.querySelectorAll('.mc-lookbook:checked')].map(c => parseInt(c.value, 10));
  // ...in the fetch body JSON, alongside role_models:
  //   mcp_endpoints: mcpEndpoints,
  //   lookbook_ids: lookbookIds,
```

- [ ] **Step 5: Verify by building + serving.** Run: `make build && go vet ./...` → clean. Manual: launch a local dev brain (see Task 7 harness), open `http://127.0.0.1:9119`, confirm the composer shows the two new sections. (Automated verification is Task 7.)

- [ ] **Step 6: Commit.**

```bash
git add internal/ui/web/index.html
git commit -m "feat(ui): composer gains MCP-endpoint + lookbook sections; fix backend tag"
```

---

### Task 6: `create_mission` MCP tool parity

**Files:**
- Modify: `internal/brain/missions.go` (`createMissionIn` ~32, the `create_mission` tool handler)
- Test: `internal/brain/missions_test.go` (append)

**Interfaces:**
- Consumes: `mission.InjectHerdContext`, `(*mission.Store).SaveHerd`, `opts.Gateway`, `opts.TaskArtifacts`.
- Produces: `create_mission` accepting `mcp_endpoints []string`, `lookbook_ids []int64`.

This mirrors Task 3 for the headless/lead path. It is parity — if execution is time-boxed, it may follow the UI path. Implement the same resolve→validate→inject→CreateMission→SaveHerd sequence, using `opts.Gateway.Usable(actorOf(req))` and `opts.TaskArtifacts.GetLookbookItem`. Add `MCPEndpoints`/`LookbookIDs` JSON fields to `createMissionIn`, and TDD a test analogous to `TestCreateMissionPersistsAndInjectsHerd` over the in-memory MCP transport (mirror the harness in `missions_test.go`). Commit:

```bash
git commit -m "feat(brain): create_mission MCP tool accepts herd endpoints + lookbook"
```

---

### Task 7: Live compose → launch probe

**Files:**
- Modify: `scratch/guardrail-probe/main.go` (add a `probeCompose` function, register it in `main`)

**Interfaces:**
- Consumes: the local dev brain's `/api/mission/create` + `/api/mission/compose-options` (HTTP), and MCP `list_tasks`/`mission_status`.

- [ ] **Step 1: Add `probeCompose`** that: registers a gateway endpoint + a lookbook item against the local brain (via `register_endpoint` MCP + `POST /api/lookbook/upload`), GETs `/api/mission/compose-options` and asserts both appear, POSTs `/api/mission/create` with `role_models`, `mcp_endpoints`, `lookbook_ids`, then reads the mission's tasks (`list_tasks`) and asserts a builder task instruction contains the lookbook guideline and the endpoint note. (The brain must be launched with the gateway + taskartifacts stores enabled — the default dev launch in the Task 7 harness does this.)

- [ ] **Step 2: Run the probe** against a freshly-built brain (reuse the Task-launch harness from the guardrail run: isolated `HOME`, `CORRALAI_ADDR=127.0.0.1:9119`, auth off). Expected: `[PASS] compose  herd persisted + injected`.

- [ ] **Step 3: Commit** (only if keeping the probe tracked; otherwise leave in `scratch/`).

```bash
git add scratch/guardrail-probe/main.go
git commit -m "test(probe): live compose→launch herd verification"
```

---

## Self-Review

**Spec coverage:**
- MCP-endpoints section → Tasks 3 (persist/validate), 4 (options), 5 (UI). ✓
- Lookbook section → Tasks 3, 4, 5. ✓
- Per-mission herd persistence → Task 1 + Task 3 (`SaveHerd`). ✓
- Backend-tagging fix → Task 5 Step 1. ✓
- Runtime injection at instruction assembly → Task 2 + Task 3. ✓
- Review-and-harden the Gemini path → Task 5 (backend fix, escaping via `esc`) + Task 3 (validation, non-fatal SaveHerd); the injected free-text is fenced (Task 2). ✓
- Both create paths → Task 3 (HTTP, primary) + Task 6 (MCP parity). ✓
- Testing (store/inject/API/live) → Tasks 1,2,3,4,6 (unit) + 7 (live). ✓
- v1/v2 boundary → data model persists per-mission (Task 1); v1 still applies role_models to the global policy at launch (Task 3, unchanged); no queue changes. ✓

**Placeholder scan:** none — every code step shows real code. Task 6 is intentionally summarized as parity mirroring Task 3 (its full code equals Task 3's with `actorOf(req)` and MCP error returns).

**Type consistency:** `Herd{RoleModels, Endpoints, LookbookIDs}` used identically in Tasks 1, 3, 6. `InjectHerdContext(plan, guidelines, endpointNames)` signature identical in Tasks 2, 3. `SaveHerd(id, Herd)` / `Herd(id) (*Herd, bool, error)` consistent. Handler field names `mcp_endpoints`/`lookbook_ids` consistent across Tasks 3, 4, 5, 6.

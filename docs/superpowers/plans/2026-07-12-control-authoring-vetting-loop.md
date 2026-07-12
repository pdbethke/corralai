<!-- SPDX-License-Identifier: Elastic-2.0 -->
# Control-owner authoring/vetting loop — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire the built-but-unwired authoring pipeline into in-process, admin-gated, audited MCP tools so a control owner can define goals, author + score a candidate test, review it, and approve it into the vetted store the live control gate runs.

**Architecture:** Four tasks. (1) Fix the `StageCandidate` glue bug + fail-loud on an invalid candidate. (2) Share one controlspec store handle across the gate + the new tools via `Options`. (3) Factor the two non-trivial tool operations (`stageControl`, `getControl`) into testable functions. (4) Register seven admin/owner-gated, audited MCP tools mirroring the `memory` tools, and wire the shared store + model in `cmd/corral`.

**Tech Stack:** Go 1.26.5; `internal/controlgate`, `internal/controlspec`, `internal/testgen`, `internal/authoring`, `internal/adequacy`, `internal/gate`, `internal/brain`, MCP (`github.com/modelcontextprotocol/go-sdk/mcp`), `internal/llm`.

## Global Constraints
- SPDX header `// SPDX-License-Identifier: Elastic-2.0` on every new file.
- **TDD**; per commit: `go vet ./...` + `go test ./...` + `go build ./...` + `bash scripts/check-security.sh` (`export PATH="$PATH:$HOME/go/bin"`) all green.
- **Human gate invariant:** a staged candidate is ALWAYS unvetted (`SaveCandidate` forces it); only `promote_control` vets, and it is **admin-gated + audited + principal-attributed**. `stage_control` can never vet.
- **Fail-loud authoring:** an authored test that does not pass on compliant code (`Report.CompliantPass == false`) is an INVALID candidate — error, store nothing.
- **Recipe coherence:** staging derives `Base`/`TestCmd` from the SAME `controlgate.LangScaffold(lang)` the gate uses, and persists `CodePath`/`TestPath`, so a promoted candidate reproduces at gate time.
- **One open controlspec handle** (no second R/W opener → no DuckDB single-writer lock conflict).
- **Owner scoping:** every tool scopes to the caller's principal (`identity(req, "")`); writes gate on `opts.isHumanAdmin(req)` → `errAdminOnly`.
- **Determinism:** stores/`internal` libs never call `time.Now()` (clocks injected); MCP handlers (glue) MAY call `time.Now()`, matching the memory tools + `StartControlGate`.
- **One-model v1:** the same `testgen.LLM` handle fills writer + reviewer seats (distinct system prompts). corral metaphor; "control owner", never "CISO".

## File Structure
- `internal/controlgate/stage.go` — persist CodePath/TestPath + CompliantPass guard. (modify)
- `internal/controlgate/stage_test.go` — glue + guard tests. (modify)
- `internal/brain/identity.go` — `Options.ControlSpec`, `Options.ControlModel`. (modify)
- `internal/brain/controlgate.go` — `controlSpecStore` helper + StartControlGate shares it. (modify)
- `internal/brain/controlgate_test.go` — helper test. (modify)
- `internal/brain/controltools.go` — `stageControl`/`getControl` logic + `registerControlTools`. (new)
- `internal/brain/controltools_test.go` — logic tests. (new)
- `internal/brain/server.go` — register the tools. (modify)
- `cmd/corral/main.go` — open the shared store + set Options fields. (modify)

## Interfaces (produced → consumed)
```go
// controlgate (Task 1): StageCandidate now sets gt.CodePath/gt.TestPath and errors if !CompliantPass.
// brain Options (Task 2):
ControlSpec  *controlspec.Store  // shared vetted/goal store handle
ControlModel testgen.LLM         // the writer+reviewer model (llm.FromEnv())
func controlSpecStore(opts Options) (store *controlspec.Store, owns bool, err error)
// brain controltools (Task 3):
type stager func(ctx context.Context, req controlgate.StageRequest) (controlspec.GateTest, error)
func stageControl(ctx context.Context, store *controlspec.Store, stage stager, owner, goalID, target, code, lang, codePath, testPath string, nMutants int, now time.Time) (stageControlOut, error)
func getControl(store *controlspec.Store, owner, goal, target string) (controlspec.GateTest, error)
func registerControlTools(s *mcp.Server, opts Options)   // Task 4
```

---

## Task 1: controlgate — persist the recipe + fail-loud on an invalid candidate

**Files:** Modify `internal/controlgate/stage.go`; Test `internal/controlgate/stage_test.go`

**Interfaces:**
- Produces: `StageCandidate` now sets `gt.CodePath`/`gt.TestPath` from the embedded `authoring.Request`, and returns an error (storing nothing) when `res.Report.CompliantPass` is false.

- [ ] **Step 1: Failing tests** — append to `stage_test.go`. (Use the file's existing fake LLM + fake jail helpers; if it lacks them, model the fakes on `internal/brain/controltools_test.go` Task 3 — but stage_test.go already exercises StageCandidate, so reuse its harness.) Add two assertions to the existing happy-path test (or a new test): the stored candidate has `CodePath`/`TestPath` set from the request; and a compliant-fail jail yields an error + no stored row.
```go
func TestStageCandidate_PersistsRecipe(t *testing.T) {
	// Reuse this file's existing StageCandidate harness (writer/reviewer fakes + jail + store).
	// After staging a candidate for a request with CodePath:"login.go", TestPath:"login_control_test.go":
	//   gt, err := StageCandidate(ctx, writer, reviewer, jail, store, req)
	//   require err == nil; require gt.CodePath == "login.go" && gt.TestPath == "login_control_test.go"
	//   and the STORED row (store.ListPending(owner)[0]) carries them too.
}

func TestStageCandidate_RejectsCompliantFail(t *testing.T) {
	// A jail whose RunTest returns (false, nil) for the COMPLIANT-code run makes
	// adequacy.Score report CompliantPass=false. StageCandidate must return an error
	// and store nothing:
	//   _, err := StageCandidate(...); require err != nil
	//   pend, _ := store.ListPending(owner); require len(pend) == 0
}
```
> Implementer: read `internal/controlgate/stage_test.go` first and match its existing fake shapes exactly (a fake `testgen.LLM` returning a valid fenced test / `===MUTATION_N===` block / `MUTANT <id>: EQUIVALENT` triage, and a fake `adequacy.Jail`). To force CompliantPass=false, the fake jail returns `false` for the compliant run. Fill the test bodies against that harness.
- [ ] **Step 2: Run, watch fail.** `go test ./internal/controlgate/ -run 'TestStageCandidate_PersistsRecipe|TestStageCandidate_RejectsCompliantFail'` → FAIL.
- [ ] **Step 3: Implement** in `stage.go`, `StageCandidate`, right after `authoring.Author` returns `res`:
```go
	if !res.Report.CompliantPass {
		return controlspec.GateTest{}, fmt.Errorf("controlgate: authored test does not pass on compliant code — invalid candidate, not staging")
	}
```
(add `"fmt"` to imports), and in the `gt := controlspec.GateTest{...}` literal add the recipe fields:
```go
		CodePath: req.CodePath, TestPath: req.TestPath,
```
- [ ] **Step 4: Run, watch pass.** `go test ./internal/controlgate/...` → PASS. Full gate. **Commit:** `fix(controlgate): StageCandidate persists CodePath/TestPath + rejects a compliant-failing candidate`.

---

## Task 2: brain — share one controlspec store across gate + tools

**Files:** Modify `internal/brain/identity.go`, `internal/brain/controlgate.go`; Test `internal/brain/controlgate_test.go`

**Interfaces:**
- Produces: `Options.ControlSpec *controlspec.Store`, `Options.ControlModel testgen.LLM`, `func controlSpecStore(opts Options) (*controlspec.Store, bool, error)`. StartControlGate reuses `opts.ControlSpec` when set.

- [ ] **Step 1: Failing test** — append to `controlgate_test.go`:
```go
func TestControlSpecStore_SharesProvided(t *testing.T) {
	provided, err := controlspec.OpenStore(filepath.Join(t.TempDir(), "cs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer provided.Close()
	s, owns, err := controlSpecStore(Options{ControlSpec: provided})
	if err != nil || s != provided || owns {
		t.Fatalf("must reuse the provided store (owns=false), got owns=%v err=%v same=%v", owns, err, s == provided)
	}
	s2, owns2, err := controlSpecStore(Options{ControlSpecDB: filepath.Join(t.TempDir(), "cs2.db")})
	if err != nil || s2 == nil || !owns2 {
		t.Fatalf("unset → open its own (owns=true), got owns=%v err=%v", owns2, err)
	}
	s2.Close()
}
```
- [ ] **Step 2: Run, watch fail** (`controlSpecStore` undefined).
- [ ] **Step 3: Implement.**
  - `identity.go`: in `Options`, near `ControlPolicies`/`ControlSpecDB`, add:
```go
	// ControlSpec, when set, is a shared controlspec store handle opened once by
	// the brain and used by BOTH the control gate (ListVetted) and the control
	// MCP tools — one open R/W handle avoids a DuckDB single-writer lock conflict.
	ControlSpec *controlspec.Store
	// ControlModel is the LLM the authoring tools use for the writer + reviewer
	// seats (llm.FromEnv()); nil disables stage_control.
	ControlModel testgen.LLM
```
    Add imports `"github.com/pdbethke/corralai/internal/controlspec"` and `"github.com/pdbethke/corralai/internal/testgen"` to `identity.go` if absent.
  - `controlgate.go`: add the helper and use it in `StartControlGate`:
```go
// controlSpecStore returns the controlspec store StartControlGate + the control
// tools share. If opts.ControlSpec is set it is reused (owns=false — the caller
// owns its lifetime); otherwise a new store is opened at opts.ControlSpecDB
// (owns=true — StartControlGate must close it on a later error).
func controlSpecStore(opts Options) (store *controlspec.Store, owns bool, err error) {
	if opts.ControlSpec != nil {
		return opts.ControlSpec, false, nil
	}
	dsn := opts.ControlSpecDB
	if dsn == "" {
		dsn = "corralai_control_spec.duckdb"
	}
	s, err := controlspec.OpenStore(dsn)
	return s, true, err
}
```
    Replace the current spec-open block in `StartControlGate`:
```go
	spec, ownsSpec, err := controlSpecStore(opts)
	if err != nil {
		return nil, nil, fmt.Errorf("control-gate: open spec store: %w", err)
	}
	runDSN := opts.ControlGateDB
	if runDSN == "" {
		runDSN = "corralai_control_gate.duckdb"
	}
	runStore, err := gate.OpenStore(runDSN)
	if err != nil {
		if ownsSpec {
			_ = spec.Close()
		}
		return nil, nil, fmt.Errorf("control-gate: open run store: %w", err)
	}
```
    (The rest of StartControlGate is unchanged; it still returns `(runStore, spec, nil)`.)
- [ ] **Step 4: Run, watch pass.** `go test ./internal/brain/ -run 'TestControlSpecStore|TestStartControlGate'` → PASS. Full gate. **Commit:** `feat(brain): share one controlspec store across the gate + control tools`.

---

## Task 3: brain — the two non-trivial tool operations (testable logic)

**Files:** Create `internal/brain/controltools.go`, `internal/brain/controltools_test.go`

**Interfaces:**
- Produces: `stager`, `stageControlOut`, `stageControl`, `getControl` (consumed by Task 4's handlers).
- Consumes: `controlspec.Store`/`GateTest`/`Goal`, `controlgate.StageRequest`/`LangScaffold`, `authoring.Request`, `testgen.Verdict`.

- [ ] **Step 1: Failing tests** — `controltools_test.go`:
```go
// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/controlgate"
	"github.com/pdbethke/corralai/internal/controlspec"
)

func TestStageControl(t *testing.T) {
	store, err := controlspec.OpenStore(filepath.Join(t.TempDir(), "cs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Unix(1_700_000_000, 0).UTC()
	if err := store.SaveGoal(controlspec.Goal{ID: "asvs-v2.1.1", Owner: "o@x", Intent: "passwords >= 12 chars", CreatedTS: now}); err != nil {
		t.Fatal(err)
	}

	var gotReq controlgate.StageRequest
	stage := func(_ context.Context, req controlgate.StageRequest) (controlspec.GateTest, error) {
		gotReq = req
		return controlspec.GateTest{Owner: req.Owner, Goal: req.GoalID, Target: req.Target,
			Test: "package control\n// t", KillRate: 0.8, Survived: []string{"m2"},
			CodePath: req.CodePath, TestPath: req.TestPath, CreatedTS: req.Now}, nil
	}
	out, err := stageControl(context.Background(), store, stage, "o@x", "asvs-v2.1.1",
		"internal/auth/login.go", "package control\n// code", "go", "login.go", "login_control_test.go", 3, now)
	if err != nil {
		t.Fatal(err)
	}
	// The staged request carries the goal INTENT (not the id), the paths, and the go scaffold.
	if gotReq.Goal != "passwords >= 12 chars" || gotReq.CodePath != "login.go" || gotReq.TestPath != "login_control_test.go" {
		t.Fatalf("staged request wrong: %+v", gotReq.Request)
	}
	if gotReq.Base["go.mod"] == "" || len(gotReq.TestCmd) == 0 {
		t.Fatalf("go scaffold not applied: %+v", gotReq.Request)
	}
	if out.KillRate != 0.8 || out.Vetted {
		t.Fatalf("summary wrong: %+v", out)
	}
	// Missing goal → error, stage untouched.
	if _, err := stageControl(context.Background(), store, stage, "o@x", "nope", "t", "c", "go", "a.go", "a_test.go", 3, now); err == nil {
		t.Fatal("missing goal must error")
	}
}

func TestGetControl(t *testing.T) {
	store, err := controlspec.OpenStore(filepath.Join(t.TempDir(), "cs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Unix(1_700_000_000, 0).UTC()
	_ = store.SaveCandidate(controlspec.GateTest{Owner: "o@x", Goal: "g1", Target: "a.go", Test: "T1", KillRate: 1, CreatedTS: now})
	_ = store.SaveCandidate(controlspec.GateTest{Owner: "o@x", Goal: "g2", Target: "b.go", Test: "T2", KillRate: 1, CreatedTS: now})

	gt, err := getControl(store, "o@x", "g2", "b.go")
	if err != nil || gt.Test != "T2" {
		t.Fatalf("getControl should return the g2 candidate: %+v %v", gt, err)
	}
	if _, err := getControl(store, "o@x", "nope", "x"); err == nil {
		t.Fatal("absent candidate must error")
	}
}
```
- [ ] **Step 2: Run, watch fail** (undefined).
- [ ] **Step 3: Implement** `controltools.go` (logic only; the `registerControlTools` func is added in Task 4 — this task adds just the funcs + types so it compiles and tests pass):
```go
// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/pdbethke/corralai/internal/authoring"
	"github.com/pdbethke/corralai/internal/controlgate"
	"github.com/pdbethke/corralai/internal/controlspec"
	"github.com/pdbethke/corralai/internal/testgen"
)

// stager stages a candidate from a fully-built request. In production it's a
// closure over controlgate.StageCandidate bound to the model + jail; tests stub it.
type stager func(ctx context.Context, req controlgate.StageRequest) (controlspec.GateTest, error)

type stageControlOut struct {
	Goal     string            `json:"goal"`
	Target   string            `json:"target"`
	KillRate float64           `json:"kill_rate"`
	Survived []string          `json:"survived"`
	Triage   []testgen.Verdict `json:"triage"`
	Vetted   bool              `json:"vetted"`
}

// stageControl looks up the goal, builds the StageRequest (goal INTENT + the
// LangScaffold recipe + the workspace paths), stages a candidate, and shapes a
// summary. The candidate is stored UNVETTED by StageCandidate.
func stageControl(ctx context.Context, store *controlspec.Store, stage stager,
	owner, goalID, target, code, lang, codePath, testPath string, nMutants int, now time.Time) (stageControlOut, error) {
	g, ok, err := store.GetGoal(owner, goalID)
	if err != nil {
		return stageControlOut{}, err
	}
	if !ok {
		return stageControlOut{}, fmt.Errorf("controltools: no goal %q for this owner", goalID)
	}
	base, testCmd, ok := controlgate.LangScaffold(lang)
	if !ok {
		return stageControlOut{}, fmt.Errorf("controltools: unsupported lang %q", lang)
	}
	if nMutants <= 0 {
		nMutants = 5
	}
	req := controlgate.StageRequest{
		Request: authoring.Request{
			Goal: g.Intent, Code: code, Lang: lang, CodePath: codePath, TestPath: testPath,
			Base: base, TestCmd: testCmd, NMutants: nMutants,
		},
		Owner: owner, GoalID: goalID, Target: target, Now: now,
	}
	gt, err := stage(ctx, req)
	if err != nil {
		return stageControlOut{}, err
	}
	var verdicts []testgen.Verdict
	if gt.VerdictsJSON != "" {
		_ = json.Unmarshal([]byte(gt.VerdictsJSON), &verdicts)
	}
	return stageControlOut{Goal: goalID, Target: target, KillRate: gt.KillRate,
		Survived: gt.Survived, Triage: verdicts, Vetted: false}, nil
}

// getControl returns one PENDING (unvetted) candidate by (goal,target) for the
// owner to read as code. ListPending returns full rows (test + survivors +
// verdicts); GetVetted can't serve unvetted rows, so we filter ListPending.
func getControl(store *controlspec.Store, owner, goal, target string) (controlspec.GateTest, error) {
	pend, err := store.ListPending(owner)
	if err != nil {
		return controlspec.GateTest{}, err
	}
	for _, gt := range pend {
		if gt.Goal == goal && gt.Target == target {
			return gt, nil
		}
	}
	return controlspec.GateTest{}, fmt.Errorf("controltools: no pending candidate %s@%s", goal, target)
}
```
- [ ] **Step 4: Run, watch pass.** `go test ./internal/brain/ -run 'TestStageControl|TestGetControl'` → PASS. Full gate. **Commit:** `feat(brain): control-tool logic (stageControl, getControl)`.

---

## Task 4: brain + cmd/corral — register the MCP tools + wire the shared store

**Files:** Modify `internal/brain/controltools.go`, `internal/brain/server.go`, `cmd/corral/main.go`

**Interfaces:**
- Produces: `func registerControlTools(s *mcp.Server, opts Options)` — seven admin/owner-gated, audited tools; registered from `NewServer`.
- Consumes: Task 3's `stageControl`/`getControl`; `opts.ControlSpec`/`ControlModel`/`GateBackend`; `adequacy.NewJail`; `gate.DefaultGateTimeout`; the memory-tool helpers (`identity`, `isHumanAdmin`, `errAdminOnly`, `auditKnowledge`, `okMsg`).

> Wiring task: the tool LOGIC is covered by Task 3; the handlers are thin gated glue. Verification = `go build`/`go vet`/full suite green + review of the gating. Add one focused test that a non-admin caller is refused by `promote_control` if practical with the repo's existing MCP-test helpers; otherwise the gating is review-verified (it is a verbatim copy of `promote_memory`'s `if !opts.isHumanAdmin(req) { return …, errAdminOnly }`).

- [ ] **Step 1: Add the tool input/output structs + `registerControlTools`** to `controltools.go`. Add imports `"github.com/modelcontextprotocol/go-sdk/mcp"`, `"github.com/pdbethke/corralai/internal/adequacy"`, `"github.com/pdbethke/corralai/internal/gate"`.
```go
type stageControlIn struct {
	GoalID   string `json:"goal_id" jsonschema:"the control goal id (from list_control_goals)"`
	Target   string `json:"target" jsonschema:"repo-relative path the control applies to, e.g. internal/auth/login.go"`
	Code     string `json:"code" jsonschema:"the target file's current source (the writer authors a test against this shape)"`
	Lang     string `json:"lang" jsonschema:"source language; v1 supports 'go'"`
	CodePath string `json:"code_path" jsonschema:"flat filename for the code in the jail workspace, e.g. login.go"`
	TestPath string `json:"test_path" jsonschema:"flat test filename, e.g. login_control_test.go (must end _test.go for go)"`
	NMutants int    `json:"n_mutants,omitempty" jsonschema:"how many seeded violations to score adequacy against (default 5)"`
}
type goalIn struct {
	Goal   string `json:"goal" jsonschema:"the control goal id"`
	Target string `json:"target" jsonschema:"the target path"`
}
type importBundleIn struct {
	Bundle string `json:"bundle" jsonschema:"bundle name, e.g. asvs-l1"`
}
type goalsOut struct {
	Goals []controlspec.Goal `json:"goals"`
}
type pendingOut struct {
	Pending []pendingSummary `json:"pending"`
}
type pendingSummary struct {
	Goal     string  `json:"goal"`
	Target   string  `json:"target"`
	KillRate float64 `json:"kill_rate"`
}
type importOut struct {
	Imported int `json:"imported"`
}

func registerControlTools(s *mcp.Server, opts Options) {
	store := opts.ControlSpec

	mcp.AddTool(s, &mcp.Tool{Name: "import_control_bundle",
		Description: "ADMIN: import a control-standard bundle (e.g. asvs-l1) as owner-scoped goals."},
		func(_ context.Context, req *mcp.CallToolRequest, in importBundleIn) (*mcp.CallToolResult, importOut, error) {
			if !opts.isHumanAdmin(req) {
				return nil, importOut{}, errAdminOnly
			}
			owner := identity(req, "")
			b, err := controlspec.LoadBundle(in.Bundle)
			if err != nil {
				return nil, importOut{}, err
			}
			n, err := controlspec.ImportBundle(store, owner, b, time.Now().UTC())
			if err == nil {
				auditKnowledge(opts, req, "import_control_bundle", map[string]any{"bundle": in.Bundle, "imported": n})
			}
			return nil, importOut{Imported: n}, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "list_control_goals",
		Description: "List your control goals (the bar the control gate holds code to)."},
		func(_ context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, goalsOut, error) {
			g, err := store.ListGoals(identity(req, ""))
			return nil, goalsOut{Goals: g}, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "stage_control",
		Description: "ADMIN: author a candidate test for a goal+target and score its mutation-adequacy. Stored UNVETTED for your review — never gates until promote_control."},
		func(ctx context.Context, req *mcp.CallToolRequest, in stageControlIn) (*mcp.CallToolResult, stageControlOut, error) {
			if !opts.isHumanAdmin(req) {
				return nil, stageControlOut{}, errAdminOnly
			}
			if opts.ControlModel == nil {
				return nil, stageControlOut{}, fmt.Errorf("stage_control: no model configured (set the brain's model backend)")
			}
			if opts.GateBackend == nil {
				return nil, stageControlOut{}, fmt.Errorf("stage_control: no sandbox backend — refusing to run authored tests unsandboxed")
			}
			model := opts.ControlModel
			jail := adequacy.NewJail(opts.GateBackend, gate.DefaultGateTimeout)
			stage := func(ctx context.Context, sreq controlgate.StageRequest) (controlspec.GateTest, error) {
				return controlgate.StageCandidate(ctx, model, model, jail, store, sreq)
			}
			owner := identity(req, "")
			out, err := stageControl(ctx, store, stage, owner, in.GoalID, in.Target, in.Code, in.Lang, in.CodePath, in.TestPath, in.NMutants, time.Now().UTC())
			if err == nil {
				auditKnowledge(opts, req, "stage_control", map[string]any{"goal": in.GoalID, "target": in.Target, "kill_rate": out.KillRate})
			}
			return nil, out, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "list_pending_controls",
		Description: "List your UNVETTED candidate controls awaiting review (goal, target, kill rate)."},
		func(_ context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, pendingOut, error) {
			pend, err := store.ListPending(identity(req, ""))
			if err != nil {
				return nil, pendingOut{}, err
			}
			out := pendingOut{}
			for _, gt := range pend {
				out.Pending = append(out.Pending, pendingSummary{Goal: gt.Goal, Target: gt.Target, KillRate: gt.KillRate})
			}
			return nil, out, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "get_control",
		Description: "Fetch one pending candidate in full — the test source, kill rate, surviving mutants, and reviewer triage — to read before promoting."},
		func(_ context.Context, req *mcp.CallToolRequest, in goalIn) (*mcp.CallToolResult, controlspec.GateTest, error) {
			gt, err := getControl(store, identity(req, ""), in.Goal, in.Target)
			return nil, gt, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "promote_control",
		Description: "ADMIN: approve a pending candidate into the vetted store the control gate runs. The recorded, attributed human gate."},
		func(_ context.Context, req *mcp.CallToolRequest, in goalIn) (*mcp.CallToolResult, okMsg, error) {
			if !opts.isHumanAdmin(req) {
				return nil, okMsg{}, errAdminOnly
			}
			owner := identity(req, "")
			ok, err := store.Promote(owner, in.Goal, in.Target, time.Now().UTC())
			if err != nil || !ok {
				return nil, okMsg{OK: false, Message: "no pending candidate to promote"}, err
			}
			auditKnowledge(opts, req, "promote_control", map[string]any{"goal": in.Goal, "target": in.Target})
			return nil, okMsg{OK: true, Message: in.Goal + "@" + in.Target + " is now vetted"}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "reject_control",
		Description: "ADMIN: delete a candidate control (vetted or not)."},
		func(_ context.Context, req *mcp.CallToolRequest, in goalIn) (*mcp.CallToolResult, okMsg, error) {
			if !opts.isHumanAdmin(req) {
				return nil, okMsg{}, errAdminOnly
			}
			ok, err := store.Reject(identity(req, ""), in.Goal, in.Target)
			if err != nil || !ok {
				return nil, okMsg{OK: false, Message: "no such candidate"}, err
			}
			auditKnowledge(opts, req, "reject_control", map[string]any{"goal": in.Goal, "target": in.Target})
			return nil, okMsg{OK: true, Message: in.Goal + "@" + in.Target + " rejected"}, nil
		})
}
```
- [ ] **Step 2: Register in `server.go`.** After the `if opts.Learn != nil { … }` block (or near the other `if opts.X != nil` registrations), add:
```go
	if opts.ControlSpec != nil {
		registerControlTools(s, opts)
	}
```
- [ ] **Step 3: Wire the shared store in `cmd/corral/main.go`.** Where the other stores are opened (near `controlSpecDB := env(...)`, before `brainOpts := brain.Options{…}`), open it once:
```go
	controlSpecStore, err := controlspec.OpenStore(controlSpecDB)
	if err != nil {
		log.Fatalf("control spec store: %v", err)
	}
```
  Import `"github.com/pdbethke/corralai/internal/controlspec"` in main.go if absent. Then in the `brainOpts := brain.Options{…}` literal add:
```go
		ControlSpec:  controlSpecStore,
		ControlModel: narrator,
```
  (`narrator` is `*llm.Client` from `llm.FromEnv()` at main.go:776 — it satisfies `testgen.LLM`.) `StartControlGate` will now reuse this handle (Task 2).
- [ ] **Step 4: Verify.** `go vet ./... && go build ./... && go test ./... && bash scripts/check-security.sh` all green. Sanity: `go doc ./internal/brain registerControlTools` compiles. **Commit:** `feat(brain): register control authoring/vetting MCP tools + share the store (cmd/corral)`.

---

## Self-Review

- **Spec coverage:** glue fix (CodePath/TestPath + CompliantPass) → Task 1; shared store → Task 2; the two logic ops → Task 3; the seven MCP tools + gating + audit + main wiring → Task 4. ✓
- **Human gate:** `stage_control` stores unvetted (SaveCandidate forces it); only `promote_control` vets, admin-gated + audited + `identity`-attributed. ✓
- **Placeholder scan:** Task 1's test bodies are described-against-existing-harness (the one place I can't hand exact fakes without reading stage_test.go) — the implementer note directs reading that file first; every other step is complete code. ✓
- **Type consistency:** `stager`/`stageControl`/`getControl` signatures match Task 4's call sites; `opts.ControlModel` (`testgen.LLM`) is passed as both writer+reviewer to `StageCandidate`; `narrator` (`*llm.Client`) satisfies `testgen.LLM`; `adequacy.NewJail(opts.GateBackend, gate.DefaultGateTimeout)` matches `StartControlGate`. ✓
- **Determinism:** stores/logic take injected clocks; only the MCP handlers + main call `time.Now()`. ✓
- **No DuckDB conflict:** one `controlspec.OpenStore` in main.go; StartControlGate + the tools reuse `opts.ControlSpec`. ✓

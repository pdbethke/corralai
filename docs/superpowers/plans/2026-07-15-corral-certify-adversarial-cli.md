<!-- SPDX-License-Identifier: Elastic-2.0 -->
# `corral certify --adversarial` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the already-shipped async adversarial pool triggerable and observable from one command: `corral certify --adversarial --code <path> --goal "<text>" -- <test cmd>` fires a run, polls to convergence, and renders a legible signed verdict.

**Architecture:** Five additive pieces. (0) Carry the signed record id on `advpool.Verdict`. (1) A pure `Driver.RunStatus` getter over the already-retained `runState.verdict`. (2) An admin-gated `get_adversarial_run` MCP tool + `AdvPoolRuntime.RunStatus`. (3) A new `--adversarial` mode in `runCertify` — an injectable `advPoolClient` (real impl over `brainclient`), flag/file/git gathering, a poll loop, and a legible verdict render with status-driven exit codes. Nothing changes in the pool's driver logic, the merge gate, or the shipped `corral certify` paths beyond the additive getter and the two new Verdict fields.

**Tech Stack:** Go 1.26.5; `github.com/modelcontextprotocol/go-sdk/mcp`; internal packages `advpool`, `brain`, `brainclient`, `queue`, `creds`.

**Spec:** `docs/superpowers/specs/2026-07-15-corral-certify-adversarial-cli-design.md`. Parent: `docs/superpowers/specs/2026-07-14-adversarial-pool-design.md`.

## Global Constraints

- **Go module:** `github.com/pdbethke/corralai`. Go 1.26.5. Every new file starts with `// SPDX-License-Identifier: Elastic-2.0`.
- **The gate is honest, never faked green:** the render MUST print `NEEDS-REVIEW` for a needs-review verdict and show `survivors`/`proven_missed`/vacuous findings truthfully even when unflattering. Never round a sub-threshold kill-rate up to "certified". The CLI only displays what the brain signed; it invents no field.
- **No trust surface added:** `get_adversarial_run` is read-only behind the same `isHumanAdmin` gate as `start_adversarial_run`. The CLI computes no verdict.
- **Do not change `advpool.Verdict`'s JSON marshaling shape for existing fields.** `advpoolSigner.SignVerdict` marshals the Verdict to compute the signed record's `output_digest`; the Verdict has **no `json:` tags** (Go-default capitalized keys). Adding the two new fields (Piece 0) is allowed and deterministic; renaming/tagging existing fields is NOT (it would change every signed digest). The CLI decodes with matching capitalized keys.
- **Off by default / admin only:** unchanged. The pool tool registers only when `Options.AdvPool != nil`.
- **CLI-docs drift gate:** `corral certify -h` output changes → run `bash scripts/gen-cli-docs.sh` and commit its output (site deploy enforces `--check`).
- **Exit codes (the `--adversarial` command):** `0` certified · `3` needs-review · `2` usage error · `1` infra/timeout error.
- **Verify gate before every task's commit:** `gofmt -l` on touched files (must be empty), `go build ./...`, `go test ./<touched package>/...`, and `bash scripts/check-security.sh` (must exit 0). Report the actual commands + output.

---

### Task 1: Piece 0 — carry the signed record id on the Verdict

**Files:**
- Modify: `internal/advpool/driver.go` (the `Verdict` struct ~line 66; `tickAggregate` ~line 431)
- Test: `internal/advpool/driver_test.go`

**Interfaces:**
- Consumes: `Signer.SignVerdict(ctx, v) (recordID int64, head string, err error)` (already defined ~line 53).
- Produces: `Verdict` gains `RecordID int64` and `RecordHead string`, populated after signing. Task 2 reads them via `RunStatus`; Task 4's `advVerdict` decodes them.

- [ ] **Step 1: Write the failing test**

Add to `internal/advpool/driver_test.go`. This asserts the record id from the Signer lands on the stored verdict. Use the existing test harness (`newTestDriver`, the fake queue/scorer/validator/signer already in this file — inspect the file for their exact names and reuse them; do not invent new fakes). The fake Signer must return a non-zero id/head. If the existing fake Signer returns `(0, "", nil)`, extend it to return a fixed `(41, "head41", nil)` and update any existing assertions that depended on the old return.

```go
func TestVerdictCarriesSignedRecordID(t *testing.T) {
	// Drive a run to convergence (reuse the helper that already does this in
	// this file — e.g. the full-run integration test's setup). After Tick
	// returns a non-nil Verdict, assert the record id/head from the fake
	// Signer are on it.
	v := /* the *Verdict returned by the converging Tick */
	if v.RecordID != 41 {
		t.Fatalf("RecordID = %d, want 41 (from the fake Signer)", v.RecordID)
	}
	if v.RecordHead != "head41" {
		t.Fatalf("RecordHead = %q, want head41", v.RecordHead)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/advpool/ -run TestVerdictCarriesSignedRecordID -v`
Expected: FAIL — `RecordID = 0, want 41` (field doesn't exist yet → first a compile error `v.RecordID undefined`, then after Step 3's struct change, the value assertion).

- [ ] **Step 3: Add the fields and populate them**

In the `Verdict` struct, append:

```go
	Status          string // certified | needs-review
	RecordID        int64  // the signed build-record id (0 if signing skipped/failed)
	RecordHead      string // the record's ledger head
}
```

In `tickAggregate`, replace the discard-the-return sign block with a capture that sets the fields **before** `run.verdict = &v` (at sign time `v.RecordID` is still 0, so the digest is over the scored fields — deterministic and honest; the record cannot contain its own id):

```go
	if d.Signer != nil {
		recordID, head, serr := d.Signer.SignVerdict(ctx, v)
		if serr != nil {
			return nil, fmt.Errorf("advpool: sign verdict: %w", serr)
		}
		v.RecordID = recordID
		v.RecordHead = head
		// Gate-earned fitness (soundness #6): the leaderboard is fed ONLY from a
		// CERTIFIED verdict — a run parked for human review has not earned fitness
		// for anyone yet. A needs-review record is still signed (evidence), but no
		// model gets leaderboard credit until the gate actually certified the run.
		if d.Leaderboard != nil && v.Status == StatusCertified {
			d.feedLeaderboard(v, run.testWriterMoot)
		}
	}

	run.verdict = &v
	return run.verdict, nil
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/advpool/ -v`
Expected: PASS (the new test + all existing advpool tests, including any whose fake-Signer return you changed).

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/advpool/driver.go internal/advpool/driver_test.go
go build ./... && go test ./internal/advpool/... && bash scripts/check-security.sh
git add internal/advpool/driver.go internal/advpool/driver_test.go
git commit -m "advpool: carry the signed record id/head on the Verdict"
```

---

### Task 2: Piece 1 — `Driver.RunStatus` getter

**Files:**
- Modify: `internal/advpool/driver.go` (near the other `func (d *Driver)` methods; the `runs` map is guarded by `d.mu`)
- Test: `internal/advpool/driver_test.go`

**Interfaces:**
- Consumes: `d.runs map[int64]*runState` (guarded by `d.mu`); `runState.verdict *Verdict` (set by `tickAggregate` in Task 1).
- Produces: `type RunState struct { Converged bool; Verdict *Verdict }` and `func (d *Driver) RunStatus(missionID int64) (RunState, bool)`. Task 3's `AdvPoolRuntime.RunStatus` delegates here.

- [ ] **Step 1: Write the failing test**

```go
func TestRunStatusUnknownConvergedRunning(t *testing.T) {
	d, _ := newTestDriver(t) // reuse this file's constructor; adjust to its real signature/returns

	// Unknown id.
	if st, found := d.RunStatus(999); found || st.Converged {
		t.Fatalf("unknown id: got found=%v converged=%v, want false/false", found, st.Converged)
	}

	// Start a run but do not converge it: found, not converged, nil verdict.
	// (Use the same StartRun call the other tests use — mission id, RunSpec,
	// sigs. Pick a mission id constant, e.g. int64(7).)
	mustStartRun(t, d, 7) // reuse/inline the file's existing start helper
	if st, found := d.RunStatus(7); !found || st.Converged || st.Verdict != nil {
		t.Fatalf("mid-run: found=%v converged=%v verdict=%v, want true/false/nil", found, st.Converged, st.Verdict)
	}

	// Drive it to convergence, then RunStatus reports Converged + the Verdict.
	v := driveToConvergence(t, d, 7) // reuse the file's full-run helper
	st, found := d.RunStatus(7)
	if !found || !st.Converged || st.Verdict == nil {
		t.Fatalf("converged: found=%v converged=%v verdict=%v, want true/true/non-nil", found, st.Converged, st.Verdict)
	}
	if st.Verdict.Status != v.Status {
		t.Fatalf("Verdict.Status = %q, want %q", st.Verdict.Status, v.Status)
	}
}
```

Note: the helper names above (`newTestDriver`, `mustStartRun`, `driveToConvergence`) are illustrative — use whatever this file already provides to start and converge a run; do not add new fakes if the file has them.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/advpool/ -run TestRunStatusUnknownConvergedRunning -v`
Expected: FAIL to compile — `d.RunStatus undefined`.

- [ ] **Step 3: Implement `RunStatus` and `RunState`**

Add near the top-level types (after `Verdict`):

```go
// RunState is the observable status of one run: Converged is true once the run
// has a terminal Verdict, and Verdict is non-nil exactly when Converged is true.
type RunState struct {
	Converged bool
	Verdict   *Verdict
}
```

Add the method (mirrors the locking the other Driver methods use):

```go
// RunStatus reports whether missionID's run has converged, and its Verdict if
// so. found is false when the driver has no such run (unknown or never-started
// id). A run is retained in d.runs after convergence (never deleted), so a
// converged verdict stays queryable after the runtime frees the active slot —
// which is exactly when a caller polls for it.
func (d *Driver) RunStatus(missionID int64) (RunState, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	run, ok := d.runs[missionID]
	if !ok {
		return RunState{}, false
	}
	return RunState{Converged: run.verdict != nil, Verdict: run.verdict}, true
}
```

Confirm the Driver's mutex field is named `mu` (grep `d.mu` in driver.go — it is used by `Tick`/`StartRun`); match it.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/advpool/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/advpool/driver.go internal/advpool/driver_test.go
go build ./... && go test ./internal/advpool/... && bash scripts/check-security.sh
git add internal/advpool/driver.go internal/advpool/driver_test.go
git commit -m "advpool: Driver.RunStatus getter over the retained run verdict"
```

---

### Task 3: Piece 2 — `get_adversarial_run` MCP tool + `AdvPoolRuntime.RunStatus`

**Files:**
- Modify: `internal/brain/advpool.go` (`AdvPoolRuntime` ~line 340; `registerAdvPoolTools` ~line 538)
- Test: `internal/brain/advpool_test.go`

**Interfaces:**
- Consumes: `advpool.Driver.RunStatus` (Task 2); `rt.driver` on `AdvPoolRuntime`; `opts.isHumanAdmin(req)`; `errAdminOnly`; `advpool.Verdict` (Task 1).
- Produces: `AdvPoolRuntime.RunStatus(runID int64) (advpool.RunState, bool)`; MCP tool `get_adversarial_run` with input `AdvPoolQuery{RunID}` → output `AdvPoolStatusOut{RunID, Found, Converged, Verdict}`. Task 4's real client calls this tool.

- [ ] **Step 1: Write the failing test**

Inspect `internal/brain/advpool_test.go` for how it constructs an `AdvPoolRuntime` with a real/fake `advpool.Driver` (there are existing pool tests). Add:

```go
func TestAdvPoolRuntimeRunStatusDelegates(t *testing.T) {
	// Build a runtime whose driver has a started+converged run at id 7
	// (reuse the file's helper that wires a driver + StartRun; drive Tick to a
	// verdict). Then:
	rt := /* the runtime */
	st, found := rt.RunStatus(7)
	if !found || !st.Converged || st.Verdict == nil {
		t.Fatalf("RunStatus(7) = %+v found=%v, want converged with a verdict", st, found)
	}
	if _, found := rt.RunStatus(999); found {
		t.Fatalf("RunStatus(999): found=true, want false for an unknown id")
	}
}
```

If the file lacks a convenient converged-run helper, assert the simpler contract instead: a runtime with a driver that has one non-converged run returns `found=true, Converged=false`, and an unknown id returns `found=false`. (The full-convergence path is already covered by Task 2's driver test; this test only needs to prove the runtime delegates.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/brain/ -run TestAdvPoolRuntimeRunStatusDelegates -v`
Expected: FAIL to compile — `rt.RunStatus undefined`.

- [ ] **Step 3: Add `AdvPoolRuntime.RunStatus`**

The driver owns the run state under its own lock, so no `rt.mu` is needed here (`rt.mu` guards only `activeID`/`tickErrors`):

```go
// RunStatus reports a run's status/verdict by id, delegating to the driver
// (which retains converged runs). Used by the get_adversarial_run tool so an
// external caller can poll an async run to convergence.
func (rt *AdvPoolRuntime) RunStatus(runID int64) (advpool.RunState, bool) {
	return rt.driver.RunStatus(runID)
}
```

- [ ] **Step 4: Add the tool types and registration**

Add the types near `AdvPoolRunSpec`/`AdvPoolRunOut`:

```go
// AdvPoolQuery is get_adversarial_run's input: a run id from start_adversarial_run.
type AdvPoolQuery struct {
	RunID int64 `json:"run_id" jsonschema:"the run id returned by start_adversarial_run"`
}

// AdvPoolStatusOut is get_adversarial_run's output: a run's status and, once
// converged, its signed Verdict. Verdict is nil while the run is still ticking
// or for an unknown id.
type AdvPoolStatusOut struct {
	RunID     int64            `json:"run_id"`
	Found     bool             `json:"found"`
	Converged bool             `json:"converged"`
	Verdict   *advpool.Verdict `json:"verdict,omitempty"`
}
```

In `registerAdvPoolTools`, after the `start_adversarial_run` registration, add:

```go
	mcp.AddTool(s, &mcp.Tool{Name: "get_adversarial_run",
		Description: "ADMIN: query an adversarial-pool run's status and (once converged) its signed verdict, by the run id returned from start_adversarial_run."},
		func(_ context.Context, req *mcp.CallToolRequest, in AdvPoolQuery) (*mcp.CallToolResult, AdvPoolStatusOut, error) {
			if !opts.isHumanAdmin(req) {
				return nil, AdvPoolStatusOut{}, errAdminOnly
			}
			st, found := rt.RunStatus(in.RunID)
			return nil, AdvPoolStatusOut{
				RunID:     in.RunID,
				Found:     found,
				Converged: st.Converged,
				Verdict:   st.Verdict,
			}, nil
		})
```

- [ ] **Step 5: Add an admin-gate test**

Add a test asserting a non-admin request is refused with `errAdminOnly`. Reuse whatever pattern `advpool_test.go` (or a sibling brain test) already uses to build a `*mcp.CallToolRequest` and an `Options` with `isHumanAdmin` false — do not invent a new admin-faking mechanism. If the existing tests gate `start_adversarial_run`, mirror that exact setup for `get_adversarial_run`.

```go
func TestGetAdversarialRunRefusesNonAdmin(t *testing.T) {
	// Build Options where isHumanAdmin(req) is false (same construction the
	// start_adversarial_run admin test uses), register the tools on a server,
	// call get_adversarial_run, assert the error is errAdminOnly.
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/brain/ -run 'TestAdvPoolRuntimeRunStatusDelegates|TestGetAdversarialRunRefusesNonAdmin' -v` then `go test ./internal/brain/...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
gofmt -w internal/brain/advpool.go internal/brain/advpool_test.go
go build ./... && go test ./internal/brain/... && bash scripts/check-security.sh
git add internal/brain/advpool.go internal/brain/advpool_test.go
git commit -m "brain: get_adversarial_run MCP tool + AdvPoolRuntime.RunStatus"
```

---

### Task 4: Piece 3 (client seam) — wire types + injectable `advPoolClient`

**Files:**
- Create: `cmd/corral/certify_adversarial.go`
- Test: `cmd/corral/certify_adversarial_test.go`

**Interfaces:**
- Consumes: `internal/brainclient` (`Dial`, `Client.CallTool`, `FirstText`, `Client.Close`); `brainToken()` (in `cmd/corral/certify.go`); the tool contracts from Task 3 (`start_adversarial_run` in `AdvPoolRunSpec` shape / `AdvPoolRunOut{run_id}`; `get_adversarial_run` in `AdvPoolQuery{run_id}` / `AdvPoolStatusOut`).
- Produces: `advStartSpec`, `advVerdict`, `advFinding`, `advStatus`; the `advPoolClient` interface; `mcpAdvClient` (real impl). Task 5 consumes `advPoolClient` + these types.

**Wire-shape note:** `advpool.Verdict` has **no `json:` tags**, so `get_adversarial_run` serializes it with Go-default **capitalized** keys (`DevKillRate`, `Survivors`, …); `VacuousFindings` is `[]queue.Finding` whose elements *do* carry lowercase tags (`type`, `severity`, …). The decode structs below match both. Do not "fix" the capitalization — it must mirror what the brain marshals.

- [ ] **Step 1: Write the failing test (decode a canned tool payload)**

```go
package main

import (
	"encoding/json"
	"testing"
)

func TestAdvVerdictDecodesToolPayload(t *testing.T) {
	// Exactly what get_adversarial_run marshals (advpool.Verdict has no json
	// tags -> capitalized keys; VacuousFindings elements use queue.Finding's
	// lowercase tags).
	payload := `{
	  "run_id": 7, "found": true, "converged": true,
	  "verdict": {
	    "Repo": "pdbethke/corralai", "Commit": "88b6ff7",
	    "DevKillRate": 0.5, "MutantsTotal": 8, "Survivors": 4, "ProvenMissed": 2,
	    "VacuousFindings": [
	      {"type": "note", "severity": "high", "target": "TestValidatePassword",
	       "evidence": "calls ValidatePassword without checking its input"}
	    ],
	    "ModelsByRole": {"test-writer": "qwen2.5-coder:7b", "test-critic": "llama3.2:3b"},
	    "Status": "needs-review", "RecordID": 41, "RecordHead": "head41"
	  }
	}`
	var st advStatus
	if err := json.Unmarshal([]byte(payload), &st); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !st.Converged || st.Verdict == nil {
		t.Fatalf("converged=%v verdict=%v", st.Converged, st.Verdict)
	}
	v := st.Verdict
	if v.DevKillRate != 0.5 || v.MutantsTotal != 8 || v.Survivors != 4 || v.ProvenMissed != 2 {
		t.Fatalf("numbers wrong: %+v", v)
	}
	if v.Status != "needs-review" || v.RecordID != 41 || v.RecordHead != "head41" {
		t.Fatalf("status/record wrong: %+v", v)
	}
	if len(v.VacuousFindings) != 1 || v.VacuousFindings[0].Target != "TestValidatePassword" {
		t.Fatalf("findings wrong: %+v", v.VacuousFindings)
	}
	if v.ModelsByRole["test-writer"] != "qwen2.5-coder:7b" {
		t.Fatalf("models wrong: %+v", v.ModelsByRole)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/corral/ -run TestAdvVerdictDecodesToolPayload -v`
Expected: FAIL to compile — `advStatus`/`advVerdict` undefined.

- [ ] **Step 3: Implement the wire types and the client seam**

Create `cmd/corral/certify_adversarial.go`:

```go
// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/pdbethke/corralai/internal/brainclient"
)

// advFinding is the subset of a queue.Finding the verdict render shows. The
// tags match queue.Finding's own (lowercase) wire tags.
type advFinding struct {
	Type          string `json:"type"`
	Severity      string `json:"severity"`
	Target        string `json:"target"`
	Evidence      string `json:"evidence"`
	ReporterModel string `json:"reporter_model"`
}

// advVerdict mirrors advpool.Verdict on the wire. advpool.Verdict has NO json
// tags, so its keys are the Go-default CAPITALIZED field names — matched here
// verbatim. Changing these breaks decoding.
type advVerdict struct {
	Repo            string            `json:"Repo"`
	Commit          string            `json:"Commit"`
	DevKillRate     float64           `json:"DevKillRate"`
	MutantsTotal    int               `json:"MutantsTotal"`
	Survivors       int               `json:"Survivors"`
	ProvenMissed    int               `json:"ProvenMissed"`
	VacuousFindings []advFinding      `json:"VacuousFindings"`
	ModelsByRole    map[string]string `json:"ModelsByRole"`
	Status          string            `json:"Status"`
	RecordID        int64             `json:"RecordID"`
	RecordHead      string            `json:"RecordHead"`
}

// advStatus mirrors brain.AdvPoolStatusOut (get_adversarial_run's output).
type advStatus struct {
	RunID     int64       `json:"run_id"`
	Found     bool        `json:"found"`
	Converged bool        `json:"converged"`
	Verdict   *advVerdict `json:"verdict"`
}

// advStartSpec mirrors brain.AdvPoolRunSpec (start_adversarial_run's input).
type advStartSpec struct {
	Repo        string `json:"repo"`
	Commit      string `json:"commit"`
	Goal        string `json:"goal"`
	CodePath    string `json:"code_path"`
	Code        string `json:"code"`
	DevTestPath string `json:"dev_test_path"`
	DevTestCode string `json:"dev_test_code"`
	TestCmd     string `json:"test_cmd"`
	NMutants    int    `json:"n_mutants,omitempty"`
}

// advPoolClient triggers and polls an adversarial-pool run over the brain's
// MCP tools. Injected so runCertifyAdversarial is testable without a brain.
type advPoolClient interface {
	StartRun(ctx context.Context, brainURL string, spec advStartSpec) (runID int64, err error)
	RunStatus(ctx context.Context, brainURL string, runID int64) (advStatus, error)
}

// mcpAdvClient is advPoolClient backed by real MCP calls, dialing the brain
// fresh per call with a token from the keystore (mirrors mcpPoster).
type mcpAdvClient struct{}

func (mcpAdvClient) call(ctx context.Context, brainURL, tool string, args map[string]any) (string, error) {
	token, err := brainToken()
	if err != nil {
		return "", fmt.Errorf("resolve brain token: %w", err)
	}
	cl, err := brainclient.Dial(ctx, brainURL, token)
	if err != nil {
		return "", err
	}
	defer func() { _ = cl.Close() }()
	res, err := cl.CallTool(ctx, tool, args)
	if err != nil {
		return "", err
	}
	text := brainclient.FirstText(res)
	if res.IsError {
		msg := text
		if msg == "" {
			msg = tool + " reported an error"
		}
		return "", fmt.Errorf("%s", msg)
	}
	return text, nil
}

func (c mcpAdvClient) StartRun(ctx context.Context, brainURL string, spec advStartSpec) (int64, error) {
	args := map[string]any{
		"repo": spec.Repo, "commit": spec.Commit, "goal": spec.Goal,
		"code_path": spec.CodePath, "code": spec.Code,
		"dev_test_path": spec.DevTestPath, "dev_test_code": spec.DevTestCode,
		"test_cmd": spec.TestCmd,
	}
	if spec.NMutants > 0 {
		args["n_mutants"] = spec.NMutants
	}
	text, err := c.call(ctx, brainURL, "start_adversarial_run", args)
	if err != nil {
		return 0, err
	}
	var out struct {
		RunID int64 `json:"run_id"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return 0, fmt.Errorf("decoding start_adversarial_run response: %w", err)
	}
	return out.RunID, nil
}

func (c mcpAdvClient) RunStatus(ctx context.Context, brainURL string, runID int64) (advStatus, error) {
	text, err := c.call(ctx, brainURL, "get_adversarial_run", map[string]any{"run_id": runID})
	if err != nil {
		return advStatus{}, err
	}
	var st advStatus
	if err := json.Unmarshal([]byte(text), &st); err != nil {
		return advStatus{}, fmt.Errorf("decoding get_adversarial_run response: %w", err)
	}
	return st, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/corral/ -run TestAdvVerdictDecodesToolPayload -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w cmd/corral/certify_adversarial.go cmd/corral/certify_adversarial_test.go
go build ./... && go test ./cmd/corral/... && bash scripts/check-security.sh
git add cmd/corral/certify_adversarial.go cmd/corral/certify_adversarial_test.go
git commit -m "corral certify: advPoolClient seam + get/start wire types"
```

---

### Task 5: Piece 3 (command) — `runCertifyAdversarial`, render, dispatch

**Files:**
- Modify: `cmd/corral/certify_adversarial.go` (add the command + render); `cmd/corral/certify.go` (one dispatch line in `runCertify`); `cmd/corral/main.go` if the dispatch needs the real client (it constructs `mcpAdvClient{}` directly, so likely no main.go change — confirm).
- Test: `cmd/corral/certify_adversarial_test.go`
- Docs: `docs/cli/` (regenerated)

**Interfaces:**
- Consumes: `advPoolClient`, `advStartSpec`, `advStatus`, `advVerdict` (Task 4); `splitCertifyArgs`, `cmdRunner`/`realRunner`, `brainToken` (existing in `certify.go`); `os.ReadFile`; `flag`.
- Produces: `runCertifyAdversarial(args []string, client advPoolClient, run cmdRunner, sleep func(time.Duration), stdout, stderr io.Writer) int`; `renderAdvVerdict(w io.Writer, codePath string, v advVerdict)`; a dispatch branch in `runCertify`.

- [ ] **Step 1: Write the failing tests (behavior + render + exit codes)**

```go
package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

// fakeAdvClient scripts StartRun + a sequence of RunStatus results.
type fakeAdvClient struct {
	startErr   error
	runID      int64
	spec       advStartSpec // captured
	statuses   []advStatus  // returned in order; last one repeats
	statusErr  error
	statusCall int
}

func (f *fakeAdvClient) StartRun(_ context.Context, _ string, spec advStartSpec) (int64, error) {
	f.spec = spec
	if f.startErr != nil {
		return 0, f.startErr
	}
	return f.runID, nil
}
func (f *fakeAdvClient) RunStatus(_ context.Context, _ string, _ int64) (advStatus, error) {
	if f.statusErr != nil {
		return advStatus{}, f.statusErr
	}
	i := f.statusCall
	if i >= len(f.statuses) {
		i = len(f.statuses) - 1
	}
	f.statusCall++
	return f.statuses[i], nil
}

func noSleep(time.Duration) {}

// gitStubRunner satisfies cmdRunner returning canned git context; RunCommand
// is unused by the adversarial path.
type gitStubRunner struct{}

func (gitStubRunner) GitOutput(args ...string) (string, error) {
	switch strings.Join(args, " ") {
	case "config --get remote.origin.url":
		return "pdbethke/corralai", nil
	case "rev-parse HEAD":
		return "88b6ff7", nil
	}
	return "", nil
}
func (gitStubRunner) GitVerifyCommit(string) (string, bool, error) { return "", false, nil }
func (gitStubRunner) RunCommand([]string, io.Writer, io.Writer) (int, time.Duration, []byte, error) {
	return 0, 0, nil, nil
}

func certifiedStatus() advStatus {
	return advStatus{RunID: 7, Found: true, Converged: true, Verdict: &advVerdict{
		Repo: "pdbethke/corralai", Commit: "88b6ff7", DevKillRate: 1.0,
		MutantsTotal: 6, Survivors: 0, ProvenMissed: 0,
		ModelsByRole: map[string]string{"mutant-generator": "qwen2.5-coder:7b", "test-writer": "qwen2.5-coder:7b", "test-critic": "llama3.2:3b"},
		Status: "certified", RecordID: 41, RecordHead: "head41",
	}}
}

func writeTmpFiles(t *testing.T) (code, test string) {
	t.Helper()
	dir := t.TempDir()
	code = dir + "/fence.go"
	test = dir + "/fence_test.go"
	if err := os.WriteFile(code, []byte("package fence\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(test, []byte("package fence\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return code, test
}

func TestAdversarialCertifiedExitsZero(t *testing.T) {
	code, _ := writeTmpFiles(t) // sibling _test.go exists in the same dir
	f := &fakeAdvClient{runID: 7, statuses: []advStatus{certifiedStatus()}}
	var out, errBuf bytes.Buffer
	args := []string{"--adversarial", "--brain", "http://b", "--code", code, "--goal", "neutralize the fence", "--poll", "1ms", "--", "go", "test", "./..."}
	rc := runCertifyAdversarial(args, f, gitStubRunner{}, noSleep, &out, &errBuf)
	if rc != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%s", rc, errBuf.String())
	}
	s := out.String()
	if !strings.Contains(s, "CERTIFIED") || !strings.Contains(s, "record 41") {
		t.Fatalf("render missing headline/record:\n%s", s)
	}
	// --test defaulted to the sibling and both files were sent.
	if f.spec.DevTestPath == "" || f.spec.Code == "" || f.spec.DevTestCode == "" {
		t.Fatalf("spec not fully populated: %+v", f.spec)
	}
	if f.spec.TestCmd != "go test ./..." {
		t.Fatalf("TestCmd = %q, want 'go test ./...'", f.spec.TestCmd)
	}
}

func TestAdversarialNeedsReviewExitsThree(t *testing.T) {
	code, _ := writeTmpFiles(t)
	nr := certifiedStatus()
	nr.Verdict.Status = "needs-review"
	nr.Verdict.DevKillRate = 0.5
	nr.Verdict.MutantsTotal = 8
	nr.Verdict.Survivors = 4
	nr.Verdict.ProvenMissed = 2
	nr.Verdict.VacuousFindings = []advFinding{{Type: "note", Severity: "high", Target: "TestValidatePassword", Evidence: "calls ValidatePassword without checking its input"}}
	f := &fakeAdvClient{runID: 7, statuses: []advStatus{nr}}
	var out, errBuf bytes.Buffer
	args := []string{"--adversarial", "--brain", "http://b", "--code", code, "--goal", "g", "--poll", "1ms", "--", "go", "test", "./..."}
	rc := runCertifyAdversarial(args, f, gitStubRunner{}, noSleep, &out, &errBuf)
	if rc != 3 {
		t.Fatalf("exit = %d, want 3", rc)
	}
	s := out.String()
	if strings.Contains(s, "CERTIFIED") {
		t.Fatalf("needs-review must NOT print CERTIFIED:\n%s", s)
	}
	if !strings.Contains(s, "NEEDS-REVIEW") || !strings.Contains(s, "TestValidatePassword") {
		t.Fatalf("render missing needs-review status or the pan:\n%s", s)
	}
}

func TestAdversarialPollsUntilConverged(t *testing.T) {
	code, _ := writeTmpFiles(t)
	running := advStatus{RunID: 7, Found: true, Converged: false}
	f := &fakeAdvClient{runID: 7, statuses: []advStatus{running, running, certifiedStatus()}}
	var out, errBuf bytes.Buffer
	args := []string{"--adversarial", "--brain", "http://b", "--code", code, "--goal", "g", "--poll", "1ms", "--timeout", "10s", "--", "go", "test", "./..."}
	rc := runCertifyAdversarial(args, f, gitStubRunner{}, noSleep, &out, &errBuf)
	if rc != 0 {
		t.Fatalf("exit = %d, want 0 after polling", rc)
	}
	if f.statusCall < 3 {
		t.Fatalf("polled %d times, want >= 3", f.statusCall)
	}
}

func TestAdversarialMissingFlagsUsage(t *testing.T) {
	var out, errBuf bytes.Buffer
	// No --code.
	rc := runCertifyAdversarial([]string{"--adversarial", "--brain", "http://b", "--goal", "g", "--", "go", "test"}, &fakeAdvClient{}, gitStubRunner{}, noSleep, &out, &errBuf)
	if rc != 2 {
		t.Fatalf("missing --code: exit = %d, want 2", rc)
	}
	// No `-- cmd`.
	code, _ := writeTmpFiles(t)
	rc = runCertifyAdversarial([]string{"--adversarial", "--brain", "http://b", "--code", code, "--goal", "g"}, &fakeAdvClient{}, gitStubRunner{}, noSleep, &out, &errBuf)
	if rc != 2 {
		t.Fatalf("missing -- cmd: exit = %d, want 2", rc)
	}
}

func TestAdversarialTimeoutExitsOne(t *testing.T) {
	code, _ := writeTmpFiles(t)
	running := advStatus{RunID: 7, Found: true, Converged: false}
	f := &fakeAdvClient{runID: 7, statuses: []advStatus{running}}
	var out, errBuf bytes.Buffer
	// --timeout 0 => the deadline is already past after StartRun; first
	// not-converged poll trips the timeout.
	args := []string{"--adversarial", "--brain", "http://b", "--code", code, "--goal", "g", "--poll", "1ms", "--timeout", "0s", "--", "go", "test"}
	rc := runCertifyAdversarial(args, f, gitStubRunner{}, noSleep, &out, &errBuf)
	if rc != 1 {
		t.Fatalf("timeout: exit = %d, want 1", rc)
	}
	if !strings.Contains(errBuf.String(), "7") {
		t.Fatalf("timeout message should name the run id for re-query:\n%s", errBuf.String())
	}
}

func TestAdversarialStartErrorExitsOne(t *testing.T) {
	code, _ := writeTmpFiles(t)
	f := &fakeAdvClient{startErr: errors.New("boom")}
	var out, errBuf bytes.Buffer
	args := []string{"--adversarial", "--brain", "http://b", "--code", code, "--goal", "g", "--poll", "1ms", "--", "go", "test"}
	if rc := runCertifyAdversarial(args, f, gitStubRunner{}, noSleep, &out, &errBuf); rc != 1 {
		t.Fatalf("start error: exit = %d, want 1", rc)
	}
}
```

Add the needed imports to the test file (`errors`, `io`, `os`). Confirm `cmdRunner`'s `RunCommand` signature in `certify.go` and match `gitStubRunner` to it exactly.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/corral/ -run TestAdversarial -v`
Expected: FAIL to compile — `runCertifyAdversarial` undefined.

- [ ] **Step 3: Implement `runCertifyAdversarial` + `renderAdvVerdict`**

Append to `cmd/corral/certify_adversarial.go` (add imports: `flag`, `io`, `os`, `path/filepath`, `sort`, `strings`, `time`):

```go
// runCertifyAdversarial implements `corral certify --adversarial`: it fires an
// adversarial-pool run against a code+dev-test pair on the brain, polls to
// convergence, renders the signed verdict, and exits by status (0 certified,
// 3 needs-review, 2 usage, 1 infra/timeout). sleep is injected so tests don't
// wait real wall-clock between polls.
func runCertifyAdversarial(args []string, client advPoolClient, run cmdRunner, sleep func(time.Duration), stdout, stderr io.Writer) int {
	flagArgs, checkArgv := splitCertifyArgs(args)

	fs := flag.NewFlagSet("certify --adversarial", flag.ContinueOnError)
	fs.SetOutput(stderr)
	_ = fs.Bool("adversarial", false, "run the adversarial testing pool (this mode)")
	brainURL := fs.String("brain", os.Getenv("CORRAL_BRAIN"), "brain MCP endpoint (or $CORRAL_BRAIN)")
	codePath := fs.String("code", "", "repo-relative path of the code under review (required)")
	testPath := fs.String("test", "", "repo-relative path of the dev's test (default: the _test.go sibling of --code)")
	goal := fs.String("goal", "", "the correctness/security goal the code must satisfy (required)")
	nMutants := fs.Int("n-mutants", 0, "how many seeded-violation mutants (default 5, brain clamps to 20)")
	poll := fs.Duration("poll", 5*time.Second, "how often to poll the run's status")
	timeout := fs.Duration("timeout", 10*time.Minute, "give up waiting for convergence after this long")
	repoFlag := fs.String("repo", "", "repository (default: git remote.origin.url)")
	commitFlag := fs.String("commit", "", "commit sha (default: git rev-parse HEAD)")
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}

	if strings.TrimSpace(*codePath) == "" {
		fmt.Fprintln(stderr, "corral certify --adversarial: --code is required")
		return 2
	}
	if strings.TrimSpace(*goal) == "" {
		fmt.Fprintln(stderr, "corral certify --adversarial: --goal is required")
		return 2
	}
	if len(checkArgv) == 0 {
		fmt.Fprintln(stderr, "corral certify --adversarial: usage: corral certify --adversarial --code <path> --goal <text> [--test <path>] -- <test command>")
		return 2
	}
	if strings.TrimSpace(*brainURL) == "" {
		fmt.Fprintln(stderr, "corral certify --adversarial: --brain <url> (or $CORRAL_BRAIN) is required")
		return 2
	}

	tp := strings.TrimSpace(*testPath)
	if tp == "" {
		tp = siblingTestPath(*codePath)
	}

	code, err := os.ReadFile(*codePath) // #nosec G304 -- operator-supplied path to the file under review
	if err != nil {
		fmt.Fprintf(stderr, "corral certify --adversarial: reading --code %s: %v\n", *codePath, err)
		return 2
	}
	devTest, err := os.ReadFile(tp) // #nosec G304 -- operator-supplied (or sibling-derived) test path
	if err != nil {
		fmt.Fprintf(stderr, "corral certify --adversarial: reading test %s: %v (pass --test to override)\n", tp, err)
		return 2
	}

	repo := strings.TrimSpace(*repoFlag)
	if repo == "" {
		if v, gerr := run.GitOutput("config", "--get", "remote.origin.url"); gerr == nil {
			repo = v
		}
	}
	commit := strings.TrimSpace(*commitFlag)
	if commit == "" {
		if v, gerr := run.GitOutput("rev-parse", "HEAD"); gerr == nil {
			commit = v
		}
	}

	spec := advStartSpec{
		Repo: repo, Commit: commit, Goal: strings.TrimSpace(*goal),
		CodePath: *codePath, Code: string(code),
		DevTestPath: tp, DevTestCode: string(devTest),
		TestCmd:  strings.Join(checkArgv, " "),
		NMutants: *nMutants,
	}

	ctx := context.Background()
	runID, err := client.StartRun(ctx, *brainURL, spec)
	if err != nil {
		fmt.Fprintf(stderr, "corral certify --adversarial: starting run: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "started adversarial run %d — grading %s against its own tests…\n", runID, *codePath)

	deadline := time.Now().Add(*timeout)
	start := time.Now()
	for {
		st, err := client.RunStatus(ctx, *brainURL, runID)
		if err != nil {
			fmt.Fprintf(stderr, "corral certify --adversarial: polling run %d: %v\n", runID, err)
			return 1
		}
		if st.Converged && st.Verdict != nil {
			renderAdvVerdict(stdout, *codePath, *st.Verdict)
			if st.Verdict.Status == "certified" {
				return 0
			}
			return 3
		}
		if time.Now().After(deadline) {
			fmt.Fprintf(stderr, "corral certify --adversarial: run %d did not converge within %s — re-query later with the brain's get_adversarial_run (run_id %d)\n", runID, *timeout, runID)
			return 1
		}
		fmt.Fprintf(stdout, "  … still running (elapsed %s)\n", time.Since(start).Round(time.Second))
		sleep(*poll)
	}
}

// siblingTestPath derives foo.go -> foo_test.go in the same directory.
func siblingTestPath(codePath string) string {
	ext := filepath.Ext(codePath)
	base := strings.TrimSuffix(codePath, ext)
	return base + "_test" + ext
}

// renderAdvVerdict prints the legible verdict block — the demo artifact. It
// prints exactly what the brain signed; it never upgrades a needs-review to
// CERTIFIED, and shows survivors/proven_missed and the test-critic's pan even
// when unflattering.
func renderAdvVerdict(w io.Writer, codePath string, v advVerdict) {
	status := "NEEDS-REVIEW"
	if v.Status == "certified" {
		status = "CERTIFIED"
	}
	killed := v.MutantsTotal - v.Survivors
	commit := v.Commit
	if len(commit) > 7 {
		commit = commit[:7]
	}
	fmt.Fprintf(w, "\nadversarial verdict — %s @ %s\n", codePath, commit)
	fmt.Fprintf(w, "  status:        %-12s (dev suite killed %d/%d mutants)\n", status, killed, v.MutantsTotal)
	fmt.Fprintf(w, "  dev_kill_rate: %.2f\n", v.DevKillRate)
	fmt.Fprintf(w, "  survivors:     %d\n", v.Survivors)
	fmt.Fprintf(w, "  proven_missed: %d\n", v.ProvenMissed)
	if len(v.VacuousFindings) == 0 {
		fmt.Fprintln(w, "  vacuous tests: none flagged")
	} else {
		fmt.Fprintf(w, "  vacuous tests: %d flagged\n", len(v.VacuousFindings))
	}
	fmt.Fprintf(w, "  models:        %s\n", formatModels(v.ModelsByRole))
	if v.RecordID != 0 {
		fmt.Fprintf(w, "  signed:        record %d  (verify offline: corral certify verify <record>)\n", v.RecordID)
	} else {
		fmt.Fprintln(w, "  signed:        (signing failed — no record id)")
	}
	for _, f := range v.VacuousFindings {
		sev := f.Severity
		if sev == "" {
			sev = "note"
		}
		fmt.Fprintf(w, "      • [%s] %s: %s\n", sev, f.Target, f.Evidence)
	}
}

// formatModels renders ModelsByRole deterministically (sorted by role).
func formatModels(m map[string]string) string {
	if len(m) == 0 {
		return "(none recorded)"
	}
	roles := make([]string, 0, len(m))
	for r := range m {
		roles = append(roles, r)
	}
	sort.Strings(roles)
	parts := make([]string, 0, len(roles))
	for _, r := range roles {
		parts = append(parts, r+"="+m[r])
	}
	return strings.Join(parts, "  ")
}
```

- [ ] **Step 4: Wire the dispatch in `runCertify`**

In `cmd/corral/certify.go`, at the top of `runCertify` (after the `verify`/`pubkey` sub-subcommand checks, before `splitCertifyArgs`), add:

```go
	// `corral certify --adversarial ...` is a distinct mode: trigger the
	// adversarial testing pool on the brain and poll to a signed verdict.
	if hasFlag(args, "--adversarial") {
		return runCertifyAdversarial(args, mcpAdvClient{}, realRunner{}, time.Sleep, stdout, stderr)
	}
```

And add the helper (in `certify.go` or `certify_adversarial.go`):

```go
// hasFlag reports whether name appears as a bare token in args, stopping at
// the first bare "--" (so it never matches inside the checked command's argv).
func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == name || strings.HasPrefix(a, name+"=") {
			return true
		}
	}
	return false
}
```

Confirm `time` and `strings` are imported in whichever file `hasFlag`/the dispatch lands in (both are already imported in `certify.go`).

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./cmd/corral/ -run 'TestAdversarial|TestAdvVerdict' -v`
Expected: PASS (all Task 4 + Task 5 tests).

- [ ] **Step 6: Full build + suite + security**

Run: `go build ./... && go test ./cmd/corral/... ./internal/advpool/... ./internal/brain/... && bash scripts/check-security.sh`
Expected: all PASS, check-security exit 0.

- [ ] **Step 7: Regenerate CLI docs (the drift gate)**

Run: `bash scripts/gen-cli-docs.sh`
Then confirm no drift remains: `bash scripts/gen-cli-docs.sh --check` (expected: clean).
This captures the new `--adversarial` flags into the generated CLI reference the site deploy checks.

- [ ] **Step 8: Commit**

```bash
gofmt -w cmd/corral/certify_adversarial.go cmd/corral/certify_adversarial_test.go cmd/corral/certify.go
go build ./... && go test ./cmd/corral/... && bash scripts/check-security.sh
git add cmd/corral/certify_adversarial.go cmd/corral/certify_adversarial_test.go cmd/corral/certify.go docs/cli
git commit -m "corral certify --adversarial: trigger the pool, poll to a signed verdict, render it"
```

---

## Self-Review (completed by plan author)

**Spec coverage:** Piece 0 → Task 1; Piece 1 → Task 2; Piece 2 → Task 3; Piece 3 (client) → Task 4; Piece 3 (command + render + dispatch + exit codes + docs) → Task 5. Every spec piece maps to a task.

**Type consistency:** `advStartSpec` keys match `brain.AdvPoolRunSpec`'s json tags (`repo`/`commit`/`goal`/`code_path`/`code`/`dev_test_path`/`dev_test_code`/`test_cmd`/`n_mutants`). `advVerdict` uses CAPITALIZED keys matching `advpool.Verdict`'s tag-less marshaling; `advFinding` uses `queue.Finding`'s lowercase tags. `advStatus` matches `brain.AdvPoolStatusOut` (`run_id`/`found`/`converged`/`verdict`). `RunStatus` returns `(RunState, bool)` consistently across driver and runtime. Exit codes 0/3/2/1 consistent between spec, plan, and tests.

**Placeholder scan:** the test helper names in Tasks 1–3 (`newTestDriver`, `mustStartRun`, `driveToConvergence`) are explicitly flagged as "reuse the file's existing helpers" rather than literal — the implementer must inspect the existing test file. This is deliberate (the plan cannot know the exact helper names without over-specifying against code it hasn't re-read), and each such note tells the implementer exactly what to find and what contract to assert. All production code is complete.

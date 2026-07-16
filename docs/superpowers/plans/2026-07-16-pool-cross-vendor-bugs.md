<!-- SPDX-License-Identifier: Elastic-2.0 -->
# Adversarial pool — cross-vendor multi-worker bug fixes

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development. Steps use `- [ ]`.

**Goal:** Make a multi-worker, multi-vendor pool run correct: a role-scoped worker never runs a task outside its role, and operational errors never pollute the signed verdict.

**Root cause (verified live, cross-vendor run: Claude workers scoped to mutant-generator+test-writer, a Gemini worker scoped to test-critic):**
- Both workers used the same `corral-svc` JWT with plain names, so `identity()` (internal/brain/identity.go:414 — returns the auth **principal** unless the name is in the principal's `namespace/`) collapsed BOTH to one bee, `corral-svc`. (Operator-side: name workers `corral-svc/<name>` for distinct identities — done at run time, not in this plan.)
- **Bug 1 — the self-heal claim path ignores role scoping.** `queue.ClaimNextAs` (internal/queue/store.go:363-396) reissues any `StatusClaimed` row where `claimed_by=bee` and the lease is stale — with NO `role IN (…)` filter (the fresh-claim SELECT at store.go:405-416 HAS the filter). So under a shared bee, the Claude worker self-healed the Gemini worker's expired-lease **test-critic** task and ran it on the wrong vendor → "model unreachable."
- **Bug 2 — operational errors pollute the signed verdict.** On `ErrModelUnreachable`, `handleTaskError` (cmd/corral-agent/main.go:622-637) files a `report_finding` with `type:"note", severity:"high", task_id: <the task>`. `tickAggregate` (internal/advpool/driver.go:505-517) puts EVERY finding whose `TaskID == test-critic task` into `Verdict.VacuousFindings` with no Type filter, and `blockingFindingOpen` (driver.go:586-599) treats any open ≥high finding as blocking — so a model-unreachable hiccup showed as "vacuous tests: 3 flagged" and forced needs-review.

**Tech Stack:** Go 1.26.5; `internal/queue`, `cmd/corral-agent`, `internal/advpool`.

## Global Constraints
- New files start `// SPDX-License-Identifier: Elastic-2.0`. Module `github.com/pdbethke/corralai`, Go 1.26.5.
- **No behavior change for single-worker / correctly-namespaced runs.** The self-heal fix must still reissue a bee's OWN role-matching orphaned task (the legitimate self-heal case).
- **The signed verdict must reflect only audit findings.** Operational events (model-unreachable) are visible to operators but MUST NOT count as critic findings or as a certification-blocking gate signal.
- Verify gate before each commit: `gofmt -l` (empty) on touched files, `go build ./...`, `go test ./<touched package>/... [-race for advpool/queue]`, `bash scripts/check-security.sh` (exit 0).

---

### Task 1: Self-heal claim path respects role scoping

**Files:**
- Modify: `internal/queue/store.go` (`ClaimNextAs` self-heal SELECT ~363-372)
- Test: `internal/queue/store_test.go`

**Interfaces:**
- Consumes: `ClaimNextAs(bee, instance string, roles []string, leaseSeconds float64)`.
- Produces: the self-heal SELECT gains the same `role IN (roles…, '')` filter the fresh-claim SELECT already applies, so a bee only reissues orphaned tasks matching the roles of THIS claim call.

- [ ] **Step 1: Write the failing test**

Read `internal/queue/store_test.go` for the existing claim/self-heal test helpers (there is coverage for orphan self-heal and role scoping — reuse the store constructor + enqueue helpers). Add a test proving the self-heal path is role-scoped:

```go
func TestSelfHealRespectsRoleScope(t *testing.T) {
	s := newTestStore(t) // reuse the file's constructor
	mid := int64(1)
	// Two tasks: one test-critic, one mutant-generator.
	if err := s.Enqueue(mid, []TaskSpec{
		{Key: "test-critic", Role: "test-critic", Title: "critique"},
		{Key: "mutant-generator", Role: "mutant-generator", Title: "mutate"},
	}); err != nil { t.Fatal(err) }
	if _, err := s.PromoteReady(mid); err != nil { t.Fatal(err) }

	// bee "shared" claims the test-critic task with roles=[test-critic].
	tc, err := s.ClaimNextAs("shared", "inst-A", []string{"test-critic"}, 0.001) // ~instant lease
	if err != nil || tc == nil || tc.Role != "test-critic" {
		t.Fatalf("setup claim of test-critic failed: %v %+v", err, tc)
	}
	time.Sleep(20 * time.Millisecond) // let the tiny lease expire

	// Same bee "shared" now polls claiming for roles=[mutant-generator] (a
	// different worker, same principal). It must NOT self-heal the orphaned
	// test-critic task — that's outside the roles it's claiming for. It should
	// instead get the mutant-generator task (fresh) or nothing test-critic.
	got, err := s.ClaimNextAs("shared", "inst-B", []string{"mutant-generator"}, 60)
	if err != nil { t.Fatal(err) }
	if got != nil && got.Role == "test-critic" {
		t.Fatalf("self-heal handed a test-critic task to a mutant-generator claim: %+v", got)
	}
	// And a test-critic-scoped claim by the same bee SHOULD recover it.
	rec, err := s.ClaimNextAs("shared", "inst-A", []string{"test-critic"}, 60)
	if err != nil { t.Fatal(err) }
	if rec == nil || rec.Role != "test-critic" {
		t.Fatalf("role-matching self-heal should recover the orphaned test-critic task, got %+v", rec)
	}
}
```

Confirm `PromoteReady`/lease-expiry semantics against the file's other tests; adjust the tiny-lease approach if the store clamps lease minimums (use the file's clock/lease pattern).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/queue/ -run TestSelfHealRespectsRoleScope -v`
Expected: FAIL — the mutant-generator claim self-heals the test-critic task (returns it).

- [ ] **Step 3: Add the role filter to the self-heal SELECT**

In `ClaimNextAs`, the self-heal SELECT (store.go ~363-372) currently:
```go
	err := tx.QueryRow(
		`SELECT id,mission_id,key,role,title,instruction,depends_on,created_ts,model FROM tasks
		 WHERE status=? AND claimed_by=?
		   AND ((claimed_instance=? AND ?!='') OR claim_expires_ts < ?)
		   AND mission_id NOT IN (SELECT mission_id FROM mission_halts)
		 ORDER BY claimed_ts, id LIMIT 1`,
		StatusClaimed, bee, instance, instance, t0,
	).Scan(...)
```
Add the same `role IN (roles…, '')` constraint the fresh path uses, built the same way (append `?` placeholders + the roles + `""` to the args), when `len(roles) > 0`. Build the query string dynamically (like the fresh SELECT does) so the role filter is inserted before `ORDER BY`. Keep the `claimed_by`/lease/halt conditions. When `len(roles)==0` (a generalist), no role filter — unchanged.

Precise shape:
```go
	selfHealQ := `SELECT id,mission_id,key,role,title,instruction,depends_on,created_ts,model FROM tasks
		 WHERE status=? AND claimed_by=?
		   AND ((claimed_instance=? AND ?!='') OR claim_expires_ts < ?)
		   AND mission_id NOT IN (SELECT mission_id FROM mission_halts)`
	shArgs := []any{StatusClaimed, bee, instance, instance, t0}
	if len(roles) > 0 {
		ph := strings.TrimSuffix(strings.Repeat("?,", len(roles)+1), ",")
		selfHealQ += ` AND role IN (` + ph + `)`
		for _, r := range roles {
			shArgs = append(shArgs, r)
		}
		shArgs = append(shArgs, "")
	}
	selfHealQ += ` ORDER BY claimed_ts, id LIMIT 1`
	err := tx.QueryRow(selfHealQ, shArgs...).Scan(...)
```
(Match the exact variable names / `Scan` targets already in the function.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/queue/ -race -run 'TestSelfHeal|Claim' -v` then `go test ./internal/queue/ -race`
Expected: PASS (new + all existing claim/self-heal tests — the legitimate same-role self-heal still works).

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/queue/store.go internal/queue/store_test.go
go build ./... && go test ./internal/queue/... -race && bash scripts/check-security.sh
git add internal/queue/store.go internal/queue/store_test.go
git commit -m "queue: self-heal claim path respects role scoping (no cross-role task theft under a shared bee)"
```

---

### Task 2: Tag operational (model-unreachable) findings distinctly

**Files:**
- Modify: `cmd/corral-agent/main.go` (`handleTaskError` ~622-637)
- Test: `cmd/corral-agent/*_test.go`

**Interfaces:**
- Produces: `handleTaskError` files the model-unreachable finding with `type: "ops"` (a distinct operational type) instead of `"note"`, and `severity: "low"` — so the pool's verdict aggregation (Task 3) can exclude operational events from audit findings and the certification gate. The finding stays visible to operators (still filed), just distinguishable.

- [ ] **Step 1: Write the failing test**

If `handleTaskError` is testable (it takes a `brain func(string, map[string]any) string`), add a test with a fake brain capturing the report_finding args:

```go
func TestHandleTaskErrorTagsOps(t *testing.T) {
	var got map[string]any
	brain := func(tool string, args map[string]any) string {
		if tool == "report_finding" { got = args }
		return "{}"
	}
	handled := handleTaskError(7, 1, "anthropic:claude-sonnet-5", fmt.Errorf("%w: 529", ErrModelUnreachable), brain)
	if !handled { t.Fatal("model-unreachable must be handled") }
	if got == nil { t.Fatal("a finding should be filed") }
	if got["type"] != "ops" {
		t.Fatalf(`operational finding must be type "ops", got %v`, got["type"])
	}
	if got["severity"] != "low" {
		t.Fatalf(`operational finding must be low severity (not a blocking audit finding), got %v`, got["severity"])
	}
}
```

If the file has no import of `fmt`/`errors` in tests, add them.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/corral-agent/ -run TestHandleTaskErrorTagsOps -v`
Expected: FAIL — type is `"note"`, severity `"high"`.

- [ ] **Step 3: Change the finding tag**

In `handleTaskError`, change the `report_finding` args: `"type": "ops"` (was `"note"`) and `"severity": "low"` (was `"high"`). Keep target/evidence/suggested_action. Update the doc comment to note it's an operational marker, excluded from the audit verdict + gate (see Task 3).

If `report_finding` (brain-side) validates `type` against an allowed set, add `"ops"` to the allowed types (grep `internal/brain` for the finding-type validation; if none, no change). Note in the report whether validation existed.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/corral-agent/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w cmd/corral-agent/main.go cmd/corral-agent/*_test.go
go build ./... && go test ./cmd/corral-agent/... && bash scripts/check-security.sh
git add cmd/corral-agent/main.go cmd/corral-agent/*_test.go
git commit -m "corral-agent: tag model-unreachable findings type=ops (operational, not an audit finding)"
```

---

### Task 3: Pool verdict + gate exclude operational findings

**Files:**
- Modify: `internal/advpool/driver.go` (`tickAggregate` criticFindings filter ~505-514; `blockingFindingOpen` ~586-599)
- Test: `internal/advpool/driver_test.go`

**Interfaces:**
- Consumes: `queue.Finding.Type`; the `"ops"` type from Task 2.
- Produces: `Verdict.VacuousFindings` never includes `type=="ops"` findings; `blockingFindingOpen` never treats a `type=="ops"` finding as certification-blocking.

**Design:** an operational event (model-unreachable) may be filed against the critic's task id, but it is not the critic's judgment. Filter `type=="ops"` out of (a) the `criticFindings` slice that becomes `VacuousFindings`, and (b) the `blockingFindingOpen` gate. Add a small helper `isOperationalFinding(f) bool` = `f.Type == "ops"` for one definition.

- [ ] **Step 1: Write the failing test**

```go
func TestAggregateExcludesOpsFindings(t *testing.T) {
	// A critic task id, with TWO findings against it: a real vacuous-test note
	// and an operational "ops" model-unreachable. Only the real one may reach
	// the verdict, and the ops one must not block certification.
	// Drive tickAggregate via the file's harness with these findings, or unit-
	// test the filter helper + blockingFindingOpen directly:
	ops := queue.Finding{TaskID: 3, Type: "ops", Severity: "high", Status: queue.FindingOpen}
	real := queue.Finding{TaskID: 3, Type: "note", Severity: "high", Status: queue.FindingOpen, Target: "TestX", Evidence: "vacuous"}

	d := &Driver{BlockSeverity: "high"}
	if d.blockingFindingOpen([]queue.Finding{ops}) {
		t.Fatal("an operational ops finding must NOT block certification")
	}
	if !d.blockingFindingOpen([]queue.Finding{real}) {
		t.Fatal("a real high finding must still block")
	}
	// And criticFindings filtering (extract the filter into a helper the test can call):
	got := filterCriticFindings([]queue.Finding{ops, real}, 3)
	if len(got) != 1 || got[0].Type != "note" {
		t.Fatalf("ops finding must be excluded from critic findings, got %+v", got)
	}
}
```

Adjust to the actual harness: if `blockingFindingOpen` is a method on `*Driver`, construct a minimal `Driver{BlockSeverity:"high"}`. Extract the `criticFindings` building (currently inline in `tickAggregate`) into a testable `filterCriticFindings(findings []queue.Finding, criticTaskID int64) []queue.Finding` that both `tickAggregate` calls and the test calls.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/advpool/ -run TestAggregateExcludesOpsFindings -v`
Expected: FAIL — `blockingFindingOpen` blocks on the ops finding; `filterCriticFindings` undefined / includes ops.

- [ ] **Step 3: Implement the exclusions**

Add:
```go
// isOperationalFinding reports whether f is an operational event (e.g. a
// model-unreachable notice filed by a worker), not an audit finding. These are
// visible to operators but never count as a critic's judgment nor block
// certification — an infrastructure hiccup is not a defect in the change.
func isOperationalFinding(f queue.Finding) bool { return f.Type == "ops" }

// filterCriticFindings returns the test-critic task's AUDIT findings (excluding
// operational events), used to populate Verdict.VacuousFindings.
func filterCriticFindings(findings []queue.Finding, criticTaskID int64) []queue.Finding {
	var out []queue.Finding
	for _, f := range findings {
		if f.TaskID == criticTaskID && !isOperationalFinding(f) {
			out = append(out, f)
		}
	}
	return out
}
```
In `tickAggregate`, replace the inline `criticFindings` loop with `criticFindings := filterCriticFindings(findings, tc.ID)`. In `blockingFindingOpen`, skip operational findings: add `if isOperationalFinding(f) { continue }` inside the loop before the severity check.

- [ ] **Step 4: Run tests to verify they pass (with -race)**

Run: `go test ./internal/advpool/ -race -run 'TestAggregate|TestRunStatus|TestVerdict|Deadline' -v` then `go test ./internal/advpool/ -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/advpool/driver.go internal/advpool/driver_test.go
go build ./... && go test ./internal/advpool/... -race && bash scripts/check-security.sh
git add internal/advpool/driver.go internal/advpool/driver_test.go
git commit -m "advpool: operational findings never pollute the verdict or block certification"
```

---

### Task 4: Deploy + clean cross-vendor re-run (controller)

Not a subagent task. After merge+deploy: re-run the cross-vendor pool with **namespaced worker names** so each worker is a distinct bee:
- `AGENT_NAME=corral-svc/claude-writer` (roles mutant-generator,test-writer)
- `AGENT_NAME=corral-svc/gemini-critic` (roles test-critic)
Trigger on `internal/fence`; assert: each worker runs ONLY its roles (no cross-vendor "model unreachable"), the verdict's findings are the critic's real judgment (no "ops" pollution), and the run signs cleanly. Then decide the recording/hero capture.

## Self-Review (plan author)
- Bug 1 (role scope leak on reissue) → T1 + the operator namespacing (T4). Bug 2 (verdict pollution) → T2 (tag) + T3 (exclude). Coverage complete.
- Type consistency: `"ops"` type string shared across T2 (filed) and T3 (`isOperationalFinding`); `filterCriticFindings(findings, criticTaskID)` signature used in T3 impl + test.
- No-regression: T1 preserves legitimate same-role self-heal; T3's exclusion is scoped to the pool's aggregate + its own `blockingFindingOpen`.

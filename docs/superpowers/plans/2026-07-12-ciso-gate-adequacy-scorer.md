<!-- SPDX-License-Identifier: Elastic-2.0 -->
# CISO Gates — Plan: the mutation-testing adequacy scorer

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Measure how well a candidate test *bites* — run it against the compliant code (must pass) and against a set of goal-violating mutants (each catch = a "kill"), and return the **kill rate** (the test-adequacy score) + the **surviving (uncaught) mutants**. This is the deterministic heart of the control loop the spike validated (spec §4e).

**Architecture:** A new `internal/adequacy` package. The scoring LOGIC is deterministic and takes a `Jail` interface (so it unit-tests with a fake, no real sandbox needed). A real `Jail` adapter writes the workspace files to a temp dir and runs the test command via `sandbox.Run` in the bwrap jail (reusing the repo-gate's isolation path). No LLM here — the mutants are *inputs*; generating them is a later plan.

**Tech Stack:** Go 1.26.5; `internal/sandbox` (`Run`/`Options`/`Isolator`).

## Global Constraints
- SPDX `// SPDX-License-Identifier: Elastic-2.0` on every new file.
- **TDD**: failing test first, watch it fail, minimal code, watch it pass.
- Per commit: `go vet ./...` + `go test ./...` + `go build ./...` + `bash scripts/check-security.sh` (`export PATH="$PATH:$HOME/go/bin"`) all green.
- **Deterministic scoring**: `Score` has no LLM, no network, no `time.Now()`; same (test, mutants, jail behavior) → same Report.
- **Fail-safe reading of results**: a test that does NOT pass on the compliant code makes the report **invalid** (`CompliantPass=false`) — a scorer must never report a kill rate for a test that's broken/overreaching on clean code.
- The untrusted test+code runs in the **bwrap jail** via the real backend; a nil backend disables real runs (the adapter surfaces that as an error, never an unsandboxed `os/exec`). Mirror the repo-gate's jail usage.
- corral metaphor.

## File Structure
- `internal/adequacy/score.go` — `Mutant`, `Report`, `Jail`, `Score`. (new)
- `internal/adequacy/jail.go` — the `sandbox`-backed `Jail` adapter. (new)
- `internal/adequacy/score_test.go`, `internal/adequacy/jail_test.go` — tests. (new)

## Interfaces (produced — the writer/reviewer/gate plans consume these)
```go
type Mutant struct {
    ID   string // stable id, e.g. "m1"
    Code string // full mutated file content (a drop-in replacement for the code file)
}
type Report struct {
    CompliantPass bool     // did the test PASS on the unmutated code? false => report invalid
    Total         int      // number of mutants scored
    Killed        []string // mutant IDs the test CAUGHT (test failed against them)
    Survived      []string // mutant IDs the test did NOT catch (test passed) — the uncaught list
}
func (r Report) KillRate() float64 // len(Killed)/Total; 0 when Total==0

// Jail runs a test in an isolated workspace and reports whether it PASSED (exit 0).
type Jail interface {
    RunTest(ctx context.Context, files map[string]string, testCmd []string) (passed bool, err error)
}

// Score runs the candidate test (carried in base, e.g. {"x_test.go":..., "go.mod":...}) against
// the compliant code and each mutant, varying only the file at codePath. It short-circuits to an
// invalid report if the test does not pass on compliant code.
func Score(ctx context.Context, j Jail, base map[string]string, codePath, compliantCode string, mutants []Mutant, testCmd []string) (Report, error)

// NewJail returns a Jail backed by the bwrap sandbox. A nil backend => RunTest errors (never unsandboxed).
func NewJail(backend sandbox.Isolator, timeout time.Duration) Jail
```

---

## Task 1: the deterministic scorer

**Files:**
- Create: `internal/adequacy/score.go`
- Test: `internal/adequacy/score_test.go`

**Interfaces:**
- Produces: `Mutant`, `Report`, `KillRate`, `Jail`, `Score`.
- Consumes: nothing external (pure logic + the injected `Jail`).

- [ ] **Step 1: Failing test — score with a fake jail (compliant passes, 2 of 3 mutants killed).**
```go
func TestScore(t *testing.T) {
	// fake jail: the test "passes" (returns true) on the compliant code and on
	// mutant m2 (a survivor the test misses); it "fails" (false) on m1 and m3.
	fj := fakeJail{passOn: map[string]bool{"COMPLIANT": true, "m1": false, "m2": true, "m3": false}}
	base := map[string]string{"code_test.go": "<test>", "go.mod": "module target\ngo 1.26\n"}
	muts := []Mutant{{"m1", "M1"}, {"m2", "M2"}, {"m3", "M3"}}
	rep, err := Score(context.Background(), fj, base, "code.go", "COMPLIANT", muts, []string{"go", "test", "./"})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.CompliantPass || rep.Total != 3 {
		t.Fatalf("unexpected: %+v", rep)
	}
	if got := rep.KillRate(); got < 0.66 || got > 0.67 {
		t.Errorf("KillRate = %v, want ~0.667 (2/3)", got)
	}
	if !eq(rep.Killed, []string{"m1", "m3"}) || !eq(rep.Survived, []string{"m2"}) {
		t.Errorf("killed=%v survived=%v", rep.Killed, rep.Survived)
	}
}

func TestScoreInvalidWhenCompliantFails(t *testing.T) {
	// A test that fails on compliant code is broken/overreaching: report invalid, no mutants run.
	fj := fakeJail{passOn: map[string]bool{"COMPLIANT": false}}
	rep, err := Score(context.Background(), fj, map[string]string{}, "code.go", "COMPLIANT",
		[]Mutant{{"m1", "M1"}}, []string{"go", "test"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.CompliantPass {
		t.Fatal("want CompliantPass=false")
	}
	if len(rep.Killed)+len(rep.Survived) != 0 || fj.calls != 1 {
		t.Fatalf("mutants must NOT run when compliant fails: %+v calls=%d", rep, fj.calls)
	}
}
```
Add a `fakeJail` (keyed on a marker in the code content: it maps the code string to pass/fail; `"COMPLIANT"` is the compliant marker, mutant `.Code` is its own marker) and an `eq(a,b []string) bool` helper. `fakeJail.RunTest` inspects `files[codePath]`… simplest: the fake keys on the code content passed in the files map — have the test set `base` empty and the fake look up `files["code.go"]` against `passOn`.

- [ ] **Step 2: Run it, watch it fail** (`Score` undefined).

- [ ] **Step 3: Implement `score.go`.**
```go
func (r Report) KillRate() float64 {
	if r.Total == 0 {
		return 0
	}
	return float64(len(r.Killed)) / float64(r.Total)
}

func Score(ctx context.Context, j Jail, base map[string]string, codePath, compliantCode string, mutants []Mutant, testCmd []string) (Report, error) {
	run := func(code string) (bool, error) {
		files := make(map[string]string, len(base)+1)
		for k, v := range base {
			files[k] = v
		}
		files[codePath] = code
		return j.RunTest(ctx, files, testCmd)
	}
	pass, err := run(compliantCode)
	if err != nil {
		return Report{}, err
	}
	rep := Report{CompliantPass: pass}
	if !pass {
		// broken/overreaching test — do not score mutants (fail-safe: no kill rate for an invalid test)
		return rep, nil
	}
	rep.Total = len(mutants)
	for _, m := range mutants {
		killed, err := run(m.Code)
		if err != nil {
			return Report{}, err
		}
		if killed { // test PASSED on a violation => it did NOT catch it
			rep.Survived = append(rep.Survived, m.ID)
		} else { // test FAILED on the violation => caught (killed)
			rep.Killed = append(rep.Killed, m.ID)
		}
	}
	return rep, nil
}
```
**Watch the inversion:** `RunTest` returns `passed`. A mutant is **killed** when the test does NOT pass (`!passed`) against it. Name the local carefully — the snippet above has the branches right; keep them.

- [ ] **Step 4: Run, watch pass.** `go test ./internal/adequacy/...` → PASS. **Commit:** `feat(adequacy): deterministic mutation kill-rate scorer`.

---

## Task 2: the bwrap-jail adapter

**Files:**
- Create: `internal/adequacy/jail.go`
- Test: `internal/adequacy/jail_test.go`

**Interfaces:**
- Produces: `NewJail(backend sandbox.Isolator, timeout time.Duration) Jail`.
- Consumes: `sandbox.Run`/`sandbox.Options`/`sandbox.Isolator` (read `internal/sandbox/sandbox.go` for the exact fields — Workspace, Timeout, Network, Backend; Result.ExitCode).

- [ ] **Step 1: Failing test — the adapter maps exit 0 → passed, nonzero → not-passed, through the REAL backend.**
```go
func TestJailAdapterExitMapping(t *testing.T) {
	backend, err := sandbox.Resolve(sandbox.Config{ /* the same way corral-agent/main.go resolves it */ })
	if err != nil || backend == nil {
		t.Skip("no sandbox backend available (bwrap) — adapter exit-mapping needs the real jail")
	}
	j := NewJail(backend, 30*time.Second)
	// a command that exits 0 => passed; one that exits nonzero => not passed.
	pass, err := j.RunTest(context.Background(), map[string]string{"marker.txt": "x"}, []string{"true"})
	if err != nil || !pass {
		t.Fatalf("exit-0 command: passed=%v err=%v, want passed=true", pass, err)
	}
	fail, err := j.RunTest(context.Background(), map[string]string{"marker.txt": "x"}, []string{"false"})
	if err != nil || fail {
		t.Fatalf("exit-1 command: passed=%v err=%v, want passed=false", fail, err)
	}
}

func TestJailAdapterNilBackendErrors(t *testing.T) {
	j := NewJail(nil, time.Second)
	if _, err := j.RunTest(context.Background(), map[string]string{}, []string{"true"}); err == nil {
		t.Fatal("nil backend must error, never run unsandboxed")
	}
}
```
(Confirm the exact `sandbox.Resolve`/`sandbox.Config` construction against `cmd/corral-agent/main.go:~1004`; if the real backend is impractical in CI, the skip keeps the suite green while the nil-backend + file-writing are still asserted.)

- [ ] **Step 2: Run, watch fail** (`NewJail` undefined).

- [ ] **Step 3: Implement `jail.go`.** The adapter: refuse a nil backend; write each file into a fresh `os.MkdirTemp` workspace (creating parent dirs for pathed keys); `sandbox.Run(ctx, strings.Join(testCmd, " "), sandbox.Options{Workspace: dir, Backend: b.backend, Network: false, Timeout: b.timeout})`; return `res.ExitCode == 0`. Clean up the temp dir (`defer os.RemoveAll`). **Pass `Result.ExitCode` through faithfully** — do not remap a timed-out/errored run to exit 0 (mirror the repo-gate jailAdapter contract: a `Result.TimedOut`/`Result.Err` must NOT read as passed).

- [ ] **Step 4: Run, watch pass.** `go test ./internal/adequacy/...` → PASS (the nil-backend + file-writing tests always run; the real-jail exit-mapping runs when bwrap is present). Then the full gate: `go vet ./... && go build ./... && go test ./... && bash scripts/check-security.sh`. **Commit:** `feat(adequacy): bwrap-jail adapter for the scorer`.

---

## Self-Review
- **Spec coverage (§4e core):** deterministic kill-rate scoring ✓; compliant-must-pass fail-safe ✓; surviving (uncaught) mutant list for the triage/feedback loop ✓; jail-isolated runs reusing the repo-gate path ✓; nil-backend never unsandboxed ✓. The LLM writer + violation-generator + reviewer triage + human-approval wiring are LATER plans (this is the mechanism they feed).
- **No placeholders:** complete code for the scorer; the adapter is concrete (temp-dir + sandbox.Run) with the one "confirm sandbox.Resolve construction" note pointing at real existing code.
- **Type consistency:** `Mutant`/`Report`/`Jail`/`Score`/`NewJail` stable; the kill-vs-pass inversion is called out so it isn't fumbled.
- **Determinism:** `Score` is pure over the injected `Jail`; no time.Now/map-order in the *output* (Killed/Survived preserve mutant input order).
- **Honesty:** a test that fails on compliant code yields an INVALID report (no kill rate), never a misleading score.

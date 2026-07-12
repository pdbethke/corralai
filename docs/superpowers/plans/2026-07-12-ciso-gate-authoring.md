<!-- SPDX-License-Identifier: Elastic-2.0 -->
# CISO Gates — Plan: the authoring-tier orchestration

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Compose the built parts into the authoring-tier loop — for one CISO goal against one target file: extract signatures → write a candidate test → generate + **compile-verify** mutants → score adequacy → return the candidate test + its adequacy report. This is where "we have all the parts" becomes "the loop runs," and it's the home of the correctness fix: a non-compiling mutant is an *invalid probe* and must be discarded, never counted as a kill.

**Architecture:** A new `internal/authoring` package. `Author` takes the injected `testgen.LLM` (generation) and `adequacy.Jail` (compile-verify + scoring) interfaces, so the whole loop is testable with fakes — no live model, no real jail. It calls `repoindex.ExtractSignatures`, `testgen.WriteTest`, `testgen.GenerateMutants`, filters mutants through `compileVerify` (build each in the jail; drop non-compilers), then `adequacy.Score` over only the valid mutants. Returns the test, the `adequacy.Report`, and the discarded (non-compiling) mutant IDs.

**Tech Stack:** Go 1.26.5; `internal/repoindex`, `internal/testgen`, `internal/adequacy` (all merged).

## Global Constraints
- SPDX `// SPDX-License-Identifier: Elastic-2.0` on every new file.
- **TDD**: failing test first (fake LLM + fake jail), watch it fail, minimal code, watch it pass.
- Per commit: `go vet ./...` + `go test ./...` + `go build ./...` + `bash scripts/check-security.sh` (`export PATH="$PATH:$HOME/go/bin"`) all green.
- **No live model / no real jail in tests** — both are injected interfaces; use fakes.
- **THE correctness fix (binding):** every mutant is compile-verified before scoring. A mutant that does NOT compile is discarded as an invalid probe — it must NOT reach `adequacy.Score` (where a non-compiling mutant would make `go test` exit non-zero → be miscounted as a *kill* → inflate the kill rate → corrupt the CISO's adequacy signal).
- **Deterministic** — no `time.Now()`; the loop is pure over the injected interfaces; `Discarded` preserves mutant input order.
- corral metaphor.

## File Structure
- `internal/authoring/authoring.go` — `Request`, `Result`, `Author`. (new)
- `internal/authoring/verify.go` — `compileVerify`. (new)
- `internal/authoring/verify_test.go`, `internal/authoring/authoring_test.go` — fake-jail / fake-LLM tests. (new)

## Interfaces (produced — the reviewer/feedback/gate plans consume these)
```go
type Request struct {
    Goal     string            // the CISO goal intent (controlspec.Goal.Intent)
    Code     string            // the compliant target file content
    Lang     string            // "go", "python", ...
    CodePath string            // the file the code/mutant occupies in the workspace, e.g. "auth.go"
    TestPath string            // where the generated test goes, e.g. "auth_gate_test.go"
    Base     map[string]string // constant workspace files (go.mod + any sibling package files); NOT the code or the test
    NMutants int               // how many violation-mutants to request
    BuildCmd []string          // compile-check command, e.g. []string{"go","build","./"}
    TestCmd  []string          // test command, e.g. []string{"go","test","./"}
}
type Result struct {
    Test      string          // the candidate test file content
    Report    adequacy.Report // kill rate, killed, survived (over the VALID mutants only)
    Discarded []string        // mutant IDs dropped because they did not compile (invalid probes)
}
func Author(ctx context.Context, m testgen.LLM, jail adequacy.Jail, req Request) (Result, error)
```

---

## Task 1: compile-verify (the invalid-probe filter)

**Files:**
- Create: `internal/authoring/verify.go`
- Test: `internal/authoring/verify_test.go`

**Interfaces:**
- Produces: `func compileVerify(ctx context.Context, jail adequacy.Jail, base map[string]string, codePath string, mutants []adequacy.Mutant, buildCmd []string) (valid []adequacy.Mutant, discarded []string, err error)`.
- Consumes: `adequacy.Jail` (build each mutant in a workspace), `adequacy.Mutant`.

- [ ] **Step 1: Failing test — a non-compiling mutant is discarded, not scored.**
```go
func TestCompileVerify(t *testing.T) {
	// fake jail: RunTest returns "compiles" (true) for mutant code containing "OK",
	// and false for code containing "BAD" (a stand-in for a build failure).
	fj := &fakeJail{compileOK: func(code string) bool { return strings.Contains(code, "OK") }}
	muts := []adequacy.Mutant{{"m1", "OK-1"}, {"m2", "BAD-2"}, {"m3", "OK-3"}}
	valid, discarded, err := compileVerify(context.Background(), fj, map[string]string{"go.mod": "x"}, "code.go", muts, []string{"go", "build", "./"})
	if err != nil {
		t.Fatal(err)
	}
	if len(valid) != 2 || valid[0].ID != "m1" || valid[1].ID != "m3" {
		t.Fatalf("valid = %+v, want [m1 m3]", valid)
	}
	if len(discarded) != 1 || discarded[0] != "m2" {
		t.Fatalf("discarded = %v, want [m2]", discarded)
	}
	// each verify ran BuildCmd against a workspace containing base + the mutant at codePath
	if fj.lastCmd[0] != "go" || fj.lastCmd[1] != "build" {
		t.Errorf("build cmd not used: %v", fj.lastCmd)
	}
}
```
Add a `fakeJail` with `compileOK func(code string) bool` whose `RunTest(files, cmd)` records `lastCmd` and returns `compileOK(files["code.go"])`.

- [ ] **Step 2: Run, watch fail** (`compileVerify` undefined).

- [ ] **Step 3: Implement `verify.go`.**
```go
func compileVerify(ctx context.Context, jail adequacy.Jail, base map[string]string, codePath string, mutants []adequacy.Mutant, buildCmd []string) ([]adequacy.Mutant, []string, error) {
	var valid []adequacy.Mutant
	var discarded []string
	for _, mut := range mutants {
		ws := make(map[string]string, len(base)+1)
		for k, v := range base {
			ws[k] = v
		}
		ws[codePath] = mut.Code
		compiles, err := jail.RunTest(ctx, ws, buildCmd)
		if err != nil {
			return nil, nil, fmt.Errorf("authoring: compile-verify mutant %s: %w", mut.ID, err)
		}
		if compiles {
			valid = append(valid, mut)
		} else {
			discarded = append(discarded, mut.ID)
		}
	}
	return valid, discarded, nil
}
```

- [ ] **Step 4: Run, watch pass.** `go test ./internal/authoring/...` → PASS. **Commit:** `feat(authoring): compile-verify filters non-compiling mutants (invalid probes)`.

---

## Task 2: Author — compose the authoring-tier loop

**Files:**
- Create: `internal/authoring/authoring.go`
- Test: `internal/authoring/authoring_test.go`

**Interfaces:**
- Produces: `Request`, `Result`, `Author`.
- Consumes: `repoindex.ExtractSignatures`, `testgen.WriteTest`/`GenerateMutants`/`LLM`, `adequacy.Score`/`Jail`/`Mutant`/`Report`, Task-1 `compileVerify`.

- [ ] **Step 1: Failing test — full loop with a fake LLM + fake jail.**
```go
func TestAuthor(t *testing.T) {
	// fake LLM: WriteTest gets writeTestSystem → returns a test; GenerateMutants gets
	// genMutantsSystem → returns 3 mutants (one won't compile).
	m := &fakeLLM{onSystem: func(sys string) string {
		if strings.Contains(sys, "TEST-WRITER") {
			return "```go\npackage target\nimport \"testing\"\nfunc TestGoal(t *testing.T){}\n```"
		}
		return "===MUTATION_1===\nOK m1\n===MUTATION_2===\nBAD m2\n===MUTATION_3===\nOK m3\n"
	}}
	// fake jail: compile-verify (build cmd) → true unless code contains "BAD";
	// score (test cmd) → test passes on COMPLIANT and on m3 (survivor), fails on m1 (killed).
	jail := &fakeJail{onRun: func(files map[string]string, cmd []string) bool {
		code := files["auth.go"]
		if cmd[1] == "build" {
			return !strings.Contains(code, "BAD")
		}
		// test cmd: pass (true) on compliant + m3; fail (false) on m1
		return code == "COMPLIANT" || strings.Contains(code, "m3")
	}}
	req := Request{
		Goal: "g", Code: "COMPLIANT", Lang: "go", CodePath: "auth.go", TestPath: "auth_gate_test.go",
		Base: map[string]string{"go.mod": "module target\ngo 1.26\n"}, NMutants: 3,
		BuildCmd: []string{"go", "build", "./"}, TestCmd: []string{"go", "test", "./"},
	}
	res, err := Author(context.Background(), m, jail, req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Test, "func TestGoal") {
		t.Errorf("test not returned: %q", res.Test)
	}
	// m2 (BAD) discarded as non-compiling — NOT scored; only m1,m3 reach Score.
	if len(res.Discarded) != 1 || res.Discarded[0] != "m2" {
		t.Errorf("discarded = %v, want [m2]", res.Discarded)
	}
	if !res.Report.CompliantPass || res.Report.Total != 2 {
		t.Fatalf("report scored the wrong mutant set: %+v", res.Report)
	}
	// m1 killed (test failed on it), m3 survived (test passed on it) → kill rate 0.5
	if kr := res.Report.KillRate(); kr < 0.49 || kr > 0.51 {
		t.Errorf("kill rate = %v, want 0.5 (m2 must not inflate it)", kr)
	}
}

func TestAuthorUnsupportedLang(t *testing.T) {
	_, err := Author(context.Background(), &fakeLLM{}, &fakeJail{}, Request{Lang: "cobol", Code: "x"})
	if err == nil {
		t.Fatal("unsupported language must error before calling the model")
	}
}
```
(Reuse/adapt the `fakeLLM` and `fakeJail` helpers; the fakeLLM keys its response on the system prompt so it can serve both WriteTest and GenerateMutants in one object.)

- [ ] **Step 2: Run, watch fail** (`Author` undefined).

- [ ] **Step 3: Implement `authoring.go`.**
```go
func Author(ctx context.Context, m testgen.LLM, jail adequacy.Jail, req Request) (Result, error) {
	sigs, err := repoindex.ExtractSignatures(req.Code, req.Lang)
	if err != nil {
		return Result{}, fmt.Errorf("authoring: extract signatures: %w", err)
	}
	test, err := testgen.WriteTest(ctx, m, req.Goal, req.Code, sigs)
	if err != nil {
		return Result{}, fmt.Errorf("authoring: write test: %w", err)
	}
	mutants, err := testgen.GenerateMutants(ctx, m, req.Goal, req.Code, sigs, req.NMutants)
	if err != nil {
		return Result{}, fmt.Errorf("authoring: generate mutants: %w", err)
	}
	valid, discarded, err := compileVerify(ctx, jail, req.Base, req.CodePath, mutants, req.BuildCmd)
	if err != nil {
		return Result{}, err
	}
	// Score the candidate test against the compliant code + ONLY the valid mutants.
	scoreBase := make(map[string]string, len(req.Base)+1)
	for k, v := range req.Base {
		scoreBase[k] = v
	}
	scoreBase[req.TestPath] = test
	rep, err := adequacy.Score(ctx, jail, scoreBase, req.CodePath, req.Code, valid, req.TestCmd)
	if err != nil {
		return Result{}, fmt.Errorf("authoring: score: %w", err)
	}
	return Result{Test: test, Report: rep, Discarded: discarded}, nil
}
```

- [ ] **Step 4: Run, watch pass.** `go test ./internal/authoring/...` → PASS. Then the full gate: `go vet ./... && go build ./... && go test ./... && bash scripts/check-security.sh`. **Commit:** `feat(authoring): the authoring-tier loop (extract→write→generate→verify→score)`.

---

## Self-Review
- **Spec coverage (§5 authoring tier):** the loop composed end-to-end ✓; **compile-verify before scoring so a broken mutant can't inflate the kill rate** ✓ (the correctness fix); returns the candidate test + adequacy report + discarded probes for the reviewer/CISO ✓; deterministic under fakes ✓.
- **No placeholders:** complete code for both `compileVerify` and `Author`.
- **Type consistency:** `Request`/`Result`/`Author` stable; consumes the real `ExtractSignatures`/`WriteTest`/`GenerateMutants`/`Score`; the fake LLM keys on the system prompt to serve both generation calls.
- **The load-bearing test:** `TestAuthor` proves the discarded (non-compiling) mutant does NOT reach `Score` and does NOT inflate the kill rate (0.5 over 2 valid, not 0.67 over 3) — the exact correctness the plan exists to guarantee.
- **Out of scope (later plans):** the reviewer triage of surviving mutants + the feedback loop + human approval; the gate dimension (4g); goal→file mapping; the model + jail wiring in the brain (a capable model behind `testgen.LLM`, the bwrap backend behind `adequacy.NewJail`).

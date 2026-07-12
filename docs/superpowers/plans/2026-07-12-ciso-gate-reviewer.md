<!-- SPDX-License-Identifier: Elastic-2.0 -->
# CISO Gates — Plan: the reviewer-triage agent

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The last generative agent: an **independent reviewer** that classifies each *surviving* mutant (one the candidate test did NOT catch) as a **real coverage gap** (the test should catch it — strengthen) vs an **equivalent mutant** (not actually a goal violation — discard), with a one-line rationale. This turns the raw uncaught-mutant list into the **curated feedback** the CISO reviews (spec §4e/§4f) — so a control freak isn't drowned in raw survivors.

**Architecture:** Add to `internal/testgen` (cohesive with the writer + violation-generator — same `LLM` interface, prompt-build, response-parse idiom). `TriageSurvivors` builds a review prompt (goal + compliant code + the candidate test + the uncaught mutants), calls the model with a *separate* system prompt (the reviewer is an independent seat — a model that reviews must not be the one that wrote/mutated), and parses a structured verdict per mutant. Generation/classification only — no compile/run.

**Tech Stack:** Go 1.26.5; `internal/adequacy` (`Mutant`), `internal/testgen` (`LLM`).

## Global Constraints
- SPDX `// SPDX-License-Identifier: Elastic-2.0` on every new file.
- **TDD**: failing test first (fake LLM / parser input), watch it fail, minimal code, watch it pass.
- Per commit: `go vet ./...` + `go test ./...` + `go build ./...` + `bash scripts/check-security.sh` (`export PATH="$PATH:$HOME/go/bin"`) all green.
- **No live model in tests** — reuse the injected `testgen.LLM` interface + a fake; assert prompt construction + verdict parsing, never live output.
- **Independence** — `TriageSurvivors` uses its OWN system prompt (`reviewSystem`), distinct from `writeTestSystem`/`genMutantsSystem`. Keep it a separate call/seat.
- **Deterministic** — no `time.Now()`; parsing pure over the response; verdicts preserve response order.
- corral metaphor.

## File Structure
- `internal/testgen/review.go` — `Verdict`, `reviewSystem`, `TriageSurvivors`, `parseVerdicts`. (new)
- `internal/testgen/review_test.go` — parser + fake-LLM tests. (new)

## Interfaces (produced — the CISO approval surface consumes these)
```go
type Verdict struct {
    MutantID  string // the surviving mutant's ID
    RealGap   bool   // true = a real coverage GAP (the test should catch this violation); false = an EQUIVALENT mutant
    Rationale string // one-line reviewer rationale
}
// TriageSurvivors asks an independent reviewer to classify each uncaught mutant.
// Returns (nil, nil) when there are no survivors to triage.
func TriageSurvivors(ctx context.Context, m LLM, goal, code, test string, survivors []adequacy.Mutant) ([]Verdict, error)
```

---

## Task 1: the verdict parser

**Files:**
- Create: `internal/testgen/review.go`
- Test: `internal/testgen/review_test.go`

**Interfaces:**
- Produces: `Verdict`, `parseVerdicts`.
- Consumes: `strings`.

- [ ] **Step 1: Failing test — parse the structured verdict lines.**
```go
func TestParseVerdicts(t *testing.T) {
	resp := "MUTANT m1: GAP: the test never exercises empty grants\n" +
		"MUTANT m2: equivalent: a wildcard grant is outside the goal's model\n" +
		"some preamble line that isn't a verdict\n" +
		"MUTANT m3: WISHYWASHY: unknown class is skipped\n"
	vs := parseVerdicts(resp)
	if len(vs) != 2 {
		t.Fatalf("got %d verdicts, want 2 (garbage + unknown-class skipped): %+v", len(vs), vs)
	}
	if vs[0].MutantID != "m1" || !vs[0].RealGap || vs[0].Rationale != "the test never exercises empty grants" {
		t.Errorf("v1 wrong: %+v", vs[0])
	}
	if vs[1].MutantID != "m2" || vs[1].RealGap || vs[1].Rationale != "a wildcard grant is outside the goal's model" {
		t.Errorf("v2 wrong (case-insensitive equivalent?): %+v", vs[1])
	}
}
```

- [ ] **Step 2: Run, watch fail** (`parseVerdicts`/`Verdict` undefined).

- [ ] **Step 3: Implement `review.go` (`Verdict` + `parseVerdicts`).**
```go
type Verdict struct {
	MutantID  string
	RealGap   bool
	Rationale string
}

// parseVerdicts extracts one Verdict per line of the form
// "MUTANT <id>: <GAP|EQUIVALENT>: <rationale>". Class is case-insensitive;
// RealGap is true only for GAP. Lines that don't match (preamble, an
// unrecognized class) are skipped. Kept verdicts preserve response order.
func parseVerdicts(resp string) []Verdict {
	var out []Verdict
	for _, line := range strings.Split(resp, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "MUTANT ")
		if !ok {
			continue
		}
		id, after, ok := strings.Cut(rest, ":")
		if !ok {
			continue
		}
		classStr, rationale, ok := strings.Cut(after, ":")
		if !ok {
			continue
		}
		cls := strings.ToUpper(strings.TrimSpace(classStr))
		if cls != "GAP" && cls != "EQUIVALENT" {
			continue
		}
		out = append(out, Verdict{
			MutantID:  strings.TrimSpace(id),
			RealGap:   cls == "GAP",
			Rationale: strings.TrimSpace(rationale),
		})
	}
	return out
}
```

- [ ] **Step 4: Run, watch pass.** `go test ./internal/testgen/...` → PASS. **Commit:** `feat(testgen): reviewer verdict parser (gap vs equivalent)`.

---

## Task 2: TriageSurvivors — the independent reviewer

**Files:**
- Modify: `internal/testgen/review.go`
- Test: `internal/testgen/review_test.go`

**Interfaces:**
- Produces: `reviewSystem`, `TriageSurvivors`.
- Consumes: Task 1's `Verdict`/`parseVerdicts`; `testgen.LLM`; `adequacy.Mutant`; `context`/`errors`/`fmt`/`strings`.

- [ ] **Step 1: Failing tests — TriageSurvivors with a fake LLM; empty survivors short-circuits.**
```go
func TestTriageSurvivors(t *testing.T) {
	f := &fakeLLM{resp: "MUTANT m2: GAP: the test does not cover the wildcard path\n"}
	survivors := []adequacy.Mutant{{ID: "m2", Code: "package target\nfunc F() bool { return true }"}}
	vs, err := TriageSurvivors(context.Background(), f, "deny by default", "package target\nfunc F() bool { return false }", "package target\n// test", survivors)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 || vs[0].MutantID != "m2" || !vs[0].RealGap {
		t.Fatalf("verdicts wrong: %+v", vs)
	}
	// prompt carries the goal, the compliant code, the test, and each survivor (id + code)
	for _, want := range []string{"deny by default", "return false", "// test", "MUTANT m2", "return true"} {
		if !strings.Contains(f.gotUser, want) {
			t.Errorf("review prompt missing %q; got:\n%s", want, f.gotUser)
		}
	}
	// independence: the reviewer's system prompt is its own, not the writer/generator's
	if !strings.Contains(f.gotSystem, "TEST-REVIEWER") {
		t.Errorf("reviewer system prompt not used: %s", f.gotSystem)
	}
}

func TestTriageSurvivorsEmpty(t *testing.T) {
	f := &fakeLLM{resp: "should not be called"}
	vs, err := TriageSurvivors(context.Background(), f, "g", "c", "t", nil)
	if err != nil || vs != nil {
		t.Fatalf("no survivors → (nil,nil); got %v, %v", vs, err)
	}
	if f.called {
		t.Fatal("no survivors must NOT call the model")
	}
}

func TestTriageSurvivorsNoneParseable(t *testing.T) {
	if _, err := TriageSurvivors(context.Background(), &fakeLLM{resp: "no verdicts here"},
		"g", "c", "t", []adequacy.Mutant{{ID: "m1", Code: "x"}}); err == nil {
		t.Fatal("unparseable reviewer response must error")
	}
}
```
(Reuse the `fakeLLM` from `testgen_test.go`; add a `called bool` field if it doesn't already record whether `Ask` ran, so `TestTriageSurvivorsEmpty` can assert the model was NOT called.)

- [ ] **Step 2: Run, watch fail.** Then implement in `review.go`:
```go
const reviewSystem = `You are a TEST-REVIEWER. You are given a security GOAL, the compliant code, a candidate test, and MUTATIONS that violate the goal but the test did NOT catch. For EACH mutation decide:
- GAP: the mutation genuinely violates the goal and the test SHOULD have caught it — a real coverage gap.
- EQUIVALENT: the mutation does not actually violate the goal under any legitimate input (or is behaviourally equivalent to the compliant code) — not a real gap.
Return ONE line per mutation, EXACTLY: "MUTANT <id>: <GAP|EQUIVALENT>: <one-line rationale>". No other prose.`

func TriageSurvivors(ctx context.Context, m LLM, goal, code, test string, survivors []adequacy.Mutant) ([]Verdict, error) {
	if len(survivors) == 0 {
		return nil, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "GOAL:\n%s\n\nCOMPLIANT CODE:\n%s\n\nCANDIDATE TEST:\n%s\n\nUNCAUGHT MUTATIONS:\n", goal, code, test)
	for _, s := range survivors {
		fmt.Fprintf(&b, "MUTANT %s:\n%s\n\n", s.ID, s.Code)
	}
	resp, err := m.Ask(ctx, reviewSystem, b.String())
	if err != nil {
		return nil, err
	}
	verdicts := parseVerdicts(resp)
	if len(verdicts) == 0 {
		return nil, errors.New("testgen: reviewer returned no parseable verdicts")
	}
	return verdicts, nil
}
```

- [ ] **Step 3: Run, watch pass.** `go test ./internal/testgen/...` → PASS (writer/generator tests too). Then the full gate: `go vet ./... && go build ./... && go test ./... && bash scripts/check-security.sh`. **Commit:** `feat(testgen): the reviewer — triage surviving mutants (gap vs equivalent)`.

---

## Self-Review
- **Spec coverage (§4e/§4f reviewer):** independent triage of each surviving mutant → gap vs equivalent + rationale ✓ (the curated feedback for the CISO); separate reviewer seat/prompt (independence) ✓; empty-survivors short-circuit (no wasted call) ✓; deterministic under a fake ✓.
- **No placeholders:** complete prompt + parser + fake-LLM tests.
- **Type consistency:** `Verdict`/`TriageSurvivors` stable; consumes `adequacy.Mutant`, `testgen.LLM`; reuses `parseVerdicts`.
- **Determinism:** no live model, no `time.Now()`; parsing pure; verdicts preserve response order.
- **The independence check under test:** the reviewer's system prompt (`reviewSystem`, contains "TEST-REVIEWER") is asserted distinct from the writer/generator prompts.
- **Out of scope (later):** faithfulness/overreach review of the whole test (beyond survivor triage); the CISO approval surface that renders verdicts + kill rate + Promote/Reject; the authoring→triage wiring; goal→file mapping; the model wiring.

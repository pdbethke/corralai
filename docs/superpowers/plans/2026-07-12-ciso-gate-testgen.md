<!-- SPDX-License-Identifier: Elastic-2.0 -->
# CISO Gates — Plan: the generative agents (test-writer + violation-generator)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The two model-driven agents at the heart of *agentic testing*: a **test-writer** that turns a CISO goal + code + signatures into a candidate Go test, and a **violation-generator** that produces goal-violating mutants to measure that test's adequacy. Both were spike-validated (2026-07-11/12); this plan makes them real, tested code.

**Architecture:** A new `internal/testgen` package. It defines a one-method `LLM` interface (`Ask(ctx, system, user)`), mirroring the existing `oracle.LLM`/`learn.Asker` precedent, satisfied by `*llm.Client` and by test fakes. `WriteTest` and `GenerateMutants` build the spike-proven prompts, call the model, and **parse** the response (fence-strip / mutation-marker split). testgen only *generates* — it does NOT compile or score; validation (compile-gate + bite) is `internal/adequacy`'s job (a non-compiling test simply fails `adequacy.Score`'s compliant run). Tests use a **fake LLM** and assert prompt construction + parsing + wiring, never live model output.

**Tech Stack:** Go 1.26.5; `internal/repoindex` (`Signature`), `internal/adequacy` (`Mutant`), `encoding/json`.

## Global Constraints
- SPDX `// SPDX-License-Identifier: Elastic-2.0` on every new file.
- **TDD**: failing test first (with the fake LLM), watch it fail, minimal code, watch it pass.
- Per commit: `go vet ./...` + `go test ./...` + `go build ./...` + `bash scripts/check-security.sh` (`export PATH="$PATH:$HOME/go/bin"`) all green.
- **No live model in tests** — the `LLM` interface is injected; tests use a fake returning canned responses. testgen must be deterministic given a fixed LLM response.
- **Generation only** — testgen never runs code, compiles, or scores. It returns candidate text; `adequacy.Score` validates.
- **Independence is the point** (the accountability thesis): the writer and the violation-generator are *separate* calls with *separate* system prompts — a model that breaks the goal to test the test must not be the one that wrote the test. Keep them distinct.
- corral metaphor.

## File Structure
- `internal/testgen/testgen.go` — the `LLM` interface, `WriteTest`, `GenerateMutants`, the shared user-prompt builder. (new)
- `internal/testgen/parse.go` — `extractCode`, `parseMutants` (response parsing). (new)
- `internal/testgen/testgen_test.go`, `internal/testgen/parse_test.go` — fake-LLM + parser tests. (new)

## Interfaces (produced — the orchestration/gate plans consume these)
```go
type LLM interface {
    Ask(ctx context.Context, system, user string) (string, error)
}
// WriteTest asks the model to write ONE Go test verifying that code satisfies goal,
// bound to the callable surface sigs. Returns the raw test file content (fence-stripped).
func WriteTest(ctx context.Context, m LLM, goal, code string, sigs []repoindex.Signature) (string, error)
// GenerateMutants asks the model for n distinct, same-signature, goal-violating mutations of code.
func GenerateMutants(ctx context.Context, m LLM, goal, code string, sigs []repoindex.Signature, n int) ([]adequacy.Mutant, error)
```

---

## Task 1: the test-writer + code extraction

**Files:**
- Create: `internal/testgen/testgen.go`, `internal/testgen/parse.go`
- Test: `internal/testgen/testgen_test.go`, `internal/testgen/parse_test.go`

**Interfaces:**
- Produces: `LLM`, `WriteTest`, `extractCode`, and the user-prompt builder.
- Consumes: `repoindex.Signature`; `encoding/json` (marshal sigs into the prompt).

- [ ] **Step 1: Failing tests — extractCode + WriteTest with a fake LLM.**
```go
func TestExtractCode(t *testing.T) {
	cases := map[string]string{
		"```go\npackage p\nfunc T(){}\n```":       "package p\nfunc T(){}",
		"here you go:\n```\npackage p\n```\ndone": "package p",
		"package p\nfunc T(){}":                    "package p\nfunc T(){}", // no fence → trimmed as-is
	}
	for in, want := range cases {
		if got := extractCode(in); got != want {
			t.Errorf("extractCode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWriteTest(t *testing.T) {
	f := &fakeLLM{resp: "```go\npackage target\nimport \"testing\"\nfunc TestGoal(t *testing.T){}\n```"}
	sigs := []repoindex.Signature{{Name: "ValidatePassword", Kind: "func", Params: []repoindex.Param{{Name: "pw", Type: "string"}}, Results: []string{"error"}, Exported: true}}
	out, err := WriteTest(context.Background(), f, "passwords >= 12 chars", "package target\nfunc ValidatePassword(pw string) error { return nil }", sigs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "func TestGoal") || strings.Contains(out, "```") {
		t.Fatalf("unexpected test output: %q", out)
	}
	// prompt construction: the user prompt must carry the goal, the code, and the signature JSON.
	for _, want := range []string{"passwords >= 12 chars", "ValidatePassword", `"Name":"ValidatePassword"`} {
		if !strings.Contains(f.gotUser, want) {
			t.Errorf("user prompt missing %q; got:\n%s", want, f.gotUser)
		}
	}
}

func TestWriteTestEmptyResponseErrors(t *testing.T) {
	if _, err := WriteTest(context.Background(), &fakeLLM{resp: "   "}, "g", "c", nil); err == nil {
		t.Fatal("empty model response must error")
	}
}
```
Add a `fakeLLM{resp string; err error; gotSystem, gotUser string}` implementing `Ask` (records system/user, returns resp/err).

- [ ] **Step 2: Run, watch fail** (`extractCode`/`WriteTest` undefined).

- [ ] **Step 3: Implement `parse.go` (`extractCode`) and `testgen.go` (`LLM`, `WriteTest`, `buildUser`).**
`extractCode`: if the response contains a ```` ``` ```` fence, return the content of the first fenced block (skipping an optional language tag on the fence's first line); else return the trimmed response.
`buildUser(goal, code string, sigs []repoindex.Signature, instruction string) string`:
```go
sigJSON, _ := json.Marshal(sigs)
var b strings.Builder
fmt.Fprintf(&b, "GOAL:\n%s\n\nTARGET FILE:\n%s\n\nSIGNATURE SURFACE (JSON):\n%s\n", goal, code, sigJSON)
if instruction != "" {
	fmt.Fprintf(&b, "\n%s\n", instruction)
}
return b.String()
```
`WriteTest`:
```go
const writeTestSystem = `You are a TEST-WRITER. Given a security control GOAL, a target source file, and its signature surface, write ONE executable Go test that verifies the code SATISFIES the goal.
- Same package as the target (white-box).
- It MUST compile against the target and MUST FAIL if the goal is violated — test the goal's boundary (what a weakened implementation would pass that a compliant one must not).
- Standard library "testing" only. Deterministic, no network.
Return ONLY the raw Go test file content — no prose, no markdown fences.`

func WriteTest(ctx context.Context, m LLM, goal, code string, sigs []repoindex.Signature) (string, error) {
	resp, err := m.Ask(ctx, writeTestSystem, buildUser(goal, code, sigs, ""))
	if err != nil {
		return "", err
	}
	test := extractCode(resp)
	if strings.TrimSpace(test) == "" {
		return "", errors.New("testgen: writer returned no code")
	}
	return test, nil
}
```

- [ ] **Step 4: Run, watch pass.** `go test ./internal/testgen/...` → PASS. **Commit:** `feat(testgen): LLM test-writer + response code extraction`.

---

## Task 2: the violation-generator + mutant parsing

**Files:**
- Modify: `internal/testgen/testgen.go`, `internal/testgen/parse.go`
- Test: `internal/testgen/testgen_test.go`, `internal/testgen/parse_test.go`

**Interfaces:**
- Produces: `GenerateMutants`, `parseMutants`.
- Consumes: Task 1's `LLM`/`buildUser`/`extractCode`; `internal/adequacy.Mutant`; `fmt`.

- [ ] **Step 1: Failing tests — parseMutants + GenerateMutants.**
```go
func TestParseMutants(t *testing.T) {
	resp := "===MUTATION_1===\npackage target\nfunc F() int { return 1 }\n" +
		"===MUTATION_2===\n```go\npackage target\nfunc F() int { return 2 }\n```\n" +
		"===MUTATION_3===\npackage target\nfunc F() int { return 3 }\n===MUTATION_3_END==="
	muts := parseMutants(resp)
	if len(muts) != 3 {
		t.Fatalf("got %d mutants, want 3: %+v", len(muts), muts)
	}
	if muts[0].ID != "m1" || !strings.Contains(muts[0].Code, "return 1") {
		t.Errorf("m1 wrong: %+v", muts[0])
	}
	if muts[1].ID != "m2" || strings.Contains(muts[1].Code, "```") || !strings.Contains(muts[1].Code, "return 2") {
		t.Errorf("m2 wrong (fence not stripped?): %+v", muts[1])
	}
	if muts[2].ID != "m3" || strings.Contains(muts[2].Code, "MUTATION_3_END") {
		t.Errorf("m3 wrong (trailing marker leaked?): %+v", muts[2])
	}
}

func TestGenerateMutants(t *testing.T) {
	f := &fakeLLM{resp: "===MUTATION_1===\npackage target\nfunc F() int { return 9 }\n===MUTATION_2===\npackage target\nfunc F() int { return 8 }\n"}
	muts, err := GenerateMutants(context.Background(), f, "F returns >0", "package target\nfunc F() int { return 1 }", nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(muts) != 2 || muts[0].ID != "m1" {
		t.Fatalf("mutants wrong: %+v", muts)
	}
	if !strings.Contains(f.gotUser, "2 distinct") { // instruction carried the count
		t.Errorf("generator prompt missing the count instruction: %s", f.gotUser)
	}
}

func TestGenerateMutantsNoneErrors(t *testing.T) {
	if _, err := GenerateMutants(context.Background(), &fakeLLM{resp: "no markers here"}, "g", "c", nil, 3); err == nil {
		t.Fatal("unparseable response must error")
	}
}
```

- [ ] **Step 2: Run, watch fail.** Then implement `parseMutants` in `parse.go`:
```go
// parseMutants splits a "===MUTATION_N===" delimited response into mutants.
// The code for each mutation is the text between its marker and the next
// marker (or end), fence-stripped and trimmed. Empty blocks are skipped; IDs
// are assigned sequentially m1, m2, ... over the kept blocks.
func parseMutants(resp string) []adequacy.Mutant {
	const mark = "===MUTATION_"
	var out []adequacy.Mutant
	parts := strings.Split(resp, mark)
	for _, p := range parts[1:] { // parts[0] is any preamble before the first marker
		// p looks like "1===\n<code>...": drop up to and including the marker's closing "==="
		close := strings.Index(p, "===")
		if close < 0 {
			continue
		}
		body := p[close+3:]
		// A trailing "..._END===" (or the next marker, already split off) may remain — cut at any residual "===".
		if e := strings.Index(body, "==="); e >= 0 {
			body = body[:e]
		}
		code := extractCode(body)
		if strings.TrimSpace(code) == "" {
			continue
		}
		out = append(out, adequacy.Mutant{ID: fmt.Sprintf("m%d", len(out)+1), Code: code})
	}
	return out
}
```
And `GenerateMutants` in `testgen.go`:
```go
const genMutantsSystem = `You are a SEEDED-VIOLATION GENERATOR. Given a GOAL, compliant code, and its signature surface, produce mutations that GENUINELY VIOLATE the goal.
Each mutation MUST keep the EXACT same signature and package (it compiles as a drop-in replacement) and must genuinely violate the goal — vary HOW they violate it. No no-ops, no compile errors, no tests.
Return ONLY the mutations, each a COMPLETE file, in this exact format:
===MUTATION_1===
<complete file>
===MUTATION_2===
<complete file>
(continue for the requested count)`

func GenerateMutants(ctx context.Context, m LLM, goal, code string, sigs []repoindex.Signature, n int) ([]adequacy.Mutant, error) {
	instr := fmt.Sprintf("Produce exactly %d distinct mutations.", n)
	resp, err := m.Ask(ctx, genMutantsSystem, buildUser(goal, code, sigs, instr))
	if err != nil {
		return nil, err
	}
	muts := parseMutants(resp)
	if len(muts) == 0 {
		return nil, errors.New("testgen: generator returned no parseable mutations")
	}
	return muts, nil
}
```
(The test asserts the instruction reads "2 distinct" — the `fmt.Sprintf` above produces "exactly 2 distinct mutations", which contains "2 distinct". Keep that substring.)

- [ ] **Step 3: Run, watch pass.** `go test ./internal/testgen/...` → PASS. Then the full gate: `go vet ./... && go build ./... && go test ./... && bash scripts/check-security.sh`. **Commit:** `feat(testgen): violation-generator + mutant parsing`.

---

## Self-Review
- **Spec coverage (§4d writer + violation-generator):** the two independent generative agents, each spike-proven, as injected-LLM functions ✓; writer→candidate test, generator→`adequacy.Mutant`s ✓; deterministic under a fake LLM ✓; generation-only (validation stays in adequacy) ✓; writer/generator kept as separate calls/prompts (independence) ✓.
- **No placeholders:** complete prompts + parsing + fake-LLM tests.
- **Type consistency:** `LLM`/`WriteTest`/`GenerateMutants` stable; returns `adequacy.Mutant`; consumes `repoindex.Signature`.
- **Determinism:** no live model, no `time.Now()`; parsing is pure over the response string; mutant IDs are sequential over kept blocks (stable).
- **DRY note:** testgen's one-method `LLM` interface duplicates `oracle.LLM`/`learn.Asker` in *declaration only* — this is the idiomatic Go "accept a consumer-side interface" pattern already used twice in this codebase, not logic duplication. A future consolidation into a shared `llm.Asker` could unify all three, but that's a separate refactor across oracle/learn; not in scope here.
- **Out of scope (later plans):** compiling/scoring the output (adequacy); the reviewer triage + feedback loop + human approval; the gate dimension; goal→file mapping; the model wiring (a capable model behind the `LLM` interface).

<!-- SPDX-License-Identifier: Elastic-2.0 -->
# Eval harness — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `corral eval` — run the adversarial pool across a versioned, content-addressed corpus (including known-adequacy targets) to give the bug-catching scorecard volume, and print a soundness report that validates the metric against ground truth.

**Architecture:** A pure `internal/eval` package (corpus loader + digest, resumable harness loop over an injected `PoolRunner`, soundness report), a committed `eval/corpus/` (manifest + self-contained Go targets with thorough/gappy test variants), and a `cmd/corral/eval.go` verb that adapts the existing `mcpAdvClient` (start_adversarial_run + poll) to `eval.PoolRunner` and prints the report. Reuses the shipped pool→scorecard feed; adds no judgement path and no scorecard-schema change.

**Tech Stack:** Go; the existing `cmd/corral/certify_adversarial.go` pool-trigger client; the shipped `internal/bugcatch` scorecard (fed automatically by every run).

## Global Constraints

- **The harness adds NO judgement.** It only triggers existing pool runs; every catch is still `proven_missed` (execution-proven), fed through the reviewed `BugCatchSink`. No scorecard-schema change; provenance rides in the run's `Repo`/`Commit` metadata (`Repo="eval:<corpus_version>"`, `Commit="<target_id>@<target_digest>"`).
- **`internal/eval` is pure + testable:** it depends on a small `PoolRunner` interface it OWNS (not package `main`'s `advPoolClient`). The CLI adapts the real client to it. `internal/eval` imports nothing from `cmd/`.
- **Corpus is versioned + content-addressed.** `target_digest = sha256(code || 0x00 || test || 0x00 || goal || 0x00 || test_cmd)`, hex. Results are only comparable within the same `corpus_version` + `target_digest`.
- **Known-adequacy targets are calibration.** A `thorough` target's dev test must kill every mutant (expect ~0 survivors); a `gappy` target's dev test misses a specific behavior (expect survivors ≥ its `expected_survivors`). Both test variants PASS on the correct (unmutated) code — a gappy test is valid, just incomplete.
- **Resumable + cost-bounded.** A git-ignored progress file (`eval/.eval-progress.json`) records completed `(corpus_version, target_id, iteration)` so re-runs top up rather than restart. The harness prints the run plan up front (`M targets × N iterations = K runs`) and honors `--iterations`/`--only`. It never runs unbounded.
- **Driver, not worker.** `corral eval` needs a reachable brain (`CORRAL_BRAIN`) + bearer (same path `certify` uses) + a running herd; it fails with ONE clear line if they're absent (no error spam).

---

### Task 1: Corpus manifest + loader + digest

**Files:**
- Create: `internal/eval/corpus.go`
- Test: `internal/eval/corpus_test.go`

**Interfaces:**
- Produces:
  - `type Target struct { ID, CodePath, TestPath, Goal, TestCmd, ExpectedAdequacy, KnownGap string; ExpectedSurvivors int; NMutants int }`
  - `type Manifest struct { CorpusVersion string; Targets []Target }`
  - `func Load(manifestPath string) (Manifest, error)` — parses JSON, resolves+reads each target's code/test files relative to the repo root (the manifest's dir's parent chain), validates required fields, returns a clear error on a missing file or a duplicate id.
  - `func (t Target) Code() string`, `func (t Target) TestCode() string` — the file contents (cached at Load).
  - `func (t Target) Digest() string` — the content-addressed hex digest defined in Global Constraints.

- [ ] **Step 1: Write the failing test**

```go
// internal/eval/corpus_test.go
package eval

import (
	"os"
	"path/filepath"
	"testing"
)

func writeCorpus(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	must := func(p, s string) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil { t.Fatal(err) }
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil { t.Fatal(err) }
	}
	must(filepath.Join(dir, "x/x.go"), "package x\nfunc F() bool { return true }\n")
	must(filepath.Join(dir, "x/x_test.go"), "package x\nimport \"testing\"\nfunc TestF(t *testing.T){ if !F(){t.Fatal(\"x\")} }\n")
	man := `{"corpus_version":"v1","targets":[
	  {"id":"x","code_path":"x/x.go","test_path":"x/x_test.go","goal":"F is true","test_cmd":"go test ./x/...","expected_adequacy":"thorough"}
	]}`
	mp := filepath.Join(dir, "manifest.json")
	must(mp, man)
	return mp
}

func TestLoadResolvesFilesAndDigest(t *testing.T) {
	mp := writeCorpus(t)
	m, err := Load(mp)
	if err != nil { t.Fatal(err) }
	if m.CorpusVersion != "v1" || len(m.Targets) != 1 { t.Fatalf("bad manifest: %+v", m) }
	tg := m.Targets[0]
	if tg.Code() == "" || tg.TestCode() == "" { t.Fatal("files not read") }
	d1 := tg.Digest()
	if len(d1) != 64 { t.Fatalf("digest not sha256 hex: %q", d1) }
	// Digest is stable and content-addressed: same inputs → same digest.
	m2, _ := Load(mp)
	if m2.Targets[0].Digest() != d1 { t.Fatal("digest not stable") }
}

func TestLoadRejectsMissingFileAndDupID(t *testing.T) {
	dir := t.TempDir()
	mp := filepath.Join(dir, "m.json")
	os.WriteFile(mp, []byte(`{"corpus_version":"v1","targets":[{"id":"a","code_path":"nope.go","test_path":"nope_test.go","goal":"g","test_cmd":"go test"}]}`), 0o644)
	if _, err := Load(mp); err == nil { t.Fatal("expected error for missing file") }
}
```

- [ ] **Step 2: Run it to see it fail** — `go test ./internal/eval/ -run TestLoad -v` → FAIL (undefined).

- [ ] **Step 3: Implement `internal/eval/corpus.go`**

```go
// internal/eval/corpus.go
// SPDX-License-Identifier: Elastic-2.0

// Package eval runs the adversarial pool across a versioned, content-addressed
// corpus to give the bug-catching scorecard volume, and validates the metric
// against known-adequacy targets. It adds no judgement — it only triggers
// existing pool runs — and depends on a small PoolRunner interface it owns.
package eval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Target struct {
	ID                string `json:"id"`
	CodePath          string `json:"code_path"`
	TestPath          string `json:"test_path"`
	Goal              string `json:"goal"`
	TestCmd           string `json:"test_cmd"`
	ExpectedAdequacy  string `json:"expected_adequacy"` // thorough | gappy | unknown
	KnownGap          string `json:"known_gap,omitempty"`
	ExpectedSurvivors int    `json:"expected_survivors,omitempty"`
	NMutants          int    `json:"n_mutants,omitempty"`

	code, testCode string // filled at Load, relative to the manifest's directory
}

type Manifest struct {
	CorpusVersion string   `json:"corpus_version"`
	Targets       []Target `json:"targets"`
}

func Load(manifestPath string) (Manifest, error) {
	raw, err := os.ReadFile(manifestPath) // #nosec G304 -- operator-supplied corpus path
	if err != nil {
		return Manifest{}, fmt.Errorf("eval: read manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Manifest{}, fmt.Errorf("eval: parse manifest: %w", err)
	}
	if m.CorpusVersion == "" {
		return Manifest{}, fmt.Errorf("eval: manifest has no corpus_version")
	}
	base := filepath.Dir(manifestPath)
	seen := map[string]bool{}
	for i := range m.Targets {
		t := &m.Targets[i]
		if t.ID == "" || t.CodePath == "" || t.TestPath == "" || t.Goal == "" || t.TestCmd == "" {
			return Manifest{}, fmt.Errorf("eval: target %d missing a required field", i)
		}
		if seen[t.ID] {
			return Manifest{}, fmt.Errorf("eval: duplicate target id %q", t.ID)
		}
		seen[t.ID] = true
		code, err := os.ReadFile(filepath.Join(base, t.CodePath)) // #nosec G304
		if err != nil {
			return Manifest{}, fmt.Errorf("eval: target %s code %s: %w", t.ID, t.CodePath, err)
		}
		test, err := os.ReadFile(filepath.Join(base, t.TestPath)) // #nosec G304
		if err != nil {
			return Manifest{}, fmt.Errorf("eval: target %s test %s: %w", t.ID, t.TestPath, err)
		}
		t.code, t.testCode = string(code), string(test)
		if t.NMutants == 0 {
			t.NMutants = 8
		}
	}
	return m, nil
}

func (t Target) Code() string     { return t.code }
func (t Target) TestCode() string { return t.testCode }

func (t Target) Digest() string {
	h := sha256.New()
	for _, s := range []string{t.code, t.testCode, t.Goal, t.TestCmd} {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
```

> **Implementer note:** the manifest paths are resolved relative to the manifest file's own directory (so `eval/corpus/manifest.json` with `code_path:"passwd/passwd.go"` resolves to `eval/corpus/passwd/passwd.go`). For the real `fence` target whose `code_path` is `internal/fence/fence.go` (outside `eval/corpus/`), the path is relative to the manifest dir too — so its `code_path` in the real manifest is `../../internal/fence/fence.go`. Verify that resolves; if you'd rather resolve real-repo targets from the repo root, add a `root` field or a `../` convention and document it — keep it simple and correct.

- [ ] **Step 4: Run the tests to green** — `go test ./internal/eval/ -run TestLoad -v` → PASS.

- [ ] **Step 5: Commit** — `git add internal/eval/corpus.go internal/eval/corpus_test.go && git commit -m "eval: corpus manifest loader + content-addressed target digest"`

---

### Task 2: The corpus content (known-adequacy targets + manifest)

**Files:**
- Create: `eval/corpus/passwd/passwd.go`, `eval/corpus/passwd/passwd_thorough_test.go`, `eval/corpus/passwd/passwd_gappy_test.go`
- Create: `eval/corpus/interval/interval.go`, `eval/corpus/interval/interval_thorough_test.go`, `eval/corpus/interval/interval_gappy_test.go`
- Create: `eval/corpus/manifest.json`
- Modify: `.gitignore` (add `eval/.eval-progress.json`)

**Interfaces:**
- Consumes: the manifest format from Task 1 (`Manifest`/`Target` fields).
- Produces: a loadable, versioned corpus of 5 targets (fence + passwd×2 + interval×2).

- [ ] **Step 1: Write the corpus code + BOTH test variants**

`eval/corpus/passwd/passwd.go`:
```go
// SPDX-License-Identifier: Elastic-2.0
package passwd

import "unicode"

// Valid reports whether p is a valid password: length >= 12 AND it contains an
// uppercase letter, a lowercase letter, a digit, and a symbol.
func Valid(p string) bool {
	if len(p) < 12 {
		return false
	}
	var up, lo, di, sy bool
	for _, r := range p {
		switch {
		case unicode.IsUpper(r):
			up = true
		case unicode.IsLower(r):
			lo = true
		case unicode.IsDigit(r):
			di = true
		case unicode.IsPunct(r) || unicode.IsSymbol(r):
			sy = true
		}
	}
	return up && lo && di && sy
}
```

`eval/corpus/passwd/passwd_thorough_test.go` — kills class-drop AND length mutants:
```go
// SPDX-License-Identifier: Elastic-2.0
package passwd

import "testing"

func TestThorough_Valid(t *testing.T) {
	if !Valid("Abcdefgh1!xy") { // 12 chars, all classes
		t.Fatal("a valid password was rejected")
	}
}
func TestThorough_TooShort(t *testing.T)   { if Valid("Ab1!xyz") { t.Fatal("short accepted") } }
func TestThorough_NoUpper(t *testing.T)    { if Valid("abcdefgh1!xy") { t.Fatal("no-upper accepted") } }
func TestThorough_NoLower(t *testing.T)    { if Valid("ABCDEFGH1!XY") { t.Fatal("no-lower accepted") } }
func TestThorough_NoDigit(t *testing.T)    { if Valid("Abcdefghij!x") { t.Fatal("no-digit accepted") } }
func TestThorough_NoSymbol(t *testing.T)   { if Valid("Abcdefgh12xy") { t.Fatal("no-symbol accepted") } }
```

`eval/corpus/passwd/passwd_gappy_test.go` — checks ONLY length (the planted gap: never tests the character-class requirement):
```go
// SPDX-License-Identifier: Elastic-2.0
package passwd

import "testing"

func TestGappy_ValidLength(t *testing.T) {
	if !Valid("Abcdefgh1!xy") {
		t.Fatal("a 12-char password was rejected")
	}
}
func TestGappy_TooShort(t *testing.T) {
	if Valid("Ab1!xyz") {
		t.Fatal("a 7-char password was accepted")
	}
}
```

`eval/corpus/interval/interval.go`:
```go
// SPDX-License-Identifier: Elastic-2.0
package interval

// Contains reports whether x is within the inclusive range [lo, hi].
func Contains(lo, hi, x int) bool { return x >= lo && x <= hi }
```

`eval/corpus/interval/interval_thorough_test.go` — kills boundary mutants (`>=`→`>`, `<=`→`<`):
```go
// SPDX-License-Identifier: Elastic-2.0
package interval

import "testing"

func TestThorough_LowerBoundary(t *testing.T)  { if !Contains(1, 10, 1) { t.Fatal("lo excluded") } }
func TestThorough_UpperBoundary(t *testing.T)  { if !Contains(1, 10, 10) { t.Fatal("hi excluded") } }
func TestThorough_Inside(t *testing.T)         { if !Contains(1, 10, 5) { t.Fatal("inside excluded") } }
func TestThorough_Below(t *testing.T)          { if Contains(1, 10, 0) { t.Fatal("below included") } }
func TestThorough_Above(t *testing.T)          { if Contains(1, 10, 11) { t.Fatal("above included") } }
```

`eval/corpus/interval/interval_gappy_test.go` — checks only well-inside/well-outside (the planted gap: never tests the boundaries):
```go
// SPDX-License-Identifier: Elastic-2.0
package interval

import "testing"

func TestGappy_Inside(t *testing.T)  { if !Contains(1, 10, 5) { t.Fatal("inside excluded") } }
func TestGappy_Outside(t *testing.T) { if Contains(1, 10, 100) { t.Fatal("far-outside included") } }
```

- [ ] **Step 2: Verify the corpus compiles and BOTH variants pass on the correct code**

Run: `go test ./eval/corpus/...`
Expected: PASS — every target's tests (thorough AND gappy) pass on the unmutated code. (A gappy test is a *valid* test; it just misses a mutant. If a gappy test FAILS here, it's wrong.)

- [ ] **Step 3: Write the manifest**

`eval/corpus/manifest.json`:
```json
{
  "corpus_version": "2026-07-17.1",
  "targets": [
    {
      "id": "fence-neutralization",
      "code_path": "../../internal/fence/fence.go",
      "test_path": "../../internal/fence/fence_test.go",
      "goal": "Untrusted content cannot forge or close the fence: every occurrence of the sentinel in content is neutralized before wrapping, so wrapped text can never escape the untrusted-data fence.",
      "test_cmd": "go test ./internal/fence/...",
      "expected_adequacy": "thorough",
      "n_mutants": 8
    },
    {
      "id": "passwd-thorough",
      "code_path": "passwd/passwd.go",
      "test_path": "passwd/passwd_thorough_test.go",
      "goal": "A password is valid iff length >= 12 AND it contains an upper, a lower, a digit, and a symbol.",
      "test_cmd": "go test ./eval/corpus/passwd/... -run Thorough",
      "expected_adequacy": "thorough",
      "n_mutants": 8
    },
    {
      "id": "passwd-gappy",
      "code_path": "passwd/passwd.go",
      "test_path": "passwd/passwd_gappy_test.go",
      "goal": "A password is valid iff length >= 12 AND it contains an upper, a lower, a digit, and a symbol.",
      "test_cmd": "go test ./eval/corpus/passwd/... -run Gappy",
      "expected_adequacy": "gappy",
      "known_gap": "the suite only checks length; a mutant that drops the character-class requirement survives — a good test-writer should catch it.",
      "expected_survivors": 1,
      "n_mutants": 8
    },
    {
      "id": "interval-thorough",
      "code_path": "interval/interval.go",
      "test_path": "interval/interval_thorough_test.go",
      "goal": "Contains(lo,hi,x) reports whether x is within the inclusive range [lo,hi].",
      "test_cmd": "go test ./eval/corpus/interval/... -run Thorough",
      "expected_adequacy": "thorough",
      "n_mutants": 8
    },
    {
      "id": "interval-gappy",
      "code_path": "interval/interval.go",
      "test_path": "interval/interval_gappy_test.go",
      "goal": "Contains(lo,hi,x) reports whether x is within the inclusive range [lo,hi].",
      "test_cmd": "go test ./eval/corpus/interval/... -run Gappy",
      "expected_adequacy": "gappy",
      "known_gap": "the suite never tests the boundaries; a mutant that turns >= into > (excluding lo) or <= into < survives.",
      "expected_survivors": 1,
      "n_mutants": 8
    }
  ]
}
```

- [ ] **Step 4: Verify the manifest loads via Task 1's loader**

Add to `internal/eval/corpus_test.go`:
```go
func TestRealCorpusManifestLoads(t *testing.T) {
	m, err := Load("../../eval/corpus/manifest.json")
	if err != nil { t.Fatal(err) }
	if len(m.Targets) != 5 { t.Fatalf("want 5 targets, got %d", len(m.Targets)) }
	for _, tg := range m.Targets {
		if tg.Code() == "" || tg.TestCode() == "" { t.Fatalf("target %s files empty", tg.ID) }
		if len(tg.Digest()) != 64 { t.Fatalf("target %s bad digest", tg.ID) }
	}
}
```
Run: `go test ./internal/eval/ -run TestRealCorpus -v` → PASS. Also `go test ./eval/corpus/...` → PASS.

Add `eval/.eval-progress.json` to `.gitignore`.

- [ ] **Step 5: Commit** — `git add eval/corpus .gitignore internal/eval/corpus_test.go && git commit -m "eval: known-adequacy corpus (fence + passwd/interval thorough+gappy) + manifest"`

---

### Task 3: Resumable harness loop

**Files:**
- Create: `internal/eval/progress.go`, `internal/eval/harness.go`
- Test: `internal/eval/harness_test.go`

**Interfaces:**
- Consumes: `Manifest`/`Target` (Task 1).
- Produces:
  - `type RunResult struct { TargetID string; Iteration int; Status string; DevKillRate float64; MutantsTotal, Survivors, ProvenMissed int; RecordID int64 }`
  - `type PoolRunner interface { RunOne(ctx context.Context, t Target) (RunResult, error) }`
  - `type Config struct { Iterations int; Only []string; ProgressPath string }`
  - `type Progress struct { ... }` with `loadProgress(path) (*Progress, error)`, `(*Progress) done(corpusVersion, targetID string, iter int) bool`, `(*Progress) mark(...) error` (persists after each mark).
  - `func Run(ctx context.Context, m Manifest, cfg Config, runner PoolRunner, out io.Writer) ([]RunResult, error)` — iterates selected targets × iterations, skipping completed ones, calling `runner.RunOne`, marking progress, collecting results; prints the plan + a per-run progress line to `out`.

- [ ] **Step 1: Write the failing test**

```go
// internal/eval/harness_test.go
package eval

import (
	"bytes"
	"context"
	"testing"
)

type fakeRunner struct {
	calls   int
	byID    map[string]RunResult // canned result per target id
}

func (f *fakeRunner) RunOne(_ context.Context, t Target) (RunResult, error) {
	f.calls++
	r := f.byID[t.ID]
	r.TargetID = t.ID
	return r, nil
}

func fakeManifest() Manifest {
	return Manifest{CorpusVersion: "v1", Targets: []Target{
		{ID: "a", Goal: "g", TestCmd: "c", code: "x", testCode: "y"},
		{ID: "b", Goal: "g", TestCmd: "c", code: "x", testCode: "y"},
	}}
}

func TestRunIteratesAndBoundsByOnlyAndIterations(t *testing.T) {
	f := &fakeRunner{byID: map[string]RunResult{"a": {Survivors: 1}}}
	cfg := Config{Iterations: 2, Only: []string{"a"}, ProgressPath: t.TempDir() + "/p.json"}
	res, err := Run(context.Background(), fakeManifest(), cfg, f, &bytes.Buffer{})
	if err != nil { t.Fatal(err) }
	if f.calls != 2 || len(res) != 2 { t.Fatalf("--only a --iterations 2 → want 2 runs, got calls=%d res=%d", f.calls, len(res)) }
	for _, r := range res { if r.TargetID != "a" { t.Fatalf("ran wrong target: %s", r.TargetID) } }
}

func TestRunResumesFromProgress(t *testing.T) {
	pp := t.TempDir() + "/p.json"
	f1 := &fakeRunner{byID: map[string]RunResult{}}
	cfg := Config{Iterations: 2, Only: []string{"a"}, ProgressPath: pp}
	Run(context.Background(), fakeManifest(), cfg, f1, &bytes.Buffer{})
	// Second invocation with the SAME progress file + iterations: nothing new to do.
	f2 := &fakeRunner{byID: map[string]RunResult{}}
	res, _ := Run(context.Background(), fakeManifest(), cfg, f2, &bytes.Buffer{})
	if f2.calls != 0 || len(res) != 0 { t.Fatalf("resume: want 0 new runs, got %d", f2.calls) }
}
```

- [ ] **Step 2: Run it to see it fail** — `go test ./internal/eval/ -run TestRun -v` → FAIL (undefined).

- [ ] **Step 3: Implement `progress.go` + `harness.go`**

```go
// internal/eval/progress.go
// SPDX-License-Identifier: Elastic-2.0
package eval

import (
	"encoding/json"
	"fmt"
	"os"
)

// Progress is a git-ignored record of completed (corpus_version, target, iter)
// so a re-run tops up rather than restarting. Persisted after each mark.
type Progress struct {
	path string
	Done map[string]bool `json:"done"` // key = corpusVersion + "|" + targetID + "|" + iter
}

func progKey(corpusVersion, targetID string, iter int) string {
	return fmt.Sprintf("%s|%s|%d", corpusVersion, targetID, iter)
}

func loadProgress(path string) (*Progress, error) {
	p := &Progress{path: path, Done: map[string]bool{}}
	raw, err := os.ReadFile(path) // #nosec G304
	if os.IsNotExist(err) {
		return p, nil
	}
	if err != nil {
		return nil, fmt.Errorf("eval: read progress: %w", err)
	}
	if err := json.Unmarshal(raw, p); err != nil || p.Done == nil {
		// Corrupt/empty progress → start fresh rather than fail the run.
		p.Done = map[string]bool{}
	}
	return p, nil
}

func (p *Progress) done(corpusVersion, targetID string, iter int) bool {
	return p.Done[progKey(corpusVersion, targetID, iter)]
}

func (p *Progress) mark(corpusVersion, targetID string, iter int) error {
	p.Done[progKey(corpusVersion, targetID, iter)] = true
	raw, _ := json.MarshalIndent(p, "", "  ")
	return os.WriteFile(p.path, raw, 0o644)
}
```

```go
// internal/eval/harness.go
// SPDX-License-Identifier: Elastic-2.0
package eval

import (
	"context"
	"fmt"
	"io"
)

type RunResult struct {
	TargetID     string
	Iteration    int
	Status       string
	DevKillRate  float64
	MutantsTotal int
	Survivors    int
	ProvenMissed int
	RecordID     int64
}

// PoolRunner triggers ONE adversarial-pool run for a target and returns its
// verdict. The CLI implements this over the real brain client; tests fake it.
type PoolRunner interface {
	RunOne(ctx context.Context, t Target) (RunResult, error)
}

type Config struct {
	Iterations   int
	Only         []string // target ids; empty = all
	ProgressPath string
}

func selected(m Manifest, only []string) []Target {
	if len(only) == 0 {
		return m.Targets
	}
	want := map[string]bool{}
	for _, id := range only {
		want[id] = true
	}
	var out []Target
	for _, t := range m.Targets {
		if want[t.ID] {
			out = append(out, t)
		}
	}
	return out
}

func Run(ctx context.Context, m Manifest, cfg Config, runner PoolRunner, out io.Writer) ([]RunResult, error) {
	if cfg.Iterations < 1 {
		cfg.Iterations = 1
	}
	prog, err := loadProgress(cfg.ProgressPath)
	if err != nil {
		return nil, err
	}
	targets := selected(m, cfg.Only)
	// Count the actual remaining work for the cost plan.
	remaining := 0
	for _, t := range targets {
		for i := 1; i <= cfg.Iterations; i++ {
			if !prog.done(m.CorpusVersion, t.ID, i) {
				remaining++
			}
		}
	}
	fmt.Fprintf(out, "eval: %d target(s) × %d iteration(s), %d run(s) to trigger (corpus %s)\n",
		len(targets), cfg.Iterations, remaining, m.CorpusVersion)

	var results []RunResult
	n := 0
	for _, t := range targets {
		for i := 1; i <= cfg.Iterations; i++ {
			if prog.done(m.CorpusVersion, t.ID, i) {
				continue
			}
			n++
			fmt.Fprintf(out, "eval: [%d/%d] %s iter %d…\n", n, remaining, t.ID, i)
			r, err := runner.RunOne(ctx, t)
			if err != nil {
				return results, fmt.Errorf("eval: run %s iter %d: %w", t.ID, i, err)
			}
			r.TargetID, r.Iteration = t.ID, i
			results = append(results, r)
			if err := prog.mark(m.CorpusVersion, t.ID, i); err != nil {
				return results, err
			}
		}
	}
	return results, nil
}
```

- [ ] **Step 4: Run the tests to green** — `go test ./internal/eval/ -run TestRun -v` → PASS.

- [ ] **Step 5: Commit** — `git add internal/eval/progress.go internal/eval/harness.go internal/eval/harness_test.go && git commit -m "eval: resumable, cost-bounded harness loop over an injected PoolRunner"`

---

### Task 4: The soundness report

**Files:**
- Create: `internal/eval/report.go`
- Test: `internal/eval/report_test.go`

**Interfaces:**
- Consumes: `[]RunResult` (Task 3) + `Manifest` (for each target's `ExpectedAdequacy`/`ExpectedSurvivors`).
- Produces:
  - `type TargetReport struct { ID, ExpectedAdequacy string; Runs int; MeanKillRate, MeanSurvivors, MeanProvenMissed float64; Calibrated bool; Note string }`
  - `func Report(m Manifest, results []RunResult) []TargetReport` — per target: aggregate the means, and set `Calibrated`/`Note` by the ground-truth check (thorough ⇒ mean survivors ≈ 0; gappy ⇒ mean survivors ≥ its ExpectedSurvivors). A violation sets `Calibrated=false` with a LOUD note.
  - `func WriteReport(out io.Writer, reps []TargetReport)` — a table + a headline "CALIBRATED" / "MISCALIBRATED (N targets)" line.

- [ ] **Step 1: Write the failing test**

```go
// internal/eval/report_test.go
package eval

import "testing"

func TestReportFlagsMiscalibration(t *testing.T) {
	m := Manifest{CorpusVersion: "v1", Targets: []Target{
		{ID: "thorough-ok", ExpectedAdequacy: "thorough"},
		{ID: "gappy-ok", ExpectedAdequacy: "gappy", ExpectedSurvivors: 1},
		{ID: "gappy-BROKEN", ExpectedAdequacy: "gappy", ExpectedSurvivors: 1},
		{ID: "thorough-BROKEN", ExpectedAdequacy: "thorough"},
	}}
	res := []RunResult{
		{TargetID: "thorough-ok", Survivors: 0},
		{TargetID: "gappy-ok", Survivors: 2},        // has the gap → calibrated
		{TargetID: "gappy-BROKEN", Survivors: 0},    // gappy but pool found NO gap → miscalibrated
		{TargetID: "thorough-BROKEN", Survivors: 3}, // thorough but riddled with survivors → miscalibrated
	}
	reps := Report(m, res)
	byID := map[string]TargetReport{}
	for _, r := range reps { byID[r.ID] = r }
	if !byID["thorough-ok"].Calibrated || !byID["gappy-ok"].Calibrated {
		t.Fatal("well-behaved targets must be calibrated")
	}
	if byID["gappy-BROKEN"].Calibrated {
		t.Fatal("a gappy target with 0 survivors must be flagged miscalibrated (metric under-sensitive)")
	}
	if byID["thorough-BROKEN"].Calibrated {
		t.Fatal("a thorough target riddled with survivors must be flagged (metric over-sensitive)")
	}
}
```

- [ ] **Step 2: Run it to see it fail** — `go test ./internal/eval/ -run TestReport -v` → FAIL (undefined).

- [ ] **Step 3: Implement `report.go`**

```go
// internal/eval/report.go
// SPDX-License-Identifier: Elastic-2.0
package eval

import (
	"fmt"
	"io"
	"text/tabwriter"
)

type TargetReport struct {
	ID               string
	ExpectedAdequacy string
	Runs             int
	MeanKillRate     float64
	MeanSurvivors    float64
	MeanProvenMissed float64
	Calibrated       bool
	Note             string
}

// thoroughSurvivorTolerance: a thorough target may occasionally leave a stray
// survivor (LLM mutant variance); above this mean it's over-sensitive/miscalibrated.
const thoroughSurvivorTolerance = 0.5

func Report(m Manifest, results []RunResult) []TargetReport {
	adeq := map[string]Target{}
	for _, t := range m.Targets {
		adeq[t.ID] = t
	}
	agg := map[string]*TargetReport{}
	order := []string{}
	for _, r := range results {
		rep, ok := agg[r.TargetID]
		if !ok {
			t := adeq[r.TargetID]
			rep = &TargetReport{ID: r.TargetID, ExpectedAdequacy: t.ExpectedAdequacy}
			agg[r.TargetID] = rep
			order = append(order, r.TargetID)
		}
		rep.Runs++
		rep.MeanKillRate += r.DevKillRate
		rep.MeanSurvivors += float64(r.Survivors)
		rep.MeanProvenMissed += float64(r.ProvenMissed)
	}
	var out []TargetReport
	for _, id := range order {
		rep := agg[id]
		if rep.Runs > 0 {
			rep.MeanKillRate /= float64(rep.Runs)
			rep.MeanSurvivors /= float64(rep.Runs)
			rep.MeanProvenMissed /= float64(rep.Runs)
		}
		t := adeq[id]
		switch rep.ExpectedAdequacy {
		case "thorough":
			if rep.MeanSurvivors <= thoroughSurvivorTolerance {
				rep.Calibrated = true
			} else {
				rep.Note = fmt.Sprintf("thorough target has mean %.2f survivors — pool is inventing gaps (over-sensitive)", rep.MeanSurvivors)
			}
		case "gappy":
			if rep.MeanSurvivors >= float64(t.ExpectedSurvivors) {
				rep.Calibrated = true
			} else {
				rep.Note = fmt.Sprintf("gappy target has mean %.2f survivors (< expected %d) — pool MISSED a known gap (under-sensitive)", rep.MeanSurvivors, t.ExpectedSurvivors)
			}
		default:
			rep.Calibrated = true // "unknown" adequacy isn't a calibration target
		}
		out = append(out, *rep)
	}
	return out
}

func WriteReport(out io.Writer, reps []TargetReport) {
	bad := 0
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "TARGET\tEXPECTED\tRUNS\tKILL-RATE\tSURVIVORS\tPROVEN\tCALIBRATED\t")
	for _, r := range reps {
		cal := "yes"
		if !r.Calibrated {
			cal = "NO — " + r.Note
			bad++
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%.2f\t%.2f\t%.2f\t%s\t\n",
			r.ID, r.ExpectedAdequacy, r.Runs, r.MeanKillRate, r.MeanSurvivors, r.MeanProvenMissed, cal)
	}
	tw.Flush()
	if bad == 0 {
		fmt.Fprintln(out, "\nCALIBRATED — the corpus behaves as its known adequacy predicts; the scorecard's signal is sound.")
	} else {
		fmt.Fprintf(out, "\nMISCALIBRATED — %d target(s) violated their known adequacy. Do NOT publish the scorecard until resolved.\n", bad)
	}
}
```

- [ ] **Step 4: Run the tests to green** — `go test ./internal/eval/ -run TestReport -v && go test ./internal/eval/ -count=1` → PASS.

- [ ] **Step 5: Commit** — `git add internal/eval/report.go internal/eval/report_test.go && git commit -m "eval: soundness report — validate the corpus against its known adequacy"`

---

### Task 5: `corral eval` CLI verb + the real PoolRunner

**Files:**
- Create: `cmd/corral/eval.go`
- Modify: `cmd/corral/main.go` (dispatch `eval` in `subcommand()` + `main()`)
- Test: `cmd/corral/eval_test.go`

**Interfaces:**
- Consumes: `internal/eval` (`Load`, `Run`, `Report`, `WriteReport`, `PoolRunner`, `Config`, `Target`, `RunResult`); the existing `mcpAdvClient` + `advStartSpec`/`advStatus` (package main).
- Produces:
  - a `mcpPoolRunner` in package main that implements `eval.PoolRunner` by building an `advStartSpec` from a `eval.Target` (stamping `Repo="eval:<corpus_version>"`, `Commit="<target_id>@<target_digest>"`), calling `mcpAdvClient.StartRun`, polling `RunStatus` to convergence, and mapping the `advVerdict` → `eval.RunResult`.
  - `func runEval(args []string, newRunner func(brainURL, corpusVersion string) eval.PoolRunner, stdout, stderr io.Writer) int` — parses flags (`--corpus`, `--iterations`, `--only`, `--brain`, `--timeout`, `--poll`), loads the manifest, runs the harness, prints the soundness report. `newRunner` is injected so a test can supply a fake.

- [ ] **Step 1: Write the failing test**

```go
// cmd/corral/eval_test.go
package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/eval"
)

type fakeEvalRunner struct{ calls int }

func (f *fakeEvalRunner) RunOne(_ context.Context, t eval.Target) (eval.RunResult, error) {
	f.calls++
	// gappy target has a gap, thorough doesn't — so the report calibrates.
	if strings.Contains(t.ID, "gappy") {
		return eval.RunResult{Survivors: 1, ProvenMissed: 1}, nil
	}
	return eval.RunResult{Survivors: 0, DevKillRate: 1.0}, nil
}

func TestRunEvalDrivesHarnessAndPrintsCalibratedReport(t *testing.T) {
	f := &fakeEvalRunner{}
	var out, errb bytes.Buffer
	rc := runEval(
		[]string{"--corpus", "../../eval/corpus/manifest.json", "--iterations", "1",
			"--only", "passwd-thorough,passwd-gappy", "--progress", t.TempDir() + "/p.json"},
		func(_, _ string) eval.PoolRunner { return f },
		&out, &errb)
	if rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, errb.String())
	}
	if f.calls != 2 {
		t.Fatalf("want 2 runs (2 targets × 1 iter), got %d", f.calls)
	}
	if !strings.Contains(out.String(), "CALIBRATED") {
		t.Fatalf("report should render calibration verdict:\n%s", out.String())
	}
}
```

> **Implementer note:** add a `--progress` flag (default `eval/.eval-progress.json`) so the test can point at a temp file. The test loads the REAL committed manifest (Task 2), so it exercises the loader end-to-end with a fake runner (no brain needed).

- [ ] **Step 2: Run it to see it fail** — `go test ./cmd/corral/ -run TestRunEval -v` → FAIL (undefined).

- [ ] **Step 3: Implement `cmd/corral/eval.go` + dispatch**

```go
// cmd/corral/eval.go
// SPDX-License-Identifier: Elastic-2.0
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/eval"
)

// mcpPoolRunner adapts the real adversarial-pool client to eval.PoolRunner:
// one converged run per target, verdict mapped to eval.RunResult. Provenance
// (corpus version + target digest) rides in the run's Repo/Commit metadata.
type mcpPoolRunner struct {
	client        advPoolClient
	brainURL      string
	corpusVersion string
	poll, timeout time.Duration
}

func (r mcpPoolRunner) RunOne(ctx context.Context, t eval.Target) (eval.RunResult, error) {
	spec := advStartSpec{
		Repo:        "eval:" + r.corpusVersion,
		Commit:      t.ID + "@" + t.Digest(),
		Goal:        t.Goal,
		CodePath:    t.CodePath,
		Code:        t.Code(),
		DevTestPath: t.TestPath,
		DevTestCode: t.TestCode(),
		TestCmd:     t.TestCmd,
		NMutants:    t.NMutants,
	}
	runID, err := r.client.StartRun(ctx, r.brainURL, spec)
	if err != nil {
		return eval.RunResult{}, err
	}
	deadline := time.Now().Add(r.timeout)
	for {
		st, err := r.client.RunStatus(ctx, r.brainURL, runID)
		if err != nil {
			return eval.RunResult{}, err
		}
		if st.Converged && st.Verdict != nil {
			v := st.Verdict
			return eval.RunResult{
				Status: v.Status, DevKillRate: v.DevKillRate, MutantsTotal: v.MutantsTotal,
				Survivors: v.Survivors, ProvenMissed: v.ProvenMissed, RecordID: v.RecordID,
			}, nil
		}
		if time.Now().After(deadline) {
			return eval.RunResult{}, fmt.Errorf("run %d did not converge within %s", runID, r.timeout)
		}
		time.Sleep(r.poll)
	}
}

func runEval(args []string, newRunner func(brainURL, corpusVersion string) eval.PoolRunner, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("eval", flag.ContinueOnError)
	fs.SetOutput(stderr)
	corpus := fs.String("corpus", "eval/corpus/manifest.json", "corpus manifest path")
	iterations := fs.Int("iterations", 1, "iterations per target")
	only := fs.String("only", "", "comma-separated target ids (default: all)")
	brainURL := fs.String("brain", os.Getenv("CORRAL_BRAIN"), "brain endpoint (or $CORRAL_BRAIN)")
	progress := fs.String("progress", "eval/.eval-progress.json", "resumable progress file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	m, err := eval.Load(*corpus)
	if err != nil {
		fmt.Fprintf(stderr, "corral eval: %v\n", err)
		return 2
	}
	var onlyIDs []string
	if strings.TrimSpace(*only) != "" {
		onlyIDs = strings.Split(*only, ",")
	}
	runner := newRunner(*brainURL, m.CorpusVersion)
	results, err := eval.Run(context.Background(), m, eval.Config{
		Iterations: *iterations, Only: onlyIDs, ProgressPath: *progress,
	}, runner, stdout)
	if err != nil {
		fmt.Fprintf(stderr, "corral eval: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout)
	eval.WriteReport(stdout, eval.Report(m, results))
	return 0
}
```

Dispatch in `cmd/corral/main.go`:
- add `"eval"` to the verb list in `subcommand()` (line ~153).
- add a `case "eval":` in `main()` beside `case "scorecard":` that builds the real runner and calls `runEval`:
```go
	case "eval":
		os.Exit(runEval(rest, func(brainURL, corpusVersion string) eval.PoolRunner {
			return mcpPoolRunner{client: mcpAdvClient{}, brainURL: brainURL, corpusVersion: corpusVersion,
				poll: 5 * time.Second, timeout: 15 * time.Minute}
		}, os.Stdout, os.Stderr))
```

> **Implementer note:** confirm `mcpAdvClient` is the concrete type implementing `advPoolClient` (it is, per `certify_adversarial.go`); match the flag names/defaults `certify` uses for `--brain`. If `runEval`'s `--brain` is empty (no `CORRAL_BRAIN`), the harness will fail on the first `StartRun` with a clear error — acceptable, but if trivial, add an early "no brain configured" message before the run plan. Add `--poll`/`--timeout` flags if you want them operator-tunable; defaults above are fine.

- [ ] **Step 4: Run the tests to green** — `go test ./cmd/corral/ -run TestRunEval -v`, `go build ./...`, `go test ./cmd/corral/ ./internal/eval/ -count=1` → PASS + clean.

- [ ] **Step 5: Regenerate CLI docs + commit**

```bash
bash scripts/gen-cli-docs.sh   # the new 'eval' verb changes corral -h
git add cmd/corral/eval.go cmd/corral/main.go cmd/corral/eval_test.go docs/cli/ site/src/content/docs/docs/cli/ 2>/dev/null
git commit -m "corral: 'eval' verb — run the pool across the corpus + soundness report"
```

---

## After all tasks
- `gofmt -l .` MUST be clean (the CI security gate runs it — a struct-alignment slip failed a merge on the scorecard branch). Run `gofmt -w` on anything it flags before the final commit.
- End-to-end (manual, documented): with the prod brain + a herd up, `corral eval --iterations 3 --only passwd-gappy,passwd-thorough` — confirm the gappy target reports survivors, the thorough doesn't (CALIBRATED), and `corral scorecard` now shows non-empty cells.
- Follow-ups (spec non-goals): MotherDuck federation (the DSN flip), the donated-time re-execution/attestation layer, external-repo corpus expansion, a UI for the report.

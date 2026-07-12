<!-- SPDX-License-Identifier: Elastic-2.0 -->
# Control-gate poller wiring — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire the built-but-unwired control-gate stack into a live per-PR poller that posts a distinct, signed `corral/control-gate` required check — running a control owner's already-vetted tests against each PR head, deterministically (no LLM).

**Architecture:** A parallel `StartControlGate` in `internal/brain` mirrors `StartGate`, reusing the proven `gate.Poller` (loop + SHA dedup) and the injected `repo.Engine` (checkout/read/status) + `certifierAdapter` (signing). A new `controlRunner` turns one PR into a verdict: checkout head → `ListVetted(owner)` → read each target's head content → `controlgate.RunControlGate` in an `adequacy.NewJail` → `controlgate.PostControlGate`. A `corral control seed` CLI seeds one vetted test so the gate demonstrably gates a live PR.

**Tech Stack:** Go 1.26.5; `internal/controlgate`, `internal/controlspec`, `internal/adequacy`, `internal/gate`, `internal/repo`, DuckDB (`go-duckdb/v2`).

## Global Constraints
- SPDX header `// SPDX-License-Identifier: Elastic-2.0` on every new file.
- **TDD**; per commit: `go vet ./...` + `go test ./...` + `go build ./...` + `bash scripts/check-security.sh` (`export PATH="$PATH:$HOME/go/bin"`) all green.
- **Deterministic stores/libraries:** no `time.Now()` inside `internal/*` stores or the runner — clocks are injected (`Now func() time.Time`). The CLI (`cmd/corral`) MAY call `time.Now()`.
- **Fail-closed:** `success` on `corral/control-gate` is posted ONLY when every vetted control passed on a real jail exit 0 AND the verdict was signed first. Missing target file at head → that control **fails**. Zero vetted controls for a configured gate → **failure** (never a vacuous green). Any jail error → failure/error. A certify error → **no status posted** (no unsigned green), and the SHA is NOT recorded so the next poll retries.
- **No unsigned green:** the real verdict (pass or fail) goes through `controlgate.PostControlGate`, which certifies before posting. Infra-error paths (`checkout`/`workspace`/`list`) post an UNSIGNED non-success status (mirrors `gate.Runner.fail`) — a failure need not be signed; only a green must.
- **`Target` convention:** `controlspec.GateTest.Target` = a repo-relative POSIX path (read via `repo.Engine.ReadFile`). Distinct from `CodePath` (the flat filename inside the jail workspace).
- **One control-owner per repo** (v1): `ListVetted` is owner-scoped; a `ControlPolicy` binds one owner to one repo.
- **Reuse the shared isolator:** the jail backend is `opts.GateBackend` (the same `execBackend` the merge gate uses) — never construct a second `sandbox.Isolator`.
- corral metaphor (herd/corral/wrangle; "control owner", never "CISO", in code identifiers).

## File Structure
- `internal/controlspec/types.go` — add `CodePath`, `TestPath` to `GateTest`. (modify)
- `internal/controlspec/store.go` — persist/scan the two recipe columns. (modify)
- `internal/controlspec/gate_tests_test.go` — recipe round-trip test. (modify)
- `internal/controlgate/config.go` — `ControlPolicy`, `ParseControlPolicies`, `langScaffold`. (new)
- `internal/controlgate/config_test.go` — parser + scaffold tests. (new)
- `internal/brain/controlgate.go` — `controlRunner`, `StartControlGate`, `fileReader`. (new)
- `internal/brain/controlgate_test.go` — runner + StartControlGate tests. (new)
- `internal/brain/identity.go` — new `Options` fields. (modify)
- `cmd/corral/main.go` — env parse + `Options` + `StartControlGate` call + subcommand dispatch + env doc comment. (modify)
- `cmd/corral/control.go` — `runControl` (the `seed` verb). (new)
- `cmd/corral/control_test.go` — seed test. (new)

## Interfaces (produced → consumed downstream)
```go
// controlspec (Task 1): GateTest gains
CodePath string
TestPath string

// controlgate/config.go (Task 2)
type ControlPolicy struct { Repo, Base, Owner, Lang string }
func ParseControlPolicies(raw string) (policies []ControlPolicy, bad []string)
func langScaffold(lang string) (base map[string]string, testCmd []string, ok bool) // unexported

// brain (Task 3/4)
func StartControlGate(ctx context.Context, opts Options) (*gate.Store, *controlspec.Store, error)
// Options gains: ControlPolicies []controlgate.ControlPolicy; ControlSpecDB, ControlGateDB string; ControlPollInterval time.Duration

// cmd/corral (Task 6)
func runControl(args []string, out io.Writer) error // `corral control seed ...`
```

---

## Task 1: controlspec — persist the per-test recipe (CodePath/TestPath)

**Files:** Modify `internal/controlspec/types.go`, `internal/controlspec/store.go`; Test `internal/controlspec/gate_tests_test.go`

**Interfaces:**
- Produces: `GateTest.CodePath`, `GateTest.TestPath`, persisted by `SaveCandidate` and returned by `GetVetted`/`ListPending`/`ListVetted`.

- [ ] **Step 1: Failing test** — append to `gate_tests_test.go`:
```go
func TestRecipeRoundTrip(t *testing.T) {
	s, _ := OpenStore(filepath.Join(t.TempDir(), "cs.db"))
	defer s.Close()
	now := time.Unix(1_700_000_000, 0).UTC()
	gt := GateTest{Owner: "ciso@bankz", Goal: "g1", Target: "internal/auth/login.go",
		Test: "package control\n// t", KillRate: 1,
		CodePath: "login.go", TestPath: "login_control_test.go", CreatedTS: now}
	if err := s.SaveCandidate(gt); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Promote("ciso@bankz", "g1", "internal/auth/login.go", now); err != nil {
		t.Fatal(err)
	}
	v, err := s.ListVetted("ciso@bankz")
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 1 || v[0].CodePath != "login.go" || v[0].TestPath != "login_control_test.go" {
		t.Fatalf("recipe did not round-trip through ListVetted: %+v", v)
	}
	got, ok, _ := s.GetVetted("ciso@bankz", "g1", "internal/auth/login.go")
	if !ok || got.CodePath != "login.go" || got.TestPath != "login_control_test.go" {
		t.Fatalf("recipe wrong from GetVetted: %+v", got)
	}
}
```
- [ ] **Step 2: Run, watch fail.** `go test ./internal/controlspec/ -run TestRecipeRoundTrip` → FAIL (`GateTest` has no `CodePath`).
- [ ] **Step 3: Implement.**
  - `types.go`: in `GateTest`, after `Target string` add:
```go
	// CodePath/TestPath are the workspace recipe: where the target's head
	// content and the vetted test land inside the minimal jail workspace when
	// the gate re-runs this test. Target is the REAL repo-relative path to
	// read head content from; CodePath is the flat filename the test expects.
	CodePath string
	TestPath string
```
  - `store.go` `OpenStore`, in the `gate_tests` `CREATE TABLE`, add before `PRIMARY KEY`:
```go
			code_path VARCHAR NOT NULL DEFAULT '', test_path VARCHAR NOT NULL DEFAULT '',
```
    and after the `gate_tests` create block (idempotent migration for any pre-existing dev DB):
```go
	for _, alter := range []string{
		`ALTER TABLE gate_tests ADD COLUMN IF NOT EXISTS code_path VARCHAR NOT NULL DEFAULT ''`,
		`ALTER TABLE gate_tests ADD COLUMN IF NOT EXISTS test_path VARCHAR NOT NULL DEFAULT ''`,
	} {
		if _, err := db.Exec(alter); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("controlspec: migrating gate_tests: %w", err)
		}
	}
```
  - `SaveCandidate`: add the two columns to the INSERT column list and `VALUES` (two more `?`), and append `gt.CodePath, gt.TestPath` to the `Exec` args:
```go
		`INSERT OR REPLACE INTO gate_tests (owner, goal, target, test, kill_rate, survived, discarded, vetted, created_ts, vetted_ts, verdicts, code_path, test_path)
		 VALUES (?, ?, ?, ?, ?, ?, ?, FALSE, ?, NULL, ?, ?, ?)`,
		gt.Owner, gt.Goal, gt.Target, gt.Test, gt.KillRate, string(survived), string(discarded), gt.CreatedTS, gt.VerdictsJSON, gt.CodePath, gt.TestPath)
```
  - `GetVetted`: add `code_path, test_path` to the `SELECT` and `&gt.CodePath, &gt.TestPath` to the `Scan` (after `&gt.VerdictsJSON`).
  - `ListPending` AND `ListVetted`: same — add `code_path, test_path` to each `SELECT` and `&gt.CodePath, &gt.TestPath` to each `rows.Scan` (after `&gt.VerdictsJSON`).
- [ ] **Step 4: Run, watch pass.** `go test ./internal/controlspec/...` → PASS (existing tests still green). Then `go vet ./... && go build ./... && go test ./... && bash scripts/check-security.sh`. **Commit:** `feat(controlspec): persist per-test recipe (CodePath/TestPath) for gate re-run`.

---

## Task 2: controlgate — the control policy parser + language scaffold

**Files:** Create `internal/controlgate/config.go`, `internal/controlgate/config_test.go`

**Interfaces:**
- Produces: `ControlPolicy`, `ParseControlPolicies`, `langScaffold` (all in `package controlgate`).

- [ ] **Step 1: Failing tests** — `config_test.go`:
```go
// SPDX-License-Identifier: Elastic-2.0

package controlgate

import "testing"

func TestParseControlPolicies(t *testing.T) {
	pols, bad := ParseControlPolicies("repo=o/r,owner=ciso@bankz,lang=go,base=main; repo=o/r2,owner=lead@x,lang=go")
	if len(pols) != 2 || len(bad) != 0 {
		t.Fatalf("pols=%+v bad=%+v", pols, bad)
	}
	if pols[0].Repo != "o/r" || pols[0].Owner != "ciso@bankz" || pols[0].Lang != "go" || pols[0].Base != "main" {
		t.Fatalf("pol0 wrong: %+v", pols[0])
	}
	// missing owner; missing repo; unknown lang → all three are bad.
	_, bad2 := ParseControlPolicies("repo=o/r,lang=go; owner=x,lang=go; repo=o/r,owner=x,lang=rust")
	if len(bad2) != 3 {
		t.Fatalf("expected 3 bad, got %+v", bad2)
	}
	// omitted lang defaults to go (valid).
	pols3, bad3 := ParseControlPolicies("repo=o/r,owner=x")
	if len(pols3) != 1 || len(bad3) != 0 || pols3[0].Lang != "go" {
		t.Fatalf("default lang: pols=%+v bad=%+v", pols3, bad3)
	}
	if p, _ := ParseControlPolicies(""); p != nil {
		t.Fatal("empty raw → nil policies (off switch)")
	}
}

func TestLangScaffold(t *testing.T) {
	base, cmd, ok := langScaffold("go")
	if !ok || base["go.mod"] == "" || len(cmd) == 0 {
		t.Fatalf("go scaffold: base=%v cmd=%v ok=%v", base, cmd, ok)
	}
	if _, _, ok := langScaffold("rust"); ok {
		t.Fatal("unknown lang must be !ok")
	}
	if _, _, ok := langScaffold(""); ok {
		t.Fatal("empty lang must be !ok")
	}
}
```
- [ ] **Step 2: Run, watch fail.** `go test ./internal/controlgate/ -run 'TestParseControlPolicies|TestLangScaffold'` → FAIL (undefined).
- [ ] **Step 3: Implement** `config.go`:
```go
// SPDX-License-Identifier: Elastic-2.0

package controlgate

import "strings"

// ControlPolicy binds one control owner to one repo's control gate. Repo is
// "owner/name" (GitHub, matching gate.Policy.Repo). Owner is the control-owner
// principal ListVetted is keyed on. Lang selects the built-in workspace
// scaffold (langScaffold). Base is the target branch ("" = all bases).
type ControlPolicy struct {
	Repo  string
	Base  string
	Owner string
	Lang  string
}

// ParseControlPolicies parses CORRALAI_CONTROL_GATE: ";"-separated entries,
// each ","-separated key=value pairs — "repo=o/r,owner=lead@x,lang=go,base=main".
// An empty raw string yields (nil,nil): the feature's off switch. An entry
// missing repo= or owner=, or naming an unknown lang, is skipped and reported
// in bad (degrade-never-block — one bad entry must not disable the others).
// Omitted lang defaults to "go".
func ParseControlPolicies(raw string) (policies []ControlPolicy, bad []string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	for _, entry := range strings.Split(raw, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		pol := ControlPolicy{Lang: "go"}
		for _, kv := range strings.Split(entry, ",") {
			key, val, ok := strings.Cut(strings.TrimSpace(kv), "=")
			if !ok {
				continue
			}
			key, val = strings.TrimSpace(key), strings.TrimSpace(val)
			switch key {
			case "repo":
				pol.Repo = val
			case "owner":
				pol.Owner = val
			case "lang":
				if val != "" {
					pol.Lang = val
				}
			case "base":
				pol.Base = val
			}
		}
		if _, _, ok := langScaffold(pol.Lang); pol.Repo == "" || pol.Owner == "" || !ok {
			bad = append(bad, entry)
			continue
		}
		policies = append(policies, pol)
	}
	return policies, bad
}

// langScaffold returns the minimal workspace (base file set + test command)
// a vetted test for lang re-runs inside. v1 supports Go; unknown → !ok, which
// ParseControlPolicies rejects loudly. The scaffold MUST match the one the
// test was authored/vetted against (package name, module path).
func langScaffold(lang string) (base map[string]string, testCmd []string, ok bool) {
	switch lang {
	case "go":
		return map[string]string{"go.mod": "module control\ngo 1.26\n"}, []string{"go", "test", "./"}, true
	}
	return nil, nil, false
}
```
- [ ] **Step 4: Run, watch pass.** `go test ./internal/controlgate/...` → PASS. Then full gate. **Commit:** `feat(controlgate): ControlPolicy parser + language scaffold`.

---

## Task 3: brain — the control runner (one PR → signed verdict)

**Files:** Create `internal/brain/controlgate.go`, `internal/brain/controlgate_test.go`

**Interfaces:**
- Consumes: `gate.Policy`/`gate.PRRef`/`gate.Store`/`gate.Run`/`gate.Checkouter`, `controlgate.{ControlPolicy,ControlCheck,ControlTestResult,ControlResult,PostRequest,Certifier,StatusPoster,RunControlGate,PostControlGate,langScaffold}`, `controlspec.Store`, `adequacy.Jail`, `certifierAdapter` (Task-existing).
- Produces: `controlRunner` with a `Run(ctx, repoURL string, p gate.Policy, pr gate.PRRef) error` method (matches `gate.Poller.Run`), and `fileReader`.

> **Note for the implementer:** `langScaffold` is unexported in `package controlgate`, so the runner cannot call it. Compute `base`/`testCmd` in `StartControlGate` (Task 4, same package boundary problem) and pass them into the runner. To keep Task 3 self-contained and testable, the `controlRunner` holds `Base map[string]string` and `TestCmd []string` fields (set by Task 4 from `langScaffold`). Do NOT call `langScaffold` from `package brain`.

- [ ] **Step 1: Failing test** — `controlgate_test.go`. Fakes for checkout, reader, cert, poster, and `adequacy.Jail`; a real temp `controlspec.Store`.
```go
// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/controlspec"
	"github.com/pdbethke/corralai/internal/gate"
)

type fakeCheckout struct{ err error }

func (f fakeCheckout) CheckoutPR(_ context.Context, _ string, _ int, _, _ string) error { return f.err }

type fakeReader struct{ files map[string]string } // path -> head content; missing = ReadFile error

func (f fakeReader) ReadFile(_ , path string) (string, error) {
	if c, ok := f.files[path]; ok {
		return c, nil
	}
	return "", errors.New("not found")
}

type fakeCert struct{ err error }

func (f fakeCert) Certify(_ context.Context, _, _, _ string, _ int, _ string) (int64, string, error) {
	return 1, "head", f.err
}

type postCall struct{ state, ctx, desc string }
type fakePoster struct{ calls []postCall }

func (f *fakePoster) SetCommitStatus(_ context.Context, _, _, context, state, _, desc string) error {
	f.calls = append(f.calls, postCall{state: state, ctx: context, desc: desc})
	return nil
}
func (f *fakePoster) last() postCall { return f.calls[len(f.calls)-1] }

// fakeJail: a vetted test "passes" unless its content contains "FAIL".
type fakeJail struct{}

func (fakeJail) RunTest(_ context.Context, files map[string]string, _ []string) (bool, error) {
	for _, c := range files {
		if len(c) >= 4 && containsFAIL(c) {
			return false, nil
		}
	}
	return true, nil
}
func containsFAIL(s string) bool { return len(s) > 0 && (s == "FAIL" || indexFAIL(s)) }
func indexFAIL(s string) bool {
	for i := 0; i+4 <= len(s); i++ {
		if s[i:i+4] == "FAIL" {
			return true
		}
	}
	return false
}

func seedRunner(t *testing.T, reader fileReader, cert controlgateCertifier, poster *fakePoster) (*controlRunner, *controlspec.Store) {
	t.Helper()
	spec, err := controlspec.OpenStore(filepath.Join(t.TempDir(), "cs.db"))
	if err != nil {
		t.Fatal(err)
	}
	runStore, err := gate.OpenStore(filepath.Join(t.TempDir(), "run.db"))
	if err != nil {
		t.Fatal(err)
	}
	r := &controlRunner{
		byRepo:   map[string]string{"o/r": "owner@x"},
		Base:     map[string]string{"go.mod": "module control\ngo 1.26\n"},
		TestCmd:  []string{"go", "test", "./"},
		Checkout: fakeCheckout{},
		Reader:   reader,
		Cert:     cert,
		Status:   poster,
		Spec:     spec,
		Jail:     fakeJail{},
		RunStore: runStore,
		Record:   func(_, sha string) string { return "/rec/" + sha },
		Now:      func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
	}
	return r, spec
}

func vet(t *testing.T, s *controlspec.Store, owner, goal, target, test, codePath string) {
	t.Helper()
	now := time.Unix(1_700_000_000, 0).UTC()
	gt := controlspec.GateTest{Owner: owner, Goal: goal, Target: target, Test: test,
		CodePath: codePath, TestPath: "x_control_test.go", KillRate: 1, CreatedTS: now}
	if err := s.SaveCandidate(gt); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Promote(owner, goal, target, now); err != nil {
		t.Fatal(err)
	}
}

func policy() gate.Policy { return gate.Policy{Repo: "o/r", Context: "corral/control-gate"} }
func pr() gate.PRRef      { return gate.PRRef{Number: 1, HeadSHA: "abc"} }

func TestControlRunner_Pass(t *testing.T) {
	poster := &fakePoster{}
	reader := fakeReader{files: map[string]string{"internal/auth/login.go": "package control\n// head ok"}}
	r, spec := seedRunner(t, reader, fakeCert{}, poster)
	defer spec.Close()
	vet(t, spec, "owner@x", "g1", "internal/auth/login.go", "package control\n// clean", "login.go")
	if err := r.Run(context.Background(), "https://github.com/o/r", policy(), pr()); err != nil {
		t.Fatal(err)
	}
	if got := poster.last(); got.state != "success" || got.ctx != "corral/control-gate" {
		t.Fatalf("expected signed success, got %+v", got)
	}
}

func TestControlRunner_FailingControl(t *testing.T) {
	poster := &fakePoster{}
	reader := fakeReader{files: map[string]string{"a.go": "package control\n// FAIL head"}}
	r, spec := seedRunner(t, reader, fakeCert{}, poster)
	defer spec.Close()
	vet(t, spec, "owner@x", "g1", "a.go", "package control\n// t", "a.go")
	_ = r.Run(context.Background(), "https://github.com/o/r", policy(), pr())
	if got := poster.last(); got.state != "failure" {
		t.Fatalf("failing control → failure, got %+v", got)
	}
}

func TestControlRunner_MissingTarget(t *testing.T) {
	poster := &fakePoster{}
	reader := fakeReader{files: map[string]string{}} // target absent at head
	r, spec := seedRunner(t, reader, fakeCert{}, poster)
	defer spec.Close()
	vet(t, spec, "owner@x", "g1", "gone.go", "package control\n// t", "gone.go")
	_ = r.Run(context.Background(), "https://github.com/o/r", policy(), pr())
	if got := poster.last(); got.state != "failure" {
		t.Fatalf("missing target → failure (fail-closed), got %+v", got)
	}
}

func TestControlRunner_ZeroVetted(t *testing.T) {
	poster := &fakePoster{}
	r, spec := seedRunner(t, fakeReader{}, fakeCert{}, poster)
	defer spec.Close()
	_ = r.Run(context.Background(), "https://github.com/o/r", policy(), pr())
	if got := poster.last(); got.state != "failure" {
		t.Fatalf("zero vetted → failure, got %+v", got)
	}
}

func TestControlRunner_CertifyError_NoPost(t *testing.T) {
	poster := &fakePoster{}
	reader := fakeReader{files: map[string]string{"a.go": "package control\n// ok"}}
	r, spec := seedRunner(t, reader, fakeCert{err: errors.New("sign failed")}, poster)
	defer spec.Close()
	vet(t, spec, "owner@x", "g1", "a.go", "package control\n// t", "a.go")
	err := r.Run(context.Background(), "https://github.com/o/r", policy(), pr())
	if err == nil {
		t.Fatal("certify error must return an error")
	}
	for _, c := range poster.calls {
		if c.state == "success" {
			t.Fatal("must not post success when signing failed (no unsigned green)")
		}
	}
}
```
> The test references `controlgateCertifier` and `byRepo map[string]string` — define the runner so these match: `byRepo` maps `repo → owner`, and `Cert` is typed `controlgate.Certifier`. Use a type alias in the test (`type controlgateCertifier = controlgate.Certifier`) OR type the `Cert` field as `controlgate.Certifier` and have the test import `controlgate` — the implementer picks the mechanically simplest form; keep the behavior identical.

- [ ] **Step 2: Run, watch fail** (`controlRunner` undefined).
- [ ] **Step 3: Implement** `controlgate.go`:
```go
// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/controlgate"
	"github.com/pdbethke/corralai/internal/controlspec"
	"github.com/pdbethke/corralai/internal/gate"
)

// fileReader reads a checked-out file's content. repo.Engine satisfies it
// (ReadFile(dir, path)); tests inject a fake.
type fileReader interface {
	ReadFile(dir, path string) (string, error)
}

// controlRunner gates one PR head against a control owner's VETTED tests:
// checkout head → ListVetted(owner) → read each target's head content → run
// the vetted tests in the jail → sign + post corral/control-gate.
//
// FAIL-CLOSED (under test): success is posted ONLY on an all-pass verdict that
// was signed first. Missing target → that control fails; zero vetted → failure;
// jail/checkout error → non-success (unsigned); certify error → nothing posted
// and the SHA is NOT recorded (retried next poll).
type controlRunner struct {
	byRepo   map[string]string // repo ("owner/name") -> control-owner principal
	Base     map[string]string // the workspace scaffold (from langScaffold)
	TestCmd  []string          // the test command (from langScaffold)
	Checkout gate.Checkouter
	Reader   fileReader
	Cert     controlgate.Certifier
	Status   controlgate.StatusPoster
	Spec     *controlspec.Store
	Jail     adequacy.Jail
	RunStore *gate.Store
	Record   func(repo, sha string) string
	Now      func() time.Time
}

func (r *controlRunner) Run(ctx context.Context, repoURL string, p gate.Policy, pr gate.PRRef) error {
	owner, ok := r.byRepo[p.Repo]
	if !ok {
		return fmt.Errorf("controlgate: no control policy for repo %q", p.Repo)
	}
	target := r.Record(p.Repo, pr.HeadSHA)
	_ = r.Status.SetCommitStatus(ctx, repoURL, pr.HeadSHA, p.Context, "pending", target, "corral control-gate running")

	dest, err := os.MkdirTemp("", "corral-control-")
	if err != nil {
		return r.fail(ctx, repoURL, p, pr, target, "error", "workspace: "+err.Error())
	}
	defer func() { _ = os.RemoveAll(dest) }()

	if err := r.Checkout.CheckoutPR(ctx, repoURL, pr.Number, pr.HeadSHA, dest); err != nil {
		return r.fail(ctx, repoURL, p, pr, target, "error", "checkout: "+err.Error())
	}

	vetted, err := r.Spec.ListVetted(owner)
	if err != nil {
		return r.fail(ctx, repoURL, p, pr, target, "error", "list vetted: "+err.Error())
	}
	if len(vetted) == 0 {
		return r.fail(ctx, repoURL, p, pr, target, "failure", "no vetted controls for "+owner)
	}

	var checks []controlgate.ControlCheck
	var missing []controlgate.ControlTestResult
	for _, gt := range vetted {
		head, err := r.Reader.ReadFile(dest, gt.Target)
		if err != nil {
			// Target absent/unreadable at head → fail-closed: this control fails.
			missing = append(missing, controlgate.ControlTestResult{Goal: gt.Goal, Target: gt.Target, Passed: false})
			continue
		}
		checks = append(checks, controlgate.ControlCheck{Test: gt, HeadCode: head, CodePath: gt.CodePath, TestPath: gt.TestPath})
	}

	res, err := controlgate.RunControlGate(ctx, r.Jail, r.Base, checks, r.TestCmd)
	if err != nil {
		return r.fail(ctx, repoURL, p, pr, target, "error", "jail: "+err.Error())
	}
	res.Results = append(res.Results, missing...)
	if len(missing) > 0 {
		res.Pass = false
	}

	req := controlgate.PostRequest{
		RepoURL:   repoURL,
		HeadSHA:   pr.HeadSHA,
		Context:   p.Context,
		RecordURL: func(sha string) string { return r.Record(p.Repo, sha) },
	}
	if err := controlgate.PostControlGate(ctx, r.Cert, r.Status, req, res); err != nil {
		// No unsigned green: certify failed, nothing was posted. Do NOT record
		// the SHA — leave it for the next poll to retry.
		return err
	}
	_ = r.RunStore.Save(gate.Run{Repo: p.Repo, HeadSHA: pr.HeadSHA, PR: pr.Number, Passed: res.Pass, RecordID: 0, RanAt: r.Now()})
	return nil
}

// fail posts a non-success status (unsigned — a failure needs no signature)
// and records the SHA so the poller doesn't re-run it. Mirrors gate.Runner.fail.
func (r *controlRunner) fail(ctx context.Context, repoURL string, p gate.Policy, pr gate.PRRef, target, state, msg string) error {
	_ = r.RunStore.Save(gate.Run{Repo: p.Repo, HeadSHA: pr.HeadSHA, PR: pr.Number, Passed: false, RanAt: r.Now()})
	return r.Status.SetCommitStatus(ctx, repoURL, pr.HeadSHA, p.Context, state, target, msg)
}
```
- [ ] **Step 4: Run, watch pass.** `go test ./internal/brain/ -run TestControlRunner` → PASS. Then full gate. **Commit:** `feat(brain): controlRunner — one PR head → signed corral/control-gate verdict (fail-closed)`.

---

## Task 4: brain — StartControlGate + Options fields

**Files:** Modify `internal/brain/identity.go`, `internal/brain/controlgate.go`; Test `internal/brain/controlgate_test.go`

**Interfaces:**
- Consumes: Task 2's `controlgate.ControlPolicy`/`langScaffold`, Task 3's `controlRunner`, `gate.Poller`/`gate.OpenStore`, `controlspec.OpenStore`, `adequacy.NewJail`, `certifierAdapter`.
- Produces: `func StartControlGate(ctx, opts Options) (*gate.Store, *controlspec.Store, error)` and the new `Options` fields.

- [ ] **Step 1: Failing test** — append to `controlgate_test.go`:
```go
func TestStartControlGate_OffSwitches(t *testing.T) {
	// no policies → complete no-op
	if rs, cs, err := StartControlGate(context.Background(), Options{}); rs != nil || cs != nil || err != nil {
		t.Fatalf("empty policies must be a no-op: %v %v %v", rs, cs, err)
	}
	// policies but nil backend → disabled (fail-closed: never run unsandboxed)
	opts := Options{ControlPolicies: []controlgate.ControlPolicy{{Repo: "o/r", Owner: "x", Lang: "go"}}}
	if rs, cs, _ := StartControlGate(context.Background(), opts); rs != nil || cs != nil {
		t.Fatal("nil GateBackend must disable the control gate")
	}
}
```
(Import `controlgate` in the test.)
- [ ] **Step 2: Run, watch fail** (`StartControlGate` undefined).
- [ ] **Step 3: Implement.**
  - `identity.go`: in `Options`, after `GateRecordURL` (near the other gate fields), add:
```go
	// Control gate (v1 run+post): the control owner's vetted tests, run against
	// each PR head and posted as a distinct corral/control-gate required check.
	ControlPolicies     []controlgate.ControlPolicy
	ControlSpecDB       string        // vetted-tests store DSN (controlspec)
	ControlGateDB       string        // dedup run-store DSN (separate from the merge gate's)
	ControlPollInterval time.Duration
```
    Add `"github.com/pdbethke/corralai/internal/controlgate"` to `identity.go`'s imports if not present.
  - `controlgate.go`: add `StartControlGate`:
```go
// StartControlGate wires and starts the control gate: the controlspec store
// (vetted tests), a separate gate.Store (SHA dedup, distinct from the merge
// gate so their dedup keys never collide), an adequacy jail over the shared
// backend, and a gate.Poller driving controlRunner. Off switches mirror
// StartGate: empty ControlPolicies → (nil,nil,nil); nil GateBackend or nil
// Repo → logged (nil,nil,nil). Returns the two opened stores.
func StartControlGate(ctx context.Context, opts Options) (*gate.Store, *controlspec.Store, error) {
	if len(opts.ControlPolicies) == 0 {
		return nil, nil, nil
	}
	if opts.GateBackend == nil {
		log.Printf("control-gate: DISABLED — CORRALAI_CONTROL_GATE is set (%d polic(ies)) but no sandbox backend; refusing to run PR tests unsandboxed (set CORRALAI_GATE_EXEC_BACKEND)", len(opts.ControlPolicies))
		return nil, nil, nil
	}
	if opts.Repo == nil {
		log.Printf("control-gate: DISABLED — CORRALAI_CONTROL_GATE is set but no repo.Engine is configured (Options.Repo is nil)")
		return nil, nil, nil
	}

	specDSN := opts.ControlSpecDB
	if specDSN == "" {
		specDSN = "corralai_control_spec.duckdb"
	}
	spec, err := controlspec.OpenStore(specDSN)
	if err != nil {
		return nil, nil, fmt.Errorf("control-gate: open spec store: %w", err)
	}
	runDSN := opts.ControlGateDB
	if runDSN == "" {
		runDSN = "corralai_control_gate.duckdb"
	}
	runStore, err := gate.OpenStore(runDSN)
	if err != nil {
		_ = spec.Close()
		return nil, nil, fmt.Errorf("control-gate: open run store: %w", err)
	}

	record := opts.GateRecordURL
	if record == nil {
		record = func(repoName, sha string) string {
			return "/api/gate/run?repo=" + url.QueryEscape(repoName) + "&sha=" + url.QueryEscape(sha)
		}
	}

	// v1 assumes one language/scaffold per brain (all control policies share it);
	// the first policy's lang selects it. langScaffold validated every policy's
	// lang at parse time, so this cannot be !ok for a policy that got this far.
	base, testCmd, _ := controlgate.LangScaffold(opts.ControlPolicies[0].Lang)

	byRepo := make(map[string]string, len(opts.ControlPolicies))
	var policies []gate.Policy
	for _, cp := range opts.ControlPolicies {
		byRepo[cp.Repo] = cp.Owner
		var bases []string
		if cp.Base != "" {
			bases = []string{cp.Base}
		}
		policies = append(policies, gate.Policy{Repo: cp.Repo, Base: bases, Context: "corral/control-gate"})
	}

	runner := &controlRunner{
		byRepo:   byRepo,
		Base:     base,
		TestCmd:  testCmd,
		Checkout: opts.Repo,
		Reader:   opts.Repo,
		Cert:     certifierAdapter{opts: opts},
		Status:   opts.Repo,
		Spec:     spec,
		Jail:     adequacy.NewJail(opts.GateBackend, gate.DefaultGateTimeout),
		RunStore: runStore,
		Record:   record,
		Now:      time.Now,
	}

	interval := opts.ControlPollInterval
	if interval <= 0 {
		interval = 2 * time.Minute
	}
	poller := &gate.Poller{Policies: policies, List: opts.Repo, Store: runStore, Run: runner.Run, Interval: interval}
	log.Printf("control-gate: ENABLED — %d polic(ies), polling every %s", len(policies), interval)
	go poller.Loop(ctx)
	return runStore, spec, nil
}
```
    Add imports to `controlgate.go`: `"log"`, `"net/url"`.
  - **Export `langScaffold`:** `StartControlGate` (in `package brain`) needs the scaffold, so rename `langScaffold` → **`LangScaffold`** (exported) in `internal/controlgate/config.go` and its two test call sites in `config_test.go` and the `ParseControlPolicies` internal call. (The runner still receives `Base`/`TestCmd` as fields — only `StartControlGate` calls `LangScaffold`.)
- [ ] **Step 4: Run, watch pass.** `go test ./internal/brain/... ./internal/controlgate/...` → PASS. Then full gate. **Commit:** `feat(brain): StartControlGate — wire the control-gate poller (off by default)`.

---

## Task 5: cmd/corral — env config + wiring + dispatch + docs

**Files:** Modify `cmd/corral/main.go`

**Interfaces:**
- Consumes: `controlgate.ParseControlPolicies`, `brain.StartControlGate`, `brain.Options` (new fields), `runControl` (Task 6 — dispatch only; Task 6 lands the function).

> This is a wiring task with no unit test of its own (the logic is covered by Tasks 2–4). Verification is `go build` + `go vet` + a `--help`/dry check. Task 6 supplies `runControl`; sequence Task 6 before this task's dispatch line compiles, OR add the dispatch line in Task 6. To keep this task independently buildable, add ONLY the env/Options/StartControlGate wiring here, and leave the `control` subcommand dispatch to Task 6.

- [ ] **Step 1: Add env parsing.** After the `gateDB := env(...)` line (~main.go:1043):
```go
	// Control gate (v1): CORRALAI_CONTROL_GATE declares repo→owner control
	// gates; empty => feature off. Runs each owner's VETTED tests against PR
	// heads and posts corral/control-gate. Reuses execBackend for the jail.
	controlPolicies, badControl := controlgate.ParseControlPolicies(os.Getenv("CORRALAI_CONTROL_GATE"))
	for _, bad := range badControl {
		log.Printf("control-gate: malformed CORRALAI_CONTROL_GATE entry (skipped): %q", bad)
	}
	controlSpecDB := env("CORRALAI_CONTROL_GATE_SPEC_DB", filepath.Join(home, ".claude", "corralai_control_spec.duckdb"))
	controlGateDB := env("CORRALAI_CONTROL_GATE_DB", filepath.Join(home, ".claude", "corralai_control_gate.duckdb"))
```
  Ensure `"github.com/pdbethke/corralai/internal/controlgate"` is imported.
- [ ] **Step 2: Set Options fields.** In the `brainOpts := brain.Options{...}` literal, next to the `GatePollInterval:` line (~1093):
```go
		ControlPolicies:     controlPolicies,
		ControlSpecDB:       controlSpecDB,
		ControlGateDB:       controlGateDB,
		ControlPollInterval: time.Duration(envInt("CORRALAI_CONTROL_GATE_POLL_SECONDS", 120)) * time.Second,
```
- [ ] **Step 3: Call StartControlGate.** Right after the `StartGate` block (~1277-1281):
```go
	if _, _, cerr := brain.StartControlGate(context.Background(), brainOpts); cerr != nil {
		log.Printf("control-gate: %v", cerr)
	}
```
- [ ] **Step 4: Update the env-var doc comment** (the top-of-file block, ~main.go:59-65, the single source `scripts/gen-cli-docs.sh` extracts). After the `CORRALAI_GATE_POLL_SECONDS` line add:
```go
//	CORRALAI_CONTROL_GATE          control gate: ";"-separated "repo=owner/name,owner=<principal>,lang=go,base=main"
//	                               — runs the owner's VETTED tests against PR heads, posts corral/control-gate
//	CORRALAI_CONTROL_GATE_SPEC_DB  control-gate vetted-tests store (default ~/.claude/corralai_control_spec.duckdb)
//	CORRALAI_CONTROL_GATE_DB       control-gate dedupe/index store (default ~/.claude/corralai_control_gate.duckdb)
//	CORRALAI_CONTROL_GATE_POLL_SECONDS  how often the control gate polls for new PR heads (default 120)
```
- [ ] **Step 5: Verify.** `go vet ./... && go build ./... && go test ./... && bash scripts/check-security.sh`. If a CLI-docs drift gate exists, run `bash scripts/gen-cli-docs.sh` (regenerate) so the reference stays in sync. **Commit:** `feat(cmd/corral): wire the control gate (CORRALAI_CONTROL_GATE, off by default)`.

---

## Task 6: cmd/corral — `corral control seed` CLI

**Files:** Create `cmd/corral/control.go`, `cmd/corral/control_test.go`; Modify `cmd/corral/main.go` (dispatch)

**Interfaces:**
- Produces: `func runControl(args []string, out io.Writer) error`.
- Consumes: `controlspec.OpenStore`/`SaveCandidate`/`Promote`.

- [ ] **Step 1: Failing test** — `control_test.go`:
```go
// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/pdbethke/corralai/internal/controlspec"
)

func TestControlSeed(t *testing.T) {
	dir := t.TempDir()
	specDB := filepath.Join(dir, "cs.db")
	testFile := filepath.Join(dir, "login_control_test.go")
	if err := os.WriteFile(testFile, []byte("package control\n// vetted test"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := runControl([]string{"seed",
		"--spec-db", specDB, "--owner", "lead@x", "--goal", "asvs-v2.1.1",
		"--target", "internal/auth/login.go", "--code-path", "login.go",
		"--test-path", "login_control_test.go", "--test-file", testFile,
	}, &out)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := controlspec.OpenStore(specDB)
	defer s.Close()
	v, _ := s.ListVetted("lead@x")
	if len(v) != 1 || v[0].Target != "internal/auth/login.go" || v[0].CodePath != "login.go" || v[0].Test == "" {
		t.Fatalf("seeded vetted control wrong: %+v", v)
	}
}
```
- [ ] **Step 2: Run, watch fail** (`runControl` undefined).
- [ ] **Step 3: Implement** `control.go`:
```go
// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/pdbethke/corralai/internal/controlspec"
)

// runControl implements `corral control seed` — write one vetted GateTest (+
// its workspace recipe) into the controlspec store so the control gate has a
// real control to run. It goes through SaveCandidate (forces unvetted) then
// Promote (vets), the same candidate→vetted human-gate path a control owner
// uses — never a back door around vetting.
func runControl(args []string, out io.Writer) error {
	if len(args) == 0 || args[0] != "seed" {
		return fmt.Errorf("usage: corral control seed --spec-db <path> --owner <principal> --goal <id> --target <repo-path> --code-path <flat> --test-path <flat> --test-file <path> [--kill-rate <float>]")
	}
	fs := flag.NewFlagSet("control seed", flag.ContinueOnError)
	specDB := fs.String("spec-db", "", "controlspec DuckDB path")
	owner := fs.String("owner", "", "control-owner principal")
	goal := fs.String("goal", "", "goal id")
	target := fs.String("target", "", "repo-relative target file path")
	codePath := fs.String("code-path", "", "flat target filename in the jail workspace")
	testPath := fs.String("test-path", "", "flat test filename in the jail workspace")
	testFile := fs.String("test-file", "", "path to the vetted test source file")
	killRate := fs.Float64("kill-rate", 1.0, "recorded adequacy kill rate")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *specDB == "" || *owner == "" || *goal == "" || *target == "" || *codePath == "" || *testPath == "" || *testFile == "" {
		return fmt.Errorf("all of --spec-db --owner --goal --target --code-path --test-path --test-file are required")
	}
	src, err := os.ReadFile(*testFile) // #nosec G304 -- operator-supplied path in a local admin CLI
	if err != nil {
		return fmt.Errorf("read test file: %w", err)
	}
	s, err := controlspec.OpenStore(*specDB)
	if err != nil {
		return err
	}
	defer s.Close()
	now := time.Now().UTC()
	gt := controlspec.GateTest{
		Owner: *owner, Goal: *goal, Target: *target, Test: string(src),
		KillRate: *killRate, CodePath: *codePath, TestPath: *testPath, CreatedTS: now,
	}
	if err := s.SaveCandidate(gt); err != nil {
		return err
	}
	ok, err := s.Promote(*owner, *goal, *target, now)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("promote: no unvetted candidate to vet (already vetted?)")
	}
	fmt.Fprintf(out, "seeded + vetted control %s@%s for %s\n", *goal, *target, *owner)
	return nil
}
```
- [ ] **Step 4: Wire dispatch** in `main.go`:
  - `subcommand()` switch (~line 140): change `case "certify", "secret":` to `case "certify", "secret", "control":`.
  - `main()` (~line 460, after the `certify` case):
```go
	case "control":
		if err := runControl(os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "corral control:", err)
			os.Exit(1)
		}
		return
```
  - `usageText()` (~line 178): add a line under the `secret` entry:
```go
  corral control seed [flags]     seed one vetted control test into the control-gate store
                                  (--spec-db --owner --goal --target --code-path --test-path --test-file)
```
- [ ] **Step 5: Run, watch pass.** `go test ./cmd/corral/ -run TestControlSeed` → PASS. Then full gate + regenerate CLI docs if a drift gate exists. **Commit:** `feat(cmd/corral): corral control seed — vet one control test into the store`.

---

## Self-Review

- **Spec coverage:**
  - §Components 1 (recipe persistence) → Task 1. ✓
  - §Components 2 (policy + scaffold) → Task 2. ✓
  - §Components 3 (control runner, fail-closed edges) → Task 3 (pass/fail/missing/zero-vetted/certify-error all under test). ✓
  - §Components 4 (StartControlGate + Options + wiring) → Tasks 4–5. ✓
  - §Components 5 (seed CLI) → Task 6. ✓
  - §Data flow + §Error handling invariants → Task 3 runner + `PostControlGate` (existing). ✓
  - §Two gaps: Target=path convention (Task 3 `ReadFile(dest, gt.Target)`, missing→fail); recipe split (base/testCmd repo-level in Task 4, CodePath/TestPath per-test in Task 1). ✓
- **Placeholder scan:** none — every step carries complete code.
- **Type consistency:** `controlRunner.Run` signature matches `gate.Poller.Run` (`func(ctx, string, gate.Policy, gate.PRRef) error`); `opts.Repo` (`*repo.Engine`) satisfies `gate.Checkouter` + `fileReader` + `controlgate.StatusPoster` + `gate.PRLister`; `certifierAdapter` satisfies `controlgate.Certifier`; `adequacy.NewJail` returns `adequacy.Jail` consumed by `RunControlGate`. The one rename: `langScaffold`→`LangScaffold` (Task 4) so `package brain` can call it — flagged in Task 4 Step 3.
- **Determinism:** stores + runner take injected clocks; only `cmd/corral` (Task 6) and `StartControlGate`'s production wiring (`Now: time.Now`) call `time.Now`, matching `StartGate`.
- **Fail-closed:** success only on signed all-pass; missing target / zero vetted / jail error → non-success; certify error → nothing posted + SHA not recorded (retried). Mirrors the merge gate's structural `fail` path.

<!-- SPDX-License-Identifier: Elastic-2.0 -->
# Audit fix batch 1 — Highs + security Mediums — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task.

**Goal:** Close the two real Highs (H-1 local-staffing bug, H-2 memory data race) and four security Mediums (M-1 egress fail-open, M-2 scan evasion, M-3 forge token-leak/SSRF, M-4 complete_task claimer) from the 2026-07-12 audit. Auth Highs (H-3) are a separate design pass; the mission/queue reliability Mediums are a later batch.

**Architecture:** Six independent tasks across `internal/mission`, `internal/memory`, `internal/brain`, `internal/egress`, `internal/repo`. Where the target logic is buried in a loop or an MCP handler, extract a testable helper and TDD the helper; otherwise the existing suites are the net.

**Tech Stack:** Go 1.26.5.

## Global Constraints
- SPDX on any new file; TDD; per commit `export PATH="$PATH:$HOME/go/bin"` then `go vet ./...` + `go build ./...` + `go test ./...` + `bash scripts/check-security.sh` all green.
- Fixes are behavior-correcting but must not regress the verified-clean invariants (fail-closed gates, claim atomicity, credential boundary).
- Determinism preserved (injected clocks in stores/libs).
- corral metaphor; "control owner" never "CISO".

## File Structure
- `internal/mission/engine.go` — H-1 staffing fix (+ helper); M-1 egress fail-closed. (modify)
- `internal/mission/staffing_model_test.go` or existing test file — H-1 helper test. (modify/new)
- `internal/memory/store.go`, `store_cgo.go`, `store_nocgo.go` — H-2 mutex + `SetMaxOpenConns(1)`. (modify)
- `internal/brain/tasks.go` — M-4 claimer guard. (modify)
- `internal/egress/scan.go` — M-2 over-size + `scanner.Err()`. (modify)
- `internal/repo/provider.go` — M-3 dedicated http.Client + CheckRedirect + Timeout. (modify)

---

## Task 1: H-1 — fix local-model staffing corruption (colon-split)

**Files:** Modify `internal/mission/engine.go`; Test `internal/mission/*_test.go`

**Bug:** `engine.go:321-340` derives `backend`/`model` for each clamped role, then unconditionally splits on `:` (`backend = model[:i]; model = model[i+1:]`). For an Ollama `name:tag` (`qwen2.5-coder:7b`) that yields `Backend:"qwen2.5-coder", Model:"7b"` → the assignment is dropped or 404s. Clamped values are model identifiers, never `backend:model`.

**Interfaces:** Produces `func staffedModelRef(model string) rolemodel.ModelRef` (package `mission`) — the backend-derivation with NO colon-split; the `Tick` loop calls it.

- [ ] **Step 1: Failing test** — add `internal/mission/staffing_model_test.go`:
```go
// SPDX-License-Identifier: Elastic-2.0
package mission

import "testing"

func TestStaffedModelRef(t *testing.T) {
	cases := []struct{ in, wantBackend, wantModel string }{
		{"qwen2.5-coder:7b", "ollama", "qwen2.5-coder:7b"}, // the bug: must NOT become ("qwen2.5-coder","7b")
		{"llama3.2:3b", "ollama", "llama3.2:3b"},
		{"claude-3-5-sonnet", "anthropic", "claude-3-5-sonnet"},
		{"gpt-4o", "openai", "gpt-4o"},
	}
	for _, c := range cases {
		got := staffedModelRef(c.in)
		if got.Backend != c.wantBackend || got.Model != c.wantModel {
			t.Errorf("staffedModelRef(%q) = {%q,%q}, want {%q,%q}", c.in, got.Backend, got.Model, c.wantBackend, c.wantModel)
		}
	}
}
```
- [ ] **Step 2: Run, watch fail** (`staffedModelRef` undefined).
- [ ] **Step 3: Implement.** Add the helper near the staffing code and route the loop through it:
```go
// staffedModelRef derives the model reference for a clamped assignment. Clamped
// values are model IDENTIFIERS — an Ollama name:tag (qwen2.5-coder:7b) or a cloud
// model name — NEVER "backend:model", so we must not split on ':' (doing so
// corrupted every local tag). Cloud models map to their provider backend; every-
// thing else is an Ollama tag kept whole.
func staffedModelRef(model string) rolemodel.ModelRef {
	backend := "ollama"
	if isCloudModel(model) {
		lower := strings.ToLower(model)
		switch {
		case strings.Contains(lower, "claude"):
			backend = "anthropic"
		case strings.Contains(lower, "gpt"):
			backend = "openai"
		case strings.Contains(lower, "gemini"):
			backend = "openai" // NOTE: audit L-item — engine vs backend.go gemini mapping disagree; out of scope for this task, keep as-is
		}
	}
	return rolemodel.ModelRef{Backend: backend, Model: model}
}
```
  In the `for role, model := range clamped` loop, DELETE the `if colonIdx := strings.Index(model, ":"); colonIdx >= 0 { ... }` block and the inline backend derivation, replacing the whole body with:
```go
		e.Staffing.RoleModels.Set(role, staffedModelRef(model))
```
- [ ] **Step 4: Run, watch pass.** `go test ./internal/mission/ -run TestStaffedModelRef`. Full gate. **Commit:** `fix(mission): don't colon-split clamped model tags — restores local-model staffing (H-1)`.

---

## Task 2: H-2 — fix the memory Store data race

**Files:** Modify `internal/memory/store.go`, `internal/memory/store_cgo.go`, `internal/memory/store_nocgo.go`; Test `internal/memory/*_test.go`

**Bug:** `memory.Store` is the only DuckDB/SQLite store without `SetMaxOpenConns(1)` and has no mutex, yet `EnsureBuilt`/`Build`/`Add`/`SetShared` (+ the `lastDirs` field) are reached concurrently from 3 MCP handlers + 2 UI paths.

**Interfaces:** `Store` gains an unexported `sync.Mutex`; the public mutating/build methods take it; a `buildLocked` helper is called from `EnsureBuilt` under the held lock (no re-lock → no deadlock). `Open` pins `SetMaxOpenConns(1)`.

- [ ] **Step 1: Failing test** — add a race test (run under `-race`) to `internal/memory` (pick the build tag that matches the default test build; if both cgo/nocgo build, put it in a shared `store_race_test.go`):
```go
func TestMemoryConcurrentBuildSearchNoRace(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil { t.Fatal(err) }
	defer s.Close()
	// seed a dir with one entry so Build has something to index (mirror the pkg's existing test setup)
	// ... (use the same corpus-dir helper the other memory tests use) ...
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = s.EnsureBuilt(); _, _ = s.Search("x", "", "", 5, false) }()
	}
	wg.Wait()
}
```
> Implementer: read the existing `internal/memory` tests first to reuse their corpus-dir/`Build` setup; the point is a `-race`-clean concurrent hammer of EnsureBuilt+Search (+ optionally Add).
- [ ] **Step 2: Run under -race, watch fail.** `go test -race ./internal/memory/ -run TestMemoryConcurrentBuildSearchNoRace` → RACE reported (on `lastDirs`/the FTS rebuild).
- [ ] **Step 3: Implement.**
  - `store.go`: add `mu sync.Mutex` to `Store` (near the other fields). `Add` and `SetShared` take `s.mu.Lock(); defer s.mu.Unlock()` at entry (they read/write `lastDirs` and reindex).
  - `store_cgo.go` + `store_nocgo.go`: in `Open`, after a successful `sql.Open`, add `db.SetMaxOpenConns(1)` (matching every sibling store). Rename the current `Build`'s body to an unexported `buildLocked(dirs []string) (int, error)` (NO locking inside); make `Build` a thin `s.mu.Lock(); defer s.mu.Unlock(); return s.buildLocked(dirs)`. `EnsureBuilt` takes `s.mu.Lock(); defer s.mu.Unlock()` and calls `s.buildLocked(...)` directly (never the public `Build`, to avoid re-lock/deadlock). Do this symmetrically in both cgo and nocgo files.
  - Verify no other method that runs under the lock calls a public locking method (audit for `EnsureBuilt`/`Build`/`Add`/`SetShared` calls within locked regions).
- [ ] **Step 4: Run, watch pass.** `go test -race ./internal/memory/...` → PASS, no race. Full gate. **Commit:** `fix(memory): guard Store with a mutex + SetMaxOpenConns(1) — closes the concurrent Build/Search race (H-2)`.

---

## Task 3: M-4 — complete_task must check the claimer before the verify-gate

**Files:** Modify `internal/brain/tasks.go`

**Bug:** `tasks.go:294-354` runs the entire verify-gate (jail run, files a `regression` finding, bumps the refusal counter, fires `escalateRefusalLoop`) before `q.Complete` (:355) — the only place the claimer is checked. A non-claimer triggers all those side effects on another's task.

- [ ] **Step 1:** Confirm the task's claimer field name (`TaskByID` returns a task with a claimed-by field — likely `t.ClaimedBy`). Read `internal/queue` task struct.
- [ ] **Step 2: Implement.** Immediately after `t, terr := q.TaskByID(in.ID)` + its error check, and BEFORE the `if t != nil && t.Verify != ""` gate block, add:
```go
	// Only the claimer may complete (or trip the gate on) a task. Check ownership
	// UP FRONT so a non-claimer can't burn the verify jail, file a spurious
	// regression finding, or trigger the refusal-escalation on someone else's task.
	if t != nil && t.ClaimedBy != "" && t.ClaimedBy != bee {
		return nil, completeTaskOut{OK: false, Message: "not the claimer of this task"}, nil
	}
```
  (Match the exact field name from Step 1. If a task can be legitimately completed by a non-claimer in some path, preserve that — but the verify-gate side effects must not run for a non-claimer; if unsure, gate only the `if t.Verify != ""` block on `t.ClaimedBy == bee`.)
- [ ] **Step 3: Verify.** `go build ./... && go vet ./... && go test ./...` green. This is an MCP-handler guard; if the brain test harness makes constructing a `*mcp.CallToolRequest` + a claimed task practical, add a test that a non-claimer gets `OK:false` and no finding is filed; otherwise it is review-verified against `q.Complete`'s existing claimer semantics. Full gate. **Commit:** `fix(brain): complete_task checks the claimer before running the verify-gate (M-4)`.

---

## Task 4: M-1 — egress secret-scan gate must fail closed

**Files:** Modify `internal/mission/engine.go`

**Bug:** `runEgressGate` (`engine.go:689-694`) returns `false` (not blocked → push proceeds) when `ChangedFilesRange` errors, so a git failure ships an UNSCANNED push.

- [ ] **Step 1: Failing test.** In `internal/mission`, test `runEgressGate` (or extract the diff+scan step into a testable helper if `runEgressGate` isn't directly callable) with a fake `Repo` whose `ChangedFilesRange` returns an error → the gate must report BLOCKED (return `true`), not `false`. Reuse the mission test fakes (`RepoOps`/`EgressScanner`).
- [ ] **Step 2: Run, watch fail** (currently returns false on error).
- [ ] **Step 3: Implement.** In `runEgressGate`, on the `ChangedFilesRange` error path, return the BLOCKING value (fail closed) and log it clearly:
```go
	files, err := e.Repo.ChangedFilesRange(context.Background(), e.workdir(m), m.Base)
	if err != nil {
		log.Printf("mission %d: egress: changed-files failed: %v — BLOCKING push (fail-closed: cannot scan what we can't diff)", m.ID, err)
		return true // blocked — never push unscanned
	}
```
  (Confirm the return-value polarity: the caller at `engine.go:740` treats the returned bool as "blocked?" — a `true` must withhold the push. Verify against the call site before flipping.)
- [ ] **Step 4: Run, watch pass.** Full gate. **Commit:** `fix(mission): egress gate fails closed when the diff can't be computed (M-1)`.

---

## Task 5: M-2 — close the secret-scan evasions

**Files:** Modify `internal/egress/scan.go`

**Bug (`scanSecrets`, scan.go:87-116):** files `> maxScanBytes` are silently skipped; `scanner.Err()` is never checked so a `> 1 MB` line (`ErrTooLong`) silently aborts the rest of a file.

- [ ] **Step 1: Failing tests** — add to `internal/egress/*_test.go`:
```go
func TestScanSecrets_OverSizeFileSurfaced(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "big.bin")
	// > maxScanBytes of filler; the point is it must NOT be silently ignored
	os.WriteFile(big, make([]byte, maxScanBytes+1), 0o600)
	out := scanSecrets(dir, []string{"big.bin"})
	if len(out) == 0 {
		t.Fatal("an unscannable over-size file must surface a finding, not be silently skipped")
	}
}
func TestScanSecrets_LongLineNotSilentlyAborted(t *testing.T) {
	dir := t.TempDir()
	// a >1MB single line containing a secret pattern; must be caught or surfaced, not dropped
	// (construct a line that trips one of secretRules embedded past the 1MB scanner cap
	//  OR assert a finding/advisory is produced rather than an empty result)
	// ... implementer: craft against an actual secretRule + a >1<<20 line ...
}
```
- [ ] **Step 2: Run, watch fail.**
- [ ] **Step 3: Implement.**
  - Over-size: instead of `continue` on `info.Size() > maxScanBytes`, emit an advisory finding so the operator sees it (fail-loud, not silent):
```go
		if info.Size() > maxScanBytes {
			out = append(out, Finding{Path: rel, Rule: "unscanned-large-file",
				Sample: "file exceeds scan size limit — not scanned for secrets", Severity: SeverityBlock})
			continue
		}
```
  - Long line: after the `for scanner.Scan()` loop, check `scanner.Err()`; on a non-nil error (e.g. `bufio.ErrTooLong`), surface it rather than silently ending:
```go
		if err := scanner.Err(); err != nil {
			out = append(out, Finding{Path: rel, Rule: "unscanned-remainder",
				Sample: "line too long to scan — remainder of file not scanned: " + err.Error(), Severity: SeverityBlock})
		}
```
  (History-only secrets — scanning the patch text rather than on-disk HEAD — is a larger change; note it as a follow-on, out of scope for this task.)
- [ ] **Step 4: Run, watch pass.** Full gate. **Commit:** `fix(egress): surface over-size + too-long-line files instead of silently skipping (M-2)`.

---

## Task 6: M-3 — forge REST client: strip token on cross-host redirect + timeout

**Files:** Modify `internal/repo/provider.go`

**Bug:** `get`/`doPost` use `http.DefaultClient.Do` (provider.go:126,154). Go strips only well-known auth headers on cross-host redirects, NOT the GitLab custom `PRIVATE-TOKEN`, so a redirect to an attacker host resends the token; there's also no client timeout.

**Interfaces:** A package-level `*http.Client` (`forgeHTTPClient`) with a `Timeout` and a `CheckRedirect` that (a) strips `PRIVATE-TOKEN` (and any custom auth header) on a cross-host redirect and (b) refuses redirecting an API call to a different host. `get`/`doPost` use it instead of `http.DefaultClient`.

- [ ] **Step 1: Failing test** — `internal/repo/*_test.go`, using `httptest`:
```go
func TestForgeClient_StripsTokenOnCrossHostRedirect(t *testing.T) {
	var gotToken string
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("PRIVATE-TOKEN")
		w.WriteHeader(200)
	}))
	defer attacker.Close()
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, attacker.URL, http.StatusFound) // cross-host redirect
	}))
	defer forge.Close()
	req, _ := http.NewRequest("GET", forge.URL, nil)
	req.Header.Set("PRIVATE-TOKEN", "secret-gitlab-token")
	_, _ = forgeHTTPClient.Do(req)
	if gotToken == "secret-gitlab-token" {
		t.Fatal("PRIVATE-TOKEN leaked across a cross-host redirect")
	}
}
```
- [ ] **Step 2: Run, watch fail** (`forgeHTTPClient` undefined; DefaultClient would leak it).
- [ ] **Step 3: Implement.**
```go
// forgeHTTPClient is the shared client for forge REST calls. It bounds request
// time and, on a redirect that crosses to a different host, strips the auth
// headers Go doesn't (notably GitLab's custom PRIVATE-TOKEN) so a forge redirect
// or open-redirect can't exfiltrate the token.
var forgeHTTPClient = &http.Client{
	Timeout: 30 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		if len(via) > 0 && req.URL.Host != via[0].URL.Host {
			req.Header.Del("PRIVATE-TOKEN")
			req.Header.Del("Authorization")
		}
		return nil
	},
}
```
  Replace `http.DefaultClient.Do(req)` at both `get` and `doPost` with `forgeHTTPClient.Do(req)`. (`time`/`fmt` already imported in provider.go — verify.)
  (Wiring `gateway.Guard.DialContext` for full SSRF-to-metadata protection is a larger change with a possible import-cycle; note it as a follow-on. The token-leak + timeout are the high-value core and are self-contained.)
- [ ] **Step 4: Run, watch pass.** `go test ./internal/repo/ -run TestForgeClient`. Full gate. **Commit:** `fix(repo): forge client strips auth on cross-host redirect + adds a timeout (M-3)`.

---

## Self-Review
- **Coverage:** H-1→T1, H-2→T2, M-4→T3, M-1→T4, M-2→T5, M-3→T6. (H-3 auth = separate design pass; mission/queue reliability Mediums + N+1/DRY + Lows = later batch — out of scope, stated.)
- **Testability:** buried logic extracted to helpers (`staffedModelRef`; the egress gate helper if needed) so TDD is clean; the memory fix is proven by a `-race` test; the MCP-handler guard (T3) is review-verified if req construction is impractical.
- **No regression to clean invariants:** T4/T5 make gates MORE strict (fail-closed); T2 adds synchronization without changing query logic; T6 only narrows redirect behavior; T1 fixes a parse; T3 adds an ownership check. None touches the ledger/jail/claim-atomicity cores.
- **Polarity checks flagged:** T4 (return-bool "blocked?") and T3 (claimer field name) each carry an explicit "verify against the call site/struct before flipping" step.

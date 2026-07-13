<!-- SPDX-License-Identifier: Elastic-2.0 -->
# Audit fix batch 3 — re-audit findings (SSRF, UI-authz, egress, Lows) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`).

**Goal:** Close the open findings from the 2026-07-13 opus re-audit — the 5 Mediums (browser DNS-SSRF, `/api/instruct` + `/api/mission/intercept` authz gaps, egress history-only-secret evasion, govulncheck unsandboxed) plus the security Lows (forge SSRF dial-guard + query-escape, lookbook writes, macOS jail gaps, staffing map-leak + no-recover, sqlguard skip invariant, empty-principals startup warning, gateway docker-bridge constant). The HIGH (sqlguard shared-pool lockdown) + one Medium (validator bypass) were already fixed (merge 9f64cea).

**Architecture:** A new shared `internal/netguard` package holds the SSRF primitives (`UnsafeIP` + a resolve-and-pin `DialContext`), consumed by gateway (refactor), browser, and forge. The rest are targeted fixes to `internal/ui`, `internal/egress` (+ a `repo` diff-text helper), `internal/sandbox`, `internal/mission`, `cmd/corral`. Phases are independent; each phase boundary is a review checkpoint.

**Tech Stack:** Go 1.26.5, module `github.com/pdbethke/corralai`.

## Global Constraints
- SPDX (`// SPDX-License-Identifier: Elastic-2.0`) on every new file; TDD; per commit run `export PATH="$PATH:$HOME/go/bin"` then `go vet ./...` + `go build ./...` + `go test ./...` + `bash scripts/check-security.sh` — all green.
- Security TIGHTENING only — never loosen a verified-clean invariant (jail nil-backend, credential boundary, claim atomicity, human-gate, the gateway resolve-and-pin guard).
- corral metaphor; "control owner", never "CISO"; no bee/hive/swarm in new prose.
- Every gate that already exists as an MCP twin must be mirrored, not reinvented — factor a shared helper where the plan says so.

---

# PHASE 1 — SSRF hardening (shared netguard + browser + forge)

## Task 1.1: extract `internal/netguard` (shared SSRF primitives) + refactor gateway onto it

**Files:** Create `internal/netguard/netguard.go`, `internal/netguard/netguard_test.go`; Modify `internal/gateway/egress.go`

**Why:** `gateway.unsafeIP` (`internal/gateway/egress.go:44`) + `Guard.DialContext` (`:50-74`) are the correct resolve-then-pin SSRF guard, but `unsafeIP` is unexported so browser/forge can't reuse it. Extract the primitives to a neutral package both can import (avoids a `repo → gateway` dependency).

**Interfaces (produced):**
- `netguard.UnsafeIP(ip net.IP) bool` — loopback ∥ private ∥ unspecified ∥ link-local uni/multi ∥ (interface-local) multicast (verbatim logic from `gateway.unsafeIP`).
- `netguard.Guard` struct + `netguard.NewGuard(allowHosts []string) *Guard` + `(*Guard) DialContext(ctx, network, addr) (net.Conn, error)` — resolve host, reject `UnsafeIP` unless host is allowlisted, pin the validated IP (verbatim from `gateway.Guard`).

- [ ] **Step 1: Write `internal/netguard/netguard_test.go`** (SPDX, `package netguard`) — the first direct unit tests for the guard:
```go
func TestUnsafeIP(t *testing.T) {
	cases := []struct{ ip string; unsafe bool }{
		{"169.254.169.254", true}, {"127.0.0.1", true}, {"10.0.0.5", true},
		{"192.168.1.1", true}, {"::1", true}, {"fe80::1", true}, {"0.0.0.0", true},
		{"8.8.8.8", false}, {"1.1.1.1", false}, {"93.184.216.34", false},
	}
	for _, c := range cases {
		if got := UnsafeIP(net.ParseIP(c.ip)); got != c.unsafe {
			t.Errorf("UnsafeIP(%s)=%v want %v", c.ip, got, c.unsafe)
		}
	}
}
```
  Add a `DialContext` test: an `httptest.NewServer` (binds 127.0.0.1) is refused by a default Guard, but ALLOWED when its host is in `NewGuard([]string{"127.0.0.1"})` — proving the allowlist seam works.
- [ ] **Step 2: Run, watch fail** (package undefined).
- [ ] **Step 3: Implement `internal/netguard/netguard.go`** — move `UnsafeIP` (exported) + `Guard`/`NewGuard`/`DialContext` from `gateway/egress.go` verbatim (rename `unsafeIP`→`UnsafeIP`). Keep the 10s dial timeout and the pin-the-validated-IP behavior.
- [ ] **Step 4: Refactor `internal/gateway/egress.go`** to delegate: either `type Guard = netguard.Guard` + `var NewGuard = netguard.NewGuard` (alias, zero call-site churn at `cmd/corral/main.go:579`, `brain/gateway.go:242`, `brain/reference.go:48`), OR keep `gateway.Guard` as a thin wrapper. Prefer the alias to avoid touching consumers. Delete the now-duplicated `unsafeIP`. Run `go test ./internal/gateway/... ./internal/brain/... ./internal/netguard/...` green.
- [ ] **Step 5: Commit.** Full gate. **Commit:** `refactor(netguard): extract shared SSRF resolve-and-pin guard from gateway (net-new unit tests)`.

## Task 1.2: browser SSRF — resolve DNS and check every IP (close the DNS-alias bypass)

**Files:** Modify `internal/brain/browser.go`; Test `internal/brain/browser_test.go`

**Bug:** `blockedHost` (`browser.go:90`) is lexical-only (`net.ParseIP` on the literal host) — `http://169-254-169-254.sslip.io/…` resolves to `169.254.169.254` and reaches the cloud metadata service; any authenticated agent can call `browser_navigate`. The browser DELIBERATELY allows loopback/private (localhost app-testing), so the fix is NOT `netguard.UnsafeIP` — it must resolve the host and check each IP against a **metadata/link-local-only** predicate.

**Interfaces:** `guardNavigateURL(raw, selfAddr string) error` gains DNS resolution. Add `func metadataOrLinkLocal(ip net.IP) bool` — link-local uni/multicast OR an explicit IMDS literal set (`169.254.169.254`, `100.100.100.100` Alibaba, `fd00:ec2::254` AWS IPv6 IMDS). Keep the `metadataHosts` hostname map + `isBrainAddr`.

- [ ] **Step 1: Failing test** — extend `TestGuardNavigateURL` (`browser_test.go:42`). Since the suite does no real DNS, resolve via a hostname the runtime CAN resolve to a link-local/metadata address, or inject a resolver seam. Add a package-level `var lookupIP = net.DefaultResolver.LookupIPAddr` and override it in the test to return `169.254.169.254` for a sentinel host — assert `guardNavigateURL("http://evil.test/", brain)` is BLOCKED. Also assert a hostname resolving only to a public IP is ALLOWED, and `http://127.0.0.1:8080/` (a non-brain loopback port) stays ALLOWED (localhost app-testing preserved).
- [ ] **Step 2: Run, watch fail** (current lexical guard allows the DNS-alias host).
- [ ] **Step 3: Implement.** In `guardNavigateURL`, after the scheme + `blockedHost(hostname)` + `isBrainAddr` checks, resolve the host and reject if ANY resolved IP is `metadataOrLinkLocal`:
```go
	host := u.Hostname()
	ips, err := lookupIP(context.Background(), host)
	if err != nil {
		return fmt.Errorf("blocked: cannot resolve %q", host)
	}
	for _, ipa := range ips {
		if metadataOrLinkLocal(ipa.IP) {
			return fmt.Errorf("blocked: %q resolves to a cloud-metadata / link-local address, off-limits to the agent browser", host)
		}
	}
```
  Add `metadataOrLinkLocal` (link-local uni/multicast + the explicit IMDS IP set). Add the `lookupIP` seam var + `context` import.
  **Note (follow-on, out of scope):** Chromium resolves independently, so a rebind between this check and the fetch is still possible; the durable fix is launching rod with a guarded proxy (`l.Set("proxy-server", …)`). Log this as a deferred hardening; resolve-and-check closes the free public-DNS-alias bypass now.
- [ ] **Step 4 (Low: isBrainAddr non-loopback).** In `isBrainAddr` (`browser.go:106`), also treat the URL host as the brain when it equals `selfAddr`'s host (not only `localhost`/loopback), and block `0.0.0.0`/`::`. Add a test case (selfAddr a non-loopback host:port → that host is blocked).
- [ ] **Step 5: Run, watch pass.** Full gate + `go test ./internal/brain/ -run 'Browser|Guard'`. **Commit:** `fix(brain): resolve DNS + reject metadata/link-local in the agent browser SSRF guard (audit M)`.

## Task 1.3: forge REST client — SSRF dial-guard + escape `rcListOpenPRs`

**Files:** Modify `internal/repo/provider.go`; Test `internal/repo/*_test.go`

**Bug A (SSRF, Low):** `forgeHTTPClient` (`provider.go:23`) has no `Transport`/`DialContext` — a forge base URL or a redirect to `169.254.169.254`/RFC1918 is dialed unguarded (auth is already stripped cross-host, so blind). **Bug B (Low):** `rcListOpenPRs` (`provider.go:346`) concatenates `&base=`+base raw (the batch-2 escape fix missed this call site).

**Design:** give `forgeHTTPClient` a `Transport{DialContext: guard.DialContext}` from a `netguard.Guard` built with an allowlist env (`CORRALAI_FORGE_ALLOWED_HOSTS`) so a sanctioned self-hosted forge on a private network can be opted in; default-deny private/link-local. Escape `rcListOpenPRs` via `url.Values` (mirror `rcFindOpenPR` at `:222`).

- [ ] **Step 1: Failing tests.** (a) `TestForgeClient_BlocksLinkLocalRedirect`: a forge `httptest` server 302s to `http://169.254.169.254/` (or a host resolving there); assert `forgeHTTPClient.Do` errors (SSRF-blocked) instead of connecting. NOTE the guard must allowlist `127.0.0.1` so the `httptest` forge itself is reachable — build the client's guard with the loopback allowlisted in the test (expose a test seam or construct the transport in the test). (b) `TestListOpenPRsQueryEscaping`: mirror `provider_query_escape_test.go` — capture `r.URL.Query().Get("base")` with a `&`/space-laden base, assert it round-trips intact.
- [ ] **Step 2: Run, watch fail.**
- [ ] **Step 3: Implement.**
  - Give `forgeHTTPClient` a transport whose `DialContext` is `netguard.NewGuard(splitList(os.Getenv("CORRALAI_FORGE_ALLOWED_HOSTS"))).DialContext`. Because `forgeHTTPClient` is a package `var`, initialize the guard in an `init()` or make it a package var; keep the existing `CheckRedirect` (header strip) — the `DialContext` re-validates every hop. Preserve the 30s Timeout.
  - `rcListOpenPRs`: `q := url.Values{"state": {"open"}, "per_page": {"100"}}; if base != "" { q.Set("base", base) }; url := rc.base + "/repos/" + owner + "/" + repo + "/pulls?" + q.Encode()`.
- [ ] **Step 4: Run, watch pass.** Full gate. **Commit:** `fix(repo): SSRF dial-guard on the forge client (+ allowlist) and escape rcListOpenPRs base (audit Low)`.

---

# PHASE 2 — UI HTTP authz parity

## Task 2.1: gate `/api/instruct`, `/api/mission/intercept`, and lookbook writes on isSuperuser

**Files:** Modify `internal/ui/ui.go`; Test `internal/ui/*_test.go`

**Bug:** three UI HTTP handlers weren't kept in sync with their hardened MCP twins.
- `/api/instruct` (`ui.go:845`) gates only `auth.ReadOnly` — a non-admin member can inject instructions into ANY principal's agents (MCP `send_instruction` gates on `canInstruct`). Cross-principal command injection.
- `/api/mission/intercept` (`ui.go:1181`) has NO authz gate — a read-only observer can `SetInterceptPending`/tear down live sessions (stall/DoS the fleet). Its WS twin `guardTerminalWS` reserves operator control for superusers.
- `lookbookUpload` (`ui.go:1333`) / `lookbookDelete` (`ui.go:1400`) gate only `auth.ReadOnly` — any member can add/remove fleet-shared design directives (memory/reference promotion is `isHumanAdmin`-gated).

**Design:** the UI has no agent-namespace helper and these callers are human operators — gate all three on `s.isSuperuser(r)` (the UI's `isHumanAdmin` twin: superuser AND not a subagent), after the existing `auth.ReadOnly` check. Make `intercept` and `lookbookDelete` POST-only.

- [ ] **Step 1: Failing tests** (`internal/ui`, reuse `observerWrap` from `observer_scope_test.go:20`, the `bearerWrap`+subagent variant from `proposals_test.go:35/157`, and nil-Roles dev mode). For EACH of the three endpoints assert: observer bearer → 403; subagent-of-superuser bearer → 403; real superuser bearer → 200 + side-effect; nil-Roles dev → 200. For `intercept`/`lookbookDelete` also assert GET → 405. Assert the underlying store (`s.coord`, `s.hosts`/`Registry`, `s.taskArtifacts`) is UNMUTATED on the 403 paths.
- [ ] **Step 2: Run, watch fail** (current handlers let a non-superuser through).
- [ ] **Step 3: Implement.** In each handler, after the `auth.ReadOnly` check, add:
```go
	if !s.isSuperuser(r) {
		http.Error(w, "forbidden: superuser only", http.StatusForbidden)
		return
	}
```
  For `/api/instruct` the message: "forbidden: superuser only (instructing an agent is an operator action)". For `intercept`: add the `auth.ReadOnly` check (it has none today) + the superuser check + `if r.Method != http.MethodPost { 405 }`. For `lookbookDelete`: add `if r.Method != http.MethodPost { 405 }`. (Confirm the UI's JS callers use POST for intercept/lookbookDelete; update the fetch calls in `internal/ui/web/*.js`/`index.html` if they use GET — grep for `/api/mission/intercept` and `/api/lookbook/delete`.)
- [ ] **Step 4: Run, watch pass.** Full gate + `go test ./internal/ui/...`. **Commit:** `fix(ui): gate instruct / mission-intercept / lookbook writes on isSuperuser — parity with the MCP twins (audit M+Low)`.

---

# PHASE 3 — Egress gate hardening

## Task 3.1: scan git HISTORY for secrets, not just the working tree

**Files:** Modify `internal/repo/changed.go` (add a diff-text helper), `internal/mission/engine.go` (RepoOps interface + runEgressGate), `internal/egress/scan.go` (add `ScanText`); Test `internal/egress/*_test.go`, `internal/repo/*_test.go`

**Bug:** `scanSecrets` (`scan.go:87`) reads the CURRENT working-tree file. A secret committed in an earlier phase and then deleted (clean final tree) is missed — but the push ships the full branch history (`base..HEAD`), so the secret leaves. The net `git diff base...HEAD` and a squash BOTH miss it (net effect: absent). The only correct detector is scanning EVERY commit's added lines: `git log -p base..HEAD`.

**Interfaces:**
- `repo`: add `func (e *Engine) DiffAddedLines(ctx, dir, base string) (string, error)` = `e.git(ctx, dir, "log", "-p", "--no-color", "--unified=0", base+"..HEAD")` returning the raw patch text. (Note `e.git` returns `redact`ed output — fine; redact only masks the configured forge token, not planted secrets.)
- `mission.RepoOps` interface (`engine.go`): add `DiffAddedLines(ctx, dir, base string) (string, error)`.
- `egress`: add `func ScanText(text string) []Finding` — runs `secretRules` over each `+`-prefixed added line (strip the leading `+`, skip `+++ ` file headers) of a `git log -p` patch, emitting `SeverityBlock` findings (path best-effort from the nearest `+++ b/...` header, line unknown/0).

- [ ] **Step 1: Failing test** (`internal/egress/scan_test.go`) — new `TestScanText_CatchesHistoryOnlySecret`: feed a `git log -p`-shaped patch string containing a `+AWS_SECRET_ACCESS_KEY=…` added line (that a later hunk removes); assert `ScanText` returns a `SeverityBlock` finding for the secret rule. Plus a `internal/repo` test for `DiffAddedLines` using the git-repo-in-tempdir pattern from `changed_test.go`: init a repo, commit a secret, commit its deletion, assert `DiffAddedLines(base)` contains the secret line while `os.ReadFile` of the working tree does not.
- [ ] **Step 2: Run, watch fail.**
- [ ] **Step 3: Implement** `ScanText` (reuse `secretRules`), `DiffAddedLines`, thread it through `RepoOps`, and in `runEgressGate` (`engine.go:775`) — after the working-tree `Scan`, ALSO get `diff, _ := e.Repo.DiffAddedLines(ctx, dir, m.Base)` and merge `e.Egress.ScanText(diff)` findings (a `ScanText` on the `EgressScanner` interface, or scan in the gate). Block on any block-severity finding (existing loop). Keep the working-tree scan (belt-and-suspenders: line numbers + on-disk-only content).
- [ ] **Step 4: Run, watch pass.** Full gate. **Commit:** `fix(egress): scan git history added-lines for secrets — commit-then-delete no longer evades the gate (audit M)`.

## Task 3.2: harden `scanGoVuln` — scrub env + neuter the toolchain

**Files:** Modify `internal/egress/scan.go`; Test `internal/egress/*_test.go`

**Bug:** `scanGoVuln` (`scan.go:190`) runs `govulncheck ./...` with `cmd.Dir=<untrusted mission workdir>`, **no `cmd.Env`** (inherits the brain's full secret env), outside the jail. `go/packages` + cgo `#cgo` flags + `GOTOOLCHAIN=auto` toolchain-fetch on hostile source = potential brain-host RCE with credentials. (Prod-mitigated today: govulncheck not installed on the host + repo-work disabled — but the code ships the insecure posture.)

**Design:** self-contained env hardening (no interface churn): set `cmd.Env` to `sandbox.MinimalEnv()` (no secrets) plus `GOTOOLCHAIN=local` (no toolchain download), `GOFLAGS=-mod=readonly` (no module mutation/fetch write), `CGO_ENABLED=0` (no cgo compiler exec), `GONOSUMDB=*`/`GOFLAGS`. Import `internal/sandbox` (no cycle: egress is imported only by `cmd/corral`). Keep the advisory/best-effort contract (LookPath-absent → skip).

- [ ] **Step 1: Failing test** — `TestScanGoVuln_UsesHardenedEnv`. Since driving real govulncheck is heavy, extract the env construction into a testable helper `govulnEnv() []string` and assert it (a) contains `GOTOOLCHAIN=local`, `CGO_ENABLED=0`, `GOFLAGS=-mod=readonly`, and (b) does NOT contain any entry sourced from a sentinel secret env var (set `CORRAL_TOKEN=SECRET` via `t.Setenv`, assert no element contains `SECRET`).
- [ ] **Step 2: Run, watch fail** (`govulnEnv` undefined; current code sets no env).
- [ ] **Step 3: Implement** `govulnEnv()` = `append(sandbox.MinimalEnv(), "GOTOOLCHAIN=local", "GOFLAGS=-mod=readonly", "CGO_ENABLED=0", "GONOSUMDB=*")`; set `cmd.Env = govulnEnv()` in `scanGoVuln`. Add a comment noting the residual (go/packages still type-checks; the jail-run option is the fuller fix, deferred).
- [ ] **Step 4: Run, watch pass.** Full gate. **Commit:** `fix(egress): run govulncheck with a scrubbed, toolchain-frozen env — no secret exposure / cgo-RCE on untrusted code (audit M)`.

---

# PHASE 4 — Lows & hardening

## Task 4.1: macOS jail — add /usr/local, resource ceiling, private tmp

**Files:** Modify `internal/sandbox/isolator_darwin.go`; Test `internal/sandbox/isolator_darwin_test.go` (`//go:build darwin`)

**Bug (dev-backend, Low):** (a) read-allowlist omits `/usr/local` (Intel Homebrew / manual toolchains) → builds fail → pressure to disable the jail; (b) no ulimit/pids ceiling (fork-bomb) vs bwrap's `rlimitPrelude`; (c) shares host `/tmp`/`/var/tmp`/`/private/tmp` (write) vs bwrap's private tmpfs.

- [ ] **Step 1:** Add `/usr/local` to the read-allow subpath list (`isolator_darwin.go:61`). Prepend a ulimit prelude to the `sh -c` command (mirror `rlimitPrelude` in `isolator_linux.go:19` — `ulimit -u 512; ulimit -f …;`). For tmp: prefer confining writes to a per-run dir under the workspace and denying the shared tmp subpaths; if that's too invasive, at minimum document the weaker guarantee in a code comment. Update/extend `isolator_darwin_test.go` to assert `/usr/local` is allowed and the ulimit prelude is present in the argv. Verify `GOOS=darwin go vet ./internal/sandbox/`.
- [ ] **Step 2:** Full gate on the host (linux) + `GOOS=darwin go vet`. **Commit:** `fix(sandbox): macOS jail allows /usr/local, adds a ulimit ceiling, tightens tmp (audit Low)`.

## Task 4.2: mission staffing — clean bookkeeping maps + recover() in the goroutine

**Files:** Modify `internal/mission/engine.go`; Test `internal/mission/*_test.go`

**Bug (Low):** `staffed`/`staffAttempts`/`staffGaveUp` are never deleted on mission end (small leak; sibling maps ARE cleaned in `failMission`/`finishRepoMission`). And `staffMission` has no `recover()` — a panic takes the whole tick loop down, defeating the fault-isolation the off-tick move was meant to give.

- [ ] **Step 1:** Delete the three staff-map keys wherever `noProgress`/`committed` are cleaned (`failMission` ~`:615`, `finishRepoMission` ~`:837`) — under `staffMu`. Add `defer func(){ if r := recover(); r != nil { log.Printf("mission %d: staffing panic recovered: %v", missionID, r); e.staffMu.Lock(); e.staffGaveUp[missionID]=true; e.staffMu.Unlock() } }()` at the top of `staffMission`. Add a test: a `fakeLLM` whose `Generate` panics → the engine survives (Tick still returns) and the mission is latched give-up (no re-dispatch).
- [ ] **Step 2:** `go test -race ./internal/mission/...` green. Full gate. **Commit:** `fix(mission): recover() in the staffing goroutine + evict staff bookkeeping on mission end (audit Low)`.

## Task 4.3: misc hardening — sqlguard skip invariant, empty-principals startup warning, gateway constant

**Files:** Modify `internal/sqlguard/sqlguard.go` (comment/test), `cmd/corral/main.go` (startup warn), `internal/brain/gateway.go` (constant)

- [ ] **Step 1 (sqlguard skip invariant, Low).** The idempotent `ApplyLockdown` skip is sound but rests on undocumented invariants. Add a code comment pinning them (lock_configuration only set by ApplyLockdown; disabled_filesystems is DB-wide) and a unit test that a fresh conn without lockdown is NOT skipped. (Now only oracle's single-pinned-conn reaches ApplyLockdown after batch-2's fix, but keep it correct.)
- [ ] **Step 2 (empty-principals startup warning, Info).** In `cmd/corral/main.go`, after building the verifier + principals store, if auth is ENABLED but the principals table has no superuser seeded, `log.Printf` a LOUD warning ("auth: WARNING — OIDC enabled but no superuser seeded; every authenticated principal is treated as admin until you set CORRALAI_ADMIN_PRINCIPALS or run create_superuser"). Don't fail-closed (bootstrap needs it), just warn. (Check `principals.Store` for a `Count`/`HasSuperuser` method; add one if absent.)
- [ ] **Step 3 (gateway docker-bridge constant, Info).** `internal/brain/gateway.go:235` hardcodes `172.19.0.1:9021 → localhost:9021`. Make it config-driven (env `CORRALAI_GATEWAY_HOST_REWRITE="from=to"`) or remove it; if kept, gate on the env being set. Small.
- [ ] **Step 4:** Full gate. **Commit(s):** `fix(sqlguard): pin the ApplyLockdown idempotent-skip invariant (comment+test)`; `feat(corral): warn at startup when auth is on but no superuser is seeded`; `refactor(gateway): make the docker-bridge host rewrite config-driven`. (Separate commits; group only if sharing a test.)

---

## Self-Review
- **Coverage:** M2 browser-SSRF→1.2; forge-SSRF+escape→1.3 (Lows); M3 instruct + M4 intercept + lookbook→2.1; M5 history-secret→3.1; M1 govulncheck→3.2; macOS jail→4.1; staffing map/recover→4.2; sqlguard-skip + empty-principals + gateway-const→4.3. netguard extraction→1.1 (enabler for 1.2/1.3). Info items preferred_username + concurrent-RoleModels = doc/moot, out of code scope (note in memory).
- **Design decisions flagged:** browser uses a metadata/link-local predicate (NOT netguard.UnsafeIP) so loopback/private stay allowed; the proxy/host-resolver-rules TOCTOU-proof fix is deferred (resolve-and-check closes the free bypass). M5: `git log -p base..HEAD` (per-commit added lines), NOT the net diff or a squash (both miss commit-then-delete). M1: env-scrub (option b) not jail-run (option a) — self-contained, no interface churn.
- **No regression to clean invariants:** all changes are tightenings (add gates, resolve+block, scrub env, scan more). netguard extraction is behavior-preserving for gateway (alias). The browser predicate keeps the deliberate loopback/private allowance. None touches the ledger/jail-nil-backend/claim-atomicity cores.
- **Test seams:** browser `lookupIP` var; forge guard allowlist for the httptest loopback; govuln `govulnEnv()` helper; egress `ScanText` on patch text; the git-repo-in-tempdir pattern (from `internal/repo/*_test.go`) for `DiffAddedLines`.

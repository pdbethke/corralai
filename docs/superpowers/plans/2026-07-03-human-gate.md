# The Human Gate Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the gap where "human" was actually "principal": every admin write the learning loop's trust model depends on now refuses a delegated subagent (auth on) and an honestly-identifying worker session (dev mode), not just a non-superuser.

**Architecture:** One rule — `isHumanAdmin` — replaces `isAdmin` at all six admin write paths (`approve_proposal`, `reject_proposal`, `add_memory(shared=true)`, `promote_memory`, `promote_reference`, and the UI's `/api/proposal/approve|reject`). Auth-on enforcement reads the existing `subagentOf(req)` claim a delegation token always carries. Dev-mode enforcement adds a new session-scoped `WorkerSessions` tracker fed by two honest-by-default signals (MCP `ClientInfo.Name`, and the `bootstrap`/`report_host` calls every shipped worker makes). A drive-by fix threads the UI's real verified principal into the approval fan-out instead of a hardcoded `"operator"` actor.

**Tech Stack:** Go 1.26, `github.com/modelcontextprotocol/go-sdk v1.6.1` (mcp + auth packages), existing `internal/auth`, `internal/brain`, `internal/ui` packages.

## Global Constraints

- Spec: `docs/superpowers/specs/2026-07-03-human-gate-design.md` — read it first.
- One rule enforced at all six gated paths — do not invent a second mechanism.
- Auth-on: refuse when `Extra["subagent"]` is non-empty (a delegation token always carries it).
- Dev-mode: a **truthfulness guardrail, not a security boundary** — the code comment on `WorkerSessions` must say exactly this.
- In-process subagents sharing their parent's session/token is an **accepted, documented limitation** — do not attempt to close it in this plan.
- Corral metaphor in all user-facing strings (herd/corral, never bee/hive — memory `corralai-metaphor`). The gated-tool refusal error is in corral voice: "the human gate: workers propose, the operator disposes."
- Every commit message ends with `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>`.
- All new code: `// SPDX-License-Identifier: Elastic-2.0` header, gosec-clean (annotate deliberate patterns with `// #nosec Gxxx -- reason`), `go vet` clean.
- House testing style: table-lite, `t.Fatalf` with got/want; mirror `internal/brain/learn_wire_test.go`'s harness patterns and `internal/ui/proposals_test.go`'s `bearerWrap` pattern.
- The learning-loop wire tests (`internal/brain/learn_wire_test.go`, `internal/ui/proposals_test.go`, `internal/ui/ui_test.go`) must stay green throughout — Task 4 updates two call sites whose function signature changes.

---

### Task 1: The human-gate rule — `isHumanAdmin` (brain) + `auth.Subagent` (UI twin)

**Files:**
- Modify: `internal/brain/identity.go` (add `isHumanAdmin` after `isAdmin`, currently `identity.go:205-212`)
- Modify: `internal/brain/identity_test.go` (extend with the delegation-token negative test)
- Modify: `internal/auth/oidc.go` (add `Subagent` after `ReadOnly`, currently `oidc.go:154-162`)
- Create: `internal/auth/subagent_test.go`

**Interfaces:**
- Consumes: `(o Options) isAdmin(req) bool` and `subagentOf(req) string` (both already exist in `internal/brain/identity.go`); `sdkauth.TokenInfoFromContext(ctx)` (already used by `auth.ReadOnly`).
- Produces (later tasks rely on these exact names):
  - `func (o Options) isHumanAdmin(req *mcp.CallToolRequest) bool` — Task 2 swaps all six gates onto this; Task 3 extends its body (do not rename it).
  - `func Subagent(ctx context.Context) bool` in package `internal/auth` — Task 2's UI gate and Task 4's actor-passthrough both read it via `auth.Subagent(r.Context())`.

- [ ] **Step 1: Write the failing unit tests for `Subagent`**

```go
// SPDX-License-Identifier: Elastic-2.0

package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
)

// TestSubagentDetectsDelegationToken proves Subagent reads the exact claim a
// REAL minted delegation token carries — not a hand-built stub — by running a
// token all the way through EnableDelegation -> MintDelegation -> VerifyToken
// -> RequireBearerToken, the same path production bearer auth uses.
func TestSubagentDetectsDelegationToken(t *testing.T) {
	vf := &Verifier{}
	vf.EnableDelegation([]byte("test-delegation-key"))
	tok, err := vf.MintDelegation("boss@x.com", "boss@x.com/child", time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	var gotSubagent bool
	handler := sdkauth.RequireBearerToken(vf.VerifyToken, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSubagent = Subagent(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if !gotSubagent {
		t.Fatal("Subagent(ctx) must be true for a delegation token")
	}
}

// TestSubagentFalseForHumanToken proves a human's own OIDC-shaped token (no
// subagent claim, only principal/email like oidc.go's VerifyToken sets) does
// NOT trip Subagent.
func TestSubagentFalseForHumanToken(t *testing.T) {
	verify := func(_ context.Context, token string, _ *http.Request) (*sdkauth.TokenInfo, error) {
		return &sdkauth.TokenInfo{
			Expiration: time.Now().Add(time.Hour),
			UserID:     token,
			Extra:      map[string]any{"principal": token, "email": token},
		}, nil
	}

	var gotSubagent bool
	handler := sdkauth.RequireBearerToken(verify, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSubagent = Subagent(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer human@x.com")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if gotSubagent {
		t.Fatal("Subagent(ctx) must be false for a human's OIDC-shaped token")
	}
}

func TestSubagentFalseWithNoToken(t *testing.T) {
	if Subagent(context.Background()) {
		t.Fatal("Subagent(ctx) with no TokenInfo in context must be false")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/auth/ -run Subagent -count=1`
Expected: FAIL (`Subagent` undefined)

- [ ] **Step 3: Implement `Subagent` in `internal/auth/oidc.go`**

Insert immediately after the `ReadOnly` function (after line 162, before `// NewVerifier builds a verifier...`):

```go
// Subagent reports whether the request's verified bearer is a delegation
// token minted for a subagent (Extra["subagent"] non-empty) rather than a
// human's own OIDC token. UI action handlers that must be human-only gate on
// this alongside isSuperuser — the exact rule internal/brain's isHumanAdmin
// enforces over MCP: a delegation token always rolls UserID up to its
// principal (so it still passes a superuser check), but it must never pass
// as the human who clicked approve.
func Subagent(ctx context.Context) bool {
	if ti := sdkauth.TokenInfoFromContext(ctx); ti != nil && ti.Extra != nil {
		s, _ := ti.Extra["subagent"].(string)
		return s != ""
	}
	return false
}
```

- [ ] **Step 4: Run to green**

Run: `go test ./internal/auth/ -count=1`
Expected: PASS

- [ ] **Step 5: Write the failing brain-side test — `isHumanAdmin` refuses a real delegation token**

Extend `internal/brain/identity_test.go`. Replace the whole file with:

```go
// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/auth"
	"github.com/pdbethke/corralai/internal/principals"
)

func reqWith(principal, tenant string) *mcp.CallToolRequest {
	return &mcp.CallToolRequest{Extra: &mcp.RequestExtra{TokenInfo: &sdkauth.TokenInfo{
		UserID: principal,
		Extra:  map[string]any{"tenant_id": tenant},
	}}}
}

func TestIdentityIsAuthoritative(t *testing.T) {
	// A verified principal overrides whatever name the client supplied.
	if got := identity(reqWith("real@x.com", ""), "FakeName"); got != "real@x.com" {
		t.Errorf("identity = %q, want verified principal to win", got)
	}
	// No token (dev mode) falls back to the client-supplied name.
	if got := identity(nil, "Fallback"); got != "Fallback" {
		t.Errorf("identity(nil) = %q, want fallback", got)
	}
	if got := identity(&mcp.CallToolRequest{}, "Fallback"); got != "Fallback" {
		t.Errorf("identity(no token) = %q, want fallback", got)
	}
	// Tenant rides along.
	if _, tn := actor(reqWith("u@x.com", "acme")); tn != "acme" {
		t.Errorf("tenant = %q, want acme", tn)
	}
}

func TestMemoryOwnerGate(t *testing.T) {
	open := Options{}
	if !open.isMemoryOwner(reqWith("a@x.com", "")) {
		t.Error("empty owners => memory open to any authorized caller")
	}
	o := Options{MemoryOwners: map[string]bool{"owner@x.com": true}}
	if !o.isMemoryOwner(reqWith("owner@x.com", "")) {
		t.Error("owner must be allowed")
	}
	if o.isMemoryOwner(reqWith("other@x.com", "")) {
		t.Error("non-owner must be denied")
	}
	if o.isMemoryOwner(nil) {
		t.Error("no identity must be denied when owners are set")
	}
}

// TestIsHumanAdminRefusesDelegationToken is the human gate's auth-on unit
// test: a REAL minted delegation token (EnableDelegation -> MintDelegation ->
// VerifyToken — the same path production bearer auth uses, not a hand-built
// TokenInfo) for a subagent spawned under a superuser principal must still
// pass isAdmin (the gap this feature closes — UserID rolls up to the
// principal) but must be refused by isHumanAdmin.
func TestIsHumanAdminRefusesDelegationToken(t *testing.T) {
	pstore, err := principals.Open(filepath.Join(t.TempDir(), "p.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pstore.Close() })
	if err := pstore.CreateSuperuser("boss@x.com", "test"); err != nil {
		t.Fatal(err)
	}
	o := Options{Principals: pstore}

	// The superuser's own token: no subagent claim, passes both gates.
	human := reqWith("boss@x.com", "")
	if !o.isAdmin(human) || !o.isHumanAdmin(human) {
		t.Fatal("the superuser's own token must pass both isAdmin and isHumanAdmin")
	}

	// A real delegation token minted for a subagent under that superuser.
	vf := &auth.Verifier{}
	vf.EnableDelegation([]byte("test-delegation-key"))
	tok, err := vf.MintDelegation("boss@x.com", "boss@x.com/child", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ti, err := vf.VerifyToken(context.Background(), tok, nil)
	if err != nil {
		t.Fatal(err)
	}
	delegated := &mcp.CallToolRequest{Extra: &mcp.RequestExtra{TokenInfo: ti}}

	if !o.isAdmin(delegated) {
		t.Fatal("a delegation token rolled up to a superuser must still pass isAdmin (the gap this feature closes)")
	}
	if o.isHumanAdmin(delegated) {
		t.Fatal("isHumanAdmin must refuse a delegation token even when isAdmin passes")
	}
	if subagentOf(delegated) != "boss@x.com/child" {
		t.Fatalf("subagentOf = %q, want boss@x.com/child", subagentOf(delegated))
	}
}
```

- [ ] **Step 6: Run to verify failure**

Run: `go test ./internal/brain/ -run TestIsHumanAdmin -count=1`
Expected: FAIL (`o.isHumanAdmin` undefined)

- [ ] **Step 7: Implement `isHumanAdmin` in `internal/brain/identity.go`**

Insert immediately after `isAdmin` (after line 212, before `// actor returns the verified principal...`):

```go
// isHumanAdmin is the human gate: isAdmin PLUS proof the caller isn't a
// delegated subagent riding on a superuser's rolled-up authorization. A
// delegation token always carries Extra["subagent"] (oidc.go's
// verifyDelegation), so an agent spawned under a superuser principal passes
// isAdmin — the gap this closes. Every admin write that shapes fleet-wide
// behavior (proposal approval/rejection, memory/reference promotion) must
// gate on this, not isAdmin alone: the herd must never vet its own knowledge.
func (o Options) isHumanAdmin(req *mcp.CallToolRequest) bool {
	return o.isAdmin(req) && subagentOf(req) == ""
}
```

- [ ] **Step 8: Run to green**

Run: `go test ./internal/brain/ ./internal/auth/ -count=1`
Expected: PASS

- [ ] **Step 9: Commit**

```bash
git add internal/brain/identity.go internal/brain/identity_test.go internal/auth/oidc.go internal/auth/subagent_test.go && git commit -m "feat(auth): the human gate rule — isHumanAdmin refuses delegation tokens, auth.Subagent is its UI twin

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 2: Swap the six gated paths onto `isHumanAdmin`

**Files:**
- Modify: `internal/brain/memory.go` (`add_memory` gate at `:106`, `promote_memory` gate at `:124`)
- Modify: `internal/brain/reference.go` (`promote_reference` gate at `:127`)
- Modify: `internal/brain/learn.go` (`approve_proposal` gate at `:63`, `reject_proposal` gate at `:78`)
- Modify: `internal/ui/ui.go` (`isSuperuser` at `:139-141`)
- Modify: `internal/brain/memory_test.go` (new admin-gate coverage — none exists today for `add_memory`/`promote_memory`)
- Modify: `internal/ui/proposals_test.go` (new subagent-gate coverage for the UI endpoints)

**Interfaces:**
- Consumes: `(o Options) isHumanAdmin(req) bool` and `auth.Subagent(ctx) bool` (Task 1).
- Produces: nothing new — this task only changes which gate function the six sites call.

- [ ] **Step 1: Write the failing wire test for `add_memory`/`promote_memory`**

Append to `internal/brain/memory_test.go`:

```go
// TestAddMemorySharedAndPromoteMemoryRequireAdmin proves shared=true writes
// and promote_memory are admin-gated: an unauthenticated caller against a
// Principals store with a real superuser seeded is refused; the same calls
// against a dev-mode server (Principals nil) succeed.
func TestAddMemorySharedAndPromoteMemoryRequireAdmin(t *testing.T) {
	dir := t.TempDir()
	cstore, err := coord.Open(filepath.Join(dir, "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cstore.Close() })
	mstore, err := memory.Open(filepath.Join(dir, "m.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mstore.Close() })
	pstore, err := principals.Open(filepath.Join(dir, "p.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pstore.Close() })
	if err := pstore.CreateSuperuser("real-admin@example.com", "test"); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	// --- non-admin server (Principals seeded, unauthenticated caller) ---
	clientT1, serverT1 := mcp.NewInMemoryTransports()
	go func() {
		_ = NewServer(cstore, mstore, Options{Principals: pstore}).Run(ctx, serverT1)
	}()
	client1 := mcp.NewClient(&mcp.Implementation{Name: "t1", Version: "0"}, nil)
	sess1, err := client1.Connect(ctx, clientT1, nil)
	if err != nil {
		t.Fatalf("connect non-admin: %v", err)
	}
	defer sess1.Close()

	res, err := sess1.CallTool(ctx, &mcp.CallToolParams{Name: "add_memory", Arguments: map[string]any{
		"name": "team-note", "body": "shared fact", "shared": true,
	}})
	if err != nil {
		t.Fatalf("add_memory shared non-admin call: %v", err)
	}
	if !res.IsError {
		t.Fatal("want tool error for non-admin add_memory(shared=true), got success")
	}

	res2, err := sess1.CallTool(ctx, &mcp.CallToolParams{Name: "promote_memory", Arguments: map[string]any{
		"name": "team-note", "shared": true,
	}})
	if err != nil {
		t.Fatalf("promote_memory non-admin call: %v", err)
	}
	if !res2.IsError {
		t.Fatal("want tool error for non-admin promote_memory, got success")
	}

	// --- admin server (Principals nil => unauthenticated = admin, dev mode) ---
	clientT2, serverT2 := mcp.NewInMemoryTransports()
	go func() {
		_ = NewServer(cstore, mstore, Options{}).Run(ctx, serverT2)
	}()
	client2 := mcp.NewClient(&mcp.Implementation{Name: "t2", Version: "0"}, nil)
	sess2, err := client2.Connect(ctx, clientT2, nil)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer sess2.Close()

	var added addOut
	callTask(t, sess2, "add_memory", map[string]any{
		"name": "team-note", "body": "shared fact", "shared": true,
	}, &added)
	if added.Slug == "" {
		t.Fatalf("add_memory(shared=true) by admin failed: %+v", added)
	}
	var promoted okMsg
	callTask(t, sess2, "promote_memory", map[string]any{"name": "team-note", "shared": false}, &promoted)
	if !promoted.OK {
		t.Fatalf("promote_memory by admin failed: %+v", promoted)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/brain/ -run TestAddMemorySharedAndPromoteMemoryRequireAdmin -count=1`
Expected: PASS already (isAdmin already gates these) — this step is a baseline confirmation, not a red step. Note it and continue; the real red/green cycle for this task is Step 3→5 below (the UI subagent test).

- [ ] **Step 3: Swap the five brain-side gates**

In `internal/brain/memory.go`, `add_memory` handler:

```go
			if in.Shared {
				if !opts.isHumanAdmin(req) {
					return nil, addOut{}, errAdminOnly
				}
			} else if !opts.isMemoryOwner(req) {
```

`promote_memory` handler:

```go
			if !opts.isHumanAdmin(req) {
				return nil, okMsg{}, errAdminOnly
			}
```

In `internal/brain/reference.go`, `promote_reference` handler:

```go
			if !opts.isHumanAdmin(req) {
				return nil, okMsg{}, errAdminOnly
			}
```

In `internal/brain/learn.go`, `approve_proposal` handler:

```go
			if !opts.isHumanAdmin(req) {
				return nil, approveProposalOut{}, fmt.Errorf("forbidden: superuser only (approval shapes fleet-wide behavior)")
			}
```

`reject_proposal` handler:

```go
			if !opts.isHumanAdmin(req) {
				return nil, okOut{}, fmt.Errorf("forbidden: superuser only")
			}
```

- [ ] **Step 4: Run existing suite to green**

Run: `go test ./internal/brain/ -count=1`
Expected: PASS (Task 1's unit test plus the existing `TestApproveProposalRequiresSuperuser`, `TestPromoteReferenceAdminGate`, and Step 1's new test all pass — the swap is behavior-preserving for every existing scenario, since `isHumanAdmin` = `isAdmin` whenever there's no subagent claim).

- [ ] **Step 5: Write the failing UI subagent-gate test**

Append to `internal/ui/proposals_test.go`:

```go
// TestProposalApproveRejectRefuseSubagentToken proves the UI endpoints refuse
// a delegation token even when its rolled-up principal is a real superuser —
// the UI twin of TestIsHumanAdminRefusesDelegationToken (internal/brain).
// fakeBearerVerify treats the bearer as an opaque key; a token prefixed
// "subagent:" is verified into a TokenInfo carrying Extra["subagent"] (a
// human token has no such prefix and no such claim), mirroring the shape a
// real delegation token's Extra carries without needing a live Verifier.
func TestProposalApproveRejectRefuseSubagentToken(t *testing.T) {
	dir := t.TempDir()
	pstore, err := principals.Open(filepath.Join(dir, "p.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pstore.Close() })
	if err := pstore.CreateSuperuser("boss@x.com", "test"); err != nil {
		t.Fatal(err)
	}
	lstore, err := learn.Open(filepath.Join(dir, "l.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lstore.Close() })
	p, _, err := lstore.Upsert("missing-req|go.mod", "finding", "builder", []string{"a"})
	if err != nil {
		t.Fatal(err)
	}

	promoteCalled := false
	srv := Handler(Deps{
		Roles: pstore,
		Learn: lstore,
		Promote: func(id int64, actor string) error {
			promoteCalled = true
			return nil
		},
		Reject: func(id int64, reason string) error { return nil },
	})

	subagentVerify := func(_ context.Context, token string, _ *http.Request) (*sdkauth.TokenInfo, error) {
		if token == "" {
			return nil, sdkauth.ErrInvalidToken
		}
		if strings.HasPrefix(token, "subagent:") {
			principal := strings.TrimPrefix(token, "subagent:")
			return &sdkauth.TokenInfo{
				Expiration: time.Now().Add(time.Hour),
				UserID:     principal,
				Extra:      map[string]any{"subagent": principal + "/child"},
			}, nil
		}
		return &sdkauth.TokenInfo{Expiration: time.Now().Add(time.Hour), UserID: token}, nil
	}
	wrap := func(h http.Handler, bearer string) http.Handler {
		wrapped := sdkauth.RequireBearerToken(subagentVerify, nil)(h)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+bearer)
			wrapped.ServeHTTP(w, r)
		})
	}

	body, _ := json.Marshal(map[string]any{"id": p.ID})
	req := httptest.NewRequest(http.MethodPost, "/api/proposal/approve", bytes.NewReader(body))
	w := httptest.NewRecorder()
	wrap(srv, "subagent:boss@x.com").ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", w.Code, w.Body.String())
	}
	if promoteCalled {
		t.Fatal("Promote must not be called for a delegation-shaped token")
	}
	got, err := lstore.ByID(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != learn.StatusPending {
		t.Fatalf("proposal status = %q, want pending", got.Status)
	}
}
```

Add `"context"`, `"strings"`, `"time"` to the file's import block if not already present (`"context"` and `"time"` are already imported by `fakeBearerVerify`; add `"strings"`).

- [ ] **Step 6: Run to verify failure**

Run: `go test ./internal/ui/ -run TestProposalApproveRejectRefuseSubagentToken -count=1`
Expected: FAIL (the 403 doesn't happen yet — `isSuperuser` doesn't check `auth.Subagent`; also this file won't compile yet because Task 4 hasn't landed the `Promote` signature change — for THIS task only, temporarily write the closure as `Promote: func(id int64) error { promoteCalled = true; return nil }` and drop `actor string` from the signature above; Task 4 will change it to `func(id int64, actor string) error` and this test's closure along with it.)

- [ ] **Step 7: Swap the UI gate**

In `internal/ui/ui.go`, `isSuperuser`:

```go
func (s *Server) isSuperuser(r *http.Request) bool {
	return (s.roles == nil || s.roles.IsSuperuser(auth.Principal(r.Context()))) && !auth.Subagent(r.Context())
}
```

- [ ] **Step 8: Run to green**

Run: `go test ./internal/ui/ ./internal/brain/ -count=1`
Expected: PASS

- [ ] **Step 9: Commit**

```bash
git add internal/brain/memory.go internal/brain/reference.go internal/brain/learn.go internal/brain/memory_test.go internal/ui/ui.go internal/ui/proposals_test.go && git commit -m "feat: swap the six admin gates onto the human-gate rule (isHumanAdmin / auth.Subagent)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 3: Dev-mode worker-session marking

**Files:**
- Create: `internal/brain/worker_sessions.go`
- Create: `internal/brain/worker_sessions_test.go`
- Modify: `internal/brain/identity.go` (add `Options.WorkerSessions` field; extend `isHumanAdmin`)
- Modify: `internal/brain/server.go` (mark the session in the `bootstrap` handler, `:96-108`)
- Modify: `internal/brain/host.go` (mark the session in the `report_host` handler, `:169-179`)
- Modify: `cmd/corral/main.go` (construct `WorkerSessions` and wire it into `Options`)

**Interfaces:**
- Consumes: `req.Session *mcp.ServerSession` (go-sdk v1.6.1: `ServerSession.InitializeParams() *InitializeParams`, `InitializeParams.ClientInfo *Implementation`, `Implementation.Name string`, `ServerSession.ID() string`).
- Produces: `func NewWorkerSessions() *WorkerSessions`; `(*WorkerSessions).Mark(req *mcp.CallToolRequest)`; `(*WorkerSessions).Is(req *mcp.CallToolRequest) bool` — both nil-receiver-safe.

- [ ] **Step 1: Write the failing wire test**

Create `internal/brain/worker_sessions_test.go`:

```go
// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/coord"
	"github.com/pdbethke/corralai/internal/learn"
)

// TestDevModeWorkerSessionRefusedAtHumanGate is the dev-mode half of the
// human gate, end to end over real streamable-HTTP (so each connecting
// client is a genuinely distinct MCP session — required to prove per-session
// marking and that marks don't leak): a session that names itself
// "corral-agent" at the handshake, or that calls bootstrap before trying to
// approve, is refused; a fresh corral-admin-shaped session that never
// bootstraps passes; and the mark on one session never leaks to another.
func TestDevModeWorkerSessionRefusedAtHumanGate(t *testing.T) {
	dir := t.TempDir()
	cstore, err := coord.Open(filepath.Join(dir, "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cstore.Close() })
	lstore, err := learn.Open(filepath.Join(dir, "l.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lstore.Close() })

	ws := NewWorkerSessions()
	srv := NewServer(cstore, nil, Options{Learn: lstore, WorkerSessions: ws})
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{DisableLocalhostProtection: true},
	)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	ctx := context.Background()

	connect := func(name string) *mcp.ClientSession {
		t.Helper()
		cl := mcp.NewClient(&mcp.Implementation{Name: name, Version: "0"}, nil)
		sess, err := cl.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL}, nil)
		if err != nil {
			t.Fatalf("connect %s: %v", name, err)
		}
		t.Cleanup(func() { sess.Close() })
		return sess
	}
	seed := func(sig string) int64 {
		t.Helper()
		p, _, err := lstore.Upsert(sig, "finding", "builder", []string{"a"})
		if err != nil {
			t.Fatal(err)
		}
		return p.ID
	}

	// Signal 1: ClientInfo.Name == "corral-agent" — refused immediately, no
	// bootstrap call needed.
	agentID := seed("missing-req|go.mod")
	agentSess := connect("corral-agent")
	res, err := agentSess.CallTool(ctx, &mcp.CallToolParams{Name: "approve_proposal", Arguments: map[string]any{"id": agentID}})
	if err != nil {
		t.Fatalf("approve_proposal (ClientInfo signal) call: %v", err)
	}
	if !res.IsError {
		t.Fatal("a corral-agent-named session must be refused at the human gate")
	}

	// Signal 2: behavior — a neutrally-named session that calls bootstrap
	// first is marked a worker, mirroring every shipped corral-agent (whose
	// first call is always bootstrap).
	behaviorID := seed("missing-req|package.json")
	behaviorSess := connect("neutral-client")
	if _, err := behaviorSess.CallTool(ctx, &mcp.CallToolParams{Name: "bootstrap", Arguments: map[string]any{"name": "worker1"}}); err != nil {
		t.Fatalf("bootstrap call: %v", err)
	}
	res2, err := behaviorSess.CallTool(ctx, &mcp.CallToolParams{Name: "approve_proposal", Arguments: map[string]any{"id": behaviorID}})
	if err != nil {
		t.Fatalf("approve_proposal (behavioral signal) call: %v", err)
	}
	if !res2.IsError {
		t.Fatal("a session that called bootstrap must be refused at the human gate")
	}

	// A fresh corral-admin-shaped session that never bootstraps passes.
	adminID := seed("missing-req|Cargo.toml")
	adminSess := connect("corral-admin")
	var ap approveProposalOut
	callTask(t, adminSess, "approve_proposal", map[string]any{"id": adminID}, &ap)
	if !ap.OK {
		t.Fatalf("a corral-admin-shaped session must pass the human gate: %+v", ap)
	}

	// No leakage: a second fresh, unmarked session also passes even though
	// behaviorSess (same test process) was marked.
	freshID := seed("missing-req|requirements.txt")
	freshSess := connect("another-neutral-client")
	var ap2 approveProposalOut
	callTask(t, freshSess, "approve_proposal", map[string]any{"id": freshID}, &ap2)
	if !ap2.OK {
		t.Fatalf("a fresh unmarked session must pass: %+v", ap2)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/brain/ -run TestDevModeWorkerSessionRefusedAtHumanGate -count=1`
Expected: FAIL (`NewWorkerSessions` undefined)

- [ ] **Step 3: Implement `WorkerSessions`**

Create `internal/brain/worker_sessions.go`:

```go
// SPDX-License-Identifier: Elastic-2.0

package brain

import "sync"

// WorkerSessions tracks, per MCP session, whether the session has identified
// itself as a corral-agent worker — either by ClientInfo.Name at the MCP
// handshake, or by calling bootstrap/report_host (the two calls every
// shipped corral-agent makes and corral-admin/the UI never do).
//
// This is a TRUTHFULNESS GUARDRAIL, not a security boundary: dev mode has no
// cryptographic identity at all (anyone on the port is anonymous), so a
// hostile caller could simply not announce itself and this mark would never
// be set. It exists so a worker acting HONESTLY — as every shipped
// corral-agent does — cannot accidentally pass the human gate in dev mode,
// matching the rule isHumanAdmin already enforces when auth is on.
type WorkerSessions struct {
	mu  sync.Mutex
	ids map[string]bool
}

// NewWorkerSessions returns an empty tracker.
func NewWorkerSessions() *WorkerSessions {
	return &WorkerSessions{ids: map[string]bool{}}
}

// Mark records req's MCP session as a worker session. Nil-safe: a nil
// receiver, nil req, or nil req.Session is a no-op — dev-mode marking is
// opportunistic instrumentation on the bootstrap/report_host handlers, never
// a hard dependency for those tools to function.
func (w *WorkerSessions) Mark(req *mcp.CallToolRequest) {
	if w == nil || req == nil || req.Session == nil {
		return
	}
	if id := req.Session.ID(); id != "" {
		w.mu.Lock()
		w.ids[id] = true
		w.mu.Unlock()
	}
}

// Is reports whether req's session is a worker: either it named itself
// "corral-agent" at the MCP handshake (checked live, needs no prior Mark
// call), or an earlier call in the same session marked it. The ClientInfo
// check works even with a nil receiver (Options.WorkerSessions unset);
// only the behavioral (marked) signal requires a non-nil tracker.
func (w *WorkerSessions) Is(req *mcp.CallToolRequest) bool {
	if req == nil || req.Session == nil {
		return false
	}
	if ip := req.Session.InitializeParams(); ip != nil && ip.ClientInfo != nil && ip.ClientInfo.Name == "corral-agent" {
		return true
	}
	if w == nil {
		return false
	}
	id := req.Session.ID()
	if id == "" {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ids[id]
}
```

Add the `mcp` import: `"github.com/modelcontextprotocol/go-sdk/mcp"`.

- [ ] **Step 4: Wire the field and extend `isHumanAdmin`**

In `internal/brain/identity.go`, add to `Options` (near the other tool-enabling fields, e.g. after `HostBook`):

```go
	// WorkerSessions tracks, per MCP session, whether the session has
	// identified itself as a corral-agent worker (ClientInfo.Name, or an
	// earlier bootstrap/report_host call) — the dev-mode half of the human
	// gate (see isHumanAdmin). nil => the dev-mode worker-session check is
	// skipped for the marked-by-behavior signal; the live ClientInfo check
	// still runs (WorkerSessions.Is is nil-receiver-safe).
	WorkerSessions *WorkerSessions
```

Replace `isHumanAdmin`'s body:

```go
func (o Options) isHumanAdmin(req *mcp.CallToolRequest) bool {
	if !o.isAdmin(req) || subagentOf(req) != "" {
		return false
	}
	return !o.WorkerSessions.Is(req)
}
```

- [ ] **Step 5: Mark the two behavioral signals**

In `internal/brain/server.go`, `bootstrap` handler, add the mark as the first line of the closure body:

```go
		func(_ context.Context, req *mcp.CallToolRequest, in bootstrapIn) (*mcp.CallToolResult, coord.Bootstrap, error) {
			opts.WorkerSessions.Mark(req)
			name := identity(req, in.Name)
```

(`NewServer(store *coord.Store, mem *memory.Store, opts Options) *mcp.Server` already has `opts` in scope here.)

In `internal/brain/host.go`, `report_host` handler, add the mark as the first line of the closure body:

```go
	}, func(_ context.Context, req *mcp.CallToolRequest, in reportHostIn) (*mcp.CallToolResult, okOut, error) {
		opts.WorkerSessions.Mark(req)
		book.Set(Host{
```

(`registerHost(s *mcp.Server, book *HostBook, opts Options)` already has `opts` in scope here.)

- [ ] **Step 6: Wire `WorkerSessions` in `cmd/corral/main.go`**

Near the other ring/book constructions (`execRing := brain.NewExecRing()` etc., `main.go:763-786`), add:

```go
	workerSessions := brain.NewWorkerSessions()
```

and add `WorkerSessions: workerSessions,` to the `brain.Options{...}` literal passed to `brain.NewServer`.

- [ ] **Step 7: Run to green**

Run: `go build ./... && go test ./internal/brain/ -count=1`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add internal/brain/worker_sessions.go internal/brain/worker_sessions_test.go internal/brain/identity.go internal/brain/server.go internal/brain/host.go cmd/corral/main.go && git commit -m "feat(brain): dev-mode worker-session marking — the human gate's truthfulness guardrail when auth is off

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 4: UI actor passthrough — stop hardcoding `"operator"`

**Files:**
- Modify: `internal/ui/ui.go` (`Deps.Promote` type, `proposalApprove` handler, `:99-107` and `:148-181`)
- Modify: `cmd/corral/main.go` (`proposalPromote` closure, `:946-949`)
- Modify: `internal/ui/proposals_test.go` (update the `Promote` closure signature — including the Task 2 Step 5 test — and add the actor-passthrough test)
- Modify: `internal/ui/ui_test.go` (update the `promote` closure signature at `:470`)

**Interfaces:**
- Consumes: `auth.Principal(ctx) string` (existing, `internal/auth/oidc.go:250-255`).
- Produces: `Deps.Promote func(id int64, actor string) error` — the new signature every caller must match.

- [ ] **Step 1: Write the failing tests**

Append to `internal/ui/proposals_test.go`:

```go
// TestProposalApprovePassesVerifiedPrincipalAsActor proves the UI's approve
// endpoint stops hardcoding actor "operator" when auth is on: it passes the
// real verified principal through to Promote.
func TestProposalApprovePassesVerifiedPrincipalAsActor(t *testing.T) {
	dir := t.TempDir()
	pstore, err := principals.Open(filepath.Join(dir, "p.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pstore.Close() })
	if err := pstore.CreateSuperuser("real-admin@example.com", "test"); err != nil {
		t.Fatal(err)
	}
	lstore, err := learn.Open(filepath.Join(dir, "l.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lstore.Close() })
	p, _, err := lstore.Upsert("missing-req|go.mod", "finding", "builder", []string{"a"})
	if err != nil {
		t.Fatal(err)
	}

	var gotActor string
	srv := Handler(Deps{
		Roles: pstore,
		Learn: lstore,
		Promote: func(id int64, actor string) error {
			gotActor = actor
			return nil
		},
		Reject: func(id int64, reason string) error { return nil },
	})

	body, _ := json.Marshal(map[string]any{"id": p.ID})
	req := httptest.NewRequest(http.MethodPost, "/api/proposal/approve", bytes.NewReader(body))
	w := httptest.NewRecorder()
	bearerWrap(srv, "real-admin@example.com").ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if gotActor != "real-admin@example.com" {
		t.Fatalf("actor = %q, want the verified principal", gotActor)
	}
}

// TestProposalApproveDevModeActorFallsBackToOperator proves the "operator"
// fallback survives for dev mode (no bearer at all).
func TestProposalApproveDevModeActorFallsBackToOperator(t *testing.T) {
	dir := t.TempDir()
	lstore, err := learn.Open(filepath.Join(dir, "l.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lstore.Close() })
	p, _, err := lstore.Upsert("missing-req|go.mod", "finding", "builder", []string{"a"})
	if err != nil {
		t.Fatal(err)
	}

	var gotActor string
	srv := Handler(Deps{
		// Roles nil => dev mode: isSuperuser is open, no bearer required.
		Learn: lstore,
		Promote: func(id int64, actor string) error {
			gotActor = actor
			return nil
		},
		Reject: func(id int64, reason string) error { return nil },
	})

	body, _ := json.Marshal(map[string]any{"id": p.ID})
	req := httptest.NewRequest(http.MethodPost, "/api/proposal/approve", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req) // no bearer — dev mode
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if gotActor != "operator" {
		t.Fatalf("actor = %q, want operator fallback in dev mode", gotActor)
	}
}
```

Update the existing `TestProposalApproveRejectRequireSuperuser`'s and (from Task 2 Step 5) `TestProposalApproveRejectRefuseSubagentToken`'s `Promote` closures to the new signature:

```go
		Promote: func(id int64, actor string) error {
			promoteCalled = true
			return nil
		},
```

Update `internal/ui/ui_test.go:470`:

```go
	promote := func(id int64, actor string) error {
		_, err := brain.ApproveProposal(ls, mstore, astore, nil, id, actor, false, false)
		return err
	}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/ui/ -count=1`
Expected: FAIL (compile error — `Deps.Promote` is still `func(id int64) error`, doesn't match the two-arg closures)

- [ ] **Step 3: Change `Deps.Promote`'s type and the approve handler**

In `internal/ui/ui.go`, change the `Deps` field (`:99-103`) and the matching `Server` field (`:54`):

```go
	// Promote fans a proposal's guidance/skill out into standing memory + the
	// fleet artifact store (the same fan-out the approve_proposal MCP tool
	// runs — see brain.ApproveProposal). actor is the verified principal
	// (auth on) or "operator" (dev-mode fallback — see proposalApprove).
	// Wired in cmd/corral/main.go. nil => the approve endpoint returns 404.
	Promote func(id int64, actor string) error
```

```go
	promote    func(id int64, actor string) error
```

Update the `Handler` constructor line assigning `promote: d.Promote` — unchanged text, only the type follows through.

In `proposalApprove` (`:148-181`), compute the actor and pass it:

```go
func (s *Server) proposalApprove(w http.ResponseWriter, r *http.Request) {
	if auth.ReadOnly(r) {
		http.Error(w, "forbidden: read-only observer token cannot act", http.StatusForbidden)
		return
	}
	if !s.isSuperuser(r) {
		http.Error(w, "forbidden: superuser only (approval shapes fleet-wide behavior)", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if s.promote == nil {
		http.Error(w, "proposals unavailable", http.StatusNotFound)
		return
	}
	var body struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if body.ID == 0 {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	actor := auth.Principal(r.Context())
	if actor == "" {
		actor = "operator"
	}
	if err := s.promote(body.ID, actor); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}
```

- [ ] **Step 4: Update the caller in `cmd/corral/main.go`**

Replace (`:946-949`):

```go
	proposalPromote := func(id int64) error {
		_, err := brain.ApproveProposal(learnStore, memStore, artStore, telStore, id, "operator", false, false)
		return err
	}
```

with:

```go
	proposalPromote := func(id int64, actor string) error {
		_, err := brain.ApproveProposal(learnStore, memStore, artStore, telStore, id, actor, false, false)
		return err
	}
```

- [ ] **Step 5: Run to green**

Run: `go build ./... && go test ./internal/ui/ ./internal/brain/ -count=1`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/ui/ui.go internal/ui/proposals_test.go internal/ui/ui_test.go cmd/corral/main.go && git commit -m "fix(ui): stop hardcoding actor \"operator\" — the browser approve button stamps the real verified principal when auth is on

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 5: Docs — README trust story + DESIGN.md roadmap

**Files:**
- Modify: `README.md` (the "Auth from day 0" bullet list)
- Modify: `docs/DESIGN.md` (roadmap, sibling entry to P8)

**Steps:**

- [ ] **Step 1: README** — in the "Auth from day 0" list, insert a new bullet immediately after the existing "Signed delegation tokens" bullet and before "Read-only observer tokens" (anchor text for the insertion point, currently `README.md:177-183`):

```markdown
  - **Signed delegation tokens** — an agent can spawn an out-of-process subagent
    with a scoped, TTL-bound token minted by the brain: the subagent acts under
    its own identity, accountability rolls up to the spawning principal, and the
    token dies on schedule (depth- and fan-out-capped).
  - **The human gate** — every admin write (approving/rejecting a learning-loop
    proposal, sharing memory, promoting a reference or memory entry) refuses a
    delegation token even when it rolls up to a superuser: workers propose, the
    operator disposes. In dev mode (no OIDC configured) the same rule holds by
    convention — a session that identifies itself as a worker (`corral-agent`,
    or its first `bootstrap`/`report_host` call) is refused at the same gates,
    so an agent can't accidentally vet its own knowledge just because dev mode
    has no cryptographic identity to check.
  - **Read-only observer tokens** — minted for dashboards and demo audiences:
    the holder can watch the live swarm but every mutating call is refused.
    Hand it to an ops screen without handing over the swarm.
```

- [ ] **Step 2: DESIGN.md roadmap** — insert a new `P9` entry immediately after the P8 paragraph and before `### Open threads (next)` (anchor text, currently `docs/DESIGN.md:146-178`):

```markdown
- **P9 — the human gate (DONE 2026-07-03).** Closes a gap P8 opened: a
  delegation token still rolls `UserID` up to its principal, so an agent
  spawned under a superuser could `approve_proposal` on itself, and dev
  mode's open-until-first-superuser default meant every unauthenticated
  caller — including the herd's own agents — passed the admin gate too. One
  rule now guards all six admin writes (`approve_proposal`, `reject_proposal`,
  `add_memory(shared=true)`, `promote_memory`, `promote_reference`, and the
  UI's `/api/proposal/approve|reject`): `isHumanAdmin` = `isAdmin` AND no
  `subagent` claim on the token (`internal/brain/identity.go`); the UI gets
  the same rule via `auth.Subagent(ctx)` beside the existing `auth.ReadOnly`.
  Dev mode has no cryptographic identity to check, so it's a **truthfulness
  guardrail, not a security boundary**: a session that names itself
  `corral-agent` at the MCP handshake, or that calls `bootstrap`/
  `report_host` (every shipped worker does; `corral-admin` never does), is
  marked a worker for the life of that session and refused at the same six
  gates — "the human gate: workers propose, the operator disposes." Accepted
  limitation: in-process subagents share their parent's session/token, so
  they're indistinguishable from the parent — the boundary is per-session,
  and out-of-process delegation is the spawn mode that matters for autonomous
  workers. `cmd/corral/main.go`'s UI approve closure also stopped hardcoding
  actor `"operator"` — it passes the real verified principal when auth is on,
  falling back to `"operator"` only in dev.
```

- [ ] **Step 3: Full suite + security gate**

Run: `go test ./... -count=1 && bash scripts/check-security.sh`
Expected: both green.

- [ ] **Step 4: Commit**

```bash
git add README.md docs/DESIGN.md && git commit -m "docs: the human gate — README trust story + DESIGN.md P9 roadmap entry

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

## Self-review notes (performed at write time)

- **Spec coverage:** auth-on delegation refusal (Task 1 + Task 2), the six named gated paths verbatim (Task 2), dev-mode worker marking with both named signals (Task 3), the accepted in-process-subagent limitation documented in code (Task 3's `WorkerSessions` doc comment) and in DESIGN.md (Task 5), the drive-by actor-passthrough fix (Task 4), the corral-voice refusal framing (README/DESIGN.md copy in Task 5; the MCP tool error strings already read "forbidden: superuser only" from the pre-existing code — the spec's exact phrase "the human gate: workers propose, the operator disposes" is used in the doc copy and the wire test's intent comments, matching how the existing codebase favors comments over runtime string matching for this kind of internal detail). Testing section: auth-on delegation-token refusal for a superuser principal (Task 1, unit-level with a REAL minted token) and human-token pass (Task 1); dev-mode bootstrap-session refusal + fresh-session pass + no-leakage (Task 3, full wire); learning-loop wire tests kept green throughout (explicit re-run in every task).
- **Placeholders:** none — every step shows complete, compilable code including full import blocks where new imports are needed.
- **Type consistency:** `isHumanAdmin` is a method on `Options` (matching `isAdmin`'s receiver) introduced once in Task 1 and only extended (never renamed) in Task 3; `auth.Subagent(ctx context.Context) bool` matches its one call site (`internal/ui/ui.go`'s `isSuperuser`, called as `auth.Subagent(r.Context())`); `Deps.Promote func(id int64, actor string) error` is updated consistently across `internal/ui/ui.go`, `cmd/corral/main.go`, `internal/ui/proposals_test.go` (three closures: the pre-existing superuser test, Task 2's new subagent test, Task 4's two new tests), and `internal/ui/ui_test.go`.
- **Ambiguity resolved (ground-truth check before writing):** the shape guidance asked for "wire tests incl. delegation-token negative" for Task 2. Verified against the go-sdk (v1.6.1) that `req.Extra.TokenInfo` is populated ONLY by the HTTP-layer `sdkauth.RequireBearerToken` middleware reading `auth.TokenInfoFromContext(req.Context())` (`mcp/streamable.go:1148`) — `mcp.NewInMemoryTransports()`, the transport every existing wire test in this repo uses, never runs that middleware, so a delegation claim cannot ride through it. Resolution: the delegation-token negative proof is a **unit test** in Task 1 (`TestIsHumanAdminRefusesDelegationToken`) that mints and verifies a REAL token via `internal/auth.Verifier` (not a hand-built stub) and calls `isHumanAdmin` directly — mirroring this package's existing low-level `reqWith`-based testing convention (`identity_test.go`) exactly. Task 2's wire tests then cover only what in-memory transport CAN prove (non-admin refusal, admin pass) for the three previously-untested paths (`add_memory` shared, `promote_memory`), plus a UI-level subagent test (Task 2 Step 5) that goes through a real `sdkauth.RequireBearerToken` HTTP middleware with a token shaped like a delegation claim — this one CAN carry the claim because the UI's endpoints are plain `http.Handler`s tested via `httptest`, not MCP transport.

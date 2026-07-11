<!-- SPDX-License-Identifier: Elastic-2.0 -->
# The Repo Gate (control-plane v1) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make a GitHub PR unmergeable until the brain has run the repo's declared check on the exact head commit — in the bwrap jail — and posted a tamper-evident signed `corral/gate` status.

**Architecture:** A new `internal/gate` package polls covered repos' open PRs (reusing the existing poll model), runs each new head SHA's check through the **bwrap jail** (untrusted PR code), signs the result via the **existing certify path** (extracted from `report_build`), stores a thin dedupe/index row, and posts `corral/gate = pending|success|failure` to the head SHA via two new `repo.Provider` methods. GitHub branch protection (org-side, one-time) requires the `corral/gate` context — that is what blocks merge (Model A). No new execution or signing path is introduced.

**Tech Stack:** Go 1.26.5; `internal/repo` (forge REST + git), `internal/sandbox` (bwrap `Run`), `internal/certify` + `internal/brain` (DSSE signing), `internal/buildstore` pattern (DuckDB via `go-duckdb`).

## Global Constraints
- SPDX header `// SPDX-License-Identifier: Elastic-2.0` on every new file.
- **TDD**: failing test first, watch it fail, minimal code, watch it pass.
- Per commit: `go vet ./...` + `go test ./...` + `go build ./...` + `bash scripts/check-security.sh` all green.
- **Fail-closed**: `success` is posted ONLY from a real exit-0 of an actual jail execution. Any internal error (checkout, jail, timeout, signing) → `failure`/`error`, NEVER `success`.
- **No self-report**: the exit code comes from the brain running the check itself, never from a claim.
- Credential boundary preserved: the forge token stays brain-side (per-forge isolation); never to a worker, never logged, never in a stored URL.
- The untrusted PR check runs in the **bwrap jail** (`sandbox.Run` with a Backend), network only if the policy opts in.
- Corral metaphor (no new bee/hive/swarm identifiers). DRY: reuse `report_build`'s signing via an extracted function; do not re-implement DSSE.
- v1 is **GitHub-only + Model A**. `gitea`/`gitlab` Provider methods return `errors.ErrUnsupported` (honest, never a silent no-op).

---

## File Structure
- `internal/repo/provider.go` — add `PRRef`, two interface methods, two `rc*` helpers. (modify)
- `internal/repo/provider_github.go` / `provider_gitea.go` / `provider_gitlab.go` — delegate (GitHub) / `ErrUnsupported` (others). (modify)
- `internal/repo/repo.go` — add Engine wrappers `ListOpenPRs`/`SetCommitStatus` (repoURL→provider) and `CheckoutPR`. (modify)
- `internal/brain/buildcert.go` — extract `certifyBuild(...)` from the `report_build` handler. (modify)
- `internal/gate/types.go` — `Policy`, `Run`. (new)
- `internal/gate/store.go` — DuckDB `gate_runs` dedupe/index store. (new)
- `internal/gate/runner.go` — the gate runner + its injected dep interfaces. (new)
- `internal/gate/poller.go` — poll open PRs → dedupe → run. (new)
- `internal/brain/gate.go` — wire config → poller (adapters to certify/jail/repo), + `GET /api/gate/run`. (new)
- `cmd/corral/main.go` — load gate policies from env, pass to brain Options. (modify)

---

## Task 1: Forge methods — list open PRs + post a commit status (GitHub)

**Files:**
- Modify: `internal/repo/provider.go`, `internal/repo/provider_github.go`, `internal/repo/provider_gitea.go`, `internal/repo/provider_gitlab.go`, `internal/repo/repo.go`
- Test: `internal/repo/provider_github_test.go`, `internal/repo/repo_test.go`

**Interfaces:**
- Produces: `type PRRef struct { Number int; HeadSHA string; HeadRef string; Base string }`; `Provider.ListOpenPRs(ctx, owner, repo, base string) ([]PRRef, error)`; `Provider.SetCommitStatus(ctx, owner, repo, sha, context, state, targetURL, description string) error`; `Engine.ListOpenPRs(ctx, repoURL, base string) ([]PRRef, error)`; `Engine.SetCommitStatus(ctx, repoURL, sha, context, state, targetURL, description string) error`.
- Consumes: existing `restClient` helpers (`get`, `doPost`, `redact`) and the `Engine`'s existing repoURL→`providerFor(host)` wrapper pattern (mirror `Engine.OpenPR`).

- [ ] **Step 1: Failing test — GitHub `SetCommitStatus` posts the right payload.** In `provider_github_test.go`, follow the file's existing fake-`httptest`-server pattern. Assert a `POST /repos/o/r/statuses/deadbeef` with body `{"state":"success","context":"corral/gate","target_url":"http://x","description":"passed"}`.
```go
func TestGithubSetCommitStatus(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.Method + " " + r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(201)
	}))
	defer srv.Close()
	p := &githubProvider{rc: restClient{base: srv.URL, accept: "application/vnd.github+json"}}
	if err := p.SetCommitStatus(context.Background(), "o", "r", "deadbeef", "corral/gate", "success", "http://x", "passed"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "POST /repos/o/r/statuses/deadbeef" {
		t.Errorf("path = %q", gotPath)
	}
	for _, want := range []string{`"state":"success"`, `"context":"corral/gate"`, `"target_url":"http://x"`, `"description":"passed"`} {
		if !strings.Contains(gotBody, want) {
			t.Errorf("body %q missing %q", gotBody, want)
		}
	}
}
```

- [ ] **Step 2: Run it, watch it fail.** `go test ./internal/repo/ -run TestGithubSetCommitStatus` → FAIL (`SetCommitStatus` undefined).

- [ ] **Step 3: Implement `PRRef`, the interface methods, the `rc*` helpers, GitHub delegation, and `ErrUnsupported` stubs.**
In `provider.go` add to the `Provider` interface (after `GetPR`):
```go
	// ListOpenPRs returns open change requests targeting base (all bases if base == "").
	ListOpenPRs(ctx context.Context, owner, repo, base string) ([]PRRef, error)
	// SetCommitStatus posts a commit status to sha. state ∈ {"pending","success","failure","error"}.
	SetCommitStatus(ctx context.Context, owner, repo, sha, context, state, targetURL, description string) error
```
Add the type + helpers in `provider.go`:
```go
// PRRef identifies an open change request and its current head.
type PRRef struct {
	Number  int
	HeadSHA string
	HeadRef string
	Base    string
}

// rcListOpenPRs lists open PRs via the GitHub REST shape.
func (rc *restClient) rcListOpenPRs(ctx context.Context, owner, repo, base string) ([]PRRef, error) {
	url := rc.base + "/repos/" + owner + "/" + repo + "/pulls?state=open&per_page=100"
	if base != "" {
		url += "&base=" + base
	}
	b, _, err := rc.get(ctx, url, "")
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Number int `json:"number"`
		Head   struct {
			SHA string `json:"sha"`
			Ref string `json:"ref"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	out := make([]PRRef, 0, len(raw))
	for _, r := range raw {
		out = append(out, PRRef{Number: r.Number, HeadSHA: r.Head.SHA, HeadRef: r.Head.Ref, Base: r.Base.Ref})
	}
	return out, nil
}

// rcSetCommitStatus posts a commit status via the GitHub REST shape.
func (rc *restClient) rcSetCommitStatus(ctx context.Context, owner, repo, sha, context, state, targetURL, description string) error {
	payload, _ := json.Marshal(map[string]any{
		"state": state, "context": context, "target_url": targetURL, "description": description,
	})
	url := rc.base + "/repos/" + owner + "/" + repo + "/statuses/" + sha
	b, resp, err := rc.doPost(ctx, url, payload)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("set commit status: %s: %s", resp.Status, rc.redact(string(b)))
	}
	return nil
}
```
In `provider_github.go` add delegating methods:
```go
func (p *githubProvider) ListOpenPRs(ctx context.Context, owner, repo, base string) ([]PRRef, error) {
	return p.rc.rcListOpenPRs(ctx, owner, repo, base)
}
func (p *githubProvider) SetCommitStatus(ctx context.Context, owner, repo, sha, context, state, targetURL, description string) error {
	return p.rc.rcSetCommitStatus(ctx, owner, repo, sha, context, state, targetURL, description)
}
```
In `provider_gitea.go` and `provider_gitlab.go` add honest stubs (import `errors`):
```go
func (p *giteaProvider) ListOpenPRs(ctx context.Context, owner, repo, base string) ([]PRRef, error) {
	return nil, fmt.Errorf("gitea: ListOpenPRs: %w", errors.ErrUnsupported)
}
func (p *giteaProvider) SetCommitStatus(ctx context.Context, owner, repo, sha, context, state, targetURL, description string) error {
	return fmt.Errorf("gitea: SetCommitStatus: %w", errors.ErrUnsupported)
}
```
(same for `gitlabProvider`.)

- [ ] **Step 4: Run it, watch it pass.** `go test ./internal/repo/ -run TestGithubSetCommitStatus` → PASS.

- [ ] **Step 5: Failing test — Engine wrappers resolve repoURL→provider.** In `repo_test.go`, mirror the existing `Engine.OpenPR` test: a `TestEngineListOpenPRs` and `TestEngineSetCommitStatus` that point the engine's github forge at a fake server and assert the call reaches it. (Use the same Engine construction the existing OpenPR test uses.)

- [ ] **Step 6: Run, watch fail; then implement the Engine wrappers in `repo.go`** mirroring `Engine.OpenPR`'s repoURL→host→`providerFor`→delegate pattern:
```go
// ListOpenPRs returns open PRs for repoURL targeting base.
func (e *Engine) ListOpenPRs(ctx context.Context, repoURL, base string) ([]PRRef, error) {
	host, owner, repo, err := parseRepoURL(repoURL) // reuse the same parser Engine.OpenPR uses
	if err != nil {
		return nil, err
	}
	p, err := e.providerFor(host)
	if err != nil {
		return nil, err
	}
	return p.ListOpenPRs(ctx, owner, repo, base)
}

// SetCommitStatus posts a commit status for repoURL@sha.
func (e *Engine) SetCommitStatus(ctx context.Context, repoURL, sha, context, state, targetURL, description string) error {
	host, owner, repo, err := parseRepoURL(repoURL)
	if err != nil {
		return err
	}
	p, err := e.providerFor(host)
	if err != nil {
		return err
	}
	return p.SetCommitStatus(ctx, owner, repo, sha, context, state, targetURL, description)
}
```
(Use whatever the file's existing repoURL parser is named — read `Engine.OpenPR` in `repo.go` and reuse it verbatim; do not invent a second parser.)

- [ ] **Step 7: Run the package.** `go test ./internal/repo/...` → PASS. **Commit:** `feat(repo): list open PRs + post commit status (GitHub); Engine wrappers`.

---

## Task 2: The gate dedupe/index store (`gate_runs`)

**Files:**
- Create: `internal/gate/types.go`, `internal/gate/store.go`
- Test: `internal/gate/store_test.go`

**Interfaces:**
- Produces: `type Policy struct { Repo string; Base []string; Context string; CheckCmd []string; AllowNet bool }`; `type Run struct { Repo, HeadSHA string; PR int; Passed bool; RecordID int64; RanAt time.Time }`; `func OpenStore(dsn string) (*Store, error)`; `(*Store).Save(Run) error`; `(*Store).GetBySHA(repo, sha string) (Run, bool, error)`.
- Consumes: the `go-duckdb` connection pattern from `internal/buildstore/store.go` (`Open(dsn)` — mirror its driver import and table-create-on-open).

- [ ] **Step 1: Failing test — save then dedupe-lookup by (repo, sha).**
```go
func TestGateStoreSaveAndGetBySHA(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "gate.db"))
	if err != nil { t.Fatal(err) }
	if _, ok, _ := s.GetBySHA("o/r", "abc"); ok {
		t.Fatal("expected not found before save")
	}
	if err := s.Save(Run{Repo: "o/r", HeadSHA: "abc", PR: 7, Passed: true, RecordID: 42}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetBySHA("o/r", "abc")
	if err != nil || !ok { t.Fatalf("get: ok=%v err=%v", ok, err) }
	if !got.Passed || got.PR != 7 || got.RecordID != 42 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}
```

- [ ] **Step 2: Run, watch fail** (`go test ./internal/gate/ -run TestGateStore` → package/undefined).

- [ ] **Step 3: Implement `types.go` (the two structs) and `store.go`.** Mirror `buildstore.Open` (same duckdb driver import). Table:
```sql
CREATE TABLE IF NOT EXISTS gate_runs (
  repo VARCHAR NOT NULL, head_sha VARCHAR NOT NULL,
  pr INTEGER NOT NULL, passed BOOLEAN NOT NULL,
  record_id BIGINT NOT NULL, ran_at TIMESTAMP NOT NULL,
  PRIMARY KEY (repo, head_sha)
);
```
`Save` uses `INSERT OR REPLACE`; `GetBySHA` selects `WHERE repo=? AND head_sha=?`, mapping no-rows → `(Run{}, false, nil)`. Set `RanAt` in `Save` if zero — but **do not call `time.Now()` yourself if a deterministic clock is needed**; take `RanAt` from the caller (the runner sets it) so the store stays clock-free and testable.

- [ ] **Step 4: Run, watch pass.** `go test ./internal/gate/...` → PASS. **Commit:** `feat(gate): DuckDB dedupe/index store for gate runs`.

---

## Task 3: Extract `certifyBuild` — the reusable signing seam (DRY)

**Files:**
- Modify: `internal/brain/buildcert.go`
- Test: `internal/brain/buildcert_test.go`

**Interfaces:**
- Produces: `func certifyBuild(ctx context.Context, opts Options, in reportBuildIn, actor string) (reportBuildOut, error)` — the exact body currently inside the `report_build` handler (BuildLedger → BuildAttestation → SignDSSE → CanonicalStatement → MarshalSteps → witness anchor → `BuildStore.Save`).
- Consumes: existing `certify.*`, `opts.CertifyKey`, `opts.BuildStore`, `opts.Witness`, `opts.Telemetry`.

- [ ] **Step 1: Failing test — `certifyBuild` signs and stores directly (no MCP).**
```go
func TestCertifyBuildSignsAndStores(t *testing.T) {
	opts := newTestOptionsWithBuildStore(t) // reuse the existing report_build test's Options builder
	out, err := certifyBuild(context.Background(), opts, reportBuildIn{
		Repo: "o/r", Commit: "abc", Command: "true", ExitCode: 0,
	}, "gate")
	if err != nil { t.Fatal(err) }
	if out.ID == 0 || out.Head == "" || out.Signature == "" || out.PublicKey == "" {
		t.Fatalf("incomplete record: %+v", out)
	}
}
```
(If no `newTestOptionsWithBuildStore` helper exists, read `buildcert_test.go` and reuse however the existing `report_build` test constructs `Options` with a `BuildStore`.)

- [ ] **Step 2: Run, watch fail** (`certifyBuild` undefined).

- [ ] **Step 3: Extract.** Move the handler body into `certifyBuild`, returning `reportBuildOut`. The tool closure becomes:
```go
func(ctx context.Context, req *mcp.CallToolRequest, in reportBuildIn) (*mcp.CallToolResult, reportBuildOut, error) {
	out, err := certifyBuild(ctx, opts, in, actorOf(req))
	return nil, out, err
}
```
Keep behavior byte-identical (the existing `report_build` tests are the regression net — they must stay green).

- [ ] **Step 4: Run.** `go test ./internal/brain/ -run 'CertifyBuild|ReportBuild|report_build'` → PASS. **Commit:** `refactor(brain): extract certifyBuild so the gate runner reuses the signing path`.

---

## Task 4: Checkout the PR head in the jail + run + sign (the runner)

**Files:**
- Modify: `internal/repo/repo.go` (add `CheckoutPR`)
- Create: `internal/gate/runner.go`
- Test: `internal/repo/repo_test.go`, `internal/gate/runner_test.go`

**Interfaces:**
- Produces: `func (e *Engine) CheckoutPR(ctx, repoURL string, pr int, sha, destDir string) error`; `gate.Runner` with dep interfaces (defined in `gate`, so no import cycle):
```go
type Checkouter interface { CheckoutPR(ctx context.Context, repoURL string, pr int, sha, destDir string) error }
type Jail interface { Run(ctx context.Context, command, workspace string, network bool) (exitCode int, output string, err error) }
type Certifier interface { Certify(ctx context.Context, repo, commit, command string, exitCode int, outputDigest string) (recordID int64, head string, err error) }
type StatusPoster interface { SetCommitStatus(ctx context.Context, repoURL, sha, context, state, targetURL, description string) error }
```
- Consumes: `repo.Engine` (private `git`, `tokenURL`), `internal/sandbox.Run`/`Options` (via the brain-provided `Jail` adapter), `brain.certifyBuild` (via the brain-provided `Certifier` adapter).

- [ ] **Step 1: Failing test — `CheckoutPR` fetches the PR head and lands on the SHA.** In `repo_test.go`, stand up a local bare git repo with a commit, expose it as `refs/pull/1/head`, and assert `CheckoutPR` leaves `git -C dest rev-parse HEAD == sha`. (If a network-free git fixture is impractical, gate this test behind the same helper the existing `Clone` test uses.)

- [ ] **Step 2: Run, watch fail.** Then implement in `repo.go`:
```go
// CheckoutPR shallow-fetches a PR head ref into destDir and checks out sha.
// GitHub/Gitea expose the head at refs/pull/<n>/head. Fails closed if the
// fetched head does not match the expected sha.
func (e *Engine) CheckoutPR(ctx context.Context, repoURL string, pr int, sha, destDir string) error {
	if _, err := e.git(ctx, "", "init", destDir); err != nil {
		return err
	}
	if _, err := e.git(ctx, destDir, "remote", "add", "origin", e.tokenURL(repoURL)); err != nil {
		return err
	}
	ref := fmt.Sprintf("refs/pull/%d/head", pr)
	if _, err := e.git(ctx, destDir, "fetch", "--depth", "1", "origin", ref); err != nil {
		return err
	}
	if _, err := e.git(ctx, destDir, "checkout", "--detach", "FETCH_HEAD"); err != nil {
		return err
	}
	got, err := e.git(ctx, destDir, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if strings.TrimSpace(got) != sha {
		return fmt.Errorf("checkout PR #%d: fetched head %s != expected %s", pr, strings.TrimSpace(got), sha)
	}
	return nil
}
```
(Use the file's real private helper names for token injection — read `Engine.Clone`/`tokenURL` in `repo.go` and match exactly.)

- [ ] **Step 3: Failing test — the runner is fail-closed and reuses the signing path.** In `runner_test.go`, with fakes for all four deps:
```go
func TestRunnerPassPostsSuccessAndStores(t *testing.T) { /* passing jail exit 0 → SetCommitStatus success + store.Save(passed=true) + Certify called */ }
func TestRunnerFailPostsFailure(t *testing.T)          { /* jail exit 1 → SetCommitStatus failure, passed=false */ }
func TestRunnerCheckoutErrorNeverPostsSuccess(t *testing.T) { /* Checkouter errors → status ∈ {failure,error}, success NEVER posted, no stored passed=true */ }
```

- [ ] **Step 4: Run, watch fail. Implement `runner.go`.**
```go
type Runner struct {
	Checkout Checkouter
	Jail     Jail
	Certify  Certifier
	Status   StatusPoster
	Store    *Store
	// RecordURL builds the status target_url for a (repo, sha) → the /api/gate/run link.
	RecordURL func(repo, sha string) string
	Now       func() time.Time // injected clock
}

// Run gates one PR head. Fail-closed: success is posted ONLY on a real exit-0.
func (r *Runner) Run(ctx context.Context, repoURL string, p Policy, pr PRRef) error {
	owner, repo := splitRepo(p.Repo) // "owner/name"
	_ = owner
	target := r.RecordURL(p.Repo, pr.HeadSHA)
	_ = r.Status.SetCommitStatus(ctx, repoURL, pr.HeadSHA, p.Context, "pending", target, "corral gate running")

	dest, err := os.MkdirTemp("", "corral-gate-")
	if err != nil {
		return r.fail(ctx, repoURL, p, pr, target, "error", "workspace: "+err.Error())
	}
	defer func() { _ = os.RemoveAll(dest) }()

	if err := r.Checkout.CheckoutPR(ctx, repoURL, pr.Number, pr.HeadSHA, dest); err != nil {
		return r.fail(ctx, repoURL, p, pr, target, "error", "checkout: "+err.Error())
	}

	exit, output, runErr := r.Jail.Run(ctx, strings.Join(p.CheckCmd, " "), dest, p.AllowNet)
	if runErr != nil {
		return r.fail(ctx, repoURL, p, pr, target, "error", "jail: "+runErr.Error())
	}
	sum := sha256.Sum256([]byte(output))
	digest := "sha256:" + hex.EncodeToString(sum[:])

	recordID, _, certErr := r.Certify.Certify(ctx, p.Repo, pr.HeadSHA, strings.Join(p.CheckCmd, " "), exit, digest)
	if certErr != nil {
		return r.fail(ctx, repoURL, p, pr, target, "error", "sign: "+certErr.Error())
	}

	passed := exit == 0
	_ = r.Store.Save(Run{Repo: p.Repo, HeadSHA: pr.HeadSHA, PR: pr.Number, Passed: passed, RecordID: recordID, RanAt: r.Now()})
	state := "failure"
	if passed {
		state = "success"
	}
	return r.Status.SetCommitStatus(ctx, repoURL, pr.HeadSHA, p.Context, state, target, gateDesc(passed))
}

func (r *Runner) fail(ctx context.Context, repoURL string, p Policy, pr PRRef, target, state, msg string) error {
	_ = r.Store.Save(Run{Repo: p.Repo, HeadSHA: pr.HeadSHA, PR: pr.Number, Passed: false, RecordID: 0, RanAt: r.Now()})
	return r.Status.SetCommitStatus(ctx, repoURL, pr.HeadSHA, p.Context, state, target, msg)
}
```
Add small helpers `splitRepo`, `gateDesc`. **Invariant under test: `state` is only `"success"` when `exit == 0` and every prior step succeeded.**

- [ ] **Step 5: Run.** `go test ./internal/gate/... ./internal/repo/...` → PASS. **Commit:** `feat(gate): jail-run + sign the PR head; repo.CheckoutPR`.

---

## Task 5: Poller + config + brain wiring + read endpoint

**Files:**
- Create: `internal/gate/poller.go`, `internal/brain/gate.go`
- Modify: `cmd/corral/main.go`
- Test: `internal/gate/poller_test.go`, `internal/brain/gate_test.go`

**Interfaces:**
- Produces: `type Poller struct { Policies []Policy; List PRLister; Store *Store; Run func(ctx, repoURL string, p Policy, pr PRRef) error; Interval time.Duration }` with `(*Poller).Tick(ctx) error` (one pass) and `(*Poller).Loop(ctx)`; `PRLister interface { ListOpenPRs(ctx, repoURL, base string) ([]PRRef, error) }`; brain adapters (`certifierAdapter` over `certifyBuild`, `jailAdapter` over `sandbox.Run` + the brain's Isolator backend), and `GET /api/gate/run?repo=&sha=`.
- Consumes: Task-1 `Engine.ListOpenPRs`/`SetCommitStatus`, Task-2 `Store`, Task-3 `certifyBuild`, Task-4 `Runner`, the brain's existing sandbox `Backend`/`Options`, `repo.Engine` (`opts.Repo`).

- [ ] **Step 1: Failing test — the poller gates a new head SHA exactly once.**
```go
func TestPollerGatesNewHeadOnce(t *testing.T) {
	store, _ := OpenStore(filepath.Join(t.TempDir(), "g.db"))
	var runs int
	p := &Poller{
		Policies: []Policy{{Repo: "o/r", Base: []string{"main"}, Context: "corral/gate", CheckCmd: []string{"true"}}},
		List:     fakeLister{prs: []PRRef{{Number: 1, HeadSHA: "abc", Base: "main"}}},
		Store:    store,
		Run: func(ctx context.Context, repoURL string, pol Policy, pr PRRef) error {
			runs++
			return store.Save(Run{Repo: pol.Repo, HeadSHA: pr.HeadSHA, PR: pr.Number, RanAt: time.Unix(0, 0)})
		},
	}
	_ = p.Tick(context.Background())
	_ = p.Tick(context.Background()) // same SHA → no second run
	if runs != 1 {
		t.Fatalf("runs = %d, want 1 (dedupe by head SHA)", runs)
	}
}
```

- [ ] **Step 2: Run, watch fail. Implement `poller.go`.** `Tick`: for each policy, for each base (or "" if none), `List.ListOpenPRs`; for each PR, if `Store.GetBySHA(policy.Repo, pr.HeadSHA)` returns `ok==false`, call `Run`. `Loop`: `Tick` every `Interval`, honoring `ctx.Done()`. Log errors loudly (design directive), never crash the loop.

- [ ] **Step 3: Run, watch pass.** `go test ./internal/gate/ -run TestPoller` → PASS.

- [ ] **Step 4: Brain wiring test — `/api/gate/run` returns a stored run.** In `gate_test.go`, save a `Run`, hit `GET /api/gate/run?repo=o/r&sha=abc`, assert JSON `{passed, pr, record_id}`. Then implement `internal/brain/gate.go`:
  - `certifierAdapter{opts Options}` implementing `gate.Certifier` by building `reportBuildIn{Repo, Commit, Command, ExitCode, OutputDigest}` and calling `certifyBuild(ctx, opts, in, "corral-gate")`, returning `(out.ID, out.Head, err)`.
  - `jailAdapter{backend sandbox.Isolator}` implementing `gate.Jail` via `sandbox.Run(ctx, command, sandbox.Options{Workspace: workspace, Network: network, Backend: b.backend})` → `(res.ExitCode, res.Output, errFromResult)`. **Backend must be the brain's real bwrap Isolator — if it is nil, gating is disabled (do not run unsandboxed).**
  - `StartGate(ctx, opts Options)`: if `opts.GatePolicies` is empty, return (opt-in). Else build `Store`, `Runner{Checkout: opts.Repo, Jail: jailAdapter, Certify: certifierAdapter, Status: opts.Repo, Store: store, RecordURL: ..., Now: time.Now}`, and `Poller{..., Run: runner.Run}`; `go poller.Loop(ctx)`.
  - Register `GET /api/gate/run` on the brain mux (auth-gated like other `/api` routes), reading `Store.GetBySHA`.

- [ ] **Step 5: Config in `cmd/corral/main.go`.** Parse `CORRALAI_GATE_POLICIES` (semicolon-separated `repo=owner/name,base=main,cmd=go test ./...,net=false`), populate `Options.GatePolicies []gate.Policy`, and call `gate.StartGate` (or `brain.StartGate`) after the brain is up. Empty var → feature off (zero behavior change).

- [ ] **Step 6: Full gate.** `go vet ./... && go build ./... && go test ./... && bash scripts/check-security.sh` → all green. **Commit:** `feat(gate): poller + brain wiring + /api/gate/run (control-plane v1 complete)`.

---

## Self-Review (run against the spec)
- **Spec coverage:** poll open PRs (T5) ✓; run declared check in jail (T4) ✓; certify-by-execution reuse (T3) ✓; signed record bound to SHA (T3/T4) ✓; post required status (T1/T4) ✓; dedupe by SHA (T2/T5) ✓; read endpoint (T5) ✓; GitHub-only + Model A + poll ✓; gitea/gitlab `ErrUnsupported` (T1) ✓. Out-of-scope items (posture verifier, reconciliation, webhooks, App check-run, role-gates, Model B) are correctly absent.
- **Fail-closed:** the runner posts `success` only on `exit==0` after every prior step succeeded; every error path routes through `fail(...)` which posts `failure`/`error` and stores `passed=false`. Named as the invariant under test in T4.
- **No undefined refs:** all reused symbols (`certify.*`, `sandbox.Run/Options/Result`, `buildstore.Open`, `Engine.git/tokenURL/providerFor`, `report_build` internals) were read from the code before writing; where a helper name must match existing code (repoURL parser, tokenURL, test Options builder) the step says "read and reuse verbatim" rather than inventing one.
- **Clock discipline:** `RanAt`/`Now` are injected, never `time.Now()` inside the store (keeps tests deterministic).

<!-- SPDX-License-Identifier: Elastic-2.0 -->
# CISO Gates — Plan: sign + post the gate verdict (PostCisoGate)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn a `RunCisoGate` verdict into an accountable forge signal: **sign** a tamper-evident record of the CISO-gate result and **post** `corral/ciso-gate = success|failure` to the head SHA — fail-closed: never post an *unsigned* green (if signing fails, don't post, let the poller retry). This is the last composable unit before the real poller/checkout wiring.

**Architecture:** Add to `internal/cisogate`. `PostCisoGate` takes injected `Certifier` + `StatusPoster` interfaces (consumer-side, same shape as `gate.Certifier`/`gate.StatusPoster` — satisfied by the brain's certify adapter and `repo.Engine` in production), so it's fully testable with fakes. It maps the `CisoResult` to a status state + a human description, certifies the verdict, then posts.

**Tech Stack:** Go 1.26.5; `crypto/sha256`, `encoding/json`.

## Global Constraints
- SPDX header on every new file.
- **TDD**; per commit: `go vet ./...` + `go test ./...` + `go build ./...` + `bash scripts/check-security.sh` (`export PATH="$PATH:$HOME/go/bin"`) all green.
- **No unsigned green (fail-closed accountability):** `PostCisoGate` certifies BEFORE posting; if `Certify` errors it returns the error and does NOT call `SetCommitStatus` — a status without a signed record behind it is never posted.
- **State mapping:** `res.Pass` → `"success"`, else `"failure"`. exit code 0 vs 1 into the certify record.
- **Deterministic**: no `time.Now()`; the verdict digest is a stable hash of the result.
- Consumer-side interfaces (idiomatic Go, like the existing `LLM`/`Certifier` decls) — not logic duplication.
- corral metaphor.

## File Structure
- `internal/cisogate/post.go` — `Certifier`, `StatusPoster`, `PostRequest`, `PostCisoGate`, `describeResult`. (new)
- `internal/cisogate/post_test.go` — tests. (new)

## Interfaces (produced — the poller/brain wiring consumes these)
```go
type Certifier interface {
    Certify(ctx context.Context, repo, commit, command string, exitCode int, outputDigest string) (recordID int64, head string, err error)
}
type StatusPoster interface {
    SetCommitStatus(ctx context.Context, repoURL, sha, context, state, targetURL, description string) error
}
type PostRequest struct {
    RepoURL   string
    HeadSHA   string
    Context   string                  // the required-check name, e.g. "corral/ciso-gate"
    RecordURL func(sha string) string  // builds the status target_url (the signed record link)
}
// PostCisoGate signs the CISO-gate verdict and posts corral/ciso-gate to the head SHA.
// Fail-closed: on a signing error it returns the error WITHOUT posting a status.
func PostCisoGate(ctx context.Context, cert Certifier, poster StatusPoster, req PostRequest, res CisoResult) error
```

---

## Task 1: describeResult — the status description

**Files:** Create `internal/cisogate/post.go`; Test `internal/cisogate/post_test.go`

**Interfaces:**
- Produces: `func describeResult(res CisoResult) string`.

- [ ] **Step 1: Failing test.**
```go
func TestDescribeResult(t *testing.T) {
	if got := describeResult(CisoResult{}); got != "no CISO controls apply" {
		t.Errorf("empty: %q", got)
	}
	allPass := CisoResult{Pass: true, Results: []CisoTestResult{{Goal: "g1", Target: "t1", Passed: true}, {Goal: "g2", Target: "t2", Passed: true}}}
	if got := describeResult(allPass); got != "all 2 CISO controls passed" {
		t.Errorf("all-pass: %q", got)
	}
	oneFail := CisoResult{Pass: false, Results: []CisoTestResult{{Goal: "g1", Target: "t1", Passed: true}, {Goal: "g2", Target: "t2", Passed: false}}}
	if got := describeResult(oneFail); got != "1/2 CISO controls FAILED: g2@t2" {
		t.Errorf("one-fail: %q", got)
	}
}
```
- [ ] **Step 2: Run, watch fail.**
- [ ] **Step 3: Implement** `describeResult`:
```go
func describeResult(res CisoResult) string {
	if len(res.Results) == 0 {
		return "no CISO controls apply"
	}
	var failed []string
	for _, r := range res.Results {
		if !r.Passed {
			failed = append(failed, r.Goal+"@"+r.Target)
		}
	}
	if len(failed) == 0 {
		return fmt.Sprintf("all %d CISO controls passed", len(res.Results))
	}
	return fmt.Sprintf("%d/%d CISO controls FAILED: %s", len(failed), len(res.Results), strings.Join(failed, ", "))
}
```
- [ ] **Step 4: Run, watch pass.** `go test ./internal/cisogate/...` → PASS. **Commit:** `feat(cisogate): describeResult — CISO-gate status description`.

---

## Task 2: PostCisoGate — sign then post (no unsigned green)

**Files:** Modify `internal/cisogate/post.go`; Test `internal/cisogate/post_test.go`

**Interfaces:**
- Produces: `Certifier`, `StatusPoster`, `PostRequest`, `PostCisoGate`.
- Consumes: Task 1's `describeResult`; `CisoResult`; `crypto/sha256`+`encoding/json` for the verdict digest.

- [ ] **Step 1: Failing tests — pass/fail mapping + the no-unsigned-green invariant.**
```go
type fakeCert struct {
	gotCommand string
	gotExit    int
	gotDigest  string
	err        error
}
func (f *fakeCert) Certify(_ context.Context, repo, commit, command string, exit int, digest string) (int64, string, error) {
	f.gotCommand, f.gotExit, f.gotDigest = command, exit, digest
	return 7, "head7", f.err
}
type fakePoster struct {
	called                                       bool
	repoURL, sha, ctx, state, target, desc       string
}
func (f *fakePoster) SetCommitStatus(_ context.Context, repoURL, sha, context, state, target, desc string) error {
	f.called = true
	f.repoURL, f.sha, f.ctx, f.state, f.target, f.desc = repoURL, sha, context, state, target, desc
	return nil
}

func TestPostCisoGate(t *testing.T) {
	req := PostRequest{RepoURL: "github.com/o/r", HeadSHA: "abc", Context: "corral/ciso-gate",
		RecordURL: func(sha string) string { return "https://brain/rec/" + sha }}

	// PASS → success, exit 0, status posted with the description + record URL.
	cert, poster := &fakeCert{}, &fakePoster{}
	passRes := CisoResult{Pass: true, Results: []CisoTestResult{{Goal: "g1", Target: "t1", Passed: true}}}
	if err := PostCisoGate(context.Background(), cert, poster, req, passRes); err != nil {
		t.Fatal(err)
	}
	if cert.gotExit != 0 || cert.gotDigest == "" {
		t.Errorf("certify args: exit=%d digest=%q", cert.gotExit, cert.gotDigest)
	}
	if poster.state != "success" || poster.target != "https://brain/rec/abc" || poster.desc != "all 1 CISO controls passed" || poster.ctx != "corral/ciso-gate" {
		t.Errorf("posted: %+v", poster)
	}

	// FAIL → failure, exit 1.
	cert2, poster2 := &fakeCert{}, &fakePoster{}
	failRes := CisoResult{Pass: false, Results: []CisoTestResult{{Goal: "g1", Target: "t1", Passed: false}}}
	_ = PostCisoGate(context.Background(), cert2, poster2, req, failRes)
	if cert2.gotExit != 1 || poster2.state != "failure" {
		t.Errorf("fail path: exit=%d state=%q", cert2.gotExit, poster2.state)
	}

	// NO UNSIGNED GREEN: a certify error → error returned AND status NOT posted.
	cert3, poster3 := &fakeCert{err: errors.New("sign failed")}, &fakePoster{}
	if err := PostCisoGate(context.Background(), cert3, poster3, req, passRes); err == nil {
		t.Fatal("certify error must return an error")
	}
	if poster3.called {
		t.Fatal("must NOT post a status when signing failed (no unsigned green)")
	}
}
```
- [ ] **Step 2: Run, watch fail** (`PostCisoGate` undefined).
- [ ] **Step 3: Implement** the interfaces + `PostRequest` + `PostCisoGate`:
```go
func PostCisoGate(ctx context.Context, cert Certifier, poster StatusPoster, req PostRequest, res CisoResult) error {
	state, exit := "success", 0
	if !res.Pass {
		state, exit = "failure", 1
	}
	b, _ := json.Marshal(res) // stable: struct + ordered slice
	sum := sha256.Sum256(b)
	digest := "sha256:" + hex.EncodeToString(sum[:])

	if _, _, err := cert.Certify(ctx, req.RepoURL, req.HeadSHA, "corral/ciso-gate", exit, digest); err != nil {
		// Never post a status without a signed record behind it — return and let the poller retry.
		return fmt.Errorf("cisogate: certify verdict (not posting unsigned): %w", err)
	}
	return poster.SetCommitStatus(ctx, req.RepoURL, req.HeadSHA, req.Context, state, req.RecordURL(req.HeadSHA), describeResult(res))
}
```
- [ ] **Step 4: Run, watch pass.** `go test ./internal/cisogate/...` → PASS. Then the full gate: `go vet ./... && go build ./... && go test ./... && bash scripts/check-security.sh`. **Commit:** `feat(cisogate): PostCisoGate — sign the verdict, then post corral/ciso-gate (no unsigned green)`.

---

## Self-Review
- **Spec coverage (§4g post/sign):** map the CISO verdict → status state ✓; sign a tamper-evident record of the verdict ✓; post `corral/ciso-gate` with the record link + a human description ✓; **fail-closed: no unsigned green** (certify error → no post) ✓; deterministic (verdict digest is a stable hash) ✓.
- **No placeholders:** complete `describeResult` + `PostCisoGate` + fake cert/poster tests.
- **Type consistency:** `Certifier`/`StatusPoster` match `gate`'s shapes (consumer-side); `PostCisoGate` consumes `CisoResult` (from RunCisoGate).
- **The load-bearing invariant under test:** a certify error returns an error and the poster is NEVER called — no status is posted without a signed record.
- **Out of scope (the remaining forge/brain integration):** the poller that per-PR does `CheckoutPR` → extract head code → `ListVetted` → `RunCisoGate` → `PostCisoGate`; branch-protection requiring `corral/ciso-gate`; the CISO surface; goal→file mapping; the brain `Options`/`StartGate` wiring of the real certify adapter + `repo.Engine` + jail + model.

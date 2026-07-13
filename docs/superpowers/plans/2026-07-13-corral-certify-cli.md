<!-- SPDX-License-Identifier: Elastic-2.0 -->
# `corral certify <change>` — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `corral certify` a standalone, server-optional command that certifies a code change by execution — check out an exact commit into a jail, run the change's declared checks there, and emit a signed, offline-verifiable record with no brain required.

**Architecture:** Extend the existing `cmd/corral/certify.go` command. A new sibling file `cmd/corral/certify_change.go` holds the delta: a `git archive <ref>` → temp-workspace extractor, a jail runner (over `internal/sandbox`), and a local signer that mirrors the brain's `certifyBuild` recipe (`certify.BuildLedger`→`BuildAttestation`→`SignDSSE`). `runCertify` gains a `<ref>` positional and a `--net` flag and routes to the new standalone path **when `--brain` is absent**; **when `--brain` is present the existing behavior is unchanged** (zero regression risk to the legacy post-to-brain flow). A new `corral certify pubkey` prints the local signing pubkey so the offline verify loop closes.

**Tech Stack:** Go 1.26.5, module `github.com/pdbethke/corralai`. Reuses `internal/certify`, `internal/buildstore`, `internal/certverify`, `internal/sandbox`.

## Global Constraints
- SPDX header (`// SPDX-License-Identifier: Elastic-2.0`) on any new file.
- Per commit, from repo root: `export PATH="$PATH:$HOME/go/bin"` then `go vet ./...` && `go build ./...` && `go test ./...` && `bash scripts/check-security.sh` — all green.
- **Backward-compat invariant:** an invocation **with** `--brain` (or `$CORRAL_BRAIN`) must behave byte-identically to today (in-place run via `realRunner.RunCommand`, post to `report_build`, brain-signed record). The new checkout+jail+local-sign path runs **only when `--brain` is empty**. Do not change the legacy path's behavior.
- **Fail closed:** the new path never emits a signed "pass" record unless the jail backend resolved, the check actually ran in the jail, and signing succeeded. A jail/backend/setup error → exit 1, no record. Never fall back to running the check unsandboxed.
- **Exit-code fidelity:** the certified check's real exit code is `corral certify`'s exit code. A signing/write failure is reported to stderr but must not flip a pass↔fail (matches the legacy contract at certify.go:371-376).
- **Local record must round-trip:** a record produced by the local signer must pass `certverify.VerifyRecord(rec, localPubkey, …, allowUnanchored=true)`.
- **Naming:** the DSSE keyID for local signing is the constant `"corral-certify"` (keyID is informational; verification uses the pubkey).
- CLI-docs drift gate: `scripts/gen-cli-docs.sh --check` is enforced in CI (Deploy site). Any change to a binary's `-h` output REQUIRES regenerating the CLI reference (Task 6).

## File Structure
- **Create** `cmd/corral/certify_change.go` — the standalone-certify delta: `extractCommit`, the `jailRunner` interface + `realJail`, and `signBuildLocally`. Keeps `certify.go` focused (already ~510 lines).
- **Create** `cmd/corral/certify_change_test.go` — tests for the three helpers + the pubkey command + the standalone integration path.
- **Modify** `cmd/corral/certify.go` — add the `<ref>` positional + `--net` flag; route to the standalone path when `--brain` is empty; dispatch `certify pubkey`.
- **Modify** `cmd/corral/main.go` — the `case "certify"` dispatch passes the new real deps; update the certify usage/help text block; (env-var doc comments already cover `CORRALAI_CERTIFY_KEY`/`CORRALAI_CERTIFY_KEY_FILE`).
- **Modify** `docs/cli/*.md` + `site/src/content/docs/docs/cli/*.md` — regenerated (Task 6).
- **Modify** `README.md` — correct the `corral certify` description to the standalone form (Task 6).

## Interfaces produced (names later tasks rely on)
- `func extractCommit(ref string) (workdir, sha string, cleanup func(), err error)` — Task 1.
- `type jailRunner interface { Run(ctx context.Context, command, workdir string, net bool, timeout time.Duration) (exitCode int, combined []byte, dur time.Duration, err error) }` and `type realJail struct{}` — Task 3.
- `func signBuildLocally(rec buildRecord, priv ed25519.PrivateKey) (buildResult, error)` — Task 2.
- `func localCertifyKeyPath() string` and `func loadLocalCertifyKey() (ed25519.PrivateKey, error)` — Task 2.
- `runCertify` gains standalone routing + `certify pubkey` dispatch — Tasks 4, 5.

---

## Task 1: `extractCommit` — git-archive a ref into a fresh workspace

**Files:**
- Create: `cmd/corral/certify_change.go`
- Test: `cmd/corral/certify_change_test.go`

**Interfaces:**
- Produces: `func extractCommit(ref string) (workdir, sha string, cleanup func(), err error)`. Resolves `ref` to a commit sha (`git rev-parse <ref>^{commit}`), creates a temp dir, streams `git archive --format=tar <sha>` into it (extracting via `archive/tar`), and returns the dir + resolved sha + a cleanup func (`os.RemoveAll`). On any error returns a nil cleanup and a non-nil err (caller must nil-check cleanup).

- [ ] **Step 1: Write the failing test**

```go
// cmd/corral/certify_change_test.go
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// gitInitRepo makes a throwaway git repo with one committed file and one
// uncommitted edit, returning the repo dir and the committed content.
func gitInitRepo(t *testing.T) (dir, committed string) {
	t.Helper()
	dir = t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	committed = "committed contents\n"
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte(committed), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "file.txt")
	run("commit", "-q", "-m", "first")
	// an uncommitted edit that MUST NOT appear in the archived tree
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("DIRTY\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, committed
}

func TestExtractCommitArchivesTheCommitNotTheWorktree(t *testing.T) {
	repo, committed := gitInitRepo(t)
	// run extractCommit with cwd = repo
	restore := chdir(t, repo)
	defer restore()

	workdir, sha, cleanup, err := extractCommit("HEAD")
	if err != nil {
		t.Fatalf("extractCommit: %v", err)
	}
	defer cleanup()
	if len(sha) != 40 {
		t.Errorf("sha = %q, want a 40-char commit sha", sha)
	}
	got, err := os.ReadFile(filepath.Join(workdir, "file.txt"))
	if err != nil {
		t.Fatalf("reading extracted file: %v", err)
	}
	if string(got) != committed {
		t.Errorf("extracted file = %q, want the COMMITTED %q (uncommitted edits must be excluded)", got, committed)
	}
}

// chdir switches to dir for the duration of the test.
func chdir(t *testing.T, dir string) func() {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	return func() { _ = os.Chdir(prev) }
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `export PATH="$PATH:$HOME/go/bin"; go test ./cmd/corral/ -run TestExtractCommit -v`
Expected: FAIL — `undefined: extractCommit`.

- [ ] **Step 3: Write the implementation**

```go
// cmd/corral/certify_change.go
// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/certify"
)

// extractCommit resolves ref to a commit sha and materializes that exact
// committed tree into a fresh temp workspace via `git archive` — so the
// checks run against the commit, never the (possibly dirty) working tree.
// The returned cleanup removes the workspace; it is nil when err != nil.
func extractCommit(ref string) (workdir, sha string, cleanup func(), err error) {
	shaOut, err := exec.Command("git", "rev-parse", "--verify", "--quiet", ref+"^{commit}").Output() // #nosec G204 -- fixed "git"; ref is the operator's own certify target
	if err != nil {
		return "", "", nil, fmt.Errorf("resolving %q to a commit (is this a git repo?): %w", ref, err)
	}
	sha = strings.TrimSpace(string(shaOut))
	if sha == "" {
		return "", "", nil, fmt.Errorf("ref %q did not resolve to a commit", ref)
	}

	dir, err := os.MkdirTemp("", "corral-certify-*")
	if err != nil {
		return "", "", nil, fmt.Errorf("creating workspace: %w", err)
	}
	cleanup = func() { _ = os.RemoveAll(dir) }

	cmd := exec.Command("git", "archive", "--format=tar", sha) // #nosec G204 -- fixed "git"; sha is corral's own resolved commit
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cleanup()
		return "", "", nil, fmt.Errorf("git archive: %w", err)
	}
	if err := cmd.Start(); err != nil {
		cleanup()
		return "", "", nil, fmt.Errorf("git archive: %w", err)
	}
	if err := extractTar(stdout, dir); err != nil {
		_ = cmd.Wait()
		cleanup()
		return "", "", nil, fmt.Errorf("extracting archive: %w", err)
	}
	if err := cmd.Wait(); err != nil {
		cleanup()
		return "", "", nil, fmt.Errorf("git archive: %w", err)
	}
	return dir, sha, cleanup, nil
}

// extractTar writes every regular file/dir in the tar stream under dest,
// rejecting any entry whose cleaned path would escape dest (zip-slip guard).
func extractTar(r io.Reader, dest string) error {
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target := filepath.Join(dest, h.Name) // #nosec G305 -- guarded below
		if !strings.HasPrefix(target, filepath.Clean(dest)+string(os.PathSeparator)) && target != filepath.Clean(dest) {
			return fmt.Errorf("archive entry %q escapes the workspace", h.Name)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(h.Mode)&0o777) // #nosec G304 -- target is guarded against escape above
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil { // #nosec G110 -- git archive of the repo's own committed tree, not hostile input
				_ = f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		}
	}
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `export PATH="$PATH:$HOME/go/bin"; go test ./cmd/corral/ -run TestExtractCommit -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/corral/certify_change.go cmd/corral/certify_change_test.go
git commit -m "feat(certify): extract a commit into a jail workspace via git archive (certify-cli 1/6)"
```

---

## Task 2: `signBuildLocally` — mirror the brain's signing recipe in the CLI

**Files:**
- Modify: `cmd/corral/certify_change.go`
- Test: `cmd/corral/certify_change_test.go`

**Interfaces:**
- Consumes: `buildRecord` and `buildResult` (defined in certify.go); `certify.*`.
- Produces:
  - `func localCertifyKeyPath() string` — env `CORRALAI_CERTIFY_KEY_FILE` or `~/.claude/corralai_certify_key` (mirrors main.go:667).
  - `func loadLocalCertifyKey() (ed25519.PrivateKey, error)` — `buildstore.LoadOrCreateSigningKey(localCertifyKeyPath())`.
  - `func signBuildLocally(rec buildRecord, priv ed25519.PrivateKey) (buildResult, error)` — builds the 2-step ledger + attestation and DSSE-signs it, returning a `buildResult` with `ID:0`, `Head`, `Signature`, `Statement`, `PublicKey` (hex), `Steps`, `Anchored:false`. Byte-for-byte the same recipe as `internal/brain.certifyBuild` (context step + execution step), so the record is indistinguishable in shape from a brain-signed one.

- [ ] **Step 1: Write the failing test**

```go
// append to cmd/corral/certify_change_test.go
import (
	"crypto/ed25519"
	// ...existing imports...
	"github.com/pdbethke/corralai/internal/certverify"
)

func TestSignBuildLocallyRoundTripsThroughVerify(t *testing.T) {
	t.Setenv("CORRALAI_CERTIFY_KEY", "") // force the file/generated path off the env seed
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := buildRecord{
		Repo: "owner/x", Commit: "abc123", Branch: "main",
		Command: "go test ./...", ExitCode: 0, DurationS: 1.5,
		OutputDigest: "sha256:deadbeef",
	}
	res, err := signBuildLocally(rec, priv)
	if err != nil {
		t.Fatalf("signBuildLocally: %v", err)
	}
	if res.Signature == "" || res.Head == "" || res.PublicKey == "" {
		t.Fatalf("incomplete record: %+v", res)
	}
	pub := priv.Public().(ed25519.PublicKey)
	checks, ok := certverify.VerifyRecord(certverify.Record{
		Statement: res.Statement,
		Signature: res.Signature,
		Steps:     res.Steps,
		Head:      res.Head,
		Anchored:  res.Anchored,
	}, pub, nil, true)
	if !ok {
		t.Fatalf("VerifyRecord failed on a locally-signed record: %+v", checks)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `export PATH="$PATH:$HOME/go/bin"; go test ./cmd/corral/ -run TestSignBuildLocally -v`
Expected: FAIL — `undefined: signBuildLocally`.

- [ ] **Step 3: Write the implementation**

```go
// append to cmd/corral/certify_change.go
import (
	// add to the existing import block:
	"os/user"
	"github.com/pdbethke/corralai/internal/buildstore"
)

// localCertifyKeyPath mirrors cmd/corral/main.go's resolution so the CLI and
// the daemon sign with the same key file by default.
func localCertifyKeyPath() string {
	if p := strings.TrimSpace(os.Getenv("CORRALAI_CERTIFY_KEY_FILE")); p != "" {
		return p
	}
	home := ""
	if u, err := os.UserHomeDir(); err == nil {
		home = u
	} else if usr, err := user.Current(); err == nil {
		home = usr.HomeDir
	}
	return filepath.Join(home, ".claude", "corralai_certify_key")
}

func loadLocalCertifyKey() (ed25519.PrivateKey, error) {
	return buildstore.LoadOrCreateSigningKey(localCertifyKeyPath())
}

// signBuildLocally turns a raw build record into a signed, self-verifying
// buildResult using the local key — the same ledger/attestation/DSSE recipe
// as internal/brain.certifyBuild, so a locally-signed record is
// indistinguishable in shape from a brain-signed one and verifies with the
// same certverify.VerifyRecord path. Actor is the fixed local principal
// "corral-certify"; anchoring is never done here (Anchored=false).
func signBuildLocally(rec buildRecord, priv ed25519.PrivateKey) (buildResult, error) {
	const actor = "corral-certify"
	steps := []certify.Step{
		{
			Kind: "context", Actor: actor, Subject: rec.Repo + "@" + rec.Commit,
			Detail: map[string]any{"repo": rec.Repo, "commit": rec.Commit, "branch": rec.Branch},
		},
		{
			Kind: "execution", Actor: actor, Subject: rec.Command,
			Detail: map[string]any{
				"exit_code": rec.ExitCode, "ok": rec.ExitCode == 0,
				"duration_s": rec.DurationS, "output_digest": rec.OutputDigest,
			},
		},
	}
	built, head := certify.BuildLedger(steps)

	stmt := certify.BuildAttestation(certify.BuildRecord{
		Repo: rec.Repo, Commit: rec.Commit, Branch: rec.Branch, Actor: actor,
		Command: rec.Command, ExitCode: rec.ExitCode, DurationS: rec.DurationS,
		OutputDigest: rec.OutputDigest, ProducedBy: rec.ProducedBy,
	}, head)

	envelope, err := certify.SignDSSE(stmt, priv, "corral-certify")
	if err != nil {
		return buildResult{}, fmt.Errorf("signing statement: %w", err)
	}
	stepsJSON, err := certify.MarshalSteps(built)
	if err != nil {
		return buildResult{}, fmt.Errorf("marshaling steps: %w", err)
	}
	var stepsOut []map[string]any
	if err := json.Unmarshal(stepsJSON, &stepsOut); err != nil {
		return buildResult{}, fmt.Errorf("decoding steps: %w", err)
	}
	pub := priv.Public().(ed25519.PublicKey)
	return buildResult{
		ID: 0, Head: head, Signature: string(envelope), Statement: stmt,
		PublicKey: hex.EncodeToString(pub), Steps: stepsOut, Anchored: false,
	}, nil
}
```

Note: remove the now-unused `bytes`/`context`/`time` imports from Task 1's block if the compiler flags them, and add them back in Task 3 (which uses `context`/`time`/`bytes`). Keep the import list matching what the file actually uses at each commit.

- [ ] **Step 4: Run the test to verify it passes**

Run: `export PATH="$PATH:$HOME/go/bin"; go test ./cmd/corral/ -run TestSignBuildLocally -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/corral/certify_change.go cmd/corral/certify_change_test.go
git commit -m "feat(certify): local DSSE signing mirroring the brain's certifyBuild recipe (certify-cli 2/6)"
```

---

## Task 3: `jailRunner` — run the check in the sandbox jail

**Files:**
- Modify: `cmd/corral/certify_change.go`
- Test: `cmd/corral/certify_change_test.go`

**Interfaces:**
- Produces:
  - `type jailRunner interface { Run(ctx context.Context, command, workdir string, net bool, timeout time.Duration) (exitCode int, combined []byte, dur time.Duration, err error) }`
  - `type realJail struct{}` implementing it via `sandbox.Resolve` + `sandbox.RunGuarded`. `err` is non-nil ONLY when the check could not be run in a jail at all (no backend, setup failure) — a normal nonzero check exit is reported via `exitCode`, not `err`. This is the fail-closed seam: a nil-backend/resolve failure returns err (caller aborts, no record).

- [ ] **Step 1: Write the failing test**

```go
// append to cmd/corral/certify_change_test.go
import (
	"context"
	"time"
)

func TestRealJailRunsInSandboxOrFailsClosed(t *testing.T) {
	// This test asserts the fail-closed contract without requiring bwrap:
	// with no exec backend available/allowed, realJail.Run must return an
	// error (never a silent unsandboxed run).
	t.Setenv("AGENT_EXEC_UNSAFE_HOST", "") // ensure the unsafe host backend is NOT opted in
	dir := t.TempDir()
	_, _, _, err := realJail{}.Run(context.Background(), "true", dir, false, 5*time.Second)
	if err == nil {
		t.Skip("a real jail backend is available in this environment; fail-closed path not exercised here")
	}
	// err != nil is the required fail-closed behavior when no backend resolves.
}
```

Note: the fake `jailRunner` used to drive the integration test lives in Task 4's test; this task only proves `realJail` fails closed (or is skipped when a real backend exists in CI).

- [ ] **Step 2: Run the test to verify it fails**

Run: `export PATH="$PATH:$HOME/go/bin"; go test ./cmd/corral/ -run TestRealJail -v`
Expected: FAIL — `undefined: realJail`.

- [ ] **Step 3: Write the implementation**

```go
// append to cmd/corral/certify_change.go
import (
	// add:
	"github.com/pdbethke/corralai/internal/sandbox"
)

// jailRunner runs the certified check inside a sandbox. err is returned ONLY
// when the check could not be jail-run at all (no backend, setup failure) —
// the fail-closed seam; a normal nonzero check exit comes back via exitCode.
type jailRunner interface {
	Run(ctx context.Context, command, workdir string, net bool, timeout time.Duration) (exitCode int, combined []byte, dur time.Duration, err error)
}

// realJail resolves a real isolator (bwrap on linux, etc.) and runs the
// command via sandbox.RunGuarded — the one true "failed run != success"
// home. A nil/unavailable backend fails closed (never an unsandboxed run).
type realJail struct{}

func (realJail) Run(ctx context.Context, command, workdir string, net bool, timeout time.Duration) (int, []byte, time.Duration, error) {
	iso, err := sandbox.Resolve(sandbox.Config{Backend: os.Getenv("CORRALAI_EXEC_BACKEND")})
	if err != nil {
		return -1, nil, 0, fmt.Errorf("no sandbox backend (refusing to run unsandboxed): %w", err)
	}
	start := time.Now()
	res, err := sandbox.RunGuarded(ctx, command, sandbox.Options{
		Workspace: workdir, Timeout: timeout, Network: net, Backend: iso,
	})
	dur := time.Since(start)
	if err != nil {
		return -1, nil, dur, err
	}
	if res.TimedOut {
		return res.ExitCode, []byte(res.Output), dur, fmt.Errorf("check timed out after %s", timeout)
	}
	return res.ExitCode, []byte(res.Output), dur, nil
}
```

Note: verify `sandbox.Config`'s exact field set (`Backend string`, `UnsafeHost bool`) against `internal/sandbox/isolator.go` and adjust the literal if needed; do not invent fields. Confirm `sandbox.Resolve` returns an error (not a nil isolator) when no backend is available — the fail-closed contract depends on it.

- [ ] **Step 4: Run the test to verify it passes**

Run: `export PATH="$PATH:$HOME/go/bin"; go test ./cmd/corral/ -run TestRealJail -v`
Expected: PASS (or SKIP if a real backend exists in the env — both are acceptable).

- [ ] **Step 5: Commit**

```bash
git add cmd/corral/certify_change.go cmd/corral/certify_change_test.go
git commit -m "feat(certify): jail runner over internal/sandbox, fail-closed on no backend (certify-cli 3/6)"
```

---

## Task 4: wire the standalone path into `runCertify`

**Files:**
- Modify: `cmd/corral/certify.go`
- Modify: `cmd/corral/main.go`
- Test: `cmd/corral/certify_change_test.go`

**Interfaces:**
- Consumes: `extractCommit`, `jailRunner`, `signBuildLocally`, `loadLocalCertifyKey`.
- `runCertify` signature gains a `jail jailRunner` parameter and a `signKey func() (ed25519.PrivateKey, error)` parameter (so tests inject fakes). New signature:
  `func runCertify(args []string, run cmdRunner, post buildPoster, jail jailRunner, signKey func() (ed25519.PrivateKey, error), stdout, stderr io.Writer) int`
- Behavior: parse a `<ref>` positional (`fs.Arg(0)`, default `HEAD`) and a `--net` bool (default true). If `*brainURL != ""` → existing legacy path unchanged. If empty → standalone path: `extractCommit(ref)` → `jail.Run(command, workdir, net, DefaultTimeout)` → build `buildRecord` (Repo/Commit/Branch/message/author/signature via the existing git-context capture, Commit forced to the resolved sha) → `signBuildLocally` → write `--out` (default `./certify-<short-sha>.json`) → print `certified <sha> (pass|fail): <out>` → return the check exit code.

- [ ] **Step 1: Write the failing test**

```go
// append to cmd/corral/certify_change_test.go

// fakeJail runs nothing; it returns a canned exit code + output.
type fakeJail struct{ exit int; out string }
func (f fakeJail) Run(_ context.Context, _, _ string, _ bool, _ time.Duration) (int, []byte, time.Duration, error) {
	return f.exit, []byte(f.out), time.Millisecond, nil
}

func TestRunCertifyStandaloneSignsLocallyAndVerifies(t *testing.T) {
	repo, _ := gitInitRepo(t)
	restore := chdir(t, repo)
	defer restore()

	_, priv, _ := ed25519.GenerateKey(nil)
	signKey := func() (ed25519.PrivateKey, error) { return priv, nil }

	out := filepath.Join(t.TempDir(), "rec.json")
	var stdout, stderr bytes.Buffer
	// no --brain -> standalone path; a passing check (fake jail exit 0)
	code := runCertify(
		[]string{"HEAD", "--out", out, "--", "true"},
		realRunner{}, nil /*post unused*/, fakeJail{exit: 0, out: "ok"}, signKey,
		&stdout, &stderr,
	)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%s", code, stderr.String())
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("record not written: %v", err)
	}
	var rec certRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("record not valid JSON: %v", err)
	}
	pub := priv.Public().(ed25519.PublicKey)
	checks, ok := certverify.VerifyRecord(certverify.Record{
		Statement: rec.Statement, Signature: rec.Signature, Steps: rec.Steps,
		Head: rec.Head, Anchored: rec.Anchored,
	}, pub, nil, true)
	if !ok {
		t.Fatalf("written record fails verification: %+v", checks)
	}
}

func TestRunCertifyStandalonePropagatesFailingExit(t *testing.T) {
	repo, _ := gitInitRepo(t)
	restore := chdir(t, repo)
	defer restore()
	_, priv, _ := ed25519.GenerateKey(nil)
	out := filepath.Join(t.TempDir(), "rec.json")
	var stdout, stderr bytes.Buffer
	code := runCertify(
		[]string{"--out", out, "--", "false"},
		realRunner{}, nil, fakeJail{exit: 1, out: "boom"}, func() (ed25519.PrivateKey, error) { return priv, nil },
		&stdout, &stderr,
	)
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (failing check must propagate)", code)
	}
	// a record is still written for a failing check ("did not pass, here's proof")
	if _, err := os.Stat(out); err != nil {
		t.Errorf("expected a record even for a failing check: %v", err)
	}
}
```

(Requires `certRecord` to expose `Statement/Signature/Steps/Head/Anchored` — it already does in verify.go; confirm the JSON tags match what the `--out` writer emits.)

- [ ] **Step 2: Run to verify it fails**

Run: `export PATH="$PATH:$HOME/go/bin"; go test ./cmd/corral/ -run TestRunCertifyStandalone -v`
Expected: FAIL — signature mismatch (`runCertify` doesn't take `jail`/`signKey` yet).

- [ ] **Step 3: Implement**

In `cmd/corral/certify.go`:
1. Change the signature to `func runCertify(args []string, run cmdRunner, post buildPoster, jail jailRunner, signKey func() (ed25519.PrivateKey, error), stdout, stderr io.Writer) int`.
2. After `fs.Parse(flagArgs)`, read the ref and net flag:
```go
netFlag := fs.Bool("net", true, "allow network inside the jail for the check (use --net=false to lock down)")
// ...after Parse:
ref := fs.Arg(0)
if ref == "" {
	ref = "HEAD"
}
```
3. Keep the legacy path guarded by `*brainURL != ""` (unchanged). When `*brainURL == ""`, run the standalone path instead of the current "brain required" error:
```go
if strings.TrimSpace(*brainURL) == "" {
	return runCertifyStandalone(ref, checkArgv, *outPath, *netFlag, producedBy, run, jail, signKey, stdout, stderr)
}
```
4. Add `runCertifyStandalone` (in certify_change.go) that: `extractCommit(ref)` (defer cleanup); capture repo/branch/message/author/signature via the existing helpers but force `commit = sha`; build the `buildRecord`; `jail.Run(strings.Join(checkArgv, " "), workdir, net, DefaultCertifyTimeout)` — on err, print + return 1 (fail closed); compute the sha256 digest of combined output; `priv, err := signKey()`; `res, err := signBuildLocally(rec, priv)`; write the record to `outPath` (default `certify-<sha[:12]>.json`) reusing the same record map the legacy `--out` writer builds; print `certified <sha> (pass|fail): <path>`; return the check exit code.
5. Define `const DefaultCertifyTimeout = 600 * time.Second` (or import `gate.DefaultGateTimeout` if you prefer — but avoid a new import cycle; a local const is fine).

In `cmd/corral/main.go` `case "certify"`: pass the new real deps:
```go
os.Exit(runCertify(os.Args[2:], realRunner{}, mcpPoster{}, realJail{}, loadLocalCertifyKey, os.Stdout, os.Stderr))
```

Extract the shared record-map writer so both the legacy and standalone paths use it (DRY) — a helper `writeRecord(path string, res buildResult) error` in certify_change.go, called by both.

- [ ] **Step 4: Run to verify it passes**

Run: `export PATH="$PATH:$HOME/go/bin"; go test ./cmd/corral/ -run 'TestRunCertify|TestExtractCommit|TestSignBuildLocally' -v`
Expected: PASS. Also run the full `go test ./cmd/corral/` to confirm the existing certify tests still pass with the new signature (update any existing `runCertify(...)` test call sites to pass `nil, nil` for `jail, signKey` on the legacy `--brain` paths, or a fake as appropriate).

- [ ] **Step 5: Commit**

```bash
git add cmd/corral/certify.go cmd/corral/certify_change.go cmd/corral/certify_change_test.go cmd/corral/main.go
git commit -m "feat(certify): standalone path — no --brain checks out+jails+local-signs a change (certify-cli 4/6)"
```

---

## Task 5: `corral certify pubkey`

**Files:**
- Modify: `cmd/corral/certify.go` (dispatch)
- Modify: `cmd/corral/certify_change.go` (impl)
- Test: `cmd/corral/certify_change_test.go`

**Interfaces:**
- `corral certify pubkey` loads the local signing key and prints its Ed25519 public key as hex (so `corral certify verify <rec> --pubkey <hex>` closes the offline loop). Dispatched in `runCertify` right after the `verify` sub-subcommand check.

- [ ] **Step 1: Write the failing test**

```go
// append to cmd/corral/certify_change_test.go
func TestCertifyPubkeyMatchesTheSigningKey(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	seed := hex.EncodeToString(priv.Seed())
	t.Setenv("CORRALAI_CERTIFY_KEY", seed)

	var stdout, stderr bytes.Buffer
	code := runCertify([]string{"pubkey"}, realRunner{}, nil, nil, loadLocalCertifyKey, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, stderr.String())
	}
	want := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	if strings.TrimSpace(stdout.String()) != want {
		t.Errorf("pubkey = %q, want %q", strings.TrimSpace(stdout.String()), want)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `export PATH="$PATH:$HOME/go/bin"; go test ./cmd/corral/ -run TestCertifyPubkey -v`
Expected: FAIL (prints usage / unknown, not the pubkey).

- [ ] **Step 3: Implement**

In `runCertify`, right after the `args[0] == "verify"` block:
```go
if len(args) > 0 && args[0] == "pubkey" {
	return runCertifyPubkey(signKey, stdout, stderr)
}
```
In certify_change.go:
```go
func runCertifyPubkey(signKey func() (ed25519.PrivateKey, error), stdout, stderr io.Writer) int {
	if signKey == nil {
		signKey = loadLocalCertifyKey
	}
	priv, err := signKey()
	if err != nil {
		fmt.Fprintf(stderr, "corral certify pubkey: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, hex.EncodeToString(priv.Public().(ed25519.PublicKey)))
	return 0
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `export PATH="$PATH:$HOME/go/bin"; go test ./cmd/corral/ -run TestCertifyPubkey -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/corral/certify.go cmd/corral/certify_change.go cmd/corral/certify_change_test.go
git commit -m "feat(certify): corral certify pubkey prints the local signing pubkey (certify-cli 5/6)"
```

---

## Task 6: help text + regenerate CLI docs + README

**Files:**
- Modify: `cmd/corral/main.go` (the `corral certify` usage block ~lines 190-199)
- Modify: `docs/cli/corral.md` + `site/src/content/docs/docs/cli/corral.md` (regenerated)
- Modify: `README.md`

- [ ] **Step 1: Update the certify usage/help text** in `cmd/corral/main.go` to reflect the standalone form. Replace the certify usage lines with:
```
  corral certify [<ref>] [--out <file>] [--net=false] [--produced-by a,b] -- <check-cmd>...
        Certify a change by execution: check out <ref> (default HEAD) into a jail,
        run <check-cmd> there, and write a signed, offline-verifiable record.
        Signs locally (no server) unless --brain is given.
  corral certify --brain <url> [flags] -- <check-cmd>...   (also post the record to a brain)
  corral certify verify <record> [--pubkey <hex>|--brain <url>] [--allow-unanchored]
  corral certify pubkey                                    (print the local signing pubkey)
```

- [ ] **Step 2: Regenerate the CLI reference (the drift gate that bit us on the last deploy).**

Run: `export PATH="$PATH:$HOME/go/bin"; bash scripts/gen-cli-docs.sh`
Then verify: `bash scripts/gen-cli-docs.sh --check` → `OK: generated CLI docs match every binary's real -h output`.

- [ ] **Step 3: Update README.md.** Find the `corral certify` description (currently framed around the brain-posting/build-record flow) and correct it to lead with the standalone form: "`corral certify <ref> -- <cmd>` checks out that commit into a jail, runs `<cmd>`, and writes a signed record you verify offline with `corral certify verify` — no server required; `--brain` optionally posts it to a brain." Keep it honest: this certifies the change's *declared checks*; the control-owner tests and the adversarial herd are later slices (link the spec).

- [ ] **Step 4: Full gate + commit.**

Run: `export PATH="$PATH:$HOME/go/bin"; go vet ./... && go build ./... && go test ./... && bash scripts/check-security.sh`
Expected: all green; `scripts/gen-cli-docs.sh --check` green.

```bash
git add cmd/corral/main.go docs/cli/ site/src/content/docs/docs/cli/ README.md
git commit -m "docs(certify): standalone certify help + regenerated CLI reference + README (certify-cli 6/6)"
```

---

## Self-Review
- **Spec coverage:** clean git-archive checkout (Task 1) ✓; jail the run + fail-closed (Task 3) ✓; local sign, server-optional (Task 2, wired Task 4) ✓; `<ref>` positional + `--net` (Task 4) ✓; `corral certify pubkey` closes the offline verify loop (Task 5) ✓; `--brain` legacy path unchanged (Task 4 routing) ✓; docs honesty + CLI-drift-gate (Task 6) ✓. Non-goals (control tests, adversarial herd, scrub, PR mode) are untouched.
- **Type consistency:** `runCertify`'s new signature `(args, run, post, jail, signKey, stdout, stderr)` is applied at its one call site (main.go) and all test call sites (Tasks 4-5); `signBuildLocally` returns the same `buildResult` the `--out` writer already consumes; the local record satisfies `certverify.Record`.
- **Fail-closed:** `realJail.Run` errors (never silently runs unsandboxed) when no backend resolves; the standalone path aborts with exit 1 and writes no record on a jail error; exit-code fidelity preserved.
- **Placeholder scan:** every code step carries real code; the two "verify the exact field set / import list" notes are explicit compiler-guided checks, not deferred work.
- **Risk called out:** `--net=false` on a `go test` checkout can fail to resolve modules (documented in help + spec); `sandbox.Config` field set must be confirmed against the real struct before relying on the literal.

# `corral certify` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `corral certify -- <check>` runs the check itself, then the brain builds a tamper-evident + SLSA-provenance record, signs it, and stores it — an independently-verified, recallable accountability record for any build.

**Architecture:** A thin `corral certify` subcommand captures git/CI context, runs the check (exit-passthrough), and POSTs a raw build record to the brain over MCP (authed with `CORRALAI_BRAIN_KEY`). The brain's `report_build` tool builds a hash-linked ledger + in-toto/SLSA attestation via a new `internal/certify` package, signs the head with a persisted Ed25519 key, stores it, and returns the signed statement. Signing lives only in the brain.

**Tech Stack:** Go 1.26; stdlib `crypto/ed25519`, `crypto/sha256`, `os/exec`; the go-sdk MCP client/server; **`go-duckdb` for storage — the same engine `internal/telemetry` already uses.** The build-record store is DuckDB-native so the identical schema federates to a shared **MotherDuck** warehouse later by swapping the DSN (a local `.duckdb` path → an `md:` connection string) — a config flip, not a rewrite. This is the "distributed dev shop accountability warehouse" the vision rides on; building the store DuckDB-native now means we don't pay twice.

## Global Constraints

- SPDX header `// SPDX-License-Identifier: Elastic-2.0` on every new Go file.
- TDD: failing test first, watch it fail, minimal code, watch it pass, commit.
- `go vet ./...` clean; full suite green; `bash scripts/check-security.sh` green before each commit (gosec + govulncheck — annotate justified `#nosec`).
- No secret/key material in logs or errors (redact); the signing key never leaves the brain.
- `corral certify` MUST pass through the check's exit code (never mask a real build failure).
- Corral metaphor in user copy; no bee/hive/swarm.
- Branch: `feat/corral-certify` (spec committed there).

## File Structure

- `internal/certify/certify.go` — `Step`, `Ledger`, `BuildRecord`, `Statement`; `BuildLedger`, `VerifyLedger`, `BuildAttestation`; Ed25519 `Sign`/`Verify`.
- `internal/certify/certify_test.go`.
- `internal/brain/buildcert.go` — the `report_build` MCP tool + the persisted signing key loader.
- `internal/brain/buildcert_test.go`.
- `internal/queue` or `internal/buildstore/store.go` — `SaveBuild`/`GetBuild` (build_records table).
- `cmd/corral/certify.go` — `runCertify` + git context + command run + MCP post.
- `cmd/corral/certify_test.go`.
- Modify `cmd/corral/main.go` — dispatch `certify` before server boot.

---

### Task 1: `internal/certify` — ledger, attestation, Ed25519 signing

**Files:** Create `internal/certify/certify.go`, `internal/certify/certify_test.go`

**Interfaces — Produces:**
- `type Step struct { Seq int; TS float64; Kind, Actor, Model, Subject string; Detail map[string]any; Prev, Hash string }`
- `type BuildRecord struct { Repo, Commit, Branch, Actor, Command string; ExitCode int; DurationS float64; OutputDigest string; ProducedBy []string; StartedTS, FinishedTS float64 }`
- `func BuildLedger(steps []Step) (out []Step, head string)` — sets `Prev`/`Hash` on each; head = last hash (`H(json(step_without_hash))`, `Prev` chained; genesis = 64×"0").
- `func VerifyLedger(steps []Step, head string) (bool, string)`.
- `func BuildAttestation(r BuildRecord, head string) map[string]any` — in-toto Statement v1 + SLSA Provenance v1 predicate; `subject[0].digest.sha256 = head`; models in `resolvedDependencies`; certification (command/exit/pass) in `byproducts`.
- `func Sign(head string, priv ed25519.PrivateKey) string` (hex) and `func VerifySig(head, sigHex string, pub ed25519.PublicKey) bool`.

- [ ] **Step 1: Write the failing test** `internal/certify/certify_test.go`:

```go
// SPDX-License-Identifier: Elastic-2.0

package certify

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
)

func TestLedgerRoundTripAndTamper(t *testing.T) {
	steps := []Step{
		{Kind: "context", Subject: "repo@abc123"},
		{Kind: "execution", Subject: "go test ./...", Detail: map[string]any{"exit_code": 0, "ok": true}},
	}
	built, head := BuildLedger(steps)
	if head == "" || built[0].Prev != "0000000000000000000000000000000000000000000000000000000000000000" {
		t.Fatalf("genesis/head wrong: head=%q prev0=%q", head, built[0].Prev)
	}
	if ok, msg := VerifyLedger(built, head); !ok {
		t.Fatalf("clean ledger should verify: %s", msg)
	}
	// Tamper: flip the recorded pass, do NOT recompute the chain.
	built[1].Detail = map[string]any{"exit_code": 1, "ok": false}
	if ok, _ := VerifyLedger(built, head); ok {
		t.Fatal("tampered ledger must fail verification")
	}
}

func TestAttestationSubjectIsLedgerHead(t *testing.T) {
	_, head := BuildLedger([]Step{{Kind: "execution", Subject: "go build"}})
	att := BuildAttestation(BuildRecord{Repo: "r", Commit: "c", Command: "go build", ExitCode: 0, ProducedBy: []string{"anthropic:claude-opus"}}, head)
	subj := att["subject"].([]map[string]any)[0]["digest"].(map[string]string)["sha256"]
	if subj != head {
		t.Fatalf("subject digest %q != ledger head %q", subj, head)
	}
	if att["predicateType"] != "https://slsa.dev/provenance/v1" {
		t.Fatalf("wrong predicateType: %v", att["predicateType"])
	}
}

func TestSignVerify(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	head := "deadbeef"
	sig := Sign(head, priv)
	if !VerifySig(head, sig, pub) {
		t.Fatal("valid signature must verify")
	}
	if VerifySig("tampered", sig, pub) {
		t.Fatal("signature must not verify a different head")
	}
}
```

- [ ] **Step 2: Run it, verify it fails.** `go test ./internal/certify/ -v` → FAIL (undefined).

- [ ] **Step 3: Implement `internal/certify/certify.go`.** Port the spike (`scratch/attestation-spike/chainlib.py` + `gen.py`) to Go. Ledger: `hash = sha256(json.Marshal(step-without-Hash))` with `Prev` chained (marshal a deterministic struct — use a fixed field order via a helper struct, since Go map marshaling is sorted by key so `json.Marshal(map)` is deterministic). Attestation mirrors the spike's structure. `Sign` = `hex(ed25519.Sign(priv, []byte(head)))`; `VerifySig` decodes hex and `ed25519.Verify`.

- [ ] **Step 4: Run it, verify it passes.** `go test ./internal/certify/ -v` → PASS.

- [ ] **Step 5: Commit.**
```bash
git add internal/certify/
git commit -m "feat(certify): tamper-evident ledger + SLSA attestation + Ed25519 signing"
```

---

### Task 2: build-records store + persisted signing key

**Files:** Create `internal/buildstore/store.go`, `internal/buildstore/store_test.go`

**Interfaces — Produces:**
- `func Open(dsn string) (*Store, error)` — DuckDB via `sql.Open("duckdb", dsn)` (mirror `internal/telemetry/store.go`). `dsn` is a local `.duckdb` path today; the *same* code accepts a MotherDuck `md:` DSN later (federation = a config flip). Table `build_records(id BIGINT PRIMARY KEY, repo VARCHAR, commit_sha VARCHAR, branch VARCHAR, actor VARCHAR, head VARCHAR, signature VARCHAR, statement JSON, created_ts DOUBLE)`.
- `func (s *Store) Save(repo, commit, branch, actor, head, sig, statementJSON string) (int64, error)`
- `func (s *Store) Get(id int64) (map[string]any, bool, error)` — returns the stored statement + signature.
- `func LoadOrCreateSigningKey(path string) (ed25519.PrivateKey, error)` — reads a 0600 seed file, generating + persisting one if absent (mirrors `internal/creds` `loadOrCreateIdentity`); env `CORRALAI_CERTIFY_KEY` (hex seed) overrides.

- [ ] **Step 1: Write the failing test** covering: `Save`→`Get` round-trip; `LoadOrCreateSigningKey` persists + reloads the *same* key (a fresh key each restart would invalidate every prior signature); the seed file is `0600`. (Mirror `internal/creds/herd_test.go` + `agefile_test.go` patterns.)

- [ ] **Step 2: Run it, verify it fails.**

- [ ] **Step 3: Implement `internal/buildstore/store.go`** — DuckDB via `sql.Open("duckdb", dsn)`, mirroring `internal/telemetry/store.go` (import `_ "github.com/marcboeker/go-duckdb/v2"`; `CREATE TABLE IF NOT EXISTS build_records (…)` in `Open`; DuckDB uses `?` placeholders as telemetry does). Keep `dsn` opaque so an `md:` MotherDuck string works unchanged. `LoadOrCreateSigningKey`: env `CORRALAI_CERTIFY_KEY` hex seed → `ed25519.NewKeyFromSeed`; else read the seed file; else `ed25519.GenerateKey` + write `priv.Seed()` as hex, `0600` (mirror `internal/creds` `loadOrCreateIdentity`).

- [ ] **Step 4: Run it, verify it passes.**

- [ ] **Step 5: Commit.**
```bash
git add internal/buildstore/
git commit -m "feat(buildstore): signed build-record store + persisted Ed25519 signing key"
```

---

### Task 3: the `report_build` brain tool

**Files:** Create `internal/brain/buildcert.go`, `internal/brain/buildcert_test.go`; modify `internal/brain/server.go` (register it) + `internal/brain/identity.go` (Options: `BuildStore *buildstore.Store`, `CertifyKey ed25519.PrivateKey`).

**Interfaces:**
- Consumes: `internal/certify`, `internal/buildstore`.
- Produces: MCP tool `report_build` accepting `{repo, commit, branch, command, exit_code, duration_s, output_digest, produced_by[]}` → builds ledger+attestation → signs head → stores → returns `{id, head, signature, statement}`. Emits a telemetry `build_certified` event. Actor = `actorOf(req)` (the CI principal).

- [ ] **Step 1: Write the failing test** `internal/brain/buildcert_test.go` — over the in-memory MCP transport (mirror `missions_test.go`): open a temp `buildstore`, generate a keypair, `NewServer(nil,nil,Options{BuildStore:bs, CertifyKey:priv})`, call `report_build` with a passing build, assert the returned `statement.subject.digest.sha256 == head`, `certify.VerifySig(head, signature, pub)` is true, the record is in the store, and a tampered statement fails `VerifyLedger`.

- [ ] **Step 2: Run it, verify it fails.**

- [ ] **Step 3: Implement `internal/brain/buildcert.go`** — the tool handler assembles `certify.Step`s (context step + execution step), `BuildLedger` → head, `BuildAttestation`, `certify.Sign(head, opts.CertifyKey)`, `opts.BuildStore.Save(...)`, `rec(tel, 0, "build_certified", actorOf(req), repo+"@"+commit, {...})`, return the signed statement. Guard `opts.BuildStore == nil` (tool off). Register in `server.go` when `opts.BuildStore != nil`.

- [ ] **Step 4: Run it, verify it passes.** `go test ./internal/brain/ -run TestReportBuild` → PASS.

- [ ] **Step 5: Commit.**
```bash
git add internal/brain/
git commit -m "feat(brain): report_build — certify + sign + store an external build record"
```

---

### Task 4: `corral certify` CLI + wiring

**Files:** Create `cmd/corral/certify.go`, `cmd/corral/certify_test.go`; modify `cmd/corral/main.go` (dispatch), `cmd/corral/main.go` `usageText` (list `certify`); wire `Options.BuildStore`/`CertifyKey` in `cmd/corral/main.go` server setup.

**Interfaces:**
- Consumes: `internal/creds` (`CORRALAI_BRAIN_KEY`), the MCP client (mirror `cmd/corral-admin/client.go` `dial`).
- Produces: `func runCertify(args []string, run cmdRunner, post buildPoster, stdout, stderr io.Writer) int` — testable with fakes; the real `run`/`post` are `os/exec` + the MCP client.

- [ ] **Step 1: Write the failing test** `cmd/corral/certify_test.go`: with a fake `run` (returns a chosen exit code + output) and a fake `post` (captures the record, returns a stub statement), assert: `runCertify(["--brain","x","--","go","test"], fakeRun, fakePost,…)` returns the runner's exit code (test 0 AND 2 — exit passthrough); the posted record carries the command, exit code, and git context (inject context via flags `--repo`/`--commit` so the test needs no real git); `--out <file>` writes the returned statement.

- [ ] **Step 2: Run it, verify it fails.**

- [ ] **Step 3: Implement `cmd/corral/certify.go`.** Parse args (split on `--`), resolve `--repo`/`--commit`/`--branch` from flags else `git` via `run`, resolve token via `creds.Open().Get("CORRALAI_BRAIN_KEY")`, run the command (stream stdout/stderr to the CI log, hash the combined output for the digest, capture exit code + duration), build the record, `post` it to `report_build`, print the returned `id`/`head`, write `--out` if set, `return exitCode`. Define `cmdRunner`/`buildPoster` interfaces so the test injects fakes; the real ones are `os/exec` and the MCP client (`dial` mirror). Wire `main.go`: `if len(os.Args) > 1 && os.Args[1] == "certify" { os.Exit(runCertify(os.Args[2:], realRunner{}, realPoster{}, os.Stdout, os.Stderr)) }` after `showHelp`, before server boot.

- [ ] **Step 4: Run it, verify it passes.** `go test ./cmd/corral/ -run TestCertify` → PASS; `go build ./...`.

- [ ] **Step 5: Regenerate CLI docs + commit.**
```bash
bash scripts/gen-cli-docs.sh   # picks up `certify` from corral -h
go test ./... && go vet ./... && bash scripts/check-security.sh
git add cmd/corral/ docs/cli/ site/src/content/docs/docs/cli/
git commit -m "feat(corral): \`corral certify\` — one-line pipeline accountability record"
```

---

## Strengthening pass (Tasks 5–7) — make the persisted record independently verifiable + close whole-branch findings

The whole-branch review found the persisted record is verifiable only in-process at creation
time (steps not stored; only the head is signed, leaving the predicate editable; the verifying
key is unpublished), plus a Critical silent-green CLI bug and an env-var collision. These tasks
close all of it so "independently-verified, tamper-evident" is TRUE for central-trust v1.

### Task 5: `internal/certify` — sign the full canonical statement + `VerifyStatement`

**Files:** Modify `internal/certify/certify.go`, `internal/certify/certify_test.go`

**Interfaces — Produces:**
- `func CanonicalStatement(stmt map[string]any) ([]byte, error)` — deterministic JSON bytes.
  `json.Marshal` IS deterministic here (all values are JSON-native: maps sort keys, slices keep
  order) — document that; do NOT add a dependency.
- `func SignStatement(stmt map[string]any, priv ed25519.PrivateKey) (sigHex string, canonical []byte, err error)` —
  `canonical, err = CanonicalStatement(stmt)`; `sigHex = hex(ed25519.Sign(priv, canonical))`;
  return the canonical bytes so the caller stores EXACTLY what was signed. Returns an error
  (never panics) on marshal failure — also closes Task 1 Minor #1's panic-style concern for this path.
- `func VerifyStatement(canonical []byte, sigHex string, pub ed25519.PublicKey) bool` — decode
  hex (false on error), `ed25519.Verify(pub, canonical, sig)`. Verifies over the STORED bytes,
  sidestepping any float/int re-marshal ambiguity.
- Keep existing `BuildLedger`/`VerifyLedger`/`BuildAttestation`. Keep `Sign`/`VerifySig` (head-level)
  — still used to bind the head inside the statement's subject digest, and harmless to retain.

- [ ] **Step 1: Failing test** in `certify_test.go`: `TestSignVerifyStatement` — build a statement
  via `BuildAttestation`, `sig, canonical, err := SignStatement(stmt, priv)` (err nil), assert
  `VerifyStatement(canonical, sig, pub)` true; mutate one predicate byte (e.g. change an exit code
  in a copy of `canonical`) → `VerifyStatement` false; a wrong-key pub → false. Also
  `TestCanonicalStatementDeterministic`: `CanonicalStatement` of the same map twice is byte-equal.
- [ ] **Step 2: Run it, verify it fails.** `go test ./internal/certify/ -run Statement -v` → FAIL (undefined).
- [ ] **Step 3: Implement** the three functions in `certify.go` per the signatures above.
- [ ] **Step 4: Run it, verify it passes** + full `go test ./internal/certify/` green.
- [ ] **Step 5: Commit.**
```bash
git add internal/certify/
git commit -m "feat(certify): sign the full canonical statement + VerifyStatement (bind the predicate, not just the head)"
```

### Task 6: `buildstore` + `report_build` — persist steps, sign the statement, publish the pubkey

**Files:** Modify `internal/buildstore/store.go` (+ test), `internal/brain/buildcert.go` (+ test),
`internal/brain/server.go` (HTTP route), `internal/brain/identity.go` if needed.

**Interfaces:**
- `buildstore`: add a `steps JSON` column to `build_records`; change
  `Save(repo, commit, branch, actor, head, sig, statementJSON, stepsJSON string) (int64, error)`
  (add `stepsJSON`); `Get` returns the stored `steps` too (add `"steps"` key to the returned map).
  Add `func PublicKey(priv ed25519.PrivateKey) ed25519.PublicKey` helper OR expose via the handler
  (`priv.Public().(ed25519.PublicKey)`).
- `report_build` handler: sign the STATEMENT (`certify.SignStatement(stmt, opts.CertifyKey)` →
  store the returned canonical bytes as the statement column), persist the built `steps`
  (`json.Marshal(built)`), and add `public_key` (hex of `opts.CertifyKey.Public()`) to the tool
  result. (Head-level `Sign` may remain but the stored `signature` is now the STATEMENT signature.)
- `server.go`: add an **unauthenticated** `GET /api/certify/pubkey` route returning the hex certify
  public key as `text/plain` (mirror how `/healthz` is registered). Only mount it when a valid
  `CertifyKey` is configured. This lets an independent verifier fetch the key without credentials.

- [ ] **Step 1: Failing tests.** buildstore: `Save`/`Get` round-trip now carries `steps`; brain:
  extend the `report_build` test to assert (a) `certify.VerifyStatement(storedCanonical, signature, pub)`
  true, (b) `certify.VerifyLedger(storedSteps, head)` true and `statement.subject[0].digest.sha256 == head`,
  (c) the response carries `public_key` matching `priv.Public()`, and a tamper on a stored predicate byte
  makes `VerifyStatement` false. Add a `server_test`-style check that `GET /api/certify/pubkey` returns the key.
- [ ] **Step 2: Run, verify fail.**
- [ ] **Step 3: Implement** the schema/column, the handler changes, and the HTTP route.
- [ ] **Step 4: Run, verify pass** + `go test ./internal/brain/ ./internal/buildstore/` green + `bash scripts/check-security.sh`.
- [ ] **Step 5: Commit.**
```bash
git add internal/buildstore/ internal/brain/
git commit -m "feat(brain): persist ledger steps, sign the statement, publish the certify pubkey (independently verifiable)"
```

### Task 7: CLI fixes (silent-green #1, token-env #3, DRY hoist #5) + verify surface + docs

**Files:** Modify `cmd/corral/main.go`, `cmd/corral/certify.go` (+ test); create
`internal/brainclient/client.go` (+ move `bearer`/`dial`/`firstText`); update `cmd/corral-admin/client.go`
to consume it; regenerate CLI docs.

**Fixes:**
- **#1 (Critical — silent green):** dispatch known subcommands by `os.Args[1]` (`certify`, `secret`)
  BEFORE `showVersion(os.Args[1:])`/`showHelp(os.Args[1:])` run, so a `-v`/`-h`/`version` inside the
  *checked command* (after `--`) can never short-circuit `main()` into printing the version and exiting 0
  without running the check. Add a test: `runCertify` is reached (check runs) even when the argv after `--`
  contains `-v`/`--help`/`version`. (For the dispatch-order itself, a focused test that
  `dispatchSubcommand(os.Args)` returns the certify path for `["corral","certify","--","x","-v"]`.)
- **#3 (Important — env collision):** the CLI bearer token must NOT reuse `CORRALAI_BRAIN_KEY` (documented
  in `main.go:47` as the Ed25519 *identity seed*). Resolve the token from `CORRALAI_BRAIN_TOKEN` (via
  `creds.Get`), and check `cmd/corral-agent`'s existing convention — if the agent also reads
  `CORRALAI_BRAIN_KEY` as a token, note it in the report for a follow-up (do NOT silently diverge; make
  certify use `CORRALAI_BRAIN_TOKEN` and document it in the `main.go` env block + regenerated CLI docs).
- **#5 (DRY — standing directive):** hoist `bearer`/`dial`/`firstText` into a new `internal/brainclient`
  package (exported: `Dial(ctx, brainURL, token) (*Client, error)`, `(*Client) CallTool(...)`,
  `FirstText(res)`); make BOTH `cmd/corral/certify.go` and `cmd/corral-admin/client.go` consume it. One
  copy, both callers. Keep behavior identical; existing corral-admin tests must stay green.
- **#6 (DRY — Task 6 review finding):** hoist the ledger-step (de)serialization into `internal/certify`
  as `func MarshalSteps(steps []Step) ([]byte, error)` and `func UnmarshalSteps(b []byte) ([]Step, error)`
  (they know `Step.Hash` is `json:"-"` and round-trip `Hash`/`Prev` explicitly via an internal storable
  shape). Refactor `internal/brain/buildcert.go` to use `certify.MarshalSteps` (replacing its local
  `storableStep`), and the `verify` subcommand below uses `certify.UnmarshalSteps` — one implementation,
  three callers (brain, CLI, tests) instead of three copies. Keep the brain test green.
- **Complete `--out` record (so verify works offline):** add `steps` (`certify.MarshalSteps(built)`) to the
  `report_build` tool result (one field beside the existing `id/head/signature/statement/public_key`), and have
  `runCertify`'s `--out` write the full self-verifying record `{statement, signature, steps, head, public_key}`
  — so `corral certify verify <that file> --pubkey <hex>` works with no brain round-trip.
- **Verify surface (makes "independently verifiable" tangible):** add `corral certify verify <record-file> [--pubkey <hex>|--brain <url>]` — `<record-file>` is a stored/exported record JSON carrying `{statement (canonical bytes), signature, steps, head}` (the shape `report_build` returns / `--out` writes). It: (1) fetches the pubkey from `GET /api/certify/pubkey` if `--brain` given, else `--pubkey`; (2) `certify.VerifyStatement(canonicalStatement, signature, pub)`; (3) `certify.UnmarshalSteps(steps)` then `certify.VerifyLedger(steps, head)`; (4) confirms `statement.subject[0].digest.sha256 == head`. ALL must pass → exit 0 + print "verified"; any failure → non-0 naming which check failed. This is the full independent-verification path. Test: build an in-process record and assert verify passes, plus a tampered-predicate case and a tampered-step case each failing.

- [ ] **Step 1: Failing tests** for the dispatch-order fix, the token-env resolution, the brainclient
  hoist (a `brainclient` unit test), and `certify verify`.
- [ ] **Step 2: Run, verify fail.**
- [ ] **Step 3: Implement** the dispatch reorder, the `CORRALAI_BRAIN_TOKEN` resolution, the `internal/brainclient`
  extraction (update both callers), and the `verify` subcommand + help text.
- [ ] **Step 4: Run** full `go test ./...`, `go vet ./...`, `go build ./...`, `bash scripts/check-security.sh`,
  and regen CLI docs (`bash scripts/gen-cli-docs.sh`).
- [ ] **Step 5: Commit.**
```bash
git add cmd/ internal/brainclient/ docs/cli/ site/src/content/docs/docs/cli/
git commit -m "fix(corral): subcommand dispatch before -v/-h scan (silent-green), CORRALAI_BRAIN_TOKEN, DRY brainclient, certify verify"
```

---

## Self-Review

**Spec coverage:** the CLI (Task 4) + verify-by-execution (Task 4 runs the check) + attestation/ledger (Task 1) + brain ingest + sign + store (Tasks 2, 3) + exit-passthrough (Task 4) + `--produced-by`/`--out` (Task 4) + recall (`buildstore.Get`, Task 2) all map to tasks. ✓ Signing brain-side only (Tasks 2, 3). ✓

**Placeholder scan:** none — the two `NOTE`s (deterministic map marshaling; mirroring `dial`) point at existing patterns with the code shown.

**Type consistency:** `Step`/`BuildRecord`/`BuildLedger`/`BuildAttestation`/`Sign`/`VerifySig` consistent across Tasks 1, 3. `buildstore.Save/Get/LoadOrCreateSigningKey` consistent across Tasks 2, 3, 4. `runCertify(args, run, post, stdout, stderr) int` consistent in Task 4.

**Out of scope (per spec):** offline artifact mode, DSSE/Sigstore/Rekor, the team dashboard, remote presentation mode, re-verifying a prebuilt external artifact.

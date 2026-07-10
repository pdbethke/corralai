# `corral certify` Trustless Tier (Rekor) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Anchor every certify record to Sigstore Rekor (a public append-only transparency log) so a record is verifiable without trusting corral's own brain — turning the central-trust wedge trustless.

**Architecture:** (1) Upgrade certify's signature to a **DSSE envelope** (the format Rekor accepts). (2) Introduce a mockable **`Witness`** interface (`internal/transparency`) with a real `RekorWitness` and a hermetic `fakeWitness`. (3) `report_build` anchors the DSSE envelope to Rekor after signing and stores the inclusion evidence; graceful if Rekor is down (`anchored=false`). (4) `corral certify verify` verifies the Rekor inclusion proof **offline** against Rekor's TUF-rooted key, rejecting unwitnessed records by default. Everything downstream of the interface tests against `fakeWitness`; the real Rekor path has one env-gated integration test.

**Tech Stack:** Go 1.26.5; `github.com/secure-systems-lab/go-securesystemslib/dsse` (DSSE); Sigstore Rekor client + `github.com/sigstore/sigstore-go` (submission + TUF-rooted inclusion/SET verification); DuckDB (`go-duckdb/v2`); the go-sdk MCP server.

## Global Constraints

- SPDX header `// SPDX-License-Identifier: Elastic-2.0` on every new Go file.
- TDD: failing test first, watch it fail, minimal code, watch it pass, commit.
- `go vet ./...` clean; full `go test ./...` green; `go build ./...`; `bash scripts/check-security.sh` (gosec + govulncheck) green before each commit — new Sigstore deps MUST pass govulncheck (pin versions; annotate justified `#nosec`).
- The signing key never leaves the brain and never appears in a log/error (public keys and the DSSE envelope are fine to expose).
- Trust anchor is external: `verify` gets Rekor's key from the Sigstore **TUF trust root** (or a pinned public-Rekor key), NEVER from the record or circularly from the same instance.
- **Tamper-EVIDENT, never tamper-proof** in any copy.
- Graceful on a Rekor outage — never fail a build because the log was unreachable.
- Corral metaphor in user copy; no bee/hive/swarm.
- Keep the `internal/transparency` wrapper thin so the Rekor client's blast radius stays contained.
- Branch: `feat/certify-trustless-rekor` (spec committed there).

## File Structure

- `internal/certify/certify.go` — replace `SignStatement`/`VerifyStatement` with DSSE `SignDSSE`/`VerifyDSSE`.
- `internal/transparency/witness.go` — `Witness` interface, `Entry`, `RekorWitness`, `fakeWitness` (test helper in `witness_test.go` or an exported `NewFakeWitness` for cross-package tests).
- `internal/buildstore/store.go` — add `rekor JSON` + `anchored BOOLEAN` columns; extend `Save`/`Get`.
- `internal/brain/buildcert.go` + `identity.go` — `Options.Witness`; anchor after signing; store evidence.
- `cmd/corral/verify.go` — verify the inclusion proof; `--allow-unanchored`; DSSE verify.
- `cmd/corral/main.go` — construct a `RekorWitness` and pass it as `Options.Witness`.
- `scripts/demo-certify-trustless.sh` — the reproducible end-to-end demo.

---

### Task 1: DSSE envelope (atomic upgrade, replaces detached signature)

**Files:** Modify `internal/certify/certify.go` (+ test), `internal/brain/buildcert.go` (+ test), `cmd/corral/verify.go` (+ test). Add dep `github.com/secure-systems-lab/go-securesystemslib/dsse`.

**Interfaces — Produces:**
- `func SignDSSE(stmt map[string]any, priv ed25519.PrivateKey, keyID string) (envelope []byte, err error)` — canonicalize the statement (reuse `CanonicalStatement`), sign it as a DSSE envelope with `payloadType = "application/vnd.in-toto+json"`, return the envelope JSON bytes.
- `func VerifyDSSE(envelope []byte, pub ed25519.PublicKey) (stmt map[string]any, ok bool, err error)` — verify the DSSE PAE signature, decode + return the in-toto statement; `ok=false` on any verification failure (never panics).
- Remove `SignStatement` and `VerifyStatement` (and their tests) once callers are migrated in this task.

**Implementation note (DSSE lib):** wrap Ed25519 in the `dsse.SignerVerifier` interface. Confirm the exact method set against the vendored version — it is approximately:
```go
type ed25519SV struct { priv ed25519.PrivateKey; pub ed25519.PublicKey; keyID string }
func (s *ed25519SV) Sign(ctx context.Context, data []byte) ([]byte, error) { return ed25519.Sign(s.priv, data), nil }
func (s *ed25519SV) Verify(ctx context.Context, data, sig []byte) error { if ed25519.Verify(s.pub, data, sig) { return nil }; return errors.New("dsse: bad signature") }
func (s *ed25519SV) KeyID() (string, error) { return s.keyID, nil }
func (s *ed25519SV) Public() crypto.PublicKey { return s.pub }
```
Sign: `es, _ := dsse.NewEnvelopeSigner(sv); env, err := es.SignPayload(ctx, "application/vnd.in-toto+json", canonical); json.Marshal(env)`.
Verify: unmarshal to `*dsse.Envelope`; `ev, _ := dsse.NewEnvelopeVerifier(sv); _, err := ev.Verify(ctx, env)`; then base64-decode `env.Payload` → `json.Unmarshal` → statement.

- [ ] **Step 1: Failing test** `internal/certify/certify_test.go` `TestSignVerifyDSSE`: build a statement via `BuildAttestation`; `env, err := SignDSSE(stmt, priv, "brain")` (err nil); `got, ok, err := VerifyDSSE(env, pub)` → ok true, err nil, `got["subject"]` equals the input's; flip a byte in the base64 payload of `env` → `VerifyDSSE` ok=false; a wrong-key pub → ok=false.
- [ ] **Step 2: Run it, verify it fails** (`go test ./internal/certify/ -run DSSE`).
- [ ] **Step 3: Implement** `SignDSSE`/`VerifyDSSE`; `go get github.com/secure-systems-lab/go-securesystemslib/dsse@latest` then `go mod tidy`.
- [ ] **Step 4: Migrate callers + delete old funcs.** In `internal/brain/buildcert.go:105`, replace `certify.SignStatement(stmt, opts.CertifyKey)` with `env, err := certify.SignDSSE(stmt, opts.CertifyKey, "brain")` and store `string(env)` as the `signature` column (the `statement` column stays the canonical statement for readability; the envelope embeds its own copy). Update `internal/brain/buildcert_test.go` to verify via `certify.VerifyDSSE(env, pub)`. In `cmd/corral/verify.go`, replace `VerifyStatement` with `VerifyDSSE` over the record's `signature` (envelope) field; update `cmd/corral/verify_test.go` fixtures to build DSSE envelopes. Delete `SignStatement`/`VerifyStatement`.
- [ ] **Step 5: Run** `go test ./... && go vet ./... && go build ./... && bash scripts/check-security.sh` — all green.
- [ ] **Step 6: Commit.**
```bash
git add internal/certify/ internal/brain/ cmd/corral/ go.mod go.sum
git commit -m "feat(certify): DSSE-wrap the attestation (replaces detached signature; Rekor-ready)"
```

---

### Task 2: `internal/transparency` — the witness interface, fake, and RekorWitness

**Files:** Create `internal/transparency/witness.go`, `internal/transparency/witness_test.go`. Add Sigstore deps.

**Interfaces — Produces:**
```go
type Entry struct {
    LogIndex       int64
    LogID          string
    IntegratedTime int64
    InclusionProof []byte // opaque; the witness impl knows how to verify it
    SET            []byte // signed entry timestamp
    Body           []byte // the canonicalized log entry body (for match checks)
}
type Witness interface {
    Anchor(ctx context.Context, dsseEnvelope []byte) (Entry, error)
    VerifyInclusion(entry Entry, dsseEnvelope []byte) (ok bool, detail string)
}
func NewRekorWitness(rekorURL string) (Witness, error) // real; TUF-rooted verification
func NewFakeWitness() Witness                           // hermetic, deterministic
```
- `RekorWitness.Anchor` submits a `dsse`-type entry to `rekorURL` (default caller passes `https://rekor.sigstore.dev`) and returns the log entry's index/proof/SET.
- `RekorWitness.VerifyInclusion` verifies the inclusion proof + SET **offline** against Rekor's public key obtained from the **Sigstore TUF trust root** (via `sigstore-go`), and confirms the entry body wraps `dsseEnvelope`. Returns `(false, reason)` on any mismatch.
- `fakeWitness` — an in-memory log: `Anchor` records the envelope and returns an `Entry` whose `InclusionProof`/`Body` are a deterministic function of the envelope (e.g. `sha256(envelope)`); `VerifyInclusion` recomputes and compares. This tests the WIRING (verify calls the witness; pass on match, fail on tamper) deterministically without network. NOT a real Merkle proof — real Rekor verification is covered by the integration test below.

- [ ] **Step 1: Failing test** `witness_test.go` `TestFakeWitnessRoundTrip`: `w := NewFakeWitness()`; `e, err := w.Anchor(ctx, env)` (err nil, `e.LogIndex >= 0`); `ok, _ := w.VerifyInclusion(e, env)` true; `ok2, _ := w.VerifyInclusion(e, tamperedEnv)` false; a corrupted `e.InclusionProof` → false.
- [ ] **Step 2: Run it, verify it fails.**
- [ ] **Step 3: Implement `fakeWitness` + the interface/types.** (Get the hermetic path green first — this is what all downstream tasks test against.)
- [ ] **Step 4: Run it, verify it passes.**
- [ ] **Step 5: Implement `RekorWitness`.** Add deps (`go get github.com/sigstore/rekor@latest github.com/sigstore/sigstore-go@latest`; `go mod tidy`). Keep the wrapper thin: a submit path (Rekor `dsse` entry) and an offline `VerifyInclusion` using `sigstore-go`'s verifier with the TUF trust root. Confirm exact APIs against the vendored versions. If the Sigstore API proves materially different from this sketch or blocks progress, STOP and report BLOCKED with specifics rather than guessing — do not fake a real proof.
- [ ] **Step 6: Env-gated integration test** `TestRekorWitnessIntegration`: skip unless `CORRALAI_REKOR_INTEGRATION=1` (so CI stays hermetic); when set, `NewRekorWitness("https://rekor.sigstore.dev")`, `Anchor` a real DSSE envelope, then `VerifyInclusion` passes and a tampered envelope fails. Document the env var.
- [ ] **Step 7: Security pass + commit.**
```bash
go vet ./... && go test ./internal/transparency/ && bash scripts/check-security.sh
git add internal/transparency/ go.mod go.sum
git commit -m "feat(transparency): Witness interface + fakeWitness + Rekor-backed RekorWitness (offline inclusion verify)"
```

---

### Task 3: `report_build` anchors to the witness; `build_records` stores the evidence

**Files:** Modify `internal/buildstore/store.go` (+ test), `internal/brain/buildcert.go` (+ test), `internal/brain/identity.go`.

**Interfaces:**
- Consumes: `internal/transparency` (`Witness`, `Entry`), `internal/certify` (`SignDSSE`).
- `buildstore`: add `rekor JSON` + `anchored BOOLEAN` columns (mirror the existing `ALTER TABLE build_records ADD COLUMN IF NOT EXISTS …` idempotent pattern at store.go:55). `Save(repo, commit, branch, actor, head, sig, statementJSON, stepsJSON, rekorJSON string, anchored bool) (int64, error)`; `Get` returns `rekor` + `anchored`.
- `Options.Witness transparency.Witness` (identity.go) — nil ⇒ anchoring disabled (records saved `anchored=false`).
- `report_build`: after `SignDSSE`, if `opts.Witness != nil` call `opts.Witness.Anchor(ctx, env)`. On success store the `Entry` JSON + `anchored=true` and return `log_index` + `anchored=true` in the result. **On Anchor error: log a warning (never the key), store `anchored=false`, and STILL succeed** (degrade-don't-deadlock). Result carries `anchored` bool.

- [ ] **Step 1: Failing tests.** buildstore: `Save`/`Get` round-trip carrying `rekor`+`anchored`. brain (`fakeWitness`): `report_build` with a witness → response `anchored=true` + a `log_index`; the stored record has `anchored=true` and a non-empty `rekor`; then a witness whose `Anchor` returns an error → the record is still saved with `anchored=false` and the tool call does NOT fail.
- [ ] **Step 2: Run, verify fail.**
- [ ] **Step 3: Implement** the columns, `Save`/`Get`, `Options.Witness`, and the anchor-after-sign + graceful-degradation logic. Add a small error-injecting fake (or extend `NewFakeWitness` with a failing variant) for the outage test.
- [ ] **Step 4: Run** `go test ./internal/brain/ ./internal/buildstore/` + `go vet` + `bash scripts/check-security.sh` green.
- [ ] **Step 5: Commit.**
```bash
git add internal/buildstore/ internal/brain/
git commit -m "feat(brain): report_build anchors the DSSE attestation to the witness; store Rekor evidence, graceful on outage"
```

---

### Task 4: `certify verify` checks the witness + wiring + demo + docs

**Files:** Modify `cmd/corral/verify.go` (+ test), `cmd/corral/certify.go` (the `--out` record shape), `cmd/corral/main.go` (wire `RekorWitness`). Create `scripts/demo-certify-trustless.sh`. Regenerate CLI docs.

**Interfaces:**
- Consumes: `internal/transparency` (`NewRekorWitness`/`Witness`), `internal/certify` (`VerifyDSSE`, `UnmarshalSteps`, `VerifyLedger`).
- `verify`: after the DSSE-signature + ledger + subject-digest checks, if the record is `anchored`, build a `RekorWitness` (from `--brain`'s configured rekor, or `--rekor-url`, default `https://rekor.sigstore.dev`) and call `VerifyInclusion(entry, envelope)`. If `anchored=false`, print `signed, NOT publicly witnessed` and exit non-zero UNLESS `--allow-unanchored`. All applicable checks pass → `verified (publicly witnessed <RFC3339 time>, Rekor #<index>)`.
- `--out` record now includes `rekor` + `anchored` (so offline `verify` has the inclusion evidence).
- `main.go`: construct `NewRekorWitness(env("CORRALAI_REKOR_URL","https://rekor.sigstore.dev"))` and set it on `Options.Witness` (alongside `BuildStore`+`CertifyKey`). On witness-construction error, log loudly and leave anchoring off (records `anchored=false`) — never crash the brain.

- [ ] **Step 1: Failing tests** `cmd/corral/verify_test.go` (with `fakeWitness` via a `--rekor` seam or an injected witness): a fully-anchored good record → exit 0, output names the Rekor index; a record whose inclusion proof is tampered → non-zero at the Rekor step; an `anchored=false` record → non-zero without `--allow-unanchored`, exit 0 with it.
- [ ] **Step 2: Run, verify fail.**
- [ ] **Step 3: Implement** the verify witness step, `--allow-unanchored`, the `--out` shape, and the `main.go` wiring. Make the witness injectable in `runCertifyVerify` (like `run`/`post`) so the test uses `fakeWitness`.
- [ ] **Step 4: Demo script** `scripts/demo-certify-trustless.sh` — a documented, reproducible sequence: start/point at a brain configured with a build DB + key, `corral certify -- <passing check>` (anchors to Rekor), `corral certify verify <record> --brain <url>` (passes, shows Rekor index+time), then a `sed`-tamper of the record and a re-verify that FAILS naming the broken link. Comment each step (it becomes the article's tape).
- [ ] **Step 5: Run full suite + regen docs + commit.**
```bash
go test ./... && go vet ./... && go build ./... && bash scripts/check-security.sh
bash scripts/gen-cli-docs.sh
git add cmd/corral/ scripts/demo-certify-trustless.sh docs/cli/ site/src/content/docs/docs/cli/
git commit -m "feat(corral): certify verify checks the Rekor inclusion proof; --allow-unanchored; wire RekorWitness; demo script"
```

---

## Self-Review

**Spec coverage:** DSSE (Task 1) · Witness interface + Rekor + fake (Task 2) · anchor-after-sign + evidence storage + graceful outage (Task 3) · verify-checks-inclusion + unanchored policy + wiring + demo (Task 4). Trust-source = TUF root (Task 2). All map to tasks. ✓

**Placeholder scan:** none — the DSSE signer sketch and the `Witness` interface are concrete; the RekorWitness impl is behavior-specified with an explicit "report BLOCKED if the library differs" guard rather than a hand-wave, plus an integration test as its acceptance.

**Type consistency:** `SignDSSE`/`VerifyDSSE` (Tasks 1, 3, 4); `Witness`/`Entry`/`NewRekorWitness`/`NewFakeWitness` (Tasks 2, 3, 4); `buildstore.Save(...rekorJSON, anchored)` / `Get` (Tasks 3, 4); `Options.Witness` (Tasks 3, 4). Consistent.

**Out of scope (per spec — v2):** keyless/Fulcio, private-Rekor air-gap productization, alternate/secondary witnesses, batched/deferred anchoring, re-verifying a prebuilt external artifact.

**Risk note:** Task 2's `RekorWitness` is the one library-dependent, network-touching unit — isolated behind `Witness` so Tasks 3–4 test hermetically against `fakeWitness`; its real behavior is pinned by the env-gated integration test, and the task says report BLOCKED (don't fake a proof) if the Sigstore API diverges.

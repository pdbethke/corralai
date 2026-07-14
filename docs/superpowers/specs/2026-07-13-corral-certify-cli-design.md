<!-- SPDX-License-Identifier: Elastic-2.0 -->
# `corral certify <change>` — the standalone certification CLI (slice 2)

**Status:** design (2026-07-13). Precedes an implementation plan. Second slice of the audit re-focus (`docs/superpowers/specs/2026-07-13-corral-refocus-audit-not-builder-design.md`); the first slice retired the builder.

## Goal

Make `corral certify` a **standalone, server-optional command that certifies a code change by execution**: check out an exact commit into a jail, run the change's declared checks there, and emit a **signed, offline-verifiable record** — no brain required. This is the CLI-first atom the re-focus spec calls for (`corral certify <change>`); the control-owner tests and the adversarial-verification herd compose onto it in later slices.

## The tell / why this shape

The re-focus made corral "a true audit for software change," CLI-first: the atom is a command, not a server. The signing/jail/ledger machinery already exists and is battle-tested (it powers the merge gate). What's missing is a single command a developer or CI step can run to get a signed "these checks passed on exactly this commit, in a jail" record they can hand to anyone who can verify it offline. That command is the adoption wedge — trivially runnable, no daemon, no forge.

## EXISTS vs DELTA (build only the delta)

**Reused as-is (do not rebuild):**
- `internal/certify` — `BuildLedger`, `BuildAttestation`, `CanonicalStatement`, `SignDSSE`, `MarshalSteps` (in-toto/SLSA statement + hash-chained step ledger + DSSE Ed25519 signature). Pure, no storage.
- `buildstore.LoadOrCreateSigningKey(path)` — local signing key: env `CORRALAI_CERTIFY_KEY` (hex seed) overrides, else a 0600 seed file (default `~/.claude/corralai_certify_key`).
- `internal/certverify.VerifyRecord` + `internal/transparency` (Rekor witness) — the verify path; `corral certify verify` already works.
- `internal/sandbox` — `Resolve(Config)` → `Isolator`, `RunGuarded(ctx, command, Options)` → `Result{ExitCode, Output, TimedOut, Err}`. The one true "failed run ≠ success" home. Backends: bwrap (linux), sandbox-exec (darwin), container, none (opt-in unsafe).
- `cmd/corral/certify.go` existing scaffolding: `splitCertifyArgs` (`--` split), git-context capture (`repo`/`commit`/`branch`/message/author + `git verify-commit` signature parse), the `--out` record writer, and the `certRecord` on-disk JSON schema.

**New (the delta this slice builds):**
1. A `git archive <ref>` → fresh temp workspace helper (the isolated clean tree the checks run against).
2. Jail-wire the check run: replace the current plain `os/exec` in the certify path with `sandbox.Resolve` + `sandbox.RunGuarded`.
3. A local-sign path in the CLI: build ledger/attestation and `certify.SignDSSE` with the local key, so `--brain` becomes **optional** (its absence = sign locally, no longer an error).
4. New CLI surface: a `<ref>` positional (default `HEAD`), `--net`/`--no-net`, `--local-sign`, and a `corral certify pubkey` helper to print the local signing pubkey (so the offline verify loop closes without a brain).

## Command shape

```
corral certify [<ref>] [flags] -- <check-cmd>...
  <ref>            git ref/commit to certify           (default: HEAD)
  --brain <url>    ALSO post the record to a brain      (optional; was required)
  --local-sign     sign with the local key             (default when --brain absent)
  --out <path>     write the signed record JSON         (default: ./certify-<shortsha>.json)
  --net / --no-net allow network inside the jail        (default: --net on)
  --rekor-url <u>  anchor in a transparency log         (default: off → unanchored)
  --produced-by x  comma-separated model/agent names    (existing flag, carried)

corral certify verify <record> [--pubkey <hex> | --brain <url>] [--allow-unanchored]   (exists, unchanged)
corral certify pubkey                                   (new: prints the local signing pubkey hex)
```

Backward-compatible: callers passing `--brain` keep the existing post-to-brain behavior. The only behavior change is that omitting `--brain` now signs locally instead of erroring.

## The flow

1. **Resolve the change.** `<ref>` (default HEAD) → commit SHA via `git rev-parse`; capture repo/branch/message/author/date and the commit-signature status (existing `captureCommitSignature`). Fail closed if the ref doesn't resolve.
2. **Materialize a clean tree.** `git archive <sha>` piped into a fresh `os.MkdirTemp` workspace (uncommitted working-tree edits are excluded by construction — the subject is the *commit*). Clean up the temp dir on exit.
3. **Run the checks in the jail.** `sandbox.Resolve(Config{Backend, UnsafeHost})` then `sandbox.RunGuarded(ctx, <check-cmd>, Options{Workspace: tmp, Timeout: DefaultGateTimeout, Network: net, Backend: isolator})`. Capture exit code + a sha256 digest of combined output (not raw output). A jail/setup error fails closed (exit 1, no signed green).
4. **Build the attestation.** `certify.BuildLedger(steps)` → head; `certify.BuildAttestation(BuildRecord{Repo, Commit, Branch, Command, ExitCode, DurationS, OutputDigest, ProducedBy, …}, head)`. Subject digest = the ledger head; byproducts carry `{command, exitCode, passed, durationS, outputDigest}`.
5. **Sign.** Local: `buildstore.LoadOrCreateSigningKey(keyPath)` → `certify.SignDSSE(stmt, priv, keyID)`. Optionally anchor via `--rekor-url` (`transparency.NewRekorWitness`). Write the `certRecord` JSON to `--out`. If `--brain` is set, ALSO POST via the existing `report_build` MCP path.
6. **Exit** with the check's real exit code. Certify never flips it; a failed sign/post is reported but does not mask the check result (matches today's behavior).

## The record (unchanged schema; digest-only payload)

The existing `certRecord`: `{statement, signature (DSSE envelope), steps (full ledger), head, public_key (hint), rekor, anchored}`. The statement is in-toto/SLSA with the commit as subject and `{command, exitCode, passed, durationS, outputDigest}` as byproducts. **Raw check output is never stored — only its sha256 digest.** Verify offline with `corral certify verify <record> --pubkey <hex>` (get the hex from `corral certify pubkey`), or against a brain's published pubkey.

## Decisions (locked in brainstorming)

- **Change model:** clean checkout of a git ref (default HEAD) via `git archive`, run in a fresh jail workspace. Reproducible; subject = the commit. (Not the in-place working tree; not a PR — `--pr` is a later slice.)
- **Server-optional:** local signing is the default; `--brain` is optional (post additionally). No daemon needed to certify or to verify.
- **Network default: on.** A clean `go test` checkout typically needs the network for modules; `--no-net` locks it down. Rationale: you're certifying your own change on your own machine; usability wins, with an explicit stricter opt-out. (Revisit per-language module-cache mounting later if net-off becomes the common ask.)
- **Anchoring default: off.** Records are unanchored unless `--rekor-url` is given; `corral certify verify` needs `--allow-unanchored` for them (existing behavior).

## Non-goals (this slice)

- **Control-owner vetted tests** (`controlgate.RunControlGate` / `controlspec`) — next slice; composes as an added phase/flag.
- **The staffed adversarial-verification herd** (`mission.Staff` + `testgen`/`authoring`/`adequacy` write→review→mutate→pentest) — the largest later slice.
- **Scrub.** Deliberately deferred: this record stores an output *digest*, not raw output, plus ordinary repo/commit/branch/author metadata — not secret for a normal repo, so a Go scrub library would be building ahead of need. Scrub earns its place when we start capturing raw logs/artifacts. (If a record ever embeds raw output, scrub becomes a prerequisite, not an add-on.)
- **PR / forge mode** (`--pr N`) — later slice; reuses `gate.Runner`'s checkout but reintroduces the forge+token dependency the CLI-first atom avoids.
- **No new metaphor/rename work**; no changes to the merge gate or control gate.

## Edge cases / risks to handle in the plan

- **Module deps under `--net` off:** `go test` on a fresh archive with no network fails to resolve modules. In-scope handling: `--net` default on covers the common case; document that `--no-net` requires a self-contained/vendored tree. (A module-cache bind-mount is a possible later refinement, out of scope here.)
- **`git archive` on a dirty repo / detached ref:** archive is commit-based, so dirty state is correctly ignored; verify the ref resolves and error clearly if not a git repo.
- **Workspace lifecycle:** create under `os.MkdirTemp`, always clean up (defer), never leave the extracted tree behind.
- **Jail backend absence:** if `sandbox.Resolve` can't provide a real backend (e.g. bwrap unavailable and `AGENT_EXEC_UNSAFE_HOST` not set), fail closed with a clear message — never silently run unsandboxed.
- **Key bootstrap:** first run creates the 0600 key file; `corral certify pubkey` and the sign path must agree on the same key path resolution.
- **Exit-code fidelity:** preserve today's contract — the wrapped check's exit code is the command's exit code; sign/post failures are reported but do not flip a pass to a fail or vice-versa.

## Testing posture

- Unit: the `git archive` → workspace helper (a repo fixture, assert the extracted tree matches the commit and excludes uncommitted edits); the jail-run wiring (fake `Isolator`, assert exit-code/digest capture and fail-closed on backend error); the local-sign path (round-trip `SignDSSE` → `certverify.VerifyRecord` with the local pubkey); `corral certify pubkey` matches the key used to sign.
- Integration (hermetic, no network): `corral certify HEAD -- <trivial passing cmd>` on a fixture repo → record written → `corral certify verify --pubkey <pubkey> --allow-unanchored` passes; a failing check yields a record whose byproducts say `passed:false` and the command exit code propagates.
- Keep the existing certify/verify tests green; the `--brain` path is unchanged.

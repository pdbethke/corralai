<!-- SPDX-License-Identifier: Elastic-2.0 -->
# Credential keystore — design

**Date:** 2026-07-09
**Status:** approved (approach A) → spec for review
**Goal:** Ship an embedded, portable, secure credential subsystem in the corralai binaries so any operator can store provider API keys (OpenAI, Gemini, Anthropic, OpenRouter, …) and the brain worker token once, and have the runtime discover + load them — the corralai analog of GCP Application Default Credentials.

## Problem

corralai is open source; an operator running their own brain + workers needs to
configure remote model keys. Today secret handling is ad-hoc env vars plus one
Linux-only `systemd-creds` path (the brain's delegation secret). That is neither
portable (macOS/Windows/Docker) nor easy. We want "configure a key once, securely,
anywhere" with a small library shipped in the binary — **not** keys embedded in
the binary (the binary is not a secret; it can't be rotated).

## Approach (chosen: A — layered resolver + `corral secret` CLI)

A new `internal/creds` package resolves a named secret through an ordered backend
chain, first hit wins:

1. **Environment variable** — the canonical name (e.g. `OPENAI_API_KEY`). 12-factor,
   CI, containers. Always overrides.
2. **OS keyring** — `github.com/zalando/go-keyring` (macOS Keychain, Windows
   Credential Manager, Linux Secret Service/libsecret). Secure, no master key to
   manage. The desktop path.
3. **Encrypted file** — an `filippo.io/age`-encrypted store at a standard path,
   for headless/servers where no keyring daemon exists. Never plaintext at rest.

A `corral secret set|get|list|rm` CLI populates the keyring (preferred) or the
encrypted file. Runtime callers use `creds.Get(name)` and never see the backend.

Rejected: OS-keyring-only (breaks on headless Linux — the main server case);
encrypted-file-only (forces a master passphrase even on desktops that have a
keyring). A is desktop + server + container unified behind one resolver, exactly
how `gh`/`aws`/`gcloud` do it.

## Security requirements (the load-bearing constraint)

1. **No plaintext secret at rest in the file backend.** The store is age-encrypted;
   only ciphertext touches disk. File mode `0600`, parent dir `0700`.
2. **Master-key (age identity) protection — the crux.** The file is encrypted to an
   age identity (X25519). That identity is protected, in order:
   - **OS keyring** entry (`corral/age-identity`) where a keyring exists — the file
     is useless without keyring access. Desktop default.
   - **A key file** (`0600`) or **env** (`CORRAL_AGE_IDENTITY`) on headless hosts.
     Documented as the SSH-private-key-grade trust boundary (a `0600` key protecting
     the store).
   - **systemd credential** (`age-identity`) on our systemd deploys — so
     `systemd-creds` protects ONE master, and all provider keys live in the portable
     encrypted file. (systemd-creds is now just an identity source, not the store.)
   A passphrase-derived identity (age scrypt) is offered for interactive desktops
   but NOT used for auto-starting services (no interactive unlock).
3. **OS keyring uses OS-native encryption + access control** — we store secrets
   there directly (service `corral`, key = the secret name); the OS owns at-rest.
4. **Never log secret values.** All log/error paths redact; a `Redact(s)` helper
   returns a fingerprint (first 4 + length), never the value. Tests assert no raw
   secret in output.
5. **Set via stdin/prompt, never argv.** `corral secret set NAME` reads the value
   from a TTY prompt or piped stdin — never a positional arg (which leaks in `ps`,
   shell history, and process listings). `--stdin` explicit for scripting.
6. **Scrub env after read.** When a secret is sourced from an env var, the process
   unsets it after first read (mirrors the brain's existing `scrubSecrets`), so it
   doesn't leak to spawned child processes' environments or an accidental `/proc`
   dump. (Value stays in memory for use; env is cleared.)
7. **No secret in the binary, no secret in the repo.** The store lives under the
   user's config dir (`$XDG_CONFIG_HOME/corral` / `~/.config/corral`, override
   `CORRAL_CREDS_DIR`); `.gitignore` covers it in dev.

## Design

### Package `internal/creds`

```go
// Store resolves and persists named secrets across a backend chain.
type Store struct { /* configured backend chain */ }

// Open builds the default chain: env → keyring → age file, honoring
// CORRAL_CREDS_DIR / CORRAL_AGE_IDENTITY. Never returns an error for a missing
// store (an unconfigured operator just has no secrets yet).
func Open() (*Store, error)

// Get returns the secret for name (canonical env var name, e.g. "OPENAI_API_KEY"),
// resolved through the chain; ("", false, nil) when unset anywhere.
func (s *Store) Get(name string) (value string, found bool, err error)

// Set writes a secret to the writable backend (keyring if available, else the age
// file). Overwrites (rotation). Never echoes the value.
func (s *Store) Set(name, value string) error

// List returns the NAMES held (never values); Remove deletes one.
func (s *Store) List() ([]string, error)
func (s *Store) Remove(name string) error
```

Backends implement a small interface (`get/set/list/remove`); the chain tries each
for `Get`, writes to the first writable for `Set`. `Redact(s string) string` lives
here for consistent fingerprinting.

### CLI `corral secret`

`corral secret set <NAME>` (prompt/stdin), `get <NAME>` (prints value — for
scripting; warns on a TTY), `list`, `rm <NAME>`. Names are the canonical env names
(`OPENAI_API_KEY`, `GEMINI_API_KEY`, `ANTHROPIC_API_KEY`, `OPENROUTER_API_KEY`,
`CORRALAI_BRAIN_KEY`). Free-form names allowed.

### Integration

- `corral-agent`: replace direct `os.Getenv("OPENAI_API_KEY")` /
  `CORRALAI_BRAIN_KEY` reads (`cmd/corral-agent/backend.go`, `main.go:1088/1250`)
  with `creds.Get(...)` — env still wins, so nothing breaks; keyring/file now also
  work. `OPENAI_BASE_URL` etc. stay plain env (not secrets).
- Optionally the brain later (out of scope v1).

### Dependencies (new, both pure-Go, minimal)

- `github.com/zalando/go-keyring` — cross-platform keyring.
- `filippo.io/age` — file encryption (X25519 / scrypt).
Both are widely used, pure Go, no CGO — consistent with corralai's one-binary,
CGO-free posture.

## Testing

- `internal/creds` unit tests: env precedence; round-trip through an age file with
  a test identity in a temp dir; `Redact` never leaks; `Set`/`List`/`Remove`;
  missing-store = no error; env-scrub after read. The keyring backend is exercised
  via a fake keyring (go-keyring's `MockInit()`), never the real OS store in CI.
- `corral secret` CLI: set-via-stdin round-trips; `get` on a TTY warns; value never
  appears in `set`/`list` output (assert redaction).
- `corral-agent` integration: a key set in a temp age file is picked up by
  `creds.Get` when the env var is unset; env var still overrides.

## Out of scope (future)

- Cloud secret managers (Vault, GCP/AWS Secret Manager) as pluggable backends.
- Brain-side adoption for its own secrets.
- A GUI/cockpit secret editor.
- Per-tenant / multi-profile stores.

## Decisions (defaulted; revisit if wrong)

- Backend order **env → keyring → age file** (env always overrides — 12-factor).
- Writable target for `Set`: **keyring if present, else age file** (best-available).
- Secret **names are canonical env var names** so env override is transparent.
- The age file's identity is protected by **keyring → key file/env → systemd-cred**,
  never a plaintext identity beside the store.

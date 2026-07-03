# Governed Cross-Swarm Coordination (v1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Cryptographically attested brain identity, then a MotherDuck-mediated signed coordination plane whose first capability is cross-swarm work-claim observation (dedup) — advisory, observe-don't-coerce, fail-closed.

**Architecture:** T1 builds the pure `internal/attest` crypto + trust core (Ed25519 sign/verify + TOFU/allowlist trust decisions over a store interface). T2 wires `fleet_brains`/`fleet_intents` in MotherDuck (register/publish/query, signed, cursor-safe retention). T3 wires the brain keypair + the `fleet_claims` dedup tool + rate limit + startup registration.

**Tech Stack:** Go 1.26; `crypto/ed25519` (stdlib); DuckDB + MotherDuck (CGO); `internal/{attest,fleet,brain}`, `cmd/corral`.

## Global Constraints (bind every task)

- **Fail-closed everywhere.** Any verification doubt (bad/absent signature, stale `ts`, reused nonce, unregistered/ conflicted brain, missing key, no MotherDuck) → the intent is IGNORED and the brain behaves as a standalone swarm. Coordination NEVER breaks normal operation.
- **Observe-don't-coerce.** A brain only publishes its OWN signed intents and reads VERIFIED peer intents. Nothing lets one brain call, control, block, or command another. The dedup check is advisory (surfaces/annotates), never a hard block.
- **The private key never leaves the brain, is never synced, is scrubbed from env after load** (the #21 process-env-scrub pattern). Only the PUBLIC key crosses to MotherDuck.
- **Only verified + non-expired + registry-trusted intents are ever considered.** Everything else is ignored.
- **Replay defense:** signature freshness (`|now−ts| ≤ skew`) in `attest`, AND nonce non-reuse enforced by a `(brain_id, nonce)` uniqueness at the `fleet_intents` storage layer.
- **TOFU trust:** first pubkey per `brain_id` is pinned; a later DIFFERENT pubkey is REFUSED (not overwritten) + surfaced as a tamper alert. Optional `CORRALAI_BRAIN_PEERS` allowlist overrides TOFU when set.
- **Canonical signing encoding is unambiguous** (length-prefixed fields + a domain-separation tag) so field-splitting attacks are impossible.
- `go build ./...` + `go test ./...` stay green each task. CGO.

---

## File Structure

- `internal/attest/attest.go` (create) — keypair load/generate, `Sign`, `Verify` (sig+freshness), canonical encoding, `Register` trust decision (TOFU/conflict/allowlist) over a `BrainStore` interface.
- `internal/attest/attest_test.go` (create).
- `internal/fleet/coord.go` (create) — `fleet_brains`/`fleet_intents` DDL + register/publish/release/query, backed by the in-mem-DuckDB-ATTACH-`md:` pattern; `fleet_intents` retention entry.
- `internal/fleet/coord_test.go` (create).
- `internal/brain/fleetclaims.go` (create) — the `fleet_claims` tool + per-brain publish rate limit; optional pre-mission check.
- `cmd/corral/main.go` (modify) — load/generate/scrub the brain key; register at startup; wire coordination into the fleet ticker + brain Options.

---

## Task 1: `internal/attest` — the crypto + trust core (pure, opus-reviewed)

**Files:** Create `internal/attest/attest.go`, `internal/attest/attest_test.go`

**Interfaces produced:**
- `type KeyPair struct{ Priv ed25519.PrivateKey; Pub ed25519.PublicKey }`
- `func LoadOrCreateKey(seedB64, keyFile string) (KeyPair, error)` — env seed → file → generate+persist (0600).
- `func PubB64(pub ed25519.PublicKey) string` / `func Sign(kp KeyPair, brainID, kind, subject string, ts float64, nonce string) string`
- `type BrainStore interface{ Get(brainID string) (pubB64 string, ok bool); Put(brainID, pubB64 string) error }`
- `func Register(store BrainStore, brainID, pubB64 string, allowlist map[string]string) (Outcome, error)` — TOFU/conflict/allowlist.
- `func Verify(pubB64, brainID, kind, subject string, ts float64, nonce, sig string, now float64, skew float64) error`

- [ ] **Step 1: Write failing tests**

```go
// internal/attest/attest_test.go
package attest

import (
	"strings"
	"testing"
)

func mkKP(t *testing.T) KeyPair {
	t.Helper()
	kp, err := LoadOrCreateKey("", t.TempDir()+"/k") // generate+persist
	if err != nil { t.Fatal(err) }
	return kp
}

func TestSignVerifyRoundTrip(t *testing.T) {
	kp := mkKP(t)
	ts, nonce := 1000.0, "n1"
	sig := Sign(kp, "brainA", "claim", "repoX", ts, nonce)
	if err := Verify(PubB64(kp.Pub), "brainA", "claim", "repoX", ts, nonce, sig, 1000.0, 300); err != nil {
		t.Fatalf("valid sig should verify: %v", err)
	}
}

func TestVerifyRejectsTamper(t *testing.T) {
	kp := mkKP(t)
	sig := Sign(kp, "brainA", "claim", "repoX", 1000, "n1")
	pub := PubB64(kp.Pub)
	// tamper each field → must fail
	for _, c := range []struct{ b, k, s string; ts float64; n string }{
		{"brainB", "claim", "repoX", 1000, "n1"},  // brain
		{"brainA", "release", "repoX", 1000, "n1"}, // kind
		{"brainA", "claim", "repoY", 1000, "n1"},   // subject
		{"brainA", "claim", "repoX", 1001, "n1"},   // ts (also fails freshness below)
		{"brainA", "claim", "repoX", 1000, "n2"},   // nonce
	} {
		if err := Verify(pub, c.b, c.k, c.s, c.ts, c.n, sig, 1000, 300); err == nil {
			t.Fatalf("tampered field must fail: %+v", c)
		}
	}
}

func TestVerifyFreshness(t *testing.T) {
	kp := mkKP(t)
	sig := Sign(kp, "a", "claim", "x", 1000, "n")
	if err := Verify(PubB64(kp.Pub), "a", "claim", "x", 1000, "n", sig, 2000, 300); err == nil {
		t.Fatal("stale ts (|now-ts|>skew) must fail")
	}
}

func TestVerifyWrongKeyFails(t *testing.T) {
	a, b := mkKP(t), mkKP(t)
	sig := Sign(a, "brainA", "claim", "x", 1000, "n") // signed by A
	// verify against B's pubkey (impersonation) → fail
	if err := Verify(PubB64(b.Pub), "brainA", "claim", "x", 1000, "n", sig, 1000, 300); err == nil {
		t.Fatal("sig verified against the wrong pubkey must fail")
	}
}

// fakeStore for Register tests
type fakeStore map[string]string
func (f fakeStore) Get(b string) (string, bool) { v, ok := f[b]; return v, ok }
func (f fakeStore) Put(b, p string) error       { f[b] = p; return nil }

func TestRegisterTOFUPinAndConflict(t *testing.T) {
	s := fakeStore{}
	a, b := mkKP(t), mkKP(t)
	if o, _ := Register(s, "X", PubB64(a.Pub), nil); o != Registered {
		t.Fatalf("first registration should pin, got %v", o)
	}
	if o, _ := Register(s, "X", PubB64(a.Pub), nil); o != AlreadyTrusted {
		t.Fatalf("same pubkey re-register should be AlreadyTrusted, got %v", o)
	}
	if o, _ := Register(s, "X", PubB64(b.Pub), nil); o != Conflict {
		t.Fatalf("DIFFERENT pubkey for pinned brain must be Conflict (refused), got %v", o)
	}
	if v, _ := s.Get("X"); v != PubB64(a.Pub) {
		t.Fatal("conflict must NOT overwrite the pinned pubkey")
	}
}

func TestRegisterAllowlist(t *testing.T) {
	s := fakeStore{}
	a, b := mkKP(t), mkKP(t)
	allow := map[string]string{"X": PubB64(a.Pub)}
	if o, _ := Register(s, "X", PubB64(a.Pub), allow); o != Registered && o != AlreadyTrusted {
		t.Fatal("allowlisted pubkey should be accepted")
	}
	if o, _ := Register(s, "X", PubB64(b.Pub), allow); o != Rejected {
		t.Fatal("non-allowlisted pubkey must be Rejected when allowlist is set")
	}
	if o, _ := Register(s, "Y", PubB64(a.Pub), allow); o != Rejected {
		t.Fatal("unlisted brain must be Rejected when allowlist is set")
	}
}
```

- [ ] **Step 2: Run red.**

- [ ] **Step 3: Implement `attest.go`**

```go
// Package attest provides cryptographic brain identity for cross-swarm coordination:
// an Ed25519 keypair per brain, signed intents, and a TOFU/allowlist trust registry.
// Verification is fail-closed — any error means "do not trust this intent."
package attest

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strconv"
)

type KeyPair struct {
	Priv ed25519.PrivateKey
	Pub  ed25519.PublicKey
}

// LoadOrCreateKey: base64 seed (env) → key file → generate + persist (0600). The seed is the
// 32-byte Ed25519 seed. Persist as base64 in keyFile.
func LoadOrCreateKey(seedB64, keyFile string) (KeyPair, error) {
	if seedB64 == "" && keyFile != "" {
		if b, err := os.ReadFile(keyFile); err == nil {
			seedB64 = string(b)
		}
	}
	if seedB64 != "" {
		seed, err := base64.StdEncoding.DecodeString(trim(seedB64))
		if err != nil || len(seed) != ed25519.SeedSize {
			return KeyPair{}, fmt.Errorf("attest: bad brain key seed")
		}
		priv := ed25519.NewKeyFromSeed(seed)
		return KeyPair{Priv: priv, Pub: priv.Public().(ed25519.PublicKey)}, nil
	}
	// generate + persist
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return KeyPair{}, err
	}
	if keyFile != "" {
		_ = os.WriteFile(keyFile, []byte(base64.StdEncoding.EncodeToString(priv.Seed())), 0o600)
	}
	return KeyPair{Priv: priv, Pub: pub}, nil
}

func PubB64(pub ed25519.PublicKey) string { return base64.StdEncoding.EncodeToString(pub) }

// canonical builds an UNAMBIGUOUS message: a domain tag then each field length-prefixed, so no
// field-splitting/confusion is possible.
func canonical(brainID, kind, subject string, ts float64, nonce string) []byte {
	var b []byte
	put := func(s string) {
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(s)))
		b = append(b, n[:]...)
		b = append(b, s...)
	}
	b = append(b, "corralai-intent-v1\x00"...)
	put(brainID)
	put(kind)
	put(subject)
	put(strconv.FormatFloat(ts, 'f', -1, 64))
	put(nonce)
	return b
}

func Sign(kp KeyPair, brainID, kind, subject string, ts float64, nonce string) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(kp.Priv, canonical(brainID, kind, subject, ts, nonce)))
}

// Verify checks the signature against pubB64 AND freshness. Nonce non-reuse is enforced by the
// storage layer ((brain_id,nonce) uniqueness) — NOT here (attest is stateless). Fail-closed.
func Verify(pubB64, brainID, kind, subject string, ts float64, nonce, sig string, now, skew float64) error {
	pub, err := base64.StdEncoding.DecodeString(pubB64)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return errors.New("attest: bad pubkey")
	}
	sigb, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		return errors.New("attest: bad signature encoding")
	}
	if d := now - ts; d > skew || d < -skew {
		return fmt.Errorf("attest: stale intent (|now-ts|=%.0f > %.0f)", abs(d), skew)
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), canonical(brainID, kind, subject, ts, nonce), sigb) {
		return errors.New("attest: signature does not verify")
	}
	return nil
}

type Outcome int
const ( Registered Outcome = iota; AlreadyTrusted; Conflict; Rejected )

type BrainStore interface {
	Get(brainID string) (pubB64 string, ok bool)
	Put(brainID, pubB64 string) error
}

// Register applies the trust policy. allowlist!=nil ⇒ ONLY allowlisted (brainID→pubB64) accepted
// (TOFU disabled). allowlist==nil ⇒ TOFU: pin first pubkey; refuse a DIFFERENT one (Conflict).
func Register(store BrainStore, brainID, pubB64 string, allowlist map[string]string) (Outcome, error) {
	if allowlist != nil {
		want, ok := allowlist[brainID]
		if !ok || want != pubB64 {
			return Rejected, nil
		}
	}
	if cur, ok := store.Get(brainID); ok {
		if cur == pubB64 {
			return AlreadyTrusted, nil
		}
		return Conflict, nil // REFUSE — never overwrite a pinned key
	}
	if err := store.Put(brainID, pubB64); err != nil {
		return Rejected, err
	}
	return Registered, nil
}

func trim(s string) string { return string([]byte(s)) } // caller trims whitespace; keep simple
func abs(f float64) float64 { if f < 0 { return -f }; return f }
```
> IMPLEMENTER: trim the seed string of surrounding whitespace/newlines properly (a file may have a trailing `\n`) — replace the placeholder `trim` with `strings.TrimSpace`. Confirm `ed25519` stdlib usage.

- [ ] **Step 4: Run green + build** — `go test ./internal/attest/ -v`; `go build ./...`.
- [ ] **Step 5: Commit** — `git commit -m "feat(attest): Ed25519 brain identity — sign/verify (freshness) + TOFU/allowlist trust registry"`

---

## Task 2: `internal/fleet` — `fleet_brains` + `fleet_intents` coordination plane

**Files:** Create `internal/fleet/coord.go`, `internal/fleet/coord_test.go`

**Interfaces produced (backed by the in-mem-DuckDB-ATTACH-`md:` pattern from `sync.go`/`retention.go`):**
- DDL: `fleet_brains(brain_id, pubkey, registered_ts)`, `fleet_intents(brain_id, intent_id, kind, subject, ts, expires_ts, nonce, sig)` with a `(brain_id, nonce)` uniqueness (replay defense).
- `func RegisterBrain(remoteAttach, brainID, pubB64 string, allowlist map[string]string, now time.Time) (attest.Outcome, error)` — reads `fleet_brains` into an `attest.BrainStore` view, calls `attest.Register`, writes on Registered.
- `func PublishIntent(kp attest.KeyPair, remoteAttach, brainID, kind, subject string, ttl time.Duration, now time.Time) error` — signs + INSERTs (nonce = random; INSERT dedups on `(brain_id,nonce)`).
- `func ActiveClaims(remoteAttach, subject, exceptBrain string, now time.Time) ([]Claim, error)` — returns VERIFIED, non-expired `claim` intents on `subject` from OTHER brains: joins `fleet_intents` with `fleet_brains`, and for each row calls `attest.Verify` against the registered pubkey (fail-closed — a row whose sig doesn't verify is dropped).
- Add `fleet_intents` to D's retention set (cursor-safe: keep by `ts`/expiry, preserve max-cursor row).

- [ ] **Step 1: Study** `sync.go` + `retention.go` (the ATTACH recipe, `md:` gating, column idiom) + `attest` (Task 1).

- [ ] **Step 2: Write failing tests** (local DuckDB standing in for `remote`, seed two brains):
  - Register brain A (Registered); re-register A same key (AlreadyTrusted); register A's id with B's key (Conflict, pinned unchanged).
  - PublishIntent(A, claim, repoX) then ActiveClaims(repoX, exceptBrain="B") returns A's claim (verified). ActiveClaims(exceptBrain="A") excludes A's own.
  - **Forged intent:** manually INSERT a `fleet_intents` row for brain A with a BOGUS sig → ActiveClaims drops it (fail-closed verify).
  - **Impersonation:** an intent tagged brain_id=A but signed by B's key → verify fails → dropped.
  - **Expired:** a claim with `expires_ts < now` → excluded.
  - **Replay:** a second INSERT with the same `(brain_id, nonce)` → rejected by uniqueness.
  - Retention: `fleet_intents` old/expired rows compacted, cursor-safe (mirror D's test).

- [ ] **Step 3: Implement `coord.go`** — mirror `Sync`/`Compact`'s connection recipe; the `BrainStore` view over `fleet_brains` reads the current pubkey per brain; `ActiveClaims` verifies EACH candidate row via `attest.Verify(pub, brain_id, "claim", subject, ts, nonce, sig, now, skew)` and only returns those that pass + are unexpired + `brain_id != exceptBrain`. Register/Publish sign or check via `attest`.

- [ ] **Step 4: Run green + build + commit** — `git commit -m "feat(fleet): signed cross-swarm coordination plane — fleet_brains registry + fleet_intents claims (fail-closed verify)"`

---

## Task 3: `internal/brain` + `cmd/corral` — keypair wiring + `fleet_claims` dedup tool

**Files:** Create `internal/brain/fleetclaims.go`; Modify `cmd/corral/main.go` (+ `internal/brain` Options).

- [ ] **Step 1: `cmd/corral` key + registration wiring**
  - Load the brain key: `attest.LoadOrCreateKey(os.Getenv("CORRALAI_BRAIN_KEY"), keyFilePath)` where `keyFilePath` = `CORRALAI_BRAIN_KEY_FILE` or a default beside the other stores. **Scrub `CORRALAI_BRAIN_KEY` from env after load** (add to the existing `scrubSecrets([]string{...})` call).
  - Parse `CORRALAI_BRAIN_PEERS` (optional allowlist: `brain_id:pubB64` lines) → `map[string]string`.
  - At startup (and/or in the fleet ticker, best-effort), when MotherDuck is configured: `fleet.RegisterBrain(target, brainID, attest.PubB64(kp.Pub), allowlist, time.Now())`; log a **loud tamper alert** on `Conflict`.
  - Pass the `KeyPair` + `target` + `brainID` + allowlist into `brain.Options` for the `fleet_claims` tool.

- [ ] **Step 2: `internal/brain/fleetclaims.go`** — register `fleet_claims{subject}` (gated when MotherDuck + key configured): call `fleet.ActiveClaims(target, subject, brainID, time.Now())`, return the verified active peer claims `{brain_id, subject, ts, expires_ts}`. **Per-principal/per-brain publish rate limit** if a publish tool is exposed (v1 may publish claims automatically from `create_mission` rather than a bee tool — keep publish brain-internal, not a bee-callable tool, to preserve observe-don't-coerce). Optional: an advisory pre-mission check in `create_mission` that logs/annotates when a verified peer claim exists on the target repo (does NOT block — configurable via `CORRALAI_CROSS_SWARM_DEDUP`).

- [ ] **Step 3: Tests** — `fleet_claims` returns only verified active other-brain claims (reuse the Task-2 in-mem seam); a forged claim is absent; disabled (no key / no MotherDuck) → tool unregistered / graceful; the pre-mission check surfaces a real peer claim and ignores a forged one.

- [ ] **Step 4: Run green + build + commit** — `git commit -m "feat(brain/corral): brain keypair + startup registration + fleet_claims cross-swarm dedup tool (advisory)"`

---

## Final verification

- [ ] `go build ./...` clean; `go test ./...` all PASS.
- [ ] **Security (the load-bearing checks):** forged/impersonated/replayed/expired intents are all IGNORED (never surfaced by `fleet_claims`, never affect a decision); a brain can't publish as another; TOFU conflict is refused + alerted; verification is fail-closed everywhere.
- [ ] **Observe-don't-coerce:** nothing lets one brain call/control/block another; the dedup check is advisory only; publish is brain-internal (not a bee-coercible tool).
- [ ] **Key hygiene:** the private key never crosses to MotherDuck, is scrubbed from env after load; only the public key is registered.
- [ ] **Graceful:** no key / no MotherDuck / empty allowlist → coordination disabled, brain runs normally.
- [ ] `fleet_intents` is covered by D's cursor-safe retention.

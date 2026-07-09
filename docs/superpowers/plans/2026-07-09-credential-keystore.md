# Credential Keystore Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** An embedded, portable, secure credential subsystem (`internal/creds`) + `corral secret` CLI so any operator stores provider keys once and the runtime discovers them — resolving env → OS keyring → age-encrypted file.

**Architecture:** A `Store` resolves a named secret through an ordered chain of `backend` tiers (env, keyring, age file), first hit wins; writes go to the first writable tier. `corral-agent` reads its keys/token via `creds` instead of `os.Getenv`. Security is the acceptance bar — this ships public and will be scrutinized.

**Tech Stack:** Go 1.26, `github.com/zalando/go-keyring` (OS keyring), `filippo.io/age` (X25519 file encryption). Both pure-Go/CGO-free.

## Global Constraints

- SPDX header `// SPDX-License-Identifier: Elastic-2.0` on every new Go file.
- TDD: failing test first, watch it fail, minimal code, watch it pass, commit.
- `go vet ./...` clean; full suite green before each commit.
- **Security is the acceptance bar (public, developer-scrutinized):**
  - No plaintext secret at rest in the file backend — age-encrypted only; file mode `0600`, dir `0700`.
  - Never log or error with a secret value — use `Redact` (fingerprint: first 4 chars + length).
  - `corral secret set` reads the value from prompt/stdin, NEVER a CLI arg.
  - The age identity is never written in plaintext beside the store.
  - Tests must assert secrets don't leak into output.
- Corral metaphor in user-facing copy (herd/corral, no bee/hive/swarm).
- Keyring backend NEVER touches the real OS store in tests — use `keyring.MockInit()`.
- Branch: `feat/creds-keystore` (spec already committed there).

## File Structure

- `internal/creds/creds.go` — `Store`, `backend` interface, `Redact`, `Open()` chain, env backend.
- `internal/creds/agefile.go` — age-encrypted file backend + identity resolution.
- `internal/creds/keyring.go` — OS-keyring backend (+ names index).
- `internal/creds/*_test.go` — per-file tests.
- `cmd/corral/secret.go` — `corral secret` subcommand (or add a case in main.go's dispatch).
- `cmd/corral-agent/backend.go`, `cmd/corral-agent/main.go` — read keys/token via `creds`.

---

### Task 1: `creds` core — backend interface, env backend, Store, Redact

**Files:**
- Create: `internal/creds/creds.go`, `internal/creds/creds_test.go`

**Interfaces:**
- Produces:
  - `type backend interface { get(name string) (string, bool, error); set(name, value string) error; remove(name string) error; names() ([]string, error); writable() bool }`
  - `type Store struct { chain []backend }`
  - `func newStore(chain ...backend) *Store`
  - `func (s *Store) Get(name string) (string, bool, error)` — first backend reporting found
  - `func (s *Store) Set(name, value string) error` — first writable backend
  - `func (s *Store) Remove(name string) error` — remove from every writable backend that has it
  - `func (s *Store) List() ([]string, error)` — union of `names()`, deduped, sorted
  - `func Redact(secret string) string`
  - `var canonicalNames = []string{"OPENAI_API_KEY","GEMINI_API_KEY","ANTHROPIC_API_KEY","OPENROUTER_API_KEY","CORRALAI_BRAIN_KEY"}`
  - `type envBackend struct{}` implementing `backend` (get = `os.Getenv`; set/remove return `errReadOnly`; names = canonical names whose env is non-empty; writable=false)
  - `var errReadOnly = errors.New("creds: backend is read-only")`

- [ ] **Step 1: Write the failing test** `internal/creds/creds_test.go`:

```go
// SPDX-License-Identifier: Elastic-2.0

package creds

import (
	"sort"
	"strings"
	"testing"
)

// memBackend is a writable in-memory backend for exercising Store.
type memBackend struct{ m map[string]string }

func newMem() *memBackend { return &memBackend{m: map[string]string{}} }
func (b *memBackend) get(n string) (string, bool, error) { v, ok := b.m[n]; return v, ok, nil }
func (b *memBackend) set(n, v string) error              { b.m[n] = v; return nil }
func (b *memBackend) remove(n string) error              { delete(b.m, n); return nil }
func (b *memBackend) names() ([]string, error) {
	var out []string
	for k := range b.m {
		out = append(out, k)
	}
	return out, nil
}
func (b *memBackend) writable() bool { return true }

func TestStoreGetFirstHitWins(t *testing.T) {
	hi, lo := newMem(), newMem()
	hi.set("K", "from-hi")
	lo.set("K", "from-lo")
	s := newStore(hi, lo)
	v, ok, err := s.Get("K")
	if err != nil || !ok || v != "from-hi" {
		t.Fatalf("Get = %q ok=%v err=%v, want from-hi", v, ok, err)
	}
	if _, ok, _ := s.Get("MISSING"); ok {
		t.Fatal("missing key must report not-found")
	}
}

func TestStoreSetGoesToFirstWritable(t *testing.T) {
	ro, w := envBackend{}, newMem()
	s := newStore(ro, w) // env (read-only) first, mem writable second
	if err := s.Set("K", "v"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if v := w.m["K"]; v != "v" {
		t.Fatalf("Set landed in %q, want the writable backend", v)
	}
}

func TestStoreListUnionSortedDeduped(t *testing.T) {
	a, b := newMem(), newMem()
	a.set("B", "1")
	a.set("A", "2")
	b.set("A", "3")
	b.set("C", "4")
	s := newStore(a, b)
	got, _ := s.List()
	want := []string{"A", "B", "C"}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("List = %v, want %v (deduped+sorted)", got, want)
	}
}

func TestRedactNeverLeaks(t *testing.T) {
	secret := "sk-supersecretvalue-1234567890"
	r := Redact(secret)
	if strings.Contains(r, "supersecret") || strings.Contains(r, secret) {
		t.Fatalf("Redact leaked the secret: %q", r)
	}
	if !strings.HasPrefix(r, "sk-s") { // first 4 shown for identification
		t.Fatalf("Redact should keep a 4-char fingerprint, got %q", r)
	}
	if Redact("") != "(empty)" {
		t.Fatalf("Redact(empty) = %q", Redact(""))
	}
}

func TestEnvBackend(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "envval")
	b := envBackend{}
	v, ok, _ := b.get("OPENAI_API_KEY")
	if !ok || v != "envval" {
		t.Fatalf("env get = %q ok=%v", v, ok)
	}
	if b.writable() {
		t.Fatal("env backend must be read-only")
	}
	if err := b.set("X", "y"); err == nil {
		t.Fatal("env set must return errReadOnly")
	}
	names, _ := b.names()
	found := false
	for _, n := range names {
		if n == "OPENAI_API_KEY" {
			found = true
		}
	}
	if !found {
		t.Fatalf("env names() should include the set canonical key, got %v", names)
	}
}
```

- [ ] **Step 2: Run it, verify it fails.** `go test ./internal/creds/ -v` → FAIL (package/types undefined).

- [ ] **Step 3: Implement `internal/creds/creds.go`:**

```go
// SPDX-License-Identifier: Elastic-2.0

// Package creds is corralai's portable credential keystore: it resolves a named
// secret (e.g. OPENAI_API_KEY) through an ordered backend chain — environment
// variable, OS keyring, then an age-encrypted file — so an operator configures a
// key once and the runtime discovers it on any platform. No secret is embedded in
// the binary and none is written in plaintext at rest.
package creds

import (
	"errors"
	"fmt"
	"os"
	"sort"
)

var errReadOnly = errors.New("creds: backend is read-only")

// canonicalNames are the provider/token secrets corral knows by convention, named
// after their env var so an env override is transparent.
var canonicalNames = []string{
	"OPENAI_API_KEY", "GEMINI_API_KEY", "ANTHROPIC_API_KEY",
	"OPENROUTER_API_KEY", "CORRALAI_BRAIN_KEY",
}

// backend is one storage tier of the chain.
type backend interface {
	get(name string) (string, bool, error)
	set(name, value string) error
	remove(name string) error
	names() ([]string, error)
	writable() bool
}

// Store resolves and persists secrets across an ordered backend chain.
type Store struct{ chain []backend }

func newStore(chain ...backend) *Store { return &Store{chain: chain} }

// Get returns the secret for name, first backend that has it wins.
func (s *Store) Get(name string) (string, bool, error) {
	for _, b := range s.chain {
		if v, ok, err := b.get(name); err != nil {
			return "", false, err
		} else if ok {
			return v, true, nil
		}
	}
	return "", false, nil
}

// Set writes to the first writable backend in the chain.
func (s *Store) Set(name, value string) error {
	for _, b := range s.chain {
		if b.writable() {
			return b.set(name, value)
		}
	}
	return errors.New("creds: no writable backend configured")
}

// Remove deletes name from every writable backend that holds it.
func (s *Store) Remove(name string) error {
	var firstErr error
	for _, b := range s.chain {
		if !b.writable() {
			continue
		}
		if _, ok, _ := b.get(name); ok {
			if err := b.remove(name); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// List returns the union of names across backends, deduped and sorted. Values are
// never returned.
func (s *Store) List() ([]string, error) {
	set := map[string]struct{}{}
	for _, b := range s.chain {
		ns, err := b.names()
		if err != nil {
			return nil, err
		}
		for _, n := range ns {
			set[n] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

// Redact returns a non-reversible fingerprint of a secret for logs/errors: the
// first 4 characters plus the length. Never returns the value.
func Redact(secret string) string {
	if secret == "" {
		return "(empty)"
	}
	head := secret
	if len(head) > 4 {
		head = head[:4]
	}
	return fmt.Sprintf("%s…(%d chars)", head, len(secret))
}

// envBackend reads canonical secrets from the process environment. Read-only.
type envBackend struct{}

func (envBackend) get(name string) (string, bool, error) {
	v, ok := os.LookupEnv(name)
	return v, ok && v != "", nil
}
func (envBackend) set(string, string) error    { return errReadOnly }
func (envBackend) remove(string) error         { return errReadOnly }
func (envBackend) writable() bool              { return false }
func (envBackend) names() ([]string, error) {
	var out []string
	for _, n := range canonicalNames {
		if v := os.Getenv(n); v != "" {
			out = append(out, n)
		}
	}
	return out, nil
}
```

- [ ] **Step 4: Run it, verify it passes.** `go test ./internal/creds/ -v` → PASS.

- [ ] **Step 5: Commit.**

```bash
git add internal/creds/creds.go internal/creds/creds_test.go
git commit -m "feat(creds): Store + env backend + Redact — the keystore core"
```

---

### Task 2: age-encrypted file backend

**Files:**
- Create: `internal/creds/agefile.go`, `internal/creds/agefile_test.go`
- Modify: `go.mod`/`go.sum` (`go get filippo.io/age`)

**Interfaces:**
- Produces:
  - `type ageFile struct { path string; id *age.X25519Identity }`
  - `func newAgeFile(path string, id *age.X25519Identity) *ageFile` implementing `backend` (writable=true)
  - `func loadOrCreateIdentity(idPath string) (*age.X25519Identity, error)` — reads an `AGE-SECRET-KEY-…` from idPath (mode 0600), generating + writing one (0600) if absent
  - `func resolveIdentity() (*age.X25519Identity, error)` — env `CORRAL_AGE_IDENTITY` (a literal `AGE-SECRET-KEY-…`) → systemd credential `$CREDENTIALS_DIRECTORY/age-identity` → the key file under the creds dir

**Interfaces consumed:** `filippo.io/age`.

- [ ] **Step 1: `go get filippo.io/age`** and verify it builds: `go build ./...`.

- [ ] **Step 2: Write the failing test** `internal/creds/agefile_test.go`:

```go
// SPDX-License-Identifier: Elastic-2.0

package creds

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
)

func TestAgeFileRoundTripEncryptedAtRest(t *testing.T) {
	dir := t.TempDir()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "creds.age")
	b := newAgeFile(path, id)

	if err := b.set("OPENAI_API_KEY", "sk-plaintext-secret-xyz"); err != nil {
		t.Fatal(err)
	}
	// At rest it must be ciphertext — the plaintext secret must NOT appear.
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "sk-plaintext-secret-xyz") {
		t.Fatal("secret is stored in plaintext on disk — must be age-encrypted")
	}
	// File perms 0600.
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("cred file mode = %v, want 0600", fi.Mode().Perm())
	}
	// Round-trips through a fresh backend with the same identity.
	v, ok, err := newAgeFile(path, id).get("OPENAI_API_KEY")
	if err != nil || !ok || v != "sk-plaintext-secret-xyz" {
		t.Fatalf("get = %q ok=%v err=%v", v, ok, err)
	}
	// A different identity cannot decrypt.
	other, _ := age.GenerateX25519Identity()
	if _, ok, err := newAgeFile(path, other).get("OPENAI_API_KEY"); ok && err == nil {
		t.Fatal("a different identity must NOT decrypt the store")
	}
}

func TestAgeFileNamesAndRemove(t *testing.T) {
	dir := t.TempDir()
	id, _ := age.GenerateX25519Identity()
	b := newAgeFile(filepath.Join(dir, "c.age"), id)
	b.set("A", "1")
	b.set("B", "2")
	if names, _ := b.names(); len(names) != 2 {
		t.Fatalf("names = %v, want 2", names)
	}
	b.remove("A")
	if _, ok, _ := b.get("A"); ok {
		t.Fatal("A should be gone after remove")
	}
	if _, ok, _ := b.get("B"); !ok {
		t.Fatal("B should survive")
	}
}

func TestLoadOrCreateIdentityPersistsAndPerms(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "identity.age")
	id1, err := loadOrCreateIdentity(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Fatalf("identity mode = %v, want 0600", fi.Mode().Perm())
	}
	id2, _ := loadOrCreateIdentity(p) // second call loads the same one
	if id1.Recipient().String() != id2.Recipient().String() {
		t.Fatal("loadOrCreateIdentity must persist + reload the same identity")
	}
}
```

- [ ] **Step 3: Run it, verify it fails.** `go test ./internal/creds/ -run TestAgeFile -v` and `-run TestLoadOrCreate` → FAIL (undefined).

- [ ] **Step 4: Implement `internal/creds/agefile.go`.** The store is a JSON map encrypted to the identity's recipient. NOTE: confirm the `age.Encrypt`/`age.Decrypt` signatures against the pinned version (`age.Encrypt(w io.Writer, recipients ...age.Recipient) (io.WriteCloser, error)`; `age.Decrypt(r io.Reader, identities ...age.Identity) (io.Reader, error)`) — adapt if the minor version differs.

```go
// SPDX-License-Identifier: Elastic-2.0

package creds

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/age"
)

// ageFile is a writable backend storing a name→secret map in a single
// age-encrypted file. Only ciphertext touches disk.
type ageFile struct {
	path string
	id   *age.X25519Identity
}

func newAgeFile(path string, id *age.X25519Identity) *ageFile { return &ageFile{path: path, id: id} }

func (b *ageFile) load() (map[string]string, error) {
	f, err := os.Open(b.path) // #nosec G304 -- path is the operator's own creds dir
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r, err := age.Decrypt(f, b.id)
	if err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func (b *ageFile) save(m map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(b.path), 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, b.id.Recipient())
	if err != nil {
		return err
	}
	if _, err := w.Write(raw); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return os.WriteFile(b.path, buf.Bytes(), 0o600)
}

func (b *ageFile) get(name string) (string, bool, error) {
	m, err := b.load()
	if err != nil {
		return "", false, err
	}
	v, ok := m[name]
	return v, ok, nil
}

func (b *ageFile) set(name, value string) error {
	m, err := b.load()
	if err != nil {
		return err
	}
	m[name] = value
	return b.save(m)
}

func (b *ageFile) remove(name string) error {
	m, err := b.load()
	if err != nil {
		return err
	}
	delete(m, name)
	return b.save(m)
}

func (b *ageFile) names() ([]string, error) {
	m, err := b.load()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out, nil
}

func (b *ageFile) writable() bool { return true }

// loadOrCreateIdentity reads an age identity from idPath (0600), generating and
// persisting one (0600) if absent. The identity is the master that unlocks the
// store — protected by the keyring / systemd-cred / a 0600 key file, never stored
// beside the store in plaintext.
func loadOrCreateIdentity(idPath string) (*age.X25519Identity, error) {
	if raw, err := os.ReadFile(idPath); err == nil { // #nosec G304 -- operator's creds dir
		return age.ParseX25519Identity(strings.TrimSpace(string(raw)))
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(idPath), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(idPath, []byte(id.String()), 0o600); err != nil {
		return nil, err
	}
	return id, nil
}

// resolveIdentity finds the age identity: env CORRAL_AGE_IDENTITY (a literal
// AGE-SECRET-KEY-…) → systemd credential ($CREDENTIALS_DIRECTORY/age-identity) →
// a 0600 key file in credsDir. This is the master-protection chain from the spec.
func resolveIdentity(credsDir string) (*age.X25519Identity, error) {
	if v := os.Getenv("CORRAL_AGE_IDENTITY"); strings.HasPrefix(strings.TrimSpace(v), "AGE-SECRET-KEY-") {
		return age.ParseX25519Identity(strings.TrimSpace(v))
	}
	if d := os.Getenv("CREDENTIALS_DIRECTORY"); d != "" {
		if raw, err := os.ReadFile(filepath.Join(d, "age-identity")); err == nil { // #nosec G304 -- systemd cred dir
			if id, perr := age.ParseX25519Identity(strings.TrimSpace(string(raw))); perr == nil {
				return id, nil
			}
		}
	}
	return loadOrCreateIdentity(filepath.Join(credsDir, "identity.age"))
}
```

- [ ] **Step 5: Run it, verify it passes.** `go test ./internal/creds/ -v` → PASS.

- [ ] **Step 6: Commit.**

```bash
git add internal/creds/agefile.go internal/creds/agefile_test.go go.mod go.sum
git commit -m "feat(creds): age-encrypted file backend + identity resolution (keyring/systemd/keyfile)"
```

---

### Task 3: OS-keyring backend

**Files:**
- Create: `internal/creds/keyring.go`, `internal/creds/keyring_test.go`
- Modify: `go.mod`/`go.sum` (`go get github.com/zalando/go-keyring`)

**Interfaces:**
- Produces: `type keyringBackend struct { service string }`; `func newKeyring(service string) *keyringBackend` implementing `backend` (writable=true). Because go-keyring cannot enumerate, it maintains a names index entry (`__names__`) it updates on set/remove.

- [ ] **Step 1: `go get github.com/zalando/go-keyring`**, `go build ./...`.

- [ ] **Step 2: Write the failing test** `internal/creds/keyring_test.go` (uses the in-memory mock — never the real OS store):

```go
// SPDX-License-Identifier: Elastic-2.0

package creds

import (
	"sort"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestKeyringBackendRoundTripAndIndex(t *testing.T) {
	keyring.MockInit() // in-memory; never touches the OS keychain
	b := newKeyring("corral-test")

	if err := b.set("OPENAI_API_KEY", "sk-1"); err != nil {
		t.Fatal(err)
	}
	if err := b.set("GEMINI_API_KEY", "g-2"); err != nil {
		t.Fatal(err)
	}
	v, ok, err := b.get("OPENAI_API_KEY")
	if err != nil || !ok || v != "sk-1" {
		t.Fatalf("get = %q ok=%v err=%v", v, ok, err)
	}
	if _, ok, _ := b.get("NOPE"); ok {
		t.Fatal("missing key must be not-found, not an error")
	}
	names, _ := b.names()
	sort.Strings(names)
	if strings.Join(names, ",") != "GEMINI_API_KEY,OPENAI_API_KEY" {
		t.Fatalf("names index = %v", names)
	}
	// The index entry itself must never surface as a listable name.
	for _, n := range names {
		if n == "__names__" {
			t.Fatal("the internal index key leaked into names()")
		}
	}
	if err := b.remove("OPENAI_API_KEY"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := b.get("OPENAI_API_KEY"); ok {
		t.Fatal("removed key still present")
	}
	names, _ = b.names()
	if len(names) != 1 || names[0] != "GEMINI_API_KEY" {
		t.Fatalf("index not updated on remove: %v", names)
	}
}
```

- [ ] **Step 3: Run it, verify it fails.** `go test ./internal/creds/ -run TestKeyring -v` → FAIL (undefined).

- [ ] **Step 4: Implement `internal/creds/keyring.go`.** NOTE: `keyring.Get` returns `keyring.ErrNotFound` when absent — map that to `(“”, false, nil)`, not an error.

```go
// SPDX-License-Identifier: Elastic-2.0

package creds

import (
	"encoding/json"
	"errors"
	"sort"

	"github.com/zalando/go-keyring"
)

const keyringIndex = "__names__"

// keyringBackend stores secrets in the OS keyring (macOS Keychain, Windows
// Credential Manager, Linux Secret Service). go-keyring cannot enumerate entries,
// so we maintain a names index under a reserved key.
type keyringBackend struct{ service string }

func newKeyring(service string) *keyringBackend { return &keyringBackend{service: service} }

func (b *keyringBackend) get(name string) (string, bool, error) {
	v, err := keyring.Get(b.service, name)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

func (b *keyringBackend) index() ([]string, error) {
	v, err := keyring.Get(b.service, keyringIndex)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	_ = json.Unmarshal([]byte(v), &names)
	return names, nil
}

func (b *keyringBackend) setIndex(names []string) error {
	raw, _ := json.Marshal(names)
	return keyring.Set(b.service, keyringIndex, string(raw))
}

func (b *keyringBackend) set(name, value string) error {
	if err := keyring.Set(b.service, name, value); err != nil {
		return err
	}
	names, err := b.index()
	if err != nil {
		return err
	}
	for _, n := range names {
		if n == name {
			return nil
		}
	}
	return b.setIndex(append(names, name))
}

func (b *keyringBackend) remove(name string) error {
	if err := keyring.Delete(b.service, name); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return err
	}
	names, err := b.index()
	if err != nil {
		return err
	}
	out := names[:0]
	for _, n := range names {
		if n != name {
			out = append(out, n)
		}
	}
	return b.setIndex(out)
}

func (b *keyringBackend) names() ([]string, error) {
	names, err := b.index()
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}

func (b *keyringBackend) writable() bool { return true }
```

- [ ] **Step 5: Run it, verify it passes.** `go test ./internal/creds/ -v` → PASS.

- [ ] **Step 6: Commit.**

```bash
git add internal/creds/keyring.go internal/creds/keyring_test.go go.mod go.sum
git commit -m "feat(creds): OS-keyring backend with a names index"
```

---

### Task 4: `Open()` — the default chain + creds dir

**Files:**
- Modify: `internal/creds/creds.go` (add `Open`, `credsDir`, `keyringAvailable`)
- Create/Modify: `internal/creds/creds_test.go` (append)

**Interfaces:**
- Produces: `func Open() (*Store, error)` — builds `env → keyring(if usable) → age file`; `func credsDir() (string, error)` — `CORRAL_CREDS_DIR` else `os.UserConfigDir()/corral`.

- [ ] **Step 1: Write the failing test** (append to `creds_test.go`): with `CORRAL_CREDS_DIR` set to a temp dir and `keyring.MockInit()`, `Open()` returns a Store where a value `Set` then `Get` round-trips, and an env var overrides a stored value.

```go
func TestOpenChainEnvOverridesStored(t *testing.T) {
	keyring.MockInit()
	t.Setenv("CORRAL_CREDS_DIR", t.TempDir())
	s, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set("OPENROUTER_API_KEY", "stored"); err != nil {
		t.Fatal(err)
	}
	if v, ok, _ := s.Get("OPENROUTER_API_KEY"); !ok || v != "stored" {
		t.Fatalf("stored get = %q ok=%v", v, ok)
	}
	// env wins.
	t.Setenv("OPENROUTER_API_KEY", "fromenv")
	s2, _ := Open()
	if v, _, _ := s2.Get("OPENROUTER_API_KEY"); v != "fromenv" {
		t.Fatalf("env must override stored, got %q", v)
	}
}
```

(Import `github.com/zalando/go-keyring` in the test file.)

- [ ] **Step 2: Run it, verify it fails.** `go test ./internal/creds/ -run TestOpenChain -v` → FAIL (Open undefined).

- [ ] **Step 3: Implement** in `creds.go`:

```go
// credsDir is where the age store + fallback identity live: CORRAL_CREDS_DIR, else
// the OS user config dir + "corral".
func credsDir() (string, error) {
	if d := os.Getenv("CORRAL_CREDS_DIR"); d != "" {
		return d, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "corral"), nil
}

// Open builds the default resolution chain: environment → OS keyring → age file.
// A missing store is not an error (an unconfigured operator simply has no secrets).
func Open() (*Store, error) {
	dir, err := credsDir()
	if err != nil {
		return nil, err
	}
	chain := []backend{envBackend{}}
	if keyringUsable() {
		chain = append(chain, newKeyring("corral"))
	}
	id, err := resolveIdentity(dir)
	if err != nil {
		return nil, err
	}
	chain = append(chain, newAgeFile(filepath.Join(dir, "creds.age"), id))
	return newStore(chain...), nil
}
```

Add `keyringUsable()` — probe the keyring with a harmless get and treat a backend-unavailable error (no Secret Service on headless Linux) as "not usable"; import `path/filepath`. Confirm the go-keyring error value for "unavailable" against the pinned version and match on it (fall back to the age file when unavailable).

- [ ] **Step 4: Run it, verify it passes.** `go test ./internal/creds/ -v` → PASS. `go vet ./...`.

- [ ] **Step 5: Commit.**

```bash
git add internal/creds/creds.go internal/creds/creds_test.go
git commit -m "feat(creds): Open() default chain (env → keyring → age file) + creds dir"
```

---

### Task 5: `corral secret` CLI

**Files:**
- Create: `cmd/corral/secret.go`
- Modify: `cmd/corral/main.go` (dispatch a `secret` subcommand; confirm how `cmd/corral` currently routes subcommands vs. booting the server — add a `case "secret"` before the server boot)
- Test: `cmd/corral/secret_test.go`

**Interfaces:**
- Consumes: `creds.Open`, `creds.Redact`.
- Produces: `corral secret set <NAME>` (value from stdin/prompt, never argv), `get <NAME>`, `list`, `rm <NAME>`.

- [ ] **Step 1: Write the failing test** `cmd/corral/secret_test.go` — drive the subcommand with an injected stdin and a temp `CORRAL_CREDS_DIR` + `keyring.MockInit()`; assert set-then-list shows the NAME (not the value), and `set` never takes the value from args.

```go
// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestSecretSetListNoValueLeak(t *testing.T) {
	keyring.MockInit()
	t.Setenv("CORRAL_CREDS_DIR", t.TempDir())

	var out bytes.Buffer
	// set reads the value from stdin, not args
	if err := runSecret([]string{"set", "OPENAI_API_KEY"}, strings.NewReader("sk-secret-value\n"), &out); err != nil {
		t.Fatalf("set: %v", err)
	}
	out.Reset()
	if err := runSecret([]string{"list"}, nil, &out); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out.String(), "OPENAI_API_KEY") {
		t.Fatalf("list should show the name, got %q", out.String())
	}
	if strings.Contains(out.String(), "sk-secret-value") {
		t.Fatal("list leaked the secret VALUE")
	}
}
```

- [ ] **Step 2: Run it, verify it fails.** `go test ./cmd/corral/ -run TestSecret -v` → FAIL (`runSecret` undefined).

- [ ] **Step 3: Implement `cmd/corral/secret.go`** with a testable `runSecret(args []string, stdin io.Reader, out io.Writer) error`. `set` reads the value from `stdin` (prompt only when stdin is a TTY); never from `args`. `get` prints the value to `out`. `list` prints names one per line. `rm` removes. Wire `main.go` to call `runSecret(os.Args[2:], os.Stdin, os.Stdout)` on `case "secret"`.

```go
// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/pdbethke/corralai/internal/creds"
)

func runSecret(args []string, stdin io.Reader, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: corral secret set|get|list|rm <NAME>")
	}
	s, err := creds.Open()
	if err != nil {
		return err
	}
	switch args[0] {
	case "set":
		if len(args) != 2 {
			return fmt.Errorf("usage: corral secret set <NAME>  (value read from stdin — never a CLI arg)")
		}
		name := args[1]
		val, err := readSecretValue(stdin)
		if err != nil {
			return err
		}
		if val == "" {
			return fmt.Errorf("no value read from stdin")
		}
		if err := s.Set(name, val); err != nil {
			return err
		}
		fmt.Fprintf(out, "stored %s (%s)\n", name, creds.Redact(val))
		return nil
	case "get":
		if len(args) != 2 {
			return fmt.Errorf("usage: corral secret get <NAME>")
		}
		v, ok, err := s.Get(args[1])
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("no secret %q", args[1])
		}
		fmt.Fprintln(out, v)
		return nil
	case "list":
		names, err := s.List()
		if err != nil {
			return err
		}
		for _, n := range names {
			fmt.Fprintln(out, n)
		}
		return nil
	case "rm":
		if len(args) != 2 {
			return fmt.Errorf("usage: corral secret rm <NAME>")
		}
		return s.Remove(args[1])
	default:
		return fmt.Errorf("unknown secret subcommand %q (set|get|list|rm)", args[0])
	}
}

// readSecretValue reads one line (the secret) from stdin, trimming the trailing
// newline. Reading from stdin (not argv) keeps the value out of ps/shell history.
func readSecretValue(stdin io.Reader) (string, error) {
	r := bufio.NewReader(stdin)
	line, err := r.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
```

- [ ] **Step 4: Run it, verify it passes.** `go test ./cmd/corral/ -run TestSecret -v` → PASS. Build: `go build ./cmd/corral`.

- [ ] **Step 5: Commit.**

```bash
git add cmd/corral/secret.go cmd/corral/secret_test.go cmd/corral/main.go
git commit -m "feat(corral): `corral secret` CLI — stdin-only set, redacted output"
```

---

### Task 6: Wire `corral-agent` to read keys/token via `creds`

**Files:**
- Modify: `cmd/corral-agent/backend.go` (`newBackend` key reads), `cmd/corral-agent/main.go` (all `CORRALAI_BRAIN_KEY` reads + the MCP transport auth if it reads the token)

**Interfaces:**
- Consumes: `creds.Open`, `(*creds.Store).Get`.

- [ ] **Step 1: Sweep the token sites.** `grep -n 'CORRALAI_BRAIN_KEY\|OPENAI_API_KEY\|ANTHROPIC_API_KEY' cmd/corral-agent/*.go` — every read becomes a `creds` lookup. Also confirm whether the MCP `StreamableClientTransport` (main.go ~198) injects the brain token; if it does via env, route it through `creds` too.

- [ ] **Step 2: Write the failing test** `cmd/corral-agent/creds_wire_test.go`: with `CORRAL_CREDS_DIR` temp + `keyring.MockInit()`, store `OPENAI_API_KEY` via a `creds` store (env UNSET), and assert the agent's key resolver returns it. Introduce a tiny helper `agentSecret(name string) string` in the agent that does `creds.Open().Get`, and test that helper (env unset → picks up the stored value; env set → env wins).

```go
// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"path/filepath"
	"testing"

	"github.com/pdbethke/corralai/internal/creds"
	"github.com/zalando/go-keyring"
)

func TestAgentSecretResolvesFromStore(t *testing.T) {
	keyring.MockInit()
	dir := t.TempDir()
	t.Setenv("CORRAL_CREDS_DIR", dir)
	t.Setenv("CREDENTIALS_DIRECTORY", "") // no systemd cred in test
	st, _ := creds.Open()
	if err := st.Set("OPENAI_API_KEY", "sk-stored"); err != nil {
		t.Fatal(err)
	}
	// env unset → resolves from the store
	if got := agentSecret("OPENAI_API_KEY"); got != "sk-stored" {
		t.Fatalf("agentSecret = %q, want sk-stored", got)
	}
	// env wins
	t.Setenv("OPENAI_API_KEY", "sk-env")
	if got := agentSecret("OPENAI_API_KEY"); got != "sk-env" {
		t.Fatalf("env override: agentSecret = %q, want sk-env", got)
	}
	_ = filepath.Join(dir, "creds.age")
}
```

- [ ] **Step 3: Run it, verify it fails.** `go test ./cmd/corral-agent/ -run TestAgentSecret -v` → FAIL (`agentSecret` undefined).

- [ ] **Step 4: Implement.** Add `func agentSecret(name string) string` to `cmd/corral-agent` (open the store once, memoized; `v,_,_ := store.Get(name); return v`). Replace `os.Getenv("OPENAI_API_KEY")` (backend.go:46), `os.Getenv("ANTHROPIC_API_KEY")` (backend.go:52), and every `os.Getenv("CORRALAI_BRAIN_KEY")` with `agentSecret(...)`. After resolving, `os.Unsetenv(name)` for the ones sourced from env is optional hardening — but do it for `CORRALAI_BRAIN_KEY` and the provider keys once, right after the first resolve at startup, so they don't leak into any child-process env the agent spawns (jailed exec). Keep `OPENAI_BASE_URL` etc. as plain env (not secrets).

- [ ] **Step 5: Run it, verify it passes.** `go test ./cmd/corral-agent/ -v` → PASS. `go build ./...`; `go vet ./...`; full suite `go test ./...`.

- [ ] **Step 6: Commit.**

```bash
git add cmd/corral-agent/
git commit -m "feat(corral-agent): resolve provider keys + brain token via the creds keystore"
```

---

## Self-Review

**Spec coverage:**
- env → keyring → age file resolver → Tasks 1 (Store+env), 3 (keyring), 2 (age), 4 (Open chain). ✓
- No plaintext at rest (age-encrypted, 0600) → Task 2 (asserted: ciphertext on disk, 0600, wrong-identity-can't-decrypt). ✓
- Master-identity protection chain (env/systemd/keyfile) → Task 2 `resolveIdentity`. ✓
- Redacted output, no value leak → Task 1 `Redact` + Task 5 CLI test (list shows name not value). ✓
- set via stdin not argv → Task 5 (`readSecretValue` from stdin; test drives injected stdin). ✓
- env scrub after read → Task 6 (os.Unsetenv the sourced secrets at startup). ✓
- keyring never touches real OS store in tests → all keyring tests call `keyring.MockInit()`. ✓
- `corral secret` CLI (set/get/list/rm) → Task 5. ✓
- corral-agent wiring (keys + token, env wins) → Task 6. ✓
- Deps go-keyring + age, pure-Go → Tasks 2, 3 (`go get`). ✓

**Placeholder scan:** the two `NOTE`s (age Encrypt/Decrypt signature; go-keyring "unavailable" error value; `main.go` subcommand routing) are explicit "confirm against the pinned version / existing code" instructions, not missing content — each has the intended code and the exact thing to verify.

**Type consistency:** `backend` interface identical across Tasks 1-3 (`get/set/remove/names/writable`). `Store`/`newStore`/`Get/Set/Remove/List` consistent (1,4). `newAgeFile(path,id)`, `newKeyring(service)`, `resolveIdentity(dir)`, `credsDir()`, `Open()`, `Redact`, `agentSecret(name)` used consistently where referenced.

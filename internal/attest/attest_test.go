// SPDX-License-Identifier: Elastic-2.0

// internal/attest/attest_test.go
package attest

import (
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func mkKP(t *testing.T) KeyPair {
	t.Helper()
	kp, err := LoadOrCreateKey("", t.TempDir()+"/k") // generate+persist
	if err != nil {
		t.Fatal(err)
	}
	return kp
}

func TestSignVerifyRoundTrip(t *testing.T) {
	kp := mkKP(t)
	ts, nonce, expiresTs := 1000.0, "n1", 1300.0
	sig := Sign(kp, "brainA", "claim", "repoX", ts, nonce, expiresTs)
	if err := Verify(PubB64(kp.Pub), "brainA", "claim", "repoX", ts, nonce, sig, expiresTs, 1000.0, 300); err != nil {
		t.Fatalf("valid sig should verify: %v", err)
	}
}

func TestVerifyRejectsTamper(t *testing.T) {
	kp := mkKP(t)
	expiresTs := 1300.0
	sig := Sign(kp, "brainA", "claim", "repoX", 1000, "n1", expiresTs)
	pub := PubB64(kp.Pub)
	// tamper each field → must fail
	for _, c := range []struct {
		b, k, s string
		ts      float64
		n       string
		exp     float64
	}{
		{"brainB", "claim", "repoX", 1000, "n1", expiresTs},   // brain
		{"brainA", "release", "repoX", 1000, "n1", expiresTs}, // kind
		{"brainA", "claim", "repoY", 1000, "n1", expiresTs},   // subject
		{"brainA", "claim", "repoX", 1001, "n1", expiresTs},   // ts (also fails freshness below)
		{"brainA", "claim", "repoX", 1000, "n2", expiresTs},   // nonce
		{"brainA", "claim", "repoX", 1000, "n1", 9999999.0},   // expiresTs (replay-resurrection attack field)
	} {
		if err := Verify(pub, c.b, c.k, c.s, c.ts, c.n, sig, c.exp, 1000, 300); err == nil {
			t.Fatalf("tampered field must fail: %+v", c)
		}
	}
}

func TestVerifyFreshness(t *testing.T) {
	kp := mkKP(t)
	sig := Sign(kp, "a", "claim", "x", 1000, "n", 1300.0)
	if err := Verify(PubB64(kp.Pub), "a", "claim", "x", 1000, "n", sig, 1300.0, 2000, 300); err == nil {
		t.Fatal("stale ts (|now-ts|>skew) must fail")
	}
}

func TestVerifyWrongKeyFails(t *testing.T) {
	a, b := mkKP(t), mkKP(t)
	sig := Sign(a, "brainA", "claim", "x", 1000, "n", 1300.0) // signed by A
	// verify against B's pubkey (impersonation) → fail
	if err := Verify(PubB64(b.Pub), "brainA", "claim", "x", 1000, "n", sig, 1300.0, 1000, 300); err == nil {
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

// TestLoadOrCreateKeyFromSeed verifies round-trip via env seed path.
func TestLoadOrCreateKeyFromSeed(t *testing.T) {
	// Generate a keypair, grab its seed, reload from that seed, verify keys match.
	kp1 := mkKP(t)
	seed64 := PubB64(kp1.Priv.Seed()) // base64 of 32-byte seed
	kp2, err := LoadOrCreateKey(seed64, "")
	if err != nil {
		t.Fatal(err)
	}
	if PubB64(kp1.Pub) != PubB64(kp2.Pub) {
		t.Fatal("seed reload should reproduce the same pubkey")
	}
}

// TestLoadOrCreateKeyFromFile verifies file round-trip (persisted 0600 seed).
func TestLoadOrCreateKeyFromFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/brain.key"
	kp1, err := LoadOrCreateKey("", path)
	if err != nil {
		t.Fatal(err)
	}
	// reload from file — must reproduce the same pubkey
	kp2, err := LoadOrCreateKey("", path)
	if err != nil {
		t.Fatal(err)
	}
	if PubB64(kp1.Pub) != PubB64(kp2.Pub) {
		t.Fatal("file reload must reproduce the same pubkey")
	}
}

// TestKeyFilePerm verifies the persisted seed file is 0600.
func TestKeyFilePerm(t *testing.T) {
	path := t.TempDir() + "/brain.key"
	if _, err := LoadOrCreateKey("", path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("key file perm must be 0600, got %v", info.Mode().Perm())
	}
}

// TestLoadOrCreateKeyPersistFailureIsLoud verifies that if the seed cannot be persisted,
// LoadOrCreateKey returns an error rather than silently returning an unpersisted key
// (which would churn the brain's identity on restart).
func TestLoadOrCreateKeyPersistFailureIsLoud(t *testing.T) {
	// keyFile whose parent directory does not exist → WriteFile fails.
	path := filepath.Join(t.TempDir(), "nonexistent-dir", "brain.key")
	if _, err := LoadOrCreateKey("", path); err == nil {
		t.Fatal("LoadOrCreateKey must return an error when the key cannot be persisted")
	}
}

// TestLoadOrCreateKeyPersistFailureReadOnlyDir is a second, stronger check: the parent dir
// exists but is not writable. Skipped when running as root (root bypasses perms).
func TestLoadOrCreateKeyPersistFailureReadOnlyDir(t *testing.T) {
	if os.Geteuid() == 0 || runtime.GOOS == "windows" {
		t.Skip("cannot enforce dir perms as root / on windows")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil { // read+execute, no write
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) // restore so TempDir cleanup works
	if _, err := LoadOrCreateKey("", filepath.Join(dir, "brain.key")); err == nil {
		t.Fatal("LoadOrCreateKey must error when the key dir is not writable")
	}
}

// TestVerifyRejectsNonFiniteTS ensures NaN/Inf timestamps are rejected on the verify path.
func TestVerifyRejectsNonFiniteTS(t *testing.T) {
	kp := mkKP(t)
	pub := PubB64(kp.Pub)
	for _, ts := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		// Sign over the same non-finite ts so only the finiteness guard can reject it.
		sig := Sign(kp, "a", "claim", "x", ts, "n", 1300.0)
		if err := Verify(pub, "a", "claim", "x", ts, "n", sig, 1300.0, 0, 300); err == nil {
			t.Fatalf("non-finite ts=%v must be rejected", ts)
		}
	}
}

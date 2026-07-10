// SPDX-License-Identifier: Elastic-2.0

package creds

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/zalando/go-keyring"
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

func TestResolveIdentityCorruptSystemdCredFailsClosed(t *testing.T) {
	credsDir := t.TempDir()
	systemdDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(systemdDir, "age-identity"), []byte("not-a-valid-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CREDENTIALS_DIRECTORY", systemdDir)
	t.Setenv("CORRAL_AGE_IDENTITY", "")

	if _, err := resolveIdentity(credsDir); err == nil {
		t.Fatal("resolveIdentity must return an error when the systemd cred file is corrupt, not silently generate a new identity")
	}
	if _, err := os.Stat(filepath.Join(credsDir, "identity.age")); !os.IsNotExist(err) {
		t.Fatal("resolveIdentity must NOT orphan the store by generating a new identity.age when the systemd cred is corrupt")
	}
}

func TestResolveIdentityValidSystemdCred(t *testing.T) {
	credsDir := t.TempDir()
	systemdDir := t.TempDir()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(systemdDir, "age-identity"), []byte(id.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CREDENTIALS_DIRECTORY", systemdDir)
	t.Setenv("CORRAL_AGE_IDENTITY", "")

	got, err := resolveIdentity(credsDir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Recipient().String() != id.Recipient().String() {
		t.Fatalf("resolveIdentity returned a different identity than the systemd cred file provided")
	}
}

func TestResolveIdentityValidEnvVar(t *testing.T) {
	credsDir := t.TempDir()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	t.Setenv("CORRAL_AGE_IDENTITY", id.String())

	got, err := resolveIdentity(credsDir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Recipient().String() != id.Recipient().String() {
		t.Fatal("resolveIdentity returned a different identity than CORRAL_AGE_IDENTITY provided")
	}
}

func TestResolveIdentityMalformedEnvVarFailsClosed(t *testing.T) {
	credsDir := t.TempDir()
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	t.Setenv("CORRAL_AGE_IDENTITY", "garbage-not-an-age-key")

	if _, err := resolveIdentity(credsDir); err == nil {
		t.Fatal("resolveIdentity must return an error when CORRAL_AGE_IDENTITY is set but malformed, not silently ignore it")
	}
	if _, err := os.Stat(filepath.Join(credsDir, "identity.age")); !os.IsNotExist(err) {
		t.Fatal("resolveIdentity must NOT generate a new identity.age when CORRAL_AGE_IDENTITY is malformed")
	}
}

func TestAgeFileLazyDoesNotResolveUntilFirstUse(t *testing.T) {
	dir := t.TempDir()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	b := newAgeFileLazy(filepath.Join(dir, "creds.age"), func() (*age.X25519Identity, error) {
		calls++
		return id, nil
	})
	if calls != 0 {
		t.Fatalf("resolver called %d times at construction, want 0", calls)
	}
	if _, _, err := b.get("OPENAI_API_KEY"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("resolver called %d times after first get, want 1", calls)
	}
	if _, _, err := b.get("OPENAI_API_KEY"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("resolver called %d times after second get, want 1 (cached)", calls)
	}
}

func TestOpenWithKeyringDoesNotMintIdentityFile(t *testing.T) {
	keyring.MockInit() // in-memory; never touches the OS keychain
	dir := t.TempDir()
	t.Setenv("CORRAL_CREDS_DIR", dir)
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	t.Setenv("CORRAL_AGE_IDENTITY", "")

	st, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Set("OPENAI_API_KEY", "sk-keyring-only"); err != nil {
		t.Fatal(err)
	}
	got, ok, err := st.Get("OPENAI_API_KEY")
	if err != nil || !ok || got != "sk-keyring-only" {
		t.Fatalf("Get = %q ok=%v err=%v", got, ok, err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "identity.age")); !os.IsNotExist(statErr) {
		t.Fatal("identity.age was minted even though the keyring satisfied the Set/Get — age tier should never have been touched")
	}
}

func TestResolveIdentityFallsThroughToKeyfileWhenUnset(t *testing.T) {
	credsDir := t.TempDir()
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	t.Setenv("CORRAL_AGE_IDENTITY", "")

	id, err := resolveIdentity(credsDir)
	if err != nil {
		t.Fatal(err)
	}
	if id == nil {
		t.Fatal("resolveIdentity should have generated and returned a fresh identity")
	}
	if _, err := os.Stat(filepath.Join(credsDir, "identity.age")); err != nil {
		t.Fatalf("resolveIdentity should have persisted a new identity.age in credsDir: %v", err)
	}
}

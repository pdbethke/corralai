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

// SPDX-License-Identifier: Elastic-2.0

package buildstore

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveGetRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "build.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	stmt := `{"predicateType":"https://slsa.dev/provenance/v1","subject":[{"name":"corral"}]}`
	steps := `[{"seq":0,"kind":"context","hash":"abc"}]`
	id, err := s.Save("pdbethke/corralai", "abc123", "feat/x", "peter", "deadbeef", "sig-bytes-hex", stmt, steps)
	if err != nil {
		t.Fatal(err)
	}
	if id == 0 {
		t.Fatal("expected a non-zero id")
	}

	got, ok, err := s.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected found=true for a saved id")
	}
	if got["predicateType"] != "https://slsa.dev/provenance/v1" {
		t.Fatalf("statement not round-tripped correctly: %v", got)
	}
	rawSteps, ok := got["steps"]
	if !ok {
		t.Fatal("expected a \"steps\" key in the returned map")
	}
	stepsList, ok := rawSteps.([]any)
	if !ok || len(stepsList) != 1 {
		t.Fatalf("steps not round-tripped correctly: %v (%T)", rawSteps, rawSteps)
	}
	stepObj, ok := stepsList[0].(map[string]any)
	if !ok || stepObj["kind"] != "context" {
		t.Fatalf("steps[0] not round-tripped correctly: %v", stepsList[0])
	}

	// Absent id.
	_, ok, err = s.Get(id + 999)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected found=false for an absent id")
	}
}

func TestSaveAssignsIncreasingIDs(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "build.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	id1, err := s.Save("r", "c1", "b", "a", "h1", "sig1", `{"n":1}`, `[]`)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := s.Save("r", "c2", "b", "a", "h2", "sig2", `{"n":2}`, `[]`)
	if err != nil {
		t.Fatal(err)
	}
	if id2 <= id1 {
		t.Fatalf("expected increasing ids, got id1=%d id2=%d", id1, id2)
	}
}

func TestLoadOrCreateSigningKeyPersistsAndReloads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "certify.key")

	priv1, err := LoadOrCreateSigningKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(priv1) != ed25519.PrivateKeySize {
		t.Fatalf("unexpected key size %d", len(priv1))
	}

	// Seed file must be 0600.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("seed file mode = %v, want 0600", fi.Mode().Perm())
	}

	priv2, err := LoadOrCreateSigningKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(priv1, priv2) {
		t.Fatal("reloading from the same path must return the byte-identical key")
	}
}

func TestLoadOrCreateSigningKeyEnvOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "unused.key")

	seed := bytes.Repeat([]byte{0x42}, ed25519.SeedSize)
	t.Setenv("CORRALAI_CERTIFY_KEY", hex.EncodeToString(seed))

	priv, err := LoadOrCreateSigningKey(path)
	if err != nil {
		t.Fatal(err)
	}
	want := ed25519.NewKeyFromSeed(seed)
	if !bytes.Equal(priv, want) {
		t.Fatal("env override must produce the key derived from the given seed")
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("env override must not write a seed file")
	}
}

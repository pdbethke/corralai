// SPDX-License-Identifier: Elastic-2.0

package creds

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"filippo.io/age"
)

// ageFile is a writable backend storing a name→secret map in a single
// age-encrypted file. Only ciphertext touches disk.
type ageFile struct {
	path string

	id      *age.X25519Identity
	resolve func() (*age.X25519Identity, error)
	idOnce  sync.Once
	idErr   error
}

// newAgeFile builds a backend with an already-resolved identity — used by
// tests and anywhere the identity is known up front.
func newAgeFile(path string, id *age.X25519Identity) *ageFile {
	return &ageFile{path: path, id: id}
}

// newAgeFileLazy builds a backend that only resolves its identity (via
// resolve) the first time it is actually read from or written to. This keeps
// resolveIdentity — and any plaintext identity.age it might mint — from ever
// running for an operator whose secrets are satisfied by an earlier tier
// (env, OS keyring) in the chain.
func newAgeFileLazy(path string, resolve func() (*age.X25519Identity, error)) *ageFile {
	return &ageFile{path: path, resolve: resolve}
}

// identity returns the concrete age identity, resolving it lazily (and only
// once) via the configured resolver if one wasn't supplied up front.
func (b *ageFile) identity() (*age.X25519Identity, error) {
	if b.id != nil {
		return b.id, nil
	}
	b.idOnce.Do(func() {
		b.id, b.idErr = b.resolve()
	})
	return b.id, b.idErr
}

func (b *ageFile) load() (map[string]string, error) {
	id, err := b.identity()
	if err != nil {
		return nil, err
	}
	f, err := os.Open(b.path) // #nosec G304 -- path is the operator's own creds dir
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r, err := age.Decrypt(f, id)
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
	id, err := b.identity()
	if err != nil {
		return err
	}
	dir := filepath.Dir(b.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, id.Recipient())
	if err != nil {
		return err
	}
	if _, err := w.Write(raw); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	// Atomic replace: write ciphertext to a temp file in the same directory,
	// then rename over the real path. A crash or disk-full before the rename
	// leaves the existing store untouched instead of a torn, corrupted
	// read-modify-write of the whole map.
	tmp := b.path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, b.path); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Chmod(b.path, 0o600)
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
	if err := os.Chmod(idPath, 0o600); err != nil {
		return nil, err
	}
	return id, nil
}

// resolveIdentity finds the age identity: env CORRAL_AGE_IDENTITY (a literal
// AGE-SECRET-KEY-…) → systemd credential ($CREDENTIALS_DIRECTORY/age-identity) →
// a 0600 key file in credsDir. This is the master-protection chain from the spec.
func resolveIdentity(credsDir string) (*age.X25519Identity, error) {
	if v := strings.TrimSpace(os.Getenv("CORRAL_AGE_IDENTITY")); v != "" {
		if !strings.HasPrefix(v, "AGE-SECRET-KEY-") {
			return nil, fmt.Errorf("CORRAL_AGE_IDENTITY is set but is not a valid age identity")
		}
		return age.ParseX25519Identity(v) // parse err returned, not swallowed
	}
	if d := os.Getenv("CREDENTIALS_DIRECTORY"); d != "" {
		raw, err := os.ReadFile(filepath.Join(d, "age-identity")) // #nosec G304 -- systemd cred dir
		switch {
		case err == nil:
			return age.ParseX25519Identity(strings.TrimSpace(string(raw))) // parse err returned, not swallowed
		case !os.IsNotExist(err):
			return nil, err // exists but unreadable -> fail closed
		}
		// not present -> fall through
	}
	return loadOrCreateIdentity(filepath.Join(credsDir, "identity.age"))
}

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

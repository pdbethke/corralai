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
	"path/filepath"
	"sort"

	"filippo.io/age"
	"github.com/zalando/go-keyring"
)

var errReadOnly = errors.New("creds: backend is read-only")

// CanonicalNames are the provider/token secrets corral knows by convention,
// named after their env var so an env override is transparent. Exported so
// callers outside the package (e.g. cmd/corral-agent's env-scrub) can act on
// the same list without duplicating it.
var CanonicalNames = []string{
	"OPENAI_API_KEY", "GEMINI_API_KEY", "ANTHROPIC_API_KEY",
	"OPENROUTER_API_KEY", "CORRALAI_BRAIN_KEY", "CORRALAI_BRAIN_TOKEN",
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
		_, ok, err := b.get(name)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if ok {
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

// Redact returns a non-reversible fingerprint of a secret for logs/errors:
// for secrets longer than 8 characters, the first 4 characters plus the
// total length; for secrets of 8 characters or fewer, just the length
// (masked, so a short secret doesn't reveal most or all of itself). Never
// returns the value.
func Redact(secret string) string {
	if secret == "" {
		return "(empty)"
	}
	// Only reveal a 4-char prefix when the secret is comfortably longer than
	// that prefix; otherwise a short secret would show most or all of itself.
	if len(secret) <= 8 {
		return fmt.Sprintf("****(%d)", len(secret))
	}
	return fmt.Sprintf("%s…(%d chars)", secret[:4], len(secret))
}

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

// keyringUsable probes the OS keyring with a harmless lookup of a key that is
// never expected to exist. On a headless Linux host with no Secret Service
// daemon, the underlying D-Bus call fails with a connection error rather than
// go-keyring's ErrNotFound sentinel; go-keyring does not export a distinct
// sentinel for "backend unavailable" (verified against the pinned
// github.com/zalando/go-keyring v0.2.8: keyring_unix.go's secretServiceProvider
// simply propagates whatever error ss.NewSecretService()/dbus returns). So we
// take the conservative reading: ErrNotFound means the keyring backend answered
// and is usable; any other error (including "no backend available") means it
// is not, and the chain must fall back to the age file rather than error out.
func keyringUsable() bool {
	_, err := keyring.Get("corral", "__corral_probe__")
	return err == nil || errors.Is(err, keyring.ErrNotFound)
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
	chain = append(chain, newAgeFileLazy(filepath.Join(dir, "creds.age"), func() (*age.X25519Identity, error) {
		return resolveIdentity(dir)
	}))
	return newStore(chain...), nil
}

// envBackend reads canonical secrets from the process environment. Read-only.
type envBackend struct{}

func (envBackend) get(name string) (string, bool, error) {
	v, ok := os.LookupEnv(name)
	return v, ok && v != "", nil
}
func (envBackend) set(string, string) error { return errReadOnly }
func (envBackend) remove(string) error      { return errReadOnly }
func (envBackend) writable() bool           { return false }
func (envBackend) names() ([]string, error) {
	var out []string
	for _, n := range CanonicalNames {
		if v := os.Getenv(n); v != "" {
			out = append(out, n)
		}
	}
	return out, nil
}

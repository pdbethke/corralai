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

// Redact returns a non-reversible fingerprint of a secret for logs/errors: the
// first 4 characters plus the length. Never returns the value.
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

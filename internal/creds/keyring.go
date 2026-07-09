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

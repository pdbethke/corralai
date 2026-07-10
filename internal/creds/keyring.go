// SPDX-License-Identifier: Elastic-2.0

package creds

import (
	"encoding/json"
	"errors"
	"sort"

	"github.com/zalando/go-keyring"
)

// dataPrefix namespaces every user secret's keyring key so it can never
// collide with indexKey (indexKey does not start with dataPrefix, and every
// data key does, so the two key spaces are disjoint by construction).
const dataPrefix = "secret:"

// indexKey holds the JSON-encoded list of user-chosen names. Because it does
// not start with dataPrefix, no data key set via dataKey() can ever equal it,
// regardless of what name the caller chooses (including "__index__" itself,
// which becomes "secret:__index__" as a data key).
const indexKey = "__index__"

func dataKey(name string) string { return dataPrefix + name }

// keyringBackend stores secrets in the OS keyring (macOS Keychain, Windows
// Credential Manager, Linux Secret Service). go-keyring cannot enumerate entries,
// so we maintain a names index under a reserved key that lives in a disjoint
// namespace from the data keys (see dataPrefix/indexKey).
type keyringBackend struct{ service string }

func newKeyring(service string) *keyringBackend { return &keyringBackend{service: service} }

func (b *keyringBackend) get(name string) (string, bool, error) {
	v, err := keyring.Get(b.service, dataKey(name))
	if errors.Is(err, keyring.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

func (b *keyringBackend) index() ([]string, error) {
	v, err := keyring.Get(b.service, indexKey)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	if err := json.Unmarshal([]byte(v), &names); err != nil {
		return nil, err
	}
	return names, nil
}

func (b *keyringBackend) setIndex(names []string) error {
	raw, err := json.Marshal(names)
	if err != nil {
		return err
	}
	return keyring.Set(b.service, indexKey, string(raw))
}

func (b *keyringBackend) set(name, value string) error {
	if err := keyring.Set(b.service, dataKey(name), value); err != nil {
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
	if err := keyring.Delete(b.service, dataKey(name)); err != nil && !errors.Is(err, keyring.ErrNotFound) {
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

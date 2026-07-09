// SPDX-License-Identifier: Elastic-2.0

package creds

import (
	"sort"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestKeyringBackendRoundTripAndIndex(t *testing.T) {
	keyring.MockInit() // in-memory; never touches the OS keychain
	b := newKeyring("corral-test")

	if err := b.set("OPENAI_API_KEY", "sk-1"); err != nil {
		t.Fatal(err)
	}
	if err := b.set("GEMINI_API_KEY", "g-2"); err != nil {
		t.Fatal(err)
	}
	v, ok, err := b.get("OPENAI_API_KEY")
	if err != nil || !ok || v != "sk-1" {
		t.Fatalf("get = %q ok=%v err=%v", v, ok, err)
	}
	if _, ok, _ := b.get("NOPE"); ok {
		t.Fatal("missing key must be not-found, not an error")
	}
	names, _ := b.names()
	sort.Strings(names)
	if strings.Join(names, ",") != "GEMINI_API_KEY,OPENAI_API_KEY" {
		t.Fatalf("names index = %v", names)
	}
	// The index entry itself must never surface as a listable name.
	for _, n := range names {
		if n == "__names__" {
			t.Fatal("the internal index key leaked into names()")
		}
	}
	if err := b.remove("OPENAI_API_KEY"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := b.get("OPENAI_API_KEY"); ok {
		t.Fatal("removed key still present")
	}
	names, _ = b.names()
	if len(names) != 1 || names[0] != "GEMINI_API_KEY" {
		t.Fatalf("index not updated on remove: %v", names)
	}
}

// TestKeyringBackendNameCollisionWithInternalKeys guards against the
// namespace collision where a user secret named after an internal key
// (the old "__names__" index key, a new "__index__" key, or a
// "secret:"-prefixed data key) would corrupt or shadow the index.
func TestKeyringBackendNameCollisionWithInternalKeys(t *testing.T) {
	keyring.MockInit() // in-memory; never touches the OS keychain
	b := newKeyring("corral-test-collision")

	cases := map[string]string{
		"__names__":  "v1",
		"__index__":  "v2",
		"secret:foo": "v3",
	}

	for name, value := range cases {
		if err := b.set(name, value); err != nil {
			t.Fatalf("set(%q) failed: %v", name, err)
		}
	}

	for name, want := range cases {
		got, ok, err := b.get(name)
		if err != nil {
			t.Fatalf("get(%q) error: %v", name, err)
		}
		if !ok {
			t.Fatalf("get(%q) not found", name)
		}
		if got != want {
			t.Fatalf("get(%q) = %q, want %q", name, got, want)
		}
	}

	names, err := b.names()
	if err != nil {
		t.Fatalf("names() error: %v", err)
	}
	wantNames := []string{"__index__", "__names__", "secret:foo"}
	sort.Strings(names)
	if strings.Join(names, ",") != strings.Join(wantNames, ",") {
		t.Fatalf("names() = %v, want %v", names, wantNames)
	}
	// Every entry in names() must be one of the exact user-chosen names above
	// (not, say, "secret:__index__" or some other mangled internal key).
	for _, n := range names {
		if _, ok := cases[n]; !ok {
			t.Fatalf("names() contains unexpected entry %q (want only user-chosen names): %v", n, names)
		}
	}

	// The index must not be corrupted: a further normal set/names round trip
	// must still work correctly after the collision-prone names were stored.
	if err := b.set("ANTHROPIC_API_KEY", "sk-ant-1"); err != nil {
		t.Fatalf("set(ANTHROPIC_API_KEY) after collision names failed: %v", err)
	}
	v, ok, err := b.get("ANTHROPIC_API_KEY")
	if err != nil || !ok || v != "sk-ant-1" {
		t.Fatalf("get(ANTHROPIC_API_KEY) = %q ok=%v err=%v", v, ok, err)
	}
	names, err = b.names()
	if err != nil {
		t.Fatalf("names() error after further set: %v", err)
	}
	wantNames = []string{"ANTHROPIC_API_KEY", "__index__", "__names__", "secret:foo"}
	sort.Strings(names)
	if strings.Join(names, ",") != strings.Join(wantNames, ",") {
		t.Fatalf("names() after further set = %v, want %v", names, wantNames)
	}
}

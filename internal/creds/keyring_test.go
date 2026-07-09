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

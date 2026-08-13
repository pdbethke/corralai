// SPDX-License-Identifier: Elastic-2.0

package reposcan

import "testing"

func TestCanonicalKVIsOrderIndependent(t *testing.T) {
	a := CanonicalKV(map[string]string{"writer": "claude-sonnet-5", "critic": "gemini-3.6-flash"})
	b := CanonicalKV(map[string]string{"critic": "gemini-3.6-flash", "writer": "claude-sonnet-5"})
	if a != b {
		t.Fatalf("map order changed the serialization: %q vs %q", a, b)
	}
	if want := "critic=gemini-3.6-flash,writer=claude-sonnet-5"; a != want {
		t.Fatalf("CanonicalKV = %q, want %q", a, want)
	}
}

func TestCanonicalKVOmitsEmptyValues(t *testing.T) {
	// An unset role must serialize identically to an absent one, or the same
	// audit keys two different ways depending on how the caller spelled it.
	got := CanonicalKV(map[string]string{"writer": "claude-sonnet-5", "shadow": ""})
	if want := "writer=claude-sonnet-5"; got != want {
		t.Fatalf("CanonicalKV = %q, want %q", got, want)
	}
}

func TestCanonicalKVEmptyMap(t *testing.T) {
	if got := CanonicalKV(nil); got != "" {
		t.Fatalf("CanonicalKV(nil) = %q, want empty", got)
	}
}

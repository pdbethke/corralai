// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"os"
	"testing"
)

// TestScrubSecrets verifies that scrubSecrets removes each key from the
// process environment. This is the unit-level proof of the getenv exfiltration
// defense: once a secret is loaded into its owning struct and scrubSecrets is
// called, os.Getenv (and therefore DuckDB's getenv()) returns "".
func TestScrubSecrets(t *testing.T) {
	const key = "TEST_CORRALAI_SCRUB_CANARY"
	os.Setenv(key, "hunter2")
	if got := os.Getenv(key); got == "" {
		t.Fatal("precondition failed: env var was not set before scrub")
	}
	scrubSecrets([]string{key})
	if got := os.Getenv(key); got != "" {
		t.Fatalf("scrubSecrets: expected empty string after scrub, got %q", got)
	}
}

// TestScrubSecretsMultiple verifies that all listed keys are cleared in one call.
func TestScrubSecretsMultiple(t *testing.T) {
	keys := []string{"TEST_SCRUB_A", "TEST_SCRUB_B", "TEST_SCRUB_C"}
	for _, k := range keys {
		os.Setenv(k, "secret-value")
	}
	scrubSecrets(keys)
	for _, k := range keys {
		if got := os.Getenv(k); got != "" {
			t.Errorf("scrubSecrets: key %s: expected empty, got %q", k, got)
		}
	}
}

// TestScrubSecretsIdempotent verifies that calling scrubSecrets on a key that
// is already unset does not panic or error.
func TestScrubSecretsIdempotent(t *testing.T) {
	const key = "TEST_SCRUB_IDEMPOTENT_NOTSET"
	os.Unsetenv(key) // ensure absent
	scrubSecrets([]string{key})
	if got := os.Getenv(key); got != "" {
		t.Fatalf("expected empty after idempotent scrub, got %q", got)
	}
}

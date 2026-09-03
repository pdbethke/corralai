// SPDX-License-Identifier: Elastic-2.0

package console

import (
	"os"
	"testing"

	"github.com/pdbethke/corralai/internal/consolebundle"
)

// TestMain opts this package's tests into the PUBLISHED development key, and
// the fact that it must is the point.
//
// consolebundle.TrustAnchor has no default. Before that, the anchor was a
// hardcoded constant whose private half is committed to this public
// repository, so anyone could sign a bundle these clients would verify, cache
// and render same-origin with the console session. Nineteen tests in this
// package went red the moment the default was removed — every one of them had
// been silently relying on an anchor an attacker could also use.
//
// Opting in HERE, once and visibly, keeps that property visible: a future test
// that verifies a signature is exercising a key this repository publishes, not
// a release key, and the log line the client emits says so at runtime too.
func TestMain(m *testing.M) {
	if err := os.Setenv(consolebundle.DevAnchorEnv, "1"); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

// TestTrustAnchorHasNoForgeableDefault is the regression for the security
// defect itself: with no environment configured, a client must REFUSE rather
// than fall back to a key whose private half is published.
//
// It deliberately clears both variables rather than trusting the ambient
// environment, because TestMain above sets one of them — a test that passed
// only because of its own harness would be exactly the kind of evidence this
// repository does not accept.
func TestTrustAnchorHasNoForgeableDefault(t *testing.T) {
	t.Setenv(consolebundle.TrustAnchorEnv, "")
	t.Setenv(consolebundle.DevAnchorEnv, "")

	got, source, err := consolebundle.TrustAnchor()
	if err == nil {
		t.Fatalf("TrustAnchor returned %q from %q with nothing configured — a default anchor whose private key is published converts \"unverified\" into \"verified\"", got, source)
	}
	if got != "" {
		t.Errorf("a refused anchor must return an empty key, got %q", got)
	}
	if got == consolebundle.DevPubKeyHex {
		t.Error("the published DEV key must never be returned without an explicit opt-in")
	}
}

// TestTrustAnchorPrefersTheConfiguredKey pins the order: an operator's own
// release key wins over the dev opt-in, so a machine that has both set cannot
// silently fall back to the published one.
func TestTrustAnchorPrefersTheConfiguredKey(t *testing.T) {
	const real = "1111111111111111111111111111111111111111111111111111111111111111"
	t.Setenv(consolebundle.TrustAnchorEnv, real)
	t.Setenv(consolebundle.DevAnchorEnv, "1")

	got, source, err := consolebundle.TrustAnchor()
	if err != nil {
		t.Fatalf("TrustAnchor: %v", err)
	}
	if got != real {
		t.Errorf("anchor = %q from %q, want the configured release key", got, source)
	}
	if source != consolebundle.TrustAnchorEnv {
		t.Errorf("source = %q, want %q", source, consolebundle.TrustAnchorEnv)
	}
}

// TestTrustAnchorRejectsAMalformedKey: a truncated or non-hex value must be an
// error, never silently treated as absent (which would fall through to the dev
// key when that opt-in is also set).
func TestTrustAnchorRejectsAMalformedKey(t *testing.T) {
	t.Setenv(consolebundle.DevAnchorEnv, "1")
	for _, bad := range []string{"nothex", "1111", "zz11111111111111111111111111111111111111111111111111111111111111"} {
		t.Setenv(consolebundle.TrustAnchorEnv, bad)
		got, _, err := consolebundle.TrustAnchor()
		if err == nil {
			t.Errorf("TrustAnchor accepted %q and returned %q — a malformed anchor must fail loudly, not fall back", bad, got)
		}
	}
}

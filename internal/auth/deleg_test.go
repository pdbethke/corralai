// SPDX-License-Identifier: Elastic-2.0

package auth

import (
	"context"
	"testing"
	"time"
)

func delegVerifier(t *testing.T) *Verifier {
	t.Helper()
	vf, err := NewVerifier(context.Background(), nil) // no OIDC clients
	if err != nil {
		t.Fatalf("new verifier: %v", err)
	}
	vf.EnableDelegation([]byte("a-32-byte-test-delegation-key!!!"))
	return vf
}

func TestDelegationRoundTrip(t *testing.T) {
	vf := delegVerifier(t)
	tok, err := vf.MintDelegation("boss@x.com", "boss@x.com/tester", time.Minute)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	ti, err := vf.VerifyToken(context.Background(), tok, nil)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	// UserID is the PRINCIPAL (authz rolls up); subagent identity rides in Extra.
	if ti.UserID != "boss@x.com" {
		t.Fatalf("UserID=%q want principal", ti.UserID)
	}
	if ti.Extra["subagent"] != "boss@x.com/tester" {
		t.Fatalf("subagent claim=%v", ti.Extra["subagent"])
	}
}

func TestDelegationExpired(t *testing.T) {
	vf := delegVerifier(t)
	tok, _ := vf.MintDelegation("p@x", "p@x/c", -time.Second) // already expired
	if _, err := vf.VerifyToken(context.Background(), tok, nil); err == nil {
		t.Fatal("expired delegation token must be rejected")
	}
}

func TestDelegationTampered(t *testing.T) {
	vf := delegVerifier(t)
	tok, _ := vf.MintDelegation("p@x", "p@x/c", time.Minute)
	bad := tok[:len(tok)-1] + "X" // flip the last signature byte
	if _, err := vf.VerifyToken(context.Background(), bad, nil); err == nil {
		t.Fatal("tampered signature must be rejected")
	}
}

func TestDelegationWrongKeyRejected(t *testing.T) {
	mint := delegVerifier(t)
	tok, _ := mint.MintDelegation("p@x", "p@x/c", time.Minute)
	other, _ := NewVerifier(context.Background(), nil)
	other.EnableDelegation([]byte("a-DIFFERENT-32-byte-delegation-k"))
	if _, err := other.VerifyToken(context.Background(), tok, nil); err == nil {
		t.Fatal("token signed by another key must be rejected")
	}
}

func TestDelegationDisabledErrors(t *testing.T) {
	vf, _ := NewVerifier(context.Background(), nil) // delegation NOT enabled
	if _, err := vf.MintDelegation("p@x", "p@x/c", time.Minute); err == nil {
		t.Fatal("minting with delegation disabled must error")
	}
}

func TestEnableDelegationRejectsShortKey(t *testing.T) {
	vf, _ := NewVerifier(context.Background(), nil)
	vf.EnableDelegation([]byte("tiny")) // < floor
	if vf.delegKey != nil {
		t.Fatal("short delegation key must be rejected")
	}
	if _, err := vf.MintDelegation("p@x", "p@x/c", time.Minute); err == nil {
		t.Fatal("minting must still fail: delegation stays disabled with a short key")
	}
}

func TestEnableDelegationAccepts32ByteKey(t *testing.T) {
	vf := delegVerifier(t) // uses the exact 32-byte key
	if vf.delegKey == nil {
		t.Fatal("32-byte key must enable delegation")
	}
	if _, err := vf.MintDelegation("p@x", "p@x/c", time.Minute); err != nil {
		t.Fatalf("mint with 32-byte key should succeed: %v", err)
	}
}

// TestDelegationTTLIsClampedAndRevocable pins two halves of the sixth
// review's finding #3. A caller's ttl used to pass straight into the claim
// (a worker asked for ~3 years and got it); and a despawned subagent's
// token stayed valid because tokens are stateless HMAC blobs and nothing
// consulted the coordination store. Now: the TTL is clamped to
// MaxDelegationTTL, and a revocation predicate can refuse a live-looking
// token for a subagent that no longer exists.
func TestDelegationTTLIsClampedAndRevocable(t *testing.T) {
	vf := delegVerifier(t)
	tok, err := vf.MintDelegation("boss@x.com", "boss@x.com/worker", 100000000*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ti, err := vf.VerifyToken(context.Background(), tok, nil)
	if err != nil {
		t.Fatal(err)
	}
	if left := time.Until(ti.Expiration); left > MaxDelegationTTL+time.Minute {
		t.Errorf("a ~3-year TTL was honoured: %s left, want <= %s", left, MaxDelegationTTL)
	}

	// Revocation: the subagent is despawned; the same token must die.
	alive := true
	vf.SetRevocationCheck(func(p, sub string) bool { return !(alive && sub == "boss@x.com/worker") })
	if _, err := vf.VerifyToken(context.Background(), tok, nil); err != nil {
		t.Fatalf("a live subagent's token was refused: %v", err)
	}
	alive = false
	if _, err := vf.VerifyToken(context.Background(), tok, nil); err == nil {
		t.Error("a despawned subagent's token still authenticates — despawn is not revocation")
	}
	// Observer tokens are not tied to a subagent row and stay valid.
	obs, _ := vf.MintObserver("boss@x.com", time.Minute)
	if _, err := vf.VerifyToken(context.Background(), obs, nil); err != nil {
		t.Errorf("an observer token was caught by subagent revocation: %v", err)
	}
}

// TestPickPrincipalRefusesAnEmailShapedUsernameBesideAnUnverifiedEmail: an
// unverified email was skipped and preferred_username returned bare, so a
// token carrying email=attacker@evil (unverified) and
// preferred_username=boss@x.com mapped to the superuser.
func TestPickPrincipalRefusesAnEmailShapedUsernameBesideAnUnverifiedEmail(t *testing.T) {
	if got := pickPrincipal("attacker@evil.example", false, "boss@x.com", "app", ""); got == "boss@x.com" {
		t.Fatalf("pickPrincipal = %q — a human-controlled username impersonated a principal", got)
	} else if got != "client:app" {
		t.Errorf("pickPrincipal = %q, want the machine claim", got)
	}
	// The two agreeing, or no email claim at all, keep the old behaviour.
	if got := pickPrincipal("me@x.com", false, "me@x.com", "", ""); got != "me@x.com" {
		t.Errorf("agreeing claims: %q", got)
	}
	if got := pickPrincipal("", false, "login@corral.id.example", "", ""); got != "login@corral.id.example" {
		t.Errorf("username-only token: %q", got)
	}
}

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

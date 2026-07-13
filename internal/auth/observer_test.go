// SPDX-License-Identifier: Elastic-2.0

package auth

import (
	"context"
	"testing"
	"time"
)

func TestObserverTokenIsReadOnly(t *testing.T) {
	vf := &Verifier{}
	vf.EnableDelegation([]byte("test-delegation-key-that-is-32-bytes!!"))

	// An observer token authenticates as the principal but carries readonly=true.
	tok, err := vf.MintObserver("alice@x.com", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ti, err := vf.VerifyToken(context.Background(), tok, nil)
	if err != nil {
		t.Fatalf("verify observer: %v", err)
	}
	if ti.UserID != "alice@x.com" {
		t.Fatalf("principal = %q, want alice@x.com (so it still passes the allowlist)", ti.UserID)
	}
	if ro, _ := ti.Extra["readonly"].(bool); !ro {
		t.Fatalf("observer token must carry readonly=true, got %+v", ti.Extra)
	}

	// A normal delegation token is NOT read-only.
	d, err := vf.MintDelegation("alice@x.com", "alice@x.com/child", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ti2, err := vf.VerifyToken(context.Background(), d, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ro, _ := ti2.Extra["readonly"].(bool); ro {
		t.Fatal("a normal delegation token must NOT be readonly")
	}
}

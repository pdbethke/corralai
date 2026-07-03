// SPDX-License-Identifier: Elastic-2.0

package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/pdbethke/corralai/internal/learn"
	"github.com/pdbethke/corralai/internal/principals"
)

// fakeBearerVerify wires an httptest request through the real
// sdkauth.RequireBearerToken middleware (the same middleware auth.Verifier.Wrap
// uses in prod) without needing a live OIDC provider: the "token" IS the
// principal email, verified trivially. This exercises the exact code path
// auth.Principal(r.Context()) reads from.
func fakeBearerVerify(_ context.Context, token string, _ *http.Request) (*sdkauth.TokenInfo, error) {
	if token == "" {
		return nil, sdkauth.ErrInvalidToken
	}
	return &sdkauth.TokenInfo{Expiration: time.Now().Add(time.Hour), UserID: token}, nil
}

// bearerWrap authenticates as principal via a fake bearer token.
func bearerWrap(h http.Handler, principal string) http.Handler {
	wrapped := sdkauth.RequireBearerToken(fakeBearerVerify, nil)(h)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+principal)
		wrapped.ServeHTTP(w, r)
	})
}

// TestProposalApproveRejectRequireSuperuser proves the UI's approve/reject
// endpoints refuse a non-superuser bearer even though they pass the
// read-only-observer check — mirroring brain's
// TestApproveProposalRequiresSuperuser so approving from the browser and
// approving over MCP require the same gate. Before the fix, these endpoints
// checked only auth.ReadOnly, so any non-observer bearer (including agent
// delegation tokens) could promote guidance fleet-wide.
func TestProposalApproveRejectRequireSuperuser(t *testing.T) {
	dir := t.TempDir()
	pstore, err := principals.Open(filepath.Join(dir, "p.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pstore.Close() })
	if err := pstore.CreateSuperuser("real-admin@example.com", "test"); err != nil {
		t.Fatal(err)
	}

	lstore, err := learn.Open(filepath.Join(dir, "l.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lstore.Close() })
	p, _, err := lstore.Upsert("missing-req|go.mod", "finding", "builder", []string{"a"})
	if err != nil {
		t.Fatal(err)
	}

	promoteCalled, rejectCalled := false, false
	srv := Handler(Deps{
		Roles: pstore,
		Learn: lstore,
		Promote: func(id int64) error {
			promoteCalled = true
			return nil
		},
		Reject: func(id int64, reason string) error {
			rejectCalled = true
			return nil
		},
	})

	for _, ep := range []string{"/api/proposal/approve", "/api/proposal/reject"} {
		body, _ := json.Marshal(map[string]any{"id": p.ID})
		req := httptest.NewRequest(http.MethodPost, ep, bytes.NewReader(body))
		w := httptest.NewRecorder()
		bearerWrap(srv, "not-an-admin@example.com").ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s: status = %d, want 403; body=%s", ep, w.Code, w.Body.String())
		}
	}
	if promoteCalled || rejectCalled {
		t.Fatal("gate must deny BEFORE calling Promote/Reject")
	}

	got, err := lstore.ByID(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != learn.StatusPending {
		t.Fatalf("proposal status = %q, want pending (gate must actually deny)", got.Status)
	}

	// Sanity: the real superuser IS allowed through (still hits Promote).
	body, _ := json.Marshal(map[string]any{"id": p.ID})
	req := httptest.NewRequest(http.MethodPost, "/api/proposal/approve", bytes.NewReader(body))
	w := httptest.NewRecorder()
	bearerWrap(srv, "real-admin@example.com").ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("superuser approve: status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if !promoteCalled {
		t.Fatal("superuser approve should have called Promote")
	}
}

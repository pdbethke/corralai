// SPDX-License-Identifier: Elastic-2.0

package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
)

// observerWrap authenticates as a read-only observer: a verified bearer whose
// TokenInfo carries Extra["readonly"]=true, exactly what auth.ReadOnly reads and
// what Verifier mints for mint_observer tokens.
func observerWrap(h http.Handler) http.Handler {
	verify := func(_ context.Context, token string, _ *http.Request) (*sdkauth.TokenInfo, error) {
		if token == "" {
			return nil, sdkauth.ErrInvalidToken
		}
		return &sdkauth.TokenInfo{
			Expiration: time.Now().Add(time.Hour),
			UserID:     "watcher@example.com",
			Extra:      map[string]any{"readonly": true},
		}, nil
	}
	wrapped := sdkauth.RequireBearerToken(verify, nil)(h)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("Authorization", "Bearer observer")
		wrapped.ServeHTTP(w, r)
	})
}

// TestObserverCannotDriveNarratorOrOracle proves a read-only observer token is
// refused the /api/ask (narrator) and /api/ask_fleet (NL→SQL) POST endpoints —
// both invoke a model, an action an observer must not trigger. Over MCP the
// equivalent ask_fleet tool is already behind denyReadOnly; the UI HTTP path
// must match. The 403 must fire regardless of whether a narrator/oracle is even
// configured (the gate precedes the availability check), so the observer never
// reaches the model. The GET availability probe on /api/ask_fleet stays open so
// the observer's UI can still decide whether to show the panel.
func TestObserverCannotDriveNarratorOrOracle(t *testing.T) {
	srv := Handler(Deps{}) // no narrator, no oracle wired

	for _, ep := range []string{"/api/ask", "/api/ask_fleet"} {
		body, _ := json.Marshal(map[string]any{"agent": "scout-1", "question": "what did you do?"})
		req := httptest.NewRequest(http.MethodPost, ep, bytes.NewReader(body))
		w := httptest.NewRecorder()
		observerWrap(srv).ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s: observer POST status = %d, want 403; body=%s", ep, w.Code, w.Body.String())
		}
	}

	// The GET availability probe stays open to observers (read-only info).
	req := httptest.NewRequest(http.MethodGet, "/api/ask_fleet", nil)
	w := httptest.NewRecorder()
	observerWrap(srv).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("observer GET /api/ask_fleet probe status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
}

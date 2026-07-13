// SPDX-License-Identifier: Elastic-2.0

package ui

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/pdbethke/corralai/internal/brain"
	"github.com/pdbethke/corralai/internal/coord"
	"github.com/pdbethke/corralai/internal/principals"
	"github.com/pdbethke/corralai/internal/taskartifacts"
)

// subagentWrap authenticates as a delegation token rolled up to principal —
// the UI twin of TestProposalApproveRejectRefuseSubagentToken's local wrap,
// factored out here so all three new gates can reuse it. A token prefixed
// "subagent:" verifies into a TokenInfo carrying Extra["subagent"], exactly
// the shape auth.Subagent(ctx) reads and a human bearer never carries.
func subagentWrap(h http.Handler, principal string) http.Handler {
	verify := func(_ context.Context, token string, _ *http.Request) (*sdkauth.TokenInfo, error) {
		if token == "" {
			return nil, sdkauth.ErrInvalidToken
		}
		if strings.HasPrefix(token, "subagent:") {
			p := strings.TrimPrefix(token, "subagent:")
			return &sdkauth.TokenInfo{
				Expiration: time.Now().Add(time.Hour),
				UserID:     p,
				Extra:      map[string]any{"subagent": p + "/child"},
			}, nil
		}
		return &sdkauth.TokenInfo{Expiration: time.Now().Add(time.Hour), UserID: token}, nil
	}
	wrapped := sdkauth.RequireBearerToken(verify, nil)(h)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("Authorization", "Bearer subagent:"+principal)
		wrapped.ServeHTTP(w, r)
	})
}

// TestInstructRequiresSuperuser proves /api/instruct refuses an observer AND a
// non-superuser/subagent bearer, matching the MCP send_instruction tool's
// canInstruct gate — before the fix this endpoint only checked auth.ReadOnly,
// so any non-observer bearer (including a delegation token) could inject an
// instruction into ANY agent.
func TestInstructRequiresSuperuser(t *testing.T) {
	dir := t.TempDir()
	cstore, err := coord.Open(filepath.Join(dir, "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cstore.Close() })

	pstore, err := principals.Open(filepath.Join(dir, "p.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pstore.Close() })
	if err := pstore.CreateSuperuser("real-admin@example.com", "test"); err != nil {
		t.Fatal(err)
	}

	srv := Handler(Deps{Coord: cstore, Roles: pstore})
	reqBody := `{"target":"scout-1","text":"stop and report"}`

	post := func(h http.Handler) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/instruct", strings.NewReader(reqBody))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	assertUnmutated := func(t *testing.T) {
		t.Helper()
		ins, err := cstore.RecentInstructions("scout-1", 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(ins) != 0 {
			t.Fatalf("instructions = %d, want 0 (store must stay unmutated on a denied request)", len(ins))
		}
	}

	// Observer: refused by the pre-existing auth.ReadOnly check.
	if w := post(observerWrap(srv)); w.Code != http.StatusForbidden {
		t.Fatalf("observer: status = %d, want 403; body=%s", w.Code, w.Body.String())
	}
	assertUnmutated(t)

	// Non-superuser human bearer: must now be refused too.
	if w := post(bearerWrap(srv, "not-an-admin@example.com")); w.Code != http.StatusForbidden {
		t.Fatalf("non-admin: status = %d, want 403; body=%s", w.Code, w.Body.String())
	}
	assertUnmutated(t)

	// Delegation token rolled up to a real superuser: still refused.
	if w := post(subagentWrap(srv, "real-admin@example.com")); w.Code != http.StatusForbidden {
		t.Fatalf("subagent-of-superuser: status = %d, want 403; body=%s", w.Code, w.Body.String())
	}
	assertUnmutated(t)

	// Real superuser: allowed, and it actually queues the instruction.
	if w := post(bearerWrap(srv, "real-admin@example.com")); w.Code != http.StatusOK {
		t.Fatalf("superuser: status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	ins, err := cstore.RecentInstructions("scout-1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ins) != 1 {
		t.Fatalf("instructions after superuser POST = %d, want 1", len(ins))
	}
}

// TestInstructDevModeNilRolesAllowed proves the pre-existing no-Roles-store
// dev mode still works (isSuperuser is permissive when s.roles == nil).
func TestInstructDevModeNilRolesAllowed(t *testing.T) {
	dir := t.TempDir()
	cstore, err := coord.Open(filepath.Join(dir, "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cstore.Close() })

	srv := Handler(Deps{Coord: cstore}) // Roles nil => dev mode
	req := httptest.NewRequest(http.MethodPost, "/api/instruct", strings.NewReader(`{"target":"scout-1","text":"go"}`))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req) // no bearer at all — dev mode
	if w.Code != http.StatusOK {
		t.Fatalf("dev mode: status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
}

// TestInterceptRequiresSuperuser proves /api/mission/intercept — which had NO
// authz gate at all before the fix — now refuses observers, non-superusers,
// and subagent-of-superuser tokens, is POST-only, and leaves the HostBook /
// terminal Registry untouched on every denied path. Its WS twin
// guardTerminalWS already reserves operator control for superusers; this
// closes the matching HTTP hole (a read-only observer could otherwise
// stall/tear down a live agent session).
func TestInterceptRequiresSuperuser(t *testing.T) {
	dir := t.TempDir()
	pstore, err := principals.Open(filepath.Join(dir, "p.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pstore.Close() })
	if err := pstore.CreateSuperuser("real-admin@example.com", "test"); err != nil {
		t.Fatal(err)
	}

	hosts := brain.NewHostBook()
	srv := Handler(Deps{Roles: pstore, Hosts: hosts})

	const agent = "scout-intercept"
	url := "/api/mission/intercept?agent=" + agent + "&enable=true"

	assertUnmutated := func(t *testing.T) {
		t.Helper()
		if hosts.IsInterceptPending(agent) {
			t.Fatal("HostBook mutated on a denied intercept request")
		}
	}

	// Observer: no gate existed before the fix — must now be refused.
	req := httptest.NewRequest(http.MethodPost, url, nil)
	w := httptest.NewRecorder()
	observerWrap(srv).ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("observer: status = %d, want 403; body=%s", w.Code, w.Body.String())
	}
	assertUnmutated(t)

	// Non-superuser human bearer.
	req = httptest.NewRequest(http.MethodPost, url, nil)
	w = httptest.NewRecorder()
	bearerWrap(srv, "not-an-admin@example.com").ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-admin: status = %d, want 403; body=%s", w.Code, w.Body.String())
	}
	assertUnmutated(t)

	// Delegation token rolled up to a real superuser.
	req = httptest.NewRequest(http.MethodPost, url, nil)
	w = httptest.NewRecorder()
	subagentWrap(srv, "real-admin@example.com").ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("subagent-of-superuser: status = %d, want 403; body=%s", w.Code, w.Body.String())
	}
	assertUnmutated(t)

	// GET must be refused (POST-only) even for a real superuser.
	req = httptest.NewRequest(http.MethodGet, url, nil)
	w = httptest.NewRecorder()
	bearerWrap(srv, "real-admin@example.com").ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("superuser GET: status = %d, want 405; body=%s", w.Code, w.Body.String())
	}
	assertUnmutated(t)

	// Real superuser, POST: allowed, and it actually flips the pending flag.
	req = httptest.NewRequest(http.MethodPost, url, nil)
	w = httptest.NewRecorder()
	bearerWrap(srv, "real-admin@example.com").ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("superuser POST: status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if !hosts.IsInterceptPending(agent) {
		t.Fatal("superuser POST should have set intercept pending")
	}
}

// TestInterceptDevModeNilRolesAllowed proves dev mode (no Roles store) still
// works for the intercept endpoint.
func TestInterceptDevModeNilRolesAllowed(t *testing.T) {
	hosts := brain.NewHostBook()
	srv := Handler(Deps{Hosts: hosts}) // Roles nil => dev mode
	req := httptest.NewRequest(http.MethodPost, "/api/mission/intercept?agent=scout-dev&enable=true", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req) // no bearer at all — dev mode
	if w.Code != http.StatusOK {
		t.Fatalf("dev mode: status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
}

// TestLookbookWriteRequiresSuperuser proves lookbookUpload and lookbookDelete
// both refuse observers, non-superusers, and subagent-of-superuser tokens —
// mirroring memory/reference promotion's isHumanAdmin gate — and that delete
// is POST-only. Before the fix either endpoint let any authenticated member
// add/remove a fleet-shared design directive.
func TestLookbookWriteRequiresSuperuser(t *testing.T) {
	dir := t.TempDir()
	pstore, err := principals.Open(filepath.Join(dir, "p.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pstore.Close() })
	if err := pstore.CreateSuperuser("real-admin@example.com", "test"); err != nil {
		t.Fatal(err)
	}

	tstore, err := taskartifacts.Open(filepath.Join(dir, "t.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tstore.Close() })

	// Seed one lookbook item (a 1x1 PNG) to exercise delete.
	onePxPNG := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
		0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89, 0x00, 0x00, 0x00,
		0x0a, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
		0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00, 0x00, 0x00, 0x49,
		0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
	}
	seedID, err := tstore.SaveLookbookItem("seed", "seed item", "image/png", onePxPNG)
	if err != nil {
		t.Fatal(err)
	}

	srv := Handler(Deps{Roles: pstore, TaskArtifacts: tstore})

	uploadBody := func(dataB64 string) string {
		return `{"name":"n","description":"d","data":"` + dataB64 + `"}`
	}
	b64 := base64.StdEncoding.EncodeToString(onePxPNG)

	assertItemCount := func(t *testing.T, want int) {
		t.Helper()
		metas, err := tstore.GetLookbookItemsMeta()
		if err != nil {
			t.Fatal(err)
		}
		if len(metas) != want {
			t.Fatalf("lookbook items = %d, want %d", len(metas), want)
		}
	}

	deleteURL := "/api/lookbook/delete?id=" + strconv.FormatInt(seedID, 10)

	for _, tc := range []struct {
		name string
		wrap func(http.Handler) http.Handler
	}{
		{"observer", func(h http.Handler) http.Handler { return observerWrap(h) }},
		{"non-admin", func(h http.Handler) http.Handler { return bearerWrap(h, "not-an-admin@example.com") }},
		{"subagent-of-superuser", func(h http.Handler) http.Handler { return subagentWrap(h, "real-admin@example.com") }},
	} {
		// upload denied
		req := httptest.NewRequest(http.MethodPost, "/api/lookbook/upload", strings.NewReader(uploadBody(b64)))
		w := httptest.NewRecorder()
		tc.wrap(srv).ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("upload/%s: status = %d, want 403; body=%s", tc.name, w.Code, w.Body.String())
		}
		assertItemCount(t, 1)

		// delete denied
		req = httptest.NewRequest(http.MethodPost, deleteURL, nil)
		w = httptest.NewRecorder()
		tc.wrap(srv).ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("delete/%s: status = %d, want 403; body=%s", tc.name, w.Code, w.Body.String())
		}
		assertItemCount(t, 1)
	}

	// delete must be POST-only, even for a real superuser.
	req := httptest.NewRequest(http.MethodGet, deleteURL, nil)
	w := httptest.NewRecorder()
	bearerWrap(srv, "real-admin@example.com").ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("superuser GET delete: status = %d, want 405; body=%s", w.Code, w.Body.String())
	}
	assertItemCount(t, 1)

	// real superuser: upload allowed.
	req = httptest.NewRequest(http.MethodPost, "/api/lookbook/upload", strings.NewReader(uploadBody(b64)))
	w = httptest.NewRecorder()
	bearerWrap(srv, "real-admin@example.com").ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("superuser upload: status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	assertItemCount(t, 2)

	// real superuser: delete allowed.
	req = httptest.NewRequest(http.MethodPost, deleteURL, nil)
	w = httptest.NewRecorder()
	bearerWrap(srv, "real-admin@example.com").ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("superuser delete: status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	assertItemCount(t, 1)
}

// TestLookbookDevModeNilRolesAllowed proves dev mode (no Roles store) still
// allows lookbook writes.
func TestLookbookDevModeNilRolesAllowed(t *testing.T) {
	dir := t.TempDir()
	tstore, err := taskartifacts.Open(filepath.Join(dir, "t.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tstore.Close() })

	srv := Handler(Deps{TaskArtifacts: tstore}) // Roles nil => dev mode
	onePxPNG := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
		0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89, 0x00, 0x00, 0x00,
		0x0a, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
		0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00, 0x00, 0x00, 0x49,
		0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
	}
	body := `{"name":"n","description":"d","data":"` + base64.StdEncoding.EncodeToString(onePxPNG) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/lookbook/upload", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req) // no bearer at all — dev mode
	if w.Code != http.StatusOK {
		t.Fatalf("dev mode upload: status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
}

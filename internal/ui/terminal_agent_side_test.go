// SPDX-License-Identifier: Elastic-2.0

package ui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/brain"
	"github.com/pdbethke/corralai/internal/principals"
)

// TestTerminalAgentSideIsOwnedAndNeverReadOnly pins the sixth cold review's
// highest finding: guardTerminalWS gated role=operator only, and the
// intercept registry keys a session on the ?agent= query string alone. So a
// READ-ONLY observer token — the kind minted for a dashboard — could open
// role=agent&agent=<any name>, register as that agent's terminal, and then
// receive every keystroke the superuser typed once they opened role=operator.
// The agent side is now gated: never an observer, and only a bearer that OWNS
// the agent name (its own delegation name, or a name under its principal).
//
// The websocket upgrade itself is not exercised — the guard runs before the
// handler, and a refused request never reaches the registry.
func TestTerminalAgentSideIsOwnedAndNeverReadOnly(t *testing.T) {
	pstore, err := principals.Open(filepath.Join(t.TempDir(), "p.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pstore.Close() })
	if err := pstore.CreateSuperuser("real-admin@example.com", "test"); err != nil {
		t.Fatal(err)
	}
	srv := Handler(Deps{Roles: pstore, Hosts: brain.NewHostBook()})

	// A real server, because the legitimate cases reach the websocket
	// handler, which hijacks the connection — a ResponseRecorder cannot.
	get := func(wrap func(http.Handler) http.Handler, url string) (int, string) {
		ts := httptest.NewServer(wrap(srv))
		defer ts.Close()
		resp, err := http.Get(ts.URL + url)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	refused := func(code int, body string) bool {
		return code == http.StatusForbidden || strings.Contains(body, "role must be")
	}
	const victim = "/api/terminal/ws?role=agent&agent=real-admin@example.com/builder"

	// The hijack: an observer registering as someone else's agent.
	if code, body := get(observerWrap, victim); code != http.StatusForbidden {
		t.Errorf("observer as agent side: status = %d (%q), want 403 — an observer could read the operator's keystrokes", code, body)
	}
	// A different principal's human bearer registering as this agent.
	if code, body := get(func(h http.Handler) http.Handler { return bearerWrap(h, "mallory@example.com") }, victim); code != http.StatusForbidden {
		t.Errorf("another principal as agent side: status = %d (%q), want 403", code, body)
	}
	// A delegation token for a DIFFERENT subagent under the same principal.
	other := func(h http.Handler) http.Handler { return subagentWrap(h, "real-admin@example.com") } // names itself real-admin@example.com/child
	if code, body := get(other, victim); code != http.StatusForbidden {
		t.Errorf("a sibling subagent as agent side: status = %d (%q), want 403", code, body)
	}
	// An unknown role is refused before the registry can mint a session for it.
	if code, body := get(func(h http.Handler) http.Handler { return bearerWrap(h, "real-admin@example.com") }, "/api/terminal/ws?role=spy&agent=x"); code != http.StatusBadRequest || !strings.Contains(body, "role must be") {
		t.Errorf("unknown role: status = %d (%q), want 400 from the guard", code, body)
	}

	// The legitimate cases pass the guard (and then fail the websocket
	// upgrade on a plain GET, which is the handler's own 400, not ours).
	own := "/api/terminal/ws?role=agent&agent=real-admin@example.com/child"
	if code, body := get(other, own); refused(code, body) {
		t.Errorf("a delegation token streaming ITS OWN terminal was refused: %d %q", code, body)
	}
	if code, body := get(func(h http.Handler) http.Handler { return bearerWrap(h, "real-admin@example.com") }, victim); refused(code, body) {
		t.Errorf("a principal streaming an agent under its own namespace was refused: %d %q", code, body)
	}
}

// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/coord"
)

type cohort struct {
	t        *testing.T
	store    *coord.Store
	ts       *httptest.Server
	sessions []*mcp.ClientSession
}

// newCohort boots the real brain on an httptest server and connects n independent
// MCP clients to it over real streamable-HTTP (auth off). Each session behaves like
// a separate machine.
func newCohort(t *testing.T, n int) *cohort {
	t.Helper()
	ctx := context.Background()
	cs, err := coord.Open(filepath.Join(t.TempDir(), "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(cs, nil, Options{})
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{DisableLocalhostProtection: true},
	)
	ts := httptest.NewServer(handler)
	c := &cohort{t: t, store: cs, ts: ts}
	for i := 0; i < n; i++ {
		cl := mcp.NewClient(&mcp.Implementation{Name: "harness-client", Version: "0"}, nil)
		sess, err := cl.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL}, nil)
		if err != nil {
			t.Fatalf("client %d connect: %v", i, err)
		}
		c.sessions = append(c.sessions, sess)
	}
	t.Cleanup(func() {
		for _, s := range c.sessions {
			s.Close()
		}
		ts.Close()
		cs.Close()
	})
	return c
}

// call invokes a tool on session i and unmarshals the structured result into out
// (pass nil to ignore the result).
func (c *cohort) call(i int, tool string, args map[string]any, out any) {
	c.t.Helper()
	res, err := c.sessions[i].CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		c.t.Fatalf("call %s: %v", tool, err)
	}
	if res.IsError {
		c.t.Fatalf("tool %s errored: %+v", tool, res.Content)
	}
	if out != nil {
		b, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(b, out); err != nil {
			c.t.Fatalf("decode %s result: %v", tool, err)
		}
	}
}

func TestHarnessSmoke(t *testing.T) {
	c := newCohort(t, 2)
	c.call(0, "bootstrap", map[string]any{"name": "alice"}, nil)
	c.call(1, "bootstrap", map[string]any{"name": "bob"}, nil)
	var out struct {
		Agents []coord.Agent `json:"agents"`
	}
	c.call(0, "list_active", map[string]any{}, &out)
	if len(out.Agents) != 2 {
		t.Fatalf("want 2 active agents over the wire, got %d", len(out.Agents))
	}
}

// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/coord"
	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/taskartifacts"
)

func TestBrowserToolsRegistration(t *testing.T) {
	dir := t.TempDir()

	cstore, err := coord.Open(filepath.Join(dir, "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer cstore.Close()

	qstore, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer qstore.Close()

	artstore, err := taskartifacts.Open(filepath.Join(dir, "art.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer artstore.Close()

	bm := NewBrowserManager()
	defer bm.Close()

	ws := NewWorkerSessions()
	srv := NewServer(cstore, nil, Options{
		Queue:          qstore,
		TaskArtifacts:  artstore,
		Browser:        bm,
		WorkerSessions: ws,
	})

	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{DisableLocalhostProtection: true},
	)
	ts := httptest.NewServer(handler)
	defer ts.Close()

	ctx := context.Background()
	cl := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	sess, err := cl.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sess.Close()

	res, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	foundNavigate := false
	foundScreenshot := false
	foundClick := false
	foundInput := false
	foundGetHTML := false
	for _, tool := range res.Tools {
		switch tool.Name {
		case "browser_navigate":
			foundNavigate = true
		case "browser_screenshot":
			foundScreenshot = true
		case "browser_click":
			foundClick = true
		case "browser_input":
			foundInput = true
		case "browser_get_html":
			foundGetHTML = true
		}
	}

	if !foundNavigate {
		t.Error("expected browser_navigate tool to be registered")
	}
	if !foundScreenshot {
		t.Error("expected browser_screenshot tool to be registered")
	}
	if !foundClick {
		t.Error("expected browser_click tool to be registered")
	}
	if !foundInput {
		t.Error("expected browser_input tool to be registered")
	}
	if !foundGetHTML {
		t.Error("expected browser_get_html tool to be registered")
	}
}

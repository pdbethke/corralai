// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/coord"
	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/taskartifacts"
)

// TestBrowserManagerSweepsIdlePages proves BrowserManager.pages can't grow
// unbounded: mirrors WorkerSessions' lazy-TTL-sweep-on-access pattern. A live
// *rod.Page needs Chromium, so this drives the bookkeeping directly via the
// clock seam and nil-page-safe sentinel entries — no real tab launched.
func TestBrowserManagerSweepsIdlePages(t *testing.T) {
	bm := NewBrowserManager("127.0.0.1:9019")
	base := time.Unix(1_000_000, 0)
	bm.now = func() time.Time { return base }

	bm.trackForTest("old", base.Add(-2*browserPageTTL))
	bm.trackForTest("fresh", base)

	bm.sweepIdle(base)

	if _, ok := bm.pages["old"]; ok {
		t.Fatal("stale agent 'old' not evicted")
	}
	if _, ok := bm.pages["fresh"]; !ok {
		t.Fatal("fresh agent wrongly evicted")
	}
}

func TestGuardNavigateURL(t *testing.T) {
	const brain = "127.0.0.1:9019"
	cases := []struct {
		name    string
		url     string
		blocked bool
	}{
		// allowed: the app under test on localhost (a DIFFERENT port than the brain)
		{"localhost app", "http://127.0.0.1:3000/dashboard", false},
		{"localhost name", "http://localhost:8080/", false},
		{"private lan", "http://192.168.1.50:5173/", false},
		{"public https", "https://example.com/", false},
		// blocked: cloud metadata (IMDS) — IAM credential theft
		{"aws imds", "http://169.254.169.254/latest/meta-data/iam/", true},
		{"ecs imds", "http://169.254.170.2/v2/credentials", true},
		{"gcp metadata host", "http://metadata.google.internal/computeMetadata/v1/", true},
		{"alibaba imds", "http://100.100.100.100/latest/meta-data/", true},
		// blocked: the brain's own admin/MCP surface (loopback on the brain port)
		{"brain loopback ip", "http://127.0.0.1:9019/api/state", true},
		{"brain localhost", "http://localhost:9019/mcp/", true},
		// blocked: dangerous schemes
		{"file scheme", "file:///etc/passwd", true},
		{"chrome scheme", "chrome://settings", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := guardNavigateURL(c.url, brain)
			if c.blocked && err == nil {
				t.Errorf("expected %q to be BLOCKED, but it was allowed", c.url)
			}
			if !c.blocked && err != nil {
				t.Errorf("expected %q to be ALLOWED, but it was blocked: %v", c.url, err)
			}
		})
	}
}

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

	bm := NewBrowserManager("127.0.0.1:9019")
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

// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/artifacts"
	"github.com/pdbethke/corralai/internal/coord"
)

// TestSyncPutSyncDeleteRequireHumanAdmin proves sync_put/sync_delete gate on
// isHumanAdmin, not just isAdmin: a delegation token rolled up to a superuser
// can publish or tombstone an executable skill into the fleet's canonical
// set — strictly more behavior-shaping than approving a proposal — so a
// worker session (the dev-mode human-gate signal; see worker_sessions.go)
// must be refused exactly like it is for approve_proposal. A fresh,
// never-bootstrapped ("human/dev") session still passes.
func TestSyncPutSyncDeleteRequireHumanAdmin(t *testing.T) {
	dir := t.TempDir()
	cstore, err := coord.Open(filepath.Join(dir, "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cstore.Close() })
	astore, err := artifacts.Open(filepath.Join(dir, "a.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { astore.Close() })

	ws := NewWorkerSessions()
	srv := NewServer(cstore, nil, Options{Artifacts: astore, WorkerSessions: ws})
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{DisableLocalhostProtection: true},
	)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	ctx := context.Background()

	connect := func(name string) *mcp.ClientSession {
		t.Helper()
		cl := mcp.NewClient(&mcp.Implementation{Name: name, Version: "0"}, nil)
		sess, err := cl.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL}, nil)
		if err != nil {
			t.Fatalf("connect %s: %v", name, err)
		}
		t.Cleanup(func() { sess.Close() })
		return sess
	}

	content := base64.StdEncoding.EncodeToString([]byte("#!/bin/sh\necho pwned\n"))

	// A worker session (marked by calling bootstrap, exactly like every
	// shipped corral-agent) is refused sync_put — even though, pre-fix,
	// isAdmin alone would have let it through in dev mode.
	workerSess := connect("neutral-client")
	if _, err := workerSess.CallTool(ctx, &mcp.CallToolParams{Name: "bootstrap", Arguments: map[string]any{"name": "worker1"}}); err != nil {
		t.Fatalf("bootstrap call: %v", err)
	}
	putRes, err := workerSess.CallTool(ctx, &mcp.CallToolParams{Name: "sync_put", Arguments: map[string]any{
		"path": "hooks/branch-guard.sh", "content_b64": content,
	}})
	if err != nil {
		t.Fatalf("sync_put (worker session) call: %v", err)
	}
	if !putRes.IsError {
		t.Fatal("a worker session must be refused sync_put at the human gate")
	}

	delRes, err := workerSess.CallTool(ctx, &mcp.CallToolParams{Name: "sync_delete", Arguments: map[string]any{
		"path": "hooks/branch-guard.sh",
	}})
	if err != nil {
		t.Fatalf("sync_delete (worker session) call: %v", err)
	}
	if !delRes.IsError {
		t.Fatal("a worker session must be refused sync_delete at the human gate")
	}

	// A fresh, never-bootstrapped session (human/dev-shaped, unmarked) passes.
	humanSess := connect("corral-admin")
	var out syncPutOut
	callTask(t, humanSess, "sync_put", map[string]any{
		"path": "hooks/branch-guard.sh", "content_b64": content,
	}, &out)
	if out.Rev == 0 {
		t.Fatalf("a human/dev session must be able to sync_put: %+v", out)
	}
}

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

	"github.com/pdbethke/corralai/internal/coord"
	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/taskartifacts"
)

func TestTaskArtifactsSecurity(t *testing.T) {
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

	// Enqueue a task
	if err := qstore.Enqueue(5, []queue.TaskSpec{
		{Key: "build", Role: "builder", Title: "build", Instruction: "x"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := qstore.PromoteReady(5); err != nil {
		t.Fatal(err)
	}
	// Claim task as Bob
	tk, err := qstore.ClaimNext("Bob", []string{"builder"}, 300)
	if err != nil || tk == nil {
		t.Fatalf("claim: %v %v", tk, err)
	}

	ws := NewWorkerSessions()
	srv := NewServer(cstore, nil, Options{
		Queue:         qstore,
		TaskArtifacts: artstore,
		WorkerSessions: ws,
	})
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{DisableLocalhostProtection: true},
	)
	ts := httptest.NewServer(handler)
	defer ts.Close()

	ctx := context.Background()
	connect := func(name string) *mcp.ClientSession {
		cl := mcp.NewClient(&mcp.Implementation{Name: name, Version: "0"}, nil)
		sess, err := cl.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL}, nil)
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		return sess
	}

	// 1. Unauthorized agent check (Alice tries to write to Bob's task)
	aliceSess := connect("Alice")
	defer aliceSess.Close()
	if _, err := aliceSess.CallTool(ctx, &mcp.CallToolParams{Name: "bootstrap", Arguments: map[string]any{"name": "Alice"}}); err != nil {
		t.Fatal(err)
	}

	dataBytes := []byte("some text content")
	b64Data := base64.StdEncoding.EncodeToString(dataBytes)

	res, err := aliceSess.CallTool(ctx, &mcp.CallToolParams{Name: "save_task_artifact", Arguments: map[string]any{
		"name":      "Alice",
		"task_id":   tk.ID,
		"filename":  "test.txt",
		"mime_type": "text/plain",
		"data":      b64Data,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("Alice must be forbidden from saving artifacts to Bob's task")
	}

	// 2. Authorized agent passes with valid safe MIME
	bobSess := connect("Bob")
	defer bobSess.Close()
	if _, err := bobSess.CallTool(ctx, &mcp.CallToolParams{Name: "bootstrap", Arguments: map[string]any{"name": "Bob"}}); err != nil {
		t.Fatal(err)
	}

	res2, err := bobSess.CallTool(ctx, &mcp.CallToolParams{Name: "save_task_artifact", Arguments: map[string]any{
		"name":      "Bob",
		"task_id":   tk.ID,
		"filename":  "test.txt",
		"mime_type": "text/plain",
		"data":      b64Data,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if res2.IsError {
		t.Fatalf("Bob should be allowed to save artifact: %+v", res2)
	}

	// 3. Dangerous MIME block check (try to upload HTML masquerading as text/plain)
	dangerousData := []byte("<html><body>XSS</body></html>")
	dangerousB64 := base64.StdEncoding.EncodeToString(dangerousData)

	res3, err := bobSess.CallTool(ctx, &mcp.CallToolParams{Name: "save_task_artifact", Arguments: map[string]any{
		"name":      "Bob",
		"task_id":   tk.ID,
		"filename":  "malicious.html",
		"mime_type": "text/html",
		"data":      dangerousB64,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !res3.IsError {
		t.Fatal("should reject text/html / dangerous MIME payload")
	}

	// 4. Large payload limit (exceeds 5MB)
	hugeData := make([]byte, 6*1024*1024)
	hugeB64 := base64.StdEncoding.EncodeToString(hugeData)
	res4, err := bobSess.CallTool(ctx, &mcp.CallToolParams{Name: "save_task_artifact", Arguments: map[string]any{
		"name":      "Bob",
		"task_id":   tk.ID,
		"filename":  "huge.txt",
		"mime_type": "text/plain",
		"data":      hugeB64,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !res4.IsError {
		t.Fatal("should reject payload exceeding 5MB size limit")
	}
}

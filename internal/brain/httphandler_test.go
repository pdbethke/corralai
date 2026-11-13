// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"encoding/base64"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The brain's own caps are the limits a caller should meet: 64 MiB for a
// repo_push, 5 MiB for a task artifact. go-sdk v1.8.0 put a 4 MiB default
// limit on every Streamable HTTP request body, which would refuse a 10 MiB
// push with a bare 413 before repo_push ever saw it. This test sends a call
// between the SDK default and the brain's cap through the handler the daemon
// mounts, and requires that the TOOL answers it. A too-small transport limit
// fails here as a transport error.
func TestHTTPHandlerLetsEveryToolCapReachItsTool(t *testing.T) {
	if MaxRequestBodyBytes <= mcp.DefaultMaxRequestBodyBytes {
		t.Fatalf("MaxRequestBodyBytes %d is not above the SDK default %d", MaxRequestBodyBytes, mcp.DefaultMaxRequestBodyBytes)
	}
	// The largest artifact the tool accepts, base64-encoded, must fit.
	if enc := int64(base64.StdEncoding.EncodedLen(maxArtifactBytes)); enc >= MaxRequestBodyBytes {
		t.Fatalf("a %d-byte artifact encodes to %d bytes, not under the body limit %d", maxArtifactBytes, enc, MaxRequestBodyBytes)
	}

	srv := mcp.NewServer(&mcp.Implementation{Name: "brain-under-test", Version: "0"}, nil)
	var got int
	mcp.AddTool(srv, &mcp.Tool{Name: "echo_len"}, func(_ context.Context, _ *mcp.CallToolRequest, in struct {
		Data string `json:"data"`
	}) (*mcp.CallToolResult, struct {
		N int `json:"n"`
	}, error) {
		got = len(in.Data)
		return nil, struct {
			N int `json:"n"`
		}{len(in.Data)}, nil
	})
	ts := httptest.NewServer(NewHTTPHandler(srv))
	defer ts.Close()

	ctx := context.Background()
	sess, err := mcp.NewClient(&mcp.Implementation{Name: "bee", Version: "0"}, nil).
		Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	payload := strings.Repeat("a", 10<<20) // 10 MiB: over the SDK default, under maxPushBytes
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "echo_len", Arguments: map[string]any{"data": payload}})
	if err != nil {
		t.Fatalf("a 10 MiB call never reached the tool: %v", err)
	}
	if res.IsError || got != len(payload) {
		t.Fatalf("tool saw %d bytes (IsError=%v), want %d", got, res.IsError, len(payload))
	}
}

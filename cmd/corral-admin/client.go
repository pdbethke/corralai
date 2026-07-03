// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// bearer injects the operator's token on every request to the brain.
type bearer struct {
	token string
	next  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r2 := r.Clone(r.Context())
	r2.Header.Set("Authorization", "Bearer "+b.token)
	return b.next.RoundTrip(r2)
}

// brainClient is a connected MCP session to a brain's /mcp endpoint.
type brainClient struct {
	sess *mcp.ClientSession
}

// dial opens an authenticated MCP session to the brain. The command verbs are
// pure request/response, so the standalone server->client SSE stream is disabled.
func dial(ctx context.Context, brainURL, token string) (*brainClient, error) {
	endpoint := strings.TrimRight(brainURL, "/") + "/mcp"
	hc := &http.Client{Timeout: 30 * time.Second, Transport: bearer{token, http.DefaultTransport}}
	tr := &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: hc, DisableStandaloneSSE: true}
	cl := mcp.NewClient(&mcp.Implementation{Name: "corral-admin", Version: version}, nil)
	sess, err := cl.Connect(ctx, tr, nil)
	if err != nil {
		return nil, fmt.Errorf("connect to brain %s: %w", brainURL, err)
	}
	return &brainClient{sess: sess}, nil
}

func (c *brainClient) close() { _ = c.sess.Close() }

// call invokes a tool and returns its structured result as raw JSON. A tool that
// reports an error (e.g. "forbidden: superuser only") surfaces as a Go error.
func (c *brainClient) call(ctx context.Context, name string, args map[string]any) (json.RawMessage, error) {
	res, err := c.sess.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return nil, err
	}
	text := firstText(res)
	if res.IsError {
		msg := strings.TrimSpace(text)
		if msg == "" {
			msg = "tool reported an error"
		}
		return nil, fmt.Errorf("%s", msg)
	}
	return json.RawMessage(text), nil
}

func firstText(res *mcp.CallToolResult) string {
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			return tc.Text
		}
	}
	return ""
}

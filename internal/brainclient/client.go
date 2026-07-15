// SPDX-License-Identifier: Elastic-2.0

// Package brainclient is the one shared implementation of "dial a brain's
// MCP endpoint with a bearer token and call a tool" — corral's operator CLI
// (corral-admin) and corral certify both need exactly this, and previously
// each carried its own byte-identical copy of bearer/dial/firstText. One
// implementation now, two callers.
package brainclient

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// bearer injects the operator's/certify's brain token on every request.
type bearer struct {
	token string
	next  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r2 := r.Clone(r.Context())
	if b.token != "" {
		r2.Header.Set("Authorization", "Bearer "+b.token)
	}
	return b.next.RoundTrip(r2)
}

// AuthedHTTPClient returns an http.Client that attaches the bearer token to
// every request. It sets NO client timeout — safe for a long-lived streaming
// MCP session (e.g. a worker agent's connection); callers that make only short
// request/response calls should set a Timeout on the returned client (Dial does).
func AuthedHTTPClient(token string) *http.Client {
	return &http.Client{Transport: bearer{token, http.DefaultTransport}}
}

// Client is a connected MCP session to a brain's /mcp endpoint.
type Client struct {
	sess *mcp.ClientSession
}

// Dial opens an authenticated MCP session to the brain at brainURL. The
// command verbs corral-admin/certify use are pure request/response, so the
// standalone server->client SSE stream is disabled.
func Dial(ctx context.Context, brainURL, token string) (*Client, error) {
	endpoint := strings.TrimRight(brainURL, "/") + "/mcp"
	hc := AuthedHTTPClient(token)
	hc.Timeout = 30 * time.Second // Dial's verbs are short request/response
	tr := &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: hc, DisableStandaloneSSE: true}
	cl := mcp.NewClient(&mcp.Implementation{Name: "corral-client", Version: "0"}, nil)
	sess, err := cl.Connect(ctx, tr, nil)
	if err != nil {
		return nil, fmt.Errorf("connect to brain %s: %w", brainURL, err)
	}
	return &Client{sess: sess}, nil
}

// Close ends the MCP session.
func (c *Client) Close() error { return c.sess.Close() }

// CallTool invokes a tool by name and returns its raw *mcp.CallToolResult.
// A tool that reports an error (e.g. "forbidden: superuser only") comes
// back as a normal result with IsError set — callers use FirstText to read
// the message — err is reserved for transport-level failures.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (*mcp.CallToolResult, error) {
	return c.sess.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
}

// FirstText returns the text of the first TextContent block in res, or ""
// if there is none.
func FirstText(res *mcp.CallToolResult) string {
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			return tc.Text
		}
	}
	return ""
}

// SPDX-License-Identifier: Elastic-2.0

package brainclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// echoServer stands up a real MCP-over-HTTP brain exposing "whoami" (echoes
// success) and "boom" (always a tool-level error), so Dial/CallTool/
// FirstText can be exercised end to end.
func echoServer(t *testing.T) *httptest.Server {
	t.Helper()
	s := mcp.NewServer(&mcp.Implementation{Name: "echo-brain", Version: "0"}, nil)
	mcp.AddTool(s, &mcp.Tool{Name: "whoami", Description: "says hi"},
		func(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "hi"}},
			}, nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "boom", Description: "always errors"},
		func(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: "boom: deliberate failure"}},
			}, nil, nil
		})
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return s },
		&mcp.StreamableHTTPOptions{DisableLocalhostProtection: true},
	)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts
}

// roundTripperFunc adapts a function to http.RoundTripper.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestBearerRoundTripperSetsAuthorizationHeader is a direct unit test of
// the hoisted bearer type (the piece Dial builds its http.Client on): it
// must attach "Bearer <token>" to every outgoing request without mutating
// the caller's original *http.Request.
func TestBearerRoundTripperSetsAuthorizationHeader(t *testing.T) {
	var gotAuth string
	rt := bearer{token: "sekrit-token", next: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		gotAuth = r.Header.Get("Authorization")
		return &http.Response{StatusCode: 200, Body: http.NoBody, Header: http.Header{}}, nil
	})}

	orig := httptest.NewRequest(http.MethodGet, "http://brain.example/mcp", nil)
	if _, err := rt.RoundTrip(orig); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if gotAuth != "Bearer sekrit-token" {
		t.Fatalf("Authorization header = %q, want %q", gotAuth, "Bearer sekrit-token")
	}
	if orig.Header.Get("Authorization") != "" {
		t.Fatal("bearer must not mutate the caller's original request")
	}
}

func TestDialAndCallTool(t *testing.T) {
	ts := echoServer(t)
	ctx := context.Background()

	c, err := Dial(ctx, ts.URL, "sekrit-token")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()

	res, err := c.CallTool(ctx, "whoami", nil)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", FirstText(res))
	}
	if got := FirstText(res); got != "hi" {
		t.Fatalf("FirstText = %q, want %q", got, "hi")
	}
}

func TestCallToolSurfacesToolError(t *testing.T) {
	ts := echoServer(t)
	ctx := context.Background()

	c, err := Dial(ctx, ts.URL, "tok")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()

	res, err := c.CallTool(ctx, "boom", nil)
	if err != nil {
		t.Fatalf("CallTool transport error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected the tool result to report IsError")
	}
	if FirstText(res) == "" {
		t.Fatal("expected error text via FirstText")
	}
}

func TestDialTrimsTrailingSlashAndAppendsMcp(t *testing.T) {
	ts := echoServer(t)
	ctx := context.Background()

	// Dial must work whether or not the caller's brainURL has a trailing
	// slash, and must reach the same /mcp endpoint either way.
	c, err := Dial(ctx, ts.URL+"/", "t")
	if err != nil {
		t.Fatalf("Dial with trailing slash: %v", err)
	}
	defer c.Close()

	if _, err := c.CallTool(ctx, "whoami", nil); err != nil {
		t.Fatalf("CallTool: %v", err)
	}
}

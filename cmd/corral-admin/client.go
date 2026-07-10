// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pdbethke/corralai/internal/brainclient"
)

// brainClient is a connected MCP session to a brain's /mcp endpoint. It
// wraps internal/brainclient's shared dial/call plumbing with corral-admin's
// own call() error-surfacing convention.
type brainClient struct {
	c *brainclient.Client
}

// dial opens an authenticated MCP session to the brain. The command verbs are
// pure request/response, so the standalone server->client SSE stream is disabled.
func dial(ctx context.Context, brainURL, token string) (*brainClient, error) {
	c, err := brainclient.Dial(ctx, brainURL, token)
	if err != nil {
		return nil, err
	}
	return &brainClient{c: c}, nil
}

func (b *brainClient) close() { _ = b.c.Close() }

// call invokes a tool and returns its structured result as raw JSON. A tool that
// reports an error (e.g. "forbidden: superuser only") surfaces as a Go error.
func (b *brainClient) call(ctx context.Context, name string, args map[string]any) (json.RawMessage, error) {
	res, err := b.c.CallTool(ctx, name, args)
	if err != nil {
		return nil, err
	}
	text := brainclient.FirstText(res)
	if res.IsError {
		msg := strings.TrimSpace(text)
		if msg == "" {
			msg = "tool reported an error"
		}
		return nil, fmt.Errorf("%s", msg)
	}
	return json.RawMessage(text), nil
}

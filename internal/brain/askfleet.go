// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/oracle"
)

type askFleetIn struct {
	Name     string `json:"name"`
	Question string `json:"question"`
}

// rateLimiter is a per-principal fixed-window token bucket. Window slides on
// each allow() call; calls that fall outside the window are pruned. Thread-safe.
type rateLimiter struct {
	mu     sync.Mutex
	window time.Duration
	limit  int
	hits   map[string][]time.Time
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{limit: limit, window: window, hits: make(map[string][]time.Time)}
}

// allow returns true if the key is within the rate limit for the current window,
// recording the call. Returns false (without recording) when the limit is exceeded.
func (r *rateLimiter) allow(key string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	cutoff := now.Add(-r.window)
	// filter in-place: reuse the slice backing array to avoid allocations on hot path
	existing := r.hits[key]
	kept := existing[:0]
	for _, t := range existing {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	// Map-leak guard: a fully-expired window leaves the key with no live hits;
	// drop it rather than retaining an empty entry — under rotating identities this
	// bounds the map to principals with hits in the last window. The allowed path
	// below re-creates the key with the current hit; the denied path never reaches
	// here with an empty window (len(kept) >= limit > 0).
	if len(kept) == 0 {
		delete(r.hits, key)
	}
	if len(kept) >= r.limit {
		r.hits[key] = kept
		return false
	}
	r.hits[key] = append(kept, now)
	return true
}

// registerAskFleet adds the ask_fleet MCP tool to s. The rate limiter is keyed
// on the verified principal (identity) so each principal gets an independent
// quota. Rate checking happens BEFORE the oracle runs — over-limit calls return
// immediately without touching the LLM or DuckDB.
func registerAskFleet(s *mcp.Server, opts Options) {
	limit := opts.AskFleetRateLimit
	if limit <= 0 {
		limit = 10 // default: 10 asks/min/principal
	}
	rl := newRateLimiter(limit, time.Minute)

	mcp.AddTool(s,
		&mcp.Tool{
			Name:        "ask_fleet",
			Description: "Ask a natural-language question about the whole fleet's state (missions, tasks, telemetry across all swarms). Read-only; returns a narrated answer and the raw result rows.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, in askFleetIn) (*mcp.CallToolResult, oracle.Answer, error) {
			who := identity(req, in.Name)
			if !rl.allow(who, time.Now()) {
				return nil, oracle.Answer{}, fmt.Errorf("rate limit: max %d fleet questions/minute per principal — try again shortly", limit)
			}
			ans, err := opts.Oracle.Ask(ctx, in.Question)
			if err != nil {
				return nil, oracle.Answer{}, err
			}
			return nil, ans, nil
		},
	)
}

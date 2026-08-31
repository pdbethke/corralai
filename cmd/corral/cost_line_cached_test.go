// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/advpool"
)

func i64p(v int64) *int64 { return &v }

// TestCostLineShowsTheCachedShare. The per-survivor writer's whole cost claim
// is that a file's N calls share one cached prefix, and the `cost:` line is
// where an operator reads what a scan spent. Without the cached share, the
// line reports a big input total and no way to tell a cheap fan-out from an
// expensive one.
func TestCostLineShowsTheCachedShare(t *testing.T) {
	got := costLine([]advpool.ModelCall{
		{Role: "mutant-generator", Model: "m", Calls: 8, InputTokens: 90_000, OutputTokens: 9_000},
		{Role: "test-writer", Model: "w", Calls: 24, InputTokens: 900_000, OutputTokens: 31_000,
			CachedInputTokens: i64p(760_000)},
	})
	if !strings.Contains(got, "across 32 calls (0.8M cached)") {
		t.Errorf("the cached share is missing from the cost line: %q", got)
	}

	// Nothing reported a cached count: the line must say NOTHING about
	// caching rather than "(0 cached)", which would claim a measured miss on
	// a provider that never spoke about caching at all.
	quiet := costLine([]advpool.ModelCall{
		{Role: "test-writer", Model: "w", Calls: 2, InputTokens: 100, OutputTokens: 10},
	})
	if strings.Contains(quiet, "cached") {
		t.Errorf("a run nothing measured caching for advertised a cached share: %q", quiet)
	}
}

// TestScanTotalsSumTheNullableCacheCounts: the totals feed the cost line, so
// the nullable columns must sum the same NULL-not-zero way Retries does — a
// role no call reported caching for contributes nothing and leaves the total
// nil, not 0.
func TestScanTotalsSumTheNullableCacheCounts(t *testing.T) {
	sum := sumCacheCounts([]*int64{nil, i64p(10), i64p(5)})
	if sum == nil || *sum != 15 {
		t.Errorf("sum = %v, want 15", sum)
	}
	if got := sumCacheCounts([]*int64{nil, nil}); got != nil {
		t.Errorf("sum = %d, want nil — nothing measured one", *got)
	}
}

// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/reposcan"
)

func TestOldestReuseIgnoresFreshResults(t *testing.T) {
	old := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	got, ok := oldestReuse([]reposcan.FileResult{
		{CacheHit: false, ComputedAt: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)},
		{CacheHit: true, ComputedAt: newer},
		{CacheHit: true, ComputedAt: old},
	})
	if !ok {
		t.Fatal("expected a reused result")
	}
	if !got.Equal(old) {
		t.Fatalf("oldestReuse = %v, want %v — the OLDEST contributing verdict is what a reader needs", got, old)
	}
}

func TestOldestReuseReportsNothingWhenNothingWasReused(t *testing.T) {
	if _, ok := oldestReuse([]reposcan.FileResult{{CacheHit: false, ComputedAt: time.Now()}}); ok {
		t.Fatal("reported reuse age for a scan that reused nothing")
	}
}

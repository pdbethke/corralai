// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/pricing"
	"github.com/pdbethke/corralai/internal/scanstore"
)

func costRow(t *testing.T, c scanstore.ModelCall, seatTools string) (micro *int64, basis string) {
	t.Helper()
	rows := buildModelCallRows([]scanstore.ModelCall{c}, 1, bundleMeta{Repo: "o/r", SeatTools: seatTools})
	if len(rows) != 1 {
		t.Fatalf("got %d rows", len(rows))
	}
	return rows[0].CostMicroUSD, rows[0].CostBasis
}

// An API seat on a model the table prices gets a figure and the table as its
// basis. 1,000,000 prompt tokens, 200,000 of them cached, 100,000 written,
// 10,000 out on claude-sonnet-5-5: 700k*2 + 200k*0.10 + 100k*2.50 + 10k*10
// per million = 1.4 + 0.02 + 0.25 + 0.1 = $1.77.
func TestModelCallRowCostFromTheTable(t *testing.T) {
	c := scanstore.ModelCall{Role: "test-writer", Model: "claude-sonnet-5-5", Calls: 4,
		InputTokens: 1_000_000, OutputTokens: 10_000,
		CachedInputTokens: i64ptr(200_000), CacheWriteInputTokens: i64ptr(100_000)}
	m, b := costRow(t, c, "")
	if m == nil || *m != 1_770_000 {
		t.Fatalf("cost = %v, want 1770000", m)
	}
	if b != pricing.Current.BasisPriced() {
		t.Fatalf("basis = %q", b)
	}
}

func TestModelCallRowCostUnpricedModel(t *testing.T) {
	m, b := costRow(t, scanstore.ModelCall{Role: "test-writer", Model: "no-such-model", Calls: 1, InputTokens: 5, OutputTokens: 5}, "")
	if m != nil || b != pricing.Current.BasisNoPrice("no-such-model") {
		t.Fatalf("got %v, %q", m, b)
	}
}

// An agent seat is recognised by what ForSeat resolved (SeatTools), and is
// never priced even when its model is in the table and it reported tokens.
func TestModelCallRowCostAgentSeat(t *testing.T) {
	c := scanstore.ModelCall{Role: "test-writer", Model: "claude-code:claude-sonnet-5-5", Calls: 1, InputTokens: 100, OutputTokens: 10}
	m, b := costRow(t, c, `{"test-writer":"claude-code 2.1"}`)
	if m != nil || b != pricing.BasisAgentSeat {
		t.Fatalf("got %v, %q", m, b)
	}
	// A different role's tool does not make this role an agent.
	c.Model = "claude-sonnet-5-5"
	m, _ = costRow(t, c, `{"critic":"codex 1"}`)
	if m == nil {
		t.Fatal("an API seat beside an agent seat must still be priced")
	}
}

func TestModelCallRowCostUnmetered(t *testing.T) {
	all := scanstore.ModelCall{Role: "critic", Model: "claude-sonnet-5-5", Calls: 3, UnmeteredCalls: 3}
	m, b := costRow(t, all, "")
	if m != nil || b != pricing.BasisNotMetered {
		t.Fatalf("all unmetered: %v, %q", m, b)
	}
	// Metered and unmetered in one row: the metered part alone would read as
	// the whole row's cost, so no figure, and the basis names the count.
	mixed := scanstore.ModelCall{Role: "critic", Model: "claude-sonnet-5-5", Calls: 5, UnmeteredCalls: 2, InputTokens: 1000, OutputTokens: 100}
	m, b = costRow(t, mixed, "")
	if m != nil || !strings.Contains(b, "2 of 5 calls") {
		t.Fatalf("mixed: %v, %q", m, b)
	}
}

func TestModelCallRowCostUnreadableSeatTools(t *testing.T) {
	c := scanstore.ModelCall{Role: "critic", Model: "claude-sonnet-5-5", Calls: 1, InputTokens: 10, OutputTokens: 1}
	m, b := costRow(t, c, `{not json`)
	if m != nil || !strings.Contains(b, "seat tools unreadable") {
		t.Fatalf("got %v, %q", m, b)
	}
}

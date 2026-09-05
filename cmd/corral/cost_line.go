// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/agentbackend"
)

// rosterRoleOrder is the order a roster is presented in everywhere this
// package prints or sums it: the role that plants the fault first, then the
// one that has to catch it, then the one that grades the catch, then the two
// measurement-only seats that never gate a verdict. costLine's callers build
// their []advpool.ModelCall slices in this order so the printed breakdown is
// stable run to run rather than following Go's randomized map iteration.
var rosterRoleOrder = []string{
	advpool.RoleMutantGenerator,
	advpool.RoleTestWriter,
	advpool.RoleTestCritic,
	advpool.RoleMutantGeneratorShadow,
	advpool.RoleTestWriterShadow,
}

// auditRoleMeters builds one agentbackend.UsageMeter PER ROLE that assign
// actually names — never one for a role left empty or resolved to "off",
// because a role that never runs must never have a meter that could later be
// read as "a call was made". The meter's Model is filled from assign so a
// ledger row built from its Snapshot never has to look the model up a second
// way.
func auditRoleMeters(assign advpool.RoleAssignment) map[string]*agentbackend.UsageMeter {
	meters := make(map[string]*agentbackend.UsageMeter, len(assign))
	for role, model := range assign {
		model = strings.TrimSpace(model)
		if model == "" || model == "off" {
			continue
		}
		meters[role] = &agentbackend.UsageMeter{Model: model}
	}
	return meters
}

// modelCallsFromMeters reads every role's meter and returns the roles that
// actually made at least one call, in roster order. A role with a meter that
// never recorded a call (never dispatched, or resolved but unused) is
// omitted — the ZERO-CALLS-MEANS-NO-ROW rule that keeps a warehouse query
// from reading an absent seat as "ran and cost nothing".
func modelCallsFromMeters(meters map[string]*agentbackend.UsageMeter) []advpool.ModelCall {
	var out []advpool.ModelCall
	for _, role := range rosterRoleOrder {
		m, ok := meters[role]
		if !ok {
			continue
		}
		snap := m.Snapshot()
		if snap.Calls == 0 {
			continue
		}
		out = append(out, advpool.ModelCall{
			Role: role, Model: snap.Model,
			Calls: int(snap.Calls),
			// Retries stays nil — see advpool.ModelCall's doc and
			// agentbackend.UsageMeter's: nothing in this codebase has a
			// retry loop to observe yet, so there is no measured count to
			// put here. nil, never a stored 0, which would read as
			// "measured: zero retries".
			InputTokens: snap.InputTokens, OutputTokens: snap.OutputTokens,
			// nil unless at least one of this seat's calls actually
			// reported a cached-prompt count — see
			// agentbackend.ModelUsage.CachedInputTokens.
			CachedInputTokens:     snap.CachedInputTokens,
			CacheWriteInputTokens: snap.CacheWriteInputTokens,
			Wall:                  snap.Wall,
		})
	}
	return out
}

// costLine formats what a set of model calls cost: the scan's total first,
// then a per-role breakdown in the order calls is given (every caller in
// this codebase passes rosterRoleOrder order). Shared by the end-of-scan
// stdout line and `corral scans show --timing` so the two can never
// disagree about the format.
//
// TOKENS, NOT DOLLARS — see agentbackend.Usage's doc for why. Returns "" when
// calls carries no calls at all (every role's Calls is 0, or the slice is
// empty), and the caller must print no line rather than a cost line for zero
// calls.
//
// A scan that reused every verdict from the cache reaches that empty case,
// but NOT because a cached verdict carries no ModelCalls — it carries the
// full slice the run that earned it recorded, restored verbatim from
// verdict_json. It reaches it because scanModelCallTotals drops a cache hit
// before summing. This function is a formatter and enforces nothing about
// reuse; the exclusion lives at the one place both the line and
// scan_model_calls are built from (see buildScanModelCallRows).
func costLine(calls []advpool.ModelCall) string {
	var totalIn, totalOut int64
	var totalCalls int
	var cached []*int64
	parts := make([]string, 0, len(calls))
	for _, c := range calls {
		if c.Calls == 0 {
			continue
		}
		totalIn += c.InputTokens
		totalOut += c.OutputTokens
		totalCalls += c.Calls
		cached = append(cached, c.CachedInputTokens)
		parts = append(parts, fmt.Sprintf("%s %s/%s (%d call%s)",
			c.Role, abbreviateTokens(c.InputTokens), abbreviateTokens(c.OutputTokens), c.Calls, pluralS(c.Calls)))
	}
	if totalCalls == 0 {
		return ""
	}
	// The cached share of those input tokens, when any provider reported one.
	// It is the number the per-survivor writer's whole cost story turns on —
	// a file's N calls share one prefix, and `900k in` alone cannot tell a
	// fan-out that reused it from one that paid for it N times. Absent, never
	// "(0 cached)", when nothing measured it: most providers say nothing
	// about caching, and a zero would read as a measured total miss.
	cachedNote := ""
	if sum := sumCacheCounts(cached); sum != nil {
		cachedNote = fmt.Sprintf(" (%s cached)", abbreviateTokens(*sum))
	}
	return fmt.Sprintf("  cost: %s tokens in / %s out across %d call%s%s — %s",
		abbreviateTokens(totalIn), abbreviateTokens(totalOut), totalCalls, pluralS(totalCalls),
		cachedNote, strings.Join(parts, ", "))
}

// sumCacheCounts totals a nullable per-role counter the NULL-not-zero way: a
// role nothing measured contributes nothing, and a set where NOTHING was
// measured totals nil rather than 0.
//
// The distinction is the whole point of the column. 0 is a measurement ("this
// scan read nothing from cache"); nil is "no provider here told us", and the
// two must not print the same.
func sumCacheCounts(vs []*int64) *int64 {
	var total int64
	seen := false
	for _, v := range vs {
		if v == nil {
			continue
		}
		total += *v
		seen = true
	}
	if !seen {
		return nil
	}
	return &total
}

// abbreviateTokens renders a token count the way the cost line does: plain
// below 1000, "Nk" from 1,000 (a whole thousand prints with no decimal;
// anything else gets one), and "N.NM" from 100,000 — the point at which a
// k-value would need six digits to stay exact, which reads worse than the
// one digit of precision an M value loses.
func abbreviateTokens(n int64) string {
	switch {
	case n >= 100_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		if n%1000 == 0 {
			return fmt.Sprintf("%dk", n/1000)
		}
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	default:
		return strconv.FormatInt(n, 10)
	}
}

// pluralS is the one place "call(s)" decides which spelling to use.
func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// addCacheCount folds one row's nullable cache counter into a running total,
// keeping nil until something is actually measured. It is sumCacheCounts in
// accumulator form, for a loop that has one value at a time.
func addCacheCount(total, add *int64) *int64 {
	if add == nil {
		return total
	}
	if total == nil {
		zero := int64(0)
		total = &zero
	}
	v := *total + *add
	return &v
}

// budgetLine is the cost cap's disclosure, printed under the cost line when
// a --max-tokens was given: "cap: N tokens — reached after K calls at M
// tokens; seats past that point were refused" when it bit, or the cap and
// the spend beside it when it did not. "" with no cap, so a run that named
// none says nothing about money it never bounded.
func budgetLine(b *agentbackend.TokenBudget) string {
	if b == nil {
		return ""
	}
	hit, cap, spent, at := b.Exhausted()
	if hit {
		return fmt.Sprintf("  cap: --max-tokens %s REACHED after %d call(s) at %s tokens — every later model call was refused; a file whose generator never ran is ungradable for that reason, a writer or critic seat past it was skipped", abbreviateTokens(cap), at, abbreviateTokens(spent))
	}
	return fmt.Sprintf("  cap: --max-tokens %s, %s spent", abbreviateTokens(cap), abbreviateTokens(b.Spent()))
}

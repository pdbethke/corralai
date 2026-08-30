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
			// Retries stays 0 — see advpool.ModelCall's doc and
			// agentbackend.UsageMeter's: nothing in this codebase has a
			// retry loop to observe yet.
			InputTokens: snap.InputTokens, OutputTokens: snap.OutputTokens,
			Wall: snap.Wall,
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
// empty): a scan that reused every verdict from the cache spent nothing, and
// the caller must print no line rather than a cost line for zero calls.
func costLine(calls []advpool.ModelCall) string {
	var totalIn, totalOut int64
	var totalCalls int
	parts := make([]string, 0, len(calls))
	for _, c := range calls {
		if c.Calls == 0 {
			continue
		}
		totalIn += c.InputTokens
		totalOut += c.OutputTokens
		totalCalls += c.Calls
		parts = append(parts, fmt.Sprintf("%s %s/%s (%d call%s)",
			c.Role, abbreviateTokens(c.InputTokens), abbreviateTokens(c.OutputTokens), c.Calls, pluralS(c.Calls)))
	}
	if totalCalls == 0 {
		return ""
	}
	return fmt.Sprintf("  cost: %s tokens in / %s out across %d call%s — %s",
		abbreviateTokens(totalIn), abbreviateTokens(totalOut), totalCalls, pluralS(totalCalls), strings.Join(parts, ", "))
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

// SPDX-License-Identifier: Elastic-2.0

package agentbackend

import (
	"sync"

	"github.com/pdbethke/corralai/internal/agentworker"
)

// Usage is what a provider reported for ONE model call.
//
// Every provider returns this on every response and corral used to drop it at
// the JSON boundary — the response structs simply did not declare the field, so
// encoding/json discarded it in silence. That is the same failure shape this
// codebase keeps finding in itself: a real measurement, handed over, thrown
// away.
//
// It matters because an audit's cost is O(mutants x the target's suite runtime)
// on the execution side and O(tokens) on the model side. The ledger records the
// suite half (scan_files.suite_baseline_ms). Without this it cannot record the
// other half, so "what did that audit cost" and "what would a hosted tier cost"
// are both answerable only by estimate.
//
// TOKENS, NOT DOLLARS, on purpose. Prices change, differ by contract, and are
// renegotiated; a token count is a measurement that stays true. Pricing belongs
// in the query that reads the ledger, not in the record.
type Usage struct {
	InputTokens  int
	OutputTokens int
}

// UsageMeter accumulates Usage across the calls of one run.
//
// It is mutex-guarded because certify --local auto-sizes its swarm to the
// host's cores — eight seats calling models concurrently is the normal case,
// not the edge — and the totals are read once at the end from a different
// goroutine.
//
// The zero value is ready to use, and a nil *UsageMeter is a valid no-op so a
// caller that does not care about tokens need not invent one.
type UsageMeter struct {
	mu    sync.Mutex
	in    int64
	out   int64
	calls int64
}

// Add records one call's usage. Safe on a nil meter.
func (m *UsageMeter) Add(u Usage) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.in += int64(u.InputTokens)
	m.out += int64(u.OutputTokens)
	m.calls++
}

// Totals reports the accumulated input tokens, output tokens, and call count.
//
// calls is reported alongside the tokens because a provider that returns no
// usage leaves the token counts at zero, and "0 tokens over 0 calls" (nothing
// ran) must stay distinguishable from "0 tokens over 40 calls" (it ran and the
// provider told us nothing). A bare token total cannot carry that difference.
func (m *UsageMeter) Totals() (inputTokens, outputTokens, calls int64) {
	if m == nil {
		return 0, 0, 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.in, m.out, m.calls
}

// meteredChatter is AsChatter plus an observer. It changes nothing about the
// reply — the meter is written on the way past, and a metering failure cannot
// exist because there is nothing to fail.
type meteredChatter struct {
	inner Backend
	meter *UsageMeter
}

// AsChatterMetered adapts b to agentworker.Chatter and records every call's
// reported usage into meter. A nil meter makes it equivalent to AsChatter.
func AsChatterMetered(b Backend, meter *UsageMeter) agentworker.Chatter {
	return meteredChatter{inner: b, meter: meter}
}

func (c meteredChatter) Chat(messages []agentworker.Message, tools []any) (agentworker.Message, error) {
	reply, usage, err := chatConverting(c.inner, messages, tools)
	// Recorded even on error: a call that failed after the provider billed it
	// still cost money, and a ledger that counts only successes understates the
	// bill in exactly the runs worth understanding. Read off the REPLY rather
	// than stashed on the backend — certify --local runs up to eight seats
	// against one backend value concurrently, and per-backend mutable state
	// would race.
	c.meter.Add(usage)
	return reply, err
}

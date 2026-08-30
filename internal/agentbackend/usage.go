// SPDX-License-Identifier: Elastic-2.0

package agentbackend

import (
	"sync"
	"time"

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

// UsageMeter accumulates Usage across the calls of one run — or, since this
// task, of one ROLE's seat within a run. certify_local.go's localChatterFor
// now builds one meter PER ROLE rather than one shared by the whole run, so a
// per-file, per-role ledger row (scan_model_calls) can be built from it
// without guessing which seat spent what.
//
// It is mutex-guarded because certify --local auto-sizes its swarm to the
// host's cores — eight seats calling models concurrently is the normal case,
// not the edge — and the totals are read once at the end from a different
// goroutine.
//
// The zero value is ready to use, and a nil *UsageMeter is a valid no-op so a
// caller that does not care about tokens need not invent one.
//
// Retries are deliberately NOT a field here. agentbackend has no retry or
// backoff loop anywhere in this package (checked before writing this) — every
// backend makes exactly one HTTP call per Chat and returns whatever came
// back, success or error. There is nothing for this meter to observe, so
// there is no Retries counter to fake by defaulting it to zero and letting a
// reader mistake that for "measured: none happened". Callers that map a
// meter's Snapshot into a ledger row (advpool.ModelCall, scanstore.ModelCall)
// must say so explicitly rather than let a bare 0 imply a measurement that
// was never made.
type UsageMeter struct {
	mu    sync.Mutex
	in    int64
	out   int64
	calls int64
	wall  time.Duration

	// Model is which model this meter is timing. Set once, at construction,
	// before the meter is handed to any Chatter — one meter per role means
	// the model never changes after that, so reading it needs no lock.
	Model string
}

// Add records one call's usage with no timing. Safe on a nil meter. Kept
// beside AddTimed (rather than folded into it with a zero duration argument
// at every call site) because the concurrency test in this file, and any
// other caller that does not have a duration to report, should not have to
// invent one.
func (m *UsageMeter) Add(u Usage) {
	m.AddTimed(u, 0)
}

// AddTimed records one call's usage AND how long the call took. meteredChatter
// is the only caller that has a duration to give it — everything else that
// wants to record usage without timing it should call Add.
func (m *UsageMeter) AddTimed(u Usage, wall time.Duration) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.in += int64(u.InputTokens)
	m.out += int64(u.OutputTokens)
	m.calls++
	m.wall += wall
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

// ModelUsage is a snapshot of one meter's accumulated totals, read out as a
// single value rather than four separate accessor calls that could observe
// the meter mid-update relative to one another.
type ModelUsage struct {
	Model                     string
	InputTokens, OutputTokens int64
	Calls                     int64
	Wall                      time.Duration
}

// Snapshot reads every field of the meter at once, under one lock
// acquisition. A nil meter reports the zero value — no calls, no model —
// which every caller building a ledger row already treats as "this role made
// no calls" (see the ZERO-CALLS-MEANS-NO-ROW rule at every consumer of this
// type).
func (m *UsageMeter) Snapshot() ModelUsage {
	if m == nil {
		return ModelUsage{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return ModelUsage{Model: m.Model, InputTokens: m.in, OutputTokens: m.out, Calls: m.calls, Wall: m.wall}
}

// meteredChatter is AsChatter plus an observer. It changes nothing about the
// reply — the meter is written on the way past, and a metering failure cannot
// exist because there is nothing to fail.
type meteredChatter struct {
	inner Backend
	meter *UsageMeter
}

// AsChatterMetered adapts b to agentworker.Chatter and records every call's
// reported usage, and its wall-clock duration, into meter. A nil meter makes
// it equivalent to AsChatter.
func AsChatterMetered(b Backend, meter *UsageMeter) agentworker.Chatter {
	return meteredChatter{inner: b, meter: meter}
}

func (c meteredChatter) Chat(messages []agentworker.Message, tools []any) (agentworker.Message, error) {
	// Timed around the WHOLE call, success or failure: a call that failed
	// after the provider billed it still cost money and still held a
	// connection open, and a ledger that only clocks successes understates
	// the wait in exactly the runs worth understanding.
	start := time.Now()
	reply, usage, err := chatConverting(c.inner, messages, tools)
	wall := time.Since(start)
	// Read off the REPLY rather than stashed on the backend — certify --local
	// runs up to eight seats against one backend value concurrently, and
	// per-backend mutable state would race.
	c.meter.AddTimed(usage, wall)
	return reply, err
}

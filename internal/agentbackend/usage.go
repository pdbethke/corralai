// SPDX-License-Identifier: Elastic-2.0

package agentbackend

import (
	"errors"
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
	// InputTokens is the WHOLE prompt this call sent, cached part included —
	// one meaning for every provider, so the ledger's input_tokens series can
	// be summed across seats of different vendors.
	//
	// It is NORMALISED at the backend where a wire disagrees. The
	// OpenAI-compatible wire's prompt_tokens already includes its cached
	// half. Anthropic's three input counters are DISJOINT — `input_tokens`
	// is only the uncached remainder — so the Anthropic backend adds the two
	// cache counters back in before filling this field. Without that, a seat
	// whose prefix cached well would report a smaller prompt than the
	// identical uncached one, which reads as a saving in the wrong column.
	InputTokens  int
	OutputTokens int
	// CachedInputTokens is how many of InputTokens the provider served from
	// its own prompt cache — Anthropic's `cache_read_input_tokens`, the
	// OpenAI-compatible wire's `prompt_tokens_details.cached_tokens` (which
	// is also what Gemini's OpenAI-compatible endpoint reports for its
	// implicit cache).
	//
	// It exists because the writer seat now sends the SAME prefix (the file,
	// the signature surface, the harness exemplar) on every one of a file's
	// per-survivor calls, and that prefix is most of the prompt. Whether the
	// provider actually reused it is the difference between a fan-out that
	// costs N x the file and one that costs the file once — and InputTokens
	// alone cannot express it: a cached 40k-token prompt and a fresh one
	// report the same total.
	//
	// A SUBSET of InputTokens, on every provider — see that field: the
	// Anthropic backend normalises its disjoint counters so the subset
	// relation holds there too, rather than leaving one vendor's numbers to
	// mean something else.
	//
	// A POINTER, and nil is the common case. A response that says nothing
	// about caching has not reported a MISS; it has reported nothing. A
	// stored 0 is a measurement ("this call read nothing from cache") that a
	// later query would average, so the honest value for silence is NULL —
	// the same NULL-not-zero rule ModelCall.Retries follows, for the same
	// reason.
	CachedInputTokens *int64
	// CacheWriteInputTokens is how many tokens the provider WROTE into its
	// cache on this call — Anthropic's `cache_creation_input_tokens`.
	//
	// It is recorded beside the reads because a cache write is billed at
	// 1.25x an ordinary input token: the FIRST call of a fan-out costs more
	// than an uncached one, and only the calls after it save. A ledger that
	// held the reads alone would report the saving and hide its price, which
	// is the wrong half of the trade to be able to see.
	//
	// Nullable, separately from CachedInputTokens: a response that reports a
	// write and no read has not reported a read of zero. Only Anthropic
	// reports this at all — the OpenAI-compatible wire has no equivalent, and
	// Gemini's implicit cache has no write to bill — so it is NULL almost
	// everywhere, which is exactly what NULL is for.
	CacheWriteInputTokens *int64
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
	// cached accumulates CachedInputTokens across the calls that REPORTED
	// one; cachedSeen says whether any call did. Two fields rather than a
	// *int64 because a meter is written under a lock from several
	// goroutines, and "measured, and it was zero" must stay distinguishable
	// from "nothing measured" without allocating on every Add.
	cached     int64
	cachedSeen bool
	// The write half of the same pair, tracked separately for the same
	// reason: "reported a write, said nothing about reads" is a real shape.
	cacheWrite     int64
	cacheWriteSeen bool

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
	if u.CachedInputTokens != nil {
		m.cached += *u.CachedInputTokens
		m.cachedSeen = true
	}
	if u.CacheWriteInputTokens != nil {
		m.cacheWrite += *u.CacheWriteInputTokens
		m.cacheWriteSeen = true
	}
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
	// CachedInputTokens is the sum over the calls that REPORTED a cached
	// prompt-token count, and nil when no call did — see Usage's own field
	// for why silence is NULL and never 0.
	CachedInputTokens *int64
	// CacheWriteInputTokens is the sum over the calls that reported a cache
	// WRITE, nil when none did — see Usage's field for why the price of the
	// saving is recorded beside the saving.
	CacheWriteInputTokens *int64
	Calls                 int64
	Wall                  time.Duration
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
	var cached, written *int64
	if m.cachedSeen {
		v := m.cached
		cached = &v
	}
	if m.cacheWriteSeen {
		v := m.cacheWrite
		written = &v
	}
	return ModelUsage{Model: m.Model, InputTokens: m.in, OutputTokens: m.out,
		CachedInputTokens: cached, CacheWriteInputTokens: written,
		Calls: m.calls, Wall: m.wall}
}

// meteredChatter is AsChatter plus an observer. It changes nothing about the
// reply — the meter is written on the way past, and a metering failure cannot
// exist because there is nothing to fail.
type meteredChatter struct {
	inner  Backend
	meter  *UsageMeter
	budget *TokenBudget
}

// AsChatterMetered adapts b to agentworker.Chatter and records every call's
// reported usage, and its wall-clock duration, into meter. A nil meter makes
// it equivalent to AsChatter.
func AsChatterMetered(b Backend, meter *UsageMeter) agentworker.Chatter {
	return meteredChatter{inner: b, meter: meter}
}

// AsChatterBudgeted is AsChatterMetered with a run-wide TokenBudget the
// chatter consults before every call; a nil budget is no cap.
func AsChatterBudgeted(b Backend, meter *UsageMeter, budget *TokenBudget) agentworker.Chatter {
	return meteredChatter{inner: b, meter: meter, budget: budget}
}

func (c meteredChatter) Chat(messages []agentworker.Message, tools []any) (agentworker.Message, error) {
	// The cap is consulted BEFORE the provider is dialled: a refused call
	// costs nothing and reports nothing to the meter.
	if !c.budget.Allow() {
		return agentworker.Message{}, ErrTokenBudgetExhausted
	}
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
	c.budget.Charge(usage)
	return reply, err
}

// TokenBudget is a per-RUN cap on tokens (input + output, every seat, every
// model) that a meteredChatter consults before each call. Corral bounded
// mutants, shards, wall clock and concurrency and nothing in tokens: a
// per-survivor writer on a survivor-heavy file paid N calls and nothing
// clamped it. The cap is checked BEFORE a call and charged after it, so one
// in-flight call can overshoot by its own size — said in the error rather
// than pretended away; estimating a call's cost from its prompt would be a
// guess presented as a bound.
//
// Shared by every seat of a run (a scan's files included), so it is
// mutex-guarded; a nil *TokenBudget is "no cap", so every existing caller
// keeps its behaviour.
type TokenBudget struct {
	mu    sync.Mutex
	cap   int64
	spent int64
	// hit is set the first time a call was refused, so the disclosure can
	// say WHEN the cap bit rather than only that it did.
	hit      bool
	hitCalls int64
	calls    int64
}

// NewTokenBudget returns a budget of cap tokens; cap <= 0 returns nil (no
// cap), so "unset" and "no cap" are the same value everywhere.
func NewTokenBudget(cap int64) *TokenBudget {
	if cap <= 0 {
		return nil
	}
	return &TokenBudget{cap: cap}
}

// ErrTokenBudgetExhausted is returned by a metered chatter INSTEAD of
// calling the provider once the run's spend has reached its cap. It wraps
// nothing: no provider was contacted, no money was spent on the refused call.
var ErrTokenBudgetExhausted = errors.New("the run's --max-tokens cap is reached; no further model call is made")

// Allow reports whether one more call may be made, refusing once spent >=
// cap. Recorded so Exhausted can say after how many calls.
func (b *TokenBudget) Allow() bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.spent >= b.cap {
		if !b.hit {
			b.hit, b.hitCalls = true, b.calls
		}
		return false
	}
	return true
}

// Charge records a completed call's usage.
func (b *TokenBudget) Charge(u Usage) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.spent += int64(u.InputTokens) + int64(u.OutputTokens)
	b.calls++
	b.mu.Unlock()
}

// Exhausted reports whether the cap ever refused a call, with the spend and
// the call count at that moment — the disclosure a verdict carries.
func (b *TokenBudget) Exhausted() (hit bool, cap, spent, calls int64) {
	if b == nil {
		return false, 0, 0, 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.hit, b.cap, b.spent, b.hitCalls
}

// Spent is the running total: what the cost line prints beside the cap.
func (b *TokenBudget) Spent() int64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.spent
}

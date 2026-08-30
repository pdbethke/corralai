// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"encoding/json"
	"time"
)

// ModelCall is what ONE role's seat cost on ONE file — the money grain,
// mirrored from agentbackend.UsageMeter.Snapshot into a value this package's
// callers can carry on a Verdict without importing agentbackend.
//
// The scan header (and Verdict as a whole) can say what a WHOLE run cost;
// this is what lets a caller ask "which seat was slow, and on which file" —
// the same reason scanstore.ModelCall (the ledger row this mirrors into)
// exists.
//
// Retries is *int, not int, and is nil on every ModelCall this package
// produces today. agentbackend has no retry or backoff loop in any of its
// backends — every Chat call is exactly one HTTP round trip — so there is
// nothing this field could report short of inventing a number. A stored 0
// is a MEASUREMENT ("this seat retried zero times"); nil is the honest
// value for "nothing here observes retries yet", the same NULL-not-zero rule
// every other unmeasured column in this ledger follows. It stays a pointer
// field, not a bool "was this measured" flag beside a bare int, because a
// backend that DOES grow a retry loop should not need a second field to say
// so — a non-nil value already means "measured".
type ModelCall struct {
	Role, Model               string
	Calls                     int
	Retries                   *int
	InputTokens, OutputTokens int64
	// CachedInputTokens is how many of InputTokens the provider served from
	// its own prompt cache, summed over this seat's calls — mirrored from
	// agentbackend.ModelUsage.
	//
	// It is the measurement the per-survivor writer's whole cost story rests
	// on: one call per survivor sends the same file, signatures and harness
	// every time, and whether that prefix was reused is the difference
	// between paying for the file once and paying for it N times.
	// InputTokens alone cannot say — a cached 40k prompt and a fresh one
	// report the same total.
	//
	// *int64 and nil by default, for exactly the reason Retries is: a
	// provider that says nothing about caching has reported nothing, not a
	// miss. A stored 0 is a MEASUREMENT ("this seat read nothing from
	// cache") that a later query would average; NULL is the honest value
	// for silence.
	CachedInputTokens *int64
	// CacheWriteInputTokens is what this seat paid to FILL the cache the
	// reads above drew on — Anthropic bills a write at 1.25x an ordinary
	// input token, so the first call of a fan-out is dearer than an uncached
	// one and only the rest save. Recording the reads alone would show the
	// saving and hide its price. Nullable, and nil separately from
	// CachedInputTokens: only Anthropic reports it.
	CacheWriteInputTokens *int64
	// Wall is this role's accumulated wall-clock time across every call this
	// file's audit made it — a MEASUREMENT (agentbackend.UsageMeter times
	// every call), never a budget. It rides on the Verdict the same way
	// Timing does, and for the same reason it is excluded from the signed
	// attestation and the verdict cache key: wall-clock varies run to run for
	// reasons that have nothing to do with what was proven (host load,
	// network jitter), so it must never change what a cached verdict serves
	// or what a statement attests to — see certify_repo.go's
	// writeAuditStatement, which builds certify.AuditedFile from named
	// fields and never reaches for Timing or ModelCalls.
	Wall time.Duration
}

// modelCallWire is ModelCall on the wire: milliseconds, not a raw
// time.Duration nanosecond count — the same reasoning as timingWire. A phase
// that made no calls is omitted rather than written as an explicit zero, and
// an unmeasured Retries is ABSENT rather than an explicit `"retries":0` —
// the same NULL-not-zero rule the ledger column follows, carried onto the
// wire so a cached verdict cannot round-trip nil into 0.
type modelCallWire struct {
	Role                  string `json:"role"`
	Model                 string `json:"model"`
	Calls                 int    `json:"calls"`
	Retries               *int   `json:"retries,omitempty"`
	InputTokens           int64  `json:"input_tokens,omitempty"`
	OutputTokens          int64  `json:"output_tokens,omitempty"`
	CachedInputTokens     *int64 `json:"cached_input_tokens,omitempty"`
	CacheWriteInputTokens *int64 `json:"cache_write_input_tokens,omitempty"`
	WallMillis            int64  `json:"wall_ms,omitempty"`
}

// MarshalJSON writes the millisecond form, so a verdict served from the
// ledger's cache round-trips ModelCalls the same way it already round-trips
// Timing.
func (c ModelCall) MarshalJSON() ([]byte, error) {
	return json.Marshal(modelCallWire{
		Role: c.Role, Model: c.Model, Calls: c.Calls, Retries: c.Retries,
		InputTokens: c.InputTokens, OutputTokens: c.OutputTokens,
		CachedInputTokens:     c.CachedInputTokens,
		CacheWriteInputTokens: c.CacheWriteInputTokens,
		WallMillis:            c.Wall.Milliseconds(),
	})
}

// UnmarshalJSON reads the millisecond form back into a time.Duration.
func (c *ModelCall) UnmarshalJSON(b []byte) error {
	var w modelCallWire
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*c = ModelCall{
		Role: w.Role, Model: w.Model, Calls: w.Calls, Retries: w.Retries,
		InputTokens: w.InputTokens, OutputTokens: w.OutputTokens,
		CachedInputTokens:     w.CachedInputTokens,
		CacheWriteInputTokens: w.CacheWriteInputTokens,
		Wall:                  time.Duration(w.WallMillis) * time.Millisecond,
	}
	return nil
}

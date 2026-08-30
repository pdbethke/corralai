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
// Retries is always 0 today. agentbackend has no retry or backoff loop in
// any of its backends — every Chat call is exactly one HTTP round trip — so
// there is nothing this field could report short of inventing a number. It
// stays in the type because the interface this package promises callers
// includes it, and because a backend that DOES grow a retry loop should not
// need a new field to report it. Until then, read it as "not measured", not
// "zero retries occurred".
type ModelCall struct {
	Role, Model               string
	Calls, Retries            int
	InputTokens, OutputTokens int64
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
// that made no calls is omitted rather than written as an explicit zero.
type modelCallWire struct {
	Role         string `json:"role"`
	Model        string `json:"model"`
	Calls        int    `json:"calls"`
	Retries      int    `json:"retries,omitempty"`
	InputTokens  int64  `json:"input_tokens,omitempty"`
	OutputTokens int64  `json:"output_tokens,omitempty"`
	WallMillis   int64  `json:"wall_ms,omitempty"`
}

// MarshalJSON writes the millisecond form, so a verdict served from the
// ledger's cache round-trips ModelCalls the same way it already round-trips
// Timing.
func (c ModelCall) MarshalJSON() ([]byte, error) {
	return json.Marshal(modelCallWire{
		Role: c.Role, Model: c.Model, Calls: c.Calls, Retries: c.Retries,
		InputTokens: c.InputTokens, OutputTokens: c.OutputTokens,
		WallMillis: c.Wall.Milliseconds(),
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
		Wall: time.Duration(w.WallMillis) * time.Millisecond,
	}
	return nil
}

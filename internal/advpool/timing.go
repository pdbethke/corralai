// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"encoding/json"
	"time"
)

// Timing is where one file's audit spent its wall clock, phase by phase.
//
// It exists because the answer was previously unobtainable. A run on
// psf/requests' adapters.py takes ~43 minutes and ~35 of them are the dev
// pass; the only timing the ledger carried was the compliant baseline
// (adequacy.Report.BaselineDuration), so establishing that ratio meant
// subtracting timestamps out of log lines by hand, once, for one file. Cost
// is the SaaS blocker for this product and it was being measured by eye.
//
// EVERY field is a duration that was actually MEASURED, and the zero value
// means the phase did not run — never "it ran and took no time". Two of the
// seven cannot be measured by the driver at all and arrive on the RunSpec
// (see RunSpec.SelectionDuration and RunSpec.PoolDuration): the scan runs the
// instrumented selection pass once for the whole repo, and the workspace
// pool's copies and probe happen before the driver is even constructed. The
// remaining five are measured at the DAG's own boundaries in driver.go.
//
// Total is the file's WHOLE audit — the driver's own elapsed time plus the
// two phases paid before the driver existed — so it is at least the sum of
// the six phases. The difference is queue latency and the bookkeeping between
// them, which is exactly the residual worth seeing.
type Timing struct {
	// Selection is the scan's ONE instrumented run (see
	// RunSpec.SelectionDuration). It is a per-scan cost carried on every
	// file's verdict, not a per-file measurement: a sum over files would
	// count it once per file and invent time nobody spent.
	Selection time.Duration
	// Generation is from the moment the mutant-generator seats were enqueued
	// to the moment their results were parsed into the exam. Zero on a
	// --mutants replay, which dispatches no generator at all.
	Generation time.Duration
	// Pool is the workspace substrate's copy of the checkout plus its
	// concurrency probe (adequacy.Disclosure.CopyDuration + ProbeDuration).
	// Zero on the jail substrate and on a pool of one, both of which copy
	// nothing and probe nothing.
	Pool time.Duration
	// DevPass is the dev suite scored against every mutant — on real repos
	// the overwhelming majority of an audit, and the number this whole type
	// exists to surface.
	//
	// It is the WHOLE dev pass, which is one adequacy.Score call: the
	// compliant baseline run and the canary run are inside it, as are the
	// compile gate's checks. The baseline is ALSO reported on its own as
	// Verdict.BaselineDuration — that is a component of this number, not an
	// eighth phase beside it, and adding the two would double-count it.
	DevPass time.Duration
	// AuthoredPass is from the end of the dev pass to the pool's own score:
	// the test-writer seat's model time, its compile retries, and the run of
	// its test against the survivors. Zero when a perfect dev suite made the
	// writer moot.
	AuthoredPass time.Duration
	// Critic is what the critic seat ADDED to the run: the time the run
	// waited on it after the pool had already scored. The critic runs in
	// parallel with the rest of the DAG, so the honest cost of having one is
	// the delay it imposes at the end, not its own elapsed model time. Zero
	// when no critic was assigned (`--critic-model off`).
	Critic time.Duration
	// Total is the file's whole audit: the driver's own wall clock, from
	// StartRun to the verdict, PLUS Selection and Pool — which were spent on
	// this file's behalf before StartRun and so are not in that window. See
	// totalWith. It is not measured independently of the parts, so it cannot
	// disagree with them about which work it covers.
	Total time.Duration
}

// timingWire is Timing on the wire: MILLISECONDS, as integers, under
// snake_case keys, and a phase that did not run is ABSENT rather than 0.
//
// The whole Verdict is marshalled into the ledger's verdict_json (and read
// back from it on a cache hit), pushed to the warehouse and hashed into the
// signed record. Go's default rendering of a time.Duration is a bare
// nanosecond integer under a Go field name — a number no other reader of that
// column could interpret without knowing it came from Go, at a precision
// nothing here measures to.
type timingWire struct {
	Selection    int64 `json:"selection_ms,omitempty"`
	Generation   int64 `json:"generation_ms,omitempty"`
	Pool         int64 `json:"pool_ms,omitempty"`
	DevPass      int64 `json:"dev_pass_ms,omitempty"`
	AuthoredPass int64 `json:"authored_pass_ms,omitempty"`
	Critic       int64 `json:"critic_ms,omitempty"`
	Total        int64 `json:"total_ms,omitempty"`
}

// MarshalJSON writes the millisecond form. Sub-millisecond durations round to
// zero and therefore vanish, which is correct for this record: nothing in an
// audit that is worth a column finishes in under a millisecond, and a phase
// reported as "0" would be read as one that did not run.
func (t Timing) MarshalJSON() ([]byte, error) {
	return json.Marshal(timingWire{
		Selection:    t.Selection.Milliseconds(),
		Generation:   t.Generation.Milliseconds(),
		Pool:         t.Pool.Milliseconds(),
		DevPass:      t.DevPass.Milliseconds(),
		AuthoredPass: t.AuthoredPass.Milliseconds(),
		Critic:       t.Critic.Milliseconds(),
		Total:        t.Total.Milliseconds(),
	})
}

// UnmarshalJSON reads it back, so a verdict served from the ledger's cache
// still says where its time went.
func (t *Timing) UnmarshalJSON(b []byte) error {
	var w timingWire
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*t = Timing{
		Selection:    time.Duration(w.Selection) * time.Millisecond,
		Generation:   time.Duration(w.Generation) * time.Millisecond,
		Pool:         time.Duration(w.Pool) * time.Millisecond,
		DevPass:      time.Duration(w.DevPass) * time.Millisecond,
		AuthoredPass: time.Duration(w.AuthoredPass) * time.Millisecond,
		Critic:       time.Duration(w.Critic) * time.Millisecond,
		Total:        time.Duration(w.Total) * time.Millisecond,
	}
	return nil
}

// Measured reports whether ANY phase of this run was timed. A verdict served
// from a ledger row written before the clock existed measured none of them,
// and every reader (the report line, `corral scans show --timing`) stays
// silent rather than printing seven em dashes.
func (t Timing) Measured() bool { return t != Timing{} }

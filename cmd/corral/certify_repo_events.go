// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/scanstore"
)

// scanEventCap bounds how many events one scan's sink holds in memory. A
// runaway suite (a flapping test that keeps the pool ticking, a shard that
// never converges) must not OOM the runner just because the run's timeline
// is now a table — see scanEventSink.record for what happens at the cap.
const scanEventCap = 100_000

// scanEventSink collects a `certify --repo` scan's driver beats into
// scanstore.Event rows — the run's timeline as a table (see
// cmd/corral/certify_repo_record.go's RecordEvents call, which this sink
// finally has something to feed).
//
// ONE sink per scan, shared by every file job: Seq must be strictly
// increasing PER SCAN, not per file, so the tape's order is the scan's own
// order, not an accident of which file's goroutine happened to run first.
// Every file's driver gets its OWN advpool.EventSink (see forFile), a thin
// adapter that stamps that file's path onto every beat before handing it to
// this shared sink — the driver's own Emit carries no path, and scan-scoped
// beats (the one instrumented selection pass, preflight) use forScan
// instead, which records Path "".
//
// Thread-safe: per-file LLM workers run concurrently on the workspace
// substrate's own budget (see localExecutor.perFileSwarm), and every file on
// the jail substrate runs in its own goroutine regardless.
type scanEventSink struct {
	mu     sync.Mutex
	now    func() time.Time
	seq    int64
	events []scanstore.Event

	// truncated flips once the cap is hit. From then on, record no longer
	// appends real rows — it counts them (dropped) and rewrites the ONE
	// events_truncated row's detail so the final count is accurate, rather
	// than silently discarding what happened after the cap, which is exactly
	// the silent-measurement-loss shape this ledger exists to end.
	truncated    bool
	truncatedIdx int
	dropped      int64
}

// newScanEventSink constructs a sink whose events carry now()'s time. now
// nil defaults to time.Now — every production caller's position; tests
// inject a fixed or stepping clock so TS is assertable.
func newScanEventSink(now func() time.Time) *scanEventSink {
	if now == nil {
		now = time.Now
	}
	return &scanEventSink{now: now}
}

// record appends one event under path ("" for a scan-scoped beat), JSON-
// encoding detail (nil becomes "{}" — an event with no detail still
// happened, so Detail is never NULL) and lifting a "duration_ms" key in
// detail into DurationMillis when it is present and positive. A phase that
// did not run must never emit a duration key at all — see the driver's own
// phase-boundary emits, which only ever set it when the phase actually
// closed.
func (s *scanEventSink) record(path, kind, subject string, detail map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.truncated {
		s.dropped++
		s.rewriteTruncatedLocked()
		return
	}
	s.seq++
	s.events = append(s.events, buildScanEvent(path, s.seq, s.now(), kind, subject, detail))
	if int64(len(s.events)) >= scanEventCap {
		s.truncated = true
		s.seq++
		s.truncatedIdx = len(s.events)
		s.events = append(s.events, scanstore.Event{
			Seq: s.seq, TS: s.now(), Kind: "events_truncated", Detail: `{"dropped":0}`,
		})
	}
}

// rewriteTruncatedLocked updates the ONE events_truncated row's detail with
// the current drop count. Called with mu held.
func (s *scanEventSink) rewriteTruncatedLocked() {
	b, err := json.Marshal(map[string]any{"dropped": s.dropped})
	if err != nil {
		return
	}
	s.events[s.truncatedIdx].Detail = string(b)
}

// forScan returns a record closure for scan-scoped beats (Path "") — the
// scan's one instrumented selection pass, a repo-wide preflight. Not an
// EventSink: the driver never emits these itself (they happen outside any
// per-file run), so the caller (localExecutor) records them directly.
func (s *scanEventSink) forScan(kind, subject string, detail map[string]any) {
	if s == nil {
		return
	}
	s.record("", kind, subject, detail)
}

// forFile returns an advpool.EventSink that stamps path onto every beat the
// driver emits for THAT file's run, then forwards it into this shared scan
// sink. Returns nil for a nil sink so a dry-run/no-ledger executor's
// localAuditInput.eventSink stays nil (advpool.Driver's emit helper already
// no-ops on a nil Events).
func (s *scanEventSink) forFile(path string) advpool.EventSink {
	if s == nil {
		return nil
	}
	return &scanEventFileSink{sink: s, path: path}
}

// drain returns a copy of every event recorded so far, in Seq order (the
// order they were appended). A copy, not the live slice: the caller (the
// end-of-scan recording sequence) must not observe a sink another file's
// still-running goroutine is still appending to — but by the point
// certify --repo calls this, every job has already finished.
func (s *scanEventSink) drain() []scanstore.Event {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]scanstore.Event, len(s.events))
	copy(out, s.events)
	return out
}

// buildScanEvent turns one driver beat into a scanstore.Event: detail
// marshalled to JSON text (defaulting to "{}", never empty/NULL), and
// DurationMillis lifted from a positive "duration_ms" detail key.
func buildScanEvent(path string, seq int64, ts time.Time, kind, subject string, detail map[string]any) scanstore.Event {
	if detail == nil {
		detail = map[string]any{}
	}
	b, err := json.Marshal(detail)
	if err != nil {
		b = []byte("{}")
	}
	e := scanstore.Event{Path: path, Seq: seq, TS: ts, Kind: kind, Subject: subject, Detail: string(b)}
	if ms, ok := durationMillisFromDetail(detail); ok {
		e.DurationMillis = &ms
	}
	return e
}

// durationMillisFromDetail reads detail["duration_ms"], accepting every
// numeric shape a driver emit's map[string]any literal can carry (int,
// int64, float64 — json.Unmarshal into any always produces float64, but the
// driver's own emits build the map in Go code, which is typically int64).
// <= 0 is treated as absent: a phase that did not run must record NO
// duration, never a false zero (see scanstore.Event.DurationMillis' doc).
func durationMillisFromDetail(detail map[string]any) (int64, bool) {
	v, ok := detail["duration_ms"]
	if !ok {
		return 0, false
	}
	var ms int64
	switch n := v.(type) {
	case int64:
		ms = n
	case int:
		ms = int64(n)
	case float64:
		ms = int64(n)
	default:
		return 0, false
	}
	if ms <= 0 {
		return 0, false
	}
	return ms, true
}

// scanEventFileSink adapts a shared scanEventSink to advpool.EventSink for
// ONE file's driver run, stamping that file's path onto every beat — the
// grain scanstore.Event records file-scoped events at, since the driver's
// Emit signature carries no path of its own.
type scanEventFileSink struct {
	sink *scanEventSink
	path string
}

// Emit implements advpool.EventSink.
func (f *scanEventFileSink) Emit(_ int64, kind, subject string, detail map[string]any) {
	if f == nil || f.sink == nil {
		return
	}
	f.sink.record(f.path, kind, subject, detail)
}

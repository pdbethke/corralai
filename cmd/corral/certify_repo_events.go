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

// sourceBearingDetailKeys names every event-detail key whose VALUE is
// withheld from the tape — the audited file's bytes, a suite's bytes, a
// mutant's bytes, a runner's raw output, or a
// runner's raw output. The sink drops each one before the detail map is ever
// encoded, so neither the local ledger's scan_events nor the warehouse's
// corral_events can hold source bytes, with or without --push-source.
//
// THE BUG THIS CLOSES. advpool's pool_subject beat carries the whole audited
// file (`code`) and the whole dev suite (`dev_test_code`); the sink JSON-
// encodes a beat's entire detail map into one column and the warehouse
// writer inserts that column with no custody guard on it at all. Every
// audited file's source therefore reached the operator's warehouse on every
// push. Redacting HERE — at the one sink both stores are fed from — is what
// makes the rule hold for both at once, rather than being re-litigated at
// each writer.
//
// `code` and `dev_test_code` are the only two any emit in internal/advpool
// produces today (audited beat by beat: pool_subject, phase_pool,
// phase_generation, pool_shard, pool_dev_adequacy, phase_authored_pass,
// phase_critic, pool_verdict, plus the scan-scoped phase_selection). The
// rest are named in advance because this is a leak that is invisible until
// someone reads a warehouse: a future emit that reaches for the obvious name
// is redacted on arrival instead of shipping source for a release.
// `goal` is on the list for a different reason from the rest, and it is the
// reason the rule is worth stating as a rule. It is not source bytes: it is
// PROSE — what this repo is trying to be true, written by the operator or
// derived from the code — and it is the only free-text field left on the
// beat. The warehouse holds numbers, hashes, reasons and model names. A
// sentence describing what a private repo is defending against is none of
// those, and it rides to the operator's warehouse (and, on a shared one, to
// everyone with SELECT) exactly as the source did. Its LENGTH still ships, so
// "no goal was set" and "a goal was set" stay distinguishable.
var sourceBearingDetailKeys = map[string]bool{
	"code":          true,
	"dev_test_code": true,
	"goal":          true,
	"authored_test": true,
	"test_code":     true,
	"pool_test":     true,
	"mutant_code":   true,
	"output":        true,
	"stdout":        true,
	"stderr":        true,
}

// redactSourceDetail returns detail with every source-bearing key replaced by
// a `<key>_bytes` LENGTH, or detail itself when it carries none. The length,
// not a bare deletion: "the file had 4,102 bytes we did not ship" and "there
// was no file" are different answers, and a tape that cannot tell them apart
// has lost a measurement to protect a custody rule it could have kept
// anyway.
//
// Copy-on-write: the caller's map belongs to the driver that built it (and,
// on the local-run path, to a second sink that renders the same beat), so it
// is never mutated in place.
func redactSourceDetail(detail map[string]any) map[string]any {
	needs := false
	for k := range detail {
		if sourceBearingDetailKeys[k] {
			needs = true
			break
		}
	}
	if !needs {
		return detail
	}
	out := make(map[string]any, len(detail))
	for k, v := range detail {
		if !sourceBearingDetailKeys[k] {
			out[k] = v
			continue
		}
		switch b := v.(type) {
		case string:
			out[k+"_bytes"] = len(b)
		case []byte:
			out[k+"_bytes"] = len(b)
		default:
			// Not a shape whose length means anything. Say that it was
			// withheld rather than guess a size — or, worse, keep it.
			out[k+"_redacted"] = true
		}
	}
	return out
}

// buildScanEvent turns one driver beat into a scanstore.Event: detail
// REDACTED of source (see redactSourceDetail — the custody rule, applied at
// the one sink both the ledger and the warehouse are fed from), marshalled
// to JSON text (defaulting to "{}", never empty/NULL), and DurationMillis
// lifted from a positive "duration_ms" detail key.
func buildScanEvent(path string, seq int64, ts time.Time, kind, subject string, detail map[string]any) scanstore.Event {
	if detail == nil {
		detail = map[string]any{}
	}
	detail = redactSourceDetail(detail)
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

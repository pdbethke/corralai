// SPDX-License-Identifier: Elastic-2.0

package gate

import "time"

// DefaultGateTimeout is the jail deadline a policy gets when it doesn't
// declare its own TimeoutS (or declares <=0). 10 minutes comfortably covers
// a real test suite (corralai's own tests run minutes) — the sandbox
// package's own 60s default is far too short for anything but a toy check
// and, left unset, permanently blocks merge on any real-world command.
const DefaultGateTimeout = 600 * time.Second

// Policy describes how a repo's merge gate should be run: which base
// branches trigger it, the status-check context name reported back to the
// forge, the command that runs the gate's checks, whether that command is
// allowed network access, and how long the jail lets it run before killing
// it. Later tasks (the poller, the runner) consume this; Task 2 only
// defines the shape.
type Policy struct {
	Repo     string
	Base     []string
	Context  string
	CheckCmd []string
	AllowNet bool
	// TimeoutS is the jail deadline in seconds. 0 (unset) means "use
	// DefaultGateTimeout" — see Runner.Run, which computes the effective
	// timeout so Policy itself stays a plain data shape.
	TimeoutS int
}

// Run is one dedupe/index row: (Repo, HeadSHA) identifies a gate run, PR is
// the pull request it ran against, Passed is the outcome, RecordID points at
// the full SIGNED gate record in buildstore, and RanAt is the time the
// runner (Task 4) executed the gate — set by the caller, never by the store,
// so Store stays clock-free and deterministic under test.
type Run struct {
	Repo    string
	HeadSHA string
	PR      int
	Passed  bool
	// Context is the commit-status context this run reported under, and it is
	// part of the row's IDENTITY. Dedupe was keyed on (Repo, HeadSHA) alone,
	// so two policies for one repo under different contexts — which the config
	// documentation invites and configurable contexts imply — collapsed into
	// one row: whichever policy ran first stored it, and the poller then
	// skipped the SECOND policy on every head forever. Its check never ran and
	// its status was never posted, so if that context was a required check the
	// pull request was blocked indefinitely.
	// (Cold review 2026-09-12, R3 — reproduced.)
	Context string
	// StatusPosted records that the verdict actually REACHED the forge. The
	// row used to be written before the status post, so a post that failed
	// (forge 5xx, rate limit, a context cancelled at shutdown) was never
	// retried: the head counted as gated and kept whatever the forge last
	// saw, usually "pending", until someone pushed a new commit. The poller
	// now treats an undelivered row as work still to do.
	// (Cold review 2026-09-12, R4 — reproduced.)
	StatusPosted bool
	RecordID     int64
	RanAt        time.Time
}

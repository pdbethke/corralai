// SPDX-License-Identifier: Elastic-2.0

package gate

import "time"

// Policy describes how a repo's merge gate should be run: which base
// branches trigger it, the status-check context name reported back to the
// forge, the command that runs the gate's checks, and whether that command
// is allowed network access. Later tasks (the poller, the runner) consume
// this; Task 2 only defines the shape.
type Policy struct {
	Repo     string
	Base     []string
	Context  string
	CheckCmd []string
	AllowNet bool
}

// Run is one dedupe/index row: (Repo, HeadSHA) identifies a gate run, PR is
// the pull request it ran against, Passed is the outcome, RecordID points at
// the full SIGNED gate record in buildstore, and RanAt is the time the
// runner (Task 4) executed the gate — set by the caller, never by the store,
// so Store stays clock-free and deterministic under test.
type Run struct {
	Repo     string
	HeadSHA  string
	PR       int
	Passed   bool
	RecordID int64
	RanAt    time.Time
}

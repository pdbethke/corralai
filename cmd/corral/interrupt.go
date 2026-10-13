// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/agentbackend"
)

// operatorInterrupted is THE answer to "did the operator interrupt this run",
// for every verb and every seat type: an error that carries the interrupt
// (agentbackend.ErrInterrupted, through any wrapping), or the process-wide
// record of one (agentbackend.WasInterrupted), which both the agent seats'
// forwarding and auditContext set when they catch the signal. The second half
// is what catches a signal that landed after the last call returned.
//
// When it is true, nothing is recorded: no verdict signed, no ledger entry,
// no attestation, no push, no file labelled with a failure it did not have.
// A record of a run the operator stopped is a record of something that did
// not finish — the same false claim as a measured zero.
func operatorInterrupted(err error) bool {
	return agentbackend.Interrupted(err) || agentbackend.WasInterrupted()
}

// errOperatorInterrupt is the error a run returns when operatorInterrupted
// stops it; it carries agentbackend.ErrInterrupted so every caller's check
// is the same check.
var errOperatorInterrupt = fmt.Errorf("the run was stopped: %w", agentbackend.ErrInterrupted)

// exitInterrupted is every verb's exit status for an operator's interrupt:
// 128 + SIGINT, the shell's own convention, so a script can tell "the
// operator stopped it" from a failed gate (1), a usage error (2) or a
// standing finding (3).
const exitInterrupted = 130

// refuseInterrupted prints the one message an interrupted verb ends with and
// returns exitInterrupted. It is for a run that wrote NOTHING; a run that
// wrote something before the signal says what (refuseInterruptedAfter).
func refuseInterrupted(stderr io.Writer, cmdName string) int {
	fmt.Fprintf(stderr, "%s: interrupted — nothing recorded\n", cmdName)
	return exitInterrupted
}

// refuseInterruptedAfter is refuseInterrupted for a run that had already
// written files before the signal: it removes them, so "nothing recorded"
// stays true, and when one cannot be removed it names what remains instead
// of claiming nothing was written.
func refuseInterruptedAfter(stderr io.Writer, cmdName string, written []string) int {
	var left []string
	for _, f := range written {
		if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
			left = append(left, f)
		}
	}
	if len(left) == 0 {
		return refuseInterrupted(stderr, cmdName)
	}
	fmt.Fprintf(stderr, "%s: interrupted — no ledger entry and no push, but these files written before the signal could not be removed: %s\n", cmdName, strings.Join(left, ", "))
	return exitInterrupted
}

// localInterruptRefuses decides an interrupt for certify --local, which signs
// INSIDE the run: a verdict already signed (RecordID != 0) is a complete,
// recorded run, and a signal that landed after it must not disown it. Any
// other interrupt refuses.
func localInterruptRefuses(err error, v *advpool.Verdict) bool {
	if !operatorInterrupted(err) {
		return false
	}
	return err != nil || v == nil || v.RecordID == 0
}

// interruptGuardSigner refuses to sign a verdict once the operator has
// interrupted the run. The driver signs at convergence, inside a tick, so a
// signal landing just before it would otherwise produce a signed record of a
// run the operator stopped.
type interruptGuardSigner struct{ inner advpool.Signer }

func (s interruptGuardSigner) SignVerdict(ctx context.Context, v advpool.Verdict) (int64, string, error) {
	if operatorInterrupted(nil) {
		return 0, "", errOperatorInterrupt
	}
	return s.inner.SignVerdict(ctx, v)
}

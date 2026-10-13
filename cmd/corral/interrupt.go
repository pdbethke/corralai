// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"fmt"
	"io"

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

// refuseInterrupted prints the one message an interrupted verb ends with and
// returns its exit status (1: the run did not complete).
func refuseInterrupted(stderr io.Writer, cmdName string) int {
	fmt.Fprintf(stderr, "%s: interrupted — nothing recorded\n", cmdName)
	return 1
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

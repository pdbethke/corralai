// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"errors"
	"fmt"

	"github.com/pdbethke/corralai/internal/adequacy"
)

// ErrNoUsableMutants is returned when a run produced no mutants to grade the
// dev suite against. It is a sentinel TYPE, mirroring adequacy.ErrSnapToolchain,
// so a caller can recognize the case and stop instead of retrying it.
//
// WHY IT MUST BE RECOGNIZABLE. A dropped generator region is permanent within
// a run — droppedKeys exists specifically so a region is recorded once and not
// re-probed — so once every region is dropped, the seat is never invoked again
// and every subsequent tick re-derives the identical failure from the identical
// dropped state. Retrying is not resilience; it is printing the same sentence
// twenty times while the operator waits.
//
// Observed before this existed: the generator ran 3 times, then the local drive
// loop retried 21 more times without ever calling it again. driver.go already
// documented the condition as fatal ("Zero mutants to grade against is fatal
// regardless of why") — the intent was right and only the classification was
// missing, so the generic transient-retry path swallowed it.
type ErrNoUsableMutants struct {
	Regions int // mutant-generator regions the run had
	Dropped int // of those, how many were abandoned after exhausting retries
}

func (e ErrNoUsableMutants) Error() string {
	return fmt.Sprintf("advpool: no usable mutants from any of %d mutant-generator region(s) (%d dropped) — nothing to grade the dev suite against; a dropped region is not re-probed within a run, so retrying cannot help",
		e.Regions, e.Dropped)
}

// IsTerminalRunErr reports whether a Tick error can never succeed on a retry,
// so a caller's transient-retry loop must stop instead of spending its budget
// re-deriving the same failure.
//
// This is the SINGLE classification, called by both tick loops (cmd/corral's
// local drive loop and internal/brain's daemon loop). They previously made the
// judgment separately, which is how one of them came to recognize a terminal
// toolchain failure while neither recognized a terminal generator failure.
//
// Terminal today:
//   - adequacy.ErrSnapToolchain — the toolchain resolves to a snap, which a
//     network-isolated jail structurally cannot exec. Mounting more paths
//     cannot fix it, and neither can waiting.
//   - ErrNoUsableMutants — every generator region was dropped, and a dropped
//     region is not re-probed within a run.
//
// Anything else is treated as transient, which is the safe default: a wrongly
// transient error costs retries, a wrongly terminal one costs a run.
func IsTerminalRunErr(err error) bool {
	if err == nil {
		return false
	}
	var snap adequacy.ErrSnapToolchain
	if errors.As(err, &snap) {
		return true
	}
	var noMutants ErrNoUsableMutants
	return errors.As(err, &noMutants)
}

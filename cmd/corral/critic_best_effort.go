// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"fmt"
	"io"

	"github.com/pdbethke/corralai/internal/criticscore"
	"github.com/pdbethke/corralai/internal/statedir"
)

// openCriticStoreBestEffort opens the local critic-accuracy store for a
// surface that joins it as one optional column (`corral scorecard`'s C-PREC,
// `corral models rank`'s critic seat). A store that cannot be opened — held
// by the running brain, say — leaves the column empty and the surface prints
// anyway: refusing the whole table over one column would trade the table for
// it. But a path statedir REFUSED (the store exists only at the legacy
// location) is a different thing: the operator's history is there and was
// not read, so it is said, once, on stderr, rather than rendering as a
// column nobody ever scored. The returned func closes the store; it is safe
// to call when the store is nil.
func openCriticStoreBestEffort(verb string, stderr io.Writer) (*criticscore.Store, func()) {
	p, err := statedir.PathForWrite(statedir.CriticScoreDB)
	if err != nil {
		fmt.Fprintf(stderr, "%s: the critic store was not read, so the critic column is empty: %v\n", verb, err)
		return nil, func() {}
	}
	cs, err := criticscore.Open(p)
	if err != nil {
		return nil, func() {}
	}
	return cs, func() { _ = cs.Close() }
}

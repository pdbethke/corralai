// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/scanstore"
)

// unmeasured is what a phase that did not run prints. NEVER "0s": the two
// claims are different, and the difference is the whole reason the ledger's
// timing columns are nullable. A pool of one copied nothing; a run with
// `--critic-model off` had no critic. Reporting either as zero seconds tells
// a reader those phases are free, which is how a cost model learns something
// nobody measured.
const unmeasured = "—"

// timingLine renders one file's clock: every phase named, in the order they
// happen, with the dev pass carrying its own per-mutant spread because that
// is where an audit's minutes actually go.
//
// ONE function, used by the report `certify --repo` prints the moment a file
// finishes AND by `corral scans show --timing` reading the same file back out
// of the ledger months later. Two renderings of the same numbers would drift,
// and the whole point of the ledger is that a stored scan and the run that
// produced it are comparable.
//
// n/med/max are the graded-mutant count and the spread of how long grading
// one took; all three zero means nothing timed a mutant and the dev pass
// prints bare.
func timingLine(t advpool.Timing, n int, med, max time.Duration) string {
	dev := durationText(t.DevPass)
	if n > 0 && (med > 0 || max > 0) {
		dev = fmt.Sprintf("%s (%d mutants, median %s, max %s)", dev, n, durationText(med), durationText(max))
	}
	parts := []string{
		"selection " + durationText(t.Selection),
		"generation " + durationText(t.Generation),
		"pool " + durationText(t.Pool),
		"dev pass " + dev,
		"authored " + durationText(t.AuthoredPass),
		"critic " + durationText(t.Critic),
		"total " + durationText(t.Total),
	}
	return "   time: " + strings.Join(parts, " · ")
}

// durationText renders a measured duration to the second, zero-padding the
// smaller units so a column of them lines up and so "35m04s" cannot be
// misread as "35m4…" mid-scan. A zero duration is not a duration at all —
// see unmeasured.
//
// Not time.Duration.String(): that renders 35m4s, which reads as a different
// magnitude at a glance next to 35m40s, and it carries sub-second precision
// no phase of an audit is measured to.
//
// A positive duration NEVER renders as "0s". Rounding a phase that really ran
// down to zero seconds is the same false claim as storing 0 for a phase that
// did not run, from the other side — and "—" would be a second one, because
// this phase demonstrably happened. Sub-second is reported as "<1s": the only
// honest reading of a phase too fast for the unit this line is written in.
func durationText(d time.Duration) string {
	if d <= 0 {
		return unmeasured
	}
	if d < time.Second {
		return "<1s"
	}
	d = d.Round(time.Second)
	h := int(d / time.Hour)
	m := int(d/time.Minute) % 60
	s := int(d/time.Second) % 60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh%02dm%02ds", h, m, s)
	case m > 0:
		return fmt.Sprintf("%dm%02ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

// timingOf lifts a ledger row's nullable millisecond columns back into the
// advpool.Timing the printer takes, so `scans show --timing` renders through
// the SAME helper the live report does. A NULL column is a phase that was
// never measured and comes back as a zero duration, which is exactly what
// timingLine dashes.
func timingOf(f scanstore.File) (advpool.Timing, time.Duration, time.Duration) {
	ms := func(p *int64) time.Duration {
		if p == nil {
			return 0
		}
		return time.Duration(*p) * time.Millisecond
	}
	return advpool.Timing{
			Selection:    ms(f.SelectionMillis),
			Generation:   ms(f.GenerationMillis),
			Pool:         ms(f.PoolMillis),
			DevPass:      ms(f.DevPassMillis),
			AuthoredPass: ms(f.AuthoredPassMillis),
			Critic:       ms(f.CriticMillis),
			Total:        ms(f.TotalMillis),
		},
		ms(f.MutantMillisMedian), ms(f.MutantMillisMax)
}

// millisOrNil converts a measured duration to the ledger's nullable
// millisecond column. Zero — a phase that did not run — becomes NULL, never
// 0: a stored zero is a positive claim that the phase was free, and averaged
// across a corpus it says the pool costs nothing.
func millisOrNil(d time.Duration) *int64 {
	if d <= 0 {
		return nil
	}
	v := d.Milliseconds()
	if v == 0 {
		// Measured, but under a millisecond. Round UP rather than to NULL:
		// the phase demonstrably ran, and "unknown" would be the one wrong
		// answer.
		v = 1
	}
	return &v
}

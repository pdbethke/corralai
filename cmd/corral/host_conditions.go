// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"time"

	"github.com/pdbethke/corralai/internal/hoststat"
	"github.com/pdbethke/corralai/internal/scanstore"
)

// hostSampleInterval is how often a run samples the host's load. Ten seconds
// is far below the minute a 1-minute load average moves over, so it sees a
// peak without costing the audit a measurable amount.
const hostSampleInterval = 10 * time.Second

// recordHostConditions copies what the sampler observed onto the scan header,
// the one place both certify verbs do it so neither can forget a field. A
// Conditions that observed nothing AND names no reason (a sampler that never
// ran) is recorded as exactly that, so an absent figure always has a sentence
// beside it.
func recordHostConditions(scan *scanstore.Scan, c hoststat.Conditions) {
	scan.LoadStart, scan.LoadEnd, scan.LoadPeak = c.LoadStart, c.LoadEnd, c.LoadPeak
	scan.MemAvailableStartBytes, scan.PeakRSSBytes, scan.CPUMillis = c.MemAvailableStartBytes, c.PeakRSSBytes, c.CPUMillis
	scan.HostNotMeasured = c.NotMeasured
	if c.LoadStart == nil && c.LoadEnd == nil && c.CPUMillis == nil && c.NotMeasured == "" {
		scan.HostNotMeasured = "host conditions were not sampled for this run"
	}
}

// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"testing"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/hoststat"
	"github.com/pdbethke/corralai/internal/scanstore"
)

func TestHostConditionsReachTheScanAndTheBundle(t *testing.T) {
	f, i := 38.0, int64(7)
	var scan scanstore.Scan
	recordHostConditions(&scan, hoststat.Conditions{LoadStart: &f, LoadEnd: &f, LoadPeak: &f, MemAvailableStartBytes: &i, PeakRSSBytes: &i, CPUMillis: &i})
	b := buildBundle(scan, 0, nil, nil, nil, nil, auditpush.Link{}, false, "r", "c", "", bundleMeta{})
	s := b.Scan
	if s.LoadStart != &f || s.LoadEnd != &f || s.LoadPeak != &f || s.MemAvailableStartBytes != &i || s.PeakRSSBytes != &i || s.CPUMillis != &i {
		t.Errorf("a host figure was dropped crossing buildBundle: %+v", s)
	}
}

func TestAnUnsampledRunSaysSoRatherThanRecordNothing(t *testing.T) {
	var scan scanstore.Scan
	recordHostConditions(&scan, hoststat.Conditions{})
	if scan.HostNotMeasured == "" || scan.LoadStart != nil || scan.CPUMillis != nil {
		t.Errorf("%+v", scan)
	}
	recordHostConditions(&scan, hoststat.Conditions{NotMeasured: "not measured on plan9"})
	if scan.HostNotMeasured != "not measured on plan9" {
		t.Errorf("the sampler's own reason was overwritten: %q", scan.HostNotMeasured)
	}
}

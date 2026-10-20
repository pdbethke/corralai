// SPDX-License-Identifier: Elastic-2.0

package recordview

import (
	"sort"
	"time"
)

// SealRow is one file's newest kill-rate-bearing verdict, as the
// warehouse's corral_seal view holds it. recordview does not compute the
// seal; the caller reads it and hands it over. Caveat is the one-word
// reason the numbers must not be read at face value (BASELINE-FAILED,
// WRITER-FAILED, TEST-UNSOUND), or "".
type SealRow struct {
	Repo, Path   string
	KillRate     float64
	Survivors    int
	ProvenMissed int
	AuditedAt    time.Time
	Caveat       string
}

// WithSeal returns s carrying the seal. readErr != nil is a failed read,
// shown as one; rows == nil with no error is a seal that was read and is
// empty. A site never given a seal (the static build) has SealRead false
// and shows no seal section at all.
func (s Site) WithSeal(rows []SealRow, readErr error) Site {
	s.SealRead = true
	if readErr != nil {
		s.SealErr = readErr.Error()
		return s
	}
	rs := append([]SealRow(nil), rows...)
	sort.SliceStable(rs, func(i, j int) bool {
		// A caveated row's kill rate is not a measurement of the suite
		// (baseline failed, writer failed...), so it must not rank by it:
		// a BASELINE-FAILED 0.00 would otherwise lead as the worst-tested file.
		if ci, cj := rs[i].Caveat != "", rs[j].Caveat != ""; ci != cj {
			return !ci
		}
		if rs[i].ProvenMissed != rs[j].ProvenMissed {
			return rs[i].ProvenMissed > rs[j].ProvenMissed // proven gaps first: the earned findings
		}
		return rs[i].KillRate < rs[j].KillRate
	})
	s.Seal = rs
	return s
}

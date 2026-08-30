// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"encoding/json"
	"time"
)

// verdictAlias strips Verdict of its methods so the marshaller below can
// encode it without calling itself. A plain type conversion, nothing else.
type verdictAlias Verdict

// verdictWire is Verdict on the wire: the whole struct, plus the three
// durations that need a MILLISECOND form of their own (see timingWire) —
// the two mutant-duration summaries and the baseline's own wall clock.
//
// All three are `json:"-"` on Verdict itself, so the embedded alias
// contributes nothing for them and there is exactly one rendering of each —
// no Go-named nanosecond twin beside the millisecond key.
//
// omitempty on all three: zero means nothing timed that phase, and an absent
// key is how the rest of this record spells "not measured". A stored 0 would
// be read as "measured: instant".
type verdictWire struct {
	verdictAlias
	MutantDurationMedianMillis int64 `json:"mutant_duration_median_ms,omitempty"`
	MutantDurationMaxMillis    int64 `json:"mutant_duration_max_ms,omitempty"`
	BaselineDurationMillis     int64 `json:"baseline_duration_ms,omitempty"`
}

// MarshalJSON writes the verdict the ledger stores in verdict_json, the
// warehouse receives, and the signed statement hashes.
//
// It exists for the three fields above and nothing else. Every other field
// rides through the alias untouched — so a field added to Verdict is on the
// wire the moment it is added, and only a field needing a wire form of its
// own has to be named here.
func (v Verdict) MarshalJSON() ([]byte, error) {
	return json.Marshal(verdictWire{
		verdictAlias:               verdictAlias(v),
		MutantDurationMedianMillis: v.MutantDurationMedian.Milliseconds(),
		MutantDurationMaxMillis:    v.MutantDurationMax.Milliseconds(),
		BaselineDurationMillis:     v.BaselineDuration.Milliseconds(),
	})
}

// UnmarshalJSON reads it back, so a verdict served from the ledger's cache
// still knows the shape of the dev pass that earned it.
//
// baseline_duration_ms is read tolerantly: a verdict_json row written before
// this key existed simply has no baseline_duration_ms member, json.Unmarshal
// leaves BaselineDurationMillis at its zero value, and BaselineDuration comes
// back zero — which is the honest answer for a cached verdict from an older
// build: it never carried this measurement, not that the phase took no time.
func (v *Verdict) UnmarshalJSON(b []byte) error {
	var w verdictWire
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*v = Verdict(w.verdictAlias)
	v.MutantDurationMedian = time.Duration(w.MutantDurationMedianMillis) * time.Millisecond
	v.MutantDurationMax = time.Duration(w.MutantDurationMaxMillis) * time.Millisecond
	v.BaselineDuration = time.Duration(w.BaselineDurationMillis) * time.Millisecond
	return nil
}

// mutantRefAlias strips MutantRef of its methods so the marshaller below can
// encode it without calling itself.
type mutantRefAlias MutantRef

// mutantRefWire is MutantRef on the wire: the whole struct, plus Duration in
// the same millisecond form every other duration on this record uses.
// Duration is `json:"-"` on MutantRef itself, so the alias contributes
// nothing for it. omitempty: zero means the run was not timed per mutant
// (see MutantRef.Duration), and an absent key says that honestly.
type mutantRefWire struct {
	mutantRefAlias
	DurationMillis int64 `json:"duration_ms,omitempty"`
}

// MarshalJSON writes a MutantRef the way it appears inside
// Verdict.DevKilledMutants / DevSurvivedMutants — reached through Verdict's
// own MarshalJSON, so this fires for every mutant reference the ledger,
// warehouse and signed statement ever see.
func (m MutantRef) MarshalJSON() ([]byte, error) {
	return json.Marshal(mutantRefWire{
		mutantRefAlias: mutantRefAlias(m),
		DurationMillis: m.Duration.Milliseconds(),
	})
}

// UnmarshalJSON reads it back tolerantly: a mutant reference cached before
// duration_ms existed simply has no such key, and Duration comes back zero —
// honest for a measurement that build never took, not a claim of "instant".
func (m *MutantRef) UnmarshalJSON(b []byte) error {
	var w mutantRefWire
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*m = MutantRef(w.mutantRefAlias)
	m.Duration = time.Duration(w.DurationMillis) * time.Millisecond
	return nil
}

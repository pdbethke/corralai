// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"encoding/json"
	"time"
)

// verdictAlias strips Verdict of its methods so the marshaller below can
// encode it without calling itself. A plain type conversion, nothing else.
type verdictAlias Verdict

// verdictWire is Verdict on the wire: the whole struct, plus the two
// mutant-duration summaries in the MILLISECOND form every other duration in
// this record already uses (see timingWire).
//
// Both fields are `json:"-"` on Verdict itself, so the embedded alias
// contributes nothing for them and there is exactly one rendering of each —
// no Go-named nanosecond twin beside the millisecond key.
//
// omitempty on both: zero means nothing timed a mutant, and an absent key is
// how the rest of this record spells "not measured". A stored 0 would be read
// as "measured: instant".
type verdictWire struct {
	verdictAlias
	MutantDurationMedianMillis int64 `json:"mutant_duration_median_ms,omitempty"`
	MutantDurationMaxMillis    int64 `json:"mutant_duration_max_ms,omitempty"`
}

// MarshalJSON writes the verdict the ledger stores in verdict_json, the
// warehouse receives, and the signed statement hashes.
//
// It exists for the two mutant-duration fields and nothing else. Every other
// field rides through the alias untouched — so a field added to Verdict is on
// the wire the moment it is added, and only a field needing a wire form of
// its own has to be named here.
func (v Verdict) MarshalJSON() ([]byte, error) {
	return json.Marshal(verdictWire{
		verdictAlias:               verdictAlias(v),
		MutantDurationMedianMillis: v.MutantDurationMedian.Milliseconds(),
		MutantDurationMaxMillis:    v.MutantDurationMax.Milliseconds(),
	})
}

// UnmarshalJSON reads it back, so a verdict served from the ledger's cache
// still knows the shape of the dev pass that earned it.
func (v *Verdict) UnmarshalJSON(b []byte) error {
	var w verdictWire
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*v = Verdict(w.verdictAlias)
	v.MutantDurationMedian = time.Duration(w.MutantDurationMedianMillis) * time.Millisecond
	v.MutantDurationMax = time.Duration(w.MutantDurationMaxMillis) * time.Millisecond
	return nil
}

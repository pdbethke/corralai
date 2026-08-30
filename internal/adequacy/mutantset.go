// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pdbethke/corralai/internal/lang"
)

// MutantSetFormat is the only document version this package writes or reads.
// It is checked on BOTH sides — an unknown format is refused at write time as
// well as at read time — because the whole value of a recorded set is that
// the run replaying it is grading against exactly what the run that recorded
// it graded against. A document whose shape corral cannot vouch for must
// never enter that exchange in either direction.
const MutantSetFormat = "corral-mutants-1"

// MutantSetFile is the corral-mutants-1 document: one recorded mutant set
// per audited file, tied to the exact source it was derived from.
//
// It exists because every corral run generates its mutants with a MODEL, and
// generator variance on one unchanged file is roughly ±0.15 kill rate — so
// two runs of the same audit are not comparable measurements, they are two
// different exams. A recorded set turns the exam into a FIXED input: the same
// mutants, in the same order, against whatever else is being varied.
type MutantSetFile struct {
	Format string                    `json:"format"` // "corral-mutants-1"
	Files  map[string]MutantSetEntry `json:"files"`  // repo-relative path
}

// MutantSetEntry is one file's recorded mutants plus the fingerprint of the
// source they are edits OF. ParentSHA256 is what makes the document safe to
// replay: a mutant is a single-point edit of specific bytes, and re-applying
// it to different bytes produces something nobody generated and nobody
// reviewed — a fabricated exam wearing a recorded set's name.
type MutantSetEntry struct {
	ParentSHA256 string           `json:"parent_sha256"`
	Mutants      []RecordedMutant `json:"mutants"`
}

// RecordedMutant is a Mutant as it survives to disk. ParentSHA256 is NOT
// repeated per mutant — it belongs to the entry, since every mutant in an
// entry is by construction derived from the same source — and is restored
// onto each Mutant by MutantsFor.
type RecordedMutant struct {
	ID   string         `json:"id"`
	Code string         `json:"code"`
	Span lang.LineRange `json:"span"`
}

// WriteMutantSet writes set to path as pretty-printed JSON, refusing any
// format this package does not itself produce.
func WriteMutantSet(path string, set MutantSetFile) error {
	if set.Format != MutantSetFormat {
		return fmt.Errorf("adequacy: refusing to write a mutant set with format %q — this build writes %q only", set.Format, MutantSetFormat)
	}
	if set.Files == nil {
		set.Files = map[string]MutantSetEntry{}
	}
	raw, err := json.MarshalIndent(set, "", "  ")
	if err != nil {
		return fmt.Errorf("adequacy: encoding mutant set: %w", err)
	}
	raw = append(raw, '\n')
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("adequacy: creating %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("adequacy: writing mutant set %s: %w", path, err)
	}
	return nil
}

// ReadMutantSet reads a corral-mutants-1 document from path. A document of
// any other format is refused rather than best-effort decoded: a shape corral
// does not know is a shape it cannot honestly claim to be replaying.
func ReadMutantSet(path string) (MutantSetFile, error) {
	var set MutantSetFile
	raw, err := os.ReadFile(path) // #nosec G304 -- operator-supplied path, same contract as --goals
	if err != nil {
		return set, fmt.Errorf("adequacy: reading mutant set %s: %w", path, err)
	}
	if err := json.Unmarshal(raw, &set); err != nil {
		return set, fmt.Errorf("adequacy: parsing mutant set %s: %w", path, err)
	}
	if set.Format != MutantSetFormat {
		return MutantSetFile{}, fmt.Errorf("adequacy: mutant set %s has format %q, want %q", path, set.Format, MutantSetFormat)
	}
	if set.Files == nil {
		set.Files = map[string]MutantSetEntry{}
	}
	return set, nil
}

// MutantsFor returns the recorded mutants for codePath, refusing when the
// file's current bytes are not the ones the set was derived from.
//
// Both refusals are hard, and neither degrades into "generate a fresh set for
// this one file instead": a run that is half-replayed and half-freshly-
// generated is exactly the incomparable measurement a recorded set exists to
// eliminate, and it would be indistinguishable afterwards from a fully
// replayed one.
func (s MutantSetFile) MutantsFor(codePath, currentSource string) ([]Mutant, error) {
	entry, ok := s.Files[codePath]
	if !ok {
		return nil, fmt.Errorf("adequacy: the mutant set records no mutants for %s — a replayed scan cannot generate one file's exam and replay another's", codePath)
	}
	sum := sha256.Sum256([]byte(currentSource))
	have := hex.EncodeToString(sum[:])
	if have != entry.ParentSHA256 {
		return nil, fmt.Errorf("adequacy: %s has changed since the mutant set was recorded (sha256 %s on disk, %s in the set) — its mutants are edits of source that no longer exists", codePath, have, entry.ParentSHA256)
	}
	out := make([]Mutant, 0, len(entry.Mutants))
	for _, rm := range entry.Mutants {
		out = append(out, Mutant{
			ID: rm.ID, Code: rm.Code, Span: rm.Span,
			// Restored from the entry, never trusted from the record: every
			// mutant in an entry is by construction an edit of that entry's
			// parent, and the hash was just proven to match the file on disk.
			ParentSHA256: entry.ParentSHA256,
		})
	}
	return out, nil
}

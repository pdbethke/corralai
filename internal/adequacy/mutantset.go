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

// MutantSetFormat is the only document version this package WRITES. Both
// sides are still checked — an unknown format is refused at write time as
// well as at read time — because the whole value of a recorded set is that
// the run replaying it is grading against exactly what the run that recorded
// it graded against. A document whose shape corral cannot vouch for must
// never enter that exchange in either direction.
//
// v2 exists because a mutant is now its HUNK: v1 stored the whole mutated
// file per mutant, which is the same waste the representation change removed
// everywhere else.
const MutantSetFormat = "corral-mutants-2"

// MutantSetFormatV1 is the previous document version. It is READ and never
// written: every set already recorded must keep replaying, and it does —
// each v1 entry becomes a WHOLE-FILE mutant (Search empty, Replace the file
// v1 stored), so the bytes the jail grades are byte-for-byte the ones that
// run graded. Nothing measured moves.
const MutantSetFormatV1 = "corral-mutants-1"

// MutantSetFile is the corral-mutants-2 document: one recorded mutant set
// per audited file, tied to the exact source it was derived from.
//
// It exists because every corral run generates its mutants with a MODEL, and
// generator variance on one unchanged file is roughly ±0.15 kill rate — so
// two runs of the same audit are not comparable measurements, they are two
// different exams. A recorded set turns the exam into a FIXED input: the same
// mutants, in the same order, against whatever else is being varied.
type MutantSetFile struct {
	Format string                    `json:"format"` // "corral-mutants-2"
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

// RecordedMutant is a Mutant as it survives to disk: its HUNK, not a copy of
// the file it edits. ParentSHA256 is NOT repeated per mutant — it belongs to
// the entry, since every mutant in an entry is by construction derived from
// the same source — and is restored onto each Mutant by MutantsFor.
//
// The parent hash is what makes storing only the hunk safe: the source the
// hunk re-applies to is proven to be the source it was cut from before any
// mutant is handed back.
type RecordedMutant struct {
	ID      string         `json:"id"`
	Span    lang.LineRange `json:"span"`
	Search  string         `json:"search"`
	Replace string         `json:"replace"`
}

// recordedMutantV1 is one entry of a corral-mutants-1 document. Its "code"
// field is the WHOLE mutated file — hence the name: it is not a hunk, and
// nothing in this build produces one. It is decoded into its own type rather
// than into RecordedMutant so that no live code path can read a mutant's file
// by accident.
type recordedMutantV1 struct {
	ID   string         `json:"id"`
	File string         `json:"code"`
	Span lang.LineRange `json:"span"`
}

type mutantSetEntryV1 struct {
	ParentSHA256 string             `json:"parent_sha256"`
	Mutants      []recordedMutantV1 `json:"mutants"`
}

type mutantSetFileV1 struct {
	Format string                      `json:"format"`
	Files  map[string]mutantSetEntryV1 `json:"files"`
}

// upgrade turns a v1 document into the in-memory shape the rest of corral
// speaks, one whole-file mutant per recorded entry.
func (v mutantSetFileV1) upgrade() MutantSetFile {
	out := MutantSetFile{Format: MutantSetFormatV1, Files: map[string]MutantSetEntry{}}
	for path, e := range v.Files {
		entry := MutantSetEntry{ParentSHA256: e.ParentSHA256}
		for _, rm := range e.Mutants {
			// Search stays EMPTY on purpose. v1 never recorded the anchor, and
			// inventing one here — by diffing the stored file against the
			// parent — would replay a hunk nobody generated. The whole-file
			// shape replays exactly what was graded, which is the only claim
			// this document can honestly make.
			entry.Mutants = append(entry.Mutants, RecordedMutant{ID: rm.ID, Span: rm.Span, Replace: rm.File})
		}
		out.Files[path] = entry
	}
	return out
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

// ReadMutantSet reads a corral-mutants-2 document from path, or a
// corral-mutants-1 one, which is upgraded in memory to whole-file mutants. A
// document of any other format is refused rather than best-effort decoded: a
// shape corral does not know is a shape it cannot honestly claim to be
// replaying.
//
// The returned MutantSetFile keeps the format it was READ as, so a caller can
// still say which document it replayed.
func ReadMutantSet(path string) (MutantSetFile, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- operator-supplied path, same contract as --goals
	if err != nil {
		return MutantSetFile{}, fmt.Errorf("adequacy: reading mutant set %s: %w", path, err)
	}
	var probe struct {
		Format string `json:"format"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return MutantSetFile{}, fmt.Errorf("adequacy: parsing mutant set %s: %w", path, err)
	}
	switch probe.Format {
	case MutantSetFormat:
		var set MutantSetFile
		if err := json.Unmarshal(raw, &set); err != nil {
			return MutantSetFile{}, fmt.Errorf("adequacy: parsing mutant set %s: %w", path, err)
		}
		if set.Files == nil {
			set.Files = map[string]MutantSetEntry{}
		}
		return set, nil
	case MutantSetFormatV1:
		var v1 mutantSetFileV1
		if err := json.Unmarshal(raw, &v1); err != nil {
			return MutantSetFile{}, fmt.Errorf("adequacy: parsing mutant set %s: %w", path, err)
		}
		return v1.upgrade(), nil
	default:
		return MutantSetFile{}, fmt.Errorf("adequacy: mutant set %s has format %q, want %q (or the read-only %q)", path, probe.Format, MutantSetFormat, MutantSetFormatV1)
	}
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
			ID: rm.ID, Span: rm.Span, Search: rm.Search, Replace: rm.Replace,
			// Restored from the entry, never trusted from the record: every
			// mutant in an entry is by construction an edit of that entry's
			// parent, and the hash was just proven to match the file on disk.
			ParentSHA256: entry.ParentSHA256,
		})
	}
	return out, nil
}

// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/pdbethke/corralai/internal/adequacy"
)

// mutantSetRecorder accumulates `--record-mutants`: one entry per audited
// file, fed from the driver's MutantSink as each file's dev pass scores.
//
// Mutex-guarded because a repo scan audits files CONCURRENTLY — the sink is
// called from whichever worker goroutine finished a file's dev pass, and a
// bare map would race. `certify --local` audits one file and never contends;
// paying for a mutex there costs nothing and means there is one recorder,
// not two implementations that could disagree about the document they write.
type mutantSetRecorder struct {
	mu    sync.Mutex
	files map[string]adequacy.MutantSetEntry
	// skipped names files whose mutants carried no parent hash, so no
	// replayable entry could be written for them. Reported, never silent: a
	// set that quietly omits a file looks identical on disk to a set recorded
	// from a scan that never audited it.
	skipped []string
}

func newMutantSetRecorder() *mutantSetRecorder {
	return &mutantSetRecorder{files: map[string]adequacy.MutantSetEntry{}}
}

// sink is the advpool.Driver.MutantSink for every run this command drives.
func (r *mutantSetRecorder) sink(codePath string, ms []adequacy.Mutant) {
	if len(ms) == 0 {
		return
	}
	// The parent hash comes from the MUTANTS, not from re-reading the file:
	// a mutant carries the sha256 of the exact bytes it is a single-point
	// edit of (see adequacy.Mutant.ParentSHA256), and that is the only
	// fingerprint a later replay can safely check itself against. Re-hashing
	// the file here would instead record "what was on disk when the scan
	// finished", which is the same value only when nothing moved underneath
	// the run — precisely the assumption the parent hash exists to stop
	// anyone making.
	parent := ms[0].ParentSHA256
	recorded := make([]adequacy.RecordedMutant, 0, len(ms))
	for _, m := range ms {
		if m.ParentSHA256 != parent {
			// Mutants of one file that disagree about their own parent cannot
			// all be replayed against one source. Refuse the whole entry
			// rather than write a set that is right about some of it.
			parent = ""
			break
		}
		recorded = append(recorded, adequacy.RecordedMutant{ID: m.ID, Code: m.Code, Span: m.Span})
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if parent == "" {
		r.skipped = append(r.skipped, codePath)
		return
	}
	r.files[codePath] = adequacy.MutantSetEntry{ParentSHA256: parent, Mutants: recorded}
}

// write flushes the accumulated set to path and returns how many files it
// holds. A recorder with nothing in it still writes: an empty
// corral-mutants-1 document is an honest answer ("this scan graded nothing"),
// and a missing file would be indistinguishable from a crashed run.
func (r *mutantSetRecorder) write(path string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := adequacy.WriteMutantSet(path, adequacy.MutantSetFile{
		Format: adequacy.MutantSetFormat, Files: r.files,
	}); err != nil {
		return 0, err
	}
	return len(r.files), nil
}

// report writes the recorder's own disclosure: how many files landed, and
// every file that could not be recorded replayably.
func (r *mutantSetRecorder) report(w io.Writer, path string, n int) {
	fmt.Fprintf(w, "  wrote %d file(s) of graded mutants to %s — replay them with `--mutants %s`\n", n, path, path)
	r.mu.Lock()
	skipped := append([]string(nil), r.skipped...)
	r.mu.Unlock()
	if len(skipped) > 0 {
		sort.Strings(skipped)
		fmt.Fprintf(w, "  %d file(s) NOT recorded — their mutants carry no parent hash, so nothing could verify a replay: %v\n", len(skipped), skipped)
	}
}

// presetMutantsForSelection resolves a `--mutants` set against the files a
// scan is about to audit, returning path -> the mutants to replay.
//
// It REFUSES, naming the first offending file, when any of them is absent
// from the set or has changed since it was recorded. That is deliberately not
// a per-file fallback to generation: a run that replays some files and
// generates others is exactly the incomparable measurement a recorded set
// exists to remove, and afterwards nothing distinguishes it from a fully
// replayed one. Refusing costs the operator a re-record; not refusing costs
// them a number they cannot trust.
//
// Reads go through root (an *os.Root over the repository), so a candidate
// that is a symlink out of the checkout cannot be followed — the same
// confinement every other read in a scan is held to.
func presetMutantsForSelection(root *os.Root, set adequacy.MutantSetFile, paths []string) (map[string][]adequacy.Mutant, error) {
	out := make(map[string][]adequacy.Mutant, len(paths))
	for _, p := range paths {
		src, err := readRootFile(root, p)
		if err != nil {
			return nil, fmt.Errorf("--mutants: reading %s to check it against the recorded set: %w", p, err)
		}
		ms, merr := set.MutantsFor(p, src)
		if merr != nil {
			return nil, fmt.Errorf("--mutants: %w", merr)
		}
		out[p] = ms
	}
	return out, nil
}

func readRootFile(root *os.Root, p string) (string, error) {
	f, err := root.Open(p)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// sha256Hex is the fingerprint recorded as scan_files.mutants_from: WHICH set
// a replayed scan replayed. A row that says nothing about its exam cannot be
// compared to another row that had a different one.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func fileSHA256(path string) (string, error) {
	b, err := os.ReadFile(path) // #nosec G304 -- operator-supplied path, same contract as --goals
	if err != nil {
		return "", err
	}
	return sha256Hex(b), nil
}

// localMutantSourcePath resolves the file `certify --local` is about to
// audit, for the byte check --mutants performs before the run. In --repo-dir
// mode --code is repo-relative (the same key a scan records); without it,
// --code is already a filesystem path.
func localMutantSourcePath(repoDir, codePath string) string {
	if repoDir == "" {
		return codePath
	}
	return filepath.Join(repoDir, codePath)
}

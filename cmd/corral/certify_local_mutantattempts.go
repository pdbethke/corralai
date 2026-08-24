// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/mutantattempts"
)

// localMutantAttemptsDBPath mirrors localBugCatchDBPath for the writer-seat
// correlation store, so a --local run's per-seat, per-mutant outcomes persist
// next to the signed build ledger and the scorecard.
func localMutantAttemptsDBPath() string {
	if p := strings.TrimSpace(os.Getenv("CORRALAI_MUTANT_ATTEMPTS_DB")); p != "" {
		return p
	}
	home := ""
	if u, err := os.UserHomeDir(); err == nil {
		home = u
	} else if usr, err := user.Current(); err == nil {
		home = usr.HomeDir
	}
	return filepath.Join(home, ".claude", "corralai_mutant_attempts.duckdb")
}

// localMutantAttemptSink persists a converged --local run's per-seat mutant
// outcomes, stamping the run context (ts, repo, commit) the pure driver does
// not carry. It is the ADAPTER the whole feature was missing: advpool defines
// MutantAttemptSink and never imports a store, so without a composition root
// attaching one, d.MutantAttempts stayed nil and a measured challenger wrote
// zero rows — the feature was inert end to end.
//
// `certify --local` is the ONLY writer of RunSpec.ShadowWriterModel (the repo
// scan exposes no such flag and the daemon never sets it), so this is the one
// place the adapter can possibly matter.
//
// Recording is best-effort by design: this is measurement, never the gate, so
// a failed write warns and the audit stands.
type localMutantAttemptSink struct {
	store        *mutantattempts.Store
	missionID    int64
	repo, commit string
	warn         io.Writer
	// rows counts attempts actually WRITTEN (never merely computed), so a
	// caller can print an honest past-tense claim — the same discipline
	// wireLocalBugCatch's shadowRows counter exists for.
	rows *int64
}

// wireLocalMutantAttempts opens the correlation store at path and points the
// driver's MutantAttempts feed at it, returning a closer that is always safe
// to call plus a live counter of attempts actually persisted (nil if the store
// never opened).
//
// A store that will not open is a WARNING, never a failure: the verdict does
// not depend on this, and refusing to audit over an unwritable measurement
// file would trade the product for its telemetry.
func wireLocalMutantAttempts(d *advpool.Driver, path, repo, commit string, warn io.Writer) (closer func(), opened bool, rows *int64) {
	st, err := mutantattempts.Open(path)
	if err != nil {
		if warn != nil {
			fmt.Fprintf(warn, "corral certify --local: opening the writer-correlation store (measurement only — the audit continues): %v\n", err)
		}
		return func() {}, false, nil
	}
	var n int64
	d.MutantAttempts = localMutantAttemptSink{
		store: st, missionID: localMissionID, repo: repo, commit: commit, warn: warn, rows: &n,
	}
	return func() { _ = st.Close() }, true, &n
}

func (s localMutantAttemptSink) Record(recordID int64, recordHead string, attempts []advpool.MutantAttempt) {
	if s.store == nil || len(attempts) == 0 {
		return
	}
	now := time.Now()
	rows := make([]mutantattempts.Attempt, 0, len(attempts))
	for _, a := range attempts {
		rows = append(rows, mutantattempts.Attempt{
			TS: now, RecordID: recordID, RecordHead: recordHead,
			MissionID: s.missionID, Repo: s.repo, Commit: s.commit,
			Path: a.Path, MutantID: a.MutantID, Model: a.Model, Role: a.Role,
			Shadow: a.Shadow, Outcome: a.Outcome,
		})
	}
	if err := s.store.Record(context.Background(), rows); err != nil {
		if s.warn != nil {
			fmt.Fprintf(s.warn, "corral certify --local: recording writer-correlation rows failed (the verdict stands): %v\n", err)
		}
		return
	}
	if s.rows != nil {
		atomic.AddInt64(s.rows, int64(len(rows)))
	}
}

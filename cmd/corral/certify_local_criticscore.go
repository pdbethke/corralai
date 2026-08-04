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
	"github.com/pdbethke/corralai/internal/criticscore"
)

// localCriticScoreDBPath mirrors localBugCatchDBPath for the critic-accuracy
// store, so a --local run's critic findings persist next to the signed build
// ledger and feed the same `corral criticscore` / `corral scorecard` surfaces
// the daemon's runs feed.
func localCriticScoreDBPath() string {
	if p := strings.TrimSpace(os.Getenv("CORRALAI_CRITICSCORE_DB")); p != "" {
		return p
	}
	home := ""
	if u, err := os.UserHomeDir(); err == nil {
		home = u
	} else if usr, err := user.Current(); err == nil {
		home = usr.HomeDir
	}
	return filepath.Join(home, ".claude", "corralai_criticscore.duckdb")
}

// localCriticSink persists a converged --local run's execution-checked critic
// findings into the criticscore store, stamping the run context (ts, repo,
// commit) the pure driver does not carry. The daemon-side analogue is
// internal/brain.advpoolCriticSink.
//
// This exists for the same reason localBugCatchSink does, and it is the same
// defect one layer over: CriticFindings was wired ONLY in the brain, while
// `certify --local` is the command the quickstart, the README and every
// external user actually runs. So on the only path most people will ever use,
// every critic finding was computed, printed once, and thrown away.
//
// What that cost is specific. `corral scorecard` carries a C-PREC column whose
// entire purpose is the test-critic's execution-checked precision, derived from
// human confirm/refute adjudications. With no local writer there is nothing to
// adjudicate, so C-PREC read "—" forever for every user without a brain — a
// column measuring the one signal the product could not collect.
//
// Recording is best-effort by design: findings are advisory and never gate a
// verdict, so a failed write warns and the audit stands.
type localCriticSink struct {
	store        *criticscore.Store
	missionID    int64
	repo, commit string
	warn         io.Writer
	// rows counts findings actually WRITTEN (never merely computed), so a
	// caller can print an honest past-tense claim — the same discipline
	// wireLocalBugCatch's shadowRows counter exists for.
	rows *int64
}

// wireLocalCriticScore opens the critic store at path and points the driver's
// CriticFindings feed at it, returning a closer that is always safe to call
// plus a live counter of findings actually persisted (nil if the store never
// opened).
//
// A store that will not open is a WARNING, never a failure: the verdict does
// not depend on this, and refusing to audit over an unwritable metrics file
// would trade the product for its telemetry.
func wireLocalCriticScore(d *advpool.Driver, path, repo, commit string, warn io.Writer) (closer func(), opened bool, rows *int64) {
	cs, err := criticscore.Open(path)
	if err != nil {
		if warn != nil {
			fmt.Fprintf(warn, "corral certify --local: opening critic store (metrics only — the audit continues): %v\n", err)
		}
		return func() {}, false, nil
	}
	var n int64
	d.CriticFindings = localCriticSink{
		store: cs, missionID: localMissionID, repo: repo, commit: commit, warn: warn, rows: &n,
	}
	return func() { _ = cs.Close() }, true, &n
}

func (s localCriticSink) Record(recordID int64, recordHead string, obs []advpool.CriticFindingObservation) {
	if s.store == nil {
		return
	}
	// Whole-second granularity is schema-driven (the store's ts column is a
	// float64 epoch-second), matching advpoolCriticSink exactly — rows are
	// keyed by finding ID, so a coarser stamp costs nothing.
	ts := float64(time.Now().Unix())
	rows := make([]criticscore.Finding, 0, len(obs))
	for _, o := range obs {
		rows = append(rows, criticscore.Finding{
			// "<recordID>:<queueFindingID>" — the same stable composite the
			// brain writes, so a finding keeps one identity whether it was
			// produced locally or by a daemon.
			ID:         fmt.Sprintf("%d:%d", recordID, o.QueueFindingID),
			TS:         ts,
			RecordID:   recordID,
			RecordHead: recordHead,
			Repo:       s.repo, Commit: s.commit, MissionID: s.missionID,
			Model:        o.Model,
			TargetTest:   o.TargetTest,
			TestFile:     o.TestFile,
			TestSelector: o.TestSelector,
			Scope:        o.Scope,
			Evidence:     o.Evidence,
			Severity:     o.Severity,
			Adjudication: o.Adjudication,
			Source:       o.Source,
		})
	}
	if err := s.store.Record(context.Background(), rows); err != nil {
		if s.warn != nil {
			fmt.Fprintf(s.warn, "corral certify --local: recording critic findings (metrics only — the audit stands): %v\n", err)
		}
		return
	}
	if s.rows != nil {
		atomic.AddInt64(s.rows, int64(len(rows)))
	}
}

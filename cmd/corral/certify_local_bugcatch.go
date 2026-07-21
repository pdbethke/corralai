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
	"github.com/pdbethke/corralai/internal/bugcatch"
)

// localBugCatchDBPath mirrors localBuildDBPath for the bug-catching scorecard
// store, so a --local run's per-seat metrics (including the shadow
// head-to-head) persist next to the signed build ledger and feed the same
// `corral scorecard` surface the daemon's runs feed.
func localBugCatchDBPath() string {
	if p := strings.TrimSpace(os.Getenv("CORRALAI_BUGCATCH_DB")); p != "" {
		return p
	}
	home := ""
	if u, err := os.UserHomeDir(); err == nil {
		home = u
	} else if usr, err := user.Current(); err == nil {
		home = usr.HomeDir
	}
	return filepath.Join(home, ".claude", "corralai_bugcatch.duckdb")
}

// localBugCatchSink persists a converged --local run's per-seat observations
// into the DuckDB scorecard store, stamping the run context (ts, repo, commit,
// source="pool") the pure driver does not carry. The daemon-side analogue is
// internal/brain.advpoolBugCatchSink; this exists because the brain is the
// only place BugCatch was ever wired, while `certify --local` is the ONLY
// writer of RunSpec.ShadowModel — so on the only path where shadow actually
// runs, every shadow row was computed and thrown away.
//
// Recording is best-effort by design: metrics are not the gate, so a failed
// write warns and the audit stands.
type localBugCatchSink struct {
	store        *bugcatch.Store
	missionID    int64
	repo, commit string
	warn         io.Writer
	// shadowRows counts shadow rows actually WRITTEN to the store (never
	// merely computed) — see wireLocalBugCatch's third return value. Read via
	// atomic because Record is called from the driver's tick path; today that
	// is always the single in-process --local goroutine, but this sink has no
	// business assuming that stays true.
	shadowRows *int64
}

// wireLocalBugCatch opens the scorecard store at path and points the driver's
// BugCatch feed at it, returning a closer that is always safe to call, plus a
// live counter of shadow rows actually persisted (nil if the store never
// opened). A store that will not open is a WARNING, never a failure: the
// audit's verdict does not depend on metrics, so refusing to run over an
// unwritable metrics file would trade the whole product for the telemetry.
//
// The counter exists so a caller can print an honest, PAST-TENSE "recorded to
// the scorecard" claim: printing it merely because shadow was ENABLED was
// false whenever the store failed to open, the run hit its deadline (which
// signs a verdict without ever calling BugCatch), or every shadow seat ended
// unmeasured — see runCertifyLocal.
func wireLocalBugCatch(d *advpool.Driver, path, repo, commit string, warn io.Writer) (closer func(), opened bool, shadowRows *int64) {
	bcs, err := bugcatch.Open(path)
	if err != nil {
		if warn != nil {
			fmt.Fprintf(warn, "corral certify --local: opening scorecard store (metrics only — the audit continues): %v\n", err)
		}
		return func() {}, false, nil
	}
	var n int64
	d.BugCatch = localBugCatchSink{
		store: bcs, missionID: localMissionID, repo: repo, commit: commit, warn: warn, shadowRows: &n,
	}
	return func() { _ = bcs.Close() }, true, &n
}

func (s localBugCatchSink) Record(recordID int64, recordHead string, obs []advpool.BugCatchObservation) {
	if s.store == nil {
		return
	}
	now := time.Now()
	rows := make([]bugcatch.Observation, 0, len(obs))
	shadowCount := int64(0)
	for _, o := range obs {
		if o.Shadow {
			shadowCount++
		}
		rows = append(rows, bugcatch.Observation{
			TS: now, RecordID: recordID, RecordHead: recordHead,
			MissionID: s.missionID, Repo: s.repo, Commit: s.commit,
			Model: o.Model, Role: o.Role, Source: "pool",
			Catches: o.Catches, Opportunities: o.Opportunities,
			SoundTests: o.SoundTests, AuthoredTests: o.AuthoredTests,
			CriticFlags: o.CriticFlags, MutantsPlanted: o.MutantsPlanted, MutantsSurvived: o.MutantsSurvived,
			Shard: o.Shard, Region: o.Region, RegionComplexity: o.RegionComplexity, RegionLines: o.RegionLines,
			TestComplexity: o.TestComplexity, ParseRetries: o.ParseRetries, Dropped: o.Dropped, Shadow: o.Shadow,
		})
	}
	if err := s.store.Record(context.Background(), rows); err != nil {
		if s.warn != nil {
			fmt.Fprintf(s.warn, "corral certify --local: recording scorecard metrics failed (the verdict stands): %v\n", err)
		}
		return
	}
	if s.shadowRows != nil && shadowCount > 0 {
		atomic.AddInt64(s.shadowRows, shadowCount)
	}
}

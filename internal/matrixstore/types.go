// SPDX-License-Identifier: Elastic-2.0

package matrixstore

// Row is one test's adequacy result for one converged run: how many mutants
// it killed out of the total planted for that region, plus the
// delete-candidate verdict (kills == 0 and mutants_total > 0 — a test that
// caught nothing the mutants offered). One row per test per converged run,
// append-only — mirrors internal/bugcatch's per-run observation shape, not
// internal/criticscore's mutable single-row-per-key shape.
type Row struct {
	TS         float64
	RecordID   int64
	RecordHead string
	Repo       string
	Commit     string
	MissionID  int64
	Lang       string

	TestSelector string
	TestFile     string

	Kills        int
	MutantsTotal int

	DeleteCandidate bool
}

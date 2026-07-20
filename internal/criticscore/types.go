// SPDX-License-Identifier: Elastic-2.0

package criticscore

// Finding is one critic finding against one target test, keyed by a stable
// ID composed of the pool record it came from and the queue finding within
// that record: fmt.Sprintf("%d:%d", RecordID, QueueFindingID). Adjudication
// starts "unadjudicated" (Source "auto") and can only ever be moved by a
// human via Adjudicate — Record itself never downgrades a human verdict back
// to auto, which is the load-bearing invariant this store exists to hold.
type Finding struct {
	ID         string
	TS         float64
	RecordID   int64
	RecordHead string
	Repo       string
	Commit     string
	MissionID  int64
	Model      string // critic model
	TargetTest string

	TestFile     string
	TestSelector string
	Scope        string // whole-test|dead-check (already normalized)

	Evidence string
	Severity string

	Adjudication  string // unadjudicated|confirmed|refuted
	Source        string // auto|human
	AdjudicatedBy string
	AdjudicatedTS float64
}

// CriticCell is one row of the per-model precision rollup: how often a
// critic model's findings, once adjudicated, turn out to be confirmed vs
// refuted. Precision is nil when there's no adjudicated evidence yet
// (confirmed+refuted == 0) rather than a misleading 0.
type CriticCell struct {
	Model         string
	Confirmed     int
	Refuted       int
	Unadjudicated int
	Precision     *float64 // confirmed/(confirmed+refuted); nil if denom 0
}

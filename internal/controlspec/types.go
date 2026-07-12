// SPDX-License-Identifier: Elastic-2.0

package controlspec

import "time"

// Goal is one durable control-owner test goal: a control the owner wants gated on,
// scoped to the owner (the control owner/org) who set it. Owner+ID together identify
// a goal; a goal set by one owner never appears in another owner's list or
// lookups — that owner scoping is what makes goals dev-untouchable once the
// auth gate (Plan 3) is wired in front of this store.
//
// Standard/Ref/Level describe where the goal comes from when it's sourced
// from an imported bundle (e.g. OWASP ASVS 4.0.3, V2.1.1, L1); they're
// optional for a hand-authored custom goal. Mode records how the goal is
// checked ("executable" vs "attested"). CreatedTS is caller-stamped and
// persisted exactly as given — the store never calls time.Now() itself,
// which keeps it deterministic under test.
type Goal struct {
	ID       string
	Owner    string
	Standard string
	Ref      string
	Intent   string
	Level    string
	Mode     string
	// CreatedTS is stored and returned as UTC: the store normalizes it to
	// UTC on read (GetGoal/ListGoals), regardless of the location on the
	// time.Time the caller passed to SaveGoal.
	CreatedTS time.Time
}

// Requirement is one control requirement inside a Bundle — the unit a
// standard's authors publish (e.g. OWASP ASVS "V2.1.1"). ImportBundle turns
// each Requirement into one owner-scoped Goal.
type Requirement struct {
	Ref    string `json:"ref"`
	Level  string `json:"level"`
	Mode   string `json:"mode"`
	Intent string `json:"intent"`
}

// GateTest is one candidate control-owner test for a (Owner, Goal, Target) triple:
// the executable test plus the adequacy evidence (KillRate, Survived,
// Discarded mutants) that justified authoring it. A saved GateTest is always
// unvetted (Vetted=false) regardless of what the caller sets on the struct —
// SaveCandidate enforces that a fresh or re-authored candidate must be
// re-approved by a human before it can gate; only Promote (Task 2) flips
// Vetted to true. GetVetted only ever returns a vetted row, so an unvetted
// candidate is invisible to the gate. CreatedTS/VettedTS are caller-stamped
// and persisted exactly as given — the store never calls time.Now() itself.
//
// Survived/Discarded are JSON-encoded on the way into the store: a nil
// slice encodes as "null" (json.Marshal([]string(nil)) == "null"), not "[]".
// That's harmless here — it round-trips symmetrically back to a nil slice on
// read — but it's worth knowing if you compare the stored JSON by eye.
type GateTest struct {
	Owner  string
	Goal   string
	Target string
	// CodePath/TestPath are the workspace recipe: where the target's head
	// content and the vetted test land inside the minimal jail workspace when
	// the gate re-runs this test. Target is the REAL repo-relative path to
	// read head content from; CodePath is the flat filename the test expects.
	CodePath  string
	TestPath  string
	Test      string
	KillRate  float64
	Survived  []string
	Discarded []string
	Vetted    bool
	CreatedTS time.Time
	VettedTS  time.Time
	// VerdictsJSON is an opaque JSON blob of the reviewer's per-mutant
	// []Verdict decisions (StageCandidate, Task 3): the store never
	// interprets it — it's persisted and returned exactly as given, and the
	// control-owner surface decodes it with testgen.Verdict. Empty string when unset.
	VerdictsJSON string
}

// Bundle is a named, versioned set of Requirements from a published
// standard (e.g. OWASP ASVS 4.0.3) — the control owner's starter library, loaded via
// LoadBundle and turned into goals via ImportBundle.
type Bundle struct {
	Standard     string        `json:"standard"`
	Version      string        `json:"version"`
	Requirements []Requirement `json:"requirements"`
}

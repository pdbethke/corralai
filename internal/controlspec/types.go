// SPDX-License-Identifier: Elastic-2.0

package controlspec

import "time"

// Goal is one durable CISO test goal: a control the CISO wants gated on,
// scoped to the owner (the CISO/org) who set it. Owner+ID together identify
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
	ID        string
	Owner     string
	Standard  string
	Ref       string
	Intent    string
	Level     string
	Mode      string
	CreatedTS time.Time
}

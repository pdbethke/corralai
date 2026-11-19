// SPDX-License-Identifier: Elastic-2.0

// Package autoseat proposes which model of an operator's pool takes each
// audit seat, from the rankings corral has recorded.
//
// WHY A PURE FUNCTION. Staffing is a decision, and a decision that reads a
// clock, a network or a model cannot be reproduced from the record that
// justifies it. Propose takes the evidence as an argument and returns the
// same lineup for the same input, so a proposal can be tested table-style and
// explained to the operator row by row.
//
// WHY NO DEFAULTS. corral names no model of its own. Every seat is filled by
// something the operator typed: a pin, or a member of the pool they named. A
// model outside the pool that is not pinned never takes a seat, however well
// it ranks, and a seat nothing qualifies for is left unfilled with the flag
// that would fill it, never filled with a substitute. The evidence floor is
// the same: a row below it is not evidence, and the floor is never lowered to
// fill a seat.
package autoseat

import (
	"fmt"
	"strings"

	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/modelrank"
)

// How a seat came to be filled. The three are the whole vocabulary a plan and
// the record use, so a reader never has to guess which rule applied.
const (
	ByOperator = "operator"
	ByEvidence = "evidence"
	ByOrder    = "order"
)

// PinFlag maps a role to the flag that pins it. It is the one table the
// "pin --writer-model" text is built from, so a refusal names a flag that
// exists.
var PinFlag = map[string]string{
	modelrank.SeatGoalDeriver: "derive-model", modelrank.SeatMutantGenerator: "mutant-model",
	modelrank.SeatTestWriter: "writer-model", modelrank.SeatTestCritic: "critic-model",
	modelrank.SeatReviewer: "reviewer-model", modelrank.SeatVerifier: "verifier-model",
}

// off is the pin that disables a seat. It satisfies the decorrelation rule
// because there is no critic left to be correlated with the writer.
const off = "off"

// Member is one pool entry, in the order the operator named it.
type Member struct {
	Given    string // exactly what the operator typed; what a seat flag is set to
	Recorded string // the name the record ranks it under (ResolvedSeat.Spec)
	Provider string
	Rankable bool // false for a bare agent seat with no model
}

// Input is everything a proposal depends on.
type Input struct {
	Roles  []string          // in the order the verb meets them
	Pool   []Member          // in the order the operator named it
	Pins   map[string]string // role -> the operator's flag value ("off" included)
	Report modelrank.Report  // already filtered to the pool and the language
	Floor  int
}

// Seat is the proposal for one role.
type Seat struct {
	Role     string
	Given    string // "" when unfilled
	Recorded string
	By       string
	Reason   string
	Unfilled string // the fix, when the role could not be filled
}

// Plan is the whole lineup, one Seat per role in Roles order.
type Plan struct {
	Seats   []Seat
	Refused bool   // true when any seat is Unfilled or the lineup breaks a hard rule
	Why     string // the lineup-level hard-rule failure, when there is one
}

// counterpart is the seat a role must differ from: a critic that is its own
// writer, or a verifier that is its own reviewer, is a model grading its own
// work, which is what the decorrelation rule exists to prevent.
var counterpart = map[string]string{
	modelrank.SeatTestCritic: modelrank.SeatTestWriter,
	modelrank.SeatVerifier:   modelrank.SeatReviewer,
}

// measured roles are filled only from sufficient evidence. The critic is not
// here: it is measured only where a person adjudicated it, and otherwise
// falls through to pool order.
var measured = map[string]bool{
	modelrank.SeatMutantGenerator: true, modelrank.SeatTestWriter: true,
	modelrank.SeatReviewer: true, modelrank.SeatVerifier: true,
}

// Propose applies the rules in order: pins win; measured seats take the
// measured best; unmeasured seats take the pool order; the hard rules are
// checked on the whole lineup, pins included.
func Propose(in Input) Plan {
	plan := Plan{Seats: make([]Seat, len(in.Roles))}
	if in.Floor < 1 {
		// The floor is how "enough evidence" is defined; without one every
		// row would silently count, which is a default by another name.
		plan.Refused = true
		plan.Why = "the evidence floor must be at least 1; none was given"
		for i, r := range in.Roles {
			plan.Seats[i] = Seat{Role: r, Unfilled: "no evidence floor was given"}
		}
		return plan
	}

	done := map[string]Seat{}
	// Seats with a counterpart depend on it, so they are filled second, but
	// the plan keeps the caller's order.
	for _, dependent := range []bool{false, true} {
		for i, r := range in.Roles {
			if _, dep := counterpart[r]; dep != dependent {
				continue
			}
			plan.Seats[i] = in.seat(r, done)
			done[r] = plan.Seats[i]
		}
	}

	for _, s := range plan.Seats {
		if s.Unfilled != "" {
			plan.Refused = true
		}
	}
	for role, other := range counterpart {
		a, b := done[role], done[other]
		if a.Given == "" || b.Given == "" || strings.EqualFold(a.Given, off) {
			continue
		}
		if agentbackend.SameSeat(a.Given, b.Given) {
			plan.Refused = true
			plan.Why = fmt.Sprintf("%s and %s are both %q; a model must not grade its own work",
				role, other, a.Given)
		}
	}
	return plan
}

func (in Input) seat(role string, done map[string]Seat) Seat {
	if v, ok := in.Pins[role]; ok {
		return Seat{Role: role, Given: v, Recorded: in.recordedFor(v), By: ByOperator,
			Reason: fmt.Sprintf("pinned by --%s", PinFlag[role])}
	}
	avoid := in.avoid(role, done)
	if row, ok := in.best(role, avoid); ok {
		mem, _ := in.member(row.Model)
		return Seat{Role: role, Given: mem.Given, Recorded: mem.Recorded, By: ByEvidence, Reason: row.Evidence}
	}
	switch {
	case measured[role]:
		return Seat{Role: role, Unfilled: fmt.Sprintf(
			"no model in --models has %d+ %s as %s; pin --%s",
			in.Floor, in.unit(role), role, PinFlag[role])}
	case role == modelrank.SeatGoalDeriver, role == modelrank.SeatTestCritic:
		return in.byOrder(role, avoid)
	}
	return Seat{Role: role, Unfilled: fmt.Sprintf("no rule seats %s; pin --%s", role, PinFlag[role])}
}

// byOrder seats the first pool member that is not the counterpart, preferring
// a different provider: two models from one vendor satisfy the letter of
// decorrelation and not its point.
func (in Input) byOrder(role, avoid string) Seat {
	const reason = "no measurement for this seat: taken from the order you named"
	var same *Member
	for i, mem := range in.Pool {
		if avoid != "" && agentbackend.SameSeat(mem.Recorded, avoid) {
			continue
		}
		if avoid != "" && same == nil && in.providerOf(avoid) != "" && mem.Provider == in.providerOf(avoid) {
			same = &in.Pool[i]
			continue
		}
		return Seat{Role: role, Given: mem.Given, Recorded: mem.Recorded, By: ByOrder, Reason: reason}
	}
	if same != nil {
		return Seat{Role: role, Given: same.Given, Recorded: same.Recorded, By: ByOrder,
			Reason: reason + " (same provider as " + counterpart[role] + "; no other member was available)"}
	}
	fix := fmt.Sprintf("name a second model in --models, or pin --%s", PinFlag[role])
	if role == modelrank.SeatTestCritic {
		fix = "name a second model in --models, or pass --critic-model off"
	}
	return Seat{Role: role, Unfilled: fmt.Sprintf("no pool member other than the %s can take %s; %s",
		counterpart[role], role, fix)}
}

// best walks the group's rows in the order modelrank returned them and takes
// the first sufficient, rankable pool member that is not the avoided model.
// Members whose metric ties the best are resolved by pool position.
func (in Input) best(role, avoid string) (modelrank.Row, bool) {
	var cands []modelrank.Row
	for _, g := range in.Report.Groups {
		if g.Seat != role {
			continue
		}
		for _, r := range g.Rows {
			mem, ok := in.member(r.Model)
			if !ok || !mem.Rankable || !r.Sufficient || r.Metric == nil {
				continue
			}
			if avoid != "" && agentbackend.SameSeat(mem.Recorded, avoid) {
				continue
			}
			cands = append(cands, r)
		}
		break
	}
	if len(cands) == 0 {
		return modelrank.Row{}, false
	}
	win, winPos := cands[0], in.position(cands[0].Model)
	for _, r := range cands[1:] {
		if *r.Metric == *cands[0].Metric && in.position(r.Model) < winPos {
			win, winPos = r, in.position(r.Model)
		}
	}
	return win, true
}

// avoid is the recorded name the role must differ from, taken from the
// counterpart's seat (a pin included), or "".
func (in Input) avoid(role string, done map[string]Seat) string {
	other, ok := counterpart[role]
	if !ok {
		return ""
	}
	s := done[other]
	if s.Given == "" || strings.EqualFold(s.Given, off) {
		return ""
	}
	if s.Recorded != "" {
		return s.Recorded
	}
	return s.Given
}

func (in Input) member(recorded string) (Member, bool) {
	for _, m := range in.Pool {
		if m.Recorded == recorded {
			return m, true
		}
	}
	return Member{}, false
}

func (in Input) position(recorded string) int {
	for i, m := range in.Pool {
		if m.Recorded == recorded {
			return i
		}
	}
	return len(in.Pool)
}

// recordedFor resolves a pin to the name the record knows it by when it is a
// pool member; a pin outside the pool is recorded as typed.
func (in Input) recordedFor(pin string) string {
	for _, m := range in.Pool {
		if agentbackend.SameSeat(m.Given, pin) || agentbackend.SameSeat(m.Recorded, pin) {
			return m.Recorded
		}
	}
	return pin
}

func (in Input) providerOf(recorded string) string {
	if m, ok := in.member(recorded); ok {
		return m.Provider
	}
	return ""
}

// unit names the seat's own evidence unit for the refusal text, read from a
// row of the report so it is the unit modelrank counted in.
func (in Input) unit(role string) string {
	for _, g := range in.Report.Groups {
		if g.Seat == role {
			for _, r := range g.Rows {
				if r.NUnit != "" {
					return r.NUnit
				}
			}
		}
	}
	return "observations"
}

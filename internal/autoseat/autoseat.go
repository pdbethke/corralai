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

// EvidenceFor is the evidence line a record keeps beside seatedBy (role ->
// how it was chosen): the line when any seat seatedBy names was seated by
// evidence, and "" otherwise. A record narrows SeatedBy to the seats it
// holds, and the line explains only an evidence seat, so a record whose
// evidence seats were all narrowed away keeps no line.
func EvidenceFor(seatedBy map[string]string, evidence string) string {
	for _, by := range seatedBy {
		if by == ByEvidence {
			return evidence
		}
	}
	return ""
}

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

// Pin is a seat the operator named with its own flag, resolved by the same
// resolver as the pool's members. The hard rule compares the models that
// would run: an alias, a vendor/model spelling and an agent pinned to a model
// can all name one model, and comparing the typed text would let a model
// grade its own work under a second spelling.
type Pin struct {
	Given    string // exactly what the operator typed; "off" disables the seat
	Recorded string // the resolved name (ResolvedSeat.Spec); "" only for "off"
	Provider string // the resolved provider; "" when the resolver names none
}

// Input is everything a proposal depends on.
type Input struct {
	Roles  []string         // in the order the verb meets them
	Pool   []Member         // in the order the operator named it
	Pins   map[string]Pin   // role -> the operator's resolved flag value ("off" included)
	Report modelrank.Report // already filtered to the pool and the language
	Floor  int
}

// Seat is the proposal for one role.
type Seat struct {
	Role     string
	Given    string // "" when unfilled
	Recorded string // "" when unfilled or pinned "off"
	Provider string
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
	// The hard rule, on the whole lineup with pins included, comparing the
	// resolved names. Walked in Roles order, never over the map, so the same
	// input always gives the same text, and every broken pair is reported:
	// an operator who fixes one should not discover the next on a rerun.
	var why []string
	for _, role := range in.Roles {
		other, ok := counterpart[role]
		if !ok {
			continue
		}
		a, b := done[role], done[other]
		// An unfilled seat or one pinned off has no Recorded name, and there
		// is nothing for it to be correlated with.
		if a.Recorded == "" || b.Recorded == "" {
			continue
		}
		if agentbackend.SameSeat(a.Recorded, b.Recorded) {
			why = append(why, fmt.Sprintf(
				"%s and %s are both %s; a model must not grade its own work: pin --%s or --%s to a different model, or pass --%s off",
				role, other, agentbackend.SeatModelOf(a.Recorded), PinFlag[role], PinFlag[other], PinFlag[role]))
		}
	}
	if len(why) > 0 {
		plan.Refused = true
		plan.Why = strings.Join(why, "; and ")
	}
	return plan
}

func (in Input) seat(role string, done map[string]Seat) Seat {
	if p, ok := in.Pins[role]; ok {
		reason := fmt.Sprintf("pinned by --%s", PinFlag[role])
		if strings.EqualFold(strings.TrimSpace(p.Given), off) {
			return Seat{Role: role, Given: p.Given, By: ByOperator, Reason: reason}
		}
		if strings.TrimSpace(p.Recorded) == "" {
			// A pin the caller did not resolve cannot be checked against
			// the hard rule, and treating its typed text as its model is
			// the comparison this field exists to replace.
			return Seat{Role: role, Unfilled: fmt.Sprintf(
				"--%s %q reached the proposer unresolved, so the lineup cannot be checked; this is a corral defect",
				PinFlag[role], p.Given)}
		}
		return Seat{Role: role, Given: p.Given, Recorded: p.Recorded, Provider: p.Provider, By: ByOperator, Reason: reason}
	}
	other := in.counterSeat(role, done)
	if row, ok := in.best(role, other.Recorded); ok {
		mem, _ := in.member(row.Model)
		return Seat{Role: role, Given: mem.Given, Recorded: mem.Recorded, Provider: mem.Provider, By: ByEvidence, Reason: row.Evidence}
	}
	switch {
	case measured[role]:
		if other.Recorded != "" {
			if row, ok := in.best(role, ""); ok {
				// Every member with enough evidence is the counterpart's
				// model, so "no model has the evidence" would be false.
				return Seat{Role: role, Unfilled: fmt.Sprintf(
					"the only model in --models with %d+ %s as %s is %s, already the %s; name another model in --models, or pin --%s",
					in.Floor, in.unit(role), role, row.Model, counterpart[role], PinFlag[role])}
			}
		}
		return Seat{Role: role, Unfilled: fmt.Sprintf(
			"no model in --models has %d+ %s as %s; pin --%s",
			in.Floor, in.unit(role), role, PinFlag[role])}
	case role == modelrank.SeatGoalDeriver, role == modelrank.SeatTestCritic:
		return in.byOrder(role, other)
	}
	return Seat{Role: role, Unfilled: fmt.Sprintf("no rule seats %s; pin --%s", role, PinFlag[role])}
}

// byOrder seats the first pool member that is not the counterpart, preferring
// a different provider: two models from one vendor satisfy the letter of
// decorrelation and not its point. The counterpart's provider is known for a
// pin as well as a member, because pins are resolved before they get here.
func (in Input) byOrder(role string, other Seat) Seat {
	const reason = "no measurement for this seat: taken from the order you named"
	var same *Member
	for i, mem := range in.Pool {
		if other.Recorded != "" && agentbackend.SameSeat(mem.Recorded, other.Recorded) {
			continue
		}
		if other.Provider != "" && same == nil && strings.EqualFold(mem.Provider, other.Provider) {
			same = &in.Pool[i]
			continue
		}
		return Seat{Role: role, Given: mem.Given, Recorded: mem.Recorded, Provider: mem.Provider, By: ByOrder, Reason: reason}
	}
	if same != nil {
		return Seat{Role: role, Given: same.Given, Recorded: same.Recorded, Provider: same.Provider, By: ByOrder,
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
//
// A row must clear this proposal's own floor as well as be marked
// sufficient: Sufficient is modelrank's verdict at whatever floor that
// ranking ran with, and the proposer is the one place that enforces the
// floor it was given rather than trusting a caller to have ranked at it.
func (in Input) best(role, avoid string) (modelrank.Row, bool) {
	var cands []modelrank.Row
	for _, g := range in.Report.Groups {
		if g.Seat != role {
			continue
		}
		for _, r := range g.Rows {
			mem, ok := in.member(r.Model)
			if !ok || !mem.Rankable || !r.Sufficient || r.N < in.Floor || r.Metric == nil {
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

// counterSeat is the seat the role must differ from (a pin included), or
// the zero Seat when the role has no counterpart, or the counterpart is
// unfilled or pinned off. Its Recorded is what the role avoids and its
// Provider what the order rule prefers to avoid.
func (in Input) counterSeat(role string, done map[string]Seat) Seat {
	other, ok := counterpart[role]
	if !ok {
		return Seat{}
	}
	return done[other]
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

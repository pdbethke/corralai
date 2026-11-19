// SPDX-License-Identifier: Elastic-2.0

package autoseat

import (
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/modelrank"
)

func f(v float64) *float64 { return &v }

func row(seat, model string, metric float64, sufficient bool) modelrank.Row {
	return modelrank.Row{Seat: seat, Model: model, Metric: f(metric), Sufficient: sufficient,
		Evidence: "evidence for " + model, N: 9, NUnit: "survivors attempted"}
}

func report(rows ...modelrank.Row) modelrank.Report {
	var r modelrank.Report
	bySeat := map[string]int{}
	for _, rw := range rows {
		i, ok := bySeat[rw.Seat]
		if !ok {
			r.Groups = append(r.Groups, modelrank.Group{Seat: rw.Seat})
			i = len(r.Groups) - 1
			bySeat[rw.Seat] = i
		}
		r.Groups[i].Rows = append(r.Groups[i].Rows, rw)
	}
	return r
}

func m(name, provider string) Member {
	return Member{Given: name, Recorded: name, Provider: provider, Rankable: true}
}

var certifyRoles = []string{modelrank.SeatGoalDeriver, modelrank.SeatMutantGenerator, modelrank.SeatTestWriter, modelrank.SeatTestCritic}

func seatOf(t *testing.T, p Plan, role string) Seat {
	t.Helper()
	for _, s := range p.Seats {
		if s.Role == role {
			return s
		}
	}
	t.Fatalf("no seat for %s", role)
	return Seat{}
}

func TestPropose(t *testing.T) {
	W, G, C, D := modelrank.SeatTestWriter, modelrank.SeatMutantGenerator, modelrank.SeatTestCritic, modelrank.SeatGoalDeriver
	three := []Member{m("a", "anthropic"), m("b", "anthropic"), m("c", "openai")}
	tests := []struct {
		name    string
		in      Input
		role    string
		given   string
		by      string
		reason  string
		refused bool
	}{
		{"pin wins over a better row", Input{Roles: certifyRoles, Pool: three, Pins: map[string]string{W: "x"},
			Report: report(row(W, "a", 0.99, true)), Floor: 5}, W, "x", ByOperator, "", false},
		{"measured best", Input{Roles: certifyRoles, Pool: three,
			Report: report(row(W, "b", 0.99, true), row(W, "a", 0.80, true)), Floor: 5}, W, "b", ByEvidence, "evidence for b", false},
		{"tie goes to pool order", Input{Roles: certifyRoles, Pool: three,
			Report: report(row(W, "c", 0.9, true), row(W, "b", 0.9, true)), Floor: 5}, W, "b", ByEvidence, "evidence for b", false},
		{"below the floor", Input{Roles: certifyRoles, Pool: three,
			Report: report(row(W, "a", 0.99, false)), Floor: 5}, W, "", "", "", true},
		{"row outside the pool is never chosen", Input{Roles: certifyRoles, Pool: three,
			Report: report(row(W, "zzz", 0.99, true), row(W, "c", 0.5, true)), Floor: 5}, W, "c", ByEvidence, "", false},
		{"unrankable member is not chosen by evidence", Input{Roles: certifyRoles,
			Pool:   []Member{{Given: "codex", Recorded: "codex", Provider: "openai"}, m("b", "anthropic")},
			Report: report(row(G, "codex", 0.99, true), row(G, "b", 0.4, true)), Floor: 5}, G, "b", ByEvidence, "", false},
		{"unrankable member can take the deriver", Input{Roles: certifyRoles,
			Pool: []Member{{Given: "codex", Recorded: "codex", Provider: "openai"}, m("b", "anthropic")}, Floor: 5},
			D, "codex", ByOrder, "no measurement for this seat: taken from the order you named", false},
		{"critic prefers a different provider", Input{Roles: certifyRoles, Pool: three, Pins: map[string]string{W: "a"}, Floor: 5},
			C, "c", ByOrder, "taken from the order", false},
		{"critic with a sufficient row is by evidence", Input{Roles: certifyRoles, Pool: three,
			Report: report(row(C, "b", 0.7, true)), Floor: 5}, C, "b", ByEvidence, "evidence for b", false},
		{"critic evidence equal to writer is skipped", Input{Roles: certifyRoles, Pool: three, Pins: map[string]string{W: "a"},
			Report: report(row(C, "a", 0.9, true), row(C, "b", 0.7, true)), Floor: 5}, C, "b", ByEvidence, "evidence for b", false},
		{"critic evidence only the writer falls back to order", Input{Roles: certifyRoles, Pool: three, Pins: map[string]string{W: "a"},
			Report: report(row(C, "a", 0.9, true)), Floor: 5}, C, "c", ByOrder, "", false},
		{"one model cannot seat a critic", Input{Roles: certifyRoles, Pool: []Member{m("a", "anthropic")}, Pins: map[string]string{W: "a"}, Floor: 5},
			C, "", "", "", true},
		{"critic off is a pin", Input{Roles: certifyRoles, Pool: []Member{m("a", "anthropic")}, Pins: map[string]string{W: "a", C: "off"}, Floor: 5},
			C, "off", ByOperator, "", false},
		{"pinned writer b leaves critic a", Input{Roles: certifyRoles, Pool: []Member{m("a", "anthropic"), m("b", "anthropic")},
			Pins: map[string]string{W: "b"}, Floor: 5}, C, "a", ByOrder, "", false},
		{"pinned critic equal to writer breaks the hard rule", Input{Roles: certifyRoles, Pool: three,
			Pins: map[string]string{W: "a", C: "a"}, Floor: 5}, C, "a", ByOperator, "", true},
		{"verifier is never the reviewer", Input{Roles: []string{modelrank.SeatReviewer, modelrank.SeatVerifier}, Pool: three,
			Report: report(row(modelrank.SeatReviewer, "a", 0.9, true), row(modelrank.SeatVerifier, "a", 0.95, true), row(modelrank.SeatVerifier, "c", 0.6, true)),
			Floor:  5}, modelrank.SeatVerifier, "c", ByEvidence, "evidence for c", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := Propose(tc.in)
			s := seatOf(t, p, tc.role)
			if s.Given != tc.given || (tc.by != "" && s.By != tc.by) {
				t.Fatalf("seat = %+v, want given %q by %q", s, tc.given, tc.by)
			}
			if tc.reason != "" && !strings.Contains(s.Reason, tc.reason) {
				t.Fatalf("reason %q lacks %q", s.Reason, tc.reason)
			}
			// The other roles of the lineup may be legitimately unfilled in
			// these hand-built inputs, so the case asserts about ITS seat (or
			// the lineup-level rule), and that any such failure refuses the plan.
			failed := s.Unfilled != "" || p.Why != ""
			if failed != tc.refused || (failed && !p.Refused) {
				t.Fatalf("failed = %v, want %v (plan %+v)", failed, tc.refused, p)
			}
			if len(p.Seats) != len(tc.in.Roles) {
				t.Fatalf("%d seats for %d roles", len(p.Seats), len(tc.in.Roles))
			}
		})
	}
}

func TestProposeUnfilledNamesTheFix(t *testing.T) {
	W, C := modelrank.SeatTestWriter, modelrank.SeatTestCritic
	p := Propose(Input{Roles: certifyRoles, Pool: []Member{m("a", "anthropic")},
		Pins: map[string]string{W: "a"}, Report: report(row(W, "a", 0.99, false)), Floor: 5})
	w := Propose(Input{Roles: certifyRoles, Pool: []Member{m("a", "anthropic")},
		Report: report(row(W, "a", 0.99, false)), Floor: 5})
	if s := seatOf(t, w, W); !strings.Contains(s.Unfilled, "pin --writer-model") || !strings.Contains(s.Unfilled, "5") || !w.Refused {
		t.Fatalf("writer unfilled text = %q refused=%v", s.Unfilled, w.Refused)
	}
	if s := seatOf(t, p, C); !strings.Contains(s.Unfilled, "--critic-model off") {
		t.Fatalf("critic unfilled text = %q", s.Unfilled)
	}
}

func TestProposeHardRuleSetsWhy(t *testing.T) {
	p := Propose(Input{Roles: certifyRoles, Pool: []Member{m("a", "x"), m("b", "x")},
		Pins: map[string]string{modelrank.SeatTestWriter: "a", modelrank.SeatTestCritic: "A"}, Floor: 5})
	if !p.Refused || p.Why == "" {
		t.Fatalf("plan = %+v, want refused with Why", p)
	}
}

func TestProposeRefusesAMissingFloor(t *testing.T) {
	p := Propose(Input{Roles: certifyRoles, Pool: []Member{m("a", "x")}})
	if !p.Refused || !strings.Contains(p.Why, "floor") {
		t.Fatalf("plan = %+v, want a refusal naming the floor", p)
	}
}

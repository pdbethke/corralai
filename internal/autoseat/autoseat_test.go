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

// testProviders is the provider each test model resolves to, so a pin built
// by pins carries what the door's resolver would have given it.
var testProviders = map[string]string{"a": "anthropic", "b": "anthropic", "c": "openai"}

// pins builds resolved pins from role, name pairs, as seatFromPool hands them
// over: Recorded is the name (these tests have no aliases) and "off" has none.
func pins(kv ...string) map[string]Pin {
	out := map[string]Pin{}
	for i := 0; i+1 < len(kv); i += 2 {
		if strings.EqualFold(kv[i+1], "off") {
			out[kv[i]] = Pin{Given: kv[i+1]}
			continue
		}
		out[kv[i]] = Pin{Given: kv[i+1], Recorded: kv[i+1], Provider: testProviders[strings.ToLower(kv[i+1])]}
	}
	return out
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
		{"pin wins over a better row", Input{Roles: certifyRoles, Pool: three, Pins: pins(W, "x"),
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
		{"critic prefers a different provider", Input{Roles: certifyRoles, Pool: three, Pins: pins(W, "a"), Floor: 5},
			C, "c", ByOrder, "taken from the order", false},
		{"critic with a sufficient row is by evidence", Input{Roles: certifyRoles, Pool: three,
			Report: report(row(C, "b", 0.7, true)), Floor: 5}, C, "b", ByEvidence, "evidence for b", false},
		{"critic evidence equal to writer is skipped", Input{Roles: certifyRoles, Pool: three, Pins: pins(W, "a"),
			Report: report(row(C, "a", 0.9, true), row(C, "b", 0.7, true)), Floor: 5}, C, "b", ByEvidence, "evidence for b", false},
		{"critic evidence only the writer falls back to order", Input{Roles: certifyRoles, Pool: three, Pins: pins(W, "a"),
			Report: report(row(C, "a", 0.9, true)), Floor: 5}, C, "c", ByOrder, "", false},
		{"one model cannot seat a critic", Input{Roles: certifyRoles, Pool: []Member{m("a", "anthropic")}, Pins: pins(W, "a"), Floor: 5},
			C, "", "", "", true},
		{"critic off is a pin", Input{Roles: certifyRoles, Pool: []Member{m("a", "anthropic")}, Pins: pins(W, "a", C, "off"), Floor: 5},
			C, "off", ByOperator, "", false},
		{"pinned writer b leaves critic a", Input{Roles: certifyRoles, Pool: []Member{m("a", "anthropic"), m("b", "anthropic")},
			Pins: pins(W, "b"), Floor: 5}, C, "a", ByOrder, "", false},
		{"pinned critic equal to writer breaks the hard rule", Input{Roles: certifyRoles, Pool: three,
			Pins: pins(W, "a", C, "a"), Floor: 5}, C, "a", ByOperator, "", true},
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
			// A refused seat names its flag and the floor it missed (C3).
			if tc.name == "below the floor" && (!strings.Contains(s.Unfilled, "5+") || !strings.Contains(s.Unfilled, "pin --writer-model")) {
				t.Errorf("unfilled %q must name the floor (5+) and the flag", s.Unfilled)
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
		Pins: pins(W, "a"), Report: report(row(W, "a", 0.99, false)), Floor: 5})
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
		Pins: pins(modelrank.SeatTestWriter, "a", modelrank.SeatTestCritic, "A"), Floor: 5})
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

// The floor is the proposer's own rule, not only modelrank's: a row marked
// sufficient by a ranking run at a lower floor is still below this one.
func TestProposeEnforcesTheFloorItself(t *testing.T) {
	W := modelrank.SeatTestWriter
	r := row(W, "a", 0.99, true)
	r.N = 3
	p := Propose(Input{Roles: certifyRoles, Pool: []Member{m("a", "anthropic"), m("b", "openai")},
		Report: report(r), Floor: 5})
	if s := seatOf(t, p, W); s.Given != "" || !strings.Contains(s.Unfilled, "5+") || !p.Refused {
		t.Fatalf("writer seated from a row with N=3 under a floor of 5: %+v", s)
	}
}

// A measured seat whose only qualified member is its counterpart says so,
// rather than claiming no member has the evidence.
func TestProposeSaysWhenTheOnlyQualifiedMemberIsTheCounterpart(t *testing.T) {
	R, V := modelrank.SeatReviewer, modelrank.SeatVerifier
	p := Propose(Input{Roles: []string{R, V}, Pool: []Member{m("a", "anthropic"), m("c", "openai")},
		Report: report(row(R, "a", 0.9, true), row(V, "a", 0.95, true)), Floor: 5})
	s := seatOf(t, p, V)
	if !strings.Contains(s.Unfilled, "already the reviewer") || !strings.Contains(s.Unfilled, "pin --verifier-model") {
		t.Fatalf("verifier unfilled text = %q", s.Unfilled)
	}
}

// Every broken pair is reported, in a fixed order, and each names the flags
// that resolve it.
func TestProposeHardRuleReportsEveryPairInOrder(t *testing.T) {
	W, C, R, V := modelrank.SeatTestWriter, modelrank.SeatTestCritic, modelrank.SeatReviewer, modelrank.SeatVerifier
	in := Input{Roles: []string{W, C, R, V}, Pool: []Member{m("a", "x"), m("b", "y")},
		Pins: pins(W, "a", C, "a", R, "b", V, "b"), Floor: 5}
	first := Propose(in).Why
	for i := 0; i < 20; i++ {
		if got := Propose(in).Why; got != first {
			t.Fatalf("Why differs between runs:\n%q\n%q", first, got)
		}
	}
	for _, want := range []string{"test-critic and test-writer", "verifier and reviewer",
		"--critic-model", "--writer-model", "--critic-model off", "--verifier-model", "--reviewer-model"} {
		if !strings.Contains(first, want) {
			t.Errorf("Why %q lacks %q", first, want)
		}
	}
	if strings.Index(first, "test-critic") > strings.Index(first, "verifier and") {
		t.Errorf("pairs not in Roles order: %q", first)
	}
}

// Pins are compared by the model they resolve to, not the text the operator
// typed: an alias, a vendor/model spelling and an agent pinned to a model can
// name one model (B3). Every case here has Given != Recorded somewhere.
func TestProposeComparesResolvedPins(t *testing.T) {
	W, C := modelrank.SeatTestWriter, modelrank.SeatTestCritic
	sonnet := Member{Given: "anthropic/claude-sonnet-5-5", Recorded: "claude-sonnet-5-5", Provider: "anthropic", Rankable: true}
	haiku := Member{Given: "anthropic/claude-haiku-5-5", Recorded: "claude-haiku-5-5", Provider: "anthropic", Rankable: true}
	codex := Member{Given: "codex:gpt-6", Recorded: "codex:gpt-6", Provider: "openai", Rankable: true}
	edge := Member{Given: "edge2", Recorded: "localmodel-a:1", Provider: "ollama", Rankable: true}

	t.Run("agent-pinned critic against the evidence-seated vendor/model writer", func(t *testing.T) {
		p := Propose(Input{Roles: certifyRoles, Pool: []Member{sonnet, codex},
			Pins:   map[string]Pin{C: {Given: "claude-code:claude-sonnet-5-5", Recorded: "claude-code:claude-sonnet-5-5", Provider: "anthropic"}},
			Report: report(row(W, "claude-sonnet-5-5", 0.9, true)), Floor: 5})
		if !p.Refused || !strings.Contains(p.Why, "both claude-sonnet-5-5") {
			t.Fatalf("plan = %+v, want the hard rule to refuse one model in both seats", p)
		}
	})
	t.Run("bare pin against vendor/model pin", func(t *testing.T) {
		p := Propose(Input{Roles: certifyRoles, Pool: []Member{codex},
			Pins: map[string]Pin{
				W: {Given: "anthropic/claude-sonnet-5-5", Recorded: "claude-sonnet-5-5", Provider: "anthropic"},
				C: {Given: "sonnet-alias", Recorded: "claude-sonnet-5-5", Provider: "anthropic"},
			}, Floor: 5})
		if !p.Refused || p.Why == "" {
			t.Fatalf("plan = %+v, want refused", p)
		}
	})
	t.Run("alias-pinned writer is not proposed again as the critic", func(t *testing.T) {
		p := Propose(Input{Roles: certifyRoles, Pool: []Member{edge, haiku},
			Pins: map[string]Pin{W: {Given: "edge", Recorded: "localmodel-a:1", Provider: "ollama"}}, Floor: 5})
		if s := seatOf(t, p, C); s.Given != haiku.Given || s.By != ByOrder {
			t.Fatalf("critic = %+v, want %s by order", s, haiku.Given)
		}
	})
	t.Run("a pinned writer outside the pool still steers the critic's provider", func(t *testing.T) {
		p := Propose(Input{Roles: certifyRoles, Pool: []Member{haiku, codex},
			Pins: map[string]Pin{W: {Given: "anthropic/claude-sonnet-5-5", Recorded: "claude-sonnet-5-5", Provider: "anthropic"}}, Floor: 5})
		if s := seatOf(t, p, C); s.Given != codex.Given {
			t.Fatalf("critic = %+v, want %s (a provider other than the writer's)", s, codex.Given)
		}
	})
	t.Run("an unresolved pin is refused, not compared by its typed text", func(t *testing.T) {
		p := Propose(Input{Roles: certifyRoles, Pool: []Member{haiku, codex},
			Pins: map[string]Pin{W: {Given: "anthropic/claude-sonnet-5-5"}}, Floor: 5})
		if s := seatOf(t, p, W); s.Unfilled == "" || !p.Refused {
			t.Fatalf("writer = %+v, want refused", s)
		}
	})
}

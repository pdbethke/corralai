// SPDX-License-Identifier: Elastic-2.0

package agentrole

import (
	"reflect"
	"testing"
)

func TestParseSingleRoleUnchanged(t *testing.T) {
	s := Parse("builder")
	if s.Any {
		t.Fatal("single role must not be Any")
	}
	if !reflect.DeepEqual(s.Roles, []string{"builder"}) {
		t.Fatalf("Roles = %v, want [builder]", s.Roles)
	}
	if got := s.Display(); got != "builder" {
		t.Fatalf("Display() = %q, want %q", got, "builder")
	}
	if got := s.ClaimArg(); !reflect.DeepEqual(got, []string{"builder"}) {
		t.Fatalf("ClaimArg() = %v, want [builder]", got)
	}
}

func TestParseCommaList(t *testing.T) {
	s := Parse("researcher, designer,tester")
	want := []string{"researcher", "designer", "tester"}
	if !reflect.DeepEqual(s.Roles, want) {
		t.Fatalf("Roles = %v, want %v (whitespace must be trimmed)", s.Roles, want)
	}
	if s.Any {
		t.Fatal("comma list must not be Any")
	}
	if got := s.Display(); got != "researcher+designer+tester" {
		t.Fatalf("Display() = %q", got)
	}
	if got := s.ClaimArg(); !reflect.DeepEqual(got, want) {
		t.Fatalf("ClaimArg() = %v, want %v", got, want)
	}
}

func TestParseAnyVariants(t *testing.T) {
	for _, raw := range []string{"", "any", "ANY", "Any", "*", "  ", " , , ", "generalist", "Generalist"} {
		s := Parse(raw)
		if !s.Any {
			t.Fatalf("Parse(%q).Any = false, want true", raw)
		}
		if len(s.Roles) != 0 {
			t.Fatalf("Parse(%q).Roles = %v, want empty", raw, s.Roles)
		}
		if got := s.Display(); got != "generalist" {
			t.Fatalf("Parse(%q).Display() = %q, want generalist", raw, got)
		}
		if got := s.ClaimArg(); len(got) != 0 {
			t.Fatalf("Parse(%q).ClaimArg() = %v, want empty (any ready task)", raw, got)
		}
	}
}

func TestParseTrimsWhitespaceAroundEntries(t *testing.T) {
	s := Parse("  builder ,  tester  ")
	want := []string{"builder", "tester"}
	if !reflect.DeepEqual(s.Roles, want) {
		t.Fatalf("Roles = %v, want %v", s.Roles, want)
	}
}

// Coverage must reverse Display(): the string a worker registers into its
// single coord.Agent.Role field ("a+b", "generalist", "builder") has to parse
// back into the role Set that worker actually claims against, so the brain's
// coverage checks stop reading the collapsed Display string as one opaque role.
func TestCoverageReversesDisplay(t *testing.T) {
	for _, raw := range []string{"builder", "researcher, designer, tester", "any", "*", ""} {
		want := Parse(raw)
		got := Coverage(want.Display())
		if got.Any != want.Any || !reflect.DeepEqual(got.ClaimArg(), want.ClaimArg()) {
			t.Fatalf("Coverage(%q.Display()=%q) = %+v, want %+v", raw, want.Display(), got, want)
		}
	}
}

// An empty Role field is an unregistered worker, NOT a generalist — Display()
// never emits empty, so Coverage("") must cover nothing rather than wildcard.
func TestCoverageEmptyIsNoCoverage(t *testing.T) {
	s := Coverage("")
	if s.Any {
		t.Fatal("Coverage(\"\") must not be Any — empty Role is unregistered, not generalist")
	}
	if s.Covers("builder") {
		t.Fatal("Coverage(\"\") must cover nothing")
	}
}

func TestCovers(t *testing.T) {
	if !Coverage("generalist").Covers("perf") {
		t.Fatal("a generalist must cover every role")
	}
	multi := Coverage("researcher+designer")
	if !multi.Covers("designer") || !multi.Covers("researcher") {
		t.Fatal("a multi-role worker must cover each of its roles")
	}
	if multi.Covers("tester") {
		t.Fatal("a multi-role worker must not cover a role it does not list")
	}
}

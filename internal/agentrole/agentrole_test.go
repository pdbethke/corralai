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
	for _, raw := range []string{"", "any", "ANY", "Any", "*", "  ", " , , "} {
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

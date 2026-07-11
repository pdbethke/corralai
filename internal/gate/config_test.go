// SPDX-License-Identifier: Elastic-2.0

package gate

import (
	"reflect"
	"testing"
)

func TestParsePoliciesSingleEntry(t *testing.T) {
	pols, bad := ParsePolicies("repo=owner/name,base=main,cmd=go test ./...,net=false")
	if len(bad) != 0 {
		t.Fatalf("unexpected bad entries: %v", bad)
	}
	want := []Policy{{
		Repo:     "owner/name",
		Base:     []string{"main"},
		Context:  "corral/gate",
		CheckCmd: []string{"go", "test", "./..."},
		AllowNet: false,
	}}
	if !reflect.DeepEqual(pols, want) {
		t.Fatalf("got %+v, want %+v", pols, want)
	}
}

func TestParsePoliciesMultipleEntriesSemicolonSeparated(t *testing.T) {
	pols, bad := ParsePolicies("repo=o/a,base=main,cmd=make test;repo=o/b,base=main,cmd=make check,net=true")
	if len(bad) != 0 {
		t.Fatalf("unexpected bad entries: %v", bad)
	}
	if len(pols) != 2 {
		t.Fatalf("got %d policies, want 2: %+v", len(pols), pols)
	}
	if pols[0].Repo != "o/a" || pols[0].AllowNet {
		t.Fatalf("policy 0 wrong: %+v", pols[0])
	}
	if pols[1].Repo != "o/b" || !pols[1].AllowNet {
		t.Fatalf("policy 1 wrong: %+v", pols[1])
	}
}

func TestParsePoliciesEmptyStringYieldsNoPolicies(t *testing.T) {
	pols, bad := ParsePolicies("")
	if len(pols) != 0 || len(bad) != 0 {
		t.Fatalf("got pols=%v bad=%v, want both empty (feature off)", pols, bad)
	}
}

func TestParsePoliciesMissingRepoIsBadNotFatal(t *testing.T) {
	pols, bad := ParsePolicies("base=main,cmd=true;repo=o/ok,base=main,cmd=true")
	if len(pols) != 1 || pols[0].Repo != "o/ok" {
		t.Fatalf("expected the one well-formed entry to survive: %+v", pols)
	}
	if len(bad) != 1 {
		t.Fatalf("expected the malformed entry reported, got %v", bad)
	}
}

func TestParsePoliciesMissingCmdIsBadNotFatal(t *testing.T) {
	pols, bad := ParsePolicies("repo=o/r,base=main")
	if len(pols) != 0 {
		t.Fatalf("expected no policies from an entry with no cmd, got %+v", pols)
	}
	if len(bad) != 1 {
		t.Fatalf("expected the malformed entry reported, got %v", bad)
	}
}

func TestParsePoliciesDefaultsBaseAndContext(t *testing.T) {
	pols, bad := ParsePolicies("repo=o/r,cmd=true")
	if len(bad) != 0 {
		t.Fatalf("unexpected bad entries: %v", bad)
	}
	if len(pols) != 1 {
		t.Fatalf("got %d policies, want 1", len(pols))
	}
	if len(pols[0].Base) != 0 {
		t.Fatalf("expected no base restriction (all bases) when base= is omitted, got %v", pols[0].Base)
	}
	if pols[0].Context != "corral/gate" {
		t.Fatalf("expected default context 'corral/gate', got %q", pols[0].Context)
	}
}

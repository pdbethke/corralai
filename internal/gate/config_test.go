// SPDX-License-Identifier: Elastic-2.0

package gate

import (
	"reflect"
	"strings"
	"testing"
)

func TestParsePoliciesSingleEntry(t *testing.T) {
	pols, bad := ParsePolicies("repo=owner/name,base=main,net=false,cmd=go test ./...")
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
	pols, bad := ParsePolicies("repo=o/a,base=main,cmd=make test;repo=o/b,base=main,net=true,cmd=make check")
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

// TestParsePoliciesTimeoutParsesToSeconds: an explicit timeout= field parses
// into Policy.TimeoutS as an int number of seconds.
func TestParsePoliciesTimeoutParsesToSeconds(t *testing.T) {
	pols, bad := ParsePolicies("repo=o/r,base=main,timeout=120,cmd=true")
	if len(bad) != 0 {
		t.Fatalf("unexpected bad entries: %v", bad)
	}
	if len(pols) != 1 || pols[0].TimeoutS != 120 {
		t.Fatalf("got %+v, want TimeoutS=120", pols)
	}
}

// TestParsePoliciesOmittedTimeoutIsZero: no timeout= field means TimeoutS
// stays 0 — the runner is the one that turns 0 into DefaultGateTimeout, not
// the parser.
func TestParsePoliciesOmittedTimeoutIsZero(t *testing.T) {
	pols, bad := ParsePolicies("repo=o/r,cmd=true")
	if len(bad) != 0 {
		t.Fatalf("unexpected bad entries: %v", bad)
	}
	if len(pols) != 1 || pols[0].TimeoutS != 0 {
		t.Fatalf("got %+v, want TimeoutS=0", pols)
	}
}

// TestParsePoliciesCmdWithCommaIsPreservedVerbatim is the fix-#2 regression:
// a cmd containing a comma (e.g. "go test -run A,B ./...") must NOT be
// silently truncated at the first comma — that would run a weaker command
// than the operator declared and could manufacture a wrongful "success".
// cmd= must be the LAST field in an entry; everything after "cmd=" is the
// command verbatim, commas included.
func TestParsePoliciesCmdWithCommaIsPreservedVerbatim(t *testing.T) {
	pols, bad := ParsePolicies("repo=o/r,base=main,cmd=go test -run A,B ./...")
	if len(bad) != 0 {
		t.Fatalf("unexpected bad entries: %v", bad)
	}
	if len(pols) != 1 {
		t.Fatalf("got %d policies, want 1: %+v", len(pols), pols)
	}
	got := strings.Join(pols[0].CheckCmd, " ")
	want := "go test -run A,B ./..."
	if got != want {
		t.Fatalf("cmd = %q, want %q (comma must survive, never truncated)", got, want)
	}
}

// TestParsePoliciesCmdMustBeLastEntryHasNoCmdFieldIsBad: an entry with no
// cmd= at all is rejected loudly (bad), never silently accepted with an
// empty/default command — a missing check must never look like a passing
// gate.
func TestParsePoliciesCmdMustBeLastEntryHasNoCmdFieldIsBad(t *testing.T) {
	pols, bad := ParsePolicies("repo=o/r,base=main,net=true")
	if len(pols) != 0 {
		t.Fatalf("expected no policies from an entry with no cmd=, got %+v", pols)
	}
	if len(bad) != 1 {
		t.Fatalf("expected the malformed (cmd-less) entry reported, got %v", bad)
	}
}

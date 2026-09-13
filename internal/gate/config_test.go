// SPDX-License-Identifier: Elastic-2.0

package gate

import (
	"strings"
	"testing"
)

// These tests cover the policy format adopted on 2026-09-13: ONE policy per
// CORRALAI_GATE_POLICY_<NAME> variable, replacing the single ";"-separated
// CORRALAI_GATE_POLICIES. The change was made because three successive guards
// against ";"-in-a-command were each defeated by a cold reviewer, every time
// letting a truncated, weaker command post a wrongful success.
//
// So the headline test is the inverse of the ones it replaces: a semicolon in
// a command must now survive VERBATIM, because nothing is competing for it.

func TestACommandMayContainASemicolon(t *testing.T) {
	for _, cmd := range []string{
		"go vet ./... ; go test ./...",
		"true; env repo=x false",          // round four's defeat of guard three
		"go test -tags=integration ./...", // round three's defeat of guard two
		"make a; make b; make c",
		"sh -c 'a; b'",
	} {
		t.Run(cmd, func(t *testing.T) {
			pol, reason := ParsePolicy("repo=o/r,cmd=" + cmd)
			if reason != "" {
				t.Fatalf("refused a legitimate command: %s", reason)
			}
			if pol.CheckCmd != cmd {
				t.Errorf("CheckCmd = %q, want the command verbatim %q — a truncated command is a WEAKER command that can exit 0 and post success", pol.CheckCmd, cmd)
			}
		})
	}
}

// TestACommandKeepsItsNewlinesAndQuoting is round four's gate R2, which was
// pre-existing: the command was strings.Fields-split and rejoined with spaces
// at two call sites, so "true # comment\nfalse" collapsed onto one line and
// the failing step vanished behind the comment. Quoted arguments containing
// spaces would have been mangled the same way.
func TestACommandKeepsItsNewlinesAndQuoting(t *testing.T) {
	for _, cmd := range []string{
		"true # comment\nfalse",
		"go test -run 'A B' ./...",
		"a\nb\nc",
		`echo "two  spaces"`,
	} {
		pol, reason := ParsePolicy("repo=o/r,cmd=" + cmd)
		if reason != "" {
			t.Fatalf("refused %q: %s", cmd, reason)
		}
		if pol.CheckCmd != cmd {
			t.Errorf("CheckCmd = %q, want %q — rejoining a split command drops newlines, and a commented-out failing step passes", pol.CheckCmd, cmd)
		}
	}
}

func TestParsePolicyFields(t *testing.T) {
	pol, reason := ParsePolicy("repo=o/r,base=main,context=corral/lint,net=true,timeout=900,cmd=go test ./...")
	if reason != "" {
		t.Fatalf("reason = %q, want none", reason)
	}
	if pol.Repo != "o/r" || len(pol.Base) != 1 || pol.Base[0] != "main" {
		t.Errorf("repo/base = %q/%v", pol.Repo, pol.Base)
	}
	if pol.Context != "corral/lint" || !pol.AllowNet || pol.TimeoutS != 900 {
		t.Errorf("context/net/timeout = %q/%v/%d", pol.Context, pol.AllowNet, pol.TimeoutS)
	}
	if pol.CheckCmd != "go test ./..." {
		t.Errorf("CheckCmd = %q", pol.CheckCmd)
	}
}

// TestParsePolicyDefaults: context is deliberately NOT defaulted here. It is
// defaulted by Policy.normalized at the door that acts on the policy, because
// defaulting it in two places is how the forge and the store came to disagree
// about which check had spoken.
func TestParsePolicyDefaults(t *testing.T) {
	pol, reason := ParsePolicy("repo=o/r,cmd=make test")
	if reason != "" {
		t.Fatalf("reason = %q", reason)
	}
	if pol.Base != nil {
		t.Errorf("Base = %v, want nil (all bases)", pol.Base)
	}
	if pol.AllowNet {
		t.Error("AllowNet defaulted true — the fail-closed default is no network")
	}
	if pol.TimeoutS != 0 {
		t.Errorf("TimeoutS = %d, want 0 so the runner applies DefaultGateTimeout", pol.TimeoutS)
	}
	if got := pol.normalized().Context; got != DefaultStatusContext {
		t.Errorf("normalized context = %q, want %q", got, DefaultStatusContext)
	}
}

func TestParsePolicyRefusals(t *testing.T) {
	for _, tc := range []struct{ name, raw, wantSubstr string }{
		{"empty", "", "empty"},
		{"no repo", "cmd=go test ./...", "repo"},
		{"no cmd", "repo=o/r,base=main", "cmd"},
		{"empty cmd", "repo=o/r,cmd=   ", "empty"},
		{"field after cmd", "repo=o/r,cmd=make test,base=release", "after cmd="},
		{"field after cmd, spaced", "repo=o/r,cmd=make test, base=release", "after cmd="},
		{"unknown field", "repo=o/r,mode=fast,cmd=make test", "unknown field"},
		{"timeout not a number", "repo=o/r,timeout=soon,cmd=make test", "not a number"},
		{"timeout negative", "repo=o/r,timeout=-1,cmd=make test", "negative"},
		{"timeout overflows", "repo=o/r,timeout=9223372036854775807,cmd=make test", "exceeds"},
		{"not key=value", "repo=o/r,justaword,cmd=make test", "not key=value"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pol, reason := ParsePolicy(tc.raw)
			if reason == "" {
				t.Fatalf("accepted %q, giving CheckCmd %q", tc.raw, pol.CheckCmd)
			}
			if !strings.Contains(reason, tc.wantSubstr) {
				t.Errorf("reason %q does not mention %q, so the operator cannot act on it", reason, tc.wantSubstr)
			}
		})
	}
}

// TestParsePolicyEnvIsDeterministic: policies decide which check runs against
// a pull request, so they may not arrive in a different order on different
// runs. Ranging a map and taking what comes is the exact non-determinism that
// produced two findings in this repository within a day.
func TestParsePolicyEnvIsDeterministic(t *testing.T) {
	env := []string{
		PolicyEnvPrefix + "ZEBRA=repo=o/z,cmd=z",
		"PATH=/usr/bin",
		PolicyEnvPrefix + "ALPHA=repo=o/a,cmd=a",
		PolicyEnvPrefix + "MIDDLE=repo=o/m,cmd=m",
	}
	for i := 0; i < 20; i++ {
		pol, bad := ParsePolicyEnv(env)
		if len(bad) != 0 {
			t.Fatalf("bad = %v", bad)
		}
		if len(pol) != 3 {
			t.Fatalf("policies = %d, want 3", len(pol))
		}
		if pol[0].Repo != "o/a" || pol[1].Repo != "o/m" || pol[2].Repo != "o/z" {
			t.Fatalf("order = %s, %s, %s — want sorted by variable name", pol[0].Repo, pol[1].Repo, pol[2].Repo)
		}
	}
}

// TestOneBadPolicyDoesNotTakeTheOthersDown — degrade, never block. Under the
// old single-variable format a stray character could take a neighbour with it;
// now each policy is isolated by construction.
func TestOneBadPolicyDoesNotTakeTheOthersDown(t *testing.T) {
	pol, bad := ParsePolicyEnv([]string{
		PolicyEnvPrefix + "GOOD=repo=o/r,cmd=go test ./...",
		PolicyEnvPrefix + "BROKEN=cmd=no repo here",
	})
	if len(pol) != 1 || pol[0].Repo != "o/r" {
		t.Fatalf("policies = %v, want the one good policy to survive", pol)
	}
	if len(bad) != 1 || !strings.Contains(bad[0], "BROKEN") {
		t.Errorf("bad = %v, want it to name the offending variable", bad)
	}
}

// TestLegacyVariableIsRefusedLoudly: the retired format is not parsed and not
// silently ignored either. Silently ignoring it would turn a configured gate
// into no gate at all, which is the failure mode this package exists to
// prevent.
func TestLegacyVariableIsRefusedLoudly(t *testing.T) {
	pol, bad := ParsePolicyEnv([]string{
		LegacyPolicyEnv + "=repo=o/r,cmd=go test ./...",
	})
	if len(pol) != 0 {
		t.Fatalf("parsed %d policies from the retired variable — it must not be honored", len(pol))
	}
	if len(bad) == 0 {
		t.Fatal("the retired variable was ignored SILENTLY; an operator would believe their gate was running")
	}
	if !strings.Contains(bad[0], PolicyEnvPrefix) {
		t.Errorf("the message %q does not name the replacement", bad[0])
	}
}

func TestNoPolicyVariablesMeansFeatureOff(t *testing.T) {
	pol, bad := ParsePolicyEnv([]string{"PATH=/usr/bin", "HOME=/root"})
	if len(pol) != 0 || len(bad) != 0 {
		t.Errorf("policies=%v bad=%v, want both empty (the off switch)", pol, bad)
	}
}

// SPDX-License-Identifier: Elastic-2.0

package gate

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A review of main at 6951ca4c, 2026-09-15 (reviewer claude-code:
// claude-fable-5-1, verifier codex:gpt-6-astra), entry 8be2189163b0 on the
// ledger. All seven findings are here: R1 and R2 (fixed in #347), and R3–R7.

// TestTwoPoliciesCannotShareOneStatus is R1 (high).
//
// THE DEFECT: two policies for one repo that both omitted context= were both
// accepted and both normalized to corral/gate. The poller keys its dedupe on
// (repo, head, context), so the policy whose variable sorted first ran and
// the second was skipped on every head, forever — and the first one's status
// was posted under the shared context, standing in for a check that never
// ran. Two policies that would answer the same pull request under the same
// status are now refused at parse time, loudly, instead of one of them
// disappearing.
func TestTwoPoliciesCannotShareOneStatus(t *testing.T) {
	env := []string{
		"CORRALAI_GATE_POLICY_A=repo=o/r,base=main,cmd=go test ./...",
		"CORRALAI_GATE_POLICY_B=repo=o/r,base=main,cmd=go vet ./...",
	}
	pols, bad := ParsePolicyEnv(env)
	if len(pols) != 1 || pols[0].CheckCmd != "go test ./..." {
		t.Fatalf("policies = %+v: want only A, the first by name", pols)
	}
	if len(bad) != 1 || !strings.Contains(bad[0], "CORRALAI_GATE_POLICY_B") || !strings.Contains(bad[0], "CORRALAI_GATE_POLICY_A") || !strings.Contains(bad[0], "context=") {
		t.Fatalf("bad = %q: want B refused, naming A and telling the operator to set context=", bad)
	}
}

// The collision is a SAME-PULL-REQUEST collision. Policies on one repo whose
// bases cannot both match a pull request, or that report under distinct
// contexts, never share a status and stay legal.
func TestPoliciesThatCannotAnswerTheSamePullRequestAreFine(t *testing.T) {
	for name, env := range map[string][]string{
		"disjoint bases": {
			"CORRALAI_GATE_POLICY_A=repo=o/r,base=main,cmd=true",
			"CORRALAI_GATE_POLICY_B=repo=o/r,base=release,cmd=true",
		},
		"distinct contexts": {
			"CORRALAI_GATE_POLICY_A=repo=o/r,base=main,cmd=true",
			"CORRALAI_GATE_POLICY_B=repo=o/r,base=main,context=corral/vet,cmd=true",
		},
		"different repos": {
			"CORRALAI_GATE_POLICY_A=repo=o/r,cmd=true",
			"CORRALAI_GATE_POLICY_B=repo=o/s,cmd=true",
		},
	} {
		t.Run(name, func(t *testing.T) {
			pols, bad := ParsePolicyEnv(env)
			if len(pols) != 2 || len(bad) != 0 {
				t.Fatalf("policies=%d bad=%q: want both accepted", len(pols), bad)
			}
		})
	}
	// An unset base means ALL bases, so it overlaps any named base.
	_, bad := ParsePolicyEnv([]string{
		"CORRALAI_GATE_POLICY_A=repo=o/r,cmd=true",
		"CORRALAI_GATE_POLICY_B=repo=o/r,base=release,cmd=true",
	})
	if len(bad) != 1 {
		t.Fatalf("bad = %q: a policy on every base and one on release both answer a release PR", bad)
	}
}

// TestASignedVerdictIsRedeliveredNotRecertified is R2.
//
// THE DEFECT: a row whose status never reached the forge was "retried" by
// calling Run again — a new checkout, a new jail run, and a NEW SIGNED
// RECORD on every tick for as long as the post failed. A permanent refusal
// (403 without the statuses scope, 422 on the context) became an unbounded
// re-certify loop. A row that already carries a signed record now has its
// verdict RE-POSTED and nothing re-run. A row with no record (a fail-closed
// run cut short by shutdown, which is what the retry exists for) is still
// re-run, exactly as before.
func TestASignedVerdictIsRedeliveredNotRecertified(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Save(Run{Repo: "o/r", HeadSHA: "abc", PR: 1, Passed: true, RecordID: 42,
		Context: "corral/gate", StatusPosted: false, RanAt: time.Unix(0, 0)}); err != nil {
		t.Fatal(err)
	}

	runs, redelivered := 0, 0
	p := &Poller{
		Policies: []Policy{{Repo: "o/r", Base: []string{"main"}, Context: "corral/gate", CheckCmd: "true"}},
		List:     &fakeLister{prs: []PRRef{{Number: 1, HeadSHA: "abc", Base: "main"}}},
		Store:    store,
		Run: func(ctx context.Context, repoURL string, pol Policy, pr PRRef) error {
			runs++
			return nil
		},
		Redeliver: func(ctx context.Context, repoURL string, pol Policy, pr PRRef, prev Run) error {
			redelivered++
			if prev.RecordID != 42 || !prev.Passed {
				t.Errorf("Redeliver got %+v: want the stored signed verdict", prev)
			}
			return errors.New("forge still refuses") // a permanent 403
		},
	}
	for i := 0; i < 3; i++ {
		_ = p.Tick(context.Background())
	}
	if runs != 0 {
		t.Fatalf("Run called %d times: a signed verdict must never be re-run and re-signed to deliver it", runs)
	}
	if redelivered != 3 {
		t.Fatalf("Redeliver called %d times, want 3 (once per tick while the forge refuses)", redelivered)
	}

	// With no Redeliver wired, a signed verdict is still never re-run.
	p.Redeliver = nil
	_ = p.Tick(context.Background())
	if runs != 0 {
		t.Fatalf("Run called with no Redeliver wired: a signed verdict must never be re-signed")
	}
}

// Runner.Redeliver posts the STORED verdict and marks it delivered; it runs
// nothing and signs nothing.
func TestRunnerRedeliverPostsTheStoredVerdict(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	prev := Run{Repo: "o/r", HeadSHA: "abc", PR: 1, Passed: false, RecordID: 9,
		Context: "corral/gate", StatusPosted: false, RanAt: time.Unix(0, 0)}
	if err := store.Save(prev); err != nil {
		t.Fatal(err)
	}
	status := &fakeStatusPoster{}
	cert := &fakeCertifier{}
	r := &Runner{Status: status, Store: store, Certify: cert,
		RecordURL: func(repo, sha, _ string) string { return "/r/" + sha }, Now: func() time.Time { return time.Unix(1, 0) }}

	if err := r.Redeliver(context.Background(), "https://github.com/o/r", testPolicy(), PRRef{Number: 1, HeadSHA: "abc", Base: "main"}, prev); err != nil {
		t.Fatal(err)
	}
	if len(status.states) != 1 || status.states[0] != "failure" {
		t.Fatalf("posted %v: want exactly the stored verdict, failure", status.states)
	}
	if cert.calls != 0 {
		t.Fatalf("Redeliver signed %d record(s); it must sign none", cert.calls)
	}
	got, ok, err := store.GetByHead("o/r", "abc", "corral/gate")
	if err != nil || !ok || !got.StatusPosted {
		t.Fatalf("row after redelivery = %+v ok=%v err=%v: want StatusPosted", got, ok, err)
	}
}

// R1's rule at the door that ACTS on policies. A Policy built
// programmatically (brain Options.GatePolicies) never passes through
// ParsePolicyEnv, so the poller holds the same rule with the same function:
// the second of two colliding policies is skipped loudly, and the first is
// the one that runs.
func TestThePollerHoldsTheSharedStatusRuleToo(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	var ran []string
	p := &Poller{
		Policies: []Policy{
			{Repo: "o/r", Base: []string{"main"}, CheckCmd: "first"},
			{Repo: "o/r", Base: []string{"main"}, CheckCmd: "second"},
		},
		List:  &fakeLister{prs: []PRRef{{Number: 1, HeadSHA: "abc", Base: "main"}}},
		Store: store,
		Run: func(ctx context.Context, repoURL string, pol Policy, pr PRRef) error {
			ran = append(ran, pol.CheckCmd)
			return nil
		},
	}
	_ = p.Tick(context.Background())
	if strings.Join(ran, ",") != "first" {
		t.Fatalf("ran %v: want only the first of two policies sharing one status", ran)
	}
}

// TestASpaceBeforeCmdIsStillACommand is R3 (low, reproduced).
//
// THE DEFECT: ParsePolicy found the command only by the exact substrings
// "cmd=" and ",cmd=", while every other field — and the stray-field guard —
// tolerated whitespace after the comma. "repo=o/r, cmd=true" was refused as
// "no cmd=", and that repo's gate was off apart from one log line.
func TestASpaceBeforeCmdIsStillACommand(t *testing.T) {
	for _, raw := range []string{
		"repo=o/r, cmd=true",
		"repo=o/r ,cmd=true",
		"repo=o/r,cmd = true",
		"  cmd =true,still the command",
	} {
		pol, reason := ParsePolicy(raw)
		if reason != "" && !strings.Contains(raw, "still the command") {
			t.Errorf("ParsePolicy(%q) refused: %s", raw, reason)
			continue
		}
		if raw == "  cmd =true,still the command" {
			// cmd= first: everything after it is the command, verbatim.
			if reason != "no repo=" {
				t.Errorf("ParsePolicy(%q) = %q, want the no-repo refusal (the command took the rest)", raw, reason)
			}
			continue
		}
		if pol.CheckCmd != "true" || pol.Repo != "o/r" {
			t.Errorf("ParsePolicy(%q) = %+v, want repo o/r and command \"true\"", raw, pol)
		}
	}
	// The FIRST cmd= wins and the rest is verbatim, even when the command
	// itself contains ", cmd=".
	pol, reason := ParsePolicy("repo=o/r, cmd=echo a, cmd=b")
	if reason != "" || pol.CheckCmd != "echo a, cmd=b" {
		t.Fatalf("first cmd= must win: got %+v, %q", pol, reason)
	}
}

// TestNetRefusesAValueItDoesNotUnderstand is R4 (low, code-read).
//
// THE DEFECT: net= mapped every value other than "true" and "1" to
// no-network, silently. timeout= refuses a bad value; net= did not, so an
// operator who wrote net=yes got a gate that failed every network-needing
// check with nothing pointing at the policy.
func TestNetRefusesAValueItDoesNotUnderstand(t *testing.T) {
	for raw, want := range map[string]bool{"true": true, "1": true, "True": true, "TRUE": true, "false": false, "0": false, "False": false} {
		pol, reason := ParsePolicy("repo=o/r,net=" + raw + ",cmd=true")
		if reason != "" || pol.AllowNet != want {
			t.Errorf("net=%s: got AllowNet=%v reason=%q, want %v", raw, pol.AllowNet, reason, want)
		}
	}
	for _, raw := range []string{"yes", "on", "no", "off", "enabled", ""} {
		_, reason := ParsePolicy("repo=o/r,net=" + raw + ",cmd=true")
		if !strings.Contains(reason, "net=") {
			t.Errorf("net=%q was accepted (reason %q); an unrecognized value must be refused, naming net=", raw, reason)
		}
	}
}

// TestAStrayFieldIsCaughtInAnyCaseAndAfterANewline is R6 (low, code-read).
//
// THE DEFECT: the guard matched only a comma followed by a LOWERCASE field
// name, so "cmd=true,Base=release" and "cmd=true\nbase=release" were
// swallowed into the command and the policy gated every base while the
// operator named one — the exact outcome the guard exists to report.
func TestAStrayFieldIsCaughtInAnyCaseAndAfterANewline(t *testing.T) {
	for _, raw := range []string{
		"repo=o/r,cmd=true,Base=release",
		"repo=o/r,cmd=true, BASE = release",
		"repo=o/r,cmd=true\nbase=release",
		"repo=o/r,cmd=true\n  Timeout=5",
	} {
		if _, reason := ParsePolicy(raw); !strings.Contains(reason, "after cmd=") {
			t.Errorf("ParsePolicy(%q) reason = %q, want the stray-field refusal", raw, reason)
		}
	}
	// A field NAME inside a word is not a field: "rebase=" and "basename"
	// must not trip the guard.
	for _, raw := range []string{
		"repo=o/r,cmd=git rebase=x",
		"repo=o/r,cmd=echo basename",
	} {
		if _, reason := ParsePolicy(raw); reason != "" {
			t.Errorf("ParsePolicy(%q) refused (%q); a field name inside a word is not a stray field", raw, reason)
		}
	}
}

// R5 (low, hypothesis — its structure confirmed by the founder's ruling): the
// key migration ran CREATE / INSERT / DROP / RENAME as four separately
// committed statements, so a crash between them left the store in a state the
// next OpenStore could not recover from. It now runs in one transaction, and
// OpenStore repairs either state an interrupted migration from an older
// binary may already have left on disk. Each test builds that state by hand.

// TestAMigrationInterruptedAfterCreateIsRetried: the crash came after
// CREATE gate_runs_wide (and possibly its INSERT), before DROP. The narrow
// table still holds every row; the leftover copy used to make every later
// OpenStore fail on "gate_runs_wide already exists", disabling the gate.
func TestAMigrationInterruptedAfterCreateIsRetried(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "interrupted-create.db")
	legacy, err := openRawLegacyStore(t, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`INSERT INTO gate_runs (repo, head_sha, pr, passed, record_id, ran_at)
		VALUES ('o/r', 'abc', 7, true, 41, TIMESTAMP '2026-09-01 00:00:00')`); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`CREATE TABLE gate_runs_wide (repo VARCHAR, head_sha VARCHAR, context VARCHAR,
		pr INTEGER, passed BOOLEAN, status_posted BOOLEAN, record_id BIGINT, ran_at TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	_ = legacy.Close()

	s, err := OpenStore(dsn)
	if err != nil {
		t.Fatalf("OpenStore after an interrupted migration must recover, got: %v", err)
	}
	defer s.Close()
	run, ok, err := s.GetByHead("o/r", "abc", "")
	if err != nil || !ok || run.RecordID != 41 || run.PR != 7 {
		t.Fatalf("the narrow table's row must survive the retried migration: %+v ok=%v err=%v", run, ok, err)
	}
	assertNoTable(t, s, "gate_runs_wide")
	assertWideKey(t, s)
}

// TestAMigrationInterruptedAfterDropKeepsItsHistory: the crash came after
// DROP gate_runs, before RENAME. Every row lived only in gate_runs_wide, and
// the next OpenStore created a fresh, empty, already-wide gate_runs — so
// migrateKey saw a wide key, returned, and the whole dedupe history sat
// orphaned: every open head re-gated and re-certified. The repair merges the
// orphan back, keeping any row the live table wrote since (it is newer).
func TestAMigrationInterruptedAfterDropKeepsItsHistory(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "interrupted-drop.db")
	s, err := OpenStore(dsn) // the fresh, wide, live table an older binary created
	if err != nil {
		t.Fatal(err)
	}
	// A row the live table wrote AFTER the crash, for a head the orphan also holds.
	if err := s.Save(Run{Repo: "o/r", HeadSHA: "abc", PR: 7, Passed: false, StatusPosted: true, RecordID: 99, RanAt: time.Unix(2000, 0)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TABLE gate_runs_wide (repo VARCHAR NOT NULL, head_sha VARCHAR NOT NULL,
		context VARCHAR NOT NULL DEFAULT 'corral/gate', pr INTEGER NOT NULL, passed BOOLEAN NOT NULL,
		status_posted BOOLEAN NOT NULL DEFAULT FALSE, record_id BIGINT NOT NULL, ran_at TIMESTAMP NOT NULL,
		PRIMARY KEY (repo, head_sha, context))`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO gate_runs_wide VALUES
		('o/r', 'abc', 'corral/gate', 7, true, true, 41, TIMESTAMP '2026-09-01 00:00:00'),
		('o/r', 'def', 'corral/gate', 8, true, true, 42, TIMESTAMP '2026-09-01 00:00:00')`); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	s, err = OpenStore(dsn)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer s.Close()
	if run, ok, _ := s.GetByHead("o/r", "def", ""); !ok || run.RecordID != 42 {
		t.Fatalf("the orphaned history must be merged back: def -> %+v ok=%v", run, ok)
	}
	if run, ok, _ := s.GetByHead("o/r", "abc", ""); !ok || run.RecordID != 99 {
		t.Fatalf("a row the live table wrote since the crash must win over the orphan's: abc -> %+v ok=%v", run, ok)
	}
	assertNoTable(t, s, "gate_runs_wide")
}

func assertNoTable(t *testing.T, s *Store, name string) {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM duckdb_tables() WHERE table_name = ?`, name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("table %s still exists after recovery", name)
	}
}

func assertWideKey(t *testing.T, s *Store) {
	t.Helper()
	var cols string
	if err := s.db.QueryRow(`SELECT list_aggregate(constraint_column_names, 'string_agg', ',')
		FROM duckdb_constraints() WHERE table_name = 'gate_runs' AND constraint_type = 'PRIMARY KEY'`).Scan(&cols); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cols, "context") {
		t.Fatalf("gate_runs key is %q, want it widened to include context", cols)
	}
}

// TestEachStatusLinksToItsOwnCheck is R7's runner half: every status the
// runner posts links to a record URL built WITH that status's context, so the
// link on check A cannot open check B's result.
func TestEachStatusLinksToItsOwnCheck(t *testing.T) {
	status := &fakeStatusPoster{}
	r := newTestRunner(t, &fakeCheckouter{}, &fakeJail{exitCode: 0, output: "ok"}, &fakeCertifier{recordID: 42, head: "h"}, status)
	r.RecordURL = func(repo, sha, statusContext string) string { return "/run/" + sha + "/" + statusContext }
	pol := testPolicy()
	pol.Context = "corral/lint"
	if err := r.Run(context.Background(), "https://github.com/o/r", pol, testPR()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(status.targets) == 0 {
		t.Fatal("no status was posted")
	}
	for i, target := range status.targets {
		if target != "/run/deadbeef/corral/lint" {
			t.Fatalf("status %d (%s) links to %q; it must link to its own check", i, status.states[i], target)
		}
	}
}

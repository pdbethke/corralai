// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/auditpush"
)

// The consent property, door by door: a --models run the operator declines,
// or one with nobody to ask, writes no ledger entry, opens no store for
// write and makes no model call. seatFromPool's own tests prove it returns
// "done"; these prove each door stops on it, so a door that carried on past
// a decline (or wired the flag and ignored it) fails here.

// consentPool names two wired agent seats, each pinned to a model so the
// record can rank them: fakea:m1 is measured as generator and writer, and
// fakec takes the critic by pool order. Every seat is an agent whose script
// logs each call, so "no model call" is read from two files.
const consentPool = "fakea:m1,fakec"

// consentEvidence measures fakea:m1 over six Go runs in both measured seats.
func consentEvidence() rankEvidence {
	runs := []string{"r1", "r2", "r3", "r4", "r5", "r6"}
	obs := genObs("fakea:m1", "go", runs...)
	for _, r := range runs {
		obs = append(obs, writerObs("fakea:m1", "go", r, 5, 5))
	}
	return rankEvidence{Obs: obs, Source: "test evidence (6 scan(s))", RecordSource: "test evidence (6 scan(s))",
		Generations: generationRange{Min: "48", Max: "48", Scans: 6}}
}

// consentAnswer is how the stubbed operator answers.
type consentAnswer struct {
	name                  string
	accepted, interactive bool
}

var consentAnswers = []consentAnswer{
	{"declined", false, true},
	{"no terminal", false, false},
}

// stubConsent wires the doors' seams to consentEvidence and an operator who
// answers as a says, wires both agents, isolates the state directory and the
// machine ledger, and returns the two agents' call logs and the state dir.
func stubConsent(t *testing.T, a consentAnswer) (logs []string, state, machine string) {
	t.Helper()
	asked := new(int)
	orig := doorPoolDeps
	t.Cleanup(func() { doorPoolDeps = orig })
	doorPoolDeps = func(stdout, stderr io.Writer) poolDeps {
		return poolDeps{
			load:    func(rankSource) (rankEvidence, error) { return consentEvidence(), nil },
			confirm: func(string) (bool, bool, error) { *asked++; return a.accepted, a.interactive, nil },
			stdout:  stdout, stderr: stderr,
		}
	}
	origRun := shadowMemberRunnable
	t.Cleanup(func() { shadowMemberRunnable = origRun })
	shadowMemberRunnable = func(agentbackend.ResolvedSeat) error { return nil }
	t.Cleanup(func() {
		if *asked != 1 {
			t.Errorf("the operator was asked %d time(s), want once", *asked)
		}
	})
	noKeys(t)
	clearSeatEnv(t)
	state = t.TempDir()
	t.Setenv("CORRALAI_STATE_DIR", state)
	t.Setenv("CORRALAI_BUGCATCH_DB", "")
	machine = withMachineLedger(t)
	logs = []string{wiredAgent(t, "fakea", "claude-json", claudeJSONReply), wiredAgent(t, "fakec", "claude-json", claudeJSONReply)}
	// fakea is seated pinned to a model (fakea:m1), so its definition must
	// carry the pin, or the door refuses the seat before it could run.
	t.Setenv("CORRALAI_AGENT_FAKEA", filepath.Join(filepath.Dir(logs[0]), "agent-fakea")+" {model:--model}")
	return logs, state, machine
}

// assertNothingHappened is the property: no call logged by either agent, no
// entry in any ledger named, and no DuckDB store anywhere under the state
// directory (the bug-catch store is the one a certify run opens for write).
func assertNothingHappened(t *testing.T, code int, out, errs string, logs []string, state string, ledgers ...string) {
	t.Helper()
	if code != 0 {
		t.Errorf("exit %d, want 0 (a decline and a missing terminal are not failures)\n%s%s", code, out, errs)
	}
	for _, l := range logs {
		if b, err := os.ReadFile(l); err == nil && len(b) > 0 { // #nosec G304 -- test temp path
			t.Errorf("a model call was made (%s):\n%s", l, b)
		}
	}
	for _, dir := range ledgers {
		if _, err := os.Stat(dir); err == nil {
			entries, _ := auditpush.ReadLedgerDir(dir)
			t.Errorf("ledger %s exists with %d entr(ies)", dir, len(entries))
		}
	}
	_ = filepath.WalkDir(state, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".duckdb") {
			t.Errorf("a store was opened under the state directory: %s", p)
		}
		return nil
	})
	if !strings.Contains(out, "seats (from --models "+consentPool+")") {
		t.Errorf("the door never reached the plan, so the test proves nothing:\n%s%s", out, errs)
	}
}

func TestDeclinedOrUnaskedLocalRunDoesNothing(t *testing.T) {
	for _, a := range consentAnswers {
		t.Run(a.name, func(t *testing.T) {
			root, setPath, _ := localEntryFixture(t)
			logs, state, machine := stubConsent(t, a)
			var out, errb bytes.Buffer
			code := runCertifyLocal([]string{
				"--repo-dir", root, "--code", "pkg/a.go", "--goal", "A returns 1",
				"--models", consentPool, "--mutants", setPath, "--quiet", "--", "go", "test", "./...",
			}, &out, &errb)
			assertNothingHappened(t, code, out.String(), errb.String(), logs, state, machine)
		})
	}
}

func TestDeclinedOrUnaskedRepoRunDoesNothing(t *testing.T) {
	for _, a := range consentAnswers {
		t.Run(a.name, func(t *testing.T) {
			root, setPath, goals := telemetryRepoFixture(t)
			logs, state, machine := stubConsent(t, a)
			var out, errb bytes.Buffer
			code := runCertifyRepo([]string{
				"--repo", root, "--models", consentPool,
				"--goals", goals, "--substrate", substrateWorkspace, "--all", "--mutants", setPath,
			}, &out, &errb)
			assertNothingHappened(t, code, out.String(), errb.String(), logs, state, machine, filepath.Join(root, ".corral", "ledger"))
		})
	}
}

// demo reaches certify --local by its argv, so the property must hold
// through the forward too.
func TestDeclinedOrUnaskedDemoDoesNothing(t *testing.T) {
	for _, a := range consentAnswers {
		t.Run(a.name, func(t *testing.T) {
			logs, state, machine := stubConsent(t, a)
			var out, errb bytes.Buffer
			code := runDemo([]string{"--dir", t.TempDir(), "--models", consentPool}, &out, &errb)
			assertNothingHappened(t, code, out.String(), errb.String(), logs, state, machine)
		})
	}
}

// review builds its seats through newReviewerBackend, so "no backend built"
// is read directly there as well as from the ledgers.
func TestDeclinedOrUnaskedReviewDoesNothing(t *testing.T) {
	for _, a := range consentAnswers {
		t.Run(a.name, func(t *testing.T) {
			asked := new(int)
			orig := doorPoolDeps
			t.Cleanup(func() { doorPoolDeps = orig })
			doorPoolDeps = func(stdout, stderr io.Writer) poolDeps {
				return poolDeps{
					load:    func(rankSource) (rankEvidence, error) { return reviewEvidence(), nil },
					confirm: func(string) (bool, bool, error) { *asked++; return a.accepted, a.interactive, nil },
					stdout:  stdout, stderr: stderr,
				}
			}
			origRun := shadowMemberRunnable
			t.Cleanup(func() { shadowMemberRunnable = origRun })
			shadowMemberRunnable = func(agentbackend.ResolvedSeat) error { return nil }
			r := reviewModelsRunWith(t, reviewRunOpts{verifierReply: `{"opinion":"v","refutations":[]}`}, "--models", reviewPool)
			if *asked != 1 {
				t.Errorf("asked %d time(s), want once", *asked)
			}
			if r.code != 0 || len(r.seen) != 0 || len(r.entries) != 0 {
				t.Errorf("exit %d, seats built %v, %d entries; want 0, none, none\n%s%s", r.code, r.seen, len(r.entries), r.out, r.errs)
			}
			if _, err := os.Stat(r.machineLedger); err == nil {
				t.Errorf("the machine ledger %s was written", r.machineLedger)
			}
			if !strings.Contains(r.out, "seats (from --models "+reviewPool+")") {
				t.Errorf("the door never reached the plan:\n%s%s", r.out, r.errs)
			}
		})
	}
}

// Scan time and host load are measured from the moment the operator
// accepted, never from before the question (B1): a run's recorded duration
// is what the run took, not how long someone read the plan.
func TestAcceptedRunTimesFromTheAnswer(t *testing.T) {
	slowAccept := func(t *testing.T) *time.Time {
		t.Helper()
		at := new(time.Time)
		orig := doorPoolDeps
		t.Cleanup(func() { doorPoolDeps = orig })
		doorPoolDeps = func(stdout, stderr io.Writer) poolDeps {
			return poolDeps{
				load: func(rankSource) (rankEvidence, error) { return recordEvidence(), nil },
				confirm: func(string) (bool, bool, error) {
					time.Sleep(1500 * time.Millisecond)
					*at = time.Now()
					return true, true, nil
				},
				stdout: stdout, stderr: stderr,
			}
		}
		origRun := shadowMemberRunnable
		t.Cleanup(func() { shadowMemberRunnable = origRun })
		shadowMemberRunnable = func(agentbackend.ResolvedSeat) error { return nil }
		return at
	}
	check := func(t *testing.T, e auditpush.LedgerEntry, accepted time.Time, finished time.Time) {
		t.Helper()
		s := e.Bundle.Scan
		if s.StartedAt == nil || s.StartedAt.Before(accepted.Truncate(time.Microsecond)) {
			t.Errorf("started_at %v is before the operator accepted at %v", s.StartedAt, accepted)
		}
		if s.TotalMillis == nil {
			t.Fatalf("no total_millis recorded")
		}
		if *s.TotalMillis > finished.Sub(accepted).Milliseconds() {
			t.Errorf("total_millis %d includes the wait (the run after acceptance took at most %dms)", *s.TotalMillis, finished.Sub(accepted).Milliseconds())
		}
	}
	t.Run("certify --local", func(t *testing.T) {
		root, setPath, _ := localEntryFixture(t)
		at := slowAccept(t)
		ml := withMachineLedger(t)
		var out, errb bytes.Buffer
		code := runCertifyLocal([]string{
			"--repo-dir", root, "--code", "pkg/a.go", "--goal", "A returns 1",
			"--models", recordPool, "--mutants", setPath, "--quiet", "--", "go", "test", "./...",
		}, &out, &errb)
		finished := time.Now()
		localRunFinished(t, code, out.String(), errb.String())
		entries := scanEntriesIn(t, ml)
		if len(entries) != 1 {
			t.Fatalf("%d entries", len(entries))
		}
		check(t, entries[0], *at, finished)
	})
	t.Run("certify --repo", func(t *testing.T) {
		root, setPath, goals := telemetryRepoFixture(t)
		at := slowAccept(t)
		machine := withMachineLedger(t)
		noKeys(t)
		t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
		wiredAgent(t, "fakec", "claude-json", claudeJSONReply)
		var out, errb bytes.Buffer
		code := runCertifyRepo([]string{
			"--repo", root, "--models", recordPool,
			"--goals", goals, "--substrate", substrateWorkspace, "--all", "--mutants", setPath,
		}, &out, &errb)
		finished := time.Now()
		if code != 0 {
			t.Fatalf("exit=%d\n%s%s", code, out.String(), errb.String())
		}
		entries, err := auditpush.ReadLedgerDir(machine)
		if err != nil || len(entries) != 1 {
			t.Fatalf("%d entries, %v", len(entries), err)
		}
		check(t, entries[0], *at, finished)
	})
}

// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/modelrank"
	"github.com/pdbethke/corralai/internal/reposcan"
)

// How each seat was chosen reaches the WRITTEN entry at every door that
// writes one. seatFromPool computes it; the bug shape this file guards is
// that value dropped at one of the struct copies between the door and the
// ledger row (AGENTS.md, "The bug shape that keeps recurring"), so each test
// reads the entry back from disk rather than inspecting an intermediate.

// recordPool is the pool the record tests name: a hosted model the evidence
// below ranks in both measured seats, and an agent seat the record cannot
// rank, so the critic is seated by pool order.
const recordPool = "anthropic/claude-sonnet-5,fakec"

const recordSource = "test evidence (6 scan(s))"

// wantSeatedBy is SeatedBy for recordPool over recordEvidence, as the row's
// JSON text (encoding/json sorts the keys).
const wantSeatedBy = `{"mutant-generator":"evidence","test-critic":"order","test-writer":"evidence"}`

// recordEvidence measures claude-sonnet-5 over six Go runs as generator and
// six as writer (five survivors attempted, all proven, in each), so both
// measured seats clear the floor of five.
func recordEvidence() rankEvidence {
	runs := []string{"r1", "r2", "r3", "r4", "r5", "r6"}
	obs := genObs("claude-sonnet-5", "go", runs...)
	for _, r := range runs {
		obs = append(obs, writerObs("claude-sonnet-5", "go", r, 5, 5))
	}
	return rankEvidence{Obs: obs, Source: recordSource, Generations: generationRange{Min: "48", Max: "48", Scans: 6}}
}

// stubDoorPool replaces the doors' production seams (the machine ledger and
// the terminal) with recordEvidence and an operator who accepts, and makes
// every pool member pass its credential check. It returns how many times the
// operator was asked.
func stubDoorPool(t *testing.T) *int {
	t.Helper()
	asked := new(int)
	orig := doorPoolDeps
	t.Cleanup(func() { doorPoolDeps = orig })
	doorPoolDeps = func(stdout, stderr io.Writer) poolDeps {
		return poolDeps{
			load: func(rankSource) (rankEvidence, error) { return recordEvidence(), nil },
			confirm: func(string) (bool, bool, error) {
				*asked++
				return true, true, nil
			},
			stdout: stdout, stderr: stderr,
		}
	}
	origRun := shadowMemberRunnable
	t.Cleanup(func() { shadowMemberRunnable = origRun })
	shadowMemberRunnable = func(agentbackend.ResolvedSeat) error { return nil }
	return asked
}

// verdictSeating decodes seated_by and seat_evidence from a row's verdict
// document, and reports whether either key is present at all.
func verdictSeating(t *testing.T, verdictJSON string) (seatedBy map[string]string, evidence string, present bool) {
	t.Helper()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(verdictJSON), &raw); err != nil {
		t.Fatalf("verdict document does not parse: %v", err)
	}
	_, a := raw["seated_by"]
	_, b := raw["seat_evidence"]
	var doc struct {
		SeatedBy     map[string]string `json:"seated_by"`
		SeatEvidence string            `json:"seat_evidence"`
	}
	_ = json.Unmarshal([]byte(verdictJSON), &doc)
	return doc.SeatedBy, doc.SeatEvidence, a || b
}

func TestLocalEntryRecordsHowEachSeatWasChosen(t *testing.T) {
	root, setPath, _ := localEntryFixture(t)
	asked := stubDoorPool(t)
	ml := withMachineLedger(t)
	args := []string{
		"--repo-dir", root, "--code", "pkg/a.go", "--goal", "A returns 1",
		"--models", recordPool, "--mutants", setPath, "--quiet",
		"--", "go", "test", "./...",
	}
	var out, errb bytes.Buffer
	code := runCertifyLocal(args, &out, &errb)
	localRunFinished(t, code, out.String(), errb.String())
	if *asked != 1 {
		t.Fatalf("the operator was asked %d times, want once", *asked)
	}
	if !strings.Contains(out.String(), "seats (from --models "+recordPool+")") {
		t.Errorf("no plan on stdout:\n%s", out.String())
	}
	entries := scanEntriesIn(t, ml)
	if len(entries) != 1 || len(entries[0].Bundle.Files) != 1 {
		t.Fatalf("want one entry with one file row, got %d entries\n%s%s", len(entries), out.String(), errb.String())
	}
	row := entries[0].Bundle.Files[0]
	if row.SeatedBy != wantSeatedBy {
		t.Errorf("file row SeatedBy = %q, want %q", row.SeatedBy, wantSeatedBy)
	}
	wantEvidence := recordSource + "; language go"
	if row.SeatEvidence != wantEvidence {
		t.Errorf("file row SeatEvidence = %q, want %q", row.SeatEvidence, wantEvidence)
	}
	by, ev, _ := verdictSeating(t, row.VerdictJSON)
	if by[modelrank.SeatTestCritic] != "order" || by[modelrank.SeatTestWriter] != "evidence" || by[modelrank.SeatMutantGenerator] != "evidence" || ev != wantEvidence {
		t.Errorf("verdict document seated_by = %v, seat_evidence = %q", by, ev)
	}
	// The proposed models are the ones that ran, not only the ones recorded.
	var roster map[string]string
	_ = json.Unmarshal([]byte(row.ModelsByRole), &roster)
	if roster[modelrank.SeatTestWriter] != "claude-sonnet-5" || roster[modelrank.SeatTestCritic] != "fakec" {
		t.Errorf("roster = %v, want the proposed writer claude-sonnet-5 and critic fakec", roster)
	}
}

func TestRepoEntryRecordsHowEachSeatWasChosen(t *testing.T) {
	root, setPath, goals := telemetryRepoFixture(t)
	asked := stubDoorPool(t)
	machine := withMachineLedger(t)
	noKeys(t)
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	wiredAgent(t, "fakec", "claude-json", claudeJSONReply)
	args := []string{
		"--repo", root, "--models", recordPool,
		"--goals", goals, "--substrate", substrateWorkspace, "--all", "--mutants", setPath,
	}
	var out, errb bytes.Buffer
	if code := runCertifyRepo(args, &out, &errb); code != 0 {
		t.Fatalf("exit=%d\n%s%s", code, out.String(), errb.String())
	}
	if *asked != 1 {
		t.Fatalf("the operator was asked %d times, want once for the whole scan", *asked)
	}
	wantEvidence := recordSource + "; language go"
	for name, dir := range map[string]string{"repo": filepath.Join(root, ".corral", "ledger"), "machine": machine} {
		entries, err := auditpush.ReadLedgerDir(dir)
		if err != nil || len(entries) != 1 {
			t.Fatalf("%s ledger: %d entries, %v", name, len(entries), err)
		}
		audited := 0
		for _, f := range entries[0].Bundle.Files {
			if f.Disposition != "audited" {
				continue
			}
			audited++
			if f.SeatedBy != wantSeatedBy || f.SeatEvidence != wantEvidence {
				t.Errorf("%s %s row: SeatedBy %q, SeatEvidence %q; want %q, %q", name, f.Path, f.SeatedBy, f.SeatEvidence, wantSeatedBy, wantEvidence)
			}
			by, ev, _ := verdictSeating(t, f.VerdictJSON)
			if by[modelrank.SeatTestCritic] != "order" || by[modelrank.SeatTestWriter] != "evidence" || ev != wantEvidence {
				t.Errorf("%s %s verdict: seated_by %v, seat_evidence %q", name, f.Path, by, ev)
			}
		}
		if audited != 2 {
			t.Fatalf("%s ledger: %d audited rows, want the fixture's two", name, audited)
		}
	}
}

// A --repo scan with no --goals map runs a goal deriver, so the roster names
// it and the row records how it was chosen: by pool order when --models
// proposed it, by the operator when --derive-model named it. The deriver is
// stubbed, so the "anthropic" seat makes no call.
func TestRepoEntryRecordsHowTheDeriverWasChosen(t *testing.T) {
	cases := []struct {
		name  string
		extra []string
		want  string
	}{
		{"proposed by --models", nil, "order"},
		{"named by --derive-model", []string{"--derive-model", "anthropic/claude-sonnet-5"}, "operator"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root, setPath, _ := telemetryRepoFixture(t)
			stubDoorPool(t)
			machine := withMachineLedger(t)
			noKeys(t)
			t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
			wiredAgent(t, "fakec", "claude-json", claudeJSONReply)
			prev := certifyRepoDeriver
			t.Cleanup(func() { certifyRepoDeriver = prev })
			certifyRepoDeriver = func(rs agentbackend.ResolvedSeat) (reposcan.Deriver, error) {
				return newMeteredDeriver(&fakeBackend{reply: "the function returns its documented constant"}, rs.Spec), nil
			}
			args := append([]string{
				"--repo", root, "--models", recordPool, "--no-goal-cache", "--cache-db", filepath.Join(t.TempDir(), "cache.duckdb"),
				"--substrate", substrateWorkspace, "--all", "--mutants", setPath,
			}, c.extra...)
			var out, errb bytes.Buffer
			if code := runCertifyRepo(args, &out, &errb); code != 0 {
				t.Fatalf("exit=%d\n%s%s", code, out.String(), errb.String())
			}
			entries, err := auditpush.ReadLedgerDir(machine)
			if err != nil || len(entries) != 1 {
				t.Fatalf("machine ledger: %d entries, %v", len(entries), err)
			}
			audited := 0
			for _, f := range entries[0].Bundle.Files {
				if f.Disposition != "audited" {
					continue
				}
				audited++
				var roster, by map[string]string
				_ = json.Unmarshal([]byte(f.ModelsByRole), &roster)
				_ = json.Unmarshal([]byte(f.SeatedBy), &by)
				if roster[modelrank.SeatGoalDeriver] != "claude-sonnet-5" {
					t.Errorf("%s: roster %v does not name the deriver claude-sonnet-5", f.Path, roster)
				}
				if by[modelrank.SeatGoalDeriver] != c.want {
					t.Errorf("%s: SeatedBy %v, want goal-deriver=%s", f.Path, by, c.want)
				}
			}
			if audited != 2 {
				t.Fatalf("%d audited rows, want the fixture's two\n%s%s", audited, out.String(), errb.String())
			}
		})
	}
}

// Without --models nothing is recorded: the row and the verdict document are
// shaped exactly as before, and absence reads as "every seat was named by
// flag".
func TestARunWithoutModelsRecordsNoSeating(t *testing.T) {
	t.Run("certify --local", func(t *testing.T) {
		root, setPath, _ := localEntryFixture(t)
		ml := withMachineLedger(t)
		code, out, errs := runLocalEntryFixture(t, root, setPath)
		localRunFinished(t, code, out, errs)
		entries := scanEntriesIn(t, ml)
		if len(entries) != 1 || len(entries[0].Bundle.Files) != 1 {
			t.Fatalf("want one entry with one file row, got %d", len(entries))
		}
		row := entries[0].Bundle.Files[0]
		if row.SeatedBy != "" || row.SeatEvidence != "" {
			t.Errorf("row SeatedBy %q, SeatEvidence %q; want neither", row.SeatedBy, row.SeatEvidence)
		}
		if _, _, present := verdictSeating(t, row.VerdictJSON); present {
			t.Errorf("the verdict document carries a seating key:\n%s", row.VerdictJSON)
		}
	})
	t.Run("certify --repo", func(t *testing.T) {
		root, setPath, goals := telemetryRepoFixture(t)
		machine := withMachineLedger(t)
		code, out, errs := runTelemetryFixture(t, root, setPath, goals)
		if code != 0 {
			t.Fatalf("exit=%d\n%s%s", code, out, errs)
		}
		entries, err := auditpush.ReadLedgerDir(machine)
		if err != nil || len(entries) != 1 {
			t.Fatalf("machine ledger: %d entries, %v", len(entries), err)
		}
		for _, f := range entries[0].Bundle.Files {
			if f.SeatedBy != "" || f.SeatEvidence != "" {
				t.Errorf("%s row: SeatedBy %q, SeatEvidence %q; want neither", f.Path, f.SeatedBy, f.SeatEvidence)
			}
			if f.VerdictJSON == "" {
				continue
			}
			if _, _, present := verdictSeating(t, f.VerdictJSON); present {
				t.Errorf("%s: the verdict document carries a seating key", f.Path)
			}
		}
	})
}

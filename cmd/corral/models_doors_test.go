// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/agentbackend"
)

// What --models does at each door that is not a recorded run: doctor and a
// dry run show the lineup and ask nothing, demo hands the pool to the real
// certify --local, and every door's help says a pool is not a default.

// doctor rehearses a run and spends nothing, so it proposes, prints the plan,
// checks the proposed seats, and never asks: there is nothing to accept. The
// pool seats the goal deriver too (doctor previews certify --repo), and a
// deriver the pool chose must not trigger the one paid probe doctor makes
// for a deriver the operator named.
func TestDoctorModelsPrintsThePlanAndAsksNothing(t *testing.T) {
	asked := stubDoorPool(t)
	noKeys(t)
	clearSeatEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	wiredAgent(t, "fakec", "claude-json", claudeJSONReply)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var out, errb bytes.Buffer
	runDoctor([]string{"--repo", t.TempDir(), "--models", recordPool}, &out, &errb)
	if *asked != 0 {
		t.Errorf("doctor asked the operator %d time(s); it runs nothing, so there is nothing to accept", *asked)
	}
	text := out.String()
	plan := strings.Index(text, "seats (from --models "+recordPool+")")
	note := strings.Index(text, "doctor asks nothing: this is the lineup `certify` would propose")
	if plan < 0 || note < plan {
		t.Fatalf("want the plan, then the doctor note under it:\n%s\nstderr:\n%s", text, errb.String())
	}
	if strings.Contains(text, "spending one derive call") {
		t.Errorf("doctor spent a derive call on a deriver the pool proposed:\n%s", text)
	}
	if !strings.Contains(text, "claude-sonnet-5") {
		t.Errorf("doctor did not check the proposed seats:\n%s", text)
	}
}

// A dry run spends nothing and demands no credential for any seat today, so
// it neither credential-checks the pool's members nor asks: it prints the
// lineup a real run would propose and says that it asked nothing.
func TestRepoDryRunModelsAsksNothingAndChecksNoCredential(t *testing.T) {
	root, _, goals := telemetryRepoFixture(t)
	asked := stubDoorPool(t)
	shadowMemberRunnable = func(agentbackend.ResolvedSeat) error {
		return errors.New("no credential in this test")
	}
	noKeys(t)
	clearSeatEnv(t)
	wiredAgent(t, "fakec", "claude-json", claudeJSONReply)
	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{"--repo", root, "--models", recordPool, "--goals", goals, "--dry-run"}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit=%d\n%s%s", code, out.String(), errb.String())
	}
	if *asked != 0 {
		t.Errorf("a dry run asked the operator %d time(s)", *asked)
	}
	text := out.String()
	plan := strings.Index(text, "seats (from --models "+recordPool+")")
	note := strings.Index(text, "a dry run asks nothing and runs nothing")
	if plan < 0 || note < plan {
		t.Fatalf("want the plan, then the dry-run note under it:\n%s\nstderr:\n%s", text, errb.String())
	}
	if !strings.Contains(text, "language go") {
		t.Errorf("a Go-only scan must rank by Go's evidence:\n%s", text)
	}
}

// certify --local ranks by the audited file's language, so a pool with no
// language to read is refused at the door with the fix, as a shadow pool is.
func TestLocalModelsNeedsALanguage(t *testing.T) {
	stubDoorPool(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.unknownlang"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	code := runCertifyLocal([]string{"--code", filepath.Join(root, "notes.unknownlang"), "--goal", "g", "--models", recordPool, "--", "true"}, &out, &errb)
	if code != 2 || !strings.Contains(errb.String(), "--models ranks evidence for the audited file's language") || !strings.Contains(errb.String(), "pass --lang") {
		t.Fatalf("exit %d, want 2 saying --models needs the language and naming --lang:\n%s", code, errb.String())
	}
}

// A run the command line already refuses is refused before --models asks
// anything: an operator never accepts a lineup for a run that cannot start.
func TestLocalModelsRefusesUsageBeforeAsking(t *testing.T) {
	asked := stubDoorPool(t)
	var out, errb bytes.Buffer
	code := runCertifyLocal([]string{"--lang", "go", "--code", "a.go", "--models", recordPool, "--", "true"}, &out, &errb)
	if code != 2 || !strings.Contains(errb.String(), "--goal is required") {
		t.Fatalf("exit %d, want 2 for the missing --goal:\n%s", code, errb.String())
	}
	if *asked != 0 || strings.Contains(out.String(), "seats (from --models") {
		t.Errorf("asked %d time(s) and printed a plan for a run with no --goal:\n%s", *asked, out.String())
	}
}

// corral demo is the real certify --local, reached by its argv, so the pool
// travels there and certify --local proposes the seats (and records how they
// were chosen). A seat the pool fills is not passed as an empty flag.
func TestDemoForwardsModelsToCertifyLocal(t *testing.T) {
	orig := demoRunLocal
	t.Cleanup(func() { demoRunLocal = orig })
	var got []string
	demoRunLocal = func(args []string, _ bool, _, _ io.Writer) int {
		got = args
		return 0
	}
	var out, errb bytes.Buffer
	if code := runDemo([]string{"--dir", t.TempDir(), "--models", recordPool}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	i := slices.Index(got, "--models")
	if i < 0 || i+1 >= len(got) || got[i+1] != recordPool {
		t.Fatalf("demo did not forward --models %s: %v", recordPool, got)
	}
	for _, f := range []string{"--writer-model", "--mutant-model", "--critic-model"} {
		if slices.Contains(got, f) {
			t.Errorf("demo passed %s with no value to pin: %v", f, got)
		}
	}
	// Still refused when there is neither a pool nor the required seats.
	if code := runDemo([]string{"--dir", t.TempDir()}, &bytes.Buffer{}, &bytes.Buffer{}); code != 2 {
		t.Errorf("demo with no seats and no pool: exit %d, want 2", code)
	}
}

// Every door's --models help says the pool is not a default and that nothing
// runs until the operator accepts, in that door's own terms.
func TestModelsHelpIsTrueAtEachDoor(t *testing.T) {
	doors := []struct {
		name string
		run  func([]string, io.Writer, io.Writer) int
		must []string
	}{
		{"certify --local", runCertifyLocal, []string{"not a default", "accept", "language"}},
		{"certify --repo", runCertifyRepo, []string{"not a default", "accept", "goal-deriver", "--dry-run", "audits nothing and exits 0", "none or several"}},
		{"doctor", runDoctor, []string{"not a default", "accept", "doctor asks nothing and spends nothing on a seat it proposes; a `--derive-model` you name is still probed with one real call"}},
		{"demo", runDemo, []string{"not a default", "accept", "Go"}},
	}
	for _, d := range doors {
		help := flagUsage(t, d.run, "models")
		for _, w := range d.must {
			if !strings.Contains(help, w) {
				t.Errorf("%s -models help lacks %q:\n%s", d.name, w, help)
			}
		}
	}
}

// certify --repo refuses what its flags alone decide before --models asks
// anything (B4): --scope-tests and a malformed --local-endpoint used to be
// refused after the operator had accepted a lineup.
func TestRepoModelsRefusesFlagErrorsBeforeAsking(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"--scope-tests", []string{"--scope-tests"}, "--scope-tests was removed"},
		{"--local-endpoint", []string{"--local-endpoint", "no-such-role=http://localhost:1"}, "unknown role"},
	} {
		t.Run(c.name, func(t *testing.T) {
			root, setPath, goals := telemetryRepoFixture(t)
			asked := stubDoorPool(t)
			var out, errb bytes.Buffer
			code := runCertifyRepo(append([]string{"--repo", root, "--models", recordPool, "--goals", goals,
				"--substrate", substrateWorkspace, "--all", "--mutants", setPath}, c.args...), &out, &errb)
			if code != 2 || !strings.Contains(errb.String(), c.want) {
				t.Fatalf("exit %d, want 2 naming %q:\n%s", code, c.want, errb.String())
			}
			if *asked != 0 || strings.Contains(out.String(), "seats (from --models") {
				t.Errorf("asked %d time(s) and printed a plan for a run its flags refuse:\n%s", *asked, out.String())
			}
		})
	}
}

// The top-level usage names what `corral models rank` reads without --db:
// this machine's ledger, with --source to choose the old scorecard store
// (B14). It said "instead of the local bug-catching ledger" after the default
// had moved.
func TestTopLevelUsageNamesTheRankSources(t *testing.T) {
	u := usageText()
	if strings.Contains(u, "instead of the local\n") || strings.Contains(u, "bug-catching ledger; unreachable") {
		t.Errorf("usage still says --db replaces the local bug-catching ledger")
	}
	for _, want := range []string{"instead of this machine's ledger", "--source ledger|scorecard"} {
		if !strings.Contains(u, want) {
			t.Errorf("usage lacks %q", want)
		}
	}
}

// Under --json a dry run's stdout is the inventory document and nothing
// else; the --models plan and the dry-run note go to stderr (C6).
func TestRepoJSONDryRunModelsKeepsStdoutJSON(t *testing.T) {
	root, _, goals := telemetryRepoFixture(t)
	asked := stubDoorPool(t)
	noKeys(t)
	clearSeatEnv(t)
	wiredAgent(t, "fakec", "claude-json", claudeJSONReply)
	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{"--repo", root, "--models", recordPool, "--goals", goals, "--dry-run", "--json"}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit=%d\n%s%s", code, out.String(), errb.String())
	}
	if *asked != 0 {
		t.Errorf("asked %d time(s)", *asked)
	}
	if !json.Valid(out.Bytes()) || !strings.HasPrefix(strings.TrimSpace(out.String()), "{") {
		t.Errorf("stdout is not exactly one JSON document:\n%s", out.String())
	}
	if strings.Contains(out.String(), "seats (from --models") {
		t.Errorf("the plan reached stdout")
	}
	if !strings.Contains(errb.String(), "seats (from --models "+recordPool+")") || !strings.Contains(errb.String(), "a dry run asks nothing") {
		t.Errorf("the plan and the dry-run note must be on stderr:\n%s", errb.String())
	}
}

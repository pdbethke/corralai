// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
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
	note := strings.Index(text, "doctor runs nothing: this is the lineup `certify` would propose")
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
		{"certify --repo", runCertifyRepo, []string{"not a default", "accept", "goal-deriver", "--dry-run"}},
		{"doctor", runDoctor, []string{"not a default", "accept", "runs nothing"}},
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

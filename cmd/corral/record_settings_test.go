// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/creds"
	"github.com/pdbethke/corralai/internal/sandbox"
)

// TestRedactCommandHidesSecretsInTheCommand: the command is recorded on a
// signed, public ledger entry, so a credential typed after `--` must not
// reach it. A name is secret when it is a credential corral knows by name or
// looks like one; here a false positive only hides a value from a record and
// never breaks a run (unlike ScrubbedEnviron, which strips a suite's own
// environment), so the heuristic is the right side to err on.
func TestRedactCommandHidesSecretsInTheCommand(t *testing.T) {
	canon := creds.CanonicalNames[0]
	own := sandbox.CorralOwnSecrets[0]
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"env assignment", []string{"env", "API_KEY=x", "npm", "test"}, []string{"env", "API_KEY=<redacted>", "npm", "test"}},
		{"flag with equals", []string{"pytest", "--token=abc"}, []string{"pytest", "--token=<redacted>"}},
		{"flag with separate value", []string{"pytest", "--token", "abc"}, []string{"pytest", "--token", "<redacted>"}},
		{"assignment inside an sh -c script", []string{"sh", "-c", "GH_TOKEN=x make test"}, []string{"sh", "-c", "GH_TOKEN=<redacted> make test"}},
		{"flag inside an sh -c script", []string{"sh", "-c", "tool --api-key abc && tool --password=hunter2 run"}, []string{"sh", "-c", "tool --api-key <redacted> && tool --password=<redacted> run"}},
		{"quoted value in a script", []string{"sh", "-c", `DB_PASSWORD="a b c" go test ./...`}, []string{"sh", "-c", `DB_PASSWORD=<redacted> go test ./...`}},
		{"lower case name", []string{"env", "my_secret=x", "go", "test"}, []string{"env", "my_secret=<redacted>", "go", "test"}},
		{"passwd", []string{"env", "DB_PASSWD=x", "go", "test"}, []string{"env", "DB_PASSWD=<redacted>", "go", "test"}},
		{"a canonical credential name", []string{"env", canon + "=x", "go", "test"}, []string{"env", canon + "=<redacted>", "go", "test"}},
		{"a corral secret name", []string{"env", own + "=x", "go", "test"}, []string{"env", own + "=<redacted>", "go", "test"}},
		{"a secret in a url query", []string{"curl", "https://h/p?token=abc&x=1"}, []string{"curl", "https://h/p?token=<redacted>&x=1"}},
		{"a non-secret assignment stays", []string{"env", "CI=1", "go", "test"}, []string{"env", "CI=1", "go", "test"}},
		{"nothing secret", []string{"node", "./node_modules/vitest/vitest.mjs", "run"}, []string{"node", "./node_modules/vitest/vitest.mjs", "run"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			orig := append([]string(nil), c.in...)
			got := redactCommand(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("redactCommand(%q) = %q, want %q", c.in, got, c.want)
			}
			if !reflect.DeepEqual(c.in, orig) {
				t.Errorf("redactCommand mutated its argument: %q", c.in)
			}
			again := redactCommand(got)
			if !reflect.DeepEqual(again, got) {
				t.Errorf("redaction is not idempotent: %q then %q", got, again)
			}
		})
	}
}

// The recorded text is a shell-joined command, with an argument that needs
// quoting quoted, so it reads as the command the operator typed.
func TestTestCommandTextIsShellJoined(t *testing.T) {
	got := testCommandText([]string{"sh", "-c", "GH_TOKEN=x make test"})
	if want := `sh -c 'GH_TOKEN=<redacted> make test'`; got != want {
		t.Errorf("testCommandText = %q, want %q", got, want)
	}
	if got := testCommandText(nil); got != "" {
		t.Errorf("no command recorded as %q", got)
	}
}

// A setting the run did not use is ABSENT, never a zero standing in for it,
// while a boolean flag is recorded with the value the run used.
func TestSettingsOmitWhatTheRunDidNotUse(t *testing.T) {
	no := false
	got := runSettingsJSON(runSettings{Timeout: 90 * time.Minute, NoFailFast: &no})
	var m map[string]any
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatalf("%q: %v", got, err)
	}
	want := map[string]any{"timeout": "1h30m0s", "no_fail_fast": false}
	if !reflect.DeepEqual(m, want) {
		t.Errorf("settings = %v, want %v", m, want)
	}
	if got := runSettingsJSON(runSettings{}); got != "" {
		t.Errorf("a run with no settings records %q", got)
	}
	full := runSettingsJSON(runSettings{
		Timeout: time.Hour, EvidenceTimeout: 60 * time.Minute, SeedLimitBytes: 128 << 20,
		Swarm: 6, ScanWorkers: 6, UnitWorkers: 6, MutantConcurrency: 2, PerFileSwarm: 3, BaselineRuns: 2, MaxTokens: 5000,
		WholeSuite: &no, NoFailFast: &no, NoGoalCache: &no, NoVerdictCache: &no, NoSelectionCache: &no,
		WriterMode: "batched", Jail: "bwrap", RunnerFlag: "vitest",
	})
	if err := json.Unmarshal([]byte(full), &m); err != nil {
		t.Fatal(err)
	}
	if m["seed_limit_mib"] != float64(128) || m["evidence_timeout"] != "1h0m0s" || m["runner_flag"] != "vitest" || m["max_tokens"] != float64(5000) {
		t.Errorf("full settings = %v", m)
	}
	if len(m) != 18 {
		t.Errorf("full settings has %d keys, want 18: %v", len(m), m)
	}
}

// The command, the runner and the settings reach the signed ledger entry
// from a real --repo run, the command redacted.
func TestTheCommandAndSettingsReachTheLedger(t *testing.T) {
	root, setPath, goals := telemetryRepoFixture(t)
	withMachineLedger(t)
	code, out, errs := runTelemetryFixture(t, root, setPath, goals,
		"--evidence-timeout", "60m", "--swarm", "2", "--no-verdict-cache",
		"--", "sh", "-c", "GH_TOKEN=abc123 go test ./...")
	if code != 0 {
		t.Fatalf("exit=%d\n%s%s", code, out, errs)
	}
	entries, err := auditpush.ReadLedgerDir(filepath.Join(root, ".corral", "ledger"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("want one entry, got %d (%v)", len(entries), err)
	}
	sc := entries[0].Bundle.Scan
	if strings.Contains(sc.TestCommand, "abc123") || !strings.Contains(sc.TestCommand, "GH_TOKEN=<redacted>") || !strings.Contains(sc.TestCommand, "go test ./...") {
		t.Errorf("TestCommand = %q", sc.TestCommand)
	}
	if sc.Runner != "go-test" {
		t.Errorf("Runner = %q", sc.Runner)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(sc.Settings), &m); err != nil {
		t.Fatalf("Settings %q: %v", sc.Settings, err)
	}
	if m["evidence_timeout"] != "1h0m0s" || m["swarm"] != float64(2) || m["no_verdict_cache"] != true || m["no_goal_cache"] != false || m["whole_suite"] != false {
		t.Errorf("Settings = %s", sc.Settings)
	}
	if _, ok := m["seed_limit_mib"]; ok {
		t.Errorf("the workspace substrate seeds no jail, yet Settings records a seed limit: %s", sc.Settings)
	}
	if _, ok := m["runner_flag"]; ok {
		t.Errorf("--runner was not given, yet Settings records it: %s", sc.Settings)
	}
}

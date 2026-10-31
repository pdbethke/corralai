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
)

// redactRepo is a repository holding the files the positive controls name.
func redactRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "node_modules", "vitest", "vitest.mjs"), "x")
	mustWrite(t, filepath.Join(root, "tests", "a_test.py"), "x")
	return root
}

// TestRedactCommandHidesSecretsInTheCommand: the command is recorded on a
// signed entry on a public branch, so the recording is an ALLOWLIST (ruling
// R-T5): a token is kept only when provably safe, and a credential reaches
// the record by none of the routes a denylist of names cannot close. Each
// case's secret part is asserted absent from the joined result, not the
// exact output, so a different redaction shape cannot pass by accident.
func TestRedactCommandHidesSecretsInTheCommand(t *testing.T) {
	root := redactRepo(t)
	cases := []struct {
		name   string
		in     []string
		secret string
	}{
		{"credentials in a url", []string{"curl", "https://usr1:s3cr3tpw@host/x"}, "s3cr3tpw"},
		{"credentials in a url assignment", []string{"env", "DATABASE_URL=postgres://u1:s3cr3tpw@h/db", "npm", "test"}, "s3cr3tpw"},
		{"authorization header", []string{"curl", "-H", "Authorization: Bearer s3cr3tpw"}, "s3cr3tpw"},
		{"api key header", []string{"curl", "-H", "X-Api-Key: s3cr3tpw"}, "s3cr3tpw"},
		{"short flag with separate value", []string{"tool", "-t", "s3cr3tpw"}, "s3cr3tpw"},
		{"short flag p with separate value", []string{"mysql", "-p", "s3cr3tpw"}, "s3cr3tpw"},
		{"short flag with attached value", []string{"mysql", "-ps3cr3tpw"}, "s3cr3tpw"},
		{"curl -u", []string{"curl", "-u", "user1:s3cr3tpw"}, "s3cr3tpw"},
		{"--pass", []string{"tool", "--pass=s3cr3tpw"}, "s3cr3tpw"},
		{"DB_PASS", []string{"env", "DB_PASS=s3cr3tpw", "go", "test"}, "s3cr3tpw"},
		{"AUTH", []string{"env", "AUTH=s3cr3tpw", "go", "test"}, "s3cr3tpw"},
		{"escaped quote in a script", []string{"sh", "-c", `PW="s3cr3\"tpw" go test ./...`}, "s3cr3"},
		{"metacharacter in a script", []string{"sh", "-c", "TOKEN=s3cr3&tpw go test ./..."}, "tpw"},
		{"dotted flag name", []string{"tool", "--api.key=s3cr3tpw"}, "s3cr3tpw"},
		{"api key before npm test", []string{"API_KEY=s3cr3tpw", "npm", "test"}, "s3cr3tpw"},
		{"pytest --token=", []string{"pytest", "--token=s3cr3tpw"}, "s3cr3tpw"},
		{"pytest --token value", []string{"pytest", "--token", "s3cr3tpw"}, "s3cr3tpw"},
		{"assignment inside an sh -c script", []string{"sh", "-c", "GH_TOKEN=s3cr3tpw make test"}, "s3cr3tpw"},
		{"a script that cannot be read", []string{"sh", "-c", "tool 's3cr3tpw"}, "s3cr3tpw"},
		{"a backquoted script", []string{"sh", "-c", "tool `echo s3cr3tpw`"}, "s3cr3tpw"},
		{"padded base64 as a separate argument", []string{"pytest", "--token", "dXNlcjpzM2NyM3Rwdw=="}, "dXNlcjpzM2NyM3Rwdw"},
		{"padded base64 value of a flag", []string{"pytest", "--token=dXNlcjpzM2NyM3Rwdw=="}, "dXNlcjpzM2NyM3Rwdw"},
		{"a name with a padded value in a script", []string{"sh", "-c", "tool --k s3cr3tpw== go"}, "s3cr3tpw"},
		{"short flag with attached value containing =", []string{"mysql", "-ps3cr3tpw=x"}, "s3cr3tpw"},
		{"an assignment that is not in assignment position", []string{"pytest", "s3cr3tpw=x"}, "s3cr3tpw"},
		{"dotdot after a flag equals", []string{"pytest", "--token=s3cr3tpw/.."}, "s3cr3tpw"},
		{"dotdot after a flag", []string{"pytest", "--token", "s3cr3tpw/.."}, "s3cr3tpw"},
		{"dotdot inside an existing path", []string{"pytest", "tests/s3cr3tpw/../a_test.py"}, "s3cr3tpw"},
		{"dotdot after a url", []string{"curl", "https://u1:s3cr3tpw@h/../.."}, "s3cr3tpw"},
		{"dotdot after a url assignment", []string{"env", "DATABASE_URL=postgres://u:s3cr3tpw@h/../..", "go", "test"}, "s3cr3tpw"},
		{"a path that does not exist", []string{"pytest", "s3cr3tpw/path"}, "s3cr3tpw"},
		{"a path escaping the repository", []string{"pytest", "../s3cr3tpw"}, "s3cr3tpw"},
		{"an absolute path", []string{"pytest", "/home/s3cr3tpw/x"}, "s3cr3tpw"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			orig := append([]string(nil), c.in...)
			got := redactCommand(c.in, root)
			if len(got) != len(c.in) {
				t.Errorf("redaction changed the shape: %q -> %q", c.in, got)
			}
			if joined := strings.Join(got, " "); strings.Contains(joined, c.secret) {
				t.Errorf("redactCommand(%q) = %q, which carries %q", c.in, got, c.secret)
			}
			if text := testCommandText(c.in, root); strings.Contains(text, c.secret) {
				t.Errorf("testCommandText(%q) = %q, which carries %q", c.in, text, c.secret)
			}
			if !reflect.DeepEqual(c.in, orig) {
				t.Errorf("redactCommand mutated its argument: %q", c.in)
			}
		})
	}
}

// What is provably safe is recorded intact, so the recorded command still
// says what ran.
func TestRedactCommandKeepsWhatIsProvablySafe(t *testing.T) {
	root := redactRepo(t)
	for _, in := range [][]string{
		{"node", "./node_modules/vitest/vitest.mjs", "run"},
		{"python3", "-m", "pytest", "-q", "tests/"},
		{"npm", "test"},
		{"go", "test", "./..."},
		{"go", "test", "-race", "-short", "./tests/..."},
		{"pytest", "--reporter=./tests/a_test.py", "--", "tests"},
	} {
		if got := redactCommand(in, root); !reflect.DeepEqual(got, in) {
			t.Errorf("redactCommand(%q) = %q, want it intact", in, got)
		}
	}
	// A path that does not exist is redacted, in its own position.
	got := redactCommand([]string{"pytest", "secret/path"}, root)
	if want := []string{"pytest", "<redacted>"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	// A script's safe words survive, its unsafe ones do not, and a flag with
	// an attached value keeps its name.
	got = redactCommand([]string{"sh", "-c", "GH_TOKEN=s3cr3t go test ./... && pytest --token=s3cr3t"}, root)
	if want := []string{"sh", "-c", "GH_TOKEN=<redacted> go test ./... ; pytest --token=<redacted>"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	// With no repository root nothing is a path that exists.
	if got := redactCommand([]string{"go", "test", "./..."}, ""); got[2] != "<redacted>" {
		t.Errorf("no root: %q", got)
	}
}

// The recorded text is a shell-joined command, with an argument that needs
// quoting quoted.
func TestTestCommandTextIsShellJoined(t *testing.T) {
	root := redactRepo(t)
	got := testCommandText([]string{"sh", "-c", "GH_TOKEN=s3cr3t go test ./..."}, root)
	if want := `sh -c 'GH_TOKEN=<redacted> go test ./...'`; got != want {
		t.Errorf("testCommandText = %q, want %q", got, want)
	}
	if got := testCommandText(nil, root); got != "" {
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

// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/reposcan"
	"github.com/pdbethke/corralai/internal/scanstore"
)

// plantedPreflightRunner answers every command with out, or with err.
type plantedPreflightRunner struct {
	out string
	err error
}

func (r plantedPreflightRunner) Enumerate(context.Context, map[string]string, []string) (string, error) {
	return r.out, r.err
}

// THE RECORD NEVER QUOTES WHY THE PRE-FLIGHT DID NOT RUN (ruling R-F3).
// The coverage pre-flight's Note is the scan row's PreflightNote, published
// on every ledger entry whether or not source is pushed. It quoted the
// jail's error, the reporter's parse error and the operator's test
// command. Each case drives a real failure path with a secret in the
// command and a host path in the failure, shows the plants reached the
// terminal (Said), and asserts neither reaches PreflightNote nor the
// marshalled scan row.
func TestAPreflightFailureNeverReachesTheScanRow(t *testing.T) {
	const (
		secret = "API_KEY=sk-live-PLANTED"
		host   = "/home/operator/private-checkout/.corral/seed"
	)
	goPlug, _ := lang.ByName("go")
	goCmd := []string{"go", "test", "-ldflags=-X main.key=" + secret, "./..."}
	cases := []struct {
		name, class string
		run         func(t *testing.T) reposcan.CoverageMap
		hostPlanted bool // the case's failure can carry the host path
	}{
		{"the jail could not be prepared", "the jail could not be prepared", func(t *testing.T) reposcan.CoverageMap {
			ex := &localExecutor{repoDir: t.TempDir(), checkArgv: goCmd,
				jailErr: errors.New("prepare " + host + " for `" + strings.Join(goCmd, " ") + "`: permission denied")}
			return ex.runPreflight(context.Background(), []string{"a.go"})
		}, true},
		{"the command has no coverage form", "no coverage instrumentation for the test command", func(t *testing.T) reposcan.CoverageMap {
			ex := &localExecutor{repoDir: t.TempDir(), checkArgv: []string{"make", secret}}
			return ex.runPreflight(context.Background(), []string{"a.go"})
		}, false},
		{"the module path could not be read", "the coverage module path could not be read", func(t *testing.T) reposcan.CoverageMap {
			// No go.mod, in a checkout whose path carries the secret: the
			// error names the path it could not read. The workspace
			// substrate gets past the jail without running anything.
			ex := &localExecutor{repoDir: plantedDir(t, secret), checkArgv: goCmd, substrate: substrateWorkspace}
			return ex.runPreflight(context.Background(), []string{"a.go"})
		}, false},
		{"the run failed", "the jail could not start it", func(t *testing.T) reposcan.CoverageMap {
			return reposcan.Preflight(context.Background(), plantedPreflightRunner{err: errors.New("exec " + host + " " + secret + ": denied")},
				nil, goPlug, goCmd, "example.com/m")
		}, true},
		{"the report is unparseable", "could not be read", func(t *testing.T) reposcan.CoverageMap {
			return reposcan.Preflight(context.Background(), plantedPreflightRunner{out: "mode: set\n" + host + "/" + secret + ".go:1.1,2.2 1 x\n"},
				nil, goPlug, goCmd, "example.com/m")
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cm := tc.run(t)
			if cm.Ran || !strings.Contains(cm.Note, tc.class) {
				t.Fatalf("Note = %q (Ran %v): not the %q refusal this case is for, so it proves nothing", cm.Note, cm.Ran, tc.class)
			}
			// Every case shows its plant reached the terminal on this
			// tree, so the absence below is the fix and not a quiet fixture.
			if !strings.Contains(cm.Said(), secret) || (tc.hostPlanted && !strings.Contains(cm.Said(), host)) {
				t.Fatalf("Said = %q lacks the plants, so this case proves nothing", cm.Said())
			}
			// The scan row as the scan builds it (runCertifyRepo:
			// PreflightNote: preflightResult.Note), through the bundle.
			b := buildBundle(scanstore.Scan{Owner: "o", Repo: "r", Commit: "c", PreflightRan: cm.Ran, PreflightNote: cm.Note}, 0,
				nil, nil, nil, nil, auditpush.Link{}, false, "o/r", "c", "", bundleMeta{})
			row, err := json.Marshal(b.Scan)
			if err != nil {
				t.Fatal(err)
			}
			for _, leak := range []string{secret, "sk-live", host, "/home/operator"} {
				if strings.Contains(b.Scan.PreflightNote, leak) || strings.Contains(string(row), leak) {
					t.Errorf("the scan row carries %q: PreflightNote %q", leak, b.Scan.PreflightNote)
				}
			}
		})
	}
}

// plantedDir is an empty checkout whose absolute path carries planted, the
// shape of an error that names the path it could not read.
func plantedDir(t *testing.T, planted string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), planted)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	return dir
}

// THE UNIT PASS AND THE SELECTION PASS RECORD NO ERROR EITHER (review of
// R-F2). collectUnits' refusals are every unit-graded file's Fallback, and
// collectSelection's is every Python file's; both are published on every
// ledger entry. Each case drives the scan's own function to a refusal with
// the secret in the error, shows the terminal has it (Said), and asserts
// the recorded Note does not.
func TestTheScansEvidenceRefusalsRecordNoError(t *testing.T) {
	const (
		secret = "API_KEY=sk-live-PLANTED"
		host   = "/home/operator/private-checkout/.corral/seed"
	)
	goMod := func(t *testing.T) string {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/m\n\ngo 1.26\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	jailErr := errors.New("prepare " + host + " for `" + secret + "`: permission denied")
	cases := []struct {
		name, class string
		said        func(t *testing.T) (note, said string)
	}{
		{"units: the module path could not be read", "the coverage module path could not be read", func(t *testing.T) (string, string) {
			ex := &localExecutor{repoDir: plantedDir(t, secret), checkArgv: []string{"go", "test", "./..."}, evidenceTimeout: time.Minute, unitWorkers: 1}
			ev := ex.collectUnits(context.Background(), []string{"a.go"})
			return ev.Note, ev.Said()
		}},
		{"units: the jail could not be prepared", "the jail could not be prepared", func(t *testing.T) (string, string) {
			ex := &localExecutor{repoDir: goMod(t), checkArgv: []string{"go", "test", "./..."}, evidenceTimeout: time.Minute, unitWorkers: 1, jailErr: jailErr}
			ev := ex.collectUnits(context.Background(), []string{"a.go"})
			return ev.Note, ev.Said()
		}},
		{"selection: the jail could not be prepared", "the jail could not be prepared", func(t *testing.T) (string, string) {
			ex := &localExecutor{repoDir: t.TempDir(), checkArgv: []string{"pytest", "-q"}, evidenceTimeout: time.Minute, jailErr: jailErr}
			ev := ex.collectSelection(context.Background(), []string{"a.py"})
			return ev.Note, ev.Said()
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			note, said := tc.said(t)
			if !strings.Contains(note, tc.class) {
				t.Fatalf("Note = %q: not the %q refusal this case is for, so it proves nothing", note, tc.class)
			}
			if !strings.Contains(said, secret) {
				t.Fatalf("Said = %q lacks the plant, so this case proves nothing", said)
			}
			for _, leak := range []string{secret, "sk-live", host, "/home/operator"} {
				if strings.Contains(note, leak) {
					t.Errorf("the recorded Note carries %q: %q", leak, note)
				}
			}
		})
	}
}

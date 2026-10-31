// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

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
		said        bool // the case's producer has terminal text to show
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
		{"the run failed", "the jail could not start it", func(t *testing.T) reposcan.CoverageMap {
			return reposcan.Preflight(context.Background(), plantedPreflightRunner{err: errors.New("exec " + host + " " + secret + ": denied")},
				nil, goPlug, goCmd, "example.com/m")
		}, true},
		{"the report is unparseable", "could not be read", func(t *testing.T) reposcan.CoverageMap {
			return reposcan.Preflight(context.Background(), plantedPreflightRunner{out: "mode: " + secret + "\n" + host + ":1.1,2.2 1 x\n"},
				nil, goPlug, goCmd, "example.com/m")
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cm := tc.run(t)
			if cm.Ran || !strings.Contains(cm.Note, tc.class) {
				t.Fatalf("Note = %q (Ran %v): not the %q refusal this case is for, so it proves nothing", cm.Note, cm.Ran, tc.class)
			}
			if tc.said && (!strings.Contains(cm.Said(), secret) || !strings.Contains(cm.Said(), host)) {
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

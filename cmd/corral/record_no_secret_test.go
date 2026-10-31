// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/reposcan"
	"github.com/pdbethke/corralai/internal/sandbox"
)

// plantedVersionRunner answers every version command one way: it never
// returns, fails with an error that quotes the command (as a real "not
// found" does), or prints nothing.
type plantedVersionRunner struct{ mode string }

func (p plantedVersionRunner) Enumerate(ctx context.Context, files map[string]string, cmd []string) (string, error) {
	res, err := p.EnumerateDetailed(ctx, files, cmd)
	return res.Output, err
}

func (p plantedVersionRunner) EnumerateDetailed(ctx context.Context, _ map[string]string, cmd []string) (sandbox.EnumerateResult, error) {
	switch p.mode {
	case "timeout":
		<-ctx.Done()
		return sandbox.EnumerateResult{ExitCode: -1}, ctx.Err()
	case "error":
		return sandbox.EnumerateResult{ExitCode: -1}, errors.New(strings.Join(cmd, " ") + ": not found")
	default:
		return sandbox.EnumerateResult{}, nil
	}
}

// ledgerBytes is every byte the ledger directory holds, each gzipped entry
// decompressed, plus the entries as ReadLedgerDir parses them, marshalled
// whole: a secret anywhere in the record is somewhere in this.
func ledgerBytes(t *testing.T, dir string) []byte {
	t.Helper()
	var all bytes.Buffer
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, rerr := os.ReadFile(p) // #nosec G304 -- test temp path
		if rerr != nil {
			return rerr
		}
		if strings.HasSuffix(p, ".gz") {
			zr, zerr := gzip.NewReader(bytes.NewReader(raw))
			if zerr != nil {
				return zerr
			}
			if raw, rerr = io.ReadAll(zr); rerr != nil {
				return rerr
			}
		}
		all.Write(raw)
		return nil
	})
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	entries, err := auditpush.ReadLedgerDir(dir)
	if err != nil {
		t.Fatalf("parsing %s: %v", dir, err)
	}
	b, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("marshalling the entries: %v", err)
	}
	all.Write(b)
	return all.Bytes()
}

// A secret in the operator's test command never reaches the published
// record, whatever the version read does: a whole `certify --repo` entry,
// read back from the ledger directory, is searched byte by byte. The command
// carries a key in an env assignment and a password in a URL, before the
// runner word, where the version command (row.VersionCmd) keeps them.
func TestASecretInTheTestCommandNeverReachesTheLedgerEntry(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	origSel, origRun, origBound := collectSelectionEvidence, versionScanRunner, toolchainReadBound
	t.Cleanup(func() {
		collectSelectionEvidence, versionScanRunner, toolchainReadBound = origSel, origRun, origBound
	})
	collectSelectionEvidence = func(ctx context.Context, runner coverageRunner, files map[string]string, p lang.Plugin, testCmd []string, sourcePaths []string) reposcan.SelectionEvidence {
		return reposcan.SelectionEvidence{Ran: true, Raw: []byte("evidence")}
	}
	toolchainReadBound = 150 * time.Millisecond
	secrets := []string{"sk-live-PLANTED", "PLANTEDPW"}

	for _, mode := range []string{"timeout", "error", "silent"} {
		t.Run(mode, func(t *testing.T) {
			versionScanRunner = func(*localExecutor, string, lang.Plugin) (coverageRunner, map[string]string, error) {
				return plantedVersionRunner{mode: mode}, map[string]string{}, nil
			}
			root, base, goals := selCacheFixture(t)
			dsn := filepath.Join(t.TempDir(), "scans.duckdb")
			args := selCacheArgs(root, base, goals, dsn,
				"--", "env", "API_KEY=sk-live-PLANTED", "PIP_INDEX_URL=https://u:PLANTEDPW@h", "pytest", "-q")
			var out, errs bytes.Buffer
			runCertifyRepo(args, &out, &errs)

			entries := scanEntriesIn(t, selCacheLedger(dsn))
			if len(entries) != 1 {
				t.Fatalf("ledger holds %d scan entries, want 1\nstdout:\n%s\nstderr:\n%s", len(entries), out.String(), errs.String())
			}
			sc := entries[0].Bundle.Scan
			if sc.Toolchains == "" || !strings.Contains(sc.Toolchains, `"pytest"`) {
				t.Fatalf("Toolchains = %q: the runner's version read this test exercises never ran", sc.Toolchains)
			}
			if !strings.Contains(sc.TestCommand, "pytest") {
				t.Fatalf("TestCommand = %q: the operator's command never reached the record", sc.TestCommand)
			}
			t.Logf("TestCommand %s\nToolchains %s", sc.TestCommand, sc.Toolchains)
			rec := ledgerBytes(t, selCacheLedger(dsn))
			for _, s := range secrets {
				if i := bytes.Index(rec, []byte(s)); i >= 0 {
					lo, hi := max(0, i-160), min(len(rec), i+80)
					t.Errorf("the ledger entry holds %q: ...%s...", s, rec[lo:hi])
				}
			}
		})
	}
}

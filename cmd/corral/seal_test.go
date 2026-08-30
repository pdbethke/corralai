// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/reposcan"
)

// fakeSealReader is the in-memory sealReader test double: no DuckDB, no
// attach, just the rows a real warehouse's corral_seal view would return.
type fakeSealReader struct{ rows []sealRow }

func (f fakeSealReader) SealRows(_ context.Context, repo string) ([]sealRow, error) {
	if strings.TrimSpace(repo) == "" {
		return f.rows, nil
	}
	var out []sealRow
	for _, r := range f.rows {
		if r.Repo == repo {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f fakeSealReader) Close() error { return nil }

// gitRunSeal runs one git command in dir, skipping the test when git itself
// is unusable — the same discipline internal/reposcan's own rank_test.go
// applies, so a host with no git binary skips rather than fails.
func gitRunSeal(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...) // #nosec G204 -- fixed binary, literal test args
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git unusable on this host (%v): %s", err, out)
	}
}

// sealFixtureRepo writes a three-file checkout (each paired with a trivial
// test so reposcan.Enumerate treats it as a candidate, not an exclusion) and
// commits it, returning the checkout dir and the repo name `auditSubject`
// would resolve for it — the SAME resolution `certify --repo --push` uses,
// per the task brief.
func sealFixtureRepo(t *testing.T) (dir, repo string) {
	t.Helper()
	dir = t.TempDir()
	files := map[string]string{
		"live.go":       "package p\n\nfunc Live() int { return 1 }\n",
		"live_test.go":  "package p\n",
		"stale.go":      "package p\n\nfunc Stale() int { return 2 }\n",
		"stale_test.go": "package p\n",
		"never.go":      "package p\n\nfunc Never() int { return 3 }\n",
		"never_test.go": "package p\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	gitRunSeal(t, dir, "init", "-q")
	gitRunSeal(t, dir, "add", ".")
	gitRunSeal(t, dir, "commit", "-q", "-m", "seed", "--no-gpg-sign")

	repo, _, err := auditSubject(dir, reposcan.RepoReport{})
	if err != nil {
		t.Fatalf("auditSubject: %v", err)
	}
	return dir, repo
}

// TestSealMarksLiveStaleAndNever is the task's headline test: a warehouse
// with two audited paths (one whose hash matches the fixture repo's file —
// LIVE, one that does not — STALE) and a fixture repo with a third hot file
// the warehouse has never seen (NEVER AUDITED). Asserts all three states and
// the coverage line.
func TestSealMarksLiveStaleAndNever(t *testing.T) {
	dir, repo := sealFixtureRepo(t)

	liveSHA, err := fileSHA256(filepath.Join(dir, "live.go"))
	if err != nil {
		t.Fatalf("hashing live.go: %v", err)
	}

	ts := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	reader := fakeSealReader{rows: []sealRow{
		{Repo: repo, Path: "live.go", ParentSHA256: liveSHA, KillRate: 0.9, Survivors: 1, ProvenMissed: 1, Trees: 4, TS: ts},
		// stale.go's parent_sha256 is a hash of bytes that do not match the
		// checkout — a real audit graded an EARLIER version of this file.
		{Repo: repo, Path: "stale.go", ParentSHA256: "0000000000000000000000000000000000000000000000000000000000000000", KillRate: 0.5, Survivors: 3, ProvenMissed: 0, Trees: 2, TS: ts},
		// never.go: deliberately no row at all.
		// A different repo's row for one of these SAME paths must not leak
		// in — proof the --repo filter, not a bare path match, decides this.
		{Repo: "someone/else", Path: "live.go", ParentSHA256: "irrelevant", KillRate: 0.1, Survivors: 9, TS: ts},
	}}

	var out, errOut bytes.Buffer
	code := runSeal([]string{"--db", "unused", "--repo", dir, "--top", "3"},
		func(string) (sealReader, error) { return reader, nil }, &out, &errOut)
	if code != 0 {
		t.Fatalf("runSeal exit %d, stderr=%s", code, errOut.String())
	}

	got := out.String()
	for _, want := range []string{
		"live", "live.go",
		"stale (file changed since 2026-08-20 10:00)", "stale.go",
		"never audited", "never.go",
		"coverage: 1 of 3 hot files carry a live verdict (33.3%)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q; got:\n%s", want, got)
		}
	}
	if strings.Contains(got, "someone/else") {
		t.Errorf("a row from a DIFFERENT repo leaked into the table:\n%s", got)
	}
}

// TestSealWithoutRepoJudgesNothing pins the other half: with no --repo, seal
// prints the warehouse's latest verdict per path and explicitly says it is
// making NO live/stale claim, rather than silently omitting the judgement a
// reader might otherwise assume it made.
func TestSealWithoutRepoJudgesNothing(t *testing.T) {
	ts := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	reader := fakeSealReader{rows: []sealRow{
		{Repo: "acme/widget", Path: "a.go", ParentSHA256: "x", KillRate: 0.8, Survivors: 2, ProvenMissed: 1, Trees: 3, TS: ts},
	}}

	var out, errOut bytes.Buffer
	code := runSeal([]string{"--db", "unused"}, func(string) (sealReader, error) { return reader, nil }, &out, &errOut)
	if code != 0 {
		t.Fatalf("runSeal exit %d, stderr=%s", code, errOut.String())
	}

	got := out.String()
	if !strings.Contains(got, "NO live/stale judgement") {
		t.Errorf("output does not disclose the missing judgement:\n%s", got)
	}
	if strings.Contains(got, "STATE") || strings.Contains(got, "never audited") {
		t.Errorf("a state verdict leaked in with no --repo given:\n%s", got)
	}
	for _, want := range []string{"acme/widget", "a.go", "0.80"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q; got:\n%s", want, got)
		}
	}
}

// TestSealJSONWithRepo pins the --json shape for the --repo path: snake_case
// keys, and a never-audited row carrying null numbers rather than zeros that
// would misreport "audited, scored 0" as a real measurement.
func TestSealJSONWithRepo(t *testing.T) {
	dir, repo := sealFixtureRepo(t)
	liveSHA, err := fileSHA256(filepath.Join(dir, "live.go"))
	if err != nil {
		t.Fatal(err)
	}
	ts := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	reader := fakeSealReader{rows: []sealRow{
		{Repo: repo, Path: "live.go", ParentSHA256: liveSHA, KillRate: 0.9, Survivors: 1, TS: ts},
	}}

	var out, errOut bytes.Buffer
	code := runSeal([]string{"--db", "unused", "--repo", dir, "--top", "3", "--json"},
		func(string) (sealReader, error) { return reader, nil }, &out, &errOut)
	if code != 0 {
		t.Fatalf("runSeal exit %d, stderr=%s", code, errOut.String())
	}
	for _, want := range []string{`"state": "live"`, `"state": "never audited"`, `"kill_rate": null`, `"path": "never.go"`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("json output missing %q; got:\n%s", want, out.String())
		}
	}
}

// TestOpenSealDBCreatesViewAndReads exercises the REAL production path —
// openSealDB against an actual DuckDB file — rather than the fakeSealReader
// every other test in this file uses: a warehouse `certify --repo --push`
// wrote (which never created corral_seal itself; PushBundle does that, but
// this proves the reader can ALSO create it when a push predates the view,
// or when a warehouse holds corral_audits from some other writer entirely),
// read back read-only, with the row that IS the point — a live verdict.
func TestOpenSealDBCreatesViewAndReads(t *testing.T) {
	target := filepath.Join(t.TempDir(), "warehouse.duckdb")
	bundle := auditpush.Bundle{
		Scan: auditpush.ScanRow{Repo: "o/r", Commit: "abc123", Candidates: 1, Audited: 1},
		Files: []auditpush.Row{
			{Repo: "o/r", Commit: "abc123", Path: "pkg/a.go", Lang: "go",
				Disposition: "audited", KillRate: func() *float64 { v := 0.75; return &v }(),
				Survivors: 2, ProvenMissed: 1, ParentSHA256: "aaaa", Evidence: "proven"},
		},
	}
	if _, err := auditpush.PushBundle(target, bundle); err != nil {
		t.Fatalf("PushBundle: %v", err)
	}

	st, err := openSealDB(target)
	if err != nil {
		t.Fatalf("openSealDB: %v", err)
	}
	defer st.Close()

	rows, err := st.SealRows(context.Background(), "o/r")
	if err != nil {
		t.Fatalf("SealRows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d row(s), want 1: %+v", len(rows), rows)
	}
	if rows[0].Path != "pkg/a.go" || rows[0].ParentSHA256 != "aaaa" || rows[0].KillRate != 0.75 {
		t.Errorf("row = %+v, want the pushed audit", rows[0])
	}

	// Re-opening a target whose view ALREADY exists must take the read-only
	// path without error — the fallback-to-writable branch is for a MISSING
	// view only.
	st2, err := openSealDB(target)
	if err != nil {
		t.Fatalf("second openSealDB: %v", err)
	}
	defer st2.Close()
	if rows2, err := st2.SealRows(context.Background(), "o/r"); err != nil || len(rows2) != 1 {
		t.Errorf("second open: rows=%v err=%v", rows2, err)
	}
}

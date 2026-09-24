// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/review"
)

// recheckFixture is a git checkout with one committed file and a ledger
// holding one review whose findings carry the given scripts.
func recheckFixture(t *testing.T, scripts map[string]string) (repo, ledger, hash string) {
	t.Helper()
	return recheckFixtureCommit(t, scripts, nil)
}

// recheckFixtureCommit is recheckFixture with the review's recorded Commit
// chosen by commitOf (nil: the fixture repo's HEAD).
func recheckFixtureCommit(t *testing.T, scripts map[string]string, commitOf func(repo string) string) (repo, ledger, hash string) {
	t.Helper()
	t.Setenv("CORRALAI_CERTIFY_KEY_FILE", filepath.Join(t.TempDir(), "certify_key"))
	repo = t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "marker.txt"), []byte("here\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"add", "marker.txt"},
		{"-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-q", "-m", "fixture"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	ledger = filepath.Join(t.TempDir(), "ledger")
	ids := make([]string, 0, len(scripts))
	for id := range scripts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	commit := gitHeadCommit(repo)
	if commitOf != nil {
		commit = commitOf(repo)
	}
	r := review.Review{Repo: "acme/r", Commit: commit, Scope: "pkg", ReviewerModel: "rev", Opinion: "o"}
	zero := 0
	for _, id := range ids {
		r.Findings = append(r.Findings, review.Finding{ID: id, Claim: "c " + id, Declared: review.TierReproduced, Tier: review.TierReproduced, Script: scripts[id], ExitCode: &zero})
	}
	signer, _ := ledgerSignerFromLocalKey()
	if _, err := auditpush.WriteReview(ledger, r, signer); err != nil {
		t.Fatal(err)
	}
	entries, err := auditpush.ReadLedgerDir(ledger)
	if err != nil {
		t.Fatal(err)
	}
	return repo, ledger, entries[len(entries)-1].Hash[:12]
}

func dirDigest(t *testing.T, dir string) string {
	t.Helper()
	h := sha256.New()
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		h.Write([]byte(p))
		h.Write(b)
		return nil
	})
	return string(h.Sum(nil))
}

func TestReviewRecheckReportsTheThreeOutcomesAndWritesNothing(t *testing.T) {
	repo, ledger, hash := recheckFixture(t, map[string]string{
		"R1": "test -f marker.txt",         // still true on HEAD
		"R2": "test -f gone.txt",           // no longer true
		"R3": "no-such-command-corral-xyz", // exit 127: a missing tool, not a result
		"R4": "sleep 5",                    // exceeds --timeout
		"R5": "kill -9 $$",                 // killed by a signal, never exits normally
	})
	before := dirDigest(t, ledger)
	for _, tc := range []struct {
		id, want string
		code     int
		extra    []string
	}{
		{"R1", recheckStill, 0, nil},
		{"R2", recheckGone, 0, nil},
		{"R3", recheckUnrun, 3, nil},
		{"R4", recheckUnrun, 3, []string{"--timeout", "1s"}},
		{"R5", recheckUnrun, 3, nil},
	} {
		t.Run(tc.id, func(t *testing.T) {
			var out, errb bytes.Buffer
			args := append([]string{"recheck", ledger, hash + "#" + tc.id, "--repo", repo, "--json"}, tc.extra...)
			code := runReview(args, &out, &errb)
			if code != tc.code {
				t.Fatalf("exit %d, want %d; stderr: %s", code, tc.code, errb.String())
			}
			var res recheckResult
			if err := json.Unmarshal(out.Bytes(), &res); err != nil {
				t.Fatalf("not JSON: %v: %s", err, out.String())
			}
			if res.Outcome != tc.want {
				t.Fatalf("outcome %q, want %q (%+v)", res.Outcome, tc.want, res)
			}
			if res.Commit != gitHeadCommit(repo) {
				t.Errorf("commit %q, want HEAD", res.Commit)
			}
		})
	}
	if dirDigest(t, ledger) != before {
		t.Fatal("recheck changed the ledger directory")
	}
}

func TestReviewRecheckRefusesAFindingWithNoScript(t *testing.T) {
	repo, ledger, hash := recheckFixture(t, map[string]string{"R1": ""})
	var out, errb bytes.Buffer
	if code := runReview([]string{"recheck", ledger, hash + "#R1", "--repo", repo}, &out, &errb); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !bytes.Contains(errb.Bytes(), []byte("no script")) {
		t.Errorf("stderr should say there is no script: %s", errb.String())
	}
}

// gitRepoWithMarker is an unrelated git checkout that also has a committed
// marker.txt, so `test -f marker.txt` would pass in it.
func gitRepoWithMarker(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "other.txt"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"add", "other.txt"},
		{"-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-q", "-m", "unrelated"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return dir
}

// A checkout that does not contain the reviewed commit is not "the fix": the
// script ran against unrelated code, so its exit says nothing about the
// finding. Before this check, an unrelated repo reported no-longer-reproduces.
func TestReviewRecheckRefusesACheckoutThatDoesNotDescendFromTheReviewedCommit(t *testing.T) {
	_, ledger, hash := recheckFixture(t, map[string]string{"R1": "test -f marker.txt"})
	other := gitRepoWithMarker(t)
	var out, errb bytes.Buffer
	code := runReview([]string{"recheck", ledger, hash + "#R1", "--repo", other, "--json"}, &out, &errb)
	if code != 3 {
		t.Fatalf("exit %d, want 3; stdout: %s stderr: %s", code, out.String(), errb.String())
	}
	var res recheckResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("not JSON: %v: %s", err, out.String())
	}
	if res.Outcome != recheckUnrun {
		t.Fatalf("outcome %q, want %q (%+v)", res.Outcome, recheckUnrun, res)
	}
	if !strings.Contains(res.Reason, "does not descend") {
		t.Errorf("reason should say HEAD does not descend from the reviewed commit: %q", res.Reason)
	}
}

// auditpush.WriteReview refuses a review with no commit, so an empty Commit
// cannot reach recheck through a well-formed ledger; the guard is defensive,
// and is tested at the helper.
func TestRecheckAncestryRefusesAnEmptyReviewedCommit(t *testing.T) {
	repo, _, _ := recheckFixture(t, map[string]string{"R1": "true"})
	if reason := recheckAncestry(repo, ""); !strings.Contains(reason, "no reviewed commit") {
		t.Fatalf("want a reason naming the missing commit, got %q", reason)
	}
	if reason := recheckAncestry(repo, gitHeadCommit(repo)); reason != "" {
		t.Fatalf("HEAD descends from itself; got %q", reason)
	}
}

// An interrupt during a recheck must unwind: the script is stopped, the
// outcome is could-not-run (a canceled run measured nothing), and the
// disposable worktree is removed rather than left registered in the
// operator's repo. Before the recheck took a signal-aware context, the first
// SIGTERM killed the process outright — here, the test binary itself.
func TestReviewRecheckUnwindsOnInterrupt(t *testing.T) {
	started := filepath.Join(t.TempDir(), "started")
	repo, ledger, hash := recheckFixture(t, map[string]string{"R1": "touch " + started + " && exec sleep 30"})
	type result struct {
		code int
		out  string
	}
	done := make(chan result, 1)
	go func() {
		var out, errb bytes.Buffer
		code := runReview([]string{"recheck", ledger, hash + "#R1", "--repo", repo, "--json", "--timeout", "40s"}, &out, &errb)
		done <- result{code, out.String()}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the script never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	var r result
	select {
	case r = <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the recheck did not unwind after the interrupt")
	}
	if r.code != 3 {
		t.Fatalf("exit %d, want 3: %s", r.code, r.out)
	}
	var res recheckResult
	if err := json.Unmarshal([]byte(r.out), &res); err != nil {
		t.Fatalf("not JSON: %v: %s", err, r.out)
	}
	if res.Outcome != recheckUnrun {
		t.Fatalf("outcome %q, want %q", res.Outcome, recheckUnrun)
	}
	list, err := exec.Command("git", "-C", repo, "worktree", "list", "--porcelain").Output()
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(list), "worktree "); n != 1 {
		t.Fatalf("%d worktrees registered after the interrupt, want only the main checkout:\n%s", n, list)
	}
}

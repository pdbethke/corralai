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
	"testing"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/review"
)

// recheckFixture is a git checkout with one committed file and a ledger
// holding one review whose findings carry the given scripts.
func recheckFixture(t *testing.T, scripts map[string]string) (repo, ledger, hash string) {
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
	r := review.Review{Repo: "acme/r", Commit: gitHeadCommit(repo), Scope: "pkg", ReviewerModel: "rev", Opinion: "o"}
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

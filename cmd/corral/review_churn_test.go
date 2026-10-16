// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/review"
)

// review churn end to end, over a real history and real ledger entries:
// round one looks at pkg for the first time; a "fix" rewrites line 3; round
// two finds the old defect on line 1 again (drain) and a new one on the line
// the fix wrote (churn). Nothing is spent and nothing is written.
func TestReviewChurnCountsDrainAndChurnFromTheRecord(t *testing.T) {
	t.Setenv("CORRALAI_CERTIFY_KEY_FILE", filepath.Join(t.TempDir(), "certify_key"))
	repo := t.TempDir()
	git := gitCmd(t, repo)
	mustWrite(t, filepath.Join(repo, "pkg", "a.go"), "package pkg\nfunc A() int { return 1 }\nfunc B() int { return 2 }\n")
	git("init", "-q")
	git("add", ".")
	git("-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-q", "-m", "one", "--no-gpg-sign")
	c1 := gitHeadCommit(repo)

	ledger := filepath.Join(t.TempDir(), "ledger")
	signer, _, _ := ledgerSignerFromLocalKey()
	zero := 0
	finding := func(id string, line int) review.Finding {
		return review.Finding{ID: id, Claim: "c", Declared: review.TierReproduced, Tier: review.TierReproduced, File: "pkg/a.go", Line: line, Script: "true", ExitCode: &zero}
	}
	write := func(commit string, fs ...review.Finding) {
		r := review.Review{Repo: "acme/r", Commit: commit, Scope: "pkg", ReviewerModel: "rev", Opinion: "o", Findings: fs}
		if _, err := auditpush.WriteReview(ledger, r, signer); err != nil {
			t.Fatal(err)
		}
	}
	write(c1, finding("R1", 2))

	mustWrite(t, filepath.Join(repo, "pkg", "a.go"), "package pkg\nfunc A() int { return 1 }\nfunc B() int { return -2 }\n")
	git("-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-q", "-am", "fix", "--no-gpg-sign")
	c2 := gitHeadCommit(repo)
	write(c2, finding("R1", 2), finding("R2", 3))

	var out, errb bytes.Buffer
	if code := runReview([]string{"churn", "--repo", repo, "--ledger", ledger, "--json"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	var rounds []review.Round
	if err := json.Unmarshal(out.Bytes(), &rounds); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out.String())
	}
	if len(rounds) != 2 || rounds[0].FirstLook != 1 || rounds[1].Drain != 1 || rounds[1].Churn != 1 || rounds[1].Since != c1 {
		t.Fatalf("rounds: %+v", rounds)
	}
	if rounds[1].Findings[1].Blamed != c2 {
		t.Errorf("the churn finding should name the fix commit that wrote its line: %+v", rounds[1].Findings[1])
	}

	out.Reset()
	if code := runReview([]string{"churn", "--repo", repo, "--ledger", ledger}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	for _, want := range []string{"DRAIN", "CHURN", "first look", "total: 1 drain, 1 churn"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the table lacks %q:\n%s", want, out.String())
		}
	}

	out.Reset()
	if code := runReview([]string{"churn", "--repo", repo, "--ledger", ledger, "--findings"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "pkg/a.go:3") || !strings.Contains(out.String(), "churn (line by "+shortSHA(c2)+")") {
		t.Errorf("--findings does not name the commit that wrote the churned line:\n%s", out.String())
	}
}

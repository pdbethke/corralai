// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// reviewInvocations runs the Run step with a stub corral that appends every
// invocation's argv (NUL-delimited, one line per call) to a log, and returns
// the argv of each `review` call, the combined output plus the step summary,
// and the step's error.
func reviewInvocations(t *testing.T, tmp string, extraEnv ...string) (reviews [][]string, out string, runErr error) {
	t.Helper()
	all := filepath.Join(tmp, "all.log")
	summary := filepath.Join(tmp, "summary.md")
	_ = os.Remove(all) // one log per invocation of the step, even in a reused tmp
	_ = os.Remove(summary)
	tail := "printf '%s\\0' \"$@\" >> \"" + all + "\"; printf '\\n' >> \"" + all + "\"\n" +
		"case \"$1\" in review) echo \"review of $4: findings: 1 reproduced\"; exit \"${STUB_REVIEW_EXIT:-0}\";; esac\n" +
		"echo 'audit report'\n"
	runStep := findStepContaining(t, loadActionYAML(t), "corral \"${args[@]}\"")
	env := append([]string{"GITHUB_STEP_SUMMARY=" + summary, "LEDGER_DIR=", "REVIEW_SCOPE=", "REVIEW_MAX_SCOPES=", "REVIEW_FAIL_ON=", "VERIFIER_MODEL=", "REVIEWER_MODEL="}, extraEnv...)
	o, err, _ := runRunCorralStepWithStub(t, runStep, tmp, "go test ./...", tail, env...)
	b, _ := os.ReadFile(all)
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		line = strings.TrimSuffix(line, "\x00")
		if line == "" {
			continue
		}
		argv := strings.Split(line, "\x00")
		if argv[0] == "review" {
			reviews = append(reviews, argv)
		}
	}
	sb, _ := os.ReadFile(summary)
	return reviews, string(o) + "\n" + string(sb), err
}

// TestActionReviewRunsOnTheChange: with reviewer-model set, the step runs
// `corral review` after the audit — one review per changed top-level
// directory when review-scope is empty, with the ledger and the gate the
// inputs name — mirrors each review into the step summary, and the step's
// status is the review's when the audit passed.
func TestActionReviewRunsOnTheChange(t *testing.T) {
	tmp := t.TempDir()
	// An explicit scope, a verifier, a ledger, the default gate.
	reviews, out, err := reviewInvocations(t, tmp, "REVIEWER_MODEL=gemini-3.6-flash", "VERIFIER_MODEL=claude-haiku-4-5", "REVIEW_SCOPE=internal/x", "LEDGER_DIR=ledger")
	if err != nil {
		t.Fatalf("step failed: %v\n%s", err, out)
	}
	if len(reviews) != 1 {
		t.Fatalf("one review for an explicit scope, got %d\n%s", len(reviews), out)
	}
	got := strings.Join(reviews[0], " ")
	for _, want := range []string{"--scope internal/x", "--reviewer-model gemini-3.6-flash", "--verifier-model claude-haiku-4-5", "--ledger ledger", "--fail-on reproduced"} {
		if !strings.Contains(got, want) {
			t.Errorf("review argv lacks %q: %s", want, got)
		}
	}
	if !strings.Contains(out, "## Corral — review of internal/x") || !strings.Contains(out, "## Corral — adversarial test audit") {
		t.Errorf("both reports must reach the step summary:\n%s", out)
	}

	// review-fail-on never: no gate flag; no ledger: --no-ledger.
	reviews, out, err = reviewInvocations(t, t.TempDir(), "REVIEWER_MODEL=gemini-3.6-flash", "REVIEW_SCOPE=pkg", "REVIEW_FAIL_ON=never")
	if err != nil || len(reviews) != 1 || strings.Contains(strings.Join(reviews[0], " "), "--fail-on") || !strings.Contains(strings.Join(reviews[0], " "), "--no-ledger") {
		t.Fatalf("never → no --fail-on; no ledger → --no-ledger: %v %v\n%s", reviews, err, out)
	}

	// The review's status is the step's when the audit passed.
	_, out, err = reviewInvocations(t, t.TempDir(), "REVIEWER_MODEL=gemini-3.6-flash", "REVIEW_SCOPE=pkg", "STUB_REVIEW_EXIT=3")
	if err == nil {
		t.Fatalf("a review that exits 3 must fail the step:\n%s", out)
	}

	// An agentic seat is refused by name, before any review runs.
	reviews, out, err = reviewInvocations(t, t.TempDir(), "REVIEWER_MODEL=claude-code:claude-sonnet-5", "REVIEW_SCOPE=pkg")
	if err == nil || len(reviews) != 0 || !strings.Contains(out, "agentic seat") {
		t.Fatalf("agentic seats cannot run on a hosted runner: reviews=%v err=%v\n%s", reviews, err, out)
	}

	// Empty scope: the change itself. A workspace with a base commit and a
	// head that touches two directories and a root file → two reviews,
	// the root file not counted as the whole repository.
	tmp = t.TempDir()
	ws := filepath.Join(tmp, "workspace")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	mustGit(t, ws, "init", "-q")
	mustWrite(t, filepath.Join(ws, "internal", "x", "a.go"), "package x\n")
	mustGit(t, ws, "add", ".")
	mustGit(t, ws, "commit", "-q", "-m", "base", "--no-gpg-sign")
	base := strings.TrimSpace(mustGit(t, ws, "rev-parse", "HEAD"))
	mustWrite(t, filepath.Join(ws, "internal", "x", "a.go"), "package x // changed\n")
	mustWrite(t, filepath.Join(ws, "cmd", "y", "main.go"), "package main\n")
	mustWrite(t, filepath.Join(ws, "README.md"), "hi\n")
	mustGit(t, ws, "add", ".")
	mustGit(t, ws, "commit", "-q", "-m", "change", "--no-gpg-sign")
	reviews, out, err = reviewInvocations(t, tmp, "REVIEWER_MODEL=gemini-3.6-flash", "DIFF_BASE="+base)
	if err != nil {
		t.Fatalf("step failed: %v\n%s", err, out)
	}
	var scopes []string
	for _, r := range reviews {
		for i, a := range r {
			if a == "--scope" {
				scopes = append(scopes, r[i+1])
			}
		}
	}
	sort.Strings(scopes)
	if strings.Join(scopes, ",") != "cmd,internal" {
		t.Fatalf("the change's top-level directories, root files not counted beside them: %v\n%s", scopes, out)
	}
	// Only a root file changed → the whole repository, once.
	mustWrite(t, filepath.Join(ws, "README.md"), "hi again\n")
	mustGit(t, ws, "add", ".")
	mustGit(t, ws, "commit", "-q", "-m", "root only", "--no-gpg-sign")
	base = strings.TrimSpace(mustGit(t, ws, "rev-parse", "HEAD~1"))
	reviews, out, err = reviewInvocations(t, tmp, "REVIEWER_MODEL=gemini-3.6-flash", "DIFF_BASE="+base)
	if err != nil || len(reviews) != 1 || !strings.Contains(strings.Join(reviews[0], " "), "--scope .") {
		t.Fatalf("a root-only change is the whole repository, once: %v %v\n%s", reviews, err, out)
	}
	// Too many directories → refused, asking for a scope.
	first := strings.TrimSpace(mustGit(t, ws, "rev-list", "--max-parents=0", "HEAD"))
	_, out, err = reviewInvocations(t, tmp, "REVIEWER_MODEL=gemini-3.6-flash", "DIFF_BASE="+first, "REVIEW_MAX_SCOPES=1")
	if err == nil || !strings.Contains(out, "review-max-scopes") {
		t.Fatalf("more directories than review-max-scopes must refuse by name: %v\n%s", err, out)
	}
}

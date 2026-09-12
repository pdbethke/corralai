// SPDX-License-Identifier: Elastic-2.0

package gate

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Round three of the cold review, 2026-09-12 (reviewer antigravity, verifier
// codex). Every finding here is a defect in a FIX I wrote hours earlier for
// round two, and four of the five are the identical shape: a rule applied at
// one door and not the others, or a guard keyed on something incidental
// instead of on the declared schema.
//
// Three were high severity. The inputs below are the reviewer's own, verbatim
// where it supplied them, because a test written from my paraphrase of a
// finding is a test written by the person who missed it.

// TestSemicolonGuardIsNotDefeatedByAnEqualsSign is round three's R1.
//
// MY BUG: the round-two guard asked `!strings.Contains(entry, "=")` to decide
// whether a fragment was a continuation of a truncated command. Any ordinary
// second command defeats that — "go test -tags=integration ./..." contains an
// '=' — so the fragment read as a policy entry, the truncation went unnoticed
// again, and the weaker command was accepted. I guarded a real case and left
// the common one open.
//
// The rule is now keyed on repo=, the field a policy REQUIRES.
func TestSemicolonGuardIsNotDefeatedByAnEqualsSign(t *testing.T) {
	for _, tail := range []string{
		"go test -tags=integration ./...", // the reviewer's own input
		"GOFLAGS=-mod=mod go test ./...",
		"make check VAR=1",
		"go test ./...", // no '=' at all: the case round two did catch
	} {
		t.Run(tail, func(t *testing.T) {
			pol, bad := ParsePolicies("repo=o/r,cmd=go vet ./... ; " + tail)
			if len(pol) != 0 {
				t.Errorf("accepted %d policy/policies with command %q — the operator wrote two steps and this gate runs one, then posts success",
					len(pol), strings.Join(pol[0].CheckCmd, " "))
			}
			if len(bad) == 0 {
				t.Fatal("the truncation was silent")
			}
		})
	}
}

// TestTwoRealEntriesSharingARepoStillParse is the control for R1's fix. The
// guard drops the PRECEDING policy when it sees a continuation, and it used to
// identify that policy with `strings.Contains(frags[i-1], policies[n-1].Repo)`
// — another guess, which misfires when two entries share a repo. It is keyed
// on the fragment's index now, and legitimate multi-entry values must survive.
func TestTwoRealEntriesSharingARepoStillParse(t *testing.T) {
	pol, bad := ParsePolicies("repo=o/r,context=corral/lint,cmd=golangci-lint run;repo=o/r,context=corral/test,cmd=go test ./...")
	if len(pol) != 2 {
		t.Fatalf("policies = %d, want 2 (bad=%v) — two contexts on one repo is the documented multi-policy shape", len(pol), bad)
	}
	if pol[0].Context == pol[1].Context {
		t.Errorf("both policies came back with context %q", pol[0].Context)
	}
	if got := strings.Join(pol[1].CheckCmd, " "); got != "go test ./..." {
		t.Errorf("second command = %q, want it intact", got)
	}
}

// TestStrayFieldAfterCmdToleratesWhitespace is round three's R2.
//
// MY BUG: strayFieldAfterCmd matched the single spelling ","+f+"=" exactly. An
// operator writing the spaced form — "cmd=make test, base=release", which is at
// least as natural — slipped through, the field was absorbed into the command,
// and Base stayed nil: a policy gating EVERY base branch rather than the one
// named. One spelling guarded, the other left open, in the fix for a finding
// about exactly that.
func TestStrayFieldAfterCmdToleratesWhitespace(t *testing.T) {
	for _, entry := range []string{
		"repo=o/r,cmd=make test, base=release", // the reviewer's own input
		"repo=o/r,cmd=make test ,base=release",
		"repo=o/r,cmd=make test , base = release",
		"repo=o/r,cmd=make test,\tcontext=corral/other",
		"repo=o/r,cmd=make test,  timeout=30",
	} {
		t.Run(entry, func(t *testing.T) {
			pol, bad := ParsePolicies(entry)
			if len(pol) != 0 {
				t.Errorf("accepted a policy whose Base is %v and command is %q — a WIDER policy than was written, silently",
					pol[0].Base, strings.Join(pol[0].CheckCmd, " "))
			}
			if len(bad) == 0 {
				t.Fatal("a policy field after cmd= was swallowed with nothing reported")
			}
		})
	}
}

// TestACommandContainingACommaEqualsPairStillParses is the control: the fix
// must not start refusing commands that legitimately contain ",<word>=" where
// the word is not a policy field.
func TestACommandContainingACommaEqualsPairStillParses(t *testing.T) {
	pol, bad := ParsePolicies("repo=o/r,cmd=go test -ldflags=-X main.v=1,other=2 ./...")
	if len(pol) != 1 {
		t.Fatalf("policies = %d, want 1 (bad=%v) — 'other' is not a policy field and must not trip the guard", len(pol), bad)
	}
}

// TestLegacyKeyIsWidenedNotLeftAlone is round three's R3, and the one I am
// least comfortable about, because I shipped it on the strength of a sentence
// I wrote asserting it was safe.
//
// MY BUG: the round-two migration added a `context` column but left the
// PRIMARY KEY as the legacy (repo, head_sha), and I wrote a comment claiming
// that was "fine — the old key is strictly narrower, so those rows keep
// deduping". It is not fine. Save uses INSERT OR REPLACE, so under the narrow
// key the second context's row OVERWRITES the first's; GetByHead then misses
// the row it just wrote for the other context, and the poller re-runs both
// policies every tick forever — a worse failure than the one the column was
// added to fix.
func TestLegacyKeyIsWidenedNotLeftAlone(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "legacy.db")

	// Build a store in the PRE-migration shape, exactly as an operator
	// running the gate before today would have on disk.
	legacy, err := openRawLegacyStore(t, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`INSERT INTO gate_runs (repo, head_sha, pr, passed, record_id, ran_at)
		VALUES ('o/r', 'abc', 1, TRUE, 7, TIMESTAMP '2026-09-01 00:00:00')`); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	// Open it the normal way: the migration must run.
	store, err := OpenStore(dsn)
	if err != nil {
		t.Fatalf("OpenStore on a legacy store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// The legacy row survives, under the default context.
	if got, ok, err := store.GetByHead("o/r", "abc", DefaultStatusContext); err != nil || !ok {
		t.Fatalf("the pre-existing row was lost by the migration: err=%v ok=%v", err, ok)
	} else if got.RecordID != 7 {
		t.Errorf("RecordID = %d, want the legacy 7", got.RecordID)
	}

	// THE DEFECT: two contexts on one head must now coexist.
	for _, c := range []string{"corral/lint", "corral/test"} {
		if err := store.Save(Run{Repo: "o/r", HeadSHA: "xyz", PR: 2, Passed: true,
			Context: c, StatusPosted: true, RecordID: 1, RanAt: time.Unix(0, 0)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []string{"corral/lint", "corral/test"} {
		if _, ok, err := store.GetByHead("o/r", "xyz", c); err != nil || !ok {
			t.Errorf("context %s was lost: err=%v ok=%v — under the narrow key one context overwrites the other and the poller loops forever", c, err, ok)
		}
	}
}

// TestMigrateKeyIsIdempotent — a second open must not rebuild or lose rows.
func TestMigrateKeyIsIdempotent(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "twice.db")
	s1, err := OpenStore(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.Save(Run{Repo: "o/r", HeadSHA: "abc", PR: 1, Context: "corral/lint", RecordID: 3, RanAt: time.Unix(0, 0)}); err != nil {
		t.Fatal(err)
	}
	_ = s1.Close()

	s2, err := OpenStore(dsn)
	if err != nil {
		t.Fatalf("reopening an already-migrated store: %v", err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	if _, ok, err := s2.GetByHead("o/r", "abc", "corral/lint"); err != nil || !ok {
		t.Fatalf("a row vanished on the second open: err=%v ok=%v", err, ok)
	}
}

// TestRunnerPostsAndRecordsTheSameContext is round three's R4.
//
// MY BUG: Store.Save substituted "corral/gate" for an empty Context, and
// Runner.Run posted the empty string straight to the forge — which files it
// under its OWN default context. So the status the forge showed and the row
// the store kept disagreed about which check had reported, and the poller's
// per-context lookup could never match what was actually posted. The default
// existed at one door and not the other.
func TestRunnerPostsAndRecordsTheSameContext(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	status := &contextSpy{}
	r := &Runner{
		Checkout: &fakeCheckouter{}, Jail: &fakeJail{exitCode: 0, output: "ok"},
		Certify: &fakeCertifier{recordID: 1}, Status: status, Store: store,
		RecordURL: func(repo, sha string) string { return "http://x" },
		Now:       func() time.Time { return time.Unix(0, 0) },
	}
	// A Policy with NO Context, as brain Options.GatePolicies may build it.
	if err := r.Run(context.Background(), "http://forge/o/r",
		Policy{Repo: "o/r", CheckCmd: []string{"true"}}, PRRef{Number: 1, HeadSHA: "abc"}); err != nil {
		t.Fatal(err)
	}

	for _, got := range status.contexts {
		if got != DefaultStatusContext {
			t.Errorf("posted a status under context %q — the forge files that under its own default, while the store records %q", got, DefaultStatusContext)
		}
	}
	if _, ok, err := store.GetByHead("o/r", "abc", DefaultStatusContext); err != nil || !ok {
		t.Fatalf("the store has no row under the context that was posted: err=%v ok=%v", err, ok)
	}
}

// TestEffectiveTimeoutIsNeverNonPositive is round three's R5 — declared
// REPRODUCED, DEMOTED to CODE-READ by our own harness because the script
// expected a negative duration and the overflow lands on exactly 0s. The
// verifier then noted the claim stands anyway: zero triggers the same 60s
// sandbox fallback. The tier fell; the defect was real.
//
// MY BUG: round two bounded timeout= in ParsePolicies only, so a Policy that
// never went through the parser still overflowed in the runner.
func TestEffectiveTimeoutIsNeverNonPositive(t *testing.T) {
	for _, ts := range []int{
		9223372036854775807, // the reviewer's input: overflows to exactly 0s
		1 << 62,
		-1,
		0,
		maxGateTimeoutS + 1,
	} {
		d := Policy{TimeoutS: ts}.effectiveTimeout()
		if d <= 0 {
			t.Errorf("TimeoutS=%d gives %v — the sandbox turns any deadline <= 0 into its own 60s default, which blocks merge on any real command", ts, d)
		}
		if d != DefaultGateTimeout && (ts < 0 || ts > maxGateTimeoutS) {
			t.Errorf("TimeoutS=%d gives %v, want the documented default %v", ts, d, DefaultGateTimeout)
		}
	}
	// And an ordinary value is still honored, or the bound is worse than the bug.
	if d := (Policy{TimeoutS: 900}).effectiveTimeout(); d != 900*time.Second {
		t.Errorf("TimeoutS=900 gives %v, want 15m — a legitimate timeout must survive", d)
	}
}

// TestTheRunnerActuallyUsesTheBoundedTimeout is the negative control for R5:
// effectiveTimeout being correct is worth nothing if Run computes its own.
func TestTheRunnerActuallyUsesTheBoundedTimeout(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	jail := &fakeJail{exitCode: 0, output: "ok"}
	r := &Runner{
		Checkout: &fakeCheckouter{}, Jail: jail, Certify: &fakeCertifier{recordID: 1},
		Status: &fakeStatusPoster{}, Store: store,
		RecordURL: func(repo, sha string) string { return "http://x" },
		Now:       func() time.Time { return time.Unix(0, 0) },
	}
	_ = r.Run(context.Background(), "http://forge/o/r",
		Policy{Repo: "o/r", Context: "corral/gate", CheckCmd: []string{"true"}, TimeoutS: 9223372036854775807},
		PRRef{Number: 1, HeadSHA: "abc"})

	if jail.lastTimeout <= 0 {
		t.Fatalf("the jail was handed a timeout of %v — Run is not using the bounded value", jail.lastTimeout)
	}
	if jail.lastTimeout != DefaultGateTimeout {
		t.Errorf("jail timeout = %v, want %v", jail.lastTimeout, DefaultGateTimeout)
	}
}

// contextSpy records the status context of every post, which fakeStatusPoster
// does not keep.
type contextSpy struct {
	contexts []string
	states   []string
}

func (c *contextSpy) SetCommitStatus(ctx context.Context, repoURL, sha, statusCtx, state, targetURL, description string) error {
	c.contexts = append(c.contexts, statusCtx)
	c.states = append(c.states, state)
	return nil
}

func (c *contextSpy) sawSuccess() bool { return slices.Contains(c.states, "success") }

// openRawLegacyStore opens dsn and creates gate_runs in the PRE-2026-09-12
// shape: no context column, no status_posted, and the narrow (repo, head_sha)
// primary key. It deliberately does NOT go through OpenStore, because the
// point is to produce the on-disk state an operator already has.
func openRawLegacyStore(t *testing.T, dsn string) (*sql.DB, error) {
	t.Helper()
	db, err := sql.Open("duckdb", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`CREATE TABLE gate_runs (
		repo VARCHAR NOT NULL,
		head_sha VARCHAR NOT NULL,
		pr INTEGER NOT NULL,
		passed BOOLEAN NOT NULL,
		record_id BIGINT NOT NULL,
		ran_at TIMESTAMP NOT NULL,
		PRIMARY KEY (repo, head_sha)
	)`); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

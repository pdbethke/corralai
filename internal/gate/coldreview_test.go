// SPDX-License-Identifier: Elastic-2.0

package gate

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// The eight findings of the cold review of 2026-09-12 (reviewer claude-code,
// verifier antigravity, all eight standing). They fall into two families, and
// the split is worth keeping in mind: R2, R6, R7 and R8 are the gate REPORTING
// SOMETHING IT DID NOT CHECK, which is the only failure that really matters
// here; R1, R3, R4 and R5 are verdicts that were computed and then not
// delivered.

// --- FAMILY ONE: the gate reports a result for a check it did not run ---

// TestSemicolonInCmdNeverYieldsAWeakerPolicy is R2, the high-severity one.
//
// THE DEFECT: ParsePolicies split the raw value on ';' BEFORE isolating cmd=.
// A shell command containing a semicolon — "cmd=go vet ./... ; go test ./..."
// under sh -c — was therefore torn in half: the first fragment parsed as a
// COMPLETE, VALID policy carrying only "go vet ./...", and just the orphaned
// tail landed in bad. The operator declared two steps, the gate ran one, and
// posted success. The file's own doc comment says cmd parsing exists to stop
// exactly this, having guarded the comma and not the semicolon.
func TestSemicolonInCmdNeverYieldsAWeakerPolicy(t *testing.T) {
	pol, bad := ParsePolicies("repo=o/r,cmd=go vet ./... ; go test ./...")

	for _, p := range pol {
		cmd := strings.Join(p.CheckCmd, " ")
		if !strings.Contains(cmd, "go test") {
			t.Errorf("accepted a policy whose command is %q — the operator wrote two steps and this runs one, then posts success", cmd)
		}
	}
	if len(pol) != 0 {
		t.Errorf("accepted %d policy/policies from an entry whose command was truncated; it must be refused, not repaired by guesswork", len(pol))
	}
	if len(bad) == 0 {
		t.Fatal("nothing reported in bad — the truncation was silent, which is the whole defect")
	}
	joined := strings.Join(bad, " | ")
	if !strings.Contains(joined, ";") {
		t.Errorf("bad = %q never mentions the ';' that caused this, so the operator cannot act on it", joined)
	}
}

// TestCmdWithCommasStillWorks is the companion control: the comma case the
// file always handled must keep working, or this fix traded one truncation for
// a refusal of good policies.
func TestCmdWithCommasStillWorks(t *testing.T) {
	pol, bad := ParsePolicies("repo=o/r,base=main,cmd=go test -run A,B ./...")
	if len(pol) != 1 {
		t.Fatalf("policies = %d, want 1 (bad=%v) — a command containing commas is legal and documented", len(pol), bad)
	}
	if got := strings.Join(pol[0].CheckCmd, " "); got != "go test -run A,B ./..." {
		t.Errorf("CheckCmd = %q, want the command verbatim", got)
	}
}

// TestFieldAfterCmdIsReportedNotSwallowed is R8.
//
// THE DEFECT: cmd= takes the rest of the entry verbatim, so a policy field
// written after it was absorbed into the command. "cmd=make test,base=release"
// produced CheckCmd ["make","test,base=release"] AND Base nil — a policy that
// gates EVERY base rather than the one named, silently, with nothing in bad.
// A wider policy than the operator wrote is the same class of wrong as a
// weaker command.
func TestFieldAfterCmdIsReportedNotSwallowed(t *testing.T) {
	for _, field := range []string{"base=release", "context=corral/other", "net=true", "timeout=30", "repo=other/repo"} {
		t.Run(field, func(t *testing.T) {
			pol, bad := ParsePolicies("repo=o/r,cmd=make test," + field)
			if len(pol) != 0 {
				t.Errorf("accepted a policy with %q after cmd=: CheckCmd=%v Base=%v — the field was swallowed and the policy is not what was written",
					field, pol[0].CheckCmd, pol[0].Base)
			}
			if len(bad) == 0 {
				t.Fatalf("%q after cmd= was accepted silently; the doc promises cmd parsing fails loudly", field)
			}
		})
	}
}

// TestRunnerRefusesAPolicyWithNoCommand is R6.
//
// THE DEFECT: ParsePolicies refuses an entry with no cmd=, but a Policy built
// programmatically (the brain's Options.GatePolicies) is never parsed. An
// empty CheckCmd reached the jail as `sh -c ""`, which exits 0, so the gate
// posted "success" for a check that ran nothing at all. The rule lived at the
// door that PARSES a policy and not at the door that ACTS on one.
func TestRunnerRefusesAPolicyWithNoCommand(t *testing.T) {
	for _, tc := range []struct {
		name string
		cmd  []string
	}{
		{"nil", nil},
		{"empty slice", []string{}},
		{"one empty string", []string{""}},
		{"whitespace only", []string{"  ", "\t"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := OpenStore(filepath.Join(t.TempDir(), "g.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })

			jail := &fakeJail{exitCode: 0, output: "ok"}
			status := &fakeStatusPoster{}
			r := &Runner{
				Checkout: &fakeCheckouter{}, Jail: jail, Certify: &fakeCertifier{recordID: 1}, Status: status, Store: store,
				RecordURL: func(repo, sha string) string { return "http://x" },
				Now:       func() time.Time { return time.Unix(0, 0) },
			}
			_ = r.Run(context.Background(), "http://forge/o/r", Policy{Repo: "o/r", Context: "corral/gate", CheckCmd: tc.cmd}, PRRef{Number: 1, HeadSHA: "abc"})

			if jail.calls != 0 {
				t.Error("the jail was invoked for a policy with no command")
			}
			if slices.Contains(status.states, "success") {
				t.Fatal("posted success for a check that would have run nothing — a green on a question nobody asked")
			}
			if !slices.Contains(status.states, "error") && !slices.Contains(status.states, "failure") {
				t.Errorf("posted neither error nor failure; states seen: %v", status.states)
			}
		})
	}
}

// TestTimeoutIsBoundedRatherThanOverflowing is R7.
//
// THE DEFECT: a huge timeout= parsed fine with strconv.Atoi, and then
// time.Duration(n)*time.Second overflowed int64 into a NEGATIVE duration. The
// sandbox turns any deadline <= 0 into its own 60s default — which is the
// precise outcome DefaultGateTimeout's comment says "permanently blocks merge
// on any real-world command" — and nothing was reported.
func TestTimeoutIsBoundedRatherThanOverflowing(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		want  bool // accepted?
	}{
		{"overflowing", "9223372036854775807", false},
		{"absurd but parseable", "999999999999", false},
		{"negative", "-1", false},
		{"not a number", "soon", false},
		{"the maximum", "86400", true},
		{"an ordinary ten minutes", "600", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pol, bad := ParsePolicies("repo=o/r,timeout=" + tc.value + ",cmd=go test ./...")
			if tc.want {
				if len(pol) != 1 {
					t.Fatalf("a legitimate timeout=%s was refused (bad=%v)", tc.value, bad)
				}
				// The effective duration must be positive, or the sandbox
				// silently substitutes its own 60s and we are back to the bug.
				if d := time.Duration(pol[0].TimeoutS) * time.Second; d <= 0 {
					t.Errorf("timeout=%s yields a non-positive duration %v", tc.value, d)
				}
				return
			}
			if len(pol) != 0 {
				d := time.Duration(pol[0].TimeoutS) * time.Second
				t.Errorf("accepted timeout=%s, giving duration %v — a value <= 0 becomes the sandbox's 60s default and blocks merges", tc.value, d)
			}
			if len(bad) == 0 {
				t.Errorf("timeout=%s was rejected silently, with nothing for the operator to read", tc.value)
			}
		})
	}
}

// --- FAMILY TWO: the verdict was computed and not delivered ---

// TestStoreFailureNeverPostsSuccess is R1.
//
// THE DEFECT: a failed Store.Save was logged and ignored, and "success" was
// posted anyway — contradicting Runner's own documented invariant that success
// follows only when checkout, sign AND store all succeeded. With no dedupe row
// the poller then re-ran the jail and re-certified on every tick, appending a
// new signed record each time, indefinitely, while the status target_url
// pointed at a record no lookup could find.
func TestStoreFailureNeverPostsSuccess(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	// Close the store so every write fails the way a full disk or a locked
	// file would, without needing either.
	_ = store.Close()

	status := &fakeStatusPoster{}
	r := &Runner{
		Checkout: &fakeCheckouter{}, Jail: &fakeJail{exitCode: 0, output: "ok"},
		Certify: &fakeCertifier{recordID: 1}, Status: status, Store: store,
		RecordURL: func(repo, sha string) string { return "http://x" },
		Now:       func() time.Time { return time.Unix(0, 0) },
	}
	_ = r.Run(context.Background(), "http://forge/o/r",
		Policy{Repo: "o/r", Context: "corral/gate", CheckCmd: []string{"true"}}, PRRef{Number: 1, HeadSHA: "abc"})

	if slices.Contains(status.states, "success") {
		t.Fatal("posted success while the run could not be recorded — the documented fail-closed invariant says otherwise, and the poller would re-certify this head forever")
	}
}

// TestDedupeIsPerStatusContext is R3.
//
// THE DEFECT: dedupe was keyed on (repo, head_sha) alone. Two policies for one
// repo under different contexts — which the config doc invites and
// configurable contexts imply — collapsed to one row, so whichever ran first
// stored it and the poller skipped the SECOND policy on every head forever.
// Its check never ran and its context was never posted; a required check would
// block the pull request indefinitely.
func TestDedupeIsPerStatusContext(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ran := map[string]int{}
	p := &Poller{
		Policies: []Policy{
			{Repo: "o/r", Base: []string{"main"}, Context: "corral/lint", CheckCmd: []string{"lint"}},
			{Repo: "o/r", Base: []string{"main"}, Context: "corral/test", CheckCmd: []string{"test"}},
		},
		List:  &fakeLister{prs: []PRRef{{Number: 1, HeadSHA: "abc", Base: "main"}}},
		Store: store,
		Run: func(ctx context.Context, repoURL string, pol Policy, pr PRRef) error {
			ran[pol.Context]++
			return store.Save(Run{Repo: pol.Repo, HeadSHA: pr.HeadSHA, PR: pr.Number,
				Context: pol.Context, StatusPosted: true, RanAt: time.Unix(0, 0)})
		},
	}
	_ = p.Tick(context.Background())

	for _, want := range []string{"corral/lint", "corral/test"} {
		if ran[want] != 1 {
			t.Errorf("%s ran %d times, want 1 — a context whose check never runs never posts a status, and a required check then blocks the PR forever", want, ran[want])
		}
	}
	// And a second tick must not re-run either: the fix widens the key, it
	// does not disable dedupe.
	_ = p.Tick(context.Background())
	for _, c := range []string{"corral/lint", "corral/test"} {
		if ran[c] != 1 {
			t.Errorf("%s ran %d times across two ticks — dedupe is broken, which is worse than the finding", c, ran[c])
		}
	}
}

// TestAnUndeliveredVerdictIsRetried is R4 and R5 together, because they share
// one cause and one fix.
//
// THE DEFECT: the dedupe row was written BEFORE the commit status was posted.
// A post that failed — forge 5xx, rate limit, or a context cancelled at
// shutdown — was therefore never retried: the head counted as gated and the
// forge kept whatever it last saw, usually "pending", until somebody pushed a
// new commit. R5 is the same trap reached through FailClosed: a clean shutdown
// cancels the context, the run is stored as a permanent Passed=false, and the
// status post on that same cancelled context fails too.
//
// The fix is deliberately NOT a guess about which errors are transient. It
// records whether the verdict actually REACHED the forge, and the poller
// treats an undelivered row as work still outstanding.
func TestAnUndeliveredVerdictIsRetried(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// A row that was gated but whose status never landed.
	if err := store.Save(Run{Repo: "o/r", HeadSHA: "abc", PR: 1, Passed: true,
		Context: "corral/gate", StatusPosted: false, RanAt: time.Unix(0, 0)}); err != nil {
		t.Fatal(err)
	}

	runs := 0
	p := &Poller{
		Policies: []Policy{{Repo: "o/r", Base: []string{"main"}, Context: "corral/gate", CheckCmd: []string{"true"}}},
		List:     &fakeLister{prs: []PRRef{{Number: 1, HeadSHA: "abc", Base: "main"}}},
		Store:    store,
		Run: func(ctx context.Context, repoURL string, pol Policy, pr PRRef) error {
			runs++
			return store.Save(Run{Repo: pol.Repo, HeadSHA: pr.HeadSHA, PR: pr.Number,
				Context: pol.Context, StatusPosted: true, RanAt: time.Unix(0, 0)})
		},
	}
	_ = p.Tick(context.Background())
	if runs != 1 {
		t.Fatalf("runs = %d, want 1 — a verdict the forge never received must be re-delivered, not treated as done", runs)
	}
	// Once delivered, it stops.
	_ = p.Tick(context.Background())
	if runs != 1 {
		t.Errorf("runs = %d after the verdict was delivered, want 1 — the retry must not become a loop", runs)
	}
}

// TestMarkPostedIsNotVacuous is the negative control for the family-two fix.
// If MarkPosted silently did nothing, or if reads always reported a row as
// delivered, the retry test above would still pass while every failed post
// was again lost.
func TestMarkPostedIsNotVacuous(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err := store.Save(Run{Repo: "o/r", HeadSHA: "abc", PR: 1, Context: "corral/gate", RanAt: time.Unix(0, 0)}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.GetByHead("o/r", "abc", "corral/gate")
	if err != nil || !ok {
		t.Fatalf("GetByHead: %v ok=%v", err, ok)
	}
	if got.StatusPosted {
		t.Fatal("a freshly saved row already reads as delivered — the flag can never be false, so the retry path is dead code")
	}
	if err := store.MarkPosted("o/r", "abc", "corral/gate"); err != nil {
		t.Fatal(err)
	}
	got, _, _ = store.GetByHead("o/r", "abc", "corral/gate")
	if !got.StatusPosted {
		t.Fatal("MarkPosted did not take — the flag can never be true, so every head would be re-gated on every tick")
	}
	// A different context must be untouched: the mark is per context, like the key.
	if _, ok, _ := store.GetByHead("o/r", "abc", "corral/other"); ok {
		t.Error("a row appeared under a context that was never saved")
	}
}

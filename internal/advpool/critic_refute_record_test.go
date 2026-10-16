// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"bytes"
	"context"
	"log"
	"reflect"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/queue"
)

// With no critic sink wired — `certify --repo`'s case — a whole-test claim
// is still checked by running the test alone, and a refuted one leaves
// VacuousFindings (the count a record publishes) for RefutedFindings. Before
// this, adjudication ran only for a wired sink, so a Warp record counted two
// "can never fail" claims that deleting one guard clause showed false.
func TestARefutedClaimLeavesThePublishedCountWithNoSinkWired(t *testing.T) {
	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(prevOut); log.SetFlags(prevFlags) })

	survivors := []adequacy.Mutant{{ID: "m1", Replace: "c1"}}
	scorer := &fakeScorer{devKillRate: 0.9, devSurvivors: survivors, poolSurvivors: nil}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m0", Replace: "c0"}, survivors[0]}}
	d, _ := newTestDriver(t, 21, scorer, validator, 0.5)
	d.Signer = &fakeSigner{}

	ctx := context.Background()
	ready := claimAllReady(t, d.Q)
	tc, mg := ready[RoleTestCritic], ready[RoleMutantGenerator]
	for _, f := range []queue.Finding{
		{Severity: "medium", Target: "TestAlwaysPasses", Evidence: "can never fail", Scope: ScopeWholeTest, TestSelector: "TestAlwaysPasses"},
		{Severity: "low", Target: "TestAlwaysPasses", Evidence: "one check is dead", Scope: ScopeDeadCheck, TestSelector: "TestAlwaysPasses"},
	} {
		f.MissionID, f.TaskID, f.Reporter, f.Type, f.TestFile = 21, tc.ID, "test-critic", "note", "target_test.go"
		if _, err := d.Q.AddFinding(f); err != nil {
			t.Fatalf("AddFinding: %v", err)
		}
	}
	mustComplete(t, d.Q, tc.ID, "typed critic judged 1 test(s), 2 flagged")
	mustComplete(t, d.Q, mg.ID, "raw mutants")
	if _, err := d.Tick(ctx, 21); err != nil {
		t.Fatalf("Tick (dev-adequacy): %v", err)
	}
	tw := claimTaskByID(t, d.Q, d.runs[21].testWriterTaskID)
	mustComplete(t, d.Q, tw.ID, "pool test source")
	v, err := d.Tick(ctx, 21)
	if err != nil || v == nil {
		t.Fatalf("Tick (aggregate): v=%v err=%v", v, err)
	}

	if len(v.RefutedFindings) != 1 || v.RefutedFindings[0].Scope != ScopeWholeTest {
		t.Errorf("RefutedFindings = %+v, want the one whole-test claim", v.RefutedFindings)
	}
	if len(v.VacuousFindings) != 1 || v.VacuousFindings[0].Scope != ScopeDeadCheck {
		t.Errorf("VacuousFindings = %+v, want only the dead-check claim, which execution never adjudicates", v.VacuousFindings)
	}
	if out := buf.String(); !strings.Contains(out, "TestAlwaysPasses can never fail is REFUTED by execution") {
		t.Errorf("run output does not say the claim was refuted:\n%s", out)
	}
}

// For Rust the single test is found through the runner's own list: one
// listing per run, the file-relative name mapped onto the listed id, and the
// operator's command narrowed with --exact. A name the list holds twice is
// left unchecked, and says so.
func TestCriticTestCommandForRust(t *testing.T) {
	enum := &fakeEnumerator{out: "tests::test_ignore_spaces_empty_query: test\ntests::test_ignore_spaces_only_spaces_query: test\ninline::dup: test\ntests::dup: test\n"}
	d := &Driver{Enumerator: enum}
	run := &runState{rs: RunSpec{Lang: "rust", CodePath: "crates/fuzzy_match/src/lib.rs", DevTestPath: "crates/fuzzy_match/src/fuzzy_tests.rs", TestCmd: "cargo test -p fuzzy_match"}}
	ctx := context.Background()

	cmd, why := d.criticTestCommand(ctx, run, queue.Finding{TestSelector: "test_ignore_spaces_empty_query"})
	if want := []string{"cargo", "test", "-p", "fuzzy_match", "--", "--exact", "tests::test_ignore_spaces_empty_query"}; !reflect.DeepEqual(adequacy.ShellSplit(cmd), want) {
		t.Errorf("command = %q (%s), want %q", cmd, why, want)
	}
	if _, why := d.criticTestCommand(ctx, run, queue.Finding{TestSelector: "dup"}); !strings.Contains(why, "more than one") {
		t.Errorf("an ambiguous name must be left unchecked, got why=%q", why)
	}
	if _, _ = d.criticTestCommand(ctx, run, queue.Finding{TestSelector: "test_ignore_spaces_only_spaces_query"}); enum.calls != 1 {
		t.Errorf("listed the tests %d times, want once per run", enum.calls)
	}

	noEnum := &runState{rs: run.rs}
	if cmd, why := (&Driver{}).criticTestCommand(ctx, noEnum, queue.Finding{TestSelector: "test_ignore_spaces_empty_query"}); cmd != "" || !strings.Contains(why, "no enumerator") {
		t.Errorf("with no enumerator: cmd=%q why=%q", cmd, why)
	}
}

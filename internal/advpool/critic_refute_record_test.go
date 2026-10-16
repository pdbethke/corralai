// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"bytes"
	"context"
	"log"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
	golang "github.com/pdbethke/corralai/internal/lang"
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

// A runner whose ids need resolving by its own list (the cargo rows) gets no
// matrix: the matrix looks critic findings up by exact selector, and a
// critic's file-relative name (`adds`) never equals a listed id
// (`tests::adds`), so every whole-test claim would go unchecked. libtest
// also lists the whole crate, so tests of unrelated modules would be scored
// against one file's mutants and offered for deletion. Until the matrix
// scopes and resolves, such a run keeps the list-and-resolve critic path.
func TestMatrixLeavesResolveByListRunnersToTheCriticPath(t *testing.T) {
	enumerator := &fakeEnumerator{out: "tests::adds: test\n"}
	d := &Driver{Enumerator: enumerator}
	rs := RunSpec{Lang: "rust", TestCmd: "cargo test", Matrix: true, CodePath: "src/a.rs", DevTestPath: "src/a.rs"}
	run := &runState{rs: rs, mutants: []adequacy.Mutant{{ID: "m1"}}}
	d.tickMatrix(context.Background(), run)
	if enumerator.calls != 0 || run.matrix != nil {
		t.Fatalf("Rust --matrix enumerated %d time(s), matrix=%v; want the matrix skipped", enumerator.calls, run.matrix)
	}
}

// The runners Phase 2 added reach the critic the way Rust does: one listing
// per run, the critic's name resolved onto a listed id, the operator's
// command narrowed to that one test. The command survives the string the
// scorer is handed (regex metacharacters and spaces included), and a name
// whose run would also run a second listed test is left unchecked.
func TestCriticTestCommandForTheListedRunners(t *testing.T) {
	read := func(name string) string {
		b, err := os.ReadFile("../lang/testdata/runners/" + name) // #nosec G304 -- test fixture
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	for _, c := range []struct {
		lang, testCmd, testPath, list, name string
		want                                []string
		collides                            string
	}{
		{"javascript", "npx jest", "calc.jest.test.js", read("jest-list.json"), "adds (a+b)",
			[]string{"npx", "jest", "--testNamePattern", `^Calc adds \(a\+b\)$`, "calc.jest.test.js"}, "Calc::adds"},
		{"typescript", "node --experimental-strip-types --test", "calc.node.test.ts", read("node-tap.txt"), "top level",
			[]string{"node", "--experimental-strip-types", "--test", "--test-name-pattern", `^top level$`, "calc.node.test.ts"}, "adds"},
		{"ruby", "bundle exec rspec", "spec/calc_spec.rb", read("rspec-list.json"), "level",
			[]string{"bundle", "exec", "rspec", "-E", `\Atop level\z`, "spec/calc_spec.rb"}, "Calc::adds x"},
		{"php", "vendor/bin/phpunit", "tests/CalcTest.php", read("phpunit-list.txt"), "CalcTest::testWith",
			[]string{"vendor/bin/phpunit", "--filter", `/(?:^|\\)CalcTest::testWith(?: with data set .*)?$/`, "tests/CalcTest.php"}, ""},
	} {
		d := &Driver{Enumerator: &fakeEnumerator{out: c.list}}
		run := &runState{rs: RunSpec{Lang: c.lang, DevTestPath: c.testPath, TestCmd: c.testCmd}}
		ctx := context.Background()
		cmd, why := d.criticTestCommand(ctx, run, queue.Finding{TestSelector: c.name, TestFile: c.testPath})
		if got := adequacy.ShellSplit(cmd); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: command = %q (%s), want %q", c.lang, got, why, c.want)
		}
		if c.collides != "" {
			if cmd, why := d.criticTestCommand(ctx, run, queue.Finding{TestSelector: c.collides, TestFile: c.testPath}); cmd != "" || !strings.Contains(why, "also run another") && !strings.Contains(why, "more than one") {
				t.Errorf("%s: %q was not left unchecked: cmd=%q why=%q", c.lang, c.collides, cmd, why)
			}
		}
	}
}

// A jest command's list is what the command runs, not the test file's tests
// (a directory argument or a config widens it), so --matrix stays off for
// jest as for Rust: it would offer other files' tests for deletion.
func TestMatrixLeavesJestToTheCriticPath(t *testing.T) {
	b, err := os.ReadFile("../lang/testdata/runners/jest-list.json") // #nosec G304 -- test fixture
	if err != nil {
		t.Fatal(err)
	}
	enumerator := &fakeEnumerator{out: string(b)}
	d := &Driver{Enumerator: enumerator}
	rs := RunSpec{Lang: "javascript", TestCmd: "npx jest src", Matrix: true, CodePath: "src/calc.js", DevTestPath: "src/calc.jest.test.js"}
	run := &runState{rs: rs, mutants: []adequacy.Mutant{{ID: "m1"}}}
	d.tickMatrix(context.Background(), run)
	if enumerator.calls != 0 || run.matrix != nil {
		t.Fatalf("jest --matrix enumerated %d time(s), matrix=%v; want the matrix skipped", enumerator.calls, run.matrix)
	}
}

// A run whose test command is `npm test` carries what was resolved about it
// at run start (the jest script behind it), so the critic's one-test command
// goes through npm to jest — and the driver never reads package.json.
func TestCriticTestCommandThroughNpmTest(t *testing.T) {
	b, err := os.ReadFile("../lang/testdata/runners/jest-list.json") // #nosec G304 -- test fixture
	if err != nil {
		t.Fatal(err)
	}
	enum := &fakeEnumerator{out: string(b)}
	d := &Driver{Enumerator: enum}
	inv := golang.Invocation{Script: []string{"jest"}, Pass: "--", Narrow: true, From: "package.json scripts.test"}
	run := &runState{rs: RunSpec{Lang: "javascript", DevTestPath: "calc.jest.test.js", TestCmd: "npm test", Invocation: inv}}
	cmd, why := d.criticTestCommand(context.Background(), run, queue.Finding{TestSelector: "top level", TestFile: "calc.jest.test.js"})
	if want := []string{"npm", "test", "--", "--testNamePattern", "^top level$", "calc.jest.test.js"}; !reflect.DeepEqual(adequacy.ShellSplit(cmd), want) {
		t.Errorf("command = %q (%s), want %q", cmd, why, want)
	}

	// Without the resolution, npm test is no runner corral can narrow.
	bare := &runState{rs: RunSpec{Lang: "javascript", DevTestPath: "calc.jest.test.js", TestCmd: "npm test"}}
	if cmd, _ := d.criticTestCommand(context.Background(), bare, queue.Finding{TestSelector: "top level"}); cmd != "" {
		t.Errorf("an unresolved npm test was narrowed to %q", cmd)
	}
}

// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"context"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/queue"
)

// authoredCall is one ScoreAuthoredReport invocation: which test was run, and
// against which mutants. The pair is the whole point of the fan-out — a
// per-survivor proof is one test against ONE mutant, and a fake that only
// counted calls could not tell that apart from the old batched pass.
type authoredCall struct {
	test    string
	mutants []string
}

// fanoutScorer scripts the dev pass and then records every authored pass,
// killing exactly the survivor its `kills` map names for that test source.
type fanoutScorer struct {
	survivors []adequacy.Mutant
	kills     map[string]string // test source -> the mutant id it kills
	devDone   bool
	authored  []authoredCall
}

func (f *fanoutScorer) Score(ctx context.Context, codePath, code, test string, mutants []adequacy.Mutant, testCmd string) (float64, []adequacy.Mutant, error) {
	return 0.5, f.survivors, nil
}

func (f *fanoutScorer) ScoreReport(ctx context.Context, codePath, code, test string, mutants []adequacy.Mutant, testCmd string) (adequacy.Report, error) {
	if !f.devDone {
		f.devDone = true
		rep := adequacy.Report{CompliantPass: true, CanaryKilled: true, Total: len(mutants)}
		alive := map[string]bool{}
		for _, s := range f.survivors {
			alive[s.ID] = true
		}
		for _, m := range mutants {
			if alive[m.ID] {
				rep.Survived = append(rep.Survived, m.ID)
			} else {
				rep.Killed = append(rep.Killed, m.ID)
			}
		}
		return rep, nil
	}
	return adequacy.Report{CompliantPass: true, CanaryKilled: true}, nil
}

func (f *fanoutScorer) ScoreAuthoredReport(ctx context.Context, codePath, code, test string, mutants []adequacy.Mutant, testCmd string) (adequacy.Report, error) {
	ids := make([]string, 0, len(mutants))
	for _, m := range mutants {
		ids = append(ids, m.ID)
	}
	f.authored = append(f.authored, authoredCall{test: test, mutants: ids})

	rep := adequacy.Report{
		CompliantPass: true, CanaryKilled: true, Total: len(mutants),
		PerMutant: map[string]adequacy.MutantGrading{},
	}
	for _, m := range mutants {
		// TestsRun 1 / RuleAuthoredAlone is what the real jail records on the
		// authored pass: only the authored test ran, so a kill is the
		// authored test's own.
		rep.PerMutant[m.ID] = adequacy.MutantGrading{TestsRun: 1, Rule: RuleAuthoredAlone}
		if f.kills[test] == m.ID {
			rep.Killed = append(rep.Killed, m.ID)
		} else {
			rep.Survived = append(rep.Survived, m.ID)
		}
	}
	return rep, nil
}

// fanoutValidator refuses to compile any test whose source is in badTests, so
// ONE survivor's seat can fail while its siblings succeed.
type fanoutValidator struct {
	mutants  []adequacy.Mutant
	badTests map[string]bool
}

func (v *fanoutValidator) ParseMutants(raw, _ string) ([]adequacy.Mutant, error) {
	return v.mutants, nil
}
func (v *fanoutValidator) ParseTest(raw string) string { return raw }
func (v *fanoutValidator) CompileTest(ctx context.Context, codePath, code, test string) error {
	if v.badTests[test] {
		return &CompileError{Output: "target_test.go:2:1: syntax error"}
	}
	return nil
}

// fanoutRun is the shared fixture: three survivors, one writer seat each.
func fanoutRun(t *testing.T, mode string, kills map[string]string, bad map[string]bool) (*Driver, int64, *fanoutScorer) {
	t.Helper()
	survivors := []adequacy.Mutant{
		{ID: "m1", Search: "func A() {}", Replace: "func A() { panic(1) }"},
		{ID: "m2", Search: "func B() {}", Replace: "func B() { panic(2) }"},
		{ID: "m3", Search: "func C() {}", Replace: "func C() { panic(3) }"},
	}
	scorer := &fanoutScorer{survivors: survivors, kills: kills}
	validator := &fanoutValidator{mutants: survivors, badTests: bad}

	q := newTestQueue(t)
	d, err := NewDriver(q, scorer, validator, decorrelatedAssign(), 0.9)
	if err != nil {
		t.Fatalf("NewDriver: %v", err)
	}
	rs := testRunSpec()
	rs.Code = "package target\nfunc A() {}\nfunc B() {}\nfunc C() {}\n"
	rs.WriterMode = mode
	const missionID = int64(9100)
	if err := d.StartRun(missionID, rs, nil); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if _, err := q.PromoteReady(missionID); err != nil {
		t.Fatalf("PromoteReady: %v", err)
	}
	d.Signer = &fakeSigner{}
	return d, missionID, scorer
}

// devTick completes the critic and generator seats and ticks once, so the
// writer seats exist.
func devTick(t *testing.T, d *Driver, missionID int64) {
	t.Helper()
	ready := claimAllReady(t, d.Q)
	for _, task := range ready {
		mustComplete(t, d.Q, task.ID, "raw")
	}
	if _, err := d.Tick(context.Background(), missionID); err != nil {
		t.Fatalf("Tick (dev-adequacy): %v", err)
	}
}

func writerTasks(t *testing.T, d *Driver, missionID int64) []queue.Task {
	t.Helper()
	tasks, err := d.tasksByRole(missionID, RoleTestWriter)
	if err != nil {
		t.Fatalf("tasksByRole: %v", err)
	}
	return liveTasks(tasks)
}

// TestPerSurvivorFansOutOneTaskPerSurvivor is the shape the whole task turns
// on: three survivors become three writer seats, each carrying the SAME
// cacheable prefix and exactly one survivor's diff. A prefix that differs by
// one byte between two of a file's tasks is not a cacheable prefix at all —
// the provider re-bills the whole file for every seat — so the identity is
// asserted, not assumed.
func TestPerSurvivorFansOutOneTaskPerSurvivor(t *testing.T) {
	d, missionID, _ := fanoutRun(t, WriterModePerSurvivor, nil, nil)
	devTick(t, d, missionID)

	tasks := writerTasks(t, d, missionID)
	if len(tasks) != 3 {
		t.Fatalf("got %d test-writer task(s), want 3 (one per survivor)", len(tasks))
	}
	wantKeys := map[string]bool{"test-writer/m1": true, "test-writer/m2": true, "test-writer/m3": true}
	for _, task := range tasks {
		if !wantKeys[task.Key] {
			t.Errorf("unexpected writer task key %q", task.Key)
		}
		delete(wantKeys, task.Key)
	}
	if len(wantKeys) != 0 {
		t.Errorf("missing writer task key(s): %v", wantKeys)
	}

	prefix := renderTestWriterPrefix(d.runs[missionID].rs, nil)
	if strings.TrimSpace(prefix) == "" {
		t.Fatal("the shared prefix is empty — there is nothing for a provider to cache")
	}
	// THE SYSTEM HALF, not a prefix of the instruction. A joined prompt goes
	// out as one user message with no system field on the request, and
	// Anthropic's cache_control block attaches to the system field and
	// nowhere else — so a "prefix" folded into the instruction is re-billed
	// in full on every seat, however byte-identical it is.
	for _, task := range tasks {
		if task.System != prefix {
			t.Fatalf("task %q does not carry the byte-identical shared prefix as its SYSTEM half — a caching provider re-bills the whole file for every seat", task.Key)
		}
		if strings.Contains(task.Instruction, prefix) {
			t.Errorf("task %q also repeats the prefix in its instruction — it is sent (and billed) twice", task.Key)
		}
		if n := strings.Count(task.Instruction, "--- SURVIVOR "); n != 1 {
			t.Errorf("task %q names %d survivors, want exactly 1", task.Key, n)
		}
	}
	// Distinct user halves: three seats told to kill the same mutant would be
	// three copies of one measurement.
	seen := map[string]bool{}
	for _, task := range tasks {
		if seen[task.Instruction] {
			t.Fatalf("two writer tasks carry the identical instruction — they are aimed at the same survivor")
		}
		seen[task.Instruction] = true
	}
}

// TestBatchedModeSendsNoSystemHalf: the system half exists for the fan-out's
// shared prefix. A batched run has one seat and nothing to share, so it must
// keep the exact one-user-message request it has always sent.
func TestBatchedModeSendsNoSystemHalf(t *testing.T) {
	d, missionID, _ := fanoutRun(t, WriterModeBatched, nil, nil)
	devTick(t, d, missionID)
	for _, task := range writerTasks(t, d, missionID) {
		if task.System != "" {
			t.Errorf("batched writer task %q carries a system half (%d bytes) — the request shape changed", task.Key, len(task.System))
		}
	}
}

// TestPerSurvivorRepairKeepsTheSharedPrefix: a repaired seat must keep sharing
// its siblings' cacheable prefix byte for byte, or the repair costs a full
// re-bill of the file on top of the retry.
func TestPerSurvivorRepairKeepsTheSharedPrefix(t *testing.T) {
	const badTest = "package target\nthis does not compile\n"
	d, missionID, _ := fanoutRun(t, WriterModePerSurvivor, nil, map[string]bool{badTest: true})
	devTick(t, d, missionID)
	prefix := renderTestWriterPrefix(d.runs[missionID].rs, nil)

	for _, task := range writerTasks(t, d, missionID) {
		claimed := claimTaskByID(t, d.Q, task.ID)
		result := "package target\n\nfunc TestOK(t *testing.T) {}\n"
		if task.Key == "test-writer/m2" {
			result = badTest
		}
		mustComplete(t, d.Q, claimed.ID, result)
	}
	_, _ = d.Tick(context.Background(), missionID)

	for _, task := range writerTasks(t, d, missionID) {
		if task.System != prefix {
			t.Errorf("after a repair, task %q no longer carries the shared prefix as its system half", task.Key)
		}
	}
}

// TestPerSurvivorRepairTouchesOnlyItsOwnSeat: a compile failure on one
// survivor must reissue THAT seat and nothing else. Under the batched shape a
// single bad test reissued the whole file's work; if that survives the
// fan-out, the fan-out buys nothing.
func TestPerSurvivorRepairTouchesOnlyItsOwnSeat(t *testing.T) {
	const badTest = "package target\nthis does not compile\n"
	d, missionID, _ := fanoutRun(t, WriterModePerSurvivor, nil, map[string]bool{badTest: true})
	devTick(t, d, missionID)

	before := map[string]int64{}
	for _, task := range writerTasks(t, d, missionID) {
		before[task.Key] = task.ID
	}

	// Complete every seat; only m2's returns a test that will not compile.
	for _, task := range writerTasks(t, d, missionID) {
		claimed := claimTaskByID(t, d.Q, task.ID)
		result := "package target\n\nfunc TestOK(t *testing.T) {}\n"
		if task.Key == "test-writer/m2" {
			result = badTest
		}
		mustComplete(t, d.Q, claimed.ID, result)
	}
	// The tick reports the compile failure as an error (as the batched path
	// always has) while still reissuing the one seat that failed.
	_, _ = d.Tick(context.Background(), missionID)

	after := map[string]int64{}
	for _, task := range writerTasks(t, d, missionID) {
		after[task.Key] = task.ID
	}
	for _, key := range []string{"test-writer/m1", "test-writer/m3"} {
		if after[key] != before[key] {
			t.Errorf("%s was reissued (task %d -> %d) because a SIBLING failed to compile", key, before[key], after[key])
		}
	}
	if after["test-writer/m2"] == before["test-writer/m2"] {
		t.Error("test-writer/m2 was not reissued after its own compile failure")
	}
}

// driveFanout completes every claimable writer task with the scripted result
// for its key and ticks until the run converges.
func driveFanout(t *testing.T, d *Driver, missionID int64, results map[string]string) Verdict {
	t.Helper()
	for i := 0; i < 40; i++ {
		v, _ := d.Tick(context.Background(), missionID)
		if v != nil {
			return *v
		}
		ready := claimAllReady(t, d.Q)
		if len(ready) == 0 {
			continue
		}
		for key, task := range ready {
			result, ok := results[key]
			if !ok {
				result = "raw"
			}
			mustComplete(t, d.Q, task.ID, result)
		}
	}
	t.Fatal("the run did not converge in 40 ticks")
	return Verdict{}
}

// TestPerSurvivorProvesEachTestAloneAgainstItsOwnSurvivor is the measurement
// claim. Each authored test is scored against ITS mutant and no other, the
// proven count counts each survivor at most once, and the file handed back to
// the operator holds the PROVEN tests only — never the one that never
// compiled.
func TestPerSurvivorProvesEachTestAloneAgainstItsOwnSurvivor(t *testing.T) {
	const (
		testA   = "package target\n\nimport \"testing\"\n\nfunc TestKillsA(t *testing.T) {}\n"
		testC   = "package target\n\nimport \"testing\"\n\nfunc TestKillsC(t *testing.T) {}\n"
		badTest = "package target\nthis does not compile\n"
	)
	kills := map[string]string{testA: "m1", testC: "m3"}
	d, missionID, scorer := fanoutRun(t, WriterModePerSurvivor, kills, map[string]bool{badTest: true})
	devTick(t, d, missionID)

	v := driveFanout(t, d, missionID, map[string]string{
		"test-writer/m1": testA,
		"test-writer/m2": badTest,
		"test-writer/m3": testC,
	})

	if v.WriterMode != WriterModePerSurvivor {
		t.Errorf("Verdict.WriterMode = %q, want %q", v.WriterMode, WriterModePerSurvivor)
	}
	if v.ProvenMissed != 2 {
		t.Errorf("ProvenMissed = %d, want 2 (m1 and m3 proven; m2 never compiled)", v.ProvenMissed)
	}
	if len(v.ProvenMutantIDs) != 2 {
		t.Errorf("ProvenMutantIDs = %v, want exactly m1 and m3", v.ProvenMutantIDs)
	}
	for _, id := range v.ProvenMutantIDs {
		if id == "m2" {
			t.Error("m2 is reported proven — no compiling test was ever authored for it")
		}
	}

	// Every authored proof ran ONE test against ONE mutant, and against the
	// mutant that seat was aimed at.
	if len(scorer.authored) == 0 {
		t.Fatal("no authored pass ran at all")
	}
	for _, call := range scorer.authored {
		if len(call.mutants) != 1 {
			t.Fatalf("an authored pass scored %d mutants at once (%v) — a per-survivor proof is one test against one mutant", len(call.mutants), call.mutants)
		}
		if want, ok := kills[call.test]; ok && call.mutants[0] != want {
			t.Errorf("the test written for %s was proven against %s instead", want, call.mutants[0])
		}
	}

	if !strings.Contains(v.AuthoredTest, "TestKillsA") || !strings.Contains(v.AuthoredTest, "TestKillsC") {
		t.Errorf("AuthoredTest is missing a PROVEN test:\n%s", v.AuthoredTest)
	}
	if strings.Contains(v.AuthoredTest, "this does not compile") {
		t.Errorf("AuthoredTest carries the test that never compiled:\n%s", v.AuthoredTest)
	}
	if n := strings.Count(v.AuthoredTest, "package target"); n != 1 {
		t.Errorf("AuthoredTest has %d package clauses, want 1 — it does not build:\n%s", n, v.AuthoredTest)
	}
}

// TestBatchedModeIsOneTask keeps the old shape reachable and unchanged: one
// call, every survivor as a diff, the whole-file proof loop.
func TestBatchedModeIsOneTask(t *testing.T) {
	const test = "package target\n\nimport \"testing\"\n\nfunc TestAll(t *testing.T) {}\n"
	d, missionID, scorer := fanoutRun(t, WriterModeBatched, map[string]string{test: "m1"}, nil)
	devTick(t, d, missionID)

	tasks := writerTasks(t, d, missionID)
	if len(tasks) != 1 {
		t.Fatalf("batched mode produced %d writer task(s), want exactly 1", len(tasks))
	}
	// The batched key stays the bare role key (SupersedeTask auto-uniquifies
	// a replacement that reuses it, hence the "-rN" suffix) — never a
	// per-survivor key, which is what would say the fan-out leaked into the
	// mode that must not have it.
	if !strings.HasPrefix(tasks[0].Key, RoleTestWriter) || strings.Contains(tasks[0].Key, "/") {
		t.Errorf("batched writer task key = %q, want the bare role key %q (optionally supersede-suffixed)", tasks[0].Key, RoleTestWriter)
	}
	if n := strings.Count(tasks[0].Instruction, "--- SURVIVOR "); n != 3 {
		t.Errorf("the batched instruction names %d survivors, want all 3", n)
	}

	v := driveFanout(t, d, missionID, map[string]string{tasks[0].Key: test})
	if v.WriterMode != WriterModeBatched {
		t.Errorf("Verdict.WriterMode = %q, want %q", v.WriterMode, WriterModeBatched)
	}
	if len(scorer.authored) != 1 {
		t.Fatalf("batched mode ran %d authored passes, want 1", len(scorer.authored))
	}
	if len(scorer.authored[0].mutants) != 3 {
		t.Errorf("the batched authored pass scored %d mutants, want all 3", len(scorer.authored[0].mutants))
	}
}

// TestWriterModeDefaultsToBatchedForAnUnsetRunSpec: a caller that never names
// a mode (the brain, every in-package test) keeps the shape it has always had,
// and its verdict claims no mode at all rather than a mode nobody chose.
func TestWriterModeDefaultsToBatchedForAnUnsetRunSpec(t *testing.T) {
	d, missionID, _ := fanoutRun(t, "", nil, nil)
	devTick(t, d, missionID)
	if tasks := writerTasks(t, d, missionID); len(tasks) != 1 {
		t.Fatalf("an unset WriterMode produced %d writer task(s), want 1", len(tasks))
	}
}

// TestResolveWriterModeRejectsAnUnknownSpelling: the flag is a closed set. A
// typo must fail loudly, never fall back to a default the operator did not
// ask for — the mode changes what a run costs and what its verdict discloses.
func TestResolveWriterModeRejectsAnUnknownSpelling(t *testing.T) {
	for _, in := range []string{"per-survivor", "batched", "PER-SURVIVOR", " batched "} {
		if _, err := ResolveWriterMode(in); err != nil {
			t.Errorf("ResolveWriterMode(%q) = %v, want it accepted", in, err)
		}
	}
	if got, err := ResolveWriterMode(""); err != nil || got != WriterModePerSurvivor {
		t.Errorf("ResolveWriterMode(\"\") = %q, %v — an unset flag takes the default", got, err)
	}
	if _, err := ResolveWriterMode("bogus"); err == nil {
		t.Error("ResolveWriterMode(\"bogus\") was accepted")
	}
}

// TestPerSurvivorTaskKeyIsUniquePerMutant guards the queue's own invariant:
// task keys are unique per mission, so two survivors may never collide onto
// one key — one seat would silently disappear and its survivor would go
// unattacked while the verdict still counted it.
func TestPerSurvivorTaskKeyIsUniquePerMutant(t *testing.T) {
	seen := map[string]bool{}
	for _, id := range []string{"m1", "s0/m1", "s1/m1"} {
		key := TestWriterTaskKey(id)
		if seen[key] {
			t.Fatalf("two mutant ids collide onto the writer task key %q", key)
		}
		seen[key] = true
		if !strings.HasPrefix(key, RoleTestWriter+"/") {
			t.Errorf("key %q does not carry the role prefix", key)
		}
		if got := ShadowTestWriterTaskKey(id); !strings.HasPrefix(got, RoleTestWriterShadow+"/") {
			t.Errorf("shadow key %q does not carry the challenger role prefix", got)
		}
	}
}

// TestUnmergeableProvenTestsRideTheVerdict: two seats each proved their own
// survivor, and both declared the same helper. The concatenator will not
// rename a helper (its call sites are the model's own code), so one part
// cannot join the merged file — and it must NOT be dropped: it is a test that
// was written, compiled and RUN to kill the survivor it names, which is
// exactly what ProvenMissed counts.
func TestUnmergeableProvenTestsRideTheVerdict(t *testing.T) {
	const (
		testA = "package target\n\nimport \"testing\"\n\nfunc helper() int { return 1 }\n\nfunc TestKillsA(t *testing.T) { _ = helper() }\n"
		testB = "package target\n\nimport \"testing\"\n\nfunc helper() int { return 2 }\n\nfunc TestKillsB(t *testing.T) { _ = helper() }\n"
		testC = "package target\n\nimport \"testing\"\n\nfunc TestKillsC(t *testing.T) {}\n"
	)
	kills := map[string]string{testA: "m1", testB: "m2", testC: "m3"}
	d, missionID, _ := fanoutRun(t, WriterModePerSurvivor, kills, nil)
	devTick(t, d, missionID)

	v := driveFanout(t, d, missionID, map[string]string{
		"test-writer/m1": testA,
		"test-writer/m2": testB,
		"test-writer/m3": testC,
	})

	if v.ProvenMissed != 3 {
		t.Fatalf("ProvenMissed = %d, want 3 — an unmergeable proof is still a proof", v.ProvenMissed)
	}
	if len(v.AuthoredExtra) != 1 || v.AuthoredExtra[0].MutantID != "m2" {
		t.Fatalf("AuthoredExtra = %+v, want exactly the part that could not merge", v.AuthoredExtra)
	}
	if strings.TrimSpace(v.AuthoredExtra[0].Reason) == "" {
		t.Error("the carried-out part says nothing about WHY it is separate")
	}
	if !strings.Contains(v.AuthoredExtra[0].Source, "TestKillsB") {
		t.Errorf("the carried-out part lost its source: %q", v.AuthoredExtra[0].Source)
	}
	if strings.Contains(v.AuthoredTest, "TestKillsB") {
		t.Errorf("the unmergeable part was merged anyway:\n%s", v.AuthoredTest)
	}
	for _, want := range []string{"TestKillsA", "TestKillsC"} {
		if !strings.Contains(v.AuthoredTest, want) {
			t.Errorf("the merged file lost %s:\n%s", want, v.AuthoredTest)
		}
	}

	// And the one artifact the ledger stores holds every proof, behind a
	// separator that says plainly it is a record and not a file to run.
	rec := v.AuthoredRecord()
	for _, want := range []string{"TestKillsA", "TestKillsB", "TestKillsC", "separate test file (unmergeable) — m2"} {
		if !strings.Contains(rec, want) {
			t.Errorf("the authored record is missing %q:\n%s", want, rec)
		}
	}

	// RunStatus is what `certify --local` prints from, so the extras must
	// reach it too or the local path drops a proof the repo path shows.
	st, ok := d.RunStatus(missionID)
	if !ok || len(st.AuthoredExtra) != 1 {
		t.Fatalf("RunStatus.AuthoredExtra = %+v, want the one carried-out part", st.AuthoredExtra)
	}
}

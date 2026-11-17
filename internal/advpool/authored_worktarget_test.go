// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"context"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/sandbox"
)

// A test that names the sandbox's working directory passes in every jailed
// run (every run sees its tree at sandbox.WorkTarget) and fails on the
// developer's machine, where that directory does not exist. Before the fixed
// path, a random temp path made such a test fail its next run; now nothing
// would. So it is rejected before it is compiled or proven, and the writer is
// told why in plain words.
const (
	workTargetTest = "package target\n\nimport (\n\t\"os\"\n\t\"testing\"\n)\n\nfunc TestKillsA(t *testing.T) {\n\t_, _ = os.ReadFile(\"" + sandbox.WorkTarget + "/testdata/a.txt\")\n}\n"
	cleanTest      = "package target\n\nimport (\n\t\"os\"\n\t\"testing\"\n)\n\nfunc TestKillsA(t *testing.T) {\n\t_, _ = os.ReadFile(\"testdata/a.txt\")\n}\n"
)

func TestAuthoredTestNamingTheWorkTargetIsRejected(t *testing.T) {
	if err := checkWorkTarget(workTargetTest); err == nil || !strings.Contains(err.Error(), "does not exist on the developer's machine") {
		t.Errorf("checkWorkTarget(path test) = %v, want the plain-words rejection", err)
	}
	if err := checkWorkTarget(cleanTest); err != nil {
		t.Errorf("checkWorkTarget(clean test) = %v, want nil", err)
	}
}

// writerScript completes every ready writer task: the FIRST completion of a
// seat in first gets first's result, every later one gets later's. Other
// tasks get "raw". It returns the instructions the writer was handed, by seat,
// in order.
func driveWriterScript(t *testing.T, d *Driver, missionID int64, seat, first, later string) (Verdict, []string) {
	t.Helper()
	var instrs []string
	served := false
	for i := 0; i < 40; i++ {
		v, _ := d.Tick(context.Background(), missionID)
		if v != nil {
			return *v, instrs
		}
		for key, task := range claimAllReady(t, d.Q) {
			result := "raw"
			if seatKeyBase(key) == seat {
				instrs = append(instrs, task.Instruction)
				result = later
				if !served {
					result, served = first, true
				}
			}
			mustComplete(t, d.Q, task.ID, result)
		}
	}
	t.Fatal("the run did not converge in 40 ticks")
	return Verdict{}, nil
}

func assertWorkTargetRejected(t *testing.T, v Verdict, instrs []string, scorer *fanoutScorer) {
	t.Helper()
	if len(instrs) < 2 {
		t.Fatalf("the writer was asked %d time(s), want the rejected attempt reissued", len(instrs))
	}
	retry := instrs[1]
	for _, want := range []string{"sandbox's working directory", "does not exist on the developer's machine", "relative to the test file or the repository", sandbox.WorkTarget + "/testdata/a.txt"} {
		if !strings.Contains(retry, want) {
			t.Errorf("the reissued instruction does not carry %q:\n%s", want, retry)
		}
	}
	if strings.Contains(retry, "DID NOT COMPILE") || strings.Contains(retry, "The compiler reported") {
		t.Errorf("the rejection is worded as a compile error, which it is not:\n%s", retry)
	}
	for _, c := range scorer.authored {
		if strings.Contains(c.test, sandbox.WorkTarget) {
			t.Errorf("the test naming %s was proven against %v; it must never be", sandbox.WorkTarget, c.mutants)
		}
	}
	if strings.Contains(v.AuthoredTest, sandbox.WorkTarget) {
		t.Errorf("the handed-back test names %s:\n%s", sandbox.WorkTarget, v.AuthoredTest)
	}
	if !strings.Contains(v.AuthoredTest, "testdata/a.txt") {
		t.Errorf("the clean second attempt was not accepted:\n%s", v.AuthoredTest)
	}
}

// The default per-survivor writer: the seat is reissued with the reason, and
// counts the rejection as one of its attempts.
func TestPerSurvivorWriterRejectsATestNamingTheWorkTarget(t *testing.T) {
	d, missionID, scorer := fanoutRun(t, WriterModePerSurvivor, map[string]string{cleanTest: "m1"}, nil)
	devTick(t, d, missionID)
	v, instrs := driveWriterScript(t, d, missionID, "test-writer/m1", workTargetTest, cleanTest)
	assertWorkTargetRejected(t, v, instrs, scorer)
	if a := d.runs[missionID].writerAttempts["m1"]; a.repairs != 1 {
		t.Errorf("seat m1 counted %d repair(s), want the rejection counted as 1", a.repairs)
	}
	found := false
	for _, id := range v.ProvenMutantIDs {
		found = found || id == "m1"
	}
	if !found {
		t.Errorf("ProvenMutantIDs = %v, want m1 proven by the clean test", v.ProvenMutantIDs)
	}
}

// The batched writer: the same rejection, the same reason, one attempt spent.
func TestBatchedWriterRejectsATestNamingTheWorkTarget(t *testing.T) {
	d, missionID, scorer := fanoutRun(t, WriterModeBatched, map[string]string{cleanTest: "m1"}, nil)
	devTick(t, d, missionID)
	v, instrs := driveWriterScript(t, d, missionID, RoleTestWriter, workTargetTest, cleanTest)
	assertWorkTargetRejected(t, v, instrs, scorer)
	if n := d.runs[missionID].testWriterAttempts; n != 1 {
		t.Errorf("testWriterAttempts = %d, want the rejection counted as 1", n)
	}
}

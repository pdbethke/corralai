// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
)

// togetherScorer is a fanoutScorer that can also run one test file against
// the unmutated code, recording what it was handed.
type togetherScorer struct {
	*fanoutScorer
	pass bool
	out  string
	ran  []string
}

func (s *togetherScorer) CompliantRun(_ context.Context, _, _, test, _ string) (bool, string, error) {
	s.ran = append(s.ran, test)
	return s.pass, s.out, nil
}

const (
	togetherTestA = "package target\n\nimport \"testing\"\n\nfunc TestKillsA(t *testing.T) {}\n"
	togetherTestC = "package target\n\nimport \"testing\"\n\nfunc TestKillsC(t *testing.T) {}\n"
)

func togetherRun(t *testing.T, pass bool, out string) (Verdict, *togetherScorer) {
	t.Helper()
	kills := map[string]string{togetherTestA: "m1", togetherTestC: "m3"}
	d, missionID, fs := fanoutRun(t, WriterModePerSurvivor, kills, nil)
	ts := &togetherScorer{fanoutScorer: fs, pass: pass, out: out}
	d.Scorer = ts
	devTick(t, d, missionID)
	v := driveFanout(t, d, missionID, map[string]string{
		"test-writer/m1": togetherTestA,
		"test-writer/m3": togetherTestC,
	})
	return v, ts
}

// Each authored test is proven ALONE, so two that each pass alone can still
// break each other in one suite — on Warp's secret_redaction, ten proven
// tests swapped one process-wide regex and one failed when the file ran in
// parallel. The proven file is run once, whole, on the unmutated code, and
// the verdict says what that showed.
func TestProvenTestsAreRunTogetherOnTheUnmutatedCode(t *testing.T) {
	v, ts := togetherRun(t, false, "test corral_tests::b ... FAILED\nassertion failed: found.len() == 2")
	if len(ts.ran) != 1 {
		t.Fatalf("the proven file must be run together exactly once, ran %d times", len(ts.ran))
	}
	if ts.ran[0] != v.AuthoredTest {
		t.Errorf("the together run must be the merged file the operator is handed, got:\n%s", ts.ran[0])
	}
	if v.AuthoredTogether != AuthoredTogetherFail {
		t.Errorf("AuthoredTogether = %q, want %q", v.AuthoredTogether, AuthoredTogetherFail)
	}
	if !strings.Contains(v.AuthoredTogetherOutput, "FAILED") {
		t.Errorf("the runner's output must ride the verdict, got %q", v.AuthoredTogetherOutput)
	}
	if v.ProvenMissed != 2 {
		t.Errorf("ProvenMissed = %d — the together run is a disclosure, it must not change the proven count", v.ProvenMissed)
	}
}

func TestProvenTestsThatPassTogetherSaySo(t *testing.T) {
	v, _ := togetherRun(t, true, "ok")
	if v.AuthoredTogether != AuthoredTogetherPass {
		t.Errorf("AuthoredTogether = %q, want %q", v.AuthoredTogether, AuthoredTogetherPass)
	}
	if v.AuthoredTogetherOutput != "" {
		t.Errorf("a passing together run carries no output, got %q", v.AuthoredTogetherOutput)
	}
}

// A scorer that cannot run a file leaves the field EMPTY — not checked —
// never "pass": an absent check must not read as a clean one.
func TestTogetherIsUncheckedWhenTheScorerCannotRunIt(t *testing.T) {
	kills := map[string]string{togetherTestA: "m1", togetherTestC: "m3"}
	d, missionID, _ := fanoutRun(t, WriterModePerSurvivor, kills, nil)
	devTick(t, d, missionID)
	v := driveFanout(t, d, missionID, map[string]string{
		"test-writer/m1": togetherTestA,
		"test-writer/m3": togetherTestC,
	})
	if v.AuthoredTogether != "" {
		t.Errorf("AuthoredTogether = %q, want empty (not checked)", v.AuthoredTogether)
	}
}

// One proven test has nothing to run together with: its own proof already
// ran it on the unmutated code.
func TestOneProvenTestIsNotRunAgain(t *testing.T) {
	kills := map[string]string{togetherTestA: "m1"}
	d, missionID, fs := fanoutRun(t, WriterModePerSurvivor, kills, nil)
	ts := &togetherScorer{fanoutScorer: fs, pass: false}
	d.Scorer = ts
	devTick(t, d, missionID)
	v := driveFanout(t, d, missionID, map[string]string{"test-writer/m1": togetherTestA})
	if len(ts.ran) != 0 || v.AuthoredTogether != "" {
		t.Errorf("one proven test must not be re-run (ran %d, AuthoredTogether %q)", len(ts.ran), v.AuthoredTogether)
	}
}

// The timeout verdict is the second construction site; a together result
// measured before the run stalled must reach it too.
func TestTimeoutVerdictCarriesTheTogetherResult(t *testing.T) {
	d := &Driver{}
	run := &runState{
		rs:                     RunSpec{Repo: "r", Commit: "c", Lang: "go"},
		authoredTogether:       AuthoredTogetherFail,
		authoredTogetherOutput: "boom",
	}
	v := d.timeoutVerdict(run)
	if v.AuthoredTogether != AuthoredTogetherFail || v.AuthoredTogetherOutput != "boom" {
		t.Errorf("timeout verdict dropped the together result: %q %q", v.AuthoredTogether, v.AuthoredTogetherOutput)
	}
}

// The real run, through the workspace runner and `go test`: a file whose two
// tests each pass alone but share a package variable fails when run whole —
// the shape of the Warp collision, in Go.
func TestJailScorerCompliantRunReportsAFailingFile(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain on PATH")
	}
	s := JailScorer{Jail: adequacy.NewWorkspaceRunner(t.TempDir(), 2*time.Minute)}
	const code = "package target\n\nvar Shared = 0\n\nfunc Bump() int { Shared++; return Shared }\n"
	alone := "package target\n\nimport \"testing\"\n\nfunc TestFirst(t *testing.T) {\n\tif Bump() != 1 {\n\t\tt.Fatal(\"first\")\n\t}\n}\n"
	pass, out, err := s.CompliantRun(context.Background(), "target.go", code, alone, "")
	if err != nil || !pass {
		t.Fatalf("one test alone must pass: pass=%v err=%v\n%s", pass, err, out)
	}
	together := alone + "\nfunc TestSecond(t *testing.T) {\n\tif Bump() != 1 {\n\t\tt.Fatal(\"second saw shared state\")\n\t}\n}\n"
	pass, out, err = s.CompliantRun(context.Background(), "target.go", code, together, "")
	if err != nil {
		t.Fatal(err)
	}
	if pass || !strings.Contains(out, "second saw shared state") {
		t.Fatalf("two interfering tests must fail together with the runner's output: pass=%v\n%s", pass, out)
	}
}

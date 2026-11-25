// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/sandbox"
)

// fakeRecordRunner stands in, as a POSIX sh script in the project, for a
// runner that writes its own JUnit record to a file its command line names:
// PHPUnit's `--log-junit <file>`, and cargo-nextest's `--tool-config-file
// corral:<config>`, whose config's junit `path` names the file (the
// config is the companion file corral's wrapper writes with printf). It
// prints nothing that names a test, so only the record can name one. The
// one test it runs (@CLASS@ / @NAME@, filled in per case) passes when the
// source (@CODE@) is byte for byte the project's own, kept beside it as
// fake-orig, and fails on any change: a mutant, or the positive control's
// canary.
const fakeRecordRunner = `log=
while [ $# -gt 0 ]; do
  case $1 in
    --log-junit) log=$2; shift ;;
    corral:*) log=$(sed -n "s/^path = '\(.*\)'$/\1/p" "${1#corral:}") ;;
  esac
  shift
done
if ! cmp -s @CODE@ fake-orig; then
  echo "F    1 / 1 (100%)"
  [ -n "$log" ] && printf '<testsuites><testsuite name="t"><testcase name="%s" class="%s" classname="%s"><failure>no</failure></testcase></testsuite></testsuites>\n' "@NAME@" "@CLASS@" "@CLASS@" > "$log"
  exit 1
fi
echo ".    1 / 1 (100%)"
[ -n "$log" ] && printf '<testsuites><testsuite name="t"><testcase name="%s" class="%s" classname="%s"/></testsuite></testsuites>\n' "@NAME@" "@CLASS@" "@CLASS@" > "$log"
exit 0
`

// recordRunnerCase is one record-file row driven by fakeRecordRunner.
type recordRunnerCase struct {
	name, langName, runner, codePath, testPath, code, test string
	argv                                                   []string
	class, testName, wantID                                string
	mutant                                                 adequacy.Mutant
}

var recordRunnerCases = []recordRunnerCase{
	{
		name: "phpunit", langName: "php", runner: "phpunit",
		codePath: "src/Calc.php", testPath: "tests/CalcTest.php",
		code:  "<?php\nclass Calc { public static function add($a, $b) { return $a + $b; } }\n",
		test:  "<?php\nclass CalcTest { public function testAdd() {} }\n",
		argv:  []string{"sh", "./phpunit", "tests"},
		class: "CalcTest", testName: "testAdd", wantID: "CalcTest::testAdd",
		mutant: adequacy.Mutant{ID: "m1", Search: "return $a + $b", Replace: "return $a - $b"},
	},
	{
		name: "nextest", langName: "rust", runner: "cargo",
		codePath: "src/lib.rs", testPath: "tests/calc.rs",
		code:  "pub fn add(a: i32, b: i32) -> i32 {\n    a + b\n}\n",
		test:  "#[test]\nfn adds() {}\n",
		argv:  []string{"sh", "./cargo", "nextest", "run"},
		class: "calc", testName: "adds", wantID: "calc adds",
		mutant: adequacy.Mutant{ID: "m1", Search: "a + b", Replace: "a - b"},
	},
}

// recordRunnerRepo writes c's project, its stand-in runner included.
func recordRunnerRepo(t *testing.T, c recordRunnerCase) (string, repoSeed) {
	t.Helper()
	dir := t.TempDir()
	runner := strings.NewReplacer("@CLASS@", c.class, "@NAME@", c.testName, "@CODE@", c.codePath).Replace(fakeRecordRunner)
	files := map[string]string{c.codePath: c.code, c.testPath: c.test, c.runner: runner, "fake-orig": c.code}
	for rel, body := range files {
		mustWrite(t, filepath.Join(dir, rel), body)
	}
	return dir, repoSeed{seedDir: dir, files: files, cleanup: func() {}}
}

// wireRecordRunner builds c's grading wiring on one substrate, "workspace"
// or "jail" (bwrap; skipped by name where there is none).
func wireRecordRunner(t *testing.T, c recordRunnerCase, substrate string) (jailWiring, repoSeed) {
	t.Helper()
	var iso sandbox.Isolator
	if substrate == "jail" {
		b, err := sandbox.Resolve(sandbox.Config{Backend: "bwrap"})
		if err != nil {
			t.Skipf("SKIP: no bwrap backend: %v", err)
		}
		iso = b
	}
	repoDir, seed := recordRunnerRepo(t, c)
	in := jailWiringInput{
		iso: iso, timeout: 2 * time.Minute,
		codePath: c.codePath, testPath: c.testPath, repoDir: repoDir, langName: c.langName,
		fsPath:    func(q string) string { return filepath.Join(repoDir, q) },
		code:      []byte(c.code),
		devTest:   []byte(c.test),
		checkArgv: c.argv, inv: lang.Cmd(c.argv),
		stdout: io.Discard, seed: &seed,
	}
	if substrate == "workspace" {
		in.substrate = substrateWorkspace
	}
	w, err := buildJailWiring(context.Background(), in)
	if err != nil {
		t.Fatalf("buildJailWiring(%s): %v", substrate, err)
	}
	t.Cleanup(w.cleanup)
	return w, seed
}

// A record-file hook works through corral's real run path, in the real jail
// and on the workspace substrate: the graded command is the row's hook
// around the operator's command (gradingRewrite), the wrapper makes its
// directory with mktemp -d (in the jail under the jail's own /tmp, the
// host's TMPDIR withheld), writes nextest's companion config with printf,
// reads the record's last byte with tail -c and prints the block, the
// block survives the door's output capture, and the row's reader names the
// failing test from it alone (the stand-in prints no test's name). The
// clean-code run of a failing test (CompliantRun) shows the block and the
// id; on PHPUnit the mutant path names the killing test from the record
// too, and with the rewrite taken away it names none: the control, showing
// the printed lines name nothing.
func TestARecordFileHookNamesTheFailingTestThroughTheRealRunPath(t *testing.T) {
	for _, c := range recordRunnerCases {
		for _, substrate := range []string{"workspace", "jail"} {
			t.Run(c.name+"/"+substrate, func(t *testing.T) {
				w, seed := wireRecordRunner(t, c, substrate)
				ctx := context.Background()
				cmd := strings.Join(c.argv, " ")
				plug, _ := lang.ByName(c.langName)
				failedTests, ok := lang.DeselectorFor(plug, lang.Cmd(c.argv))
				if !ok {
					t.Fatalf("%s has no failed-tests reader", c.name)
				}

				if pass, out, err := w.scorer.CompliantRun(ctx, w.codeKey, c.code, c.test, cmd); err != nil || !pass {
					if substrate == "jail" {
						t.Skipf("SKIP: the jail did not pass the stand-in suite on unmutated code (jail unavailable): %v\n%s", err, out)
					}
					t.Fatalf("clean run: pass=%v err=%v\n%s", pass, err, out)
				}
				failing := strings.Replace(c.code, c.mutant.Search, c.mutant.Replace, 1)
				pass, out, err := w.scorer.CompliantRun(ctx, w.codeKey, failing, c.test, cmd)
				if err != nil {
					t.Fatal(err)
				}
				if pass {
					t.Fatalf("the stand-in passed on the subtract mutant:\n%s", out)
				}
				if format, body, ok := lang.RecordBlock(out); !ok || format != "junit" || !strings.Contains(body, c.testName) {
					t.Fatalf("RecordBlock = %q, %q, %v: the record did not reach the reader, from:\n%s", format, body, ok, out)
				}
				if strings.Contains(out, "could not") {
					t.Errorf("the wrapper fell back:\n%s", out)
				}
				if got := failedTests(out); len(got) != 1 || got[0] != c.wantID {
					t.Errorf("failing = %q, want [%q], from:\n%s", got, c.wantID, out)
				}
				if got := failedTests(lang.PrintedOutput(out)); len(got) != 0 {
					t.Errorf("the printed lines alone name %q; the case must rest on the record", got)
				}

				if c.name != "phpunit" {
					return
				}
				for _, control := range []bool{false, true} {
					if control {
						w.scorer.CommandRewrite = nil
					}
					rep, err := w.scorer.ScoreReport(ctx, w.codeKey, seed.files[c.codePath], seed.files[c.testPath], []adequacy.Mutant{c.mutant}, cmd)
					if err != nil {
						t.Fatal(err)
					}
					if !rep.CompliantPass {
						t.Fatalf("control=%v: baseline failed: %s", control, rep.BaselineOutput)
					}
					g := rep.PerMutant["m1"]
					switch {
					case control && g.KilledBy != "":
						t.Errorf("control: KilledBy=%q, want none: the stand-in's printed lines must name nothing", g.KilledBy)
					case !control && (g.KilledBy != c.wantID || g.KillConfirmation != adequacy.KillConfirmedAlone):
						t.Errorf("KilledBy=%q KillConfirmation=%q, want %q and %q; report %+v", g.KilledBy, g.KillConfirmation, c.wantID, adequacy.KillConfirmedAlone, rep)
					}
				}
			})
		}
	}
}

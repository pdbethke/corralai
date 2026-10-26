// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// scriptedJail fails a compile command by rule, so a test can say which
// commands the UNMODIFIED source passes. Every `-l <file>` lint fails when
// that file's content holds "BROKEN"; the load command (`-r`) fails whenever
// loadFails says so for the workspace it is given.
type scriptedJail struct {
	loadFails func(ws map[string]string) bool
	loads     int
}

func (j *scriptedJail) RunTest(ctx context.Context, ws map[string]string, cmd []string) (bool, error) {
	ok, _, err := j.RunTestVerbose(ctx, ws, cmd)
	return ok, err
}

func (j *scriptedJail) RunTestVerbose(_ context.Context, ws map[string]string, cmd []string) (bool, string, error) {
	switch {
	case slices.Contains(cmd, "-l"):
		if strings.Contains(ws[cmd[len(cmd)-1]], "BROKEN") {
			return false, "PHP Parse error", nil
		}
		return true, "No syntax errors detected", nil
	case slices.Contains(cmd, "-r"):
		j.loads++
		if j.loadFails(ws) {
			return false, `PHP Fatal error:  Uncaught Error: Trait "Webmozart\Assert\Mixin" not found`, nil
		}
		return true, "", nil
	}
	return true, "", nil
}

const assertPHP = "<?php\nnamespace Webmozart\\Assert;\nclass Assert { use Mixin; }\n"

// TestCompileTestIgnoresACheckTheUnmodifiedSourceFails is the writer's side
// of the rule adequacy.Score applies to mutants (#416): a compile check that
// rejects the UNMODIFIED source judges nothing about the authored test.
// webmozart/assert, 2026-10-15: the load check could not find a trait, the
// mutant gate switched itself off for that reason, and the writer's gate,
// running the same command, refused all three of the writer's tests as "does
// not compile" — so a PHP survivor could never be proven.
func TestCompileTestIgnoresACheckTheUnmodifiedSourceFails(t *testing.T) {
	j := &scriptedJail{loadFails: func(map[string]string) bool { return true }}
	v := JailValidator{Jail: j}
	test := "<?php\nclass AssertCorralTest extends \\PHPUnit\\Framework\\TestCase {}\n"
	if err := v.CompileTest(context.Background(), "src/Assert.php", assertPHP, test); err != nil {
		t.Fatalf("a load check the unmodified source cannot pass refused the authored test: %v", err)
	}
	if j.loads != 2 {
		t.Fatalf("load check ran %d time(s), want 2 — once with the test, once without it to learn it fails regardless", j.loads)
	}
}

// The test's OWN check is never excused: a broken test is refused even when
// another command in the sequence cannot judge anything.
func TestCompileTestStillRefusesABrokenTest(t *testing.T) {
	v := JailValidator{Jail: &scriptedJail{loadFails: func(map[string]string) bool { return true }}}
	err := v.CompileTest(context.Background(), "src/Assert.php", assertPHP, "<?php BROKEN")
	var ce *CompileError
	if !errors.As(err, &ce) {
		t.Fatalf("a test that fails its own lint = %v, want a *CompileError", err)
	}
}

// A check the unmodified source PASSES keeps its say: when only the authored
// test makes it fail, the rejection stands.
func TestCompileTestKeepsACheckOnlyTheTestBreaks(t *testing.T) {
	v := JailValidator{Jail: &scriptedJail{loadFails: func(ws map[string]string) bool {
		for _, body := range ws {
			if strings.Contains(body, "POISON") {
				return true
			}
		}
		return false
	}}}
	err := v.CompileTest(context.Background(), "src/Assert.php", assertPHP, "<?php // POISON\n")
	var ce *CompileError
	if !errors.As(err, &ce) {
		t.Fatalf("a check only the authored test fails = %v, want a *CompileError", err)
	}
}

// tscJail answers a TypeScript-shaped compile command — ONE `sh -c` script
// with both paths inside it, never as argv elements — the way tsc does: a
// file the script names that is absent is an error, and so is a type error
// in a file it names, reported against that file's path.
type tscJail struct{ files []string }

func (j tscJail) RunTest(ctx context.Context, ws map[string]string, cmd []string) (bool, error) {
	ok, _, err := j.RunTestVerbose(ctx, ws, cmd)
	return ok, err
}

func (j tscJail) RunTestVerbose(_ context.Context, ws map[string]string, cmd []string) (bool, string, error) {
	script := strings.Join(cmd, " ")
	var out []string
	for _, p := range j.files {
		if !strings.Contains(script, p) {
			continue
		}
		body, ok := ws[p]
		switch {
		case !ok:
			out = append(out, "error TS6053: File '"+p+"' not found.")
		case strings.Contains(body, "BADTYPE"):
			out = append(out, p+"(15,23): error TS2352: Conversion of type may be a mistake.")
		}
	}
	return len(out) == 0, strings.Join(out, "\n"), nil
}

// AN AUTHORED TEST THAT DOES NOT TYPE-CHECK IS REFUSED (REA, 2026-10-26).
// TypeScript's check is one script that names the test inside it, so the
// argv check below never saw the test named; re-run without the test, tsc
// failed on the missing file, the failure was excused as "the unmodified
// source fails too", and tests with TS2352 in their own lines were graded by
// execution (vitest strips types) and counted as proven. A failure the
// compiler reports AGAINST THE TEST FILE is the test's own and is never
// excused.
func TestCompileTestRefusesATypeErrorInTheTestItself(t *testing.T) {
	testPath := advPoolTestPath("src/Service.ts")
	v := JailValidator{Jail: tscJail{files: []string{"src/Service.ts", testPath}}}
	err := v.CompileTest(context.Background(), "src/Service.ts", "export const s = 1;\n", "const x = {} as BADTYPE;\n")
	var ce *CompileError
	if !errors.As(err, &ce) || !strings.Contains(ce.Output, testPath+"(15,23)") {
		t.Fatalf("a test with a type error in its own lines = %v, want a *CompileError carrying tsc's output", err)
	}
	// A source that does not type-check by itself still excuses the check,
	// as for PHP: the output names only the source.
	if err := v.CompileTest(context.Background(), "src/Service.ts", "export const s = BADTYPE;\n", "import { s } from '../src/Service.ts';\n"); err != nil {
		t.Fatalf("a check only the source fails refused a sound test: %v", err)
	}
}

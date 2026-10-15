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

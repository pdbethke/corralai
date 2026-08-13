// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The demo's entire value is that it cannot fail for environmental reasons, so
// the fixture has to compile and its suite has to PASS on unmutated code. If it
// does not, corral reports COULD-NOT-GRADE and the newcomer's first impression
// is the exact failure this command exists to route around — auditing real
// repositories took six attempts to produce one verdict, five lost to the
// environment.
func TestDemoProjectCompilesAndItsBaselinePasses(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain on PATH")
	}
	dir := t.TempDir()
	if err := writeDemoProject(dir); err != nil {
		t.Fatalf("writeDemoProject: %v", err)
	}

	cmd := exec.Command("go", "test", "./...")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the demo's own suite must pass on unmutated code, or every demo run reports COULD-NOT-GRADE: %v\n%s", err, out)
	}
}

// The fixture is only interesting if the test is genuinely thin against the
// goal. These pin the gap the audit is meant to find: three of the five clauses
// are never asserted, so a mutant that removes one of them survives the suite.
// If someone "improves" the test later, the demo quietly stops demonstrating
// anything and this fails instead.
func TestDemoTestLeavesRealGaps(t *testing.T) {
	for _, clause := range []string{"upper", "lower", "digit", "symbol"} {
		if !strings.Contains(demoSource, clause) {
			t.Fatalf("the demo source must enforce the %q clause", clause)
		}
	}
	// The suite asserts exactly two things: one acceptance, one length
	// rejection. Nothing about uppercase, lowercase, digits or symbols.
	for _, unasserted := range []string{"NoDigits", "nouppercase", "NOLOWERCASE", "NoSymbol"} {
		if strings.Contains(demoTest, unasserted) {
			t.Fatalf("demoTest asserts %q — the fixture is supposed to leave those clauses untested", unasserted)
		}
	}
	if n := strings.Count(demoTest, "func Test"); n != 2 {
		t.Fatalf("demoTest has %d tests, want exactly 2 — a thicker suite stops demonstrating the gap", n)
	}
}

// corral has no default models anywhere, and the demo is not an exception: it
// must refuse rather than pick a vendor on the newcomer's behalf, and the
// refusal must show a complete command rather than a bare flag error.
func TestDemoRefusesWithoutModels(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runDemo(nil, &out, &errb); code == 0 {
		t.Fatal("demo with no models named must refuse — corral has no default models")
	}
	msg := errb.String()
	for _, want := range []string{"--writer-model", "--mutant-model", "no default models"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal must mention %q; got:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "claude-") || strings.Contains(msg, "gemini-") {
		t.Errorf("the refusal must not name a model — that reintroduces a default through the message; got:\n%s", msg)
	}
}

// The demo writes into a directory the caller can inspect afterwards, because
// reading the thin test is half of understanding the verdict.
func TestDemoProjectWritesTheThreeFiles(t *testing.T) {
	dir := t.TempDir()
	if err := writeDemoProject(dir); err != nil {
		t.Fatalf("writeDemoProject: %v", err)
	}
	for _, f := range []string{"go.mod", "passwd.go", "passwd_test.go"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("expected %s in the demo project: %v", f, err)
		}
	}
}

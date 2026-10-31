// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"slices"
	"strings"
	"testing"
)

// A toolchain that cannot be named cannot be recorded, so every registered
// language must answer the command that prints its interpreter's version.
func TestEveryPluginAnswersAVersionCommand(t *testing.T) {
	for _, name := range []string{"go", "python", "javascript", "typescript", "ruby", "php", "rust"} {
		p, ok := ByName(name)
		if !ok {
			t.Fatalf("language %q is not registered", name)
		}
		if cmd := p.VersionCmd(); len(cmd) == 0 || cmd[0] == "" {
			t.Errorf("%s: VersionCmd() = %q, want a command", name, cmd)
		}
	}
}

func TestVitestRowReadsItsVersionFromTheEntryScript(t *testing.T) {
	p, _ := ByName("typescript")
	r, ok := RunnerFor(p, Cmd([]string{"node", "./node_modules/vitest/vitest.mjs", "run"}))
	if !ok || r.ID != "vitest" || r.VersionCmd == nil {
		t.Fatalf("RunnerFor = %q, ok=%v, VersionCmd nil=%v: want the vitest row with a version command", r.ID, ok, r.VersionCmd == nil)
	}
	got, ok := r.VersionCmd([]string{"node", "./node_modules/vitest/vitest.mjs", "run"})
	if want := []string{"node", "./node_modules/vitest/vitest.mjs", "--version"}; !ok || !slices.Equal(got, want) {
		t.Errorf("VersionCmd = %q, %v, want %q", got, ok, want)
	}
	// vitest the word, with flags after it, keeps what launches it.
	got, ok = r.VersionCmd([]string{"vitest", "run", "--pool=threads"})
	if want := []string{"vitest", "--version"}; !ok || !slices.Equal(got, want) {
		t.Errorf("VersionCmd(vitest word) = %q, %v, want %q", got, ok, want)
	}
	if _, ok := r.VersionCmd([]string{"npm", "test"}); ok {
		t.Error("a package script names no vitest binary: the row must refuse, not guess")
	}
}

func TestPytestRowReadsItsVersionBehindItsLauncher(t *testing.T) {
	p, _ := ByName("python")
	r, ok := RunnerFor(p, Cmd([]string{"python3", "-m", "pytest", "-q"}))
	if !ok || r.ID != "pytest" || r.VersionCmd == nil {
		t.Fatalf("want the pytest row with a version command, got %q ok=%v", r.ID, ok)
	}
	got, _ := r.VersionCmd([]string{"python3", "-m", "pytest", "-q"})
	if want := []string{"python3", "-m", "pytest", "--version"}; !slices.Equal(got, want) {
		t.Errorf("VersionCmd = %q, want %q", got, want)
	}
	if _, ok := r.VersionCmd([]string{"sh", "-c", "pytest -q"}); ok {
		t.Error("a shell-wrapped command hides the binary: the row must refuse")
	}
}

// The captured outputs are the real programs' own, so a parser or a runner
// that disagrees with what they print fails here.
func TestCapturedVersionOutputsStartWithTheirVersion(t *testing.T) {
	for file, want := range map[string]string{
		"vitest-version.txt":            "vitest/5.0.3",
		"pytest-version.txt":            "pytest 9.1.1",
		"toolchain-node24-version.txt":  "v24.22.1",
		"toolchain-node-version.txt":    "v22.",
		"toolchain-go-version.txt":      "go version go1.",
		"toolchain-python3-version.txt": "Python 3.",
		"toolchain-ruby-version.txt":    "ruby 3.",
		"toolchain-php-version.txt":     "PHP 8.",
		"toolchain-rustc-version.txt":   "rustc 1.",
	} {
		if first := FirstLine(readFixture(t, file)); !strings.HasPrefix(first, want) {
			t.Errorf("%s: first line %q, want prefix %q", file, first, want)
		}
	}
}

func TestFirstLineIsTrimmedAndEmptyForNothing(t *testing.T) {
	if got := FirstLine("\n  v1.2.3  \nmore\n"); got != "v1.2.3" {
		t.Errorf("FirstLine = %q, want v1.2.3 (leading blank lines skipped, trimmed)", got)
	}
	if got := FirstLine(" \n\n"); got != "" {
		t.Errorf("FirstLine of blanks = %q, want empty", got)
	}
}

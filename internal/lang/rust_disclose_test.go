// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestRustRunnerDisclosures: what the scan line says about the runner comes
// from the one runner decision (rustRunner), read through the plugin-fed
// seam (RunDisclosures) — never a language switch at the caller.
//
//   - cargo test in a repository that keeps .config/nextest.toml: the
//     project's nextest profiles (retries, timeouts, test groups) do not
//     apply, and the scan says so rather than switching runners itself.
//   - cargo test without one: nothing to say.
//   - nextest: a test passed only on a retry is graded as passed, said out
//     loud (the flaky tests themselves are named per run, FlakyTests).
func TestRustRunnerDisclosures(t *testing.T) {
	bare := t.TempDir()
	withCfg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(withCfg, ".config"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(withCfg, ".config", "nextest.toml"), []byte("[profile.default]\nretries = 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, _ := ByName("rust")

	got := RunDisclosures(p, withCfg, []string{"cargo", "test", "-p", "warp_core"})
	if len(got) != 1 || !strings.Contains(got[0], ".config/nextest.toml") || !strings.Contains(got[0], "cargo test") || !strings.Contains(got[0], "do not apply") {
		t.Errorf("cargo test beside a nextest.toml: %q", got)
	}
	if got := RunDisclosures(p, bare, []string{"cargo", "test"}); len(got) != 0 {
		t.Errorf("cargo test, no nextest.toml: want nothing, got %q", got)
	}
	for _, dir := range []string{bare, withCfg} {
		got := RunDisclosures(p, dir, []string{"cargo", "nextest", "run", "-p", "warp_core"})
		if len(got) != 1 || !strings.Contains(got[0], "FLAKY") || !strings.Contains(got[0], "graded as passed") {
			t.Errorf("nextest: %q", got)
		}
	}
	// A plugin that implements no RunDiscloser says nothing.
	py, _ := ByName("python")
	if got := RunDisclosures(py, withCfg, []string{"pytest"}); got != nil {
		t.Errorf("python: %q", got)
	}
}

// TestRustFlakyTestsAreReadFromTheRun: the Flaky list part 1 parsed and left
// unread is what FlakyTests hands the scan line, off the same parse.
func TestRustFlakyTestsAreReadFromTheRun(t *testing.T) {
	var fr FlakyReporter = rustPlugin{}
	if got := fr.FlakyTests(rustCapture(t, "nextest", "flaky")); !reflect.DeepEqual(got, []string{"fuzzy_match tests::planted_flaky"}) {
		t.Errorf("FlakyTests(nextest flaky) = %q", got)
	}
	if got := fr.FlakyTests(rustCapture(t, "nextest", "pass")); len(got) != 0 {
		t.Errorf("FlakyTests(nextest pass) = %q", got)
	}
}

// TestRustHarnessNamesNextestConfig: nextest.toml changes what the suite
// measures (retries, timeouts, groups) from wherever it lives, so it is a
// harness file — a change to it moves the verdict cache key.
func TestRustHarnessNamesNextestConfig(t *testing.T) {
	if !IsHarnessFile(".config/nextest.toml") {
		t.Error(".config/nextest.toml is not a harness file")
	}
	if IsHarnessFile("src/nextest.rs") {
		t.Error("a source named like it is not one")
	}
}

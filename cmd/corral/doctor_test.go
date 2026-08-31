// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/sandbox"
)

// TestDoctorReportsMissingCredentialsPerModel: a key alone does not move
// providers, so the check is per MODEL rather than "is any key set". Naming the
// role and the model is what makes it fixable in one step.
func TestDoctorReportsMissingCredentialsPerModel(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")

	got := checkCredentials("gemini-3.6-flash", "gemini-3.6-flash", "claude-haiku-4-5")
	if len(got) != 2 {
		t.Fatalf("two distinct models were assigned, want two checks, got %d: %+v", len(got), got)
	}
	var joined string
	for _, r := range got {
		if r.ok {
			t.Fatalf("no keys are set; %q must not pass", r.name)
		}
		joined += r.name + " :: " + r.detail + "\n"
	}
	for _, want := range []string{"gemini-3.6-flash", "claude-haiku-4-5", "mutant-generator", "test-critic"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected %q named in the failures:\n%s", want, joined)
		}
	}
}

// TestDoctorSkipsAnOffRole: `--critic-model off` is a supported configuration
// (a single-vendor run with one usable model drops the advisory critic), and
// demanding a credential for a seat that will not run would be a false alarm.
func TestDoctorSkipsAnOffRole(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "gm")
	got := checkCredentials("gemini-3.6-flash", "gemini-3.6-flash", "off")
	for _, r := range got {
		if strings.Contains(r.name, "test-critic") {
			t.Fatalf("an 'off' role must not be checked: %+v", r)
		}
	}
}

// TestDoctorPairingNamesTheFixWhenNoTestIsFound. "No test found" moves the
// operator's problem without solving it; the conventions that fail are
// specific and worth naming.
func TestDoctorPairingNamesTheFixWhenNoTestIsFound(t *testing.T) {
	dir := t.TempDir()
	src := dir + "/widget.ts"
	if err := writeFile(src, "export const a = 1;\n"); err != nil {
		t.Fatal(err)
	}
	got := checkPairing(src, "")
	if got.ok {
		t.Fatal("a file with no sibling test must not pass the pairing check")
	}
	for _, want := range []string{"--test", "__tests__"} {
		if !strings.Contains(got.detail, want) {
			t.Fatalf("expected the fix named (%q) in: %s", want, got.detail)
		}
	}
}

// TestDoctorPairingAcceptsAnExplicitTest, and rejects one that does not exist —
// a typo in --test must not read as "paired".
func TestDoctorPairingAcceptsAnExplicitTest(t *testing.T) {
	dir := t.TempDir()
	src, tst := dir+"/widget.ts", dir+"/widget.spec.ts"
	if err := writeFile(src, "export const a = 1;\n"); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(tst, "// tests\n"); err != nil {
		t.Fatal(err)
	}
	if got := checkPairing(src, tst); !got.ok {
		t.Fatalf("an explicit, existing --test must pass: %+v", got)
	}
	if got := checkPairing(src, dir+"/nope.spec.ts"); got.ok {
		t.Fatal("a --test that does not exist must fail, not pass silently")
	}
}

// TestDoctorFailsWithNonZeroExit: doctor is meant to gate a paid run, so a
// failure has to be actionable by a script, not only by a reader.
func TestDoctorFailsWithNonZeroExit(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	var out, errOut bytes.Buffer
	if rc := runDoctor(nil, &out, &errOut); rc == 0 {
		t.Fatalf("missing credentials must exit non-zero:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "before spending a run") {
		t.Fatalf("the summary must say why it matters:\n%s", out.String())
	}
}

func writeFile(path, content string) error { return os.WriteFile(path, []byte(content), 0o644) }

// TestCheckToolchainFailsWhenBinaryInvisibleInJail reproduces the exact
// rehearsal shape this whole change exists for: a `.venv/bin/python` that IS
// on the host (so a host-only check, or a check that merely resolves the
// binary against the operator's cwd, would call it reachable) but is NOT
// inside the jail's own fresh workspace, because nothing seeds it there
// (bare mode — no --repo-dir). Before this fix, checkToolchain read the
// resulting non-zero exit as "inconclusive, not a failure"; the real run
// then died with the shell's own ".venv/bin/python: not found" having
// already spent tokens on mutants that were never graded. This must now
// FAIL, with that same "not found" wording plus the fix hint.
func TestCheckToolchainFailsWhenBinaryInvisibleInJail(t *testing.T) {
	iso, err := sandbox.Resolve(sandbox.Config{})
	if err != nil {
		t.Skipf("no working sandbox backend on this host: %v", err)
	}

	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/.venv/bin", 0o755); err != nil {
		t.Fatal(err)
	}
	real, lerr := exec.LookPath("python3")
	if lerr != nil {
		real, lerr = exec.LookPath("sh")
		if lerr != nil {
			t.Skip("neither python3 nor sh on PATH to symlink as the fake venv interpreter")
		}
	}
	if err := os.Symlink(real, dir+"/.venv/bin/python"); err != nil {
		t.Fatal(err)
	}

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd)

	got := checkToolchain(iso, []string{".venv/bin/python"}, nil)
	if got.ok {
		t.Fatalf("checkToolchain must FAIL when the run's own jail cannot see the toolchain: %+v", got)
	}
	if !strings.Contains(got.detail, "not found") {
		t.Errorf("detail %q must carry the run's own \"not found\" message", got.detail)
	}
	if !strings.Contains(got.detail, "--substrate workspace") {
		t.Errorf("detail %q must name the fix: certify --repo --substrate workspace", got.detail)
	}
	if !strings.Contains(got.detail, "CORRALAI_EXEC_IMAGE") {
		t.Errorf("detail %q must name the other fix: baking the toolchain into CORRALAI_EXEC_IMAGE", got.detail)
	}
}

// TestCheckToolchainPassesWhenReallyReachable is the control: a tool that
// genuinely exists inside the jail (/usr is always mounted) must still pass,
// so the fix above is not a blanket "any non-zero exit fails" regression.
func TestCheckToolchainPassesWhenReallyReachable(t *testing.T) {
	iso, err := sandbox.Resolve(sandbox.Config{})
	if err != nil {
		t.Skipf("no working sandbox backend on this host: %v", err)
	}
	got := checkToolchain(iso, []string{"sh"}, nil)
	if !got.ok {
		t.Fatalf("checkToolchain must pass for a genuinely reachable tool: %+v", got)
	}
}

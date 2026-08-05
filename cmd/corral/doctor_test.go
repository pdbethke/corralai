// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
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

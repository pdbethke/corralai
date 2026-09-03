// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
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
	t.Setenv("MODEL_BACKEND", "")

	got := checkHerd("gemini-3.6-flash", "gemini-3.6-flash", "claude-haiku-4-5", "", nil)
	var joined string
	ok := 0
	for _, r := range got {
		if r.ok {
			ok++
		}
		joined += r.name + " :: " + r.detail + "\n"
	}
	if ok != 0 {
		t.Fatalf("no keys are set; nothing must pass:\n%s", joined)
	}
	// certify's own refusal, naming the seat's vendor and the variable.
	for _, want := range []string{"gemini-3.6-flash", "GEMINI_API_KEY"} {
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
	t.Setenv("MODEL_BACKEND", "")
	got := checkHerd("gemini-3.6-flash", "gemini-3.6-flash", "off", "", nil)
	for _, r := range got {
		if !r.ok {
			t.Fatalf("an all-Gemini herd with a Gemini key and the critic off must pass: %+v", r)
		}
	}
}

// TestDoctorAgreesWithCertifyAboutTheHerd pins the router review's finding:
// doctor ran its own credential check (ForModel per model, no
// MODEL_BACKEND, models de-duplicated before decorrelation) and disagreed
// with certify in both directions. It now asks certify's own preflight.
func TestDoctorAgreesWithCertifyAboutTheHerd(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")

	fails := func(res []checkResult) []string {
		var out []string
		for _, r := range res {
			if !r.ok {
				out = append(out, r.name+" :: "+r.detail)
			}
		}
		return out
	}
	// 1. An all-local herd: certify accepts it (no vendor, no key needed);
	// doctor used to FAIL it with "cannot infer a cloud vendor".
	t.Setenv("MODEL_BACKEND", "")
	if f := fails(checkHerd("qwen2.5-coder:7b", "qwen2.5-coder:14b", "qwen2.5-coder:7b", "", nil)); len(f) != 0 {
		t.Errorf("all-local herd refused by doctor, accepted by certify: %v", f)
	}
	// 2. A pinned gateway: certify's preflight demands no Anthropic key for
	// a claude-* seat behind MODEL_BACKEND=openrouter; doctor demanded one.
	t.Setenv("MODEL_BACKEND", "openrouter")
	t.Setenv("OPENROUTER_API_KEY", "test-placeholder-not-a-real-key")
	if f := fails(checkHerd("claude-sonnet-5", "claude-sonnet-5", "gemini-3.6-flash", "", nil)); len(f) != 0 {
		t.Errorf("pinned-gateway herd refused by doctor, accepted by certify: %v", f)
	}
	// 3. writer == critic: certify refuses (decorrelation); doctor passed it,
	// having de-duplicated the models first.
	t.Setenv("MODEL_BACKEND", "")
	t.Setenv("GEMINI_API_KEY", "gm")
	f := fails(checkHerd("gemini-3.6-flash", "gemini-3.6-flash", "gemini-3.6-flash", "", nil))
	if len(f) == 0 {
		t.Error("writer == critic passed doctor; certify refuses it")
	} else if !strings.Contains(strings.Join(f, "\n"), "critic") {
		t.Errorf("refused for the wrong reason: %v", f)
	}
	// 4. The challenger seat is checked too: a Claude challenger on an
	// all-Gemini herd with no Anthropic key is a refusal certify makes.
	if f := fails(checkHerd("gemini-3.6-flash", "gemini-3.6-flash", "off", "claude-sonnet-5", nil)); len(f) == 0 {
		t.Error("a challenger seat with no credential passed doctor; certify refuses it")
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
	// Task 2 (php): vendor/ auto-binds exactly parallel to node_modules and
	// .venv (Composer's dep tree, holding vendor/bin/phpunit), so the hint
	// naming what --repo-dir additionally binds must say so explicitly
	// rather than trailing off in a "..." that leaves an operator guessing
	// whether their PHP project's vendor dir is covered.
	if !strings.Contains(got.detail, "vendor") {
		t.Errorf("detail %q must name vendor/ among the auto-bound dependency dirs", got.detail)
	}
}

// TestCheckToolchainPassesWhenReallyReachable is the control: a tool that
// genuinely exists inside the jail (/usr is always mounted) must still pass,
// so the fix above is not a blanket "any non-zero exit fails" regression.
// TestToolNotFoundInJailRequiresToolNameAnchor pins the fix for a false
// FAIL: the bare "No such file or directory" branch used to fire on ANY
// occurrence of that phrase, so a tool whose own --version output happens to
// mention some unrelated missing file (not the tool binary itself) would be
// read as "the jail can't see this tool" and hard-fail a run that would have
// worked. The phrase must also carry the tool's own name to count.
func TestToolNotFoundInJailRequiresToolNameAnchor(t *testing.T) {
	if toolNotFoundInJail("error: /etc/some-other-config: No such file or directory", "mytool") {
		t.Error("toolNotFoundInJail fired without the tool name in the output — must stay inconclusive, not FAIL")
	}
	if !toolNotFoundInJail("mytool: No such file or directory", "mytool") {
		t.Error("toolNotFoundInJail did not fire when the tool name IS in the output")
	}
	if !toolNotFoundInJail("sh: 1: mytool: not found", "mytool") {
		t.Error("toolNotFoundInJail did not fire on the sh-shaped \"not found\" message")
	}
}

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

// TestDoctorRehearsesTheDeriveSeatAndReadsTheRepoRegistry: the derive seat
// and the challenger were unchecked, and the registry was read from "."
// rather than the repository the run will audit.
func TestDoctorRehearsesTheDeriveSeatAndReadsTheRepoRegistry(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "gm")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("MODEL_BACKEND", "")
	t.Setenv("GITHUB_ACTIONS", "")
	t.Setenv("CORRALAI_MODELS", "")
	t.Setenv("CORRALAI_MODELS_FILE", "")

	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".corral"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(repo, ".corral", "models.json"),
		`{"fast": {"provider": "google", "model": "gemini-3.6-flash"}, "gpu": {"provider": "ollama", "model": "qwen2.5-coder:14b", "endpoint": "http://127.0.0.1:11434"}}`)

	// Aliases from the run's repo resolve; the derive seat (a Claude model,
	// no Anthropic key) is the one finding. Driven through the same
	// registry + checks runDoctor wires for --repo, --derive-model and
	// --shadow-model, below its fatal sandbox check.
	//surface: --derive-model
	//surface: --shadow-model
	mutant, writer, critic, shadow, derive := "fast", "gpu", "off", "", "claude-sonnet-5"
	var errOut bytes.Buffer
	reg, err := resolveSeatRegistry("corral doctor", repo, certifySeats(&derive, &mutant, &writer, &critic, &shadow, nil), &errOut)
	if err != nil {
		t.Fatal(err)
	}
	if mutant != "gemini-3.6-flash" || writer != "qwen2.5-coder:14b" {
		t.Fatalf("the repo's registry did not resolve the aliases: mutant=%q writer=%q\n%s", mutant, writer, errOut.String())
	}
	for _, r := range checkHerd(mutant, writer, critic, shadow, reg) {
		if !r.ok {
			t.Errorf("herd check failed: %s :: %s", r.name, r.detail)
		}
	}
	dres := checkDeriveSeat(derive, reg.deriveEndpoint())
	if len(dres) != 1 || dres[0].ok {
		t.Errorf("a derive seat with no credential passed: %+v", dres)
	}
	// And a registry-placed derive seat: the registry's daemon, no key.
	derive = "gpu"
	reg2, err := resolveSeatRegistry("corral doctor", repo, certifySeats(&derive, nil, nil, nil, nil, nil), &errOut)
	if err != nil {
		t.Fatal(err)
	}
	if reg2.deriveEndpoint() != "http://127.0.0.1:11434" {
		t.Errorf("derive endpoint = %q, want the registry's daemon", reg2.deriveEndpoint())
	}
	if dres := checkDeriveSeat(derive, reg2.deriveEndpoint()); len(dres) != 1 || !dres[0].ok {
		t.Errorf("a registry-placed local derive seat failed: %+v", dres)
	}
}

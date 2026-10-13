// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/agentworker"
)

// noKeys clears every provider credential and the backend pin, so a test
// proves what a run needs when it has NOTHING but a CLI login.
func noKeys(t *testing.T) {
	t.Helper()
	for _, k := range []string{"MODEL_BACKEND", "ANTHROPIC_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY", "OPENAI_API_KEY"} {
		t.Setenv(k, "")
	}
}

// fakeAgent writes an executable that answers --version and otherwise prints a
// fixed reply, and defines it as the operator agent NAME. No real CLI runs.
func fakeAgent(t *testing.T, name, reply string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent-"+name)
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'fakeagent 9.9'; exit 0; fi\ncat >/dev/null\necho '" + reply + "'\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil { // #nosec G306 -- test fixture must be executable
		t.Fatal(err)
	}
	def := path + " {model:--model}"
	t.Setenv("CORRALAI_AGENT_"+strings.ToUpper(name), def)
	return def
}

// fakeBuiltinBinaries puts stand-in executables for the built-in agents' CLIs
// on PATH: ForSeat refuses a seat whose binary is missing, and a CI runner has
// none of them. Nothing here runs them.
func fakeBuiltinBinaries(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	for _, name := range []string{"claude", "codex", "agy"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil { // #nosec G306 -- test fixture must be executable
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// An all-agent run holds no API key and must not be asked for one, must not
// set MODEL_BACKEND for a vendor nothing will dial, and must build every seat.
func TestAllAgentHerdNeedsNoKeyAndSetsNoBackend(t *testing.T) {
	noKeys(t)
	fakeBuiltinBinaries(t)
	r, err := resolveAuditRoles(localAuditInput{
		mutantModel: "codex", writerModel: "codex", criticModel: "claude-code", shadowModel: "off",
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("an all-agent herd must need no key: %v", err)
	}
	if got := os.Getenv("MODEL_BACKEND"); got != "" {
		t.Fatalf("MODEL_BACKEND = %q: an all-agent run must not pin a backend", got)
	}
	for _, role := range []string{advpool.RoleMutantGenerator, advpool.RoleTestWriter, advpool.RoleTestCritic} {
		if r.chatterFor(role) == nil {
			t.Errorf("no chatter for %s", role)
		}
	}
}

// The seat really is the CLI: a chat on it runs the operator's agent.
func TestAgentSeatChatterRunsTheAgent(t *testing.T) {
	noKeys(t)
	fakeAgent(t, "fakea", "from-the-agent")
	chatterFor, err := localChatterFor(advpool.RoleAssignment{advpool.RoleTestWriter: "fakea"}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := chatterFor(advpool.RoleTestWriter).Chat([]agentworker.Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply.Content, "from-the-agent") {
		t.Fatalf("reply %q did not come from the agent", reply.Content)
	}
}

// A daemon placement cannot apply to a CLI; refusing beats ignoring it.
func TestAgentSeatRefusesALocalEndpoint(t *testing.T) {
	noKeys(t)
	_, err := localChatterFor(advpool.RoleAssignment{advpool.RoleTestWriter: "codex"}, nil,
		map[string]string{advpool.RoleTestWriter: "http://127.0.0.1:11434"}, nil)
	if err == nil || !strings.Contains(err.Error(), "agent seat") {
		t.Fatalf("want a refusal naming the agent seat, got %v", err)
	}
}

// An agent seat says nothing about which API the run needs; the API seats do.
func TestSoleAssignedCloudModelSkipsAgentSeats(t *testing.T) {
	v, m := soleAssignedCloudModel(advpool.RoleAssignment{
		advpool.RoleMutantGenerator: "codex",
		advpool.RoleTestWriter:      "claude-sonnet-5",
		advpool.RoleTestCritic:      "claude-code",
	})
	if v != "anthropic" || m != "claude-sonnet-5" {
		t.Fatalf("got (%q, %q), want the one API seat's vendor", v, m)
	}
	if v, m := soleAssignedCloudModel(advpool.RoleAssignment{advpool.RoleTestWriter: "codex", advpool.RoleTestCritic: "claude-code"}); v != "" || m != "" {
		t.Fatalf("an all-agent assignment names no API vendor, got (%q, %q)", v, m)
	}
}

// A codex critic against a pinned codex writer is two seats (decorrelation
// passes) but one VENDOR: the same-vendor warning must say so.
func TestSameVendorWarningNamesAgentSeats(t *testing.T) {
	noKeys(t)
	fakeBuiltinBinaries(t)
	var warn bytes.Buffer
	if _, err := resolveAuditRoles(localAuditInput{
		mutantModel: "codex", writerModel: "codex:gpt-6-astra", criticModel: "codex", shadowModel: "off",
	}, &warn); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(warn.String(), "SAME provider (openai)") {
		t.Fatalf("no same-vendor warning for codex + codex:gpt-6-astra:\n%s", warn.String())
	}
	if _, err := resolveAuditRoles(localAuditInput{
		mutantModel: "codex", writerModel: "codex", criticModel: "codex", shadowModel: "off",
	}, &bytes.Buffer{}); err == nil {
		t.Fatal("codex + codex must be refused by decorrelation")
	}
}

// The verdict's "every graded seat is X" line sees agent seats too.
func TestSoleGradedVendorSeesAgentSeats(t *testing.T) {
	if got := soleGradedVendor(map[string]string{
		advpool.RoleMutantGenerator: "codex",
		advpool.RoleTestWriter:      "codex:gpt-6-astra",
		advpool.RoleTestCritic:      "gpt-6",
	}); got != "openai" {
		t.Fatalf("got %q, want openai", got)
	}
	fakeAgent(t, "fakea", "x")
	if got := soleGradedVendor(map[string]string{
		advpool.RoleMutantGenerator: "fakea",
		advpool.RoleTestWriter:      "gpt-6",
	}); got != "" {
		t.Fatalf("an unpinned operator agent's vendor is unknown, got %q", got)
	}
}

// doctor never prints ok for a login it did not try, and spends no call.
func TestDoctorAgentSeatLineDoesNotClaimALogin(t *testing.T) {
	noKeys(t)
	def := fakeAgent(t, "fakea", "x")
	fakeAgent(t, "fakeb", "y")
	res := checkHerd("fakea", "fakeb", "off", "", "", nil)
	var line *checkResult
	for i := range res {
		if !res[i].ok {
			t.Fatalf("unexpected failure: %s :: %s", res[i].name, res[i].detail)
		}
		if res[i].name == "agent seat fakea" {
			line = &res[i]
		}
	}
	if line == nil {
		t.Fatalf("no line for the agent seat: %+v", res)
	}
	// An operator agent's version is "<--version first line> [<definition>]".
	want := "binary ok (fakeagent 9.9 [" + def + "]), definition ok, login not verified (no call is spent)"
	if line.detail != want {
		t.Fatalf("detail %q, want %q", line.detail, want)
	}
}

// A missing binary is a FAIL, not a skipped line.
func TestDoctorAgentSeatMissingBinaryFails(t *testing.T) {
	noKeys(t)
	t.Setenv("CORRALAI_AGENT_GHOST", "/nonexistent/corral-ghost-agent")
	res := agentSeatChecks("ghost")
	if len(res) != 1 || res[0].ok || !strings.Contains(res[0].detail, "not found") {
		t.Fatalf("want one FAIL naming the missing binary, got %+v", res)
	}
}

// antigravity's command line starts with `sh`, so looking up the first word
// would find a shell on every machine. The check looks for the CLI itself.
func TestDoctorAntigravityChecksTheRealBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no agy anywhere
	res := agentSeatChecks("antigravity")
	if len(res) != 1 || res[0].ok || !strings.Contains(res[0].detail, `"agy" not found`) {
		t.Fatalf("want a FAIL naming agy, got %+v", res)
	}
}

// An operator definition wrapped in a shell hides its CLI from corral: doctor
// must say it did not check, never "ok".
func TestDoctorShellWrappedOperatorAgentIsNotChecked(t *testing.T) {
	t.Setenv("CORRALAI_AGENT_WRAPPED", "sh -c 'corral-no-such-inner-cli \"$@\"' sh")
	res := agentSeatChecks("wrapped")
	if len(res) != 1 || !res[0].ok {
		t.Fatalf("got %+v", res)
	}
	d := res[0].detail
	if !strings.Contains(d, "binary not checked (shell-wrapped definition)") || strings.Contains(d, "binary ok") {
		t.Fatalf("detail %q must say the binary was not checked", d)
	}
}

// A seat whose CLI is not installed refuses certify --local UP FRONT, naming
// the binary — never after the jail ran, and never as a "writer-failed".
func TestCertifyLocalRefusesAMissingAgentBinaryUpFront(t *testing.T) {
	noKeys(t)
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "add.go"), "package add\n\nfunc Add(a, b int) int { return a + b }\n")
	mustWrite(t, filepath.Join(dir, "add_test.go"), "package add\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) { _ = Add(1, 2) }\n")
	fakeAgent(t, "present", "{}")
	t.Setenv("CORRALAI_AGENT_GHOST", "/nonexistent/corral-ghost-agent {model:--model}")
	var out, errb bytes.Buffer
	code := runCertifyLocal([]string{
		"--code", filepath.Join(dir, "add.go"), "--goal", "adds",
		"--writer-model", "ghost", "--mutant-model", "present", "--critic-model", "off",
		"--", "go", "test", "./...",
	}, &out, &errb)
	if code == 0 || !strings.Contains(errb.String(), "corral-ghost-agent") || !strings.Contains(errb.String(), "not found") {
		t.Fatalf("want a refusal naming the missing binary, got exit %d\nstdout=%s\nstderr=%s", code, out.String(), errb.String())
	}
	if strings.Contains(out.String()+errb.String(), "writer-failed") || strings.Contains(out.String(), "baseline") {
		t.Fatalf("the run must stop before the jail:\nstdout=%s\nstderr=%s", out.String(), errb.String())
	}
}

// review resolves its seats through the same resolver, so it refuses a
// missing CLI the same way, before any seat is asked anything.
func TestReviewRefusesAMissingAgentBinaryUpFront(t *testing.T) {
	root := t.TempDir()
	gitRun := gitCmd(t, root)
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n")
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")
	t.Setenv("CORRALAI_AGENT_GHOST", "/nonexistent/corral-ghost-agent {dir} {out}")
	var out, errb bytes.Buffer
	code := runReview([]string{"--repo", root, "--scope", "pkg", "--reviewer-model", "ghost", "--no-ledger"}, &out, &errb)
	if code != 2 || !strings.Contains(errb.String(), "corral-ghost-agent") || !strings.Contains(errb.String(), "not found") {
		t.Fatalf("want exit 2 naming the missing binary, got exit %d\nstdout=%s\nstderr=%s", code, out.String(), errb.String())
	}
}

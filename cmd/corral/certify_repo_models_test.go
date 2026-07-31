// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/reposcan"
)

// TestCertifyRepoAcceptsPerRoleModelFlags closes a single-vendor lock-in on
// corral's flagship whole-repo command: `certify --local` has had
// --writer-model / --mutant-model / --critic-model / --shadow-model since it
// shipped, but `certify --repo` exposed only --derive-model. Every other role
// was pinned to the hardcoded Claude defaults with no override and no env
// escape hatch, so a repo scan could ONLY ever run on Anthropic.
//
// That is a real limitation on its own — it made the flagship command
// single-vendor, and it undercuts corral's own cross-vendor decorrelation
// argument — and it became a hard blocker the day the Anthropic account hit
// its usage limit mid-run with a Google key sitting right there in the
// credstore.
//
// Asserted through the DECORRELATION refusal because that proves the whole
// chain in one shot: the flags parse, and their values reach the role
// resolution that validates them. A flag that parsed but was dropped on the
// floor would leave the defaults in place (writer=claude-sonnet-5,
// critic=claude-haiku-4-5), which are distinct, and this would pass while
// changing nothing — the silently-discarded-input shape this codebase keeps
// producing.
func TestCertifyRepoAcceptsPerRoleModelFlags(t *testing.T) {
	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{
		"--repo", t.TempDir(),
		"--writer-model", "gemini-3.6-flash",
		"--critic-model", "gemini-3.6-flash", // same as the writer: must be refused
		// NOT --dry-run: the scan-wide preflights (jail, then role models) are
		// deliberately skipped on a dry run, since demanding a sandbox and a
		// provider key to print the jobs a scan WOULD emit would refuse the one
		// invocation that costs nothing. The workspace substrate needs no
		// sandbox, so ex.preflight() is a no-op here and the role resolution is
		// reached — which is the thing under test.
		"--substrate", "workspace",
	}, &out, &errb)

	if code == 0 {
		t.Fatalf("exit = 0, want non-zero — a critic on the same model as the writer is not a decorrelated critic.\nstdout=%s\nstderr=%s", out.String(), errb.String())
	}
	if got := errb.String(); !strings.Contains(got, "decorrelat") && !strings.Contains(got, "distinct") {
		t.Fatalf("stderr should explain the decorrelation refusal, got: %s", got)
	}
}

// TestCertifyRepoModelFlagsReachTheExecutor pins the plumbing directly: the
// resolved models must land on the per-file audit input the scan hands each
// job, not merely be parsed. This is the seam where a "supported" flag most
// easily becomes decorative.
func TestCertifyRepoModelFlagsReachTheExecutor(t *testing.T) {
	ex := newLocalExecutor(t.TempDir(), []string{"true"}, substrateWorkspace, 0, nil)
	ex.models = auditModels{
		writer: "gemini-3.6-flash",
		mutant: "gemini-3.6-flash",
		critic: "gemini-3.5-flash",
		shadow: "off",
	}

	in := ex.auditInputFor(reposcan.Job{Path: "a.py", TestPath: "test_a.py", Lang: "python"})
	if in.writerModel != "gemini-3.6-flash" {
		t.Errorf("writerModel = %q, want gemini-3.6-flash", in.writerModel)
	}
	if in.mutantModel != "gemini-3.6-flash" {
		t.Errorf("mutantModel = %q, want gemini-3.6-flash", in.mutantModel)
	}
	if in.criticModel != "gemini-3.5-flash" {
		t.Errorf("criticModel = %q, want gemini-3.5-flash", in.criticModel)
	}
	if in.shadowModel != "off" {
		t.Errorf("shadowModel = %q, want off — the default shadow is a Claude model, so a non-Anthropic run must be able to disable it", in.shadowModel)
	}
}

// TestCertifyRepoUnsetModelsStayEmpty pins that omitting the flags changes
// nothing: empty values flow through and auditRoles applies its own defaults,
// so every existing invocation keeps today's behaviour exactly.
func TestCertifyRepoUnsetModelsStayEmpty(t *testing.T) {
	ex := newLocalExecutor(t.TempDir(), []string{"true"}, substrateWorkspace, 0, nil)
	in := ex.auditInputFor(reposcan.Job{Path: "a.py", TestPath: "test_a.py", Lang: "python"})
	if in.writerModel != "" || in.mutantModel != "" || in.criticModel != "" || in.shadowModel != "" {
		t.Fatalf("unset model flags must stay empty so the defaults apply, got %+v", in)
	}
}

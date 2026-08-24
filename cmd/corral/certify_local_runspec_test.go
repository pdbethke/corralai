// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"testing"

	"github.com/pdbethke/corralai/internal/advpool"
)

// THE SEAM THAT HAD NO COVERAGE. resolveRoleModels resolved
// --shadow-writer-model, resolveAuditRoles put it in the RoleAssignment (so it
// forced a cache miss, could demand a credential, and was written into the
// SIGNED record's ModelsByRole), and then the RunSpec literal simply never
// carried it — leaving every consumer's `run.rs.ShadowWriterModel != ""` guard
// false and no challenger task ever enqueued. A signed record named a seat
// that had not run.
//
// This test walks the whole CLI→RunSpec path for BOTH challenger seats: flags
// in, resolveAuditRoles, newAuditRunSpec, RunSpec out.
func TestResolvedChallengerModelsReachTheRunSpec(t *testing.T) {
	t.Setenv("MODEL_BACKEND", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "gm-test")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")

	in := localAuditInput{
		writerModel:       "gemini-3.6-flash",
		mutantModel:       "gemini-3.6-flash",
		criticModel:       "off",
		shadowModel:       "gemini-3.5-flash",
		shadowWriterModel: "gemini-3.5-flash",
		checkArgv:         []string{"go", "test", "./..."},
	}
	roles, err := resolveAuditRoles(in, nil)
	if err != nil {
		t.Fatalf("resolveAuditRoles: %v", err)
	}
	if roles.shadowWriter != "gemini-3.5-flash" {
		t.Fatalf("roles.shadowWriter = %q — the resolver dropped the flag before the RunSpec could see it", roles.shadowWriter)
	}
	if roles.assign[advpool.RoleTestWriterShadow] != roles.shadowWriter {
		t.Errorf("assign[%s] = %q, roles.shadowWriter = %q — the two must be the same seat",
			advpool.RoleTestWriterShadow, roles.assign[advpool.RoleTestWriterShadow], roles.shadowWriter)
	}

	rs := newAuditRunSpec(in, roles, runSubject{
		repo: "r", commit: "c", codePath: "a.go", code: "package a",
		devTestPath: "a_test.go", devTest: "package a", lang: "go",
	})
	if rs.ShadowWriterModel != "gemini-3.5-flash" {
		t.Errorf("RunSpec.ShadowWriterModel = %q, want the resolved flag — advpool guards every challenger path on this field, so an empty one means the seat is paid for in the cache key and the signed record but never actually runs", rs.ShadowWriterModel)
	}
	if rs.ShadowModel != "gemini-3.5-flash" {
		t.Errorf("RunSpec.ShadowModel = %q, want the resolved flag", rs.ShadowModel)
	}
}

// Off unless named: an unnamed challenger writer must leave the RunSpec field
// empty, so the driver enqueues nothing and the run is byte-identical to a
// pre-feature one.
func TestUnnamedChallengerWriterLeavesTheRunSpecEmpty(t *testing.T) {
	t.Setenv("MODEL_BACKEND", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "gm-test")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")

	in := localAuditInput{
		writerModel: "gemini-3.6-flash",
		mutantModel: "gemini-3.6-flash",
		criticModel: "off",
		shadowModel: "off",
		checkArgv:   []string{"go", "test", "./..."},
	}
	roles, err := resolveAuditRoles(in, nil)
	if err != nil {
		t.Fatalf("resolveAuditRoles: %v", err)
	}
	rs := newAuditRunSpec(in, roles, runSubject{repo: "r", commit: "c", codePath: "a.go", lang: "go"})
	if rs.ShadowWriterModel != "" {
		t.Errorf("RunSpec.ShadowWriterModel = %q with no --shadow-writer-model — the seat is not off unless named", rs.ShadowWriterModel)
	}
	if _, ok := roles.assign[advpool.RoleTestWriterShadow]; ok {
		t.Error("an unnamed challenger writer must not appear in the role assignment at all")
	}
}

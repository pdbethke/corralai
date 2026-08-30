// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
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

// TestConcurrencyReachesTheRunSpec walks the same CLI→RunSpec seam for the
// workspace substrate's concurrency probe: buildJailWiring writes its answer
// into in.concurrency, and newAuditRunSpec is the ONLY place that copies it
// onto the RunSpec the driver actually reads — miss this hop and every
// verdict, report line and ledger row downstream discloses nothing.
//
// A nil pointer or a Trees < 1 must normalize to the ZERO Concurrency, never
// an invented {Trees: 1}: the jail substrate builds no trees at all and never
// sets in.concurrency, so a normalized "1" there would sign a measurement
// nothing made. Trees 0 is the one explicit "not recorded" state, and every
// reader downstream renders and signs it as such.
func TestConcurrencyReachesTheRunSpec(t *testing.T) {
	roles := auditRoles{}
	subj := runSubject{repo: "r", commit: "c", codePath: "a.go", lang: "go"}

	rs := newAuditRunSpec(localAuditInput{checkArgv: []string{"go", "test", "./..."}}, roles, subj)
	if rs.Concurrency.Trees != 0 || rs.Concurrency.Note != "" {
		t.Errorf("nil concurrency pointer (the jail substrate) = %+v, want the zero Concurrency", rs.Concurrency)
	}

	zero := &adequacy.Disclosure{}
	rs = newAuditRunSpec(localAuditInput{checkArgv: []string{"go", "test", "./..."}, concurrency: zero}, roles, subj)
	if rs.Concurrency.Trees != 0 || rs.Concurrency.Note != "" {
		t.Errorf("zero-value disclosure = %+v, want the zero Concurrency, not an invented 1", rs.Concurrency)
	}

	many := &adequacy.Disclosure{Trees: 6}
	rs = newAuditRunSpec(localAuditInput{checkArgv: []string{"go", "test", "./..."}, concurrency: many}, roles, subj)
	if rs.Concurrency.Trees != 6 || rs.Concurrency.Note != "" {
		t.Errorf("6-tree disclosure = %+v, want {Trees: 6, Note: \"\"}", rs.Concurrency)
	}

	// The dep dirs symlinked into every tree are the one thing the trees do
	// NOT hold privately — a channel between them — so they ride onto the
	// RunSpec with the count, not beside it.
	shared := &adequacy.Disclosure{Trees: 6, Shared: []string{".venv"}}
	rs = newAuditRunSpec(localAuditInput{checkArgv: []string{"go", "test", "./..."}, concurrency: shared}, roles, subj)
	if len(rs.Concurrency.Shared) != 1 || rs.Concurrency.Shared[0] != ".venv" {
		t.Errorf("shared dep dirs = %+v, want [.venv] carried onto the RunSpec", rs.Concurrency)
	}

	downgraded := &adequacy.Disclosure{Trees: 1, Note: "suite is not concurrency-safe: baseline failed under 3"}
	rs = newAuditRunSpec(localAuditInput{checkArgv: []string{"go", "test", "./..."}, concurrency: downgraded}, roles, subj)
	if rs.Concurrency.Trees != 1 || rs.Concurrency.Note != downgraded.Note {
		t.Errorf("downgraded disclosure = %+v, want the note preserved", rs.Concurrency)
	}
}

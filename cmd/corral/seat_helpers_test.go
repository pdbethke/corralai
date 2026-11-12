// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/agentbackend"
)

// seatsOf resolves a localAuditInput's five model seats through the REAL
// resolver, the way a certify entry point does before it builds the input, so
// a test of resolveAuditRoles / checkHerd exercises the same routing a run
// gets and not a hand-built shortcut.
//
// A bare local name needs a declared source (corral has no default daemon), so
// the helper declares one the way an operator would: OLLAMA_URL, when the test
// has not set it, pointing at a daemon whose listing is stubbed to serve
// exactly the bare names the input uses. Cloud names, agent seats and a pinned
// MODEL_BACKEND resolve by their own rules, untouched.
func seatsOf(t *testing.T, in localAuditInput) seatSet {
	t.Helper()
	set, err := resolveSeatsOf(t, in, nil)
	if err != nil {
		t.Fatalf("resolving the input's seats: %v", err)
	}
	return set
}

// seatsForAssign is seatsOf for a test that names its seats as a role
// assignment, with optional --local-endpoint placements (role -> url) folded
// in exactly as a certify entry point folds them.
func seatsForAssign(t *testing.T, assign advpool.RoleAssignment, placed map[string]string) seatSet {
	t.Helper()
	set, err := resolveSeatsOf(t, localAuditInput{
		mutantModel: assign[advpool.RoleMutantGenerator], writerModel: assign[advpool.RoleTestWriter],
		criticModel: assign[advpool.RoleTestCritic], shadowModel: assign[advpool.RoleMutantGeneratorShadow],
		shadowWriterModel: assign[advpool.RoleTestWriterShadow],
	}, placed)
	if err != nil {
		t.Fatalf("resolving the assignment's seats: %v", err)
	}
	return set
}

// resolveSeatsOf is the resolution itself, returning the refusal for a test
// that expects one.
func resolveSeatsOf(t *testing.T, in localAuditInput, placed map[string]string) (seatSet, error) {
	t.Helper()
	models := []struct {
		flag, role string
		val        string
	}{
		{"mutant-model", advpool.RoleMutantGenerator, in.mutantModel},
		{"writer-model", advpool.RoleTestWriter, in.writerModel},
		{"critic-model", advpool.RoleTestCritic, in.criticModel},
		{"shadow-model", advpool.RoleMutantGeneratorShadow, in.shadowModel},
		{"shadow-writer-model", advpool.RoleTestWriterShadow, in.shadowWriterModel},
	}
	var flags []seatFlag
	var bare []string
	for i := range models {
		v := strings.TrimSpace(models[i].val)
		models[i].val = v
		flags = append(flags, seatFlag{flag: models[i].flag, role: models[i].role, val: &models[i].val})
		if v != "" && !strings.EqualFold(v, "off") && !isAgentSeat(v) && agentbackend.VendorOf(v) == "" {
			bare = append(bare, v)
		}
	}
	if strings.TrimSpace(os.Getenv("OLLAMA_URL")) == "" {
		t.Setenv("OLLAMA_URL", "http://ollama.test.invalid:11434")
	}
	old := listOllama
	listOllama = func(context.Context, *http.Client, string) ([]string, error) { return bare, nil }
	t.Cleanup(func() { listOllama = old })

	return resolveSeatsPlaced(context.Background(), "test", "", flags, placed, io.Discard)
}

// resolveRoles is resolveAuditRoles with the seats resolved first, unless the
// input already carries them.
func resolveRoles(t *testing.T, in localAuditInput, stderr io.Writer) (auditRoles, error) {
	t.Helper()
	if in.seats == nil {
		in.seats = seatsOf(t, in)
	}
	return resolveAuditRoles(in, stderr)
}

// checkHerdFor is checkHerd with the seats resolved first, as runDoctor does.
func checkHerdFor(t *testing.T, mutant, writer, critic, shadow, shadowWriter string) []checkResult {
	t.Helper()
	seats := seatsOf(t, localAuditInput{mutantModel: mutant, writerModel: writer, criticModel: critic, shadowModel: shadow, shadowWriterModel: shadowWriter})
	return checkHerd(mutant, writer, critic, shadow, shadowWriter, seats)
}

// withSeats resolves the input's seats, for a test that builds the input by
// hand and hands it straight to auditOneFile.
func (in localAuditInput) withSeats(t *testing.T) localAuditInput {
	t.Helper()
	in.seats = seatsOf(t, in)
	return in
}

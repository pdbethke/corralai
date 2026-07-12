// SPDX-License-Identifier: Elastic-2.0

// Package testgen turns a CISO goal, target source, and its signature
// surface into a candidate Go test via an LLM test-writer. WriteTest is
// generation-only: it does not compile or run the result. A non-compiling
// or inadequate test is caught later by adequacy scoring, not here.
package testgen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/pdbethke/corralai/internal/repoindex"
)

// LLM is the narrow surface WriteTest needs; *llm.Client satisfies it, as
// does a test fake. Mirrors internal/learn.Asker and internal/oracle.LLM.
type LLM interface {
	Ask(ctx context.Context, system, user string) (string, error)
}

// writeTestSystem instructs the model to write ONE executable, white-box Go
// test that fails when the goal is violated — boundary-testing the goal,
// not just exercising happy-path behavior. The strict "raw content only"
// contract keeps extractCode's fence-stripping a formality, not a parser.
const writeTestSystem = `You are a TEST-WRITER. Given a security control GOAL, a target source file, and its signature surface, write ONE executable Go test that verifies the code SATISFIES the goal.
- Same package as the target (white-box).
- It MUST compile against the target and MUST FAIL if the goal is violated — test the goal's boundary (what a weakened implementation would pass that a compliant one must not).
- Standard library "testing" only. Deterministic, no network.
Return ONLY the raw Go test file content — no prose, no markdown fences.`

// buildUser assembles the user prompt: the goal, the target file, and its
// signature surface (JSON, from repoindex — the model's only view of the
// callable API besides the raw source). An optional trailing instruction
// lets callers (e.g. a future violation-generator) append extra guidance
// without duplicating this scaffolding.
func buildUser(goal, code string, sigs []repoindex.Signature, instruction string) string {
	sigJSON, _ := json.Marshal(sigs)
	var b strings.Builder
	fmt.Fprintf(&b, "GOAL:\n%s\n\nTARGET FILE:\n%s\n\nSIGNATURE SURFACE (JSON):\n%s\n", goal, code, sigJSON)
	if instruction != "" {
		fmt.Fprintf(&b, "\n%s\n", instruction)
	}
	return b.String()
}

// WriteTest asks m to write a Go test that verifies code satisfies goal,
// using sigs as the signature surface. It does not compile or run the
// result — an empty response (after fence-stripping) is the only failure
// mode caught here.
func WriteTest(ctx context.Context, m LLM, goal, code string, sigs []repoindex.Signature) (string, error) {
	resp, err := m.Ask(ctx, writeTestSystem, buildUser(goal, code, sigs, ""))
	if err != nil {
		return "", err
	}
	test := extractCode(resp)
	if strings.TrimSpace(test) == "" {
		return "", errors.New("testgen: writer returned no code")
	}
	return test, nil
}

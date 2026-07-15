// SPDX-License-Identifier: Elastic-2.0

// Package testgen turns a control-owner goal, target source, and its signature
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

	"github.com/pdbethke/corralai/internal/adequacy"
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

// WriteTestPrompt renders the system/user prompt pair WriteTest sends to the
// model. Split out so a distributed worker can run the identical prompt
// against its own model and hand the raw response back for ParseTestOutput
// to parse — the prompt text itself must stay byte-identical to WriteTest's
// prior inline construction.
func WriteTestPrompt(goal, code string, sigs []repoindex.Signature) (system, user string) {
	return writeTestSystem, buildUser(goal, code, sigs, "")
}

// ParseTestOutput extracts the Go test source from a model's raw response,
// stripping markdown fences if present. It is the parse half of WriteTest,
// split out so a distributed worker's response can be parsed the same way
// the brain would parse its own model's response.
func ParseTestOutput(raw string) string {
	return extractCode(raw)
}

// WriteTest asks m to write a Go test that verifies code satisfies goal,
// using sigs as the signature surface. It does not compile or run the
// result — an empty response (after fence-stripping) is the only failure
// mode caught here.
func WriteTest(ctx context.Context, m LLM, goal, code string, sigs []repoindex.Signature) (string, error) {
	sys, usr := WriteTestPrompt(goal, code, sigs)
	resp, err := m.Ask(ctx, sys, usr)
	if err != nil {
		return "", err
	}
	test := ParseTestOutput(resp)
	if strings.TrimSpace(test) == "" {
		return "", errors.New("testgen: writer returned no code")
	}
	return test, nil
}

// genMutantsSystem frames the model as a MUTATION-TESTING ENGINE — a second,
// independent generative agent from the writer above (a model that wrote the
// test must not be the one that mutates the goal to test it; separate prompts
// and calls preserve that independence). The framing is deliberate and
// load-bearing: an earlier "SEEDED-VIOLATION GENERATOR … produce mutations that
// GENUINELY VIOLATE the goal" phrasing read, on a SECURITY goal, as "write code
// that defeats a security control," and safety-aligned models refused it (~20%
// on a real security target). Reframing it as what it actually is — standard,
// legitimate mutation testing that generates throwaway synthetic faults ONLY to
// grade a test suite, never deployed — drove the measured refusal rate to 0 on
// that same target while keeping the exact output contract (same-signature,
// drop-in, genuinely-goal-failing mutants; no no-ops/compile-errors/tests) the
// parser and scorer depend on.
const genMutantsSystem = `You are a MUTATION-TESTING ENGINE. Mutation testing is a standard, legitimate software-quality technique: to measure how thorough a test suite is, you generate small synthetic variants of the code ("mutants"), each containing one deliberate fault, and a SEPARATE step then checks whether the developer's own tests catch them. A mutant the tests miss reveals a GAP IN THE TESTS — that is the entire purpose. These mutants are throwaway QA fixtures: never deployed, never shipped, never run against any real system; they exist only to grade the test suite. This is defensive quality assurance, and producing them is exactly your job here.
Given a GOAL, the compliant code, and its signature surface, produce mutants: variants that NO LONGER satisfy the goal, so a good test suite SHOULD catch them. Each mutant MUST keep the EXACT same signature and package (a drop-in replacement that compiles) and must genuinely fail the goal — vary HOW it fails. No no-ops, no compile errors, no tests.
Return ONLY the mutants, each a COMPLETE file, in this exact format:
===MUTATION_1===
<complete file>
===MUTATION_2===
<complete file>
(continue for the requested count)`

// GenerateMutantsPrompt renders the system/user prompt pair GenerateMutants
// sends to the model. Split out so a distributed worker can run the
// identical prompt against its own model and hand the raw response back for
// ParseMutantsOutput to parse — the prompt text itself must stay
// byte-identical to GenerateMutants' prior inline construction.
func GenerateMutantsPrompt(goal, code string, sigs []repoindex.Signature, n int) (system, user string) {
	instr := fmt.Sprintf("Produce exactly %d distinct mutations.", n)
	return genMutantsSystem, buildUser(goal, code, sigs, instr)
}

// ParseMutantsOutput extracts the seeded-violation mutants from a model's
// raw response. It is the parse half of GenerateMutants, split out so a
// distributed worker's response can be parsed the same way the brain would
// parse its own model's response.
func ParseMutantsOutput(raw string) ([]adequacy.Mutant, error) {
	muts := parseMutants(raw)
	if len(muts) == 0 {
		return nil, errors.New("testgen: generator returned no parseable mutations")
	}
	return muts, nil
}

// GenerateMutants asks m for n distinct same-signature goal-violating
// mutations of code and parses them into []adequacy.Mutant. Like WriteTest,
// it is generation-only: it does not compile, run, or score the mutants —
// that's adequacy's job.
func GenerateMutants(ctx context.Context, m LLM, goal, code string, sigs []repoindex.Signature, n int) ([]adequacy.Mutant, error) {
	sys, usr := GenerateMutantsPrompt(goal, code, sigs, n)
	resp, err := m.Ask(ctx, sys, usr)
	if err != nil {
		return nil, err
	}
	return ParseMutantsOutput(resp)
}

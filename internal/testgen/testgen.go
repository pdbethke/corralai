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
func WriteTestPrompt(system, goal, code string, sigs []repoindex.Signature) (sys, user string) {
	return system, buildUser(goal, code, sigs, "")
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
func WriteTest(ctx context.Context, m LLM, system, goal, code string, sigs []repoindex.Signature) (string, error) {
	sys, usr := WriteTestPrompt(system, goal, code, sigs)
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

// GenerateMutantsPrompt renders the system/user prompt pair GenerateMutants
// sends to the model. Split out so a distributed worker can run the
// identical prompt against its own model and hand the raw response back for
// ParseMutantsOutput to parse — the prompt text itself must stay
// byte-identical to GenerateMutants' prior inline construction.
func GenerateMutantsPrompt(system, goal, code string, sigs []repoindex.Signature, n int) (sys, user string) {
	return system, buildUser(goal, code, sigs, mutantFormatInstruction(n))
}

// mutantFormatInstruction is the SINGLE source of the mutant output format —
// centralized here rather than duplicated in every language plugin's
// MutantSystem (DRY). It asks for minimal, uniquely-anchored SEARCH/REPLACE
// edits (which scale to any file size), not whole-file copies (which overrun
// the model on large files and collapse to one mutant).
func mutantFormatInstruction(n int) string {
	return fmt.Sprintf(`Produce exactly %d distinct mutations. Each mutation is ONE minimal SEARCH/REPLACE edit that makes the code VIOLATE the goal — never the whole file. Output the mutations and NOTHING else, in this exact format:

===MUTATION_1===
%s
<a few EXACT consecutive lines copied VERBATIM from the code above — enough that they occur exactly once in it>
%s
<the same lines, edited so the code now violates the goal>
%s
===MUTATION_2===
%s
…
%s
…
%s
(continue for all %d)

Rules: the SEARCH block MUST match the original bytes exactly, indentation included, and occur exactly once; the REPLACE block MUST differ from it and keep the code compiling/importing (a drop-in replacement, same signatures). Vary HOW each mutant fails the goal. No no-ops, no whole-file dumps, no prose.`,
		n, srSearchHead, srDivider, srReplaceEnd, srSearchHead, srDivider, srReplaceEnd, n)
}

// ParseMutantsOutput extracts the seeded-violation mutants from a model's raw
// response and applies each SEARCH/REPLACE hunk to `original` to reconstruct
// the full mutant, dropping any that don't apply cleanly (see parseMutants /
// applyMutation for the single-point-edit integrity guarantee). It is the
// parse half of GenerateMutants, split out so a distributed worker's response
// is parsed the same way the brain would parse its own model's — which is why
// it takes the original code the worker was given, to apply the hunks against.
func ParseMutantsOutput(raw, original string) ([]adequacy.Mutant, error) {
	muts := parseMutants(raw, original)
	if len(muts) == 0 {
		return nil, errors.New("testgen: generator returned no parseable, cleanly-applying mutations")
	}
	return muts, nil
}

// GenerateMutants asks m for n distinct same-signature goal-violating
// mutations of code and parses them into []adequacy.Mutant. Like WriteTest,
// it is generation-only: it does not compile, run, or score the mutants —
// that's adequacy's job.
func GenerateMutants(ctx context.Context, m LLM, system, goal, code string, sigs []repoindex.Signature, n int) ([]adequacy.Mutant, error) {
	sys, usr := GenerateMutantsPrompt(system, goal, code, sigs, n)
	resp, err := m.Ask(ctx, sys, usr)
	if err != nil {
		return nil, err
	}
	return ParseMutantsOutput(resp, code)
}

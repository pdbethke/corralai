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

// strictnessCore is the half that is genuinely shared: rules every seat
// producing code must obey, regardless of whether it edits a hunk or authors a
// whole file. All were MEASURED as failures, not imagined.
func strictnessCore() string {
	return `- A DECLARED VARIABLE that is never used will not compile in Go. Do not declare a value you do not then use.
- ` + "`:=`" + ` requires at least one NEW variable on the left. Use ` + "`=`" + ` when reassigning existing ones.`
}

// MutantStrictnessNote is the generator's version. Its defining constraint is
// that a minimal SEARCH/REPLACE hunk CANNOT REACH THE IMPORT BLOCK: it can
// neither add an import it needs nor remove one it stranded.
//
// Measured failures behind each line:
//
//	"fmt" imported and not used      -> removed the last call into a package
//	undefined: time                  -> referenced a package not imported
//	expected '(', found readLoadAvg   -> dropped a closing brace
func MutantStrictnessNote() string {
	return `LANGUAGE STRICTNESS — these are HARD ERRORS in Go (not warnings), and are the most common reason an otherwise-correct mutation is thrown away:
` + strictnessCore() + `
- Your edit is a PARTIAL hunk and CANNOT ADD AN IMPORT. Never reference a package that is not already imported in the file.
- Never remove the LAST remaining use of an imported package: the import becomes unused and the file stops compiling, even though your edit looks local.
- KEEP BRACES/BLOCKS BALANCED: the REPLACE block must open and close exactly the blocks the SEARCH block did. Dropping a closing brace makes everything after it unparseable.
Prefer edits that change a CONDITION, a COMPARISON, a CONSTANT, or an ORDER of operations — those violate a goal without touching imports or block structure.`
}

// WriterStrictnessNote is the test-writer's version, and it says the OPPOSITE
// about imports on purpose. The writer authors a WHOLE FILE, so it MUST declare
// every import its test uses.
//
// The two seats briefly shared one note, which told the writer it "cannot add an
// import" — true for a hunk, actively wrong for a file author. A writer was
// observed failing with `undefined: models` on a dependency-heavy target, which
// is precisely the mistake that instruction would encourage. Sharing the CORE is
// right; sharing the import rule was not.
func WriterStrictnessNote() string {
	return `LANGUAGE STRICTNESS — these are HARD ERRORS in Go (not warnings), and are the most common reason an otherwise-correct test is thrown away:
` + strictnessCore() + `
- You are writing a COMPLETE file: DECLARE EVERY IMPORT your test uses, including packages referenced only inside a helper or a struct literal.
- Do not invent helpers, methods or fields. Use only what the code under test actually exposes; if you need to inject a value, look for an existing seam (a constructor parameter, an exported field) rather than assuming a setter exists.`
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

Rules: the SEARCH block MUST match the original bytes exactly, indentation included, and occur exactly once; the REPLACE block MUST differ from it and keep the code compiling/importing (a drop-in replacement, same signatures). Vary HOW each mutant fails the goal. No no-ops, no whole-file dumps, no prose.

Your edit is LOCAL but compilation is a WHOLE-FILE property.

%s`,
		n, srSearchHead, srDivider, srReplaceEnd, srSearchHead, srDivider, srReplaceEnd, n, MutantStrictnessNote())
}

// ParseMutantsOutput extracts the seeded-violation mutants from a model's raw
// response and applies each SEARCH/REPLACE hunk to `original` to reconstruct
// the full mutant, dropping any that don't apply cleanly (see parseMutants /
// applyMutation for the single-point-edit integrity guarantee). It is the
// parse half of GenerateMutants, split out so a distributed worker's response
// is parsed the same way the brain would parse its own model's — which is why
// it takes the original code the worker was given, to apply the hunks against.
func ParseMutantsOutput(raw, original string) ([]adequacy.Mutant, error) {
	muts, diag := parseMutantsDiag(raw, original)
	if err := diag.Error(); err != nil {
		// The diagnosis names WHICH gate rejected each block. The old message
		// ("no parseable, cleanly-applying mutations") was true of a malformed
		// block, a missing marker and a one-tab indentation slip alike, and
		// told an operator nothing about which had happened.
		return nil, err
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

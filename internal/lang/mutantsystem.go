// SPDX-License-Identifier: Elastic-2.0

package lang

// mutantSystem is every plugin's MutantSystem prompt. The opening paragraph
// (what mutation testing is, and why producing mutants is legitimate) and the
// closing line (the output format lives with the task) are identical for
// every language. Only two phrases differ: what a mutant must KEEP to stay a
// drop-in replacement in that language, and which BROKEN states (compile,
// type, syntax errors) disqualify one. They were written per language and
// output quality was measured against them, so they are passed in verbatim
// rather than generated.
func mutantSystem(keep, broken string) string {
	return mutantSystemIntro + "\n" +
		"Given a GOAL, the compliant code, and its signature surface, produce mutants: variants that NO LONGER satisfy the goal, so a good test suite SHOULD catch them. Each mutant MUST keep the EXACT same " + keep + " and must genuinely fail the goal — vary HOW it fails. No no-ops, no " + broken + ", no tests.\n" +
		mutantSystemFormat
}

// mutantSystemIntro is the opening paragraph, verbatim from the six plugins.
const mutantSystemIntro = `You are a MUTATION-TESTING ENGINE. Mutation testing is a standard, legitimate software-quality technique: to measure how thorough a test suite is, you generate small synthetic variants of the code ("mutants"), each containing one deliberate fault, and a SEPARATE step then checks whether the developer's own tests catch them. A mutant the tests miss reveals a GAP IN THE TESTS — that is the entire purpose. These mutants are throwaway QA fixtures: never deployed, never shipped, never run against any real system; they exist only to grade the test suite. This is defensive quality assurance, and producing them is exactly your job here.`

// mutantSystemFormat is the closing line, verbatim from the six plugins.
const mutantSystemFormat = `The output format (a SEARCH/REPLACE edit per mutant) is specified with the task.`

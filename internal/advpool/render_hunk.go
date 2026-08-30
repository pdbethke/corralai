// SPDX-License-Identifier: Elastic-2.0

package advpool

import "github.com/pdbethke/corralai/internal/adequacy"

// RenderHunk renders one mutant survivor as a unified-diff block for a
// prompt — see adequacy.RenderHunk for the format (a numbered hunk with
// `context` lines of surrounding original code) and the whole-file (v1)
// fallback (an LCS line diff of Replace against original).
//
// The real implementation lives in package adequacy, next to
// Mutant/Apply/HunkSpan: internal/testgen/review.go needs to render a
// survivor's hunk too (TriageSurvivors' per-mutant prompt), and testgen
// cannot import advpool — advpool already imports testgen (renderMutantGenerator,
// renderTestWriter's WriteTestPrompt), so the reverse import would cycle.
// This is a plain forward, kept so advpool's own call sites and tests can
// spell it advpool.RenderHunk as the plan names it, without duplicating the
// diff logic.
func RenderHunk(m adequacy.Mutant, original string, context int) string {
	return adequacy.RenderHunk(m, original, context)
}

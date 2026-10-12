// SPDX-License-Identifier: Elastic-2.0

package lang

// CommandScoper is an OPTIONAL plugin extension for a language whose test
// runner grades a UNIT LARGER THAN A FILE and must be told which one: cargo
// grades a package, and in a workspace `cargo test` from the root grades every
// member. ScopeCmd returns testCmd rewritten to grade the unit that owns
// codePath, or an error when it cannot say which that is — a refusal, never a
// guess, because a wrong unit grades the wrong suite and reads as a verdict.
//
// It is applied to the command BEFORE the baseline runs, so the baseline and
// every scored run grade the same unit. Callers do not call it directly: they
// go through ScopeTestCmd.
type CommandScoper interface {
	// ScopeCmd scopes testCmd to the unit owning codePath (repo-relative, or
	// absolute under repoDir). repoDir is the checkout; "" means there is none
	// to read, which a scoper that needs one must refuse.
	ScopeCmd(repoDir, codePath string, testCmd []string) ([]string, error)
}

// ScopeTestCmd is the one door every entry that fixes a per-file test command
// goes through. A plugin that does not implement CommandScoper gets testCmd
// back unchanged, which is every language but Rust today.
func ScopeTestCmd(p Plugin, repoDir, codePath string, testCmd []string) ([]string, error) {
	if s, ok := p.(CommandScoper); ok {
		return s.ScopeCmd(repoDir, codePath, testCmd)
	}
	return testCmd, nil
}

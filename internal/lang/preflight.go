// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// toolOnPath reports a fail-closed error if the named executable is not on
// PATH — the toolchain a plugin needs to grade in the jail. exec.LookPath
// already does the right thing for BOTH shapes this is called with: a bare
// name ("python3") is searched on PATH, and a name containing a path
// separator (".venv/bin/python", "/abs/path/to/ruby") is tried directly, PATH
// not consulted — exactly "is this binary the operator NAMED runnable",
// whichever form they gave it in.
func toolOnPath(name string) error {
	if _, err := exec.LookPath(name); err != nil {
		return fmt.Errorf("lang: required tool %q not found on PATH: %w", name, err)
	}
	return nil
}

// envAssignPattern matches a POSIX-shell-style leading environment
// assignment token (VAR=value) — an idiom this codebase's own jail-command
// building uses (see python.go's pyCachePrefixEnv) and a common operator
// idiom on the CLI (`-- PYTHONPATH=src pytest -q`).
var envAssignPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// shellOperatorTokens are argv tokens that mean something only to a shell,
// never to exec: their presence ANYWHERE in testCmd means the whole command
// is a shell pipeline/sequence (e.g. `cd sub && pytest -q`), not a single
// program invocation a preflight check can safely name a binary from.
var shellOperatorTokens = map[string]bool{
	"&&": true, "||": true, ";": true, "|": true,
	"<": true, ">": true, "(": true, ")": true,
}

// stripLeadingEnvAssignments drops leading VAR=value tokens (as many as
// there are) so a caller that needs "the actual command", not a shell
// env-assignment prefix, sees the real invocation. A pure prefix strip: an
// assignment-shaped token appearing LATER in testCmd (never valid shell
// syntax outside the leading run anyway) is left alone.
func stripLeadingEnvAssignments(testCmd []string) []string {
	i := 0
	for i < len(testCmd) && envAssignPattern.MatchString(strings.TrimSpace(testCmd[i])) {
		i++
	}
	return testCmd[i:]
}

// looksShellShaped reports whether tok could only ever be meaningful to a
// shell, never passed directly to exec.LookPath/exec.Command as a program
// name: whitespace, quoting, `$`, backticks, or a control operator EMBEDDED
// in the token (as opposed to being its own separate token — see
// shellOperatorTokens). This is what catches a plugin's own shell-snippet
// stock command (ruby.go's TestCmd(), a single argv element built to be
// space-joined and run under `sh -c` — see that method's own doc comment).
func looksShellShaped(tok string) bool {
	return strings.ContainsAny(tok, " \t\n;$\"'`&|<>(){}")
}

// firstExecutableToken finds the testCmd token that names the actual
// program to run — skipping leading VAR=value environment assignments —
// and reports ok=false when testCmd is not a single plain invocation a
// preflight check can safely name a binary from at all: a shell pipeline
// (any shellOperatorTokens member present, e.g. `cd sub && pytest -q`) or a
// token that is itself shell-shaped (looksShellShaped, e.g. ruby.go's
// TestCmd() stock snippet). In either false case, treating testCmd[0]
// literally as an executable name — the bug this function exists to
// prevent — would either refuse a legitimate operator command outright (an
// env-assignment prefix, a `&&` sequence) or hand a whole multi-line shell
// script to exec.LookPath as if it were a program name. The caller must
// fall back to its OWN stock default in either case, never guess further.
func firstExecutableToken(testCmd []string) (string, bool) {
	for _, tok := range testCmd {
		if shellOperatorTokens[strings.TrimSpace(tok)] {
			return "", false
		}
	}
	// An explicit shell wrapper (`sh -c "<script>"`) names a REAL executable in
	// argv[0], but not a MEANINGFUL one: the program that actually has to exist
	// is buried inside the script, which cannot be parsed reliably. Reporting
	// "sh" would make Preflight check that a shell is installed — true on every
	// box — and silently stop checking for the language's own toolchain, so a
	// host with no ruby would pass preflight and fail at run time instead of
	// refusing up front.
	//
	// Ruby's stock TestCmd is exactly this shape: it must discover the test file
	// and choose between rspec and ruby, so it invokes a shell explicitly rather
	// than smuggling the script into argv[0] (which broke the workspace
	// substrate, where argv is exec'd directly).
	if len(testCmd) > 1 {
		switch strings.TrimSpace(testCmd[0]) {
		case "sh", "bash", "zsh":
			if strings.TrimSpace(testCmd[1]) == "-c" {
				return "", false
			}
		}
	}
	for _, tok := range stripLeadingEnvAssignments(testCmd) {
		trimmed := strings.TrimSpace(tok)
		if trimmed == "" {
			continue
		}
		if looksShellShaped(trimmed) {
			return "", false
		}
		return trimmed, true
	}
	return "", false
}

// preflightBin picks which binary Preflight checks presence of: the
// operator's own test command's actual program token when one can be
// safely identified (firstExecutableToken — this skips a leading
// VAR=value environment-assignment prefix, and declines rather than
// guessing when testCmd is shell-shaped, e.g. a `&&` pipeline or a
// plugin's own shell-snippet stock command), else fallback, the plugin's
// stock default. Used by every plugin whose Preflight only needs a
// presence check (go, ruby, javascript); python and typescript
// additionally probe deeper (pytest importability / tsc).
func preflightBin(testCmd []string, fallback string) string {
	if bin, ok := firstExecutableToken(testCmd); ok {
		return bin
	}
	return fallback
}

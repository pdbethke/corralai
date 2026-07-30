// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"sort"
	"testing"
)

// sortedPluginNames returns the registry's keys in a stable order so test
// output (and any t.Run subtests) is deterministic across runs, since
// registry iteration order is not.
func sortedPluginNames() []string {
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// TestPluginStockCommandSatisfiesOwnPreflight is the self-consistency
// property this file exists to pin: for every registered plugin,
// p.Preflight(p.TestCmd()) must agree with p.Preflight(nil) — a plugin
// whose OWN stock command fails its OWN preflight gate is incoherent by
// construction. This is exactly the shape of the Ruby regression: TestCmd()
// returned a single-element `sh -c` shell snippet (see ruby.go's doc
// comment), and a naive testCmd-aware preflight LookPath'd it as a literal
// binary name, refusing every Ruby file on the default no-`--` invocation.
//
// The property under test is AGREEMENT between the two forms, not toolchain
// presence: if Preflight(nil) already fails for want of a toolchain (e.g.
// this host has no ruby/node/tsc installed), that plugin is skipped rather
// than asserted on — this must pass on a bare host and in CI, neither of
// which is guaranteed to have every language toolchain installed.
func TestPluginStockCommandSatisfiesOwnPreflight(t *testing.T) {
	for _, name := range sortedPluginNames() {
		p := registry[name]
		t.Run(name, func(t *testing.T) {
			nilErr := p.Preflight(nil)
			if nilErr != nil {
				t.Skipf("skipping %s: Preflight(nil) already fails for want of a toolchain on this host: %v", name, nilErr)
			}

			stockErr := p.Preflight(p.TestCmd())
			if stockErr != nil {
				t.Fatalf("%s: Preflight(nil) passed but Preflight(TestCmd()) failed — this plugin's own stock command does not satisfy its own preflight gate: %v\nTestCmd() = %v", name, stockErr, p.TestCmd())
			}
		})
	}
}

// TestPluginStockCommandSatisfiesOwnCoverageCmd is the sibling assertion:
// for every plugin implementing CoverageReporter, CoverageCmd(TestCmd())
// must report ok=true — a plugin that cannot instrument its own stock
// command is likewise inconsistent with itself, for the same reason a
// plugin whose stock command fails its own preflight is.
func TestPluginStockCommandSatisfiesOwnCoverageCmd(t *testing.T) {
	for _, name := range sortedPluginNames() {
		p := registry[name]
		cr, ok := p.(CoverageReporter)
		if !ok {
			continue
		}
		t.Run(name, func(t *testing.T) {
			if _, ok := cr.CoverageCmd(p.TestCmd()); !ok {
				t.Fatalf("%s: CoverageCmd(TestCmd()) = ok=false — this plugin cannot instrument its own stock command\nTestCmd() = %v", name, p.TestCmd())
			}
		})
	}
}

// TestPluginCompileCheckIsDirectlyExecutable closes the class of bug behind
// the pallets/flask PYTHONPYCACHEPREFIX regression and its ruby/javascript
// siblings: a plugin's CompileCheck sequence must be runnable by a bare
// exec.Command with NO shell involved — see the workspace substrate
// (internal/adequacy/workspace.go), which execs cmdArgv[0] directly and has
// no `sh -c` to interpret a stray shell token. Two ways CompileCheck output
// silently depended on an external shell were found and fixed by this
// point in the codebase's history:
//
//  1. python's old CompileCheck put a bare "VAR=value" environment
//     assignment in as argv[0] — meaningless to exec.Command, which just
//     tries (and fails) to run a file by that literal name.
//  2. ruby's and javascript's old CompileCheck joined TWO invocations with
//     a literal "&&" argv element — meaningless to exec.Command, which
//     hands it to the first program as an ordinary argument (a silent
//     false pass, not even a crash, since neither `ruby -c` nor `node
//     --check` inspects arguments past their own first file).
//
// This asserts, for every registered plugin's CompileCheck("code.ext",
// "test.ext") output: every command in the sequence has at least one
// token, and no token is a bare shell control operator
// (shellOperatorTokens — "&&", "||", ";", "|", "<", ">", "(", ")") or a
// bare VAR=value environment-assignment shape (envAssignPattern) sitting
// where a program name would be exec'd (argv[0] of each command in the
// sequence). It does not require the named programs to actually exist on
// this host (that is Preflight's job, and this must pass on a bare host
// with no language toolchains installed) — only that the ARGV SHAPE itself
// carries no shell-only meaning.
func TestPluginCompileCheckIsDirectlyExecutable(t *testing.T) {
	for _, name := range sortedPluginNames() {
		p := registry[name]
		t.Run(name, func(t *testing.T) {
			seq := p.CompileCheck("code.ext", "test.ext")
			for i, cmd := range seq {
				if len(cmd) == 0 {
					t.Fatalf("%s: CompileCheck() sequence entry %d is empty — nothing to exec", name, i)
				}
				if shellOperatorTokens[cmd[0]] {
					t.Fatalf("%s: CompileCheck() sequence entry %d has a shell control operator (%q) as its program name — this only means something under a shell; the workspace substrate execs it literally\nCompileCheck() = %v", name, i, cmd[0], seq)
				}
				if envAssignPattern.MatchString(cmd[0]) {
					t.Fatalf("%s: CompileCheck() sequence entry %d has a bare VAR=value token (%q) as its program name — a shell would treat this as an env assignment, but exec.Command tries to run a file by that literal name; set it on the process (e.g. via the env(1) program) instead\nCompileCheck() = %v", name, i, cmd[0], seq)
				}
				for _, tok := range cmd {
					if shellOperatorTokens[tok] {
						t.Fatalf("%s: CompileCheck() sequence entry %d contains a bare shell control-operator token (%q) — split it into a separate command in the sequence instead of relying on a shell to interpret it\nCompileCheck() = %v", name, i, tok, seq)
					}
				}
			}
		})
	}
}

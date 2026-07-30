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

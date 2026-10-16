// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"slices"
	"testing"
)

// The jail binds the toolchain of the command it runs: the operator's test
// command, in certify. A review reproduction is a MODEL's script, so its words
// must not choose host directories to mount (a word like /opt/x/bin/y binds
// /opt/x). WithToolchainFrom names the command the toolchain is read from
// instead — the scope language's own test command — and an empty one binds
// no toolchain beyond what the jail always mounts.
func TestWithToolchainFromReplacesTheScriptAsTheToolchainSource(t *testing.T) {
	script := []string{"sh", "-c", "/opt/vendor/bin/tool --x"}
	plain := NewEnumerator(nil, 0).(bwrapJail)
	if got := plain.toolchainCmd(script); !slices.Equal(got, script) {
		t.Errorf("by default the run command is the source: %q", got)
	}
	from := NewEnumerator(nil, 0, WithToolchainFrom([]string{"cargo", "test"})).(bwrapJail)
	if got := from.toolchainCmd(script); !slices.Equal(got, []string{"cargo", "test"}) {
		t.Errorf("WithToolchainFrom was ignored: %q", got)
	}
	none := NewEnumerator(nil, 0, WithToolchainFrom(nil)).(bwrapJail)
	if got := none.toolchainCmd(script); got != nil {
		t.Errorf("an empty source must bind nothing from the script: %q", got)
	}
}

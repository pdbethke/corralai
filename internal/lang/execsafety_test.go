// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"strings"
	"testing"
)

// Every stock TestCmd must be EXEC-SAFE: argv[0] has to be a real program name,
// because the workspace substrate execs argv directly with no shell. Only the
// jail substrate shell-joins and runs under `sh -c`, so a command that smuggles
// shell syntax into argv works there and fails everywhere else.
//
// Ruby's TestCmd returned a single element containing an entire shell script:
//
//	t="$(ls *_test.rb ...)"; ... if grep -Eq ...; then exec rspec "$t"; else ...
//
// On the workspace substrate that becomes exec("t=\"$(ls", ...) — "executable
// file not found in $PATH" — which is exactly how the first real rubocop audit
// died. The irony is that CompileCheck, six lines below it in the same file,
// documents this precise bug class and explains how it avoids it.
//
// A compound command must be passed EXPLICITLY as {"sh", "-c", script}, which
// is correct on both substrates: the workspace execs sh, and the jail's
// shell-join wraps it in another sh -c, which nests harmlessly.
func TestStockTestCmdsAreExecSafe(t *testing.T) {
	for _, name := range []string{"go", "python", "ruby", "javascript", "typescript"} {
		p, ok := ByName(name)
		if !ok {
			t.Fatalf("plugin %q not registered", name)
		}
		cmd := p.TestCmd()
		if len(cmd) == 0 {
			t.Errorf("%s: TestCmd is empty", name)
			continue
		}
		argv0 := cmd[0]

		// argv[0] is handed to exec as a program name. Anything a shell would
		// have to interpret means this command only runs under a shell.
		for _, bad := range []string{" ", "$", "\"", "'", ";", "|", "&", "(", ")", ">", "<", "`"} {
			if strings.Contains(argv0, bad) {
				t.Errorf("%s: TestCmd argv[0] = %q contains %q — the workspace substrate execs this directly, so it must be a bare program name (use {\"sh\",\"-c\",script} for a compound command)",
					name, argv0, bad)
				break
			}
		}

		// A script passed as a later argument is fine, but only when argv[0] is
		// actually a shell — otherwise the script is handed to a program that
		// will not interpret it.
		if len(cmd) > 1 {
			joined := strings.Join(cmd[1:], " ")
			if strings.ContainsAny(joined, "$;|`") && argv0 != "sh" && argv0 != "bash" && argv0 != "env" {
				t.Errorf("%s: TestCmd carries shell syntax in %q but argv[0] is %q, not a shell — nothing will interpret it",
					name, joined, argv0)
			}
		}
	}
}

// TestRubyTestCmdRunsUnderAShell pins the specific repair: ruby's stock command
// genuinely needs shell logic (it discovers the test file and chooses between
// rspec and ruby), so it must SAY so by invoking a shell explicitly rather than
// relying on a caller to shell-join it.
func TestRubyTestCmdRunsUnderAShell(t *testing.T) {
	p, _ := ByName("ruby")
	cmd := p.TestCmd()
	if len(cmd) < 3 {
		t.Fatalf("ruby TestCmd = %v, want an explicit shell invocation", cmd)
	}
	if cmd[0] != "sh" || cmd[1] != "-c" {
		t.Fatalf("ruby TestCmd = %v, want it to start with sh -c", cmd[:2])
	}
	// And the script must still do the work it always did.
	script := cmd[2]
	for _, want := range []string{"rspec", "ruby"} {
		if !strings.Contains(script, want) {
			t.Errorf("ruby TestCmd script lost its %q branch: %s", want, script)
		}
	}
}

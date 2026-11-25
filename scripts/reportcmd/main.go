// SPDX-License-Identifier: Elastic-2.0

// Command reportcmd prints the command a graded run of a test command runs
// (lang.ReportCmd: the row's report flags and its failure hook), one word
// per NUL-terminated field, for a language and a test command:
// `go run ./scripts/reportcmd php vendor/bin/phpunit fail/tests/FTest.php`.
//
// scripts/capture-runner-fixtures.sh uses it to capture a hooked run. A copy
// of the hook's wrapper script kept in the capture script would be a second
// place to change it, and a fixture captured under the copy would prove
// nothing about the row (listunitscmd's reason, for the same kind of file).
package main

import (
	"fmt"
	"os"

	"github.com/pdbethke/corralai/internal/lang"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: reportcmd <language> <test command...>")
		os.Exit(2)
	}
	p, ok := lang.ByName(os.Args[1])
	if !ok {
		fmt.Fprintf(os.Stderr, "reportcmd: no language %q\n", os.Args[1])
		os.Exit(2)
	}
	cmd, ok := lang.ReportCmd(p, lang.Cmd(os.Args[2:]), os.Args[2:])
	if !ok {
		fmt.Fprintf(os.Stderr, "reportcmd: corral adds nothing to %q\n", os.Args[2:])
		os.Exit(1)
	}
	for _, w := range cmd {
		fmt.Print(w, "\x00")
	}
}

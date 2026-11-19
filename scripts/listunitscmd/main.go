// SPDX-License-Identifier: Elastic-2.0

// Command listunitscmd prints the shell script a runner row's unit listing
// runs under (lang.ListUnitsCmd's `sh -c` script) for a language and a test
// command: `go run ./scripts/listunitscmd php vendor/bin/phpunit`.
//
// scripts/capture-runner-fixtures.sh uses it for PHPUnit, whose listing runs
// under a setup the row owns (lang.Runner.ListUnitsSetup). The script used
// to keep its own copy of the listing prelude; a copy of a setup that writes
// a PHP file would be a second place to change it, and a fixture captured
// under the copy would prove nothing about the row.
package main

import (
	"fmt"
	"os"

	"github.com/pdbethke/corralai/internal/lang"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: listunitscmd <language> <test command...>")
		os.Exit(2)
	}
	p, ok := lang.ByName(os.Args[1])
	if !ok {
		fmt.Fprintf(os.Stderr, "listunitscmd: no language %q\n", os.Args[1])
		os.Exit(2)
	}
	cmd, ok := lang.ListUnitsCmd(p, lang.Cmd(os.Args[2:]))
	if !ok || len(cmd) < 4 || cmd[0] != "sh" || cmd[1] != "-c" {
		fmt.Fprintf(os.Stderr, "listunitscmd: %q has no unit listing\n", os.Args[2:])
		os.Exit(1)
	}
	fmt.Print(cmd[2])
}

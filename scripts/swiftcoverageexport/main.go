// SPDX-License-Identifier: Elastic-2.0

// Command swiftcoverageexport prints the script swiftPlugin.CoverageCmd runs
// for a `swift test` command, ending with the raw llvm-cov export instead of
// its reduction (lang.SwiftCoverageExportCmd), as NUL-terminated words:
//
//	go run ./scripts/swiftcoverageexport swift test
//
// scripts/capture-runner-fixtures.sh runs those words where it runs Swift, to
// record testdata/runners/swift-codecov.json with the plugin's own steps
// rather than a copy of them. It exits 2, printing nothing, when the command
// is one CoverageCmd refuses.
//
// It lives in scripts/ and not cmd/ because scripts/gen-cli-docs.sh publishes
// a CLI reference page for every directory under cmd/, and nobody runs this
// but that script.
package main

import (
	"fmt"
	"os"

	"github.com/pdbethke/corralai/internal/lang"
)

func main() {
	argv, ok := lang.SwiftCoverageExportCmd(os.Args[1:])
	if !ok {
		fmt.Fprintf(os.Stderr, "swiftcoverageexport: swiftPlugin.CoverageCmd refuses %q\n", os.Args[1:])
		os.Exit(2)
	}
	for _, w := range argv {
		fmt.Printf("%s\x00", w)
	}
}

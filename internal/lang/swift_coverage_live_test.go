// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A COVERAGE RUN READS ONLY THE PRODUCTS THAT RAN IN IT. The coverage build
// directory (.build/corral-coverage) outlives a run: the workspace substrate
// reuses it, an operator re-runs, CI may cache .build. Under swiftbuild a
// test target is its own product, and when the target goes the product stays
// on disk, carrying a coverage map of the code it was built from. Taking every
// product with a coverage map read that one too (measured 2026-11-20 on Swift
// 6.3.2 and 6.4.2 with --build-system swiftbuild): after a test target was
// removed and Calc.swift lost `sub`, the report still held the removed
// target's file and read Calc.swift as 3 of 6 lines where a fresh build
// directory gives 3 of 3. Run against the real toolchain, both build systems,
// in one build directory that sees the change; skipped without swift.
func TestSwiftCoverageReadsOnlyThisRunsProducts(t *testing.T) {
	if _, err := exec.LookPath("swift"); err != nil {
		t.Skip("swift not installed — this check needs the real toolchain")
	}
	for _, bs := range []string{"native", "swiftbuild"} {
		t.Run(bs, func(t *testing.T) {
			dir := physicalTempDir(t)
			if out, err := exec.Command("cp", "-r", filepath.Join("testdata", "coverage", "swift")+"/.", dir).CombinedOutput(); err != nil {
				t.Fatalf("copy fixture: %v\n%s", err, out)
			}
			_ = os.RemoveAll(filepath.Join(dir, ".build"))
			write := func(rel, text string) {
				t.Helper()
				p := filepath.Join(dir, rel)
				if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			manifest, err := os.ReadFile(filepath.Join(dir, "Package.swift")) // #nosec G304 -- the fixture's copy
			if err != nil {
				t.Fatal(err)
			}
			const extra = "        .testTarget(name: \"ExtraTests\", dependencies: [\"Calc\"]),\n"
			const xc = "        .testTarget(name: \"CalcXCTests\", dependencies: [\"Calc\"]),\n"
			if !strings.Contains(string(manifest), xc) {
				t.Fatalf("the fixture's manifest no longer declares CalcXCTests as this test expects:\n%s", manifest)
			}
			write("Package.swift", strings.Replace(string(manifest), xc, xc+extra, 1))
			write("Tests/ExtraTests/ExtraTests.swift", "import XCTest\n@testable import Calc\n\nfinal class ExtraTests: XCTestCase {\n    func testSubtracts() {\n        XCTAssertEqual(sub(3, 1), 2)\n    }\n}\n")

			testCmd := []string{"swift", "test", "--build-system", bs}
			run := func() (map[string]LineCounts, string) {
				t.Helper()
				cmd, ok := swiftPlugin{}.CoverageCmd(testCmd)
				if !ok {
					t.Fatalf("CoverageCmd refused %q", testCmd)
				}
				c := exec.Command(cmd[0], cmd[1:]...) // #nosec G204 -- the command under test
				c.Dir = dir
				var stderr strings.Builder
				c.Stderr = &stderr
				out, _ := c.Output()
				got, err := swiftPlugin{}.CoverageCounts(string(out), "")
				if err != nil {
					t.Fatalf("CoverageCounts: %v\nstdout:\n%s\nstderr:\n%s", err, out, stderr.String())
				}
				return got, string(out)
			}
			if got, out := run(); got["Tests/ExtraTests/ExtraTests.swift"].Executed == 0 {
				t.Fatalf("the first run did not run ExtraTests:\n%s", out)
			}

			// The test target goes, and Calc.swift keeps add alone.
			write("Package.swift", string(manifest))
			if err := os.RemoveAll(filepath.Join(dir, "Tests", "ExtraTests")); err != nil {
				t.Fatal(err)
			}
			write("Sources/Calc/Calc.swift", "// add is called by the fixture's tests.\npublic func add(_ a: Int, _ b: Int) -> Int {\n    a + b\n}\n")
			got, out := run()
			if want := (LineCounts{Executed: 3, Executable: 3}); got["Sources/Calc/Calc.swift"] != want {
				t.Errorf("Sources/Calc/Calc.swift = %+v, want %+v (a fresh build directory's count):\n%s", got["Sources/Calc/Calc.swift"], want, out)
			}
			if c, ok := got["Tests/ExtraTests/ExtraTests.swift"]; ok {
				t.Errorf("the removed test target's file is still reported (%+v):\n%s", c, out)
			}
		})
	}
}

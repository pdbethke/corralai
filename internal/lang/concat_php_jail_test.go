// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestPHPConcatFakeJailBothClassesAreDeclared is Task 2's "prove your choice
// in the fake-jail fixture" proof for the central design decision behind
// phpPlugin.ConcatTests: PHPUnit's directory-based collection require()s a
// matching file and reflects on whichever TestCase subclasses it finds newly
// declared — it does not look a class up by name — so a merged file holding
// several distinctly (or suffix-disambiguated) named classes is not a
// gamble, and suffixing a colliding class name (rather than refusing the
// merge, or refusing to ever merge at all) is sound.
//
// This proves the PRECONDITION offline, with only a bare `php` interpreter
// on PATH — no PHPUnit, no composer, no network, exactly the constraint a
// CI runner without the full PHP toolchain is under: requiring the merged
// file (the shape PHPUnit's own loader uses) declares BOTH suffixed classes,
// and each proven test method is still callable. Skips cleanly on a host
// with no php on PATH, mirroring TestPluginStockCommandSatisfiesOwnPreflight's
// own skip for a missing toolchain.
func TestPHPConcatFakeJailBothClassesAreDeclared(t *testing.T) {
	if _, err := exec.LookPath("php"); err != nil {
		t.Skip("no php on PATH — cannot prove the collection precondition on this host")
	}
	c := concatenatorFor(t, "php")
	merged, err := c.ConcatTests([]AuthoredPart{
		{MutantID: "s0/m1", Source: "<?php\n\nclass InvoiceTest\n{\n    public function testA(): bool\n    {\n        return true;\n    }\n}\n"},
		{MutantID: "s0/m2", Source: "<?php\n\nclass InvoiceTest\n{\n    public function testB(): bool\n    {\n        return true;\n    }\n}\n"},
	})
	if err != nil {
		t.Fatalf("ConcatTests: %v", err)
	}
	if strings.Contains(merged, "class InvoiceTest\n") || strings.HasSuffix(strings.TrimSpace(merged), "class InvoiceTest") {
		t.Fatalf("the collision was not disambiguated before this proof ran:\n%s", merged)
	}

	dir := t.TempDir()
	mergedPath := filepath.Join(dir, "InvoiceTest.php")
	if err := os.WriteFile(mergedPath, []byte(merged), 0o644); err != nil {
		t.Fatal(err)
	}

	// A tiny driver that requires the merged file exactly the way PHPUnit's
	// own directory-based TestSuiteLoader does (a plain require, then
	// inspect what got declared), and calls every test* method it finds —
	// proving both proven tests are reachable, not merely syntactically
	// present.
	const driver = `<?php
$before = get_declared_classes();
require $argv[1];
$after = get_declared_classes();
$new = array_values(array_diff($after, $before));
sort($new);
foreach ($new as $cls) {
    $obj = new $cls();
    foreach (get_class_methods($obj) as $m) {
        if (strpos($m, 'test') === 0) {
            echo $cls . '::' . $m . '=' . var_export($obj->$m(), true) . "\n";
        }
    }
}
`
	driverPath := filepath.Join(dir, "driver.php")
	if err := os.WriteFile(driverPath, []byte(driver), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command("php", driverPath, mergedPath).CombinedOutput()
	if err != nil {
		t.Fatalf("php driver failed: %v\n%s", err, out)
	}
	got := string(out)
	if !strings.Contains(got, "InvoiceTest_s0m1::testA=true") {
		t.Errorf("the first proven test's class/method was not declared+callable:\n%s", got)
	}
	if !strings.Contains(got, "InvoiceTest_s0m2::testB=true") {
		t.Errorf("the second proven test's class/method was not declared+callable — merging into one file lost a proof:\n%s", got)
	}
}

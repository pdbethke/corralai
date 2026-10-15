// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"reflect"
	"testing"
)

// The webmozart/assert shape that sent a Haiku critic to the tool loop
// (2026-10-15): with no list it judged 0, 1, 5 or 6 of the file's tests and
// once filed findings against data-provider cases it named as tests. Each
// rule PHPUnit collects by is here, beside a near miss it does not collect.
func TestPHPTestNamesInSourceListsWhatPHPUnitCollects(t *testing.T) {
	src := `<?php

declare(strict_types=1);

namespace Webmozart\Assert\Tests;

use PHPUnit\Framework\Attributes\DataProvider;
use PHPUnit\Framework\Attributes\Test;
use PHPUnit\Framework\TestCase;

/**
 * class NotAClass { public function testInADocComment() {} }
 */
class AssertTest extends TestCase
{
    use SomeHelpers;

    private const NAME = 'class Fake { public function testInAString() {} }';

    public static function getTests(): array
    {
        $closure = function () { return 1; };
        return [['x', InvalidArgumentException::class, "brace } in a string"]];
    }

    #[DataProvider('getTests')]
    public function testAssert(string $method): void
    {
        $f = function () {
            // function testInAComment() {}
        };
        $this->assertTrue(true);
    }

    #[Test]
    public function rejectsNegativeOne(): void {}

    #[\PHPUnit\Framework\Attributes\Test]
    public function fullyQualifiedAttribute(): void {}

    /**
     * @test
     */
    public function annotatedWithDocblock(): void {}

    /** @testdox not a test marker */
    public function describedButNotATest(): void {}

    function testWithNoVisibilityIsPublic(): void {}

    public static function testStaticIsCollected(): void {}

    protected function testProtectedIsNot(): void {}

    private function testPrivateIsNot(): void {}

    public function helperNotATest(): void
    {
        $heredoc = <<<EOT
        public function testInAHeredoc() {}
        }
        EOT;
        $nowdoc = <<<'EOT'
        class Fake {
        EOT;
    }

    public function Testing(): void {}
}

trait SomeHelpers
{
    public function testInATraitIsNotListedHere(): void {}
}

final class SecondTest extends TestCase
{
    public function testSecond(): void
    {
        $anon = new class {
            public function testInAnAnonymousClass() {}
        };
    }
}
`
	want := []string{
		"AssertTest::testAssert",
		"AssertTest::rejectsNegativeOne",
		"AssertTest::fullyQualifiedAttribute",
		"AssertTest::annotatedWithDocblock",
		"AssertTest::testWithNoVisibilityIsPublic",
		"AssertTest::testStaticIsCollected",
		"SecondTest::testSecond",
	}
	if got := (phpPlugin{}).TestNamesInSource("tests/AssertTest.php", src); !reflect.DeepEqual(got, want) {
		t.Errorf("TestNamesInSource =\n  %q\nwant\n  %q", got, want)
	}
}

// A scan that loses its place lists nothing, never the tests before it went
// wrong: the critic is held to the list as complete, so an undercount would
// forbid judging every test after the point the scanner got lost.
func TestPHPTestNamesInSourceListsNothingWhenItLosesItsPlace(t *testing.T) {
	for name, src := range map[string]string{
		"unterminated string":    "<?php\nclass ATest {\n public function testA() {}\n $x = 'open;\n public function testB() {}\n}\n",
		"unterminated comment":   "<?php\nclass ATest {\n public function testA() {}\n /* open\n public function testB() {}\n}\n",
		"unterminated heredoc":   "<?php\nclass ATest {\n public function testA() { $x = <<<EOT\n text\n }\n}\n",
		"unbalanced braces":      "<?php\nclass ATest {\n public function testA() {\n",
		"stray closing brace":    "<?php\nclass ATest {\n public function testA() {}\n}\n}\n",
		"unterminated attribute": "<?php\nclass ATest {\n #[Test\n public function a() {}\n}\n",
	} {
		if got := (phpPlugin{}).TestNamesInSource("tests/ATest.php", src); got != nil {
			t.Errorf("%s: TestNamesInSource = %q, want nil", name, got)
		}
	}
}

// A test defined twice is one test (PHP refuses the redeclaration, and the
// critic must not be asked to judge it twice).
func TestPHPTestNamesInSourceListsADuplicateOnce(t *testing.T) {
	if got := (phpPlugin{}).TestNamesInSource("t.php", "<?php\nclass ATest {\n public function testA() {}\n public function testA() {}\n}\n"); !reflect.DeepEqual(got, []string{"ATest::testA"}) {
		t.Errorf("duplicate: %q", got)
	}
}

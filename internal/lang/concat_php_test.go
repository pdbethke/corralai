// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"strings"
	"testing"
)

func phpPart(id, members string) AuthoredPart {
	return AuthoredPart{MutantID: id, Source: "<?php\ndeclare(strict_types=1);\n\nnamespace App\\Tests;\n\nuse PHPUnit\\Framework\\TestCase;\nuse App\\Invoice;\n\nfinal class InvoiceTest extends TestCase\n{\n" + members + "}\n"}
}

// PHPUnit 10 and 11 load ONE class per file, the class named after the file
// (a file of suffixed classes ran nothing on 11.5), so proven PHPUnit tests
// merge into that one class: the tag, declare, namespace and use lines once,
// then every part's members in the class. A member two parts write the same
// way is kept once (a shared setUp, a shared helper); a test method two
// parts both declare, differently, is suffixed with its mutant id, as a
// colliding test is in every other language. A file this merge wrote from
// three such parts (a shared setUp and property, two testTotal, an
// attribute-marked test, a data provider) ran all five tests on PHPUnit
// 11.5.57 with PHP 8.5.4 (2026-11-26, "OK (5 tests, 5 assertions)").
func TestPHPMergeFoldsEveryPartIntoTheOneClass(t *testing.T) {
	setUp := "    private Invoice $inv;\n\n    protected function setUp(): void\n    {\n        $this->inv = new Invoice();\n    }\n\n"
	merged, extra := ConcatAuthored(phpPlugin{}, []AuthoredPart{
		phpPart("s0/m1", setUp+"    public function testTotal(): void\n    {\n        $this->assertSame(0, $this->inv->total());\n    }\n"),
		phpPart("s0/m2", setUp+"    public function testTotal(): void\n    {\n        $this->assertSame('{}', json_encode((object) []));\n    }\n"),
		phpPart("s0/m3", "    /** @test */\n    #[\\PHPUnit\\Framework\\Attributes\\Test]\n    public function addsALine(): void\n    {\n        $this->assertTrue(true);\n    }\n"),
	})
	if len(extra) != 0 {
		t.Fatalf("parts carried out: %+v\nmerged:\n%s", extra, merged)
	}
	for _, c := range []struct {
		want string
		n    int
	}{
		{"<?php\n", 1}, {"declare(strict_types=1);\n", 1}, {"namespace App\\Tests;\n", 1},
		{"use PHPUnit\\Framework\\TestCase;\n", 1}, {"use App\\Invoice;\n", 1},
		{"final class InvoiceTest extends TestCase\n{\n", 1},
		{"private Invoice $inv;", 1}, {"protected function setUp(): void", 1},
		{"public function testTotal(): void", 1}, {"public function testTotal_s0_m2(): void", 1},
		{"public function addsALine(): void", 1},
		// A brace inside a string is not a member's end.
		{"json_encode((object) [])", 1},
	} {
		if got := strings.Count(merged, c.want); got != c.n {
			t.Errorf("merged file has %q %d time(s), want %d:\n%s", c.want, got, c.n, merged)
		}
	}
	if !strings.HasSuffix(merged, "}\n") || strings.Count(merged, "final class") != 1 {
		t.Errorf("not one class closed at the end:\n%s", merged)
	}
}

// What cannot be folded is refused, and that part is carried out with the
// reason: a helper two parts write differently (renaming it means rewriting
// its callers, the model's code), a different class declaration (the file
// must declare the class named after it), a different namespace, a second
// class or a function outside the class, and a heredoc, whose body this
// merge does not read.
func TestPHPMergeRefusesWhatItCannotFold(t *testing.T) {
	base := phpPart("s0/m1", "    private function make(): int { return 1; }\n    public function testA(): void { $this->assertSame(1, $this->make()); }\n")
	for name, second := range map[string]AuthoredPart{
		"helper written differently": phpPart("s0/m2", "    private function make(): int { return 2; }\n    public function testB(): void { $this->assertSame(2, $this->make()); }\n"),
		"another class":              {MutantID: "s0/m2", Source: strings.Replace(phpPart("", "    public function testB(): void {}\n").Source, "final class InvoiceTest", "final class OtherTest", 1)},
		"another namespace":          {MutantID: "s0/m2", Source: strings.Replace(phpPart("", "    public function testB(): void {}\n").Source, "namespace App\\Tests;", "namespace Other;", 1)},
		"a second class":             {MutantID: "s0/m2", Source: phpPart("", "    public function testB(): void {}\n").Source + "class Helper {}\n"},
		"a heredoc":                  phpPart("s0/m2", "    public function testB(): void { $s = <<<EOT\n}\nEOT;\n $this->assertSame('}', $s); }\n"),
	} {
		second.MutantID = "s0/m2"
		_, extra := ConcatAuthored(phpPlugin{}, []AuthoredPart{base, second})
		if len(extra) != 1 || extra[0].MutantID != "s0/m2" || extra[0].Reason == "" {
			t.Errorf("%s: extra = %+v, want the second part carried out with a reason", name, extra)
		}
	}
}

// One proven test is handed back exactly as it was proven.
func TestPHPMergeOfOnePartIsThatPart(t *testing.T) {
	one := phpPart("s0/m1", "    public function testA(): void { $this->assertTrue(true); }\n")
	if merged, extra := ConcatAuthored(phpPlugin{}, []AuthoredPart{one}); merged != one.Source || len(extra) != 0 {
		t.Fatalf("merged = %q, extra = %+v", merged, extra)
	}
}

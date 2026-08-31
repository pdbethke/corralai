// SPDX-License-Identifier: Elastic-2.0

package lang

import "testing"

// pytestShortSummary is a real pytest tail: the progress line, the failure
// section, then the "short test summary info" block the parser reads. The id
// must come back WITHOUT pytest's trailing " - <error>" prose.
const pytestShortSummary = `============================= test session starts ==============================
collected 3 items

tests/test_recipes.py .F.                                                [100%]

=================================== FAILURES ===================================
_____________________________ test_scale[double] _______________________________

    def test_scale(factor):
>       assert scale(2, factor) == 4
E       assert 6 == 4

tests/test_recipes.py:12: AssertionError
=========================== short test summary info ============================
FAILED tests/test_recipes.py::test_scale[double] - assert 6 == 4
FAILED tests/test_recipes.py::test_total - AssertionError
========================= 2 failed, 1 passed in 0.31s ==========================
`

// pytestCollectionError is the ERROR shape: pytest could not even collect the
// test. It is a failure id too, but a DIFFERENT kind, so it is prefixed.
const pytestCollectionError = `=========================== short test summary info ============================
ERROR tests/test_recipes.py::test_scale
========================= 1 error in 0.11s =====================================
`

// pytestErrorBeforeFailed proves order decides, not kind: the first summary
// line in the stream wins even when a FAILED line follows it.
const pytestErrorBeforeFailed = `=========================== short test summary info ============================
ERROR tests/test_recipes.py::test_setup
FAILED tests/test_recipes.py::test_scale - boom
`

// pytestNoSummary is a passing run. There is no failure to name, and the
// parser must say so with "" rather than reaching for the nearest string.
const pytestNoSummary = `============================= test session starts ==============================
collected 3 items

tests/test_recipes.py ...                                                [100%]

============================== 3 passed in 0.28s ===============================
`

func TestPythonFirstFailure(t *testing.T) {
	p, ok := ByName("python")
	if !ok {
		t.Fatal("python plugin not registered")
	}
	fp, ok := p.(FailureParser)
	if !ok {
		t.Fatal("python plugin does not implement FailureParser")
	}
	for _, tc := range []struct {
		name   string
		output string
		want   string
	}{
		{"short summary, first FAILED wins", pytestShortSummary, "tests/test_recipes.py::test_scale[double]"},
		{"collection error is prefixed", pytestCollectionError, "error:tests/test_recipes.py::test_scale"},
		{"first line in the stream wins", pytestErrorBeforeFailed, "error:tests/test_recipes.py::test_setup"},
		{"no summary names nothing", pytestNoSummary, ""},
		{"empty output names nothing", "", ""},
		{"prose is not an id", "FAILED because the venv was missing\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := fp.FirstFailure([]byte(tc.output)); got != tc.want {
				t.Errorf("FirstFailure = %q, want %q", got, tc.want)
			}
		})
	}
}

// goTestParentAndSubtest is real `go test -v` output: the parent's own
// `--- FAIL:` line and its subtest's indented one. The FIRST such line in the
// stream is the answer, whichever of the two that turns out to be.
const goTestParentAndSubtest = `=== RUN   TestScale
=== RUN   TestScale/double
    scale_test.go:12: got 6, want 4
--- FAIL: TestScale (0.00s)
    --- FAIL: TestScale/double (0.00s)
=== RUN   TestTotal
--- FAIL: TestTotal (0.00s)
FAIL
exit status 1
FAIL	example.com/recipes	0.004s
`

// goTestSubtestFirst is the same run with the subtest's line reaching the
// stream first — the ordering `go test` uses when the parent's summary is
// flushed after its children. The rule does not change: first line, verbatim
// name, subtest path and all.
const goTestSubtestFirst = `=== RUN   TestScale/double
    --- FAIL: TestScale/double (0.00s)
--- FAIL: TestScale (0.00s)
FAIL
`

// goTestNoSummary is a passing package. Nothing failed; nothing is named.
const goTestNoSummary = `ok  	example.com/recipes	0.004s
`

// goBuildFailure has no `--- FAIL:` line at all — the package never built, so
// no test ran and no test can be blamed.
const goBuildFailure = `# example.com/recipes
./scale.go:7:9: undefined: factr
FAIL	example.com/recipes [build failed]
`

func TestGoFirstFailure(t *testing.T) {
	p, ok := ByName("go")
	if !ok {
		t.Fatal("go plugin not registered")
	}
	fp, ok := p.(FailureParser)
	if !ok {
		t.Fatal("go plugin does not implement FailureParser")
	}
	for _, tc := range []struct {
		name   string
		output string
		want   string
	}{
		{"first FAIL line wins", goTestParentAndSubtest, "TestScale"},
		{"a subtest id is returned verbatim", goTestSubtestFirst, "TestScale/double"},
		{"a passing package names nothing", goTestNoSummary, ""},
		{"a build failure names nothing", goBuildFailure, ""},
		{"empty output names nothing", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := fp.FirstFailure([]byte(tc.output)); got != tc.want {
				t.Errorf("FirstFailure = %q, want %q", got, tc.want)
			}
		})
	}
}

// phpunitFailureSummary is a real PHPUnit tail: the dot-progress line, the
// numbered failure section, then the "FAILURES!" summary. Each failing test
// is reported as "N) Class::method" — the id must come back verbatim,
// without the "N) " numbering.
const phpunitFailureSummary = `PHPUnit 10.5.16 by Sebastian Bergmann and contributors.

Runtime:       PHP 8.2.10

..F.                                                               4 / 4 (100%)

Time: 00:00.014, Memory: 6.00 MB

There was 1 failure:

1) Tests\InvoiceTest::testPriceIsNeverNegative
Failed asserting that -5.0 is greater than or equal to 0.

/repo/tests/InvoiceTest.php:22

FAILURES!
Tests: 4, Assertions: 4, Failures: 1.
`

// phpunitFatalError is the "Error:" shape: an exception thrown during a
// test (not a collection failure — the test DID run), reported under "There
// was 1 error:" with the same numbered "N) Class::method" id.
const phpunitFatalError = `PHPUnit 10.5.16 by Sebastian Bergmann and contributors.

Runtime:       PHP 8.2.10

.E                                                                 2 / 2 (100%)

Time: 00:00.011, Memory: 6.00 MB

There was 1 error:

1) Tests\InvoiceTest::testDescribeUnknownKind
Error: Call to undefined method Invoice::describeUnknownKind()

/repo/tests/InvoiceTest.php:40

ERRORS!
Tests: 2, Assertions: 1, Errors: 1.
`

// phpunitCleanPass is a fully passing run — PHPUnit's terse "OK" summary,
// with no numbered section at all.
const phpunitCleanPass = `PHPUnit 10.5.16 by Sebastian Bergmann and contributors.

Runtime:       PHP 8.2.10

....                                                               4 / 4 (100%)

Time: 00:00.010, Memory: 6.00 MB

OK (4 tests, 4 assertions)
`

func TestPHPFirstFailure(t *testing.T) {
	p, ok := ByName("php")
	if !ok {
		t.Fatal("php plugin not registered")
	}
	fp, ok := p.(FailureParser)
	if !ok {
		t.Fatal("php plugin does not implement FailureParser")
	}
	for _, tc := range []struct {
		name   string
		output string
		want   string
	}{
		{"FAILURES! summary names the failing test", phpunitFailureSummary, `Tests\InvoiceTest::testPriceIsNeverNegative`},
		{"a fatal Error: is named the same way", phpunitFatalError, `Tests\InvoiceTest::testDescribeUnknownKind`},
		{"a clean OK run names nothing", phpunitCleanPass, ""},
		{"empty output names nothing", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := fp.FirstFailure([]byte(tc.output)); got != tc.want {
				t.Errorf("FirstFailure = %q, want %q", got, tc.want)
			}
		})
	}
}

// The languages corral can audit but whose runners it cannot parse precisely
// must offer NO parser at all. A wrong id is worse than none: the ledger
// would name a test that never ran.
func TestLanguagesWithoutAFailureParserOfferNone(t *testing.T) {
	for _, name := range []string{"ruby", "javascript", "typescript"} {
		p, ok := ByName(name)
		if !ok {
			continue
		}
		if _, ok := p.(FailureParser); ok {
			t.Errorf("%s implements FailureParser — see FailureParser's doc: an unproven parser must not exist", name)
		}
	}
}

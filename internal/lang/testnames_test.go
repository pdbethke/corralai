// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"reflect"
	"testing"
)

// Go: exactly the functions `go test` would run. TestMain, a helper that only
// starts with "Test" but is followed by a lower-case letter, a method, and a
// function with the wrong signature are not tests.
func TestGoTestNamesInSource(t *testing.T) {
	g, _ := ByName("go")
	src := `package p

import "testing"

func TestMain(m *testing.M) {}
func TestAdd(t *testing.T) {}
func Testify(t *testing.T) {}
func Test_underscore(t *testing.T) {}
func TestHelper(x int) {}
func (s suite) TestMethod(t *testing.T) {}
func BenchmarkAdd(b *testing.B) {}
func TestSub(tt *testing.T) {}
`
	got := g.TestNamesInSource("p/p_test.go", src)
	if want := []string{"TestAdd", "Test_underscore", "TestSub"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got := g.TestNamesInSource("p/p_test.go", "not go"); got != nil {
		t.Fatalf("unparseable source lists nothing, got %v", got)
	}
}

// Python: pytest node ids, the same form ParseTestList returns from
// --collect-only, so a name the critic is handed is a runnable selector.
func TestPythonTestNamesInSource(t *testing.T) {
	py, _ := ByName("python")
	src := `import pytest

def helper():
    pass

def test_top():
    assert 1

async def test_async():
    assert 1

class TestThing:
    def setup_method(self):
        pass

    def test_method(self):
        assert 1

    def helper(self):
        pass

class NotATest:
    def test_ignored(self):
        pass

def test_after_class():
    assert 1
`
	got := py.TestNamesInSource("tests/test_x.py", src)
	want := []string{"tests/test_x.py::test_top", "tests/test_x.py::test_async", "tests/test_x.py::TestThing::test_method", "tests/test_x.py::test_after_class"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// A language with no static lister says so with nil, which callers read as
// "no list", never as "no tests".
func TestOtherLanguagesListNothing(t *testing.T) {
	for _, name := range []string{"ruby", "javascript", "typescript", "php"} {
		p, ok := ByName(name)
		if !ok {
			t.Fatalf("no plugin %q", name)
		}
		if got := p.TestNamesInSource("x", "anything"); got != nil {
			t.Errorf("%s: got %v, want nil", name, got)
		}
	}
}

// The review's cases: a class whose bases span lines, a method that follows a
// docstring with a column-0 line, a test-looking def inside a module string,
// and a test defined twice (pytest collects it once). The scanner must list
// exactly what pytest would collect, because the critic is held to the list.
func TestPythonTestNamesInSourceSurvivesStringsAndContinuations(t *testing.T) {
	py, _ := ByName("python")
	src := "import pytest\n" +
		"\n" +
		"SAMPLE = \"\"\"\n" +
		"def test_ghost():\n" +
		"    pass\n" +
		"\"\"\"\n" +
		"\n" +
		"class TestMulti(\n" +
		"    object,\n" +
		"):\n" +
		"    def test_one(self):\n" +
		"        assert 1\n" +
		"\n" +
		"class TestDoc:\n" +
		"    def test_a(self):\n" +
		"        '''\n" +
		"text at column zero\n" +
		"        '''\n" +
		"\n" +
		"    def test_b(self):\n" +
		"        assert 1\n" +
		"\n" +
		"@pytest.mark.slow\n" +
		"def test_dup():\n" +
		"    assert 1\n" +
		"\n" +
		"def test_dup():\n" +
		"    assert 2\n"
	got := py.TestNamesInSource("t.py", src)
	want := []string{"t.py::TestMulti::test_one", "t.py::TestDoc::test_a", "t.py::TestDoc::test_b", "t.py::test_dup"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}

// Go: a test file that imports testing under another name still declares
// tests, and the go tool runs them.
func TestGoTestNamesInSourceHonoursATestingAlias(t *testing.T) {
	g, _ := ByName("go")
	src := "package p\n\nimport tt \"testing\"\n\nfunc TestAlias(t *tt.T) {}\nfunc TestNot(t *testing.T) {}\n"
	if got := g.TestNamesInSource("p_test.go", src); !reflect.DeepEqual(got, []string{"TestAlias"}) {
		t.Fatalf("got %v, want [TestAlias]", got)
	}
}

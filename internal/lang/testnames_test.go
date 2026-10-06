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

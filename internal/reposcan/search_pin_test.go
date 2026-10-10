// SPDX-License-Identifier: Elastic-2.0
package reposcan

import (
	"slices"
	"testing"
)

// These characterization tests pin the stage-2 search helpers exactly as they
// behaved before Round B1b moved them. The corpus golden only has four search
// hits, so it cannot see an affix or a tie-break; these can.

func TestNormalizeSegPin(t *testing.T) {
	cases := map[string]string{
		"test_foo":      "foo",
		"spec_foo":      "foo",
		"foo_test":      "foo",
		"foo_spec":      "foo",
		"test_":         "test_",
		"spec_":         "spec_",
		"_test":         "_test",
		"_spec":         "_spec",
		"spec":          "spec",
		"tests":         "tests",
		"foo":           "foo",
		"test_test_foo": "test_foo",
		"test_foo_spec": "foo_spec",
	}
	for in, want := range cases {
		if got := normalizeSeg(in); got != want {
			t.Errorf("normalizeSeg(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBestSearchMatchPin(t *testing.T) {
	roots := []string{"tests"}
	names := map[string]bool{"t.py": true}
	cases := []struct {
		name     string
		code     string
		universe []string
		want     string
		found    bool
	}{
		{"higher score beats lower even when deeper", "a/b/c/m.py",
			[]string{"tests/c/t.py", "tests/q/b/c/t.py"}, "tests/q/b/c/t.py", true},
		{"equal score, shallower wins", "pkg/sub/m.py",
			[]string{"tests/z/pkg/sub/t.py", "tests/pkg/sub/t.py"}, "tests/pkg/sub/t.py", true},
		{"equal score and depth, lexicographically smaller wins", "sub/m.py",
			[]string{"tests/b/sub/t.py", "tests/a/sub/t.py"}, "tests/a/sub/t.py", true},
		{"zero directory correlation is refused", "pkg/m.py",
			[]string{"tests/other/t.py", "tests/t.py"}, "", false},
		{"first segment not a root is refused", "pkg/m.py",
			[]string{"other/pkg/t.py"}, "", false},
		{"basename not in the set is refused", "pkg/m.py",
			[]string{"tests/pkg/u.py"}, "", false},
		{"itsdangerous shape pairs through normalizeSeg", "src/itsdangerous/signer.py",
			[]string{"tests/test_itsdangerous/t.py"}, "tests/test_itsdangerous/t.py", true},
	}
	for _, c := range cases {
		for _, order := range []string{"forward", "reversed"} {
			u := slices.Clone(c.universe)
			if order == "reversed" {
				slices.Reverse(u)
			}
			got, ok := bestSearchMatch(c.code, roots, names, u)
			if got != c.want || ok != c.found {
				t.Errorf("%s (%s): got (%q,%v), want (%q,%v)", c.name, order, got, ok, c.want, c.found)
			}
		}
	}
}

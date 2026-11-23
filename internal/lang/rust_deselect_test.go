// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// cargo test salvages with libtest's own skip: `--exact --skip <path>` per
// failing test, among the test binaries' arguments. --exact matters: libtest's
// --skip matches a substring, so `--skip tests::a` alone also skips every test
// whose path merely contains it (measured on cargo 1.90.0). A command whose
// own test-binary arguments carry a name filter, a skip, --ignored or --list
// is refused, as its killer is (cargoTestSplit): --exact would change what
// the operator's filter matches. A nextest id (`<binary> <test>`, printed by
// a wrapper running nextest) is refused.
func TestCargoTestDeselectSkipsExactlyTheFailingTests(t *testing.T) {
	ids := []string{"tests::a_fails", "tests::b_fails"}
	for _, c := range []struct {
		argv []string
		ids  []string
		want string // "" = refused
	}{
		{[]string{"cargo", "test"}, ids, "cargo test -- --exact --skip tests::a_fails --skip tests::b_fails"},
		{[]string{"cargo", "test", "--workspace", "--", "--nocapture"}, ids, "cargo test --workspace -- --nocapture --exact --skip tests::a_fails --skip tests::b_fails"},
		{[]string{"cargo", "test", "--", "--exact"}, ids, "cargo test -- --exact --skip tests::a_fails --skip tests::b_fails"},
		{[]string{"cargo", "test", "parses"}, ids, ""},
		{[]string{"cargo", "test", "--", "parses"}, ids, ""},
		{[]string{"cargo", "test", "--", "--skip", "slow"}, ids, ""},
		{[]string{"cargo", "test"}, []string{"fail-cargo tests::a_fails"}, ""},
		{[]string{"cargo", "test"}, nil, ""},
	} {
		got, ok := cargoDeselectCmd(c.argv, c.ids)
		if c.want == "" {
			if ok {
				t.Errorf("%q %q: got %q, want refused", c.argv, c.ids, got)
			}
			continue
		}
		if !ok || strings.Join(got, " ") != c.want {
			t.Errorf("%q %q: got %q ok=%v, want %q", c.argv, c.ids, got, ok, c.want)
		}
	}
}

// nextest salvages with one filterset that excludes exactly the failing
// tests, each by binary and name: nextest names a failure `<binary id>
// <test>`, so a test of the same name in another binary still runs. nextest
// unions the filtersets it is given, so an exclusion added beside the
// command's own would add their tests back; the command's `-E <expr>`
// filtersets, the form its unit command writes (nextestUnitsCmd), are folded
// into the one filterset instead, `((e1) | (e2)) & not (…)`, which runs what
// they ran less the failing tests. Any other spelling of a filterset is
// refused, as nextestRunSplit refuses it. An operator's name filter still
// applies, since nextest intersects it with the filterset. An id with no
// binary, or a part that would end the filterset's matcher early, is refused.
func TestNextestDeselectExcludesExactlyTheFailingTests(t *testing.T) {
	ids := []string{"fail-cargo tests::a_fails", "fail-cargo tests::b_fails"}
	not := "not ((binary_id(=fail-cargo) & test(=tests::a_fails)) | (binary_id(=fail-cargo) & test(=tests::b_fails)))"
	for _, c := range []struct {
		argv []string
		ids  []string
		want []string // nil = refused
	}{
		{[]string{"cargo", "nextest", "run"}, ids, []string{"cargo", "nextest", "run", "-E", not}},
		{[]string{"cargo", "nextest", "run", "--workspace", "--", "tests::"}, ids, []string{"cargo", "nextest", "run", "--workspace", "-E", not, "--", "tests::"}},
		{[]string{"cargo", "nextest", "run", "-E", "binary_id(=fail-cargo)"}, ids, []string{"cargo", "nextest", "run", "-E", "((binary_id(=fail-cargo))) & " + not}},
		{[]string{"cargo", "nextest", "run", "-E", "binary_id(=a)", "-E", "binary_id(=b)", "--", "x"}, ids, []string{"cargo", "nextest", "run", "-E", "((binary_id(=a)) | (binary_id(=b))) & " + not, "--", "x"}},
		{[]string{"cargo", "nextest", "run", "--filterset", "test(parses)"}, ids, nil},
		{[]string{"cargo", "nextest", "run", "-Etest(parses)"}, ids, nil},
		{[]string{"cargo", "nextest", "run", "-E"}, ids, nil},
		{[]string{"cargo", "nextest", "run"}, []string{"tests::a_fails"}, nil},
		{[]string{"cargo", "nextest", "run"}, []string{"fail-cargo tests::a)"}, nil},
		{[]string{"cargo", "nextest", "run"}, nil, nil},
	} {
		got, ok := nextestDeselectCmd(c.argv, c.ids)
		if c.want == nil {
			if ok {
				t.Errorf("%q %q: got %q, want refused", c.argv, c.ids, got)
			}
			continue
		}
		if !ok || !slices.Equal(got, c.want) {
			t.Errorf("%q %q: got %q ok=%v, want %q", c.argv, c.ids, got, ok, c.want)
		}
	}
}

// The capture script runs each Rust deselect command against the real runner
// on the fixture whose failures were captured (fail/cargo: two failing tests
// and one passing), and it must run the passing test alone, and pass
// (skipok). Held equal here: the ids are read from the captured failing runs
// by the row's own reader, and the script's commands are what the row's
// DeselectCmd builds from them, so the script checks the command corral runs.
func TestRustDeselectCapturesAreTheRowsCommands(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "scripts", "capture-runner-fixtures.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(b)
	for _, c := range []struct {
		name, fixture string
		argv          []string
	}{
		{"cargo-test", "cargo-test-fail.txt", []string{"cargo", "test"}},
		{"cargo-nextest", "cargo-nextest-fail.txt", []string{"cargo", "nextest", "run"}},
	} {
		p, _ := ByName("rust")
		inv := Cmd(c.argv)
		failed, ok := DeselectorFor(p, inv)
		if !ok {
			t.Fatalf("%s: the row does not salvage", c.name)
		}
		ids := failed(readFixture(t, c.fixture))
		slices.Sort(ids) // nextest runs in parallel: the set is what counts
		cmd, ok := DeselectCmd(p, inv, ids)
		if !ok {
			t.Fatalf("%s: no deselect command for %q", c.name, ids)
		}
		want := "skipok " + c.name + " env -C fail/cargo " + shellQuoteArgs(cmd)
		if !slices.Contains(strings.Split(script, "\n"), want) {
			t.Errorf("%s: the capture script has no line\n%s", c.name, want)
		}
	}
	// Salvage from a unit command: the run's unit command (UnitsCmd), less
	// one failing test of that unit, on the units/cargo workspace.
	for _, c := range []struct {
		name, unit, id string
		argv           []string
	}{
		{"nextest-deselect-from-unit", "units-a", "units-a tests::adds", []string{"cargo", "nextest", "run", "--workspace"}},
		{"cargo-deselect-from-unit", "units-a/lib", "tests::adds", []string{"cargo", "test", "--workspace"}},
	} {
		p, _ := ByName("rust")
		r, ok := RunnerFor(p, Cmd(c.argv))
		if !ok || r.UnitsCmd == nil {
			t.Fatalf("%s: no unit row for %q", c.name, c.argv)
		}
		unit, ok := r.UnitsCmd(c.argv, []string{c.unit})
		if !ok {
			t.Fatalf("%s: no unit command for %q", c.name, c.unit)
		}
		cmd, ok := DeselectCmd(p, Cmd(unit), []string{c.id})
		if !ok {
			t.Fatalf("%s: no deselect command from %q", c.name, unit)
		}
		want := "want " + c.name + " 1 \"$(cd units/cargo && " + shellQuoteArgs(cmd) + " 2>&1 | "
		if !strings.Contains(script, want) {
			t.Errorf("%s: the capture script has no line starting\n%s", c.name, want)
		}
	}
}

// shellQuoteArgs is argv as the capture script writes it: a word with a
// character the shell would read is single-quoted.
func shellQuoteArgs(argv []string) string {
	out := make([]string, len(argv))
	for i, a := range argv {
		if strings.ContainsAny(a, " ()|&=<>*?$'\"\\;") {
			a = "'" + a + "'"
		}
		out[i] = a
	}
	return strings.Join(out, " ")
}

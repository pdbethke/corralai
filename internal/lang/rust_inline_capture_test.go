// SPDX-License-Identifier: Elastic-2.0

package lang_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/pairing"
)

// inlineFixture is the capture script's inline crate and the files around it
// (internal/lang/testdata/runners/src/inline).
const inlineFixture = "testdata/runners/src/inline"

func readInline(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(inlineFixture, filepath.FromSlash(name))) // #nosec G304 -- test fixture under testdata
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func captureScript(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "scripts", "capture-runner-fixtures.sh"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(string(b), "\n")
}

// inlineLanding is the landing of an authored test for src (a file of the
// fixture crate's src/): the source itself, depth 0, as pairing's Inline
// rule (and Primary, for a file with no test module) lands it.
func inlineLanding(src string) pairing.Landing {
	return pairing.Landing{Path: "src/" + src, Shape: pairing.Inline, Into: true, Depth: 0, DeclarationsRead: true}
}

// inlineBlock is the block a grading run appends to src: the authored body
// (inline/authored/<src>) landed by the TestLander, or, for the positive
// control, the reach canary landed the same way.
func inlineBlock(t *testing.T, src string, canary bool) string {
	t.Helper()
	l := inlineLanding(src)
	target := readInline(t, "cargo/src/"+src)
	if canary {
		b, err := lang.ReachCanaryBlock(l, target)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	return lang.LandBody(l, target, readInline(t, "authored/"+src))
}

// The capture script runs the real cargo test and cargo nextest on the files
// in inline/composed, each a source of the fixture crate with a block
// appended, as a grading run writes it (adequacy.Overlay with
// AppendBlock): the original, a mutant, or the positive control's reach
// canary in place of the block. Held equal here, so a change to LandTest,
// the canary or the append rule fails until the script has run what corral
// now writes; and the script's `inline` lines put each file in the crate
// copy its commands run in.
func TestRustInlineCapturesAreWhatCorralComposes(t *testing.T) {
	script := captureScript(t)
	for _, c := range []struct {
		dir, src, code, composed string
		canary                   bool
	}{
		{"lib-pass", "lib.rs", "cargo/src/lib.rs", "lib.rs", false},
		{"lib-kill", "lib.rs", "mutants/lib.rs", "lib.mutant.rs", false},
		{"lib-canary", "lib.rs", "cargo/src/lib.rs", "lib.canary.rs", true},
		{"extra-pass", "extra.rs", "cargo/src/extra.rs", "extra.rs", false},
		{"extra-kill", "extra.rs", "mutants/extra.rs", "extra.mutant.rs", false},
		{"extra-canary", "extra.rs", "cargo/src/extra.rs", "extra.canary.rs", true},
		{"named-pass", "named.rs", "cargo/src/named.rs", "named.rs", false},
	} {
		path := "src/" + c.src
		ov := adequacy.Overlay{CodePath: path, Compose: adequacy.AppendBlock(inlineBlock(t, c.src, c.canary))}
		if got, want := readInline(t, "composed/"+c.composed), ov.With(readInline(t, c.code))[path]; got != want {
			t.Errorf("composed/%s is not what corral writes at %s:\n--- file\n%s\n--- corral\n%s", c.composed, path, got, want)
		}
		if line := "inline " + c.dir + " " + c.src + " composed/" + c.composed; !slices.Contains(script, line) {
			t.Errorf("the capture script has no line\n%s", line)
		}
	}
	// The mutant runs without the block: the file's own tests miss it.
	for _, src := range []string{"lib.rs", "extra.rs"} {
		dir := strings.TrimSuffix(src, ".rs") + "-dev"
		if line := "inline " + dir + " " + src + " mutants/" + src; !slices.Contains(script, line) {
			t.Errorf("the capture script has no line\n%s", line)
		}
	}
	// A source that already declares corral_tests takes corral_tests_2.
	if b := inlineBlock(t, "named.rs", false); !strings.HasPrefix(b, "#[cfg(test)]\nmod corral_tests_2 {\n") {
		t.Errorf("the block landed in named.rs is not corral_tests_2:\n%s", b)
	}
}

// The authored-alone run of an inline block is the unit command narrowed by
// the row's SelectCmd to the block's ids and the reach canary's, each from
// InlineTestIDs, as advpool's inlineAlone builds it. The script runs that
// command on the original (the block's tests alone run, and pass), on the
// mutant (one kills) and with the canary in place (the canary alone runs,
// and fails), for the crate root, a module and a corral_tests_2 block; this
// holds each line's command equal to what the row builds.
func TestRustInlineSelectCapturesAreTheRowsCommands(t *testing.T) {
	script := captureScript(t)
	p, _ := lang.ByName("rust")
	root := filepath.Join(inlineFixture, "cargo")
	for _, row := range []struct {
		name string
		argv []string
	}{
		{"cargo-test", []string{"cargo", "test"}},
		{"nextest", []string{"cargo", "nextest", "run"}},
	} {
		r, ok := lang.RunnerFor(p, lang.Cmd(row.argv))
		if !ok || r.SelectCmd == nil || r.UnitsCmd == nil || r.UnitForFile == nil {
			t.Fatalf("%s: the row has no unit or select command", row.name)
		}
		for _, c := range []struct {
			src  string
			runs map[string]string // want name -> crate copy it runs in
		}{
			{"lib.rs", map[string]string{"select-lib": "lib-pass", "select-kills-lib": "lib-kill", "select-canary-lib": "lib-canary"}},
			{"extra.rs", map[string]string{"select-extra": "extra-pass", "select-kills-extra": "extra-kill", "select-canary-extra": "extra-canary"}},
			{"named.rs", map[string]string{"select-named": "named-pass"}},
		} {
			path := "src/" + c.src
			unit, ok := r.UnitForFile(root, path)
			if !ok {
				t.Fatalf("%s %s: no unit", row.name, path)
			}
			alone, ok := r.UnitsCmd(row.argv, []string{unit})
			if !ok {
				t.Fatalf("%s %s: no unit command for %q", row.name, path, unit)
			}
			var ids []string
			for _, canary := range []bool{false, true} {
				got, err := lang.InlineTestIDs(root, path, inlineBlock(t, c.src, canary))
				if err != nil {
					t.Fatal(err)
				}
				ids = append(ids, got...)
			}
			sel, ok := r.SelectCmd(alone, ids)
			if !ok {
				t.Fatalf("%s %s: no select command from %q for %q", row.name, path, alone, ids)
			}
			for name, dir := range c.runs {
				want := "want inline-" + row.name + "-" + name + " "
				tail := "\"$(cd inline/" + dir + " && " + lang.ShellQuoteArgs(sel) + " 2>&1 | pf)\""
				i := slices.IndexFunc(script, func(l string) bool { return strings.HasPrefix(l, want) })
				if i < 0 || !strings.HasSuffix(script[i], tail) {
					t.Errorf("%s: the capture script has no line\n%s… %s", name, want, tail)
				}
			}
		}
	}
}

// A salvage drops an inline block's failing tests from the select list by
// the id the runner printed for each (advpool's inlineAlone), so those ids
// must be exactly the ids InlineTestIDs builds: one that differs drops
// nothing, silently. These are the real runners' failing runs of the
// composed mutants, for the crate root (src/lib.rs, ids with no module path)
// and a module (src/extra.rs), read by each row's own failure reader.
func TestRustInlineFailingIDsAreInlineTestIDs(t *testing.T) {
	p, _ := lang.ByName("rust")
	root := filepath.Join(inlineFixture, "cargo")
	for _, c := range []struct {
		fixture, src, killer string
		argv                 []string
	}{
		{"inline-cargo-test-kill-lib.txt", "lib.rs", "corral_tests::zero_is_zero", []string{"cargo", "test"}},
		{"inline-nextest-kill-lib.txt", "lib.rs", "corral_tests::zero_is_zero", []string{"cargo", "nextest", "run"}},
		{"inline-cargo-test-kill-extra.txt", "extra.rs", "extra::corral_tests::above", []string{"cargo", "test"}},
		{"inline-nextest-kill-extra.txt", "extra.rs", "extra::corral_tests::above", []string{"cargo", "nextest", "run"}},
	} {
		r, ok := lang.RunnerFor(p, lang.Cmd(c.argv))
		if !ok || r.FailedTests == nil {
			t.Fatalf("%s: the row reads no failures", c.fixture)
		}
		b, err := os.ReadFile(filepath.Join("testdata", "runners", c.fixture)) // #nosec G304 -- test fixture under testdata
		if err != nil {
			t.Fatal(err)
		}
		var failed []string
		for _, id := range r.FailedTests(string(b)) {
			if i := strings.LastIndexByte(id, ' '); i >= 0 {
				id = id[i+1:] // nextest's `<binary> <path>`: the path, as inlineAlone reads it
			}
			if !slices.Contains(failed, id) {
				failed = append(failed, id)
			}
		}
		ids, err := lang.InlineTestIDs(root, "src/"+c.src, inlineBlock(t, c.src, false))
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(failed, []string{c.killer}) || !slices.Contains(ids, c.killer) {
			t.Errorf("%s: the runner named %q failing; InlineTestIDs gives %q, want exactly %q among them", c.fixture, failed, ids, c.killer)
		}
	}
}

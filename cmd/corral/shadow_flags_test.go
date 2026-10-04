// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// shadowFlagLiteral matches a hand-registered shadow-seat flag. Every
// shadow-seat flag must come from registerShadowSeatFlags, so a command
// cannot gain --shadow-model without --shadow-pool. Derived from the source,
// not a list of commands: a fourth door is found the day it is written.
var shadowFlagLiteral = regexp.MustCompile(`\.String(Var)?\([^)]*"shadow-(writer-)?(model|pool)"`)

func TestShadowFlagLiteralMatchesARegistration(t *testing.T) {
	// Negative control: the pattern must catch the shape it exists to catch.
	if !shadowFlagLiteral.MatchString(`x := fs.String("shadow-model", "", "h")`) {
		t.Fatal("pattern no longer matches a hand registration; the door test below would pass vacuously")
	}
}

func TestShadowSeatFlagsComeOnlyFromTheHelper(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("glob: %v (%d files)", err, len(files))
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "shadow_flags.go" {
			continue
		}
		b, err := os.ReadFile(f) // #nosec G304 -- this package's own sources
		if err != nil {
			t.Fatal(err)
		}
		if loc := shadowFlagLiteral.FindIndex(b); loc != nil {
			t.Errorf("%s registers a shadow-seat flag by hand (%q); use registerShadowSeatFlags so its pool flag comes with it", f, b[loc[0]:loc[1]])
		}
	}
}

// doctor had no --shadow-writer-model at all, so it could not check the
// challenger writer's credential. The derived test above cannot see a door
// that registers NOTHING, so the three known doors are pinned too.
func TestKnownDoorsUseTheHelper(t *testing.T) {
	for _, f := range []string{"certify_local.go", "certify_repo.go", "doctor.go"} {
		b, err := os.ReadFile(f) // #nosec G304 -- this package's own sources
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "registerShadowSeatFlags(") {
			t.Errorf("%s does not call registerShadowSeatFlags", f)
		}
	}
}

// flagUsage returns one flag's usage text as a door prints it under -h — the
// text the generated CLI reference is built from.
func flagUsage(t *testing.T, run func([]string, io.Writer, io.Writer) int, name string) string {
	t.Helper()
	var out, errb bytes.Buffer
	run([]string{"-h"}, &out, &errb)
	text := out.String() + errb.String()
	i := strings.Index(text, "\n  -"+name+" ")
	if i < 0 {
		t.Fatalf("-h output has no -%s:\n%s", name, text)
	}
	rest := text[i+1:]
	if j := strings.Index(rest[1:], "\n  -"); j >= 0 {
		rest = rest[:j+1]
	}
	return rest
}

// Each door's help says what THAT door does with a pool. The shared text
// claimed a per-language history under certify --repo (which pools every
// language) and a draw under doctor (which only validates), and doctor's
// --shadow-writer-model help described authoring a suite, which doctor never
// does — it checks the credential.
func TestShadowFlagHelpIsTrueAtEachDoor(t *testing.T) {
	type claim struct {
		flag        string
		must, never []string
	}
	doors := []struct {
		name   string
		run    func([]string, io.Writer, io.Writer) int
		claims []claim
	}{
		{"certify --local", runCertifyLocal, []claim{
			{"shadow-pool", []string{"language"}, nil},
		}},
		{"certify --repo", runCertifyRepo, []claim{
			{"shadow-pool", []string{"ONCE", "across every recorded language", "--dry-run"}, []string{"for this language"}},
			{"shadow-writer-pool", []string{"once per scan"}, nil},
		}},
		{"doctor", runDoctor, []claim{
			{"shadow-pool", []string{"draws nothing", "credential"}, []string{"each run DRAWS", "Thompson"}},
			{"shadow-writer-pool", []string{"draws nothing"}, []string{"drawn per run"}},
			{"shadow-writer-model", []string{"credential"}, []string{"authors a second suite"}},
			{"shadow-seed", []string{"draws nothing"}, nil},
		}},
	}
	for _, d := range doors {
		for _, c := range d.claims {
			help := flagUsage(t, d.run, c.flag)
			for _, w := range c.must {
				if !strings.Contains(help, w) {
					t.Errorf("%s -%s help lacks %q:\n%s", d.name, c.flag, w, help)
				}
			}
			for _, w := range c.never {
				if strings.Contains(help, w) {
					t.Errorf("%s -%s help says %q, which is false at this door:\n%s", d.name, c.flag, w, help)
				}
			}
		}
	}
}

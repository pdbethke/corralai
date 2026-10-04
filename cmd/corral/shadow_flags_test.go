// SPDX-License-Identifier: Elastic-2.0

package main

import (
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

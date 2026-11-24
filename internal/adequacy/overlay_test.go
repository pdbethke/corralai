// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestOverlayWritesCodeAtItsPathAndLeavesBaseAlone(t *testing.T) {
	base := map[string]string{"a.go": "A", "b.go": "B"}
	o := Overlay{Base: base, CodePath: "a.go"}

	if got, want := o.With("X"), map[string]string{"a.go": "X", "b.go": "B"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("With = %v, want %v", got, want)
	}
	if want := map[string]string{"a.go": "A", "b.go": "B"}; !reflect.DeepEqual(base, want) {
		t.Fatalf("Base was modified: %v", base)
	}

	o.Compose = func(c string) string { return c + "\n//t" }
	if got := o.With("X")["a.go"]; got != "X\n//t" {
		t.Fatalf("composed file = %q, want %q", got, "X\n//t")
	}
}

// TestNoWorkspaceMapIsAssembledOutsideOverlay is derived from the tree: any
// non-test .go file under internal/ and cmd/ that writes code into a map at
// the audited path by hand has bypassed Overlay, and a rule added to
// Overlay.Compose would then miss that run.
func TestNoWorkspaceMapIsAssembledOutsideOverlay(t *testing.T) {
	pat := regexp.MustCompile(`\[([A-Za-z_]+\.)*[cC]odePath\] = `)
	// The one exemption: mutantset.go's r.files is a record of
	// MutantSetEntry values keyed by path, not a workspace of file contents.
	exempt := filepath.Join("cmd", "corral", "mutantset.go")
	seen := 0
	for _, root := range []string{"../../internal", "../../cmd"} {
		err := filepath.WalkDir(root, func(f string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(f, ".go") || strings.HasSuffix(f, "_test.go") {
				return err
			}
			seen++
			if filepath.Base(f) == "overlay.go" || strings.HasSuffix(f, exempt) {
				return nil
			}
			b, err := os.ReadFile(f)
			if err != nil {
				return err
			}
			for i, line := range strings.Split(string(b), "\n") {
				if pat.MatchString(line) {
					t.Errorf("%s:%d assembles a workspace by hand, use Overlay: %s", f, i+1, strings.TrimSpace(line))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if seen < 50 {
		t.Fatalf("walked only %d files; the tree walk is not finding the repo", seen)
	}
}

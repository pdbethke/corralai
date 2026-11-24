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
// non-test file under the packages that build run workspaces that writes the
// audited code into a map by hand has bypassed Overlay, and a rule added to
// Overlay.Compose would then miss that run.
func TestNoWorkspaceMapIsAssembledOutsideOverlay(t *testing.T) {
	pat := regexp.MustCompile(`\[(rs\.|cfg\.)?[cC]odePath\] = `)
	for _, dir := range []string{".", "../advpool", "../authoring"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil || len(files) == 0 {
			t.Fatalf("no files under %s: %v", dir, err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") || filepath.Base(f) == "overlay.go" {
				continue
			}
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			for i, line := range strings.Split(string(b), "\n") {
				if pat.MatchString(line) {
					t.Errorf("%s:%d assembles a workspace by hand, use Overlay: %s", f, i+1, strings.TrimSpace(line))
				}
			}
		}
	}
}

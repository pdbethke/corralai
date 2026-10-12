// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/pairing"
)

// TestCheckTestLandingRefusesOnlyWhereTheTestWouldOverwriteTheSource derives
// both halves from the registry: every corpus source of every registered
// plugin is refused exactly when Primary names the source itself, so a
// language that has a real test file (every pre-Rust one) is untouched
// and Rust, whose Primary is its Inline candidate, is refused — with a
// message that says why, not merely that.
func TestCheckTestLandingRefusesOnlyWhereTheTestWouldOverwriteTheSource(t *testing.T) {
	refused := map[string]int{}
	for _, name := range Names() {
		p := registry[name]
		ext, ok := sourceExt(p.TestRules())
		if !ok {
			t.Fatalf("%s: no source extension", name)
		}
		for _, dir := range corpusDirs {
			for _, base := range corpusBases {
				src := filepath.ToSlash(filepath.Join(dir, base+ext))
				err := CheckTestLanding(src)
				primary, _ := pairing.Primary(p.TestRules(), src)
				if (err != nil) != (primary.Path == src) {
					t.Errorf("%s %s: refused=%v but Primary=%q", name, src, err != nil, primary.Path)
				}
				if err != nil {
					refused[name]++
					if !strings.Contains(err.Error(), "not built yet") || !strings.Contains(err.Error(), src) {
						t.Errorf("%s: refusal %q does not say the landing is not built", src, err)
					}
				}
			}
		}
	}
	if refused["rust"] == 0 {
		t.Error("no Rust source was refused — the property never fired")
	}
	if err := CheckTestLanding("src/flask/cli.py"); err != nil {
		t.Errorf("a Python source is refused: %v", err)
	}
	if err := CheckTestLanding("README"); err != nil {
		t.Errorf("an unclaimed file is refused: %v", err)
	}
}

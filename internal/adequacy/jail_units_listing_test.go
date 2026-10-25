// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
)

// The jail binds the unit LISTING's toolchain exactly as it binds the
// operator's own command's. The listing runs under a `sh -c` prelude with the
// operator's command as positional parameters; when the jail resolved only
// the prelude's words, a Go outside /usr was never bound and every jailed
// listing died with `exec: go: not found` (gin, Task 11a). The binds are
// compared to the operator's command's, which is what the scan's graded runs
// in the same jail bind.
func TestJailBindsTheUnitListingsToolchain(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go on PATH")
	}
	if real, _ := filepath.EvalSymlinks(goBin); strings.HasPrefix(real, "/usr/") {
		t.Skip("go is under /usr, which every jail mounts; nothing to bind")
	}
	p, _ := lang.ByName("go")
	argv := []string{goBin, "test", "./..."}
	listCmd, ok := lang.ListUnitsCmd(p, lang.Cmd(argv))
	if !ok {
		t.Fatalf("no listing for %q", argv)
	}
	want, err := toolchainBinds(argv)
	if err != nil || len(want) == 0 {
		t.Fatalf("the operator's command binds %+v, %v: expected its toolchain", want, err)
	}
	// The one-unit run is the coverage wrapper around the narrowed command,
	// which names the interpreter shell-quoted inside its script ('go'):
	// unread, it was the next `go: not found` once the listing worked.
	unitCmd, ok := lang.UnitRunCmd(p, lang.Cmd(argv), "example.com/m/pkg")
	if !ok {
		t.Fatalf("no unit form for %q", argv)
	}
	covCmd, ok := p.(lang.CoverageReporter).CoverageCmd(unitCmd)
	if !ok {
		t.Fatalf("no coverage form for %q", unitCmd)
	}
	for name, cmd := range map[string][]string{"listing": listCmd, "unit run": covCmd} {
		got, err := toolchainBinds(cmd)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range want {
			found := false
			for _, g := range got {
				found = found || g.Host == w.Host
			}
			if !found {
				t.Errorf("the %s %q binds %+v: missing the operator's toolchain %s", name, cmd, got, w.Host)
			}
		}
	}
}

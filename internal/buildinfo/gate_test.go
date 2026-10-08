// SPDX-License-Identifier: Elastic-2.0

package buildinfo

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every binary must resolve its version through this package: corral-admin,
// corral-observe and corral-agent printed "dev" for a `go install …@vX` build
// because each kept its own bare `var version = "dev"`.
//
// The gate keys on the PROPERTY (the binary prints or stamps a version), not on
// one spelling of it. Three signals mark a binary as version-bearing, any one
// is enough: it declares a package-level string var initialised to "dev"
// (whatever the var is called), it declares a var named by an `-X main.<name>`
// ldflags target anywhere in the build files, or it has a version flag/verb
// literal. A version-bearing main.go that never calls buildinfo.Version( fails.
var (
	devVarRe   = regexp.MustCompile(`(?m)^var\s+(\w+)(\s+string)?\s*=\s*"dev"`)
	ldflagRe   = regexp.MustCompile(`-X main\.(\w+)=`)
	versionLit = regexp.MustCompile(`"-{0,2}version"`)
)

func ldflagTargets(t *testing.T) map[string]bool {
	t.Helper()
	root := filepath.Join("..", "..")
	files := []string{filepath.Join(root, "Makefile")}
	for _, pat := range []string{".github/workflows/*.yml", "scripts/*.sh"} {
		m, _ := filepath.Glob(filepath.Join(root, pat))
		files = append(files, m...)
	}
	names := map[string]bool{}
	for _, f := range files {
		b, err := os.ReadFile(f) // #nosec G304 -- fixed repo paths
		if err != nil {
			continue
		}
		for _, m := range ldflagRe.FindAllStringSubmatch(string(b), -1) {
			names[m[1]] = true
		}
	}
	// Floor: the census must have found the real stamp, or the gate walks nothing.
	if !names["stampedVersion"] {
		t.Fatalf("ldflags census found %v; expected main.stampedVersion — the gate would be vacuous", names)
	}
	return names
}

func TestEveryBinaryResolvesItsVersionHere(t *testing.T) {
	targets := ldflagTargets(t)
	mains, _ := filepath.Glob(filepath.Join("..", "..", "cmd", "*", "main.go"))
	if len(mains) < 8 {
		t.Fatalf("found only %d cmd/*/main.go; the glob is wrong", len(mains))
	}
	bearing := 0
	for _, m := range mains {
		b, err := os.ReadFile(m) // #nosec G304 -- fixed repo paths
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		reason := ""
		if d := devVarRe.FindStringSubmatch(s); d != nil {
			reason = "declares `var " + d[1] + ` = "dev"`
		} else if versionLit.MatchString(s) {
			reason = "has a version flag or verb"
		} else {
			for n := range targets {
				if regexp.MustCompile(`(?m)^var\s+` + n + `\b`).MatchString(s) {
					reason = "declares ldflags target " + n
				}
			}
		}
		if reason == "" {
			t.Logf("%s prints no version; not covered", m)
			continue
		}
		bearing++
		if !strings.Contains(s, "buildinfo.Version(") {
			t.Errorf("%s %s but does not resolve it through buildinfo.Version", m, reason)
		}
		// A stamped var no ldflags line writes is a build line that silently misses this binary.
		if d := devVarRe.FindStringSubmatch(s); d != nil && !targets[d[1]] {
			t.Errorf("%s stamps var %q but no Makefile/workflow/script passes -X main.%s", m, d[1], d[1])
		}
	}
	if bearing < 5 {
		t.Fatalf("only %d version-bearing binaries found; expected at least 5", bearing)
	}
}

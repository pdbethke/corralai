// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/statedir"
)

// The configuration page states store defaults by hand. Each one it states
// must be the table's, so the page cannot drift from statedir.
func TestDocsConfigurationDefaultsMatchStatedir(t *testing.T) {
	b, err := os.ReadFile("../../site/src/content/docs/docs/configuration.mdx")
	if err != nil {
		t.Fatal(err)
	}
	row := regexp.MustCompile("(?m)^\\| `(CORRALAI_[A-Z_]+)` \\|[^|]*\\| `([^`]+)` \\|$")
	byVar := map[string]statedir.File{}
	for _, f := range statedir.Files() {
		byVar[f.Override] = f
	}
	checked := 0
	for _, m := range row.FindAllStringSubmatch(string(b), -1) {
		f, ok := byVar[m[1]]
		if !ok {
			continue
		}
		want := "$" + strings.ToUpper(f.Root.String()) + "/" + f.Name
		if m[2] != want {
			t.Errorf("%s: page says %q, statedir says %q", m[1], m[2], want)
		}
		checked++
	}
	if checked < 3 {
		t.Fatalf("matched only %d store rows — the regexp no longer reads the table", checked)
	}
}

// envDocProblem reports why a binary's env doc block would not reach its
// generated reference. scripts/gen-cli-docs.sh extracts the block only from
// an exact "// Env:" line, so a reworded marker silently drops the whole
// "Environment variables" section while --check stays green (the docs match
// the broken output). A main.go that mentions an env block at all must keep
// the exact marker, and its reference must carry the section.
func envDocProblem(mainSrc, doc string) string {
	hasLooseMarker := regexp.MustCompile(`(?m)^// Env\b`).MatchString(mainSrc)
	hasExactMarker := regexp.MustCompile(`(?m)^// Env:`).MatchString(mainSrc)
	switch {
	case hasLooseMarker && !hasExactMarker:
		return "env doc block marker is not exactly \"// Env:\" — gen-cli-docs.sh will skip it"
	case hasExactMarker && !strings.Contains(doc, "## Environment variables"):
		return "has an env doc block but the generated reference lacks the Environment variables section"
	}
	return ""
}

func TestDocsEnvBlockNegativeControl(t *testing.T) {
	broken := "// Env ($STATE is the state dir):\n//\n//\tCORRALAI_X  x\npackage main\n"
	if envDocProblem(broken, "# corral\n") == "" {
		t.Fatal("the guard does not catch a reworded // Env: marker")
	}
	if envDocProblem("// Env:\n//\n//\tCORRALAI_X  x\npackage main\n", "# corral\n") == "" {
		t.Fatal("the guard does not catch a missing generated section")
	}
	if envDocProblem("// Env:\n//\tX\npackage main\n", "## Environment variables\n") != "" {
		t.Fatal("the guard flags a healthy binary")
	}
}

// Every binary's main.go is found by globbing cmd/*, never enumerated.
func TestDocsEveryEnvBlockReachesTheGeneratedReference(t *testing.T) {
	mains, err := filepath.Glob("../*/main.go")
	if err != nil || len(mains) < 5 {
		t.Fatalf("found %d binaries (%v) — the glob no longer sees cmd/", len(mains), err)
	}
	withBlock := 0
	for _, m := range mains {
		src, err := os.ReadFile(m) // #nosec G304 -- globbed from this repository
		if err != nil {
			t.Fatal(err)
		}
		name := filepath.Base(filepath.Dir(m))
		var doc string
		if d, err := os.ReadFile("../../docs/cli/" + name + ".md"); err == nil { // #nosec G304
			doc = string(d)
		}
		if regexp.MustCompile(`(?m)^// Env\b`).Match(src) {
			withBlock++
		}
		if p := envDocProblem(string(src), doc); p != "" {
			t.Errorf("%s: %s", name, p)
		}
	}
	if withBlock < 2 {
		t.Fatalf("only %d binaries have an env block — the guard reads nothing", withBlock)
	}
}

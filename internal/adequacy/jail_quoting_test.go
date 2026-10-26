// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
)

// fakeTool writes an executable file at dir/rel and returns its path.
func fakeTool(t *testing.T, dir, rel string) string {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o700); err != nil { // #nosec G306 -- an executable test fixture
		t.Fatal(err)
	}
	return p
}

// THE JAIL BINDS THE OPERATOR'S TOOLCHAIN AND NOTHING A REPOSITORY NAMES
// (ruling R29, final review I3), measured on the real coverage wrappers:
//
//   - (a) an operator node outside /usr is bound for a JavaScript unit run.
//     Before, corral's own NODE_OPTIONS value split into the junk program
//     `}--require`, which swallowed the quoted node path, and only the
//     reducer's bare `node` was looked up;
//   - (b) a test file the repository names `tests/x;<tool>;.test.js` binds
//     nothing: before, <tool> stood in command position and its toolchain
//     root was bound into the jail.
func TestJailBindsTheOperatorsToolchainAndNothingARepositoryPathNames(t *testing.T) {
	dir := t.TempDir()
	node := fakeTool(t, dir, "nvm/bin/node")
	evil := fakeTool(t, dir, "evil/bin/evil")
	evilRoot := filepath.Join(dir, "evil")

	js, _ := lang.ByName("javascript")
	cov, ok := js.(lang.CoverageReporter).CoverageCmd([]string{node, "--test", "a.test.js"})
	if !ok {
		t.Fatal("no JavaScript coverage command")
	}
	binds, err := toolchainBinds(cov)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, b := range binds {
		found = found || strings.HasPrefix(node, b.Host+string(filepath.Separator))
	}
	if !found {
		t.Errorf("(a) the JavaScript unit run %q binds %+v: missing the operator's node at %s", cov, binds, node)
	}

	hostile := "tests/x;" + evil + ";.test.js"
	for _, c := range []struct {
		lang string
		argv []string
	}{
		{"javascript", []string{"node", "--test", hostile}},
		{"typescript", []string{"vitest", "run", hostile}},
		{"ruby", []string{"rspec", hostile}},
		{"php", []string{"php", "vendor/bin/phpunit", hostile}},
		{"go", []string{"go", "test", "./" + hostile}},
		{"rust", []string{"cargo", "test", "--test", hostile}},
		{"python", []string{"python3", "-m", "pytest", hostile}},
	} {
		p, _ := lang.ByName(c.lang)
		cmd, ok := p.(lang.CoverageReporter).CoverageCmd(c.argv)
		if !ok {
			t.Fatalf("%s: no coverage command for %q", c.lang, c.argv)
		}
		binds, err := toolchainBinds(cmd)
		if err != nil {
			t.Errorf("(b) %s: a repository path made the bind lookup fail: %v", c.lang, err)
			continue
		}
		for _, b := range binds {
			if b.Host == evilRoot || strings.HasPrefix(evil, b.Host+string(filepath.Separator)) {
				t.Errorf("(b) %s: a repository's test path requested the bind %+v", c.lang, b)
			}
		}
	}
}

// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/lang"
)

func writeRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// The runner behind `npm test` is read from package.json once, where the
// repository is on disk, said on the run's output, and carried to the
// driver on the RunSpec — whatever the host can then do with a jail.
func TestCertifyLocalResolvesTheScriptBehindNpmTest(t *testing.T) {
	repo := writeRepo(t, map[string]string{
		"package.json":     `{"scripts": {"test": "jest"}}`,
		"src/calc.js":      "module.exports = (a, b) => a + b;\n",
		"src/calc.test.js": "test('adds', () => {});\n",
	})
	plug, _ := lang.ByName("javascript")
	var stdout bytes.Buffer
	prep, err := prepareAuditJail(context.Background(), localAuditInput{
		repoDir: repo, codePath: "src/calc.js", testPath: "src/calc.test.js", lang: "javascript",
		checkArgv: []string{"npm", "test"},
	}, plug, time.Minute, &stdout)
	if !strings.Contains(stdout.String(), "npm test → jest (package.json scripts.test)") {
		t.Errorf("stdout = %q, want the line saying how the runner was found", stdout.String())
	}
	if err != nil {
		if _, serr := resolveLocalJail(""); serr != nil {
			t.Skipf("no sandbox backend on this host: %v", serr)
		}
		t.Fatalf("prepareAuditJail: %v", err)
	}
	defer prep.cleanup()
	rs := newAuditRunSpec(localAuditInput{checkArgv: []string{"npm", "test"}}, auditRoles{}, runSubject{inv: prep.inv})
	if r, _ := lang.RunnerFor(plug, rs.Invocation); r.ID != "jest" {
		t.Errorf("the RunSpec carries runner %q, want jest", r.ID)
	}
}

// A --runner that does not run the file's language is refused before any
// jail is built, and the refusal lists the runners that do.
func TestCertifyLocalRefusesARunnerOfAnotherLanguage(t *testing.T) {
	repo := writeRepo(t, map[string]string{
		"src/calc.js":      "module.exports = (a, b) => a + b;\n",
		"src/calc.test.js": "test('adds', () => {});\n",
	})
	plug, _ := lang.ByName("javascript")
	_, err := prepareAuditJail(context.Background(), localAuditInput{
		repoDir: repo, codePath: "src/calc.js", testPath: "src/calc.test.js", lang: "javascript",
		checkArgv: []string{"npm", "test"}, runner: "pytest",
	}, plug, time.Minute, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "jest") || !strings.Contains(err.Error(), "node-test") {
		t.Errorf("err = %v, want a refusal listing the javascript runners", err)
	}
}

// In a scan, --runner applies to the jobs whose language it runs; a Go job
// in the same scan detects its runner as before.
func TestCertifyRepoRunnerAppliesToItsLanguageOnly(t *testing.T) {
	ex := &localExecutor{runner: "vitest"}
	if got := ex.runnerFor("typescript"); got != "vitest" {
		t.Errorf("typescript job: runner %q", got)
	}
	if got := ex.runnerFor("go"); got != "" {
		t.Errorf("go job: runner %q, want detection", got)
	}
}

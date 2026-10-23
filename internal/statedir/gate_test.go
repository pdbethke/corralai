// SPDX-License-Identifier: Elastic-2.0

package statedir

import (
	"bytes"
	"fmt"
	"go/scanner"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// legacyFile is the one production file allowed to name the old directory.
// Pinned by its full repository path: a base-name match would exempt any
// file called legacy.go anywhere in the tree.
const legacyFile = "internal/statedir/legacy.go"

// integrationCaptures are the files that record what coding agents themselves
// print and where they keep their configuration. `corral setup` writes INTO
// agent config on purpose (an MCP registration, a skill copy), the way
// .claude/skills is legal everywhere, and these files must name those paths to
// say so. They hold no corral state. Pinned by full path or directory prefix,
// never by base name, as legacyFile is.
func integrationCapture(path string) bool {
	return path == "scripts/capture-agent-fixtures.sh" || strings.HasPrefix(path, "internal/agentsetup/testdata/")
}

// homeAnchoredClaude matches a ".claude" directory reached from a home
// directory: ~/.claude, $HOME/.claude, ${HOME}/.claude, /root/.claude,
// /home/<user>/.claude, or a home-directory call followed by a ".claude"
// path segment (path.join(os.homedir(), '.claude', ...)). A repo-relative
// ".claude/" (a .gitignore entry) and ".claude-plugin" are not matched.
var homeAnchoredClaude = regexp.MustCompile(`(?:~|\$HOME|\$\{HOME\}|/root|/home/[^/\s'"]+|homedir\(\)\s*,\s*['"])/?\.claude(?:[^A-Za-z0-9_-]|$)`)

// The property (plan ruling R2): corral keeps no state under another
// agent's directory. Over EVERY tracked text file (binaries skipped; test
// files and CHANGELOG.md, which records the move, exempt):
//
//	(a) No Go string literal names ".claude" outside legacy.go.
//	(b) No file — Go comment or string, Markdown, script, config — names
//	    ".claude/corralai", the old spelling of a corral file.
//	(c) No file that is neither Go nor prose (a script, a Dockerfile, a
//	    compose file, a .gitignore — anything that runs or configures a
//	    run) names a home-anchored .claude path (homeAnchoredClaude). That
//	    is where a /root/.claude volume or a $HOME/.claude default hides.
//	    Markdown and MDX are prose: they may say where corral USED to keep
//	    its files, which the migration docs must.
//
// Claude Code's own skills directory (.claude/skills) is an integration,
// not state, and is legal everywhere.
func violations(path string, src []byte) []string {
	var out []string
	if strings.HasSuffix(path, ".go") {
		fset := token.NewFileSet()
		file := fset.AddFile(path, -1, len(src))
		var s scanner.Scanner
		s.Init(file, src, nil, scanner.ScanComments)
		for {
			pos, tok, lit := s.Scan()
			if tok == token.EOF {
				break
			}
			if tok == token.STRING && strings.Contains(lit, ".claude") && path != legacyFile {
				out = append(out, fset.Position(pos).String()+": string literal names .claude")
			}
			if (tok == token.STRING || tok == token.COMMENT) && strings.Contains(lit, ".claude/corralai") && path != legacyFile {
				out = append(out, fset.Position(pos).String()+": names a corral file under .claude")
			}
		}
		return out
	}
	if path == "CHANGELOG.md" {
		return nil
	}
	text := string(src)
	if strings.Contains(text, ".claude/corralai") {
		out = append(out, path+": names a corral file under .claude")
	}
	if ext := filepath.Ext(path); ext == ".md" || ext == ".mdx" {
		return out
	}
	if integrationCapture(path) {
		return out
	}
	for _, loc := range homeAnchoredClaude.FindAllStringIndex(text, -1) {
		rest := text[loc[1]-1:]
		if strings.HasPrefix(rest, "/skills") {
			continue
		}
		line := strings.Count(text[:loc[0]], "\n") + 1
		out = append(out, fmt.Sprintf("%s:%d: names a home-anchored .claude path (%s)", path, line, strings.TrimSpace(text[loc[0]:loc[1]])))
	}
	return out
}

// isBinary is git's own heuristic: a NUL in the first 8000 bytes.
func isBinary(src []byte) bool {
	n := len(src)
	if n > 8000 {
		n = 8000
	}
	return bytes.IndexByte(src[:n], 0) >= 0
}

func TestGateCatchesAPlantedPath(t *testing.T) {
	planted := []byte("package x\nvar p = filepath.Join(home, \".claude\", \"corralai_x\")\n")
	if len(violations("internal/x/x.go", planted)) == 0 {
		t.Fatal("the gate does not catch a planted .claude path — it guards nothing")
	}
	if len(violations("docs/x.md", []byte("default ~/.claude/corralai_x.duckdb"))) == 0 {
		t.Fatal("the gate does not catch a planted doc default")
	}
	if len(violations("README.md", []byte("install to ~/.claude/skills/corral/"))) != 0 {
		t.Fatal("the gate flags Claude Code's skills dir, which is an integration, not state")
	}
	// The non-Go negative controls: each of these shipped, and the old
	// *.go/*.md/*.mdx walk could not see any of them.
	for path, src := range map[string]string{
		"deploy/x/docker-compose.yml": `volumes: ["brain-state:/root/.claude"]`,
		"deploy/x/Dockerfile":         "RUN mkdir -p /root/.claude/projects/default/memory",
		"scripts/x.sh":                `DB="${X:-$HOME/.claude/corralai_x.duckdb}"`,
		"scripts/x.py":                `HOME_DB = os.path.expanduser("~/.claude")`,
		"site/scripts/x.mjs":          `const p = path.join(os.homedir(), '.claude', 'corralai_x.duckdb');`,
		".gitignore":                  "# state lives under ~/.claude, never in-repo",
	} {
		if len(violations(path, []byte(src))) == 0 {
			t.Errorf("the gate does not catch %s: %s", path, src)
		}
	}
	for path, src := range map[string]string{
		"scripts/x.sh":                "cp -r skills/corral ~/.claude/skills/corral/",
		".gitignore":                  "/.claude/",
		"testdata/x.tsv":              "pdbethke/corralai\t.claude-plugin/marketplace.json",
		"docs/configuration.mdx":      "Before 1.0, corral kept these files in `~/.claude`.",
		"internal/statedir/legacy.go": "package statedir\nvar d = filepath.Join(h, \".claude\")\n",
	} {
		if v := violations(path, []byte(src)); len(v) != 0 {
			t.Errorf("the gate flags a legal mention in %s: %v", path, v)
		}
	}
	// The integration-capture exemption is those paths only, and only for rule (c).
	home := []byte("HOME=/home/x/.claude.json")
	if v := violations("internal/agentsetup/testdata/agents/x.txt", home); len(v) != 0 {
		t.Errorf("an agent-output capture is flagged: %v", v)
	}
	if len(violations("internal/agentsetup/other.sh", home)) == 0 || len(violations("scripts/other.sh", home)) == 0 {
		t.Error("the capture exemption is wider than its pinned paths")
	}
	if len(violations("internal/agentsetup/testdata/x.txt", []byte("~/.claude/corralai_x"))) == 0 {
		t.Error("the capture exemption must not excuse corral state under .claude")
	}
	// The exemption is the exact path, not the base name.
	if len(violations("internal/other/legacy.go", []byte("package other\nvar d = filepath.Join(h, \".claude\")\n"))) == 0 {
		t.Error("any file named legacy.go is exempt — the exemption must be internal/statedir/legacy.go only")
	}
}

func TestNoCorralStateUnderAnotherAgentsDirectory(t *testing.T) {
	root, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Skipf("not a git checkout: %v", err)
	}
	top := strings.TrimSpace(string(root))
	out, err := exec.Command("git", "-C", top, "ls-files", "-z").Output()
	if err != nil {
		t.Fatal(err)
	}
	files := strings.Split(strings.TrimRight(string(out), "\x00"), "\x00")
	if len(files) < 100 {
		t.Fatalf("only %d tracked files — the walk is not seeing the repository", len(files))
	}
	for _, rel := range files {
		if strings.HasSuffix(rel, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(top, rel)) // #nosec G304 -- from git ls-files on this repository
		if os.IsNotExist(err) {
			continue // deleted in the worktree, not yet committed
		}
		if err != nil {
			t.Fatal(err)
		}
		if isBinary(src) {
			continue
		}
		for _, v := range violations(rel, src) {
			t.Error(v)
		}
	}
}

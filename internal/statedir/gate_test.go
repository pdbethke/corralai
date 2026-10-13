// SPDX-License-Identifier: Elastic-2.0

package statedir

import (
	"go/scanner"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The property (plan ruling R2): corral keeps no state under another
// agent's directory. (a) No Go string literal names ".claude" outside
// legacy.go. (b) No Go comment or string, and no tracked Markdown outside
// CHANGELOG.md, names ".claude/corralai". Mentions of Claude Code's own
// skills directory are an integration, not state, and stay legal.
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
			if tok == token.STRING && strings.Contains(lit, ".claude") && filepath.Base(path) != "legacy.go" {
				out = append(out, fset.Position(pos).String()+": string literal names .claude")
			}
			if (tok == token.STRING || tok == token.COMMENT) && strings.Contains(lit, ".claude/corralai") && filepath.Base(path) != "legacy.go" {
				out = append(out, fset.Position(pos).String()+": names a corral file under .claude")
			}
		}
		return out
	}
	if filepath.Base(path) != "CHANGELOG.md" && strings.Contains(string(src), ".claude/corralai") {
		out = append(out, path+": names a corral file under .claude")
	}
	return out
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
}

func TestNoCorralStateUnderAnotherAgentsDirectory(t *testing.T) {
	root, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Skipf("not a git checkout: %v", err)
	}
	top := strings.TrimSpace(string(root))
	out, err := exec.Command("git", "-C", top, "ls-files", "*.go", "*.md", "*.mdx").Output()
	if err != nil {
		t.Fatal(err)
	}
	files := strings.Fields(string(out))
	if len(files) < 100 {
		t.Fatalf("only %d tracked files — the walk is not seeing the repository", len(files))
	}
	for _, rel := range files {
		if strings.HasSuffix(rel, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(top, rel)) // #nosec G304 -- from git ls-files on this repository
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range violations(rel, src) {
			t.Error(v)
		}
	}
}

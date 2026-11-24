// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/pairing"
)

const reachSrc = "pub fn double(x: i32) -> i32 { x * 2 }\n\n#[cfg(test)]\nmod tests {\n    use super::*;\n    #[test]\n    fn own() { assert_eq!(double(2), 4); }\n}\n"

var reachInline = pairing.Landing{Path: "src/lib.rs", Shape: pairing.Inline, Label: "inline", Into: true, Depth: 0, DeclarationsRead: true}

// TestReachCanaryBlockIsALandedTestThatFails: the inline positive control's
// block is the TestLander's always-failing test, landed exactly as a real
// authored test is (same module header, same use line), so a command that
// runs the authored block runs it, and one that never runs that block
// cannot tell it is there.
func TestReachCanaryBlockIsALandedTestThatFails(t *testing.T) {
	block, err := ReachCanaryBlock(reachInline, reachSrc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(block, "#[cfg(test)]\nmod corral_tests {\n#[allow(unused_imports)]\nuse super::*;\n") || !strings.Contains(block, "#[test]") || !strings.Contains(block, "panic!(") {
		t.Fatalf("not a landed, always-failing test block:\n%s", block)
	}
	if _, err := ReachCanaryBlock(pairing.Landing{Path: "a.txt", Into: true}, ""); err == nil {
		t.Error("a landing no TestLander can compose was given a reach canary")
	}
}

// TestRustReachCanaryFailsOnlyWhenTheBlockRuns is the finding this exists for,
// executed by the real runner: with the block appended to an inline source,
// `cargo test` fails (it ran the block's test), while a command that compiles
// the same file but filters to the project's own tests passes — the
// "unreached" a canary of invalid source could never show, since both
// commands compile the file.
func TestRustReachCanaryFailsOnlyWhenTheBlockRuns(t *testing.T) {
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not installed — this proof needs the real runner")
	}
	block, err := ReachCanaryBlock(reachInline, reachSrc)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{
		"Cargo.toml": "[package]\nname = \"reach\"\nversion = \"0.1.0\"\nedition = \"2021\"\n",
		"src/lib.rs": reachSrc + block,
	} {
		if err := os.WriteFile(filepath.Join(root, k), []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run := func(args ...string) (bool, string) {
		cmd := exec.Command(cargo, args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+filepath.Join(root, "target"))
		out, err := cmd.CombinedOutput()
		return err == nil, string(out)
	}
	if ok, out := run("test", "--offline", "--lib"); ok || !strings.Contains(out, "corral_reach_canary") {
		t.Fatalf("cargo test over the whole lib did not fail on the reach canary:\n%s", out)
	}
	if ok, out := run("test", "--offline", "--lib", "--", "--exact", "tests::own"); !ok {
		t.Fatalf("a command that never runs the block failed anyway, so the control could not tell reached from unreached:\n%s", out)
	}
}

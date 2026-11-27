// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/sandbox"
)

// A Rust run through the real wiring and the real jail, as
// TestGoGradingRunsShareAFrozenRunCache is for go: the file records a shared
// cache, the baseline is the one run with CARGO_TARGET_DIR writable, the
// mutant runs frozen and is killed, and no command is rewritten (cargo stores
// no test result to forbid).
func TestRustGradingRunsShareAFrozenTargetDir(t *testing.T) {
	iso, err := resolveLocalJail("")
	if err != nil {
		t.Skipf("SKIP: no sandbox backend on this host: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sandbox.SystemRustupHome, "toolchains")); err != nil {
		t.Skipf("SKIP: no system-wide rustup at %s: %v", sandbox.SystemRustupHome, err)
	}
	t.Setenv("TMPDIR", t.TempDir())
	if !sandbox.Overlays(iso) || !sandbox.OverlaysDir(iso, t.TempDir()) {
		t.Skipf("SKIP: %s cannot overlay a directory here; the shared-cache path is unexercised", iso.Name())
	}
	t.Setenv("PATH", sandbox.SystemCargoHome+"/bin:"+os.Getenv("PATH"))

	repoDir := t.TempDir()
	files := map[string]string{
		"Cargo.toml": "[package]\nname = \"m\"\nversion = \"0.1.0\"\nedition = \"2021\"\n\n[dependencies]\ncorraldep = \"0.1\"\n",
		"Cargo.lock": "version = 3\n\n[[package]]\nname = \"corraldep\"\nversion = \"0.1.0\"\n" +
			"source = \"registry+https://github.com/rust-lang/crates.io-index\"\n\n" +
			"[[package]]\nname = \"m\"\nversion = \"0.1.0\"\ndependencies = [\"corraldep\"]\n",
		".cargo/config.toml": "[source.crates-io]\nreplace-with = \"vendored-sources\"\n\n[source.vendored-sources]\ndirectory = \"vendor\"\n",
		"src/lib.rs":         "pub fn one() -> i32 { corraldep::one() * 1 }\n",
		"tests/it.rs":        "#[test]\nfn is_one() {\n    assert_eq!(m::one(), 1);\n}\n",
	}
	for rel, body := range files {
		mustWrite(t, filepath.Join(repoDir, rel), body)
	}
	for rel, body := range map[string]string{
		"vendor/corraldep/Cargo.toml":           "[package]\nname = \"corraldep\"\nversion = \"0.1.0\"\nedition = \"2021\"\n",
		"vendor/corraldep/src/lib.rs":           "pub fn one() -> i32 { 1 }\n",
		"vendor/corraldep/.cargo-checksum.json": `{"files":{},"package":null}` + "\n",
	} {
		mustWrite(t, filepath.Join(repoDir, rel), body)
	}
	seed := repoSeed{seedDir: repoDir, files: files, cleanup: func() {},
		binds: []adequacy.DepBind{{Host: filepath.Join(repoDir, "vendor"), Rel: "vendor"}}}
	argv := []string{"cargo", "test", "--offline"}
	w, err := buildJailWiring(context.Background(), jailWiringInput{
		iso: iso, timeout: 3 * time.Minute,
		codePath: "src/lib.rs", testPath: "tests/it.rs", repoDir: repoDir, langName: "rust",
		fsPath:    func(q string) string { return filepath.Join(repoDir, q) },
		code:      []byte(files["src/lib.rs"]),
		devTest:   []byte(files["tests/it.rs"]),
		checkArgv: argv, inv: lang.Cmd(argv),
		stdout: io.Discard, seed: &seed,
	})
	if err != nil {
		t.Fatalf("buildJailWiring: %v", err)
	}
	defer w.cleanup()
	if w.runCache != "shared" {
		t.Fatalf("recorded run cache = %q, want %q", w.runCache, "shared")
	}
	rec := recordRuns(t, &w)
	mutants := []adequacy.Mutant{{ID: "m1", Search: "* 1", Replace: "* 2"}}
	rep, err := w.scorer.ScoreDevReport(context.Background(), w.codeKey, files["src/lib.rs"], "", mutants, strings.Join(argv, " "), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.CompliantPass || len(rep.Killed) != 1 {
		t.Fatalf("pass=%v killed=%d, want a passing baseline and the mutant killed", rep.CompliantPass, len(rep.Killed))
	}
	if !rec.Warmed() {
		t.Error("the baseline was the warming run, but the jail does not say it was warmed")
	}
	var warm, frozen int
	for _, r := range rec.runs {
		if r.warm {
			warm++
			if !slices.Equal(r.cmd, argv) {
				t.Errorf("the warming command was rewritten: %v, want %v", r.cmd, argv)
			}
		} else if len(r.cmd) > 1 && r.cmd[1] == "test" {
			frozen++
		}
	}
	if warm != 1 || frozen < 1 {
		t.Errorf("warm runs = %d, frozen test runs = %d; want exactly one warming run (the baseline) and at least one frozen run", warm, frozen)
	}
}

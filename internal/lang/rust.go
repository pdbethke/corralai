// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pdbethke/corralai/internal/pairing"
	"github.com/pdbethke/corralai/internal/sandbox"
)

func init() { Register(rustPlugin{}) }

// rustPlugin is registered with pairing (TestRules) and detection first; the
// rest of its methods give the honest minimal answer each method's contract
// defines for "not yet" (ok=false, nil, a stock command) until the tasks that
// prove them against a real crate. Registering it is what puts Rust under
// every registry-derived test in this package at once, so a method here is
// never a guess the tests have not read.
type rustPlugin struct{}

func (rustPlugin) Name() string                { return "rust" }
func (rustPlugin) Detect(codePath string) bool { return filepath.Ext(codePath) == ".rs" }

// Scaffold is empty: a Rust audit always runs inside a real crate (the
// project's own Cargo.toml), never a corral-owned single-file workspace.
func (rustPlugin) Scaffold() map[string]string { return map[string]string{} }

func (rustPlugin) TestCmd() []string { return []string{"cargo", "test"} }

// HarnessFiles names what cargo-nextest reads before any test: its profiles
// (retries, timeouts, test groups) change what the whole suite measures, so
// a change to the file moves the verdict cache key. Matched by basename (see
// HarnessConfigurer); nextest reads it from .config/ at the workspace root.
func (rustPlugin) HarnessFiles() []string { return []string{"nextest.toml"} }

// LineComment is the rust line-comment marker.
func (rustPlugin) LineComment() string { return "//" }

// DependencyDirs is `cargo vendor`'s default destination. cargo's target/ is
// deliberately NOT declared here although every run writes into it:
// treedirs already lists it as language-neutral BUILD OUTPUT, which the scan
// skips but still ACCOUNTS in the report, and declaring it a dependency would
// silently move it to the unaccounted set for every repository
// (TestEnumerateSkipsBuildOutputDirs). Whether the workspace pool must treat
// target/ as run-written belongs to the task that runs cargo there. The jail
// seed's skip of target/ is declared separately, by BuildDirs.
func (rustPlugin) DependencyDirs() []DepDir { return []DepDir{{Name: "vendor"}} }

// cargoVendoredSource is the source name `cargo vendor` prints in the
// replacement config it asks to be appended. A cargo config that already
// names it was produced by an earlier `cargo vendor`.
const cargoVendoredSource = `"vendored-sources"`

// cargoConfigFile is the repo-relative config file cargo reads for the
// crate's own directory: the legacy extensionless `.cargo/config` when it
// exists — cargo then IGNORES config.toml, with a warning (measured, cargo
// 1.90) — else `.cargo/config.toml`.
func cargoConfigFile(repoDir string) string {
	if fi, err := os.Stat(filepath.Join(repoDir, ".cargo", "config")); err == nil && fi.Mode().IsRegular() {
		return ".cargo/config"
	}
	return ".cargo/config.toml"
}

// Vendor: a crate is vendored with `cargo vendor --locked vendor`, whose
// stdout — the source-replacement config pointing crates-io and every git
// dependency at vendor/ — is appended to the config file cargo will read.
//
// --locked because the audit grades the dependency versions the repository
// pinned, never a fresh resolution: a crate with no Cargo.lock (or a stale
// one) fails here with cargo's own "needs to be updated but --locked was
// passed", which ensureVendored surfaces verbatim. That is the honest
// outcome — ok=false would run the audit unvendored and fail later, in the
// offline jail, for a reason the operator could not see.
//
// Done is empty because cargo's "already vendored" is not a path: a vendor/
// directory is just as often C sources a -sys crate carries, and a
// .cargo/config.toml just as often holds aliases. It is the config naming
// cargo vendor's own source, which this checks itself. Any OTHER existing
// [source.crates-io] is refused at append time (see VendorStep.StdoutAppend),
// never merged into: the file is the operator's, not corral's.
func (rustPlugin) Vendor(repoDir string) (VendorStep, bool) {
	if _, err := os.Stat(filepath.Join(repoDir, "Cargo.toml")); err != nil {
		return VendorStep{}, false
	}
	cfg := cargoConfigFile(repoDir)
	if b, err := os.ReadFile(filepath.Join(repoDir, filepath.FromSlash(cfg))); err == nil && strings.Contains(string(b), cargoVendoredSource) { // #nosec G304 -- one of two fixed names under the repo dir; only searched, never executed or echoed
		return VendorStep{}, false
	}
	return VendorStep{Argv: []string{"cargo", "vendor", "--locked", "vendor"}, StdoutAppend: cfg}, true
}

// BuildDirs is cargo's default build directory:`target` beside a
// Cargo.toml, which is cargo's own definition of it. The marker keeps a
// directory merely named target (somebody's package) in the jail seed.
// A build.target-dir or CARGO_TARGET_DIR that moves it elsewhere is not
// followed: the default is what a checkout carries in practice, and a
// build directory this misses costs seed size (at worst the seed cap's
// refusal), never a wrong verdict.
func (rustPlugin) BuildDirs() []BuildDir {
	return []BuildDir{{Name: "target", Marker: "Cargo.toml"}}
}

// CompileCheck builds the test targets without running them. cargo compiles
// a crate, not a file, so there is no narrower honest scope to name here
// (cf. Go's package-scoped `go test -run ^$`); it is one command, and never
// empty — see Plugin.CompileCheck.
func (rustPlugin) CompileCheck(string, string) [][]string {
	return [][]string{{"cargo", "test", "--no-run"}}
}

// TestRules declares where a Rust source's tests live, in the order Resolve
// tries them:
//
//  1. Declared — the file the source's own out-of-line test module loads
//     (`#[path = "app_id_tests.rs"] mod tests;`, or a bare `mod tests;` and
//     rustc's default module file). The source NAMES its test; that is the
//     strongest evidence a pairing can have, so it comes first.
//  2. Inline — the source itself, when it holds `#[cfg(test)] mod tests { }`.
//     This is also Primary's answer, so an authored test lands in the
//     source file by default.
//  3. and 4. Recognize `{base}_tests.rs` and `tests.rs`: the names Rust test
//     modules are given in practice (Warp's app_id_tests.rs, a default
//     module's tests.rs), so IsTest never audits one as a source. Never
//     candidates: which source one belongs to is said by the source's own
//     module declaration, not guessed from the name.
//  5. TestDir `tests` — cargo's integration tests, a test by directory
//     alone; their file names carry no marker, which is why this needs its
//     own Shape rather than a Name (see pairing.Rule's marker invariant).
//
// Rust has no Sibling or naming-convention candidate on purpose: a file named
// like a test is only that file's test if the file says so.
func (rustPlugin) TestRules() []pairing.Rule {
	return []pairing.Rule{
		{Shape: pairing.Declared, Rank: 0},
		{Shape: pairing.Inline, Rank: 0},
		{Shape: pairing.Recognize, Name: "{base}_tests.rs"},
		{Shape: pairing.Recognize, Name: "tests.rs"},
		{Shape: pairing.TestDir, Dir: "tests"},
	}
}

// Preflight requires the runner's binary: cargo for libtest, cargo-nextest for
// nextest — or the operator's own first token (a rustup proxy at an absolute
// path) when one is given. The runner row decides; see RunnerFor.
//
// That is ALL it checks, because Preflight runs on every substrate: a stock
// rustup install (~/.cargo, no sudo) is exactly what --substrate workspace
// runs, on the host. What only the jail needs is JailPreflight's.
func (rustPlugin) Preflight(repoRoot string, testCmd []string) error {
	return toolOnPath(rustPreflightBin(testCmd))
}

// JailPreflight checks the half of the toolchain the jail needs and the host
// does not: the jail's rustup home is pinned to sandbox.SystemRustupHome
// (sandbox's pinned env), so a tool that resolves under the operator's home
// beside rustup (~/.cargo/bin/cargo, a rustup proxy; or a cargo-nextest
// there, which dispatches through that cargo) runs there only when the
// system-wide toolchain exists. Without it the audit would spend its way to
// a jail that says "rustup could not choose a version of cargo", which reads
// as a broken project. A tool that is NOT beside rustup (a distribution
// package, a tarball) needs nothing and passes.
//
// The system toolchain's presence is judged by its toolchains/ directory
// existing. That is not proof the toolchain in it is the one the project
// pins (rust-toolchain.toml) or that it is complete: a mismatch surfaces
// inside the jail, in rustup's own words, as a baseline that cannot pass.
func (rustPlugin) JailPreflight(testCmd []string) error {
	bin := rustPreflightBin(testCmd)
	// cargo-nextest is a plain binary, but `cargo nextest` dispatches through
	// whichever cargo is on PATH, so that one is checked too.
	for _, name := range []string{bin, preflightBin(testCmd, "cargo")} {
		if err := userHomeRustupNeedsSystemToolchain(name); err != nil {
			return err
		}
	}
	return nil
}

// rustPreflightBin is the binary testCmd's cargo row needs on PATH.
func rustPreflightBin(testCmd []string) string {
	r, _ := RunnerFor(rustPlugin{}, Cmd(testCmd))
	return r.PreflightBin(testCmd)
}

// systemRustLayoutHint names the install the jail can see, for every
// JailPreflight message that has to tell the operator what to do.
const systemRustLayoutHint = "install it with `RUSTUP_HOME=" + sandbox.SystemRustupHome +
	" CARGO_HOME=" + sandbox.SystemCargoHome + " rustup-init --no-modify-path --default-toolchain <version>`, add " +
	sandbox.SystemCargoHome + "/bin to PATH, and `cargo install --locked cargo-nextest` if you use nextest — or run `certify --repo --substrate workspace`, which runs your own toolchain and builds no jail"

// systemRustupHome is sandbox.SystemRustupHome, a variable only so a test can
// stand a temp dir in for the real /usr/local/rustup.
var systemRustupHome = sandbox.SystemRustupHome

// SwapSystemRustupHomeForTest stands dir in for sandbox.SystemRustupHome in
// JailPreflight's existence check and returns the restore. TESTS ONLY: it
// exists because the doors that decide whether to call JailPreflight live
// in other packages (cmd/corral), and the real /usr/local/rustup is present
// on every host that runs the jail tests, so without it no test could watch
// a door refuse. Not safe for concurrent use.
func SwapSystemRustupHomeForTest(dir string) (restore func()) {
	old := systemRustupHome
	systemRustupHome = dir
	return func() { systemRustupHome = old }
}

// userHomeRustupNeedsSystemToolchain refuses a tool that resolves under
// $HOME beside a rustup binary while the system-wide rustup home is missing.
// nil for any other tool, wherever it lives.
func userHomeRustupNeedsSystemToolchain(name string) error {
	p, err := exec.LookPath(name)
	if err != nil {
		return nil // Preflight reports a missing tool; this is not its job
	}
	home, herr := os.UserHomeDir()
	if herr != nil || home == "" || !strings.HasPrefix(p, filepath.Clean(home)+string(filepath.Separator)) {
		return nil
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(p), "rustup")); err != nil { // #nosec G703 -- existence check only
		return nil // nothing here runs rustup's toolchain
	}
	if fi, err := os.Stat(filepath.Join(systemRustupHome, "toolchains")); err == nil && fi.IsDir() { // #nosec G703 -- existence check only
		return nil
	}
	return fmt.Errorf("lang: %q resolves to %s, beside rustup under your home, so it runs rustup's toolchain — and the audit jail cannot see your home: it runs the system-wide toolchain in %s, which is not installed here. %s",
		name, p, sandbox.SystemRustupHome, systemRustLayoutHint)
}

func (rustPlugin) PromptLang() string { return "Rust" }

func (rustPlugin) TestWriterSystem() string {
	return `You are a TEST-WRITER. Given a security control GOAL, a target source file, and its signature surface, write executable Rust tests that verify the code SATISFIES the goal.
- MATCH THE PROJECT'S CONVENTIONS, which the developer's own tests (shown to you) use, ahead of everything below: their naming, their assertion helpers, and any test crates they already depend on.
- corral places what you return inside a test module it appends where the project keeps this file's tests; the task names that file and the ` + "`use`" + ` line that brings the code under test (private items included) into scope. Return ONLY that module's items — plain ` + "`#[test]`" + ` functions and any helpers or ` + "`use`" + ` lines they need — never the ` + "`mod`" + ` wrapper itself.
- It MUST compile against the target and MUST FAIL if the goal is violated — test the goal's boundary (what a weakened implementation would pass that a compliant one must not).
- Standard library only unless the project's own tests already use more. Deterministic, no network.
Return ONLY the raw Rust items — no prose, no markdown fences.`
}

func (rustPlugin) MutantSystem() string {
	return mutantSystem("signatures, types, and module paths (a drop-in replacement that compiles)", "compile errors")
}

// ImportPath is always ok=false: the authored test is a module appended to
// the landing file (LandTest), which reaches the code under test with an
// ancestor glob (`use super::super::*;`), so there is no import path to
// derive. See lang.Plugin.ImportPath's doc comment for the general rule this
// follows.
func (rustPlugin) ImportPath(string, func(string) bool) (string, bool) { return "", false }

// ImportNote is always "": see ImportPath — the landed module's own use line,
// which LandTest writes from the landing's depth, needs no per-task correction.
func (rustPlugin) ImportNote(string, bool) string { return "" }

// rustFirstFailure names the first test the runner's own summary reported as
// failing, for the killed_by label (internal/adequacy/score.go). GRADING IS BY
// EXIT CODE; this only labels, so "" means "no label", never a grade: it is
// returned for a passing run and equally for output the grammar does not
// recognise (a build failure, another runner's text).
//
// The id differs by runner: libtest gives the test path (`tests::name`),
// nextest gives `<binary-id> <name>`. FailureParser is handed output alone, so
// the grammar is read off the output (rustGrammarOf), not off a command.
func rustFirstFailure(output []byte) string {
	run := parseRustRun(rustGrammarOf(output), output)
	if len(run.Failures) == 0 {
		return ""
	}
	return run.Failures[0]
}

// WorkspaceRunEnv is a no-op FOR NOW, and that is not a claim of safety:
// cargo decides whether to rebuild a local crate by comparing source mtimes
// against target/'s dep-info, which may share the same-second staleness hole
// CPython's __pycache__ has on the workspace substrate. It is unmeasured, and
// the task that runs Rust on that substrate owns measuring it. See
// lang.Plugin.WorkspaceRunEnv's doc comment.
func (rustPlugin) WorkspaceRunEnv() (env []string, cleanup func()) { return nil, func() {} }

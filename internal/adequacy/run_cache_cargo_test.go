// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/lang"
)

// THE SAFETY ARGUMENT FOR A SHARED CARGO TARGET DIRECTORY, run against the
// real jail (the go version is run_cache_replay_test.go).
//
// cargo keeps no test RESULTS: every `cargo test` runs the test binaries it
// built, so there is no result for a frozen run to replay. What a shared
// target/ holds is BUILD output, and cargo decides whether a path crate's
// output is still good by comparing its source files' mtimes with the build's
// own (go compares content hashes). So the one way a shared target/ could
// grade a mutant as the unmutated code is a mutated file whose mtime is older
// than the warming build: cargo would then reuse the warm build's object of
// the original file. Three facts keep that from happening, and these tests
// run each of them:
//
//  1. frozen runs cannot change what another run reads: their writes vanish
//     with the run (the jail's copy-on-write overlay);
//  2. every run's workspace is written after the warming run has finished
//     (runInJailCache takes the cache before it writes the workspace), so its
//     files are newer than every object the warm build left, and cargo
//     rebuilds the project's own crate in every run;
//  3. a vendored dependency (a directory source) is not rebuilt for its
//     mtimes, which is what sharing saves: it is built once, by the warming
//     run.
//
// TestACargoMutantOlderThanTheWarmBuildIsGradedAsTheOriginal is the negative
// control for fact 2: it shows the hazard is real, so a green result here is
// not a test that could not have failed.

// cargoCacheFiles is a crate depending on one vendored crate, corraldep, whose
// one() the crate's test expects to return 1; ret is what the crate's own
// one() returns.
func cargoCacheFiles(ret string) map[string]string {
	return map[string]string{
		"Cargo.toml": "[package]\nname = \"m\"\nversion = \"0.1.0\"\nedition = \"2021\"\n\n[dependencies]\ncorraldep = \"0.1\"\n",
		"Cargo.lock": "version = 3\n\n[[package]]\nname = \"corraldep\"\nversion = \"0.1.0\"\n" +
			"source = \"registry+https://github.com/rust-lang/crates.io-index\"\n\n" +
			"[[package]]\nname = \"m\"\nversion = \"0.1.0\"\ndependencies = [\"corraldep\"]\n",
		".cargo/config.toml": "[source.crates-io]\nreplace-with = \"vendored-sources\"\n\n" +
			"[source.vendored-sources]\ndirectory = \"vendor\"\n",
		"src/lib.rs": "pub fn one() -> i32 { corraldep::one() * " + ret + " }\n" +
			"#[cfg(test)]\nmod tests {\n    #[test]\n    fn is_one() {\n        assert_eq!(super::one(), 1);\n    }\n}\n",
	}
}

// cargoCacheJail is a jail sharing CARGO_TARGET_DIR, with corraldep vendored
// on the host and bound read-only as vendor/, as a vendored crate's audit is.
func cargoCacheJail(t *testing.T) RunCacheJail {
	t.Helper()
	_ = rustJailOrSkip(t)
	backend := overlayJailOrSkip(t)
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("PATH", systemCargoBin+":"+os.Getenv("PATH"))
	vendor := corraldepVendor(t)
	j := NewJail(backend, 3*time.Minute, WithReadOnlyBinds([]DepBind{{Host: vendor, Rel: "vendor"}}),
		WithRunCache(&lang.EvidenceCache{Env: "CARGO_TARGET_DIR"})).(RunCacheJail)
	t.Cleanup(func() { _ = j.Close() })
	if c := j.RunCache(); c.Target == "" {
		t.Skipf("SKIP: no shared cache on this host (%s)", c.Unshared)
	}
	return j
}

// corraldepVendor writes corraldep, vendored, on the host and returns the
// vendor directory.
func corraldepVendor(t *testing.T) string {
	t.Helper()
	vendor := filepath.Join(t.TempDir(), "vendor")
	dep := filepath.Join(vendor, "corraldep")
	if err := os.MkdirAll(filepath.Join(dep, "src"), 0o750); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"Cargo.toml":           "[package]\nname = \"corraldep\"\nversion = \"0.1.0\"\nedition = \"2021\"\n",
		"src/lib.rs":           "pub fn one() -> i32 { 1 }\n",
		".cargo-checksum.json": `{"files":{},"package":null}` + "\n",
	} {
		if err := os.WriteFile(filepath.Join(dep, filepath.FromSlash(name)), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return vendor
}

var cargoTestOffline = []string{"cargo", "test", "--offline", "--locked"}

func cargoRun(t *testing.T, j RunCacheJail, files map[string]string, cmd []string) (bool, string) {
	t.Helper()
	ok, out, err := j.(bwrapJail).RunTestVerbose(context.Background(), files, cmd)
	if err != nil {
		t.Fatalf("run %v: %v\n%s", cmd, err, out)
	}
	return ok, out
}

func TestASharedCargoTargetDirGradesEveryRunOnItsOwnCode(t *testing.T) {
	for _, cmd := range [][]string{cargoTestOffline, {"cargo", "nextest", "run", "--offline", "--locked"}} {
		t.Run(cmd[1], func(t *testing.T) { sharedTargetDirGradesEveryRun(t, cmd) })
	}
}

func sharedTargetDirGradesEveryRun(t *testing.T, cmd []string) {
	j := cargoCacheJail(t)
	ok, out, err := j.WarmRun(context.Background(), cargoCacheFiles("1"), cmd)
	if err != nil || !ok {
		t.Fatalf("warm run: ok=%v err=%v\n%s", ok, err, out)
	}
	if !strings.Contains(string(out), "Compiling corraldep") {
		t.Fatalf("the warming run did not build the vendored crate:\n%s", out)
	}
	for i, ret := range []string{"2", "1", "2", "1"} {
		ok, out := cargoRun(t, j, cargoCacheFiles(ret), cmd)
		if want := ret == "1"; ok != want {
			t.Fatalf("frozen run %d (one() * %s): passed=%v, want %v: the run was not graded on its own code\n%s", i, ret, ok, want, out)
		}
		if !strings.Contains(out, "Compiling m v0.1.0") {
			t.Errorf("frozen run %d did not rebuild the crate's own code:\n%s", i, out)
		}
		if strings.Contains(out, "Compiling corraldep") {
			t.Errorf("frozen run %d rebuilt the vendored crate: the target directory was not shared\n%s", i, out)
		}
	}
}

// The hazard fact 2 answers: a mutated file older than the warm build is not
// rebuilt, and the run grades the original's object. Measured on cargo 1.90.
func TestACargoMutantOlderThanTheWarmBuildIsGradedAsTheOriginal(t *testing.T) {
	j := cargoCacheJail(t)
	if ok, out, err := j.WarmRun(context.Background(), cargoCacheFiles("1"), cargoTestOffline); err != nil || !ok {
		t.Fatalf("warm run: ok=%v err=%v\n%s", ok, err, out)
	}
	backdated := []string{"sh", "-c", "touch -d @946684800 src/lib.rs && " + strings.Join(cargoTestOffline, " ")}
	if ok, out := cargoRun(t, j, cargoCacheFiles("2"), backdated); !ok {
		t.Fatalf("the backdated mutant failed: the control no longer shows a stale build, so the safety test may not be able to fail\n%s", out)
	}
}

// Fact 2 under concurrency: a frozen run issued while the warming run is
// still building must still be graded on its own code. Its workspace is
// written once it holds the cache, after the warm build, so its files are
// newer than every object that build left.
func TestACargoRunIssuedDuringTheWarmBuildIsGradedOnItsOwnCode(t *testing.T) {
	j := cargoCacheJail(t)
	slowWarm := []string{"sh", "-c", "sleep 2 && " + strings.Join(cargoTestOffline, " ")}
	var wg sync.WaitGroup
	wg.Add(1)
	var warmOK bool
	var warmOut []byte
	var warmErr error
	go func() {
		defer wg.Done()
		warmOK, warmOut, warmErr = j.WarmRun(context.Background(), cargoCacheFiles("1"), slowWarm)
	}()
	// Wait until the warming run holds the cache, then issue a mutant's run.
	deadline := time.Now().Add(30 * time.Second)
	for !j.(bwrapJail).runCache.issuedWarm() {
		if time.Now().After(deadline) {
			t.Fatal("the warming run never took the cache")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	ok, out := cargoRun(t, j, cargoCacheFiles("2"), cargoTestOffline)
	wg.Wait()
	if warmErr != nil || !warmOK {
		t.Fatalf("warm run: ok=%v err=%v\n%s", warmOK, warmErr, warmOut)
	}
	if ok {
		t.Fatalf("a mutant run issued during the warm build passed: it was graded on the warm build of the original\n%s", out)
	}
}

// The unit evidence pass shares the cargo rows' cache among its units
// (lang.EvidenceCacheFor), and Rust's evidence is its coverage command, which
// builds under <CARGO_TARGET_DIR>/corral-coverage. Over one seed, the warming
// run and a frozen run of that command report the same coverage, and the
// frozen run builds nothing: the seed is the one unmutated tree (Warm refuses
// any other), so the warm build of every crate is still good for it.
func TestRustCoverageOverASharedTargetDirReportsTheSameEvidence(t *testing.T) {
	_ = rustJailOrSkip(t)
	backend := overlayJailOrSkip(t)
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("PATH", systemCargoBin+":"+os.Getenv("PATH"))
	rust, _ := lang.ByName("rust")
	inv := lang.Invocation{Argv: cargoTestOffline}
	cache := lang.EvidenceCacheFor(rust, inv)
	if cache == nil || cache.Env != "CARGO_TARGET_DIR" {
		t.Fatalf("the cargo row's evidence cache is %+v, want CARGO_TARGET_DIR", cache)
	}
	cov, ok := rust.(lang.CoverageReporter).CoverageCmd(cargoTestOffline)
	if !ok {
		t.Fatal("no coverage command for cargo test")
	}
	e := NewEnumerator(backend, 3*time.Minute, WithReadOnlyBinds([]DepBind{{Host: corraldepVendor(t), Rel: "vendor"}}))
	files := cargoCacheFiles("1")
	s, err := e.(bwrapJail).Seed(files, cache)
	if err != nil {
		t.Fatalf("Seed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if c := s.Cache(); c.Target == "" {
		t.Skipf("SKIP: no shared cache on this host (%s)", c.Unshared)
	}
	warm, err := s.Warm(context.Background(), files, cov)
	if err != nil || warm.ExitCode != 0 {
		t.Fatalf("warm coverage run: %+v err=%v", warm, err)
	}
	if strings.Contains(warm.Output, "corral-rust-coverage: unavailable") {
		t.Skipf("SKIP: Rust coverage cannot run in this host's jail, so the shared directory's evidence is unexercised: %s", strings.TrimSpace(warm.Output))
	}
	frozen, err := s.EnumerateDetailed(context.Background(), files, cov)
	if err != nil || frozen.ExitCode != 0 {
		t.Fatalf("frozen coverage run: %+v err=%v", frozen, err)
	}
	// The output is the build's and the suite's lines, then the report from
	// its header on; only the report is evidence.
	report := func(out string) string {
		if i := strings.Index(out, "corral-rust-coverage: "); i >= 0 {
			return out[i:]
		}
		return ""
	}
	if !strings.Contains(report(warm.Output), "src/lib.rs") {
		t.Fatalf("the warm run's coverage names no src/lib.rs:\n%s", warm.Output)
	}
	if report(frozen.Output) != report(warm.Output) {
		t.Errorf("the frozen run's coverage differs from the warm run's:\nwarm   %s\nfrozen %s", report(warm.Output), report(frozen.Output))
	}
	if !strings.Contains(warm.Output, "Compiling corraldep") || strings.Contains(frozen.Output, "Compiling") {
		t.Errorf("want the warming run to build and the frozen run to build nothing:\nwarm   %s\nfrozen %s", warm.Output, frozen.Output)
	}
}

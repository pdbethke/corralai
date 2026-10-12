// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/sandbox"
)

// namedIsolator is an Isolator that only carries a backend name, for seams
// that read the name and never run anything.
type namedIsolator string

func (n namedIsolator) Name() string     { return string(n) }
func (n namedIsolator) Preflight() error { return nil }
func (n namedIsolator) Wrap(string, sandbox.Options, []string) ([]string, error) {
	return []string{"false"}, nil
}

// stepIsolator records the Options of every run and, instead of the real
// toolchain, runs script in the run's workspace — standing in for the
// package manager, so the seam is checkable without go, cargo or bwrap.
type stepIsolator struct {
	script string
	got    []sandbox.Options
}

func (s *stepIsolator) Name() string     { return "bwrap" }
func (s *stepIsolator) Preflight() error { return nil }
func (s *stepIsolator) Wrap(_ string, o sandbox.Options, _ []string) ([]string, error) {
	s.got = append(s.got, o)
	return []string{"sh", "-c", s.script}, nil
}

// isolateStagingDirs points os.UserCacheDir and os.TempDir at two separate
// fresh directories, so a test can tell which one staging used.
func isolateStagingDirs(t *testing.T) (cache, tmp string) {
	t.Helper()
	cache, tmp = t.TempDir(), t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("TMPDIR", tmp)
	return cache, tmp
}

func goModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "go.mod"), "module example.com/m\n\ngo 1.22\n")
	mustWrite(t, filepath.Join(dir, "m.go"), "package m\n")
	return dir
}

func crate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "Cargo.toml"), "[package]\nname = \"c\"\nversion = \"0.1.0\"\n")
	mustWrite(t, filepath.Join(dir, "Cargo.lock"), "version = 4\n")
	mustWrite(t, filepath.Join(dir, "src", "lib.rs"), "pub fn f() {}\n")
	return dir
}

// TestEnsureVendoredRefusesOutsideASandbox: vendoring never runs on the host.
// No backend, or backend "none" (which IS the host), refuses with a message
// that says why and what to do — there is no fallback.
func TestEnsureVendoredRefusesOutsideASandbox(t *testing.T) {
	isolateStagingDirs(t)
	repo := goModule(t)
	for _, iso := range []sandbox.Isolator{nil, namedIsolator("none")} {
		_, cleanup, err := ensureVendored("corral certify --local", repo, "go", iso, io.Discard)
		cleanup()
		if err == nil || !strings.Contains(err.Error(), "never on the host") {
			t.Errorf("iso=%v: want a refusal naming the host, got %v", iso, err)
		}
	}
}

// TestEnsureVendoredRunsTheStepInTheSandboxWithNetwork: the step runs
// through the audit's isolator, networked, in a staging copy under corral's
// own cache directory — never os.TempDir — and the seed is that copy.
func TestEnsureVendoredRunsTheStepInTheSandboxWithNetwork(t *testing.T) {
	cache, tmp := isolateStagingDirs(t)
	repo := goModule(t)
	iso := &stepIsolator{script: "mkdir vendor && echo '# vendored' > vendor/modules.txt"}

	seed, cleanup, err := ensureVendored("corral certify --local", repo, "go", iso, io.Discard)
	defer cleanup()
	if err != nil {
		t.Fatalf("ensureVendored: %v", err)
	}
	if len(iso.got) != 1 {
		t.Fatalf("want exactly one sandboxed run, got %d", len(iso.got))
	}
	o := iso.got[0]
	if !o.Network {
		t.Error("the vendoring run must have the network")
	}
	if o.Workspace != seed {
		t.Errorf("Workspace = %q, want the returned seed %q", o.Workspace, seed)
	}
	if want := filepath.Join(cache, "corral", "vendor") + string(filepath.Separator); !strings.HasPrefix(seed, want) {
		t.Errorf("staging %q is not under corral's cache dir %q", seed, want)
	}
	if strings.HasPrefix(seed, tmp) {
		t.Errorf("staging %q is under os.TempDir %q", seed, tmp)
	}
	if _, err := os.Stat(filepath.Join(seed, "vendor", "modules.txt")); err != nil {
		t.Errorf("the step's output is not in the seed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "vendor")); !os.IsNotExist(err) {
		t.Error("the operator's real tree must never be written")
	}
}

// cargoVendorStdout is what `cargo vendor --locked vendor` printed on
// stdout for a crate with one crates-io dependency (cargo 1.90.0).
const cargoVendorStdout = "[source.crates-io]\nreplace-with = \"vendored-sources\"\n\n[source.vendored-sources]\ndirectory = \"vendor\"\n"

func cargoStep() *stepIsolator {
	return &stepIsolator{script: "printf '%s' '" + cargoVendorStdout + "' > .corral-fetch-stdout"}
}

// TestEnsureVendoredAppendsCargoSourceReplacement: the step's stdout lands in
// the config cargo reads, after whatever the operator's config already says.
func TestEnsureVendoredAppendsCargoSourceReplacement(t *testing.T) {
	isolateStagingDirs(t)
	repo := crate(t)
	mustWrite(t, filepath.Join(repo, ".cargo", "config.toml"), "[alias]\nx = \"build\"")

	seed, cleanup, err := ensureVendored("corral certify --local", repo, "rust", cargoStep(), io.Discard)
	defer cleanup()
	if err != nil {
		t.Fatalf("ensureVendored: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(seed, ".cargo", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "[alias]\nx = \"build\"\n" + cargoVendorStdout; string(b) != want {
		t.Errorf("config =\n%s\nwant\n%s", b, want)
	}
	if _, err := os.Stat(filepath.Join(seed, ".corral-fetch-stdout")); !os.IsNotExist(err) {
		t.Error("the stdout redirect file must not reach the seed")
	}
}

// TestEnsureVendoredRefusesAnExistingCratesIoSource (Review Focus 3): a repo
// config that already declares [source.crates-io] — in config.toml or in the
// legacy extensionless file cargo prefers — is refused, naming the file and
// the table, rather than given a duplicate cargo would reject.
func TestEnsureVendoredRefusesAnExistingCratesIoSource(t *testing.T) {
	isolateStagingDirs(t)
	for _, name := range []string{"config.toml", "config"} {
		repo := crate(t)
		mustWrite(t, filepath.Join(repo, ".cargo", name), "[source.crates-io]\nreplace-with = \"mirror\"\n\n[source.mirror]\nregistry = \"sparse+https://mirror.example/\"\n")
		_, cleanup, err := ensureVendored("corral certify --local", repo, "rust", cargoStep(), io.Discard)
		cleanup()
		if err == nil || !strings.Contains(err.Error(), ".cargo/"+name+" already declares [source.crates-io]") {
			t.Errorf(".cargo/%s: want a refusal naming the file and [source.crates-io], got %v", name, err)
		}
	}
}

// TestVendoringIgnoresPlantedParentConfig proves the JAIL wall against
// configuration walk-up, end to end with bwrap and the real `go mod vendor`:
// a go.work planted in os.TempDir and another in the parent of corral's cache
// dir (the staging root's own ancestor, which a host-side run WOULD find) are
// not seen, because the jail mounts none of the staging path's host parents.
// It cannot tell the two walls apart — the jail alone would pass it even with
// staging back under TMPDIR — so the second wall, the staging location, is
// pinned separately by TestEnsureVendoredRunsTheStepInTheSandboxWithNetwork.
// The failure it guards was measured on the old host path: a go.work in the
// temp dir made `go mod vendor` refuse ("cannot be run in workspace mode"). A
// module with no requirements needs no network, so the test does not depend
// on a module proxy.
func TestVendoringIgnoresPlantedParentConfig(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("SKIP: go not on PATH")
	}
	iso, err := sandbox.Resolve(sandbox.Config{Backend: "bwrap"})
	if err != nil {
		t.Skipf("SKIP: bwrap unavailable: %v", err)
	}
	cache, tmp := isolateStagingDirs(t)
	work := "go 1.22\n\nuse ./elsewhere\n"
	mustWrite(t, filepath.Join(tmp, "go.work"), work)
	mustWrite(t, filepath.Join(cache, "go.work"), work)
	mustWrite(t, filepath.Join(cache, ".cargo", "config.toml"), "[net]\ngit-fetch-with-cli = true\n")

	_, cleanup, err := ensureVendored("corral certify --local", goModule(t), "go", iso, io.Discard)
	defer cleanup()
	if err != nil {
		t.Fatalf("a planted parent go.work reached the vendoring step: %v", err)
	}
}

// TestEnsureVendoredRefusesTheContainerBackend: measured with rootful docker
// under container.go's flags, container root (no CAP_DAC_OVERRIDE) can
// neither read the 0700 staging copy nor create vendor/ in it, so the step
// failed with a shell permission error that read as a broken module. It is
// refused up front instead, saying what to do.
func TestEnsureVendoredRefusesTheContainerBackend(t *testing.T) {
	isolateStagingDirs(t)
	_, cleanup, err := ensureVendored("corral certify --local", goModule(t), "go", namedIsolator("container"), io.Discard)
	cleanup()
	if err == nil || !strings.Contains(err.Error(), "container backend") || !strings.Contains(err.Error(), "--jail bwrap") {
		t.Fatalf("want an up-front refusal naming the container backend and --jail bwrap, got %v", err)
	}
}

// SPDX-License-Identifier: Elastic-2.0

//go:build linux

package sandbox

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestBwrapWrapNetOffByDefault(t *testing.T) {
	argv, err := (bwrapIsolator{}).Wrap("echo hi", Options{Workspace: "/workspace"}, []string{"PATH=/usr/bin"})
	if err != nil {
		t.Fatal(err)
	}
	if !argvHas(argv, "--unshare-all") {
		t.Fatal("expected --unshare-all (net off)")
	}
	if argvHas(argv, "--share-net") {
		t.Fatal("did not expect --share-net when Network is false")
	}
	if !argvHas(argv, "--setenv", "PATH", "/usr/bin") {
		t.Fatal("expected env passed via --setenv")
	}
	if !argvHas(argv, "--bind", "/workspace", "/workspace") || !argvHas(argv, "--chdir", "/workspace") {
		t.Fatal("expected the workspace bound + chdir")
	}
	if !argvHas(argv, "--", "sh", "-c", rlimitPrelude+"echo hi") {
		t.Fatal("expected the command (with rlimit prelude) after --")
	}
	if !argvHas(argv, "--clearenv") {
		t.Fatal("expected --clearenv (the command must inherit no host env)")
	}
	if argvHas(argv, "--ro-bind-try", "/etc/resolv.conf", "/etc/resolv.conf") {
		t.Fatal("resolv.conf should not be bound when net is off")
	}
	// Loopback name resolution must work even with the net unshared: without
	// /etc/hosts, resolving "localhost" fails with EAI_AGAIN and any test runner
	// that coordinates workers over loopback (vitest, jest) dies before running a
	// single test — surfacing as COULD-NOT-GRADE, which reads like a broken
	// project rather than a jail gap. This binds names, not reachability.
	if !argvHas(argv, "--ro-bind-try", "/etc/hosts", "/etc/hosts") {
		t.Fatal("expected /etc/hosts bound so 'localhost' resolves inside the jail")
	}
	if !argvHas(argv, "--ro-bind-try", "/etc/nsswitch.conf", "/etc/nsswitch.conf") {
		t.Fatal("expected /etc/nsswitch.conf bound so the hosts file is consulted")
	}
	// The passwd database, same shape as the two above: a system database the
	// RUNTIME expects, whose absence fails at IMPORT rather than in a test.
	// Without it the jail's uid has no passwd entry and getpass.getuser() raises
	// `KeyError: getpwuid(): uid not found: 1000`. PyTorch calls exactly that
	// while computing its cache directory, at import — so any Python project
	// transitively importing torch or transformers dies before collection and
	// reports COULD-NOT-GRADE, indistinguishable from a broken project. Found by
	// auditing a third-party FastAPI service whose conftest imports the app.
	if !argvHas(argv, "--ro-bind-try", "/etc/passwd", "/etc/passwd") {
		t.Fatal("expected /etc/passwd bound so getpwuid()/getpass.getuser() resolve inside the jail")
	}
	if !argvHas(argv, "--ro-bind-try", "/etc/group", "/etc/group") {
		t.Fatal("expected /etc/group bound so gid lookups resolve alongside passwd")
	}
	// The hashes live in /etc/shadow and nothing a test runner does needs them.
	// Binding it would be a real leak, so this pins the absence rather than
	// leaving it to reviewer vigilance.
	if argvHas(argv, "--ro-bind-try", "/etc/shadow", "/etc/shadow") {
		t.Fatal("/etc/shadow must NEVER be bound into the jail")
	}
	// Debian/Ubuntu put installed gems in /var/lib/gems, OUTSIDE /usr. Without
	// this, `gem install rspec` is invisible in the jail while the executable in
	// /usr/local/bin is not, producing "can't find gem rspec-core ... with
	// executable rspec" — the whole RubyGems ecosystem, unusable. It hid because
	// Ruby had only been exercised against minitest, a default gem under /usr.
	if !argvHas(argv, "--ro-bind-try", "/var/lib/gems", "/var/lib/gems") {
		t.Fatal("expected /var/lib/gems bound so Debian-installed gems are visible in the jail")
	}
	if !argvHas(argv, "--setenv", "HOME", "/home/agent") {
		t.Fatal("expected a writable HOME set inside the jail")
	}
	if !argvHas(argv, "--tmpfs", "/home/agent") {
		t.Fatal("expected a tmpfs backing the jail HOME")
	}
}

func TestBwrapWrapNetOn(t *testing.T) {
	argv, err := (bwrapIsolator{}).Wrap("go build", Options{Workspace: "/w", Network: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !argvHas(argv, "--share-net") {
		t.Fatal("expected --share-net when Network is true")
	}
	if !argvHas(argv, "--ro-bind-try", "/etc/resolv.conf", "/etc/resolv.conf") {
		t.Fatal("expected resolv.conf bound for DNS when networked")
	}
}

func TestBwrapWrapRequiresWorkspace(t *testing.T) {
	if _, err := (bwrapIsolator{}).Wrap("x", Options{}, nil); err == nil {
		t.Fatal("expected an error when no workspace is set")
	}
}

func TestBwrapWrapRlimitPrelude(t *testing.T) {
	argv, err := (bwrapIsolator{}).Wrap("echo hi", Options{Workspace: "/w"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if last := argv[len(argv)-1]; last != rlimitPrelude+"echo hi" {
		t.Fatalf("final argv element = %q, want rlimitPrelude+%q", last, "echo hi")
	}
}

func TestBwrapWrapDropsHostHome(t *testing.T) {
	argv, err := (bwrapIsolator{}).Wrap("true", Options{Workspace: "/w"}, []string{"HOME=/root", "PATH=/usr/bin"})
	if err != nil {
		t.Fatal(err)
	}
	if argvHas(argv, "--setenv", "HOME", "/root") {
		t.Fatal("host HOME must not be forwarded into the jail")
	}
	if !argvHas(argv, "--setenv", "HOME", "/home/agent") {
		t.Fatal("jail HOME should be /home/agent")
	}
}

// TestBwrapWrapPinsGoToolchainLocal pins Bug A's fix: the offline jail forces
// GOTOOLCHAIN=local so `go` cannot attempt a network toolchain download, and the
// host's own GOTOOLCHAIN never leaks through to override it.
func TestBwrapWrapPinsGoToolchainLocal(t *testing.T) {
	argv, err := (bwrapIsolator{}).Wrap("go test ./...", Options{Workspace: "/w"}, []string{"GOTOOLCHAIN=auto", "PATH=/usr/bin"})
	if err != nil {
		t.Fatal(err)
	}
	if !argvHas(argv, "--setenv", "GOTOOLCHAIN", "local") {
		t.Fatal("offline jail must pin GOTOOLCHAIN=local")
	}
	if argvHas(argv, "--setenv", "GOTOOLCHAIN", "auto") {
		t.Fatal("host GOTOOLCHAIN=auto must not leak into the jail")
	}
}

// TestBwrapWrapReadsPinnedEnv: every pinnedEnv row is set exactly once, no host
// value for a pinned key survives, and (unlike the container) PATH is forwarded.
func TestBwrapWrapReadsPinnedEnv(t *testing.T) {
	argv, err := (bwrapIsolator{}).Wrap("true", Options{Workspace: "/w"}, hostileHostEnv())
	if err != nil {
		t.Fatal(err)
	}
	got := envSettings(argv, "--setenv")
	assertPinsHold(t, got)
	if !slices.Equal(got["KEEP"], []string{"1"}) {
		t.Errorf("KEEP = %q, want forwarded once as 1", got["KEEP"])
	}
	if !slices.Equal(got["PATH"], []string{"/host/bin"}) {
		t.Errorf("PATH = %q, bwrap forwards the host PATH", got["PATH"])
	}
}

func countPair(argv []string, a, b string) int {
	n := 0
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == a && argv[i+1] == b {
			n++
		}
	}
	return n
}

// A PerEntry dependency bind is one copy-on-write overlay when the bwrap can
// overlay, in place of one --ro-bind per top-level entry: REA's node_modules
// was 381 binds and 2.4 s of jail start per run (median, n=7 replays) against
// 0.04 s as one overlay. Without overlay support it stays today's per-entry
// binds, unchanged.
func TestBwrapArgvPerEntryBecomesOneOverlay(t *testing.T) {
	host := t.TempDir()
	for _, d := range []string{"a", "b", "c"} {
		if err := os.MkdirAll(filepath.Join(host, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	opts := Options{Workspace: "/w", ReadOnlyBinds: []Bind{{Host: host, Target: "/w/node_modules", PerEntry: true}}}

	argv, err := overlayIso(func(string) bool { return true }).Wrap("true", opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n := countPair(argv, "--overlay-src", host); n != 1 {
		t.Errorf("--overlay-src %s appears %d times, want 1: %v", host, n, argv)
	}
	if n := countPair(argv, "--tmp-overlay", "/w/node_modules"); n != 1 {
		t.Errorf("--tmp-overlay /w/node_modules appears %d times, want 1: %v", n, argv)
	}
	for i, a := range argv {
		if a == "--ro-bind" && strings.HasPrefix(argv[i+1], host) {
			t.Errorf("a per-entry --ro-bind survived the overlay: %v", argv[i:i+3])
		}
	}
	// The overlay's mountpoint is inside the workspace, so it must follow
	// the workspace mount, for the reason the per-entry binds must.
	ws := slices.Index(argv, "--bind")
	ov := slices.Index(argv, "--overlay-src")
	if ws < 0 || ov < ws {
		t.Errorf("the dependency overlay (at %d) must follow the workspace bind (at %d)", ov, ws)
	}

	argv, err = (bwrapIsolator{}).Wrap("true", opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	ro := 0
	for i, a := range argv {
		if a == "--ro-bind" && strings.HasPrefix(argv[i+1], host) {
			ro++
		}
	}
	if ro != 3 || argvHas(argv, "--overlay-src") {
		t.Errorf("without overlay: %d per-entry --ro-binds (want 3) and overlay=%v: %v", ro, argvHas(argv, "--overlay-src"), argv)
	}
}

// A seeded workspace is a copy-on-write overlay of the seed at the seed's
// own path: every path the run prints (coverage reports above all) is what
// a fresh copy at that path would print, and nothing the run writes reaches
// the seed.
func TestBwrapArgvSeedLower(t *testing.T) {
	const s = "/tmp/corral-seed"
	opts := Options{Workspace: s, SeedLower: s,
		ReadOnlyBinds: []Bind{{Host: t.TempDir(), Target: s + "/node_modules", PerEntry: true}}}
	argv, err := overlayIso(func(string) bool { return true }).Wrap("true", opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !argvHas(argv, "--overlay-src", s, "--tmp-overlay", s, "--chdir", s) {
		t.Errorf("want --overlay-src S --tmp-overlay S --chdir S: %v", argv)
	}
	if argvHas(argv, "--bind", s, s) {
		t.Errorf("a seeded workspace must not be bound writable: %v", argv)
	}
	seed := slices.Index(argv, "--tmp-overlay")
	dep := -1
	for i := range argv {
		if argv[i] == "--tmp-overlay" && i+1 < len(argv) && argv[i+1] == s+"/node_modules" {
			dep = i
		}
	}
	if seed < 0 || dep < seed {
		t.Errorf("the dependency overlay (at %d) must follow the seed overlay (at %d): %v", dep, seed, argv)
	}

	// Never a writable bind of the seed in place of an overlay.
	if _, err := (bwrapIsolator{}).Wrap("true", opts, nil); err == nil {
		t.Error("a bwrap that cannot overlay wrapped a seeded workspace")
	}
	// The seed is the workspace, at its own path; anything else is refused.
	if _, err := overlayIso(func(string) bool { return true }).Wrap("true", Options{Workspace: "/elsewhere", SeedLower: s}, nil); err == nil {
		t.Error("a seed at a path other than the workspace was wrapped")
	}
}

// The overlay probe says no for a bwrap that refuses --tmp-overlay: a bwrap
// older than 0.10 (Ubuntu 24.04 ships 0.9) keeps today's per-entry binds.
func TestBwrapOverlayProbeFailsClosed(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "bwrap"), []byte("#!/bin/sh\necho 'bwrap: Unknown option --overlay-src' >&2\nexit 1\n"), 0o755); err != nil { // #nosec G306 -- a test's fake executable
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	if probeOverlay() {
		t.Error("probeOverlay() = true for a bwrap that refuses --overlay-src")
	}
}

// overlayIso is a bwrap that overlays, whose per-filesystem lower probe is
// probe (the real one runs bwrap).
func overlayIso(probe func(string) bool) bwrapIsolator {
	return bwrapIsolator{overlay: true, lowers: newLowerCache(probe)}
}

// An overlaid dependency directory gets every top-level dot-DIRECTORY but
// .bin and .pnpm masked by an empty tmpfs, after the overlay: a host cache
// (.vite holds vitest's results) never reaches a graded run, as under the
// per-entry binds. Dot-files, .bin, .pnpm and a symlinked dot-entry are
// not masked.
func TestBwrapArgvOverlayMasksCacheDirs(t *testing.T) {
	host := t.TempDir()
	for _, d := range []string{"pkg", ".bin", ".pnpm", ".vite", ".cache"} {
		if err := os.MkdirAll(filepath.Join(host, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(host, ".package-lock.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(host, "pkg"), filepath.Join(host, ".linked")); err != nil {
		t.Fatal(err)
	}
	opts := Options{Workspace: "/w", ReadOnlyBinds: []Bind{{Host: host, Target: "/w/node_modules", PerEntry: true}}}
	argv, err := overlayIso(func(string) bool { return true }).Wrap("true", opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	ov := -1
	for i := range argv {
		if argv[i] == "--tmp-overlay" && argv[i+1] == "/w/node_modules" {
			ov = i
		}
	}
	masked := map[string]int{}
	for i := range argv {
		if argv[i] == "--tmpfs" && strings.HasPrefix(argv[i+1], "/w/node_modules/") {
			masked[strings.TrimPrefix(argv[i+1], "/w/node_modules/")] = i
		}
	}
	for _, want := range []string{".vite", ".cache"} {
		i, ok := masked[want]
		if !ok || ov < 0 || i < ov {
			t.Errorf("%s: masked at %d (ok=%v), overlay at %d: want a --tmpfs after the overlay", want, i, ok, ov)
		}
	}
	for _, not := range []string{".bin", ".pnpm", ".package-lock.json", ".linked", "pkg"} {
		if _, ok := masked[not]; ok {
			t.Errorf("%s was masked", not)
		}
	}
}

// A dependency directory on a filesystem overlayfs refuses as a lower keeps
// its per-entry binds, while the bwrap overlays everything else.
func TestBwrapArgvDependencyOnARefusingFilesystemStaysPerEntry(t *testing.T) {
	host := t.TempDir()
	for _, d := range []string{"a", "b"} {
		if err := os.MkdirAll(filepath.Join(host, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	opts := Options{Workspace: "/w", ReadOnlyBinds: []Bind{{Host: host, Target: "/w/node_modules", PerEntry: true}}}
	iso := overlayIso(func(string) bool { return false })
	argv, err := iso.Wrap("true", opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if argvHas(argv, "--overlay-src", host) || !argvHas(argv, "--ro-bind", filepath.Join(host, "a"), "/w/node_modules/a") {
		t.Errorf("want per-entry binds and no overlay: %v", argv)
	}
	if OverlaysDir(iso, host) {
		t.Error("OverlaysDir = true for a refusing filesystem")
	}
	// An overlaying isolator without its lower probe is a construction bug.
	if _, err := (bwrapIsolator{overlay: true}).Wrap("true", opts, nil); err == nil {
		t.Error("an overlaying isolator with no lower probe wrapped a run")
	}
}

// A dependency directory the isolator cannot read is not overlaid, and
// OverlaysDir says so: Wrap needs the directory's listing for its cache
// masks, and the deps line reads OverlaysDir, so the disclosure and the
// mount must come from the same decision. (Before, OverlaysDir said
// copy-on-write while Wrap fell back to per-entry binds.)
func TestBwrapUnreadableDependencyDirIsNotOverlaid(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads a directory whatever its mode")
	}
	host := filepath.Join(t.TempDir(), "node_modules")
	if err := os.Mkdir(host, 0o300); err != nil { // #nosec G301 -- deliberately unreadable
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(host, 0o700) }) // #nosec G302 -- let TempDir clean up
	iso := overlayIso(func(string) bool { return true })
	if OverlaysDir(iso, host) {
		t.Error("OverlaysDir = true for a directory that cannot be read")
	}
	opts := Options{Workspace: "/w", ReadOnlyBinds: []Bind{{Host: host, Target: "/w/node_modules", PerEntry: true}}}
	argv, err := iso.Wrap("true", opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if argvHas(argv, "--overlay-src", host) {
		t.Errorf("an unreadable directory was overlaid: %v", argv)
	}
}

// The lower probe runs once per filesystem (st_dev), however many
// directories on it ask.
func TestLowerCacheProbesOncePerFilesystem(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	for _, d := range []string{a, b} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	c := newLowerCache(func(string) bool { calls++; return true })
	for range 3 {
		if !c.overlays(a) || !c.overlays(b) {
			t.Fatal("overlays = false")
		}
	}
	if calls != 1 {
		t.Errorf("probe ran %d times for one filesystem, want 1", calls)
	}
	if c.overlays(filepath.Join(root, "missing")) {
		t.Error("a directory that does not exist overlays")
	}
}

// The real lower probe says no for a bwrap that refuses the overlay, and
// for one that hangs, within its timeout.
func TestBwrapLowerProbeFailsClosed(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct{ name, script string }{
		{"refuses", "#!/bin/sh\necho 'bwrap: Unknown option --overlay-src' >&2\nexit 1\n"},
		{"hangs", "#!/bin/sh\nexec sleep 60\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			bin := t.TempDir()
			if err := os.WriteFile(filepath.Join(bin, "bwrap"), []byte(c.script), 0o755); err != nil { // #nosec G306 -- a test's fake executable
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+":/usr/bin:/bin")
			old := probeTimeout
			probeTimeout = 500 * time.Millisecond
			t.Cleanup(func() { probeTimeout = old })
			start := time.Now()
			if probeLower(dir) {
				t.Error("probeLower = true")
			}
			if probeOverlay() {
				t.Error("probeOverlay = true")
			}
			if d := time.Since(start); d > 5*time.Second {
				t.Errorf("the probes took %s", d)
			}
		})
	}
}

// A shared tool cache is mounted at its fixed in-jail path in one of two
// shapes: frozen, a copy-on-write overlay of the host cache whose writes the
// run discards, or (the one warming run) bound writable. Either way the
// jail's Env points at it, once, whatever the host env said, and the mount
// follows the workspace and every dependency mount, as they follow the
// workspace.
func TestBwrapArgvCacheFrozenAndWarm(t *testing.T) {
	host := t.TempDir()
	dep := t.TempDir()
	base := Options{Workspace: "/w", ReadOnlyBinds: []Bind{{Host: dep, Target: "/w/node_modules", PerEntry: true}}}
	env := []string{"GOCACHE=/host/.cache/go-build", "KEEP=1"}

	frozen := base
	frozen.Cache = &CacheMount{Host: host, Target: CacheTarget, Env: "GOCACHE", Frozen: true}
	argv, err := overlayIso(func(string) bool { return true }).Wrap("true", frozen, env)
	if err != nil {
		t.Fatal(err)
	}
	if !argvHas(argv, "--overlay-src", host, "--tmp-overlay", "/corral-cache") {
		t.Errorf("frozen: want --overlay-src HOST --tmp-overlay /corral-cache: %v", argv)
	}
	if argvHas(argv, "--bind", host, "/corral-cache") {
		t.Errorf("frozen: the cache is bound writable: %v", argv)
	}
	if got := envSettings(argv, "--setenv")["GOCACHE"]; !slices.Equal(got, []string{"/corral-cache"}) {
		t.Errorf("frozen: GOCACHE = %q, want set once to /corral-cache", got)
	}
	cache := slices.Index(argv, host)
	dependency := slices.Index(argv, "/w/node_modules")
	if cache < 0 || dependency < 0 || cache < dependency {
		t.Errorf("the cache mount (at %d) must follow the dependency overlay (at %d): %v", cache, dependency, argv)
	}

	warm := base
	warm.Cache = &CacheMount{Host: host, Target: CacheTarget, Env: "GOCACHE"}
	argv, err = overlayIso(func(string) bool { return true }).Wrap("true", warm, env)
	if err != nil {
		t.Fatal(err)
	}
	if !argvHas(argv, "--bind", host, "/corral-cache") || argvHas(argv, "--overlay-src", host) {
		t.Errorf("warm: want --bind HOST /corral-cache and no overlay of it: %v", argv)
	}
	if got := envSettings(argv, "--setenv")["GOCACHE"]; !slices.Equal(got, []string{"/corral-cache"}) {
		t.Errorf("warm: GOCACHE = %q, want set once to /corral-cache", got)
	}

	// A cache with no Env is mounted and no variable is set for it.
	argv, err = overlayIso(func(string) bool { return true }).Wrap("true", Options{Workspace: "/w", Cache: &CacheMount{Host: host, Target: CacheTarget, Frozen: true}}, env)
	if err != nil {
		t.Fatal(err)
	}
	if got := envSettings(argv, "--setenv")["GOCACHE"]; !slices.Equal(got, []string{"/host/.cache/go-build"}) {
		t.Errorf("no Env: GOCACHE = %q, want the host's, untouched", got)
	}

	// Frozen is never quietly a writable bind: a bwrap that cannot
	// overlay, or a cache on a filesystem that cannot be a lower layer, is
	// refused.
	if _, err := (bwrapIsolator{}).Wrap("true", frozen, nil); err == nil {
		t.Error("a bwrap that cannot overlay wrapped a frozen cache")
	}
	if _, err := overlayIso(func(string) bool { return false }).Wrap("true", frozen, nil); err == nil {
		t.Error("a frozen cache on a filesystem that cannot be overlaid was wrapped")
	}
}

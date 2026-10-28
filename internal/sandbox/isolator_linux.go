// SPDX-License-Identifier: Elastic-2.0

//go:build linux

package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// rlimitPrelude is prepended to every bwrap command as a coarse fork-bomb and
// disk-write guard. The 2>/dev/null is intentional: the sh inside the jail may
// lack /proc/sys support; we tolerate failures silently. These are best-effort
// limits — the container/cgroup is responsible for hard enforcement.
const rlimitPrelude = "ulimit -u 1024 2>/dev/null; ulimit -f 4194304 2>/dev/null; "

// --- bwrap: Linux unprivileged namespace jail (default) ---

type bwrapIsolator struct {
	// overlay is whether this host's bwrap mounts a --tmp-overlay (bwrap
	// 0.10 and later, on a kernel that allows overlayfs in an unprivileged
	// user namespace), set once by probeOverlay when the isolator is built.
	// It decides two things: a PerEntry dependency bind becomes ONE
	// copy-on-write overlay instead of one --ro-bind per entry, and a
	// seeded workspace (Options.SeedLower) can be run at all.
	overlay bool
	// lowers answers, per filesystem, whether a dependency directory there
	// can be an overlay's lower layer. The probe above proves overlayfs on
	// the temp dir's filesystem only, and a repository's node_modules can sit
	// on one overlayfs refuses as a lower (NFS, FUSE, ecryptfs, another
	// overlayfs); a dependency bind on such a filesystem keeps its per-entry
	// binds. Required whenever overlay is true (Wrap refuses otherwise).
	lowers *lowerCache
}

func (bwrapIsolator) Name() string { return "bwrap" }

func (b bwrapIsolator) overlays() bool { return b.overlay }

// overlaysDir reports whether a PerEntry bind of host is one overlay: the
// bwrap overlays at all, and host's filesystem can be a lower layer.
func (b bwrapIsolator) overlaysDir(host string) bool {
	return b.overlay && b.lowers != nil && b.lowers.overlays(host)
}

// lowerCache is the per-filesystem answer to "can a directory here be an
// overlay's lower layer", keyed by st_dev so each filesystem is probed once
// for the isolator's lifetime, however many runs and binds ask.
type lowerCache struct {
	mu    sync.Mutex
	byDev map[uint64]bool
	probe func(dir string) bool
}

func newLowerCache(probe func(dir string) bool) *lowerCache {
	return &lowerCache{byDev: map[uint64]bool{}, probe: probe}
}

// overlays answers for dir's filesystem. A dir that cannot be stat'ed is
// false and not cached: its bind expands as per-entry binds do, to what can
// be read.
func (c *lowerCache) overlays(dir string) bool {
	fi, err := os.Stat(dir)
	if err != nil {
		return false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	dev := uint64(st.Dev) // #nosec G115 -- a device number, compared for identity only
	c.mu.Lock()
	defer c.mu.Unlock()
	if v, seen := c.byDev[dev]; seen {
		return v
	}
	v := c.probe(dir)
	c.byDev[dev] = v
	return v
}

// cacheDirMasks is the --tmpfs masks laid over an overlaid dependency
// directory: one per top-level dot-DIRECTORY of host, except .bin (the
// executables the test command runs) and .pnpm (pnpm's store, which every
// top-level entry links into). The per-entry binds never showed a
// dot-entry, so a tool cache there (vitest's results under .vite, .cache)
// started empty on every run; masked, it still does, and a graded result
// never depends on what the host's cache holds. Dot-FILES stay visible (a
// lockfile such as .package-lock.json is the tree's own record, not a
// cache). A symlinked dot-entry is not masked: bwrap would follow it.
func cacheDirMasks(host, target string) ([]string, error) {
	ents, err := os.ReadDir(host)
	if err != nil {
		return nil, err
	}
	var argv []string
	for _, e := range ents {
		name := e.Name()
		if !strings.HasPrefix(name, ".") || name == ".bin" || name == ".pnpm" || !e.IsDir() {
			continue
		}
		argv = append(argv, "--tmpfs", path.Join(target, name))
	}
	return argv, nil
}

func (bwrapIsolator) Preflight() error {
	if _, err := exec.LookPath("bwrap"); err != nil {
		return fmt.Errorf("bwrap not found on PATH: %w", err)
	}
	// Prove unprivileged user namespaces actually work — a version check alone
	// passes on kernels that compiled userns out, then every real run fails.
	out, err := exec.Command("bwrap", "--unshare-all", "--die-with-parent", "--new-session", "--ro-bind", "/", "/", "--", "true").CombinedOutput()
	if err != nil {
		return fmt.Errorf("bwrap cannot create a sandbox (user namespaces disabled?): %v: %s", err, bytes.TrimSpace(out))
	}
	return nil
}

func (b bwrapIsolator) Wrap(command string, opts Options, env []string) ([]string, error) {
	if opts.Workspace == "" {
		return nil, errors.New("bwrap: workspace required")
	}
	if b.overlay && b.lowers == nil {
		return nil, errors.New("bwrap: an overlaying isolator built without its per-filesystem probe")
	}
	if c := opts.Cache; c != nil {
		if c.Host == "" || c.Target == "" {
			return nil, errors.New("bwrap: a shared cache needs its host directory and its target")
		}
		if c.Frozen && !b.overlaysDir(c.Host) {
			// Never a writable bind in its place: a frozen cache that
			// some run could write would not be frozen.
			return nil, fmt.Errorf("bwrap: the shared cache %s cannot be overlaid frozen: %w", c.Host, ErrNoOverlay)
		}
	}
	if opts.SeedLower != "" {
		if !b.overlay {
			return nil, fmt.Errorf("bwrap: %w", ErrNoOverlay)
		}
		if opts.SeedLower != opts.Workspace {
			return nil, fmt.Errorf("bwrap: a seed is overlaid at its own path; seed %q is not the workspace %q", opts.SeedLower, opts.Workspace)
		}
	}
	argv := []string{"bwrap",
		"--unshare-all",     // user+pid+ipc+uts+cgroup+net namespaces; no privileged caps
		"--die-with-parent", // killed if the agent dies
		"--new-session",     // detach controlling terminal
		"--clearenv",        // start from nothing; only --setenv below reaches the command
	}
	if opts.Network {
		argv = append(argv, "--share-net") // undo --unshare-all's net isolation
	}
	for _, kv := range env {
		if i := strings.IndexByte(kv, '='); i > 0 {
			if isPinned(kv[:i]) {
				continue // pinned below, not inherited from the host
			}
			if opts.Cache != nil && opts.Cache.Env != "" && kv[:i] == opts.Cache.Env {
				continue // set below, to the shared cache
			}
			argv = append(argv, "--setenv", kv[:i], kv[i+1:])
		}
	}
	// The jail's own env pins (HOME, GOTOOLCHAIN, ...): see pinnedEnv.
	for _, p := range pinsFor(opts.Network) {
		argv = append(argv, "--setenv", p.Key, p.Value)
	}
	if opts.Cache != nil && opts.Cache.Env != "" {
		argv = append(argv, "--setenv", opts.Cache.Env, opts.Cache.Target)
	}
	// Minimal read-only root (usrmerged Linux): the command can't read /home or
	// host secrets, only the toolchain. The workspace is the ONLY writable path.
	argv = append(argv,
		"--ro-bind", "/usr", "/usr",
		"--symlink", "usr/bin", "/bin",
		"--symlink", "usr/sbin", "/sbin",
		"--symlink", "usr/lib", "/lib",
		"--symlink", "usr/lib64", "/lib64",
		"--ro-bind-try", "/etc/ssl", "/etc/ssl",
		"--ro-bind-try", "/etc/ca-certificates", "/etc/ca-certificates",
		// Name resolution for LOOPBACK ONLY. `--unshare-all` gives the command its
		// own network namespace, which still has a working `lo` — but with no
		// /etc/hosts, resolving the literal name "localhost" fails with
		// `EAI_AGAIN`. Test runners that coordinate workers over loopback (vitest
		// and jest both do) then die before a single test runs, and the whole file
		// reports COULD-NOT-GRADE as a "build/environment failure" — indistinguishable
		// from a genuinely broken project. Binding these two files leaks no host
		// state worth having (they map localhost and say "check /etc/hosts first")
		// and grants no reachability: there is still no route off `lo`.
		"--ro-bind-try", "/etc/hosts", "/etc/hosts",
		"--ro-bind-try", "/etc/nsswitch.conf", "/etc/nsswitch.conf",
		// The passwd database, for the same reason and with the same shape as
		// the two lines above: it is a system database the RUNTIME expects to
		// exist, and its absence fails at import time rather than in a test.
		//
		// Without it the jail's uid has no passwd entry, so getpass.getuser()
		// raises `KeyError: getpwuid(): uid not found: 1000`. PyTorch calls
		// exactly that while computing its cache directory, at import — so any
		// Python project that transitively imports torch or transformers dies
		// before collection, and the file reports COULD-NOT-GRADE as a
		// "build/environment failure" indistinguishable from a broken project.
		// Found by auditing a third-party FastAPI service whose conftest imports
		// the app, which imports sentence-transformers.
		//
		// /etc/group comes along because the same lookups reach for it (Python's
		// getpass falls back through the group database on some paths, and Go's
		// os/user resolves gids the same way).
		//
		// This leaks usernames and home paths into the jail and nothing else.
		// /etc/shadow is NOT bound and must never be: the hashes live there, and
		// nothing a test runner does needs them.
		"--ro-bind-try", "/etc/passwd", "/etc/passwd",
		"--ro-bind-try", "/etc/group", "/etc/group",
		// Debian/Ubuntu install RubyGems OUTSIDE /usr — `gem install rspec`
		// lands in /var/lib/gems/<ver>, not /usr/lib/ruby/gems. Binding only
		// /usr therefore makes the entire RubyGems ecosystem invisible in the
		// jail while the executable in /usr/local/bin stays visible, so the
		// failure is the maximally confusing "can't find gem rspec-core (>=
		// 0.a) with executable rspec" — the binary is right there.
		//
		// It hid for a long time because Ruby auditing had only ever been
		// exercised against minitest, a DEFAULT gem shipped under
		// /usr/lib/ruby/gems, which /usr already covered. Every gem a project
		// actually installs was broken.
		//
		// Read-only, and only the gem root: this grants no writable state and
		// no credentials, exactly like the toolchain in /usr it sits beside.
		"--ro-bind-try", "/var/lib/gems", "/var/lib/gems",
		// Debian/Ubuntu configure PHP OUTSIDE /usr too: every extension is
		// enabled by an ini under /etc/php/<ver>/cli/conf.d, and the `php`
		// on PATH is a symlink through /etc/alternatives. With neither
		// bound, php inside the jail loaded "(none)" — 20 built-in modules
		// instead of 54, no dom/mbstring/tokenizer/xml/pcov — and PHPUnit
		// refused to start on every run while the PHP pre-flight (which
		// probes `php -v`, which needs no extension) said it could. Both
		// are configuration, read-only, credential-free.
		"--ro-bind-try", "/etc/php", "/etc/php",
		"--ro-bind-try", "/etc/alternatives", "/etc/alternatives",
		"--proc", "/proc",
		"--dev", "/dev",
		"--tmpfs", "/tmp",
		"--tmpfs", "/home/agent",
	)
	// The workspace mount comes AFTER the tmpfs above so a /tmp workspace
	// survives. A seeded workspace is the seed overlaid at its own path: the
	// run reads the seed and writes into a private tmpfs upper layer, so the
	// seed stays what it was for every other run (Options.SeedLower).
	if opts.SeedLower != "" {
		argv = append(argv, "--overlay-src", opts.SeedLower, "--tmp-overlay", opts.Workspace)
	} else {
		argv = append(argv, "--bind", opts.Workspace, opts.Workspace)
	}
	argv = append(argv, "--chdir", opts.Workspace)
	for _, bnd := range opts.ReadOnlyBinds {
		// A PerEntry bind leaves its target a writable directory a toolchain
		// can create its cache in. With overlay support it is ONE
		// copy-on-write overlay of the whole tree — 381 per-entry binds for
		// REA's node_modules cost 2.4 s of jail start per run (median, n=7
		// replays) against 0.04 s as one overlay — and without it, one
		// --ro-bind per top-level entry. Ordering is load-bearing either
		// way: these come AFTER the workspace mount above, because bwrap
		// creates a mountpoint only when its parent already exists and is
		// writable, and an overlay or bind made before the workspace mount
		// would be hidden under it.
		if bnd.PerEntry && b.overlaysDir(bnd.Host) {
			// The cache masks come after the overlay they cover.
			if masks, err := cacheDirMasks(bnd.Host, bnd.Target); err == nil {
				argv = append(argv, "--overlay-src", bnd.Host, "--tmp-overlay", bnd.Target)
				argv = append(argv, masks...)
				continue
			}
		}
		if bnd.PerEntry {
			for _, e := range perEntryBinds(bnd) {
				argv = append(argv, "--ro-bind", e.Host, e.Target)
			}
			continue
		}
		argv = append(argv, "--ro-bind", bnd.Host, bnd.Target)
	}
	// The shared cache (Options.Cache), at its own path outside the
	// workspace, after the workspace and every dependency mount: the
	// mount order this function keeps is workspace, then dependency
	// overlays or binds with their masks, then the cache. Frozen, it is a
	// copy-on-write overlay whose writes the run discards; warming, the
	// host directory bound writable.
	if c := opts.Cache; c != nil {
		if c.Frozen {
			argv = append(argv, "--overlay-src", c.Host, "--tmp-overlay", c.Target)
		} else {
			argv = append(argv, "--bind", c.Host, c.Target)
		}
	}
	// Cover what of a toolchain root a whole-tree mount (/usr) would
	// otherwise expose — a cargo home's credentials, config and checkouts
	// (see secrets.go). AFTER every bind, so each mask lands on top of the
	// mount that holds its path.
	for _, m := range secretMasks(wholeBoundTrees) {
		if m.Dir {
			argv = append(argv, "--tmpfs", m.Path)
		} else {
			argv = append(argv, "--ro-bind", "/dev/null", m.Path)
		}
	}
	if opts.Network {
		argv = append(argv, "--ro-bind-try", "/etc/resolv.conf", "/etc/resolv.conf")
	}
	return append(argv, "--", "sh", "-c", rlimitPrelude+command), nil
}

// newBwrapIsolator returns a preflighted bwrapIsolator, or an error if bwrap is
// unavailable on this host (missing binary or user namespaces disabled).
func newBwrapIsolator() (Isolator, error) {
	b := bwrapIsolator{}
	if err := b.Preflight(); err != nil {
		return nil, err
	}
	b.overlay = probeOverlay()
	b.lowers = newLowerCache(probeLower)
	return b, nil
}

// probeTimeout bounds each overlay probe: a bwrap that hangs must not hang
// the isolator's construction or a run's argv. A probe that times out is
// a no.
var probeTimeout = 10 * time.Second

// probeLower reports whether dir can be an overlay's lower layer on this
// host: it is mounted read-only as one, at a path in the probe's own tmpfs,
// and listed. Nothing is written to dir's filesystem.
func probeLower(dir string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bwrap", "--unshare-all", "--die-with-parent", "--new-session", // #nosec G204 -- fixed argv; dir is a dependency directory corral already binds
		"--ro-bind", "/", "/", "--tmpfs", "/tmp",
		"--overlay-src", dir, "--tmp-overlay", "/tmp/corral-lower-probe",
		"--", "ls", "/tmp/corral-lower-probe")
	cmd.Stdout = nil
	return cmd.Run() == nil
}

// probeOverlay reports whether this host's bwrap can do what a seeded run
// does: overlay a directory at its own path (after a /tmp tmpfs, as Wrap
// orders it), read the lower through it, and keep a write out of it. Any
// failure is false, and the caller keeps per-entry binds and a fresh copy
// per run: a bwrap older than 0.10 refuses --overlay-src, and a kernel
// can refuse overlayfs in an unprivileged user namespace. It is run once,
// when the isolator is built, never per run.
func probeOverlay() bool {
	dir, err := os.MkdirTemp("", "corral-overlay-probe-*")
	if err != nil {
		return false
	}
	defer os.RemoveAll(dir) // #nosec G104 -- best-effort cleanup of our own probe dir
	if err := os.WriteFile(filepath.Join(dir, "lower"), []byte("x"), 0o600); err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bwrap", "--unshare-all", "--die-with-parent", "--new-session", // #nosec G204 -- fixed argv; dir is our own MkdirTemp
		"--ro-bind", "/", "/", "--tmpfs", "/tmp",
		"--overlay-src", dir, "--tmp-overlay", dir, "--chdir", dir,
		"--", "sh", "-c", "test -f lower && echo y > upper")
	if cmd.Run() != nil {
		return false
	}
	// The write must have stayed in the run's upper layer.
	_, err = os.Stat(filepath.Join(dir, "upper"))
	return os.IsNotExist(err)
}

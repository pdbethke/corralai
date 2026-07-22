// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/pdbethke/corralai/internal/adequacy"
)

// ensureGoVendored makes external Go modules resolvable inside the OFFLINE audit
// jail. The jail has no network by design (the code under audit can't phone
// home), and a repo's external deps live in the operator's module cache, which
// is (a) incomplete for a full build and (b) deliberately NOT mounted into the
// jail. So for a Go module that isn't already vendored, this stages a throwaway
// copy of the repo and runs `go mod vendor` in it (network available HERE, on
// the host, before the jail runs) — the jailed build then resolves everything
// from vendor/ with no network and no access to the operator's cache. The
// operator's real working tree is never modified; the copy is removed by the
// returned cleanup. It's a no-op for non-Go code, a non-module dir, or a repo
// that already carries vendor/ (which loadRepoFiles bind-mounts as-is).
func ensureGoVendored(repoDir, langName string, out io.Writer) (string, func(), error) {
	noop := func() {}
	if langName != "go" {
		return repoDir, noop, nil
	}
	if _, err := os.Stat(filepath.Join(repoDir, "go.mod")); err != nil {
		return repoDir, noop, nil // not a module — nothing to vendor
	}
	if fi, err := os.Stat(filepath.Join(repoDir, "vendor")); err == nil && fi.IsDir() {
		return repoDir, noop, nil // already vendored — the jail bind-mounts it
	}
	tmp, err := os.MkdirTemp("", "corral-vendor-")
	if err != nil {
		return "", noop, fmt.Errorf("corral certify --local: vendor staging dir: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }
	if cerr := copyTreeSkipGit(repoDir, tmp); cerr != nil {
		cleanup()
		return "", noop, fmt.Errorf("corral certify --local: staging repo copy for vendoring: %w", cerr)
	}
	fmt.Fprintln(out, "corral certify --local: vendoring Go dependencies (go mod vendor) so the offline jail can resolve them…")
	cmd := exec.CommandContext(context.Background(), "go", "mod", "vendor")
	cmd.Dir = tmp
	if b, verr := cmd.CombinedOutput(); verr != nil {
		cleanup()
		return "", noop, fmt.Errorf("corral certify --local: `go mod vendor` failed — the offline jail can't resolve this repo's external deps without it: %v\n%s", verr, strings.TrimSpace(string(b)))
	}
	return tmp, cleanup, nil
}

// copyTreeSkipGit recursively copies src into an existing dst, skipping .git and
// any symlink (never following one out of the tree). It stages a throwaway copy
// of a repo for `go mod vendor` so the operator's real working tree is untouched.
// Reads go through os.Root so a symlink can't escape src (gosec G122 / TOCTOU),
// mirroring loadRepoFiles.
func copyTreeSkipGit(src, dst string) error {
	r, err := os.OpenRoot(src)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	return fs.WalkDir(r.FS(), ".", func(rel string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		target := filepath.Join(dst, filepath.FromSlash(rel))
		if d.IsDir() {
			if d.Name() == ".git" {
				return fs.SkipDir
			}
			if rel == "." {
				return nil
			}
			return os.MkdirAll(target, 0o750)
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil // do not follow symlinks out of the tree
		}
		in, oerr := r.Open(rel) // root-scoped: cannot escape src
		if oerr != nil {
			return oerr
		}
		defer func() { _ = in.Close() }()
		out, ferr := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) // #nosec G304 -- target confined to the temp staging dir
		if ferr != nil {
			return ferr
		}
		if _, werr := io.Copy(out, in); werr != nil {
			_ = out.Close()
			return werr
		}
		return out.Close()
	})
}

// stringSlice is a minimal repeatable-string flag.Value (cmd/corral has no
// existing repeatable-flag helper to reuse): each `--bind-dir <path>`
// occurrence appends rather than overwrites, so an operator can pass it more
// than once.
type stringSlice []string

func (s *stringSlice) String() string { return strings.Join(*s, ",") }

func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// buildLoadOpts is the testable flag→loadOpts seam: it carries the resolved
// backend name (the container-fallback rule in shouldBind needs it),
// --bind-dir's accumulated dirs, and --no-bind-deps straight into loadOpts.
func buildLoadOpts(backendName string, bindDirs []string, noBindDeps bool) loadOpts {
	return loadOpts{BackendName: backendName, ExtraBindDir: bindDirs, NoBindDeps: noBindDeps}
}

// loadOpts configures loadRepoFiles' dep-dir detection: which sandbox
// backend will run the jail (the container-fallback rule needs it),
// operator-supplied extra dirs to bind (--bind-dir), and an opt-out that
// forces every dep dir to be copied instead of bound (--no-bind-deps).
type loadOpts struct {
	BackendName  string   // sandbox backend Name(): "bwrap" | "container" | "sandbox-exec" | ...
	ExtraBindDir []string // repo-relative dirs from --bind-dir
	NoBindDeps   bool     // --no-bind-deps: copy dep dirs instead of binding
	rootAbs      string   // absolute root, set internally before the walk
}

// depDirNames are directory basenames auto-detected as dependency trees:
// large, vendor-managed, and irrelevant to the mutant/text seed — binding
// them read-only instead of copying keeps them out of the 64 MiB cap.
var depDirNames = map[string]bool{
	"node_modules": true,
	"vendor":       true,
	".venv":        true,
	"venv":         true,
	".bundle":      true,
}

// shouldBind reports whether the dir at rel (base name name) should be
// bind-mounted read-only rather than copied. Auto-detected dep dirs and
// --bind-dir entries qualify, unless --no-bind-deps is set. The backend must
// then also be able to RELOCATE the dir to the per-run workspace (Target) —
// only bwrap (--ro-bind Host Target) and container (-v Host:Target:ro) can.
// macOS sandbox-exec (and any other/unknown backend) grants file-read* at the
// dir's ORIGINAL host path only, with no relocate primitive, so it must copy
// the dep dir into the seed instead (subject to the 64 MiB cap) — binding it
// would leave the toolchain (cwd = workspace) unable to find it at all.
func shouldBind(rel, name string, opts loadOpts) bool {
	if opts.NoBindDeps {
		return false
	}
	auto := depDirNames[name]
	extra := false
	for _, e := range opts.ExtraBindDir {
		if e == rel {
			extra = true
		}
	}
	if !auto && !extra {
		return false
	}
	switch opts.BackendName {
	case "bwrap":
		return true
	case "container":
		// The container backend maps host uid → a different uid and can't read
		// 0700 trees, and a read-only bind can't be chmod'd — so a
		// non-world-readable dep dir is copied instead (degrade loudly).
		return worldReadableDir(filepath.Join(opts.rootAbs, filepath.FromSlash(rel)))
	default:
		return false // sandbox-exec / none / unknown → copy (may hit the cap)
	}
}

// worldReadableDir reports whether dir is readable+traversable by "other"
// (o+r and o+x) — the bar a bind-mounted dep dir must clear for the
// container backend's different-uid process to read through it.
func worldReadableDir(dir string) bool {
	fi, err := os.Stat(dir) // #nosec G304 -- dir is always root/rel, confined under --repo-dir
	if err != nil {
		return false
	}
	const oRX = 0o005
	return fi.Mode().Perm()&oRX == oRX
}

// loadRepoFiles walks root and returns every regular text file keyed by its
// slash-separated repo-relative path — the seed for --repo-dir's jail
// workspace — plus the dependency dirs (node_modules, vendor, .venv, venv,
// .bundle, and any --bind-dir entries) it detected and excluded from that
// seed so they can be bind-mounted read-only instead of copied. It skips
// .git, files over 1 MiB (data/fixtures, not source), and anything that
// isn't valid UTF-8 (binaries the text-only jail can't carry), and caps the
// total so a huge checkout can't blow up the workspace. The keys are exactly
// the paths a mutant overlay and the project's own test command reference
// (e.g. `more_itertools/recipes.py`, `tests/test_recipes.py`).
func loadRepoFiles(root string, opts loadOpts) (map[string]string, []adequacy.DepBind, error) {
	const maxFile = 1 << 20   // 1 MiB per file
	const maxTotal = 64 << 20 // 64 MiB of text total

	rootAbs, aerr := filepath.Abs(root)
	if aerr != nil {
		return nil, nil, aerr
	}
	opts.rootAbs = rootAbs

	// Validate --bind-dir entries up front (fail-closed): a missing or
	// non-dir entry is a clear error before the walk starts, not a silent
	// no-op discovered later inside the sandbox. Also NORMALIZE each entry to
	// a clean slash path here, once: a non-canonical entry like "./thirdparty"
	// or "thirdparty/" passes this stat check fine but would never equal the
	// walk's already-clean slash `rel` in shouldBind's `e == rel` match, so it
	// would silently produce no bind and no error — the opposite of
	// fail-closed. Store the normalized form back into opts.ExtraBindDir so
	// every later match (in shouldBind) compares clean-to-clean.
	normalized := make([]string, 0, len(opts.ExtraBindDir))
	for _, e := range opts.ExtraBindDir {
		// Reject anything that isn't a clean repo-relative path BEFORE
		// stat'ing it: an absolute path or a `../`-escaping entry would stat
		// fine (it may well exist on the host) but can never match a
		// root-confined walk `rel`, so it would silently produce no bind and
		// no error — the opposite of fail-closed.
		clean := filepath.Clean(filepath.FromSlash(e))
		if filepath.IsAbs(e) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return nil, nil, fmt.Errorf("--bind-dir %s: must be a path inside --repo-dir (not absolute or escaping with ..)", e)
		}
		fi, serr := os.Lstat(filepath.Join(rootAbs, clean))
		if serr != nil {
			return nil, nil, fmt.Errorf("--bind-dir %s: %w", e, serr)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			// A symlinked entry would pass a following os.Stat but the walk
			// skips symlinked dirs — so it would bind nothing, silently. Reject
			// it loudly (and it keeps out-of-repo symlink targets from binding).
			return nil, nil, fmt.Errorf("--bind-dir %s: is a symlink; only a real directory inside --repo-dir can be bound", e)
		}
		if !fi.IsDir() {
			return nil, nil, fmt.Errorf("--bind-dir %s: not a directory", e)
		}
		norm := filepath.ToSlash(clean)
		normalized = append(normalized, norm)
	}
	opts.ExtraBindDir = normalized

	// os.Root confines every open to the repo dir: a symlink pointing outside
	// the tree can't be followed, so a malicious checkout can't smuggle
	// /etc/passwd into the jail workspace (gosec G122 / CWE-367 TOCTOU).
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = r.Close() }()

	files := make(map[string]string)
	var binds []adequacy.DepBind
	var total int64
	walkErr := fs.WalkDir(r.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return fs.SkipDir
			}
			if rel != "." && shouldBind(rel, d.Name(), opts) {
				absHost, aerr := filepath.Abs(filepath.Join(root, filepath.FromSlash(rel)))
				if aerr != nil {
					return aerr
				}
				binds = append(binds, adequacy.DepBind{Host: absHost, Rel: rel})
				return fs.SkipDir // do NOT copy the dep dir into the seed
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil // never follow a symlink out of the repo
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		if info.Size() > maxFile {
			return nil
		}
		f, oerr := r.Open(rel) // root-scoped: cannot escape the repo dir
		if oerr != nil {
			return oerr
		}
		b, rerr := io.ReadAll(f)
		_ = f.Close()
		if rerr != nil {
			return rerr
		}
		if !utf8.Valid(b) {
			return nil // binary — the jail workspace is text-only
		}
		total += int64(len(b))
		if total > maxTotal {
			return fmt.Errorf("repo has more than %d MiB of text — too large to seed the jail workspace", maxTotal>>20)
		}
		files[rel] = string(b) // fs.WalkDir yields slash-separated paths
		return nil
	})
	if walkErr != nil {
		return nil, nil, walkErr
	}
	return files, binds, nil
}

// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// TreeDigest is the content address of a git-tracked checkout at root: the
// sha256 over the sorted (path, digest) pairs of EVERY file the checkout
// contains for audit purposes.
//
// The universe is `git ls-files -z --cached --others --exclude-standard` —
// every tracked file plus every untracked-but-not-ignored one — the SAME
// enumeration internal/adequacy's pool copies into a private tree
// (gitUniverse). This is a deliberate reimplementation, not an import:
// reposcan does not depend on adequacy (the dependency runs the other way —
// see this package's own doc), so the same git call is made here rather
// than reused. Consequences of that universe, stated plainly:
//
//   - A tracked file's content is what is digested. Editing it (even without
//     staging or committing) changes the digest — git ls-files reads the
//     WORKING TREE, not HEAD.
//   - An untracked file NOT covered by .gitignore is INCLUDED: it is part of
//     what a suite run over this checkout would actually see, so a scan that
//     added one without committing it must not get served stale evidence.
//   - A gitignored file (build output, a venv, a cache dir) is EXCLUDED, so
//     churn inside one — which happens on every run of the suite, cache or
//     no cache — never invalidates the key. This is the asymmetry that makes
//     the cache worth having at all: without it, running the instrumented
//     suite once would poison the key for reusing its own evidence.
//   - A tracked SYMLINK is included, and what gets digested is its TARGET
//     STRING (via Lstat + Readlink), never the bytes at the far end of the
//     link. Reading through it would mean this digest — and the suite run it
//     gates — depends on content outside the checkout, and retargeting the
//     link (without touching any byte the link points at) must still count
//     as a change, which following it would miss entirely.
//   - A socket, device or submodule gitlink is skipped, same as gitUniverse:
//     none of them is content a suite run consumes.
//
// Outside a git work tree (or with no git on PATH), there is no authority to
// ask what the checkout "is" without reimplementing ignore-file semantics by
// hand, so TreeDigest returns "" and a nil error. "" is not a valid digest —
// it is the caller's signal to bypass the selection cache entirely rather
// than key it on a guess, and the caller is expected to disclose that
// bypass rather than silently miss forever.
func TreeDigest(root string) (string, error) {
	git, lerr := exec.LookPath("git")
	if lerr != nil {
		return "", nil
	}
	probe := exec.Command(git, "-C", root, "rev-parse", "--is-inside-work-tree") // #nosec G204 -- git via LookPath; root is the operator's own checkout; literal args
	out, perr := probe.Output()
	if perr != nil || strings.TrimSpace(string(out)) != "true" {
		return "", nil
	}

	ls := exec.Command(git, "-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard") // #nosec G204 -- same: LookPath binary, operator's root, literal args
	lsOut, lserr := ls.Output()
	if lserr != nil {
		return "", fmt.Errorf("reposcan: git ls-files in %s: %w", root, lserr)
	}

	seen := make(map[string]bool)
	var paths []string
	for _, rel := range strings.Split(string(lsOut), "\x00") {
		if rel == "" || seen[rel] {
			continue
		}
		seen[rel] = true
		paths = append(paths, rel)
	}
	// Sorted, not git's own order: the digest must not move because git
	// happened to list two paths in a different order on another machine.
	sort.Strings(paths)

	osRoot, oerr := os.OpenRoot(root)
	if oerr != nil {
		return "", fmt.Errorf("reposcan: open root %s: %w", root, oerr)
	}
	defer func() { _ = osRoot.Close() }()

	h := sha256.New()
	for _, p := range paths {
		full := filepath.Join(root, filepath.FromSlash(p))
		fi, serr := os.Lstat(full)
		if serr != nil {
			// Gone between the listing and the stat: the same tolerance
			// gitUniverse gives an untracked file an editor or a test run
			// deleted underneath it.
			continue
		}
		var d string
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			// Readlink, never Open: this must never follow the link to read
			// content outside the checkout (see the doc above). A broken
			// link (target does not exist) still has a target STRING, and
			// Readlink returns it regardless of whether it resolves.
			target, rerr := os.Readlink(full)
			if rerr != nil {
				continue
			}
			sum := sha256.Sum256([]byte(target))
			d = hex.EncodeToString(sum[:])
		case fi.Mode().IsRegular():
			digest, derr := DigestFile(osRoot, p)
			if derr != nil {
				return "", fmt.Errorf("reposcan: digest %s: %w", p, derr)
			}
			d = digest
		default:
			// Socket, device, submodule gitlink: not part of the universe a
			// suite run against this checkout would consume.
			continue
		}
		// Length-prefixed so no two different (path, digest) sets can fold
		// to the same sha256 by concatenation — the same discipline
		// DigestTestSurface and DigestDir already follow.
		fmt.Fprintf(h, "%d:%s|%d:%s|", len(p), p, len(d), d)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

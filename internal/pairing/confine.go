// SPDX-License-Identifier: Elastic-2.0

package pairing

import (
	"path"
	"path/filepath"
	"strings"
)

// insideRoot reports whether p — a path a source's own declaration named
// (Declared), root-relative and slash-separated, or absolute when the source
// path was — stays inside root. It is the ONE place that rule lives: the
// audited repository is untrusted, and a `#[path = "../../outside/x"]` it
// ships would otherwise have corral read a file of the operator's choosing,
// seed it into the jail, show it to the models and, on certify
// --adversarial, upload it as the developer's test (final review I4).
//
//   - A relative p that cleans to "../…" leaves any root, known or not.
//   - With no root (""), a relative p is held to that lexical rule only (it
//     is root-relative by construction), and an absolute p is refused: there
//     is nothing to confine it to.
//   - With a root, p (joined to root when relative) must lie under it, both
//     as spelled and — where the path exists — after symlinks are resolved,
//     so a link inside the repository pointing out of it is outside too.
//
// root is a directory on disk; "." is the working directory, which is what
// certify --local, --adversarial and doctor resolve an operator's --code
// against.
func insideRoot(root, p string) bool {
	disk := filepath.FromSlash(p)
	if !filepath.IsAbs(disk) {
		c := path.Clean(filepath.ToSlash(p))
		if c == ".." || strings.HasPrefix(c, "../") {
			return false
		}
		if root == "" {
			return true
		}
		disk = filepath.Join(root, filepath.FromSlash(c))
	} else if root == "" {
		return false
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	absP, err := filepath.Abs(disk)
	if err != nil || !within(absRoot, absP) {
		return false
	}
	realRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		realRoot = absRoot
	}
	if realP, err := filepath.EvalSymlinks(absP); err == nil && !within(realRoot, realP) {
		return false
	}
	return true
}

// within reports whether p is root or lies under it; both are absolute and
// clean.
func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

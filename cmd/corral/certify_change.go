// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"archive/tar"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// extractCommit resolves ref to a commit sha and materializes that exact
// committed tree into a fresh temp workspace via `git archive` — so the
// checks run against the commit, never the (possibly dirty) working tree.
// The returned cleanup removes the workspace; it is nil when err != nil.
func extractCommit(ref string) (workdir, sha string, cleanup func(), err error) {
	shaOut, err := exec.Command("git", "rev-parse", "--verify", "--quiet", ref+"^{commit}").Output() // #nosec G204 -- fixed "git"; ref is the operator's own certify target
	if err != nil {
		return "", "", nil, fmt.Errorf("resolving %q to a commit (is this a git repo?): %w", ref, err)
	}
	sha = strings.TrimSpace(string(shaOut))
	if sha == "" {
		return "", "", nil, fmt.Errorf("ref %q did not resolve to a commit", ref)
	}

	dir, err := os.MkdirTemp("", "corral-certify-*")
	if err != nil {
		return "", "", nil, fmt.Errorf("creating workspace: %w", err)
	}
	cleanup = func() { _ = os.RemoveAll(dir) }

	cmd := exec.Command("git", "archive", "--format=tar", sha) // #nosec G204 -- fixed "git"; sha is corral's own resolved commit
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cleanup()
		return "", "", nil, fmt.Errorf("git archive: %w", err)
	}
	if err := cmd.Start(); err != nil {
		cleanup()
		return "", "", nil, fmt.Errorf("git archive: %w", err)
	}
	if err := extractTar(stdout, dir); err != nil {
		_ = cmd.Wait()
		cleanup()
		return "", "", nil, fmt.Errorf("extracting archive: %w", err)
	}
	if err := cmd.Wait(); err != nil {
		cleanup()
		return "", "", nil, fmt.Errorf("git archive: %w", err)
	}
	return dir, sha, cleanup, nil
}

// extractTar writes every regular file/dir in the tar stream under dest,
// rejecting any entry whose cleaned path would escape dest (zip-slip guard).
func extractTar(r io.Reader, dest string) error {
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target := filepath.Join(dest, h.Name) // #nosec G305 -- guarded below
		if !strings.HasPrefix(target, filepath.Clean(dest)+string(os.PathSeparator)) && target != filepath.Clean(dest) {
			return fmt.Errorf("archive entry %q escapes the workspace", h.Name)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o750); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				return err
			}
			mode := os.FileMode(uint32(h.Mode&0o777)) & 0o777                       // #nosec G115 -- masked to 9 perm bits before conversion, cannot overflow
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode) // #nosec G304 -- target is guarded against escape above
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil { // #nosec G110 -- git archive of the repo's own committed tree, not hostile input
				_ = f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		}
	}
}

// SPDX-License-Identifier: Elastic-2.0

// internal/repo/read.go
package repo

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const maxReadBytes = 256 * 1024

// safeJoin resolves rel under dir, rejecting absolute paths and any .. escape.
func safeJoin(dir, rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("absolute path not allowed: %q", rel)
	}
	full := filepath.Join(dir, rel)
	rp, err := filepath.Abs(full)
	if err != nil {
		return "", err
	}
	base, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if rp != base && !strings.HasPrefix(rp, base+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes the repo: %q", rel)
	}
	return rp, nil
}

func (e *Engine) ReadFile(dir, path string) (string, error) {
	// Don't surface repo internals (.git/config, node_modules, vendor) through the
	// read surface, matching Tree/Grep's skip set.
	clean := filepath.Clean(filepath.ToSlash(path))
	if first := strings.SplitN(clean, "/", 2)[0]; skip(first) {
		return "", fmt.Errorf("path not allowed: %q", path)
	}
	// os.Root confines the read to dir: it refuses absolute paths, ".." escapes,
	// AND symlinks that resolve outside the root — so a hostile cloned repo cannot
	// use a symlink (e.g. link -> /etc/passwd or the host's secrets) to exfiltrate
	// host files through the read surface. This is stronger than the string-only
	// safeJoin check, which a symlink defeats.
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	f, err := root.Open(clean)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxReadBytes))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func skip(name string) bool { return name == ".git" || name == "node_modules" || name == "vendor" }

func (e *Engine) Tree(dir, subdir string) ([]string, error) {
	root, err := safeJoin(dir, subdir)
	if err != nil {
		return nil, err
	}
	var out []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skip(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		out = append(out, rel)
		return nil
	})
	return out, nil
}

func (e *Engine) Grep(dir, query string, k int) ([]string, error) {
	if k <= 0 {
		k = 20
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || len(out) >= k {
			return nil
		}
		if d.IsDir() {
			if skip(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil // skip symlinks/devices — never follow a symlink out of an untrusted repo
		}
		rel, _ := filepath.Rel(dir, p)
		// Read through os.Root so a symlinked path component cannot escape dir.
		rf, err := root.Open(rel)
		if err != nil {
			return nil
		}
		b, err := io.ReadAll(io.LimitReader(rf, maxReadBytes+1))
		_ = rf.Close()
		if err != nil || len(b) > maxReadBytes {
			return nil
		}
		for i, line := range strings.Split(string(b), "\n") {
			if strings.Contains(line, query) {
				out = append(out, fmt.Sprintf("%s:%d: %s", rel, i+1, strings.TrimSpace(line)))
				if len(out) >= k {
					break
				}
			}
		}
		return nil
	})
	return out, nil
}

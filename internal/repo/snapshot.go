// SPDX-License-Identifier: Elastic-2.0

package repo

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// snapshotCap bounds the uncompressed bytes a Snapshot will read (pathological repos).
const snapshotCap = 64 << 20 // 64 MiB

type FileWrite struct {
	Path    string
	Content string
}

// Snapshot returns a gzip'd tar of dir (excluding .git/node_modules/vendor) and a
// path→sha256 manifest. The tar carries no .git, so a bee that expands it never holds
// the credential surface.
func (e *Engine) Snapshot(dir string) ([]byte, map[string]string, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	manifest := map[string]string{}
	var total int64

	// os.Root confines every read to dir; combined with the IsRegular guard below
	// (symlinks/devices skipped) and WalkDir's non-following of symlinked dirs, a
	// hostile repo cannot get host files tar'd into the snapshot via a symlink.
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()

	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skip(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		rf, err := root.Open(rel)
		if err != nil {
			return err
		}
		b, err := io.ReadAll(rf)
		_ = rf.Close()
		if err != nil {
			return err
		}
		total += int64(len(b))
		if total > snapshotCap {
			return fmt.Errorf("snapshot exceeds %d bytes", int64(snapshotCap))
		}
		sum := sha256.Sum256(b)
		manifest[rel] = hex.EncodeToString(sum[:])
		if err := tw.WriteHeader(&tar.Header{Name: rel, Mode: 0o644, Size: int64(len(b))}); err != nil {
			return err
		}
		if _, err := tw.Write(b); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, nil, err
	}
	return buf.Bytes(), manifest, nil
}

// ApplyFiles writes each FileWrite under dir, creating parents. Path escapes
// (../ or absolute) are skipped and absent from the returned applied list; err is
// returned only on a real IO failure. Paths whose first segment is in the skip()
// set (.git, node_modules, vendor) are also rejected — mirroring ReadFile's guard
// so a remote bee cannot overwrite .git/config and trigger a git hook or corrupt
// the credential surface.
func (e *Engine) ApplyFiles(dir string, writes []FileWrite) ([]string, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()

	var applied []string
	for _, w := range writes {
		// Mirror ReadFile's guard: never write into .git / node_modules / vendor.
		clean := filepath.ToSlash(filepath.Clean(w.Path))
		first := clean
		if i := strings.IndexByte(clean, '/'); i >= 0 {
			first = clean[:i]
		}
		if skip(first) {
			continue // never write into protected dirs; leave out of applied
		}
		if err := writeConfined(root, filepath.FromSlash(clean), strings.NewReader(w.Content), 0); err != nil {
			continue // escape (symlink or otherwise) or IO failure — skip, leave out of applied
		}
		applied = append(applied, clean)
	}
	return applied, nil
}

// writeConfined writes from r into relPath INSIDE root. os.Root confines the write
// to the root and REFUSES any symlinked path component (a hostile cloned repo can
// commit `cfg -> /root/.ssh`), so a symlink cannot redirect the write outside dir —
// the write-side counterpart to ReadFile's os.Root read confinement (read.go). The
// string-only safeJoin this replaces on the write path is symlink-defeatable. limit<=0 = unbounded.
func writeConfined(root *os.Root, relPath string, r io.Reader, limit int64) error {
	if d := filepath.Dir(relPath); d != "." && d != "" {
		if err := root.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	f, err := root.OpenFile(relPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if limit > 0 {
		r = io.LimitReader(r, limit)
	}
	_, err = io.Copy(f, r)
	return err
}

// HeadSHA is the working copy's current commit (empty string on an unborn branch).
// An unborn branch (git init, no commits) is expected and returns ("", nil); any
// other failure (git missing, dir gone, corrupt repo) is surfaced as a real error.
func (e *Engine) HeadSHA(ctx context.Context, dir string) (string, error) {
	out, err := e.git(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		// an unborn branch (no commits yet) is expected and not an error
		msg := err.Error()
		if strings.Contains(msg, "ambiguous argument 'HEAD'") ||
			strings.Contains(msg, "unknown revision") ||
			strings.Contains(msg, "does not have any commits") {
			return "", nil
		}
		return "", err // git broken / dir missing — surface it
	}
	return strings.TrimSpace(out), nil
}

// extractTarGz expands a gzip'd tar (from Snapshot) into dir, rejecting path escapes
// (including symlink components, via os.Root).
func extractTarGz(dir string, data []byte) error {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)

	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()

	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if err := writeConfined(root, filepath.Clean(h.Name), tr, snapshotCap); err != nil {
			return err
		}
	}
	return nil
}

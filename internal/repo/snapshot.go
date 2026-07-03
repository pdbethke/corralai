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
		full, err := safeJoin(dir, w.Path)
		if err != nil {
			continue // escape — skip, leave out of applied
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			return applied, err
		}
		if err := os.WriteFile(full, []byte(w.Content), 0o600); err != nil {
			return applied, err
		}
		applied = append(applied, filepath.ToSlash(w.Path))
	}
	return applied, nil
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

// extractTarGz expands a gzip'd tar (from Snapshot) into dir, rejecting path escapes.
func extractTarGz(dir string, data []byte) error {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		full, err := safeJoin(dir, h.Name)
		if err != nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			return err
		}
		f, err := os.OpenFile(full, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600) // #nosec G304 -- path is a server-configured location (db/config/own file), not attacker input
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, io.LimitReader(tr, snapshotCap)); err != nil {
			_ = f.Close()
			return err
		}
		_ = f.Close()
	}
	return nil
}

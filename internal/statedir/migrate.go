// SPDX-License-Identifier: Elastic-2.0

package statedir

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// Move is one table row going from the legacy directory to its new path,
// with whichever database side files exist beside it.
type Move struct {
	File     File
	From, To string
	Sidecars []string // names, e.g. "corralai_coord.sqlite3-wal"
}

// Conflict is a row present at both ends. Migrate never merges and never
// overwrites; the operator decides.
type Conflict struct {
	File             File
	From, To         string
	FromInfo, ToInfo os.FileInfo
}

var rename = os.Rename

var sidecarSuffixes = []string{"-wal", "-shm", ".wal"}

// PlanMigration decides what would move from dir and changes nothing.
// Rows with a per-file override are skipped: the operator already chose
// where that file lives.
func PlanMigration(dir string) ([]Move, []Conflict, error) {
	var moves []Move
	var conflicts []Conflict
	for _, f := range Files() {
		if host.env(f.Override) != "" {
			continue
		}
		from := filepath.Join(dir, f.Name)
		fi, err := os.Lstat(from)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			target, _ := os.Readlink(from)
			return nil, nil, fmt.Errorf("%s is a symlink to %s — moving the link would leave the real file behind; move %s yourself and set %s, or replace the link with the file", from, target, target, f.Override)
		}
		rootDir, err := Dir(f.Root)
		if err != nil {
			return nil, nil, err
		}
		to := filepath.Join(rootDir, f.Name)
		if from == to {
			continue
		}
		if locked, err := lockedByAnother(from); err == nil && locked {
			return nil, nil, fmt.Errorf("%s is open in another process — stop the daemon (systemctl stop corral) before migrating", from)
		}
		if ti, err := os.Lstat(to); err == nil {
			conflicts = append(conflicts, Conflict{File: f, From: from, To: to, FromInfo: fi, ToInfo: ti})
			continue
		}
		m := Move{File: f, From: from, To: to}
		clash := false
		for _, s := range sidecarSuffixes {
			si, err := os.Lstat(from + s)
			if err != nil {
				continue
			}
			if si.Mode()&os.ModeSymlink != 0 {
				return nil, nil, fmt.Errorf("%s is a symlink — move the database and its side files yourself", from+s)
			}
			// A stale side file already at the destination would be silently
			// replaced by rename and then replayed into the wrong database.
			if ti, err := os.Lstat(to + s); err == nil {
				conflicts = append(conflicts, Conflict{File: f, From: from + s, To: to + s, FromInfo: si, ToInfo: ti})
				clash = true
				continue
			}
			m.Sidecars = append(m.Sidecars, f.Name+s)
		}
		if !clash {
			moves = append(moves, m)
		}
	}
	return moves, conflicts, nil
}

// ApplyMigration performs the moves: rename on one filesystem, otherwise
// copy, fsync, verify sha256, remove the source. Modes are preserved;
// destination directories are created 0700.
func ApplyMigration(moves []Move) error {
	for _, m := range moves {
		if err := os.MkdirAll(filepath.Dir(m.To), 0o700); err != nil {
			return err
		}
		pairs := [][2]string{{m.From, m.To}}
		for _, s := range m.Sidecars {
			pairs = append(pairs, [2]string{filepath.Join(filepath.Dir(m.From), s), filepath.Join(filepath.Dir(m.To), s)})
		}
		for _, p := range pairs {
			if err := moveFile(p[0], p[1]); err != nil {
				return fmt.Errorf("moving %s → %s: %w", p[0], p[1], err)
			}
		}
	}
	return nil
}

func moveFile(from, to string) error {
	err := rename(from, to)
	if err == nil || !errors.Is(err, syscall.EXDEV) {
		return err
	}
	return copyVerifyRemove(from, to)
}

func copyVerifyRemove(from, to string) error {
	fi, err := os.Stat(from)
	if err != nil {
		return err
	}
	src, err := os.Open(from) // #nosec G304 -- a statedir table row
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fi.Mode().Perm()) // #nosec G304 -- a statedir table row
	if err != nil {
		return err
	}
	h1 := sha256.New()
	if _, err := io.Copy(io.MultiWriter(dst, h1), src); err != nil {
		dst.Close()
		return err
	}
	if err := dst.Sync(); err != nil {
		dst.Close()
		return err
	}
	if err := dst.Close(); err != nil {
		return err
	}
	h2, err := fileSHA256(to)
	if err != nil {
		return err
	}
	if string(h2) != string(h1.Sum(nil)) {
		_ = os.Remove(to)
		return fmt.Errorf("copy of %s does not match the original; the original is untouched", from)
	}
	return os.Remove(from)
}

func fileSHA256(p string) ([]byte, error) {
	f, err := os.Open(p) // #nosec G304 -- a statedir table row
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

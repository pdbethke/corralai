// SPDX-License-Identifier: Elastic-2.0

package statedir

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
)

// Move is one table row going from the legacy directory to its new path,
// with whichever database side files exist beside it.
type Move struct {
	File     File
	From, To string
	Sidecars []string // names, e.g. "corralai_coord.sqlite3-wal"
	// Adopted are side files already at the destination with none at the
	// source — an interrupted earlier run's, kept only under PlanOptions.Resume
	// and always listed so the operator sees what was taken as theirs.
	Adopted []string
}

// Conflict is something migrate will not decide for the operator: a row
// present at both ends, a destination side file nothing explains, or a
// crashed copy's temp file. Migrate never merges and never overwrites.
// FromInfo is nil when nothing is at From; Reason, when set, says why it is
// a conflict in words the CLI prints as they are.
type Conflict struct {
	File             File
	From, To         string
	FromInfo, ToInfo os.FileInfo
	Reason           string
}

// PlanOptions are the operator's decisions a plan cannot make itself.
type PlanOptions struct {
	// Resume adopts destination side files whose main file is still at the
	// source: the operator says the earlier run that moved them was THIS
	// migration, interrupted. Without it they are conflicts.
	Resume bool
}

var rename = os.Rename

// copyData is the byte-copy step of a cross-filesystem move, a seam so a test
// can fail it midway.
var copyData = io.Copy

// probe reports whether another process holds path open. A seam so a test can
// make the probe itself fail.
var probe = lockedByAnother

var sidecarSuffixes = []string{"-wal", "-shm", ".wal"}

// PlanMigration decides what would move from dir and changes nothing.
// Rows with a per-file override are skipped: the operator already chose
// where that file lives.
func PlanMigration(dir string, opts PlanOptions) ([]Move, []Conflict, error) {
	var moves []Move
	var conflicts []Conflict
	if err := requireOwnedByMe(dir); err != nil {
		return nil, nil, err
	}
	swept := map[string]bool{}
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
		if err := ownedByMe(from, fi); err != nil {
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
		if !swept[rootDir] {
			swept[rootDir] = true
			strays, err := strayTemps(f, rootDir)
			if err != nil {
				return nil, nil, err
			}
			conflicts = append(conflicts, strays...)
		}
		// Fail closed: a probe that could not answer is not "unlocked". Moving a
		// store a running daemon holds open is the one mistake this verb must
		// never make.
		locked, err := probe(from)
		if err != nil {
			return nil, nil, fmt.Errorf("cannot tell whether %s is open in another process (%w) — refusing to move it", from, err)
		}
		if locked {
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
				// Absent at the source and present at the destination: an
				// interrupted earlier run (side files move first) — or a stale
				// file from something else, which rename would then replay
				// into the wrong database. Only the operator knows which.
				ti, terr := os.Lstat(to + s)
				switch {
				case terr != nil:
				case opts.Resume:
					m.Adopted = append(m.Adopted, to+s)
				default:
					conflicts = append(conflicts, Conflict{File: f, From: from + s, To: to + s, ToInfo: ti,
						Reason: fmt.Sprintf("%s exists but %s does not and %s has not moved — if a migration stopped partway, re-run with --resume to adopt it; otherwise remove it", to+s, from+s, from)})
					clash = true
				}
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

// strayTemps reports any .migrate-* file in a destination directory: a copy
// copyVerifyRemove made and a crash kept it from removing. It may be a copy of
// a private key, so it is named for the operator to remove, never swept here.
func strayTemps(f File, dir string) ([]Conflict, error) {
	matches, err := filepath.Glob(filepath.Join(dir, ".migrate-*"))
	if err != nil {
		return nil, err
	}
	var out []Conflict
	for _, p := range matches {
		ti, err := os.Lstat(p)
		if err != nil {
			continue
		}
		out = append(out, Conflict{File: f, To: p, ToInfo: ti,
			Reason: fmt.Sprintf("%s is a temporary copy left by a migration that crashed (it may hold a private key) — check it and remove it, then run again", p)})
	}
	return out, nil
}

// euid is the effective uid ownership is compared with; a seam so a test can
// play another user without root.
var euid = os.Geteuid

// requireOwnedByMe refuses a --from directory another user owns. Run as the
// wrong user (sudo, a service account), migrate moves files the owner's next
// run then cannot find — and the owner's key loader mints a fresh key.
func requireOwnedByMe(dir string) error {
	fi, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return ownedByMe(dir, fi)
}

func ownedByMe(p string, fi os.FileInfo) error {
	uid, ok := ownerUID(fi)
	if !ok {
		return nil
	}
	if me := euid(); uid != me {
		return fmt.Errorf("%s is owned by uid %d but this process runs as uid %d — run `corral state migrate` as the user who owns it, so the files land in that user's state directory", p, uid, me)
	}
	return nil
}

// ApplyMigration performs the moves: rename on one filesystem, otherwise
// copy, fsync, verify sha256, remove the source. Modes are preserved;
// destination directories are created 0700.
//
// Side files move FIRST and the main file LAST. The main file's arrival at the
// new path is the commit point: until it lands, the legacy-only refusal keeps
// firing, so a half-moved database is never opened, and a re-run's plan sees the
// main file still at the source and resumes. It returns the rows that moved
// completely, even when it also returns an error.
func ApplyMigration(moves []Move) ([]Move, error) {
	var done []Move
	for _, m := range moves {
		if err := EnsureParent(m.To); err != nil {
			return done, err
		}
		var pairs [][2]string
		for _, s := range m.Sidecars {
			pairs = append(pairs, [2]string{filepath.Join(filepath.Dir(m.From), s), filepath.Join(filepath.Dir(m.To), s)})
		}
		pairs = append(pairs, [2]string{m.From, m.To})
		for _, p := range pairs {
			if err := moveFile(p[0], p[1]); err != nil {
				return done, fmt.Errorf("moving %s → %s: %w", p[0], p[1], err)
			}
		}
		done = append(done, m)
	}
	return done, nil
}

func moveFile(from, to string) error {
	err := rename(from, to)
	if err == nil {
		return syncDir(filepath.Dir(to))
	}
	if !errors.Is(err, syscall.EXDEV) {
		return err
	}
	return copyVerifyRemove(from, to)
}

// copyVerifyRemove never creates anything at its final name until the bytes
// are verified: a partial file at `to` would satisfy the legacy-only check and
// be used (a 0-byte brain key mints a new identity). The temp file is removed
// on every error path; os.Link fails if `to` appeared meanwhile.
func copyVerifyRemove(from, to string) (err error) {
	fi, err := os.Stat(from)
	if err != nil {
		return err
	}
	src, err := os.Open(from) // #nosec G304 -- a statedir table row
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.CreateTemp(filepath.Dir(to), ".migrate-*") // O_EXCL, 0600
	if err != nil {
		return err
	}
	tmp := dst.Name()
	defer func() {
		if err != nil {
			_ = dst.Close()
			_ = os.Remove(tmp)
		}
	}()
	if err = dst.Chmod(fi.Mode().Perm()); err != nil {
		return err
	}
	h1 := sha256.New()
	if _, err = copyData(io.MultiWriter(dst, h1), src); err != nil {
		return err
	}
	if err = dst.Sync(); err != nil {
		return err
	}
	if err = dst.Close(); err != nil {
		return err
	}
	h2, err := fileSHA256(tmp)
	if err != nil {
		return err
	}
	if string(h2) != string(h1.Sum(nil)) {
		err = fmt.Errorf("copy of %s does not match the original; the original is untouched", from)
		return err
	}
	if err = os.Link(tmp, to); err != nil {
		return err
	}
	_ = os.Remove(tmp)
	if err = syncDir(filepath.Dir(to)); err != nil {
		return err
	}
	return os.Remove(from)
}

// syncDir makes a rename or link durable before the source is given up.
// Windows cannot fsync a directory handle; its rename path fails safely instead.
func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(dir) // #nosec G304 -- a statedir directory
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
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

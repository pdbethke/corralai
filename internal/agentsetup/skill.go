// SPDX-License-Identifier: Elastic-2.0

package agentsetup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// MarkerName is the ownership marker written beside a skill copy. A directory
// holding corral's files and a marker whose hashes still match is "managed":
// setup wrote it and nobody has edited it, so setup may refresh or remove it.
// Anything else in that place belongs to the operator and is never touched.
const MarkerName = ".corral-setup.json"

// Marker records which binary version wrote a skill copy and the sha256 (hex)
// of every file it wrote, keyed by slash-separated path.
type Marker struct {
	Version string            `json:"version"`
	Files   map[string]string `json:"files"`
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// readMarker returns the marker in dir, ok=false when absent or unparseable.
func readMarker(dir string) (Marker, bool) {
	b, err := os.ReadFile(filepath.Join(dir, MarkerName)) // #nosec G304 -- the skill directory chosen by the agent table
	if err != nil {
		return Marker{}, false
	}
	var m Marker
	if json.Unmarshal(b, &m) != nil || m.Files == nil {
		return Marker{}, false
	}
	return m, true
}

// skillState classifies dir. A missing or empty directory is "absent"; a
// symlink (the directory itself) is "refused-symlink" because writing through
// it would land outside the agent's directory; a directory with a valid marker,
// every listed file present with its recorded hash and nothing else in it is
// "managed"; everything else (no marker, an edited or added or removed file, a
// symlink inside) is "unmanaged".
func skillState(dir string) SkillState {
	if dir == "" {
		return "n/a"
	}
	fi, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return "absent"
	}
	if err != nil {
		return "unmanaged"
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return "refused-symlink"
	}
	if !fi.IsDir() {
		return "unmanaged"
	}
	var present []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == dir {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		present = append(present, filepath.ToSlash(rel))
		return nil
	})
	if len(present) == 0 {
		return "absent"
	}
	m, ok := readMarker(dir)
	if !ok {
		return "unmanaged"
	}
	for _, rel := range present {
		if rel == MarkerName {
			continue
		}
		if _, listed := m.Files[rel]; !listed {
			return "unmanaged"
		}
	}
	for rel, want := range m.Files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		fi, err := os.Lstat(p)
		if err != nil || !fi.Mode().IsRegular() {
			return "unmanaged"
		}
		b, err := os.ReadFile(p) // #nosec G304 -- inside the skill directory
		if err != nil || sum(b) != want {
			return "unmanaged"
		}
	}
	return "managed"
}

func validRel(rel string) bool {
	c := path.Clean(rel)
	return c == rel && rel != "." && rel != MarkerName && !path.IsAbs(rel) && rel != ".." && !strings.HasPrefix(rel, "../")
}

// WriteSkill copies files (slash-separated relative path -> content) into dir
// and writes the marker last. It refuses a symlinked dir and any existing copy
// that is not managed; a managed copy is refreshed, and files the new version
// no longer carries are removed. Nothing is written when it refuses.
func WriteSkill(dir, version string, files map[string][]byte) error {
	if dir == "" {
		return errors.New("skill: empty directory")
	}
	for rel := range files {
		if !validRel(rel) {
			return fmt.Errorf("skill: refusing file name %q", rel)
		}
	}
	switch st := skillState(dir); st {
	case "absent", "managed":
	case "refused-symlink":
		return fmt.Errorf("skill: %s is a symlink; refusing to write through it", dir)
	default:
		return fmt.Errorf("skill: %s exists and was not written by corral setup (or was edited); leaving it alone", dir)
	}
	old, _ := readMarker(dir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	m := Marker{Version: version, Files: map[string]string{}}
	names := make([]string, 0, len(files))
	for rel := range files {
		names = append(names, rel)
	}
	sort.Strings(names)
	for _, rel := range names {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			return err
		}
		if err := os.WriteFile(p, files[rel], 0o600); err != nil {
			return err
		}
		m.Files[rel] = sum(files[rel])
	}
	for rel := range old.Files {
		if _, keep := files[rel]; !keep {
			_ = os.Remove(filepath.Join(dir, filepath.FromSlash(rel)))
		}
	}
	pruneEmptyDirs(dir)
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, MarkerName), append(b, '\n'), 0o600)
}

// pruneEmptyDirs removes empty subdirectories of dir (never dir itself).
func pruneEmptyDirs(dir string) {
	var dirs []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && p != dir {
			dirs = append(dirs, p)
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = os.Remove(dirs[i]) // fails, harmlessly, when not empty
	}
}

// RemoveSkill deletes a managed, unedited copy: its listed files, the marker,
// then the directory if nothing else is in it. An absent directory is already
// removed (nil). Any other state is an error naming dir, with nothing deleted.
func RemoveSkill(dir string) error {
	switch st := skillState(dir); st {
	case "absent":
		return nil
	case "managed":
	default:
		return fmt.Errorf("skill: %s is %s, not an unedited copy written by corral setup; leaving it alone", dir, st)
	}
	m, _ := readMarker(dir)
	for rel := range m.Files {
		if err := os.Remove(filepath.Join(dir, filepath.FromSlash(rel))); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	if err := os.Remove(filepath.Join(dir, MarkerName)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	pruneEmptyDirs(dir)
	if err := os.Remove(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("skill: %s still holds files setup did not write: %w", dir, err)
	}
	return nil
}

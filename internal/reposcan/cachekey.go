package reposcan

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
)

// KeyInputs is every input that can change a file's audit verdict. A verdict
// may be reused only when all of these are unchanged.
//
// TestSurfaceDigest is present because adequacy is a joint property of code
// AND tests: a suite can be weakened without the source changing.
// PackageDigest is present because a file's kill rate can move when a helper
// it calls changes. Keying on the file alone UNDER-invalidates, which serves
// stale verdicts; keying on the package OVER-invalidates, which only costs
// money. Fail safe.
type KeyInputs struct {
	SourceDigest      string
	PackageDigest     string
	GoalDigest        string
	TestSurfaceDigest string
	EngineVersion     string
	ModelSet          string
	AuditConfig       string
}

// CacheKey is the content address of a verdict. Fields are length-prefixed so
// no combination of values can collide by concatenation.
func (k KeyInputs) CacheKey() string {
	h := sha256.New()
	for _, f := range []string{
		k.SourceDigest, k.PackageDigest, k.GoalDigest, k.TestSurfaceDigest,
		k.EngineVersion, k.ModelSet, k.AuditConfig,
	} {
		fmt.Fprintf(h, "%d:%s|", len(f), f)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// DigestFile is the sha256 of one file's contents, read through root.
//
// Both halves of that sentence are load-bearing:
//
//   - Through an *os.Root, so a symlink in the scanned checkout cannot make
//     the scan read a file OUTSIDE the repository. The same content is later
//     shipped to a model provider and written into the jail workspace, so
//     following `secrets.py -> ~/.aws/credentials` would be one-hop
//     exfiltration. Non-regular entries (symlinks, FIFOs, devices) are
//     REJECTED rather than read: a FIFO would block the scan forever.
//   - Streamed into the hash, never slurped: a multi-gigabyte fixture must
//     not become a multi-gigabyte allocation.
func DigestFile(root *os.Root, rel string) (string, error) {
	rel = path.Clean(rel)
	fi, err := root.Lstat(rel)
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("reposcan: %s is not a regular file (%s)", rel, fi.Mode().Type())
	}
	f, err := root.Open(rel)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	// Re-check after opening: the lstat above answers "what is on disk", this
	// answers "what did we actually open", and only the second one can be
	// trusted if the tree changed underneath us mid-scan.
	if st, serr := f.Stat(); serr != nil {
		return "", serr
	} else if !st.Mode().IsRegular() {
		return "", fmt.Errorf("reposcan: %s is not a regular file (%s)", rel, st.Mode().Type())
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// DigestDir is the sha256 over every regular file directly in dir, by sorted
// name, hashing name and content both. Subdirectories are not descended:
// "package" means the immediate directory, matching Go/Python/Ruby layout.
//
// dir is relative to root and, like DigestFile, every read is confined to it.
// Non-regular entries are SKIPPED rather than hashed: a symlink's target is
// not this repository's content, and a FIFO would block forever. Contents are
// streamed into the hash so one huge fixture in a package directory cannot be
// slurped into memory once per candidate in that directory.
func DigestDir(root *os.Root, dir string) (string, error) {
	dir = path.Clean(dir)
	entries, err := fs.ReadDir(root.FS(), dir)
	if err != nil {
		return "", err
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	h := sha256.New()
	for _, n := range names {
		rel := path.Join(dir, n)
		f, oerr := root.Open(rel)
		if oerr != nil {
			return "", oerr
		}
		st, serr := f.Stat()
		if serr != nil {
			_ = f.Close()
			return "", serr
		}
		if !st.Mode().IsRegular() {
			// Changed underneath us between ReadDir and Open. Fail closed
			// rather than hash whatever it became.
			_ = f.Close()
			return "", fmt.Errorf("reposcan: %s is not a regular file (%s)", rel, st.Mode().Type())
		}
		// Length-prefix name AND content so no pair of (name, content) values
		// can collide with a differently-split pair. The content length is
		// written AFTER the bytes because streaming means the true count is
		// only known once the copy is done — a stat'd size could disagree
		// with what was actually read if the file changed mid-scan, and a
		// wrong length prefix is exactly the ambiguity the prefix exists to
		// prevent.
		fmt.Fprintf(h, "%d:%s|", len(n), n)
		written, cerr := io.Copy(h, f)
		_ = f.Close()
		if cerr != nil {
			return "", cerr
		}
		fmt.Fprintf(h, "|%d|", written)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

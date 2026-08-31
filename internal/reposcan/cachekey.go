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
// AND tests: a suite can be weakened without the source changing. It must
// cover whatever the run is actually GRADED BY — one paired test file on the
// file-scoped path, the whole enumerated test surface otherwise (see
// DigestTestSurface, and EmitConfig.FileScopedTests for which applies).
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
	// AuditConfig is the scan-wide bundle of flags that change WHAT a run
	// measures rather than which files it audits — the grading mode, the
	// check argv, a replayed mutant set, and the WRITER MODE (per-survivor
	// vs batched, two different exams over the same survivors). Built by
	// cmd/corral's auditConfigKey, which carries the inclusion rules and the
	// reason each component is in it.
	AuditConfig string
	// Substrate is where the audit ran — SubstrateJail or SubstrateWorkspace.
	// A verdict earned under bwrap and one earned in a CI runner's checkout
	// are different claims: different isolation, different toolchain
	// provenance. Keying on it stops a seal being assembled from a mix
	// without saying so.
	Substrate string
}

// Substrate names, the permitted values of KeyInputs.Substrate.
const (
	SubstrateJail      = "jail"
	SubstrateWorkspace = "workspace"
)

// CacheKey is the content address of a verdict. Fields are length-prefixed so
// no combination of values can collide by concatenation.
func (k KeyInputs) CacheKey() string {
	h := sha256.New()
	for _, f := range []string{
		k.SourceDigest, k.PackageDigest, k.GoalDigest, k.TestSurfaceDigest,
		k.EngineVersion, k.ModelSet, k.AuditConfig, k.Substrate,
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

// DigestTestSurface is the sha256 over a SET of test files — every path in
// paths, deduplicated and folded in sorted path order, name and content both.
// It is the TestSurfaceDigest for a scan graded by the project's whole
// recursive suite, where a single paired test file is not the grading surface:
// the suite is.
//
// Under-keying here is the dangerous direction. Weaken a shared helper and,
// with only the audited file's own paired test in the key, source, package and
// paired test are all byte-identical — a HIT, and the ledger repeats an old
// kill rate for a suite that genuinely got worse, signed, into a
// tamper-evident record where nobody can find it afterwards.
//
// The consequence is deliberate and worth stating plainly: on the whole-suite
// path, ANY change to ANY test file invalidates EVERY file's verdict in the
// repo. That is CORRECT — the grading surface really did change for every
// file — and it is the cheap direction of the asymmetry: over-invalidation
// only costs money, under-invalidation signs an unmeasured claim.
//
// Length-prefixing follows DigestDir's discipline (see there): the path and
// its content digest are both prefixed with their lengths, so no two different
// path/digest SETS can fold to the same value by concatenation. Contents are
// hashed via DigestFile, so every read is confined to root and non-regular
// entries are refused rather than followed.
func DigestTestSurface(root *os.Root, paths []string) (string, error) {
	uniq := make(map[string]bool, len(paths))
	clean := make([]string, 0, len(paths))
	for _, p := range paths {
		p = path.Clean(p)
		if p == "" || p == "." || uniq[p] {
			continue
		}
		uniq[p] = true
		clean = append(clean, p)
	}
	// Sorted, not enumeration order: the key must not move because the walker
	// happened to visit a directory in a different order on another machine.
	sort.Strings(clean)

	h := sha256.New()
	for _, p := range clean {
		d, err := DigestFile(root, p)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%d:%s|%d:%s|", len(p), p, len(d), d)
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

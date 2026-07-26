package reposcan

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
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

// DigestFile is the sha256 of one file's contents.
func DigestFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// DigestDir is the sha256 over every regular file directly in dir, by sorted
// name, hashing name and content both. Subdirectories are not descended:
// "package" means the immediate directory, matching Go/Python/Ruby layout.
func DigestDir(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	h := sha256.New()
	for _, n := range names {
		b, rerr := os.ReadFile(filepath.Join(dir, n))
		if rerr != nil {
			return "", rerr
		}
		fmt.Fprintf(h, "%d:%s|%d:", len(n), n, len(b))
		h.Write(b)
		h.Write([]byte("|"))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/pdbethke/corralai/internal/memory"
)

// seedDocGlobs are the paths (relative to a cloned repo's root) ingestSeedDocs
// scans for the CORRAL.md convention: the repo's own developer-doc corpus (see
// CORRAL.md at the repo root for the convention itself).
var seedDocGlobs = []string{"CORRAL.md", "docs/corral/*.md"}

// Defensive caps: the seed docs come from a just-cloned, attacker-suppliable
// repo, so both dimensions are bounded — at most maxSeedDocFiles files ingest,
// and any single file over maxSeedDocBytes is skipped (loudly logged).
const (
	maxSeedDocFiles = 24
	maxSeedDocBytes = 128 << 10 // 128 KiB per file
)

// ingestSeedDocs ingests a repo mission's CORRAL.md + docs/corral/*.md as
// ADVISORY memory entries (shared=false) tagged to repoTag, at repo-snapshot
// time (right after create_mission clones the repo — see missions.go).
//
// This is the trust boundary the spec requires: repo-shipped content is never
// auto-vetted. A hostile repo could ship a doc whose own front-matter claims
// shared: true, but Add's shared parameter is hardcoded false here regardless
// of file content — front-matter in the source markdown cannot flip it. Only
// a human admin's promote path (approve_proposal / promote_memory, see
// learn.go) can ever set shared=true.
//
// targetDir is passed through to memory.Store.Add unchanged; "" (the
// production call from missions.go) lands entries in the default memory dir
// alongside the rest of the corpus, same as every other mem.Add call in this
// package. A non-empty targetDir is for test isolation.
//
// Best-effort throughout: a missing/unreadable file or a failed Add is
// skipped, never aborts mission provisioning — seeding memory is an aid, not
// a gate. Returns the slugs of the entries it ingested.
//
// Entry names are namespaced per repo: every adopting repo ships the SAME
// filenames (that's the convention), and memory.Add keys on slug-of-name
// alone — without namespacing, repo B's verify-gate.md would silently
// overwrite repo A's entry. The name is repoTag + a short hash of repoTag +
// the file's basename; the hash matters because Add's slugify collapses runs
// of non-[a-z0-9_-] to a single '-', so two tags differing only in separator
// characters (e.g. "a/b" vs "a.b") would otherwise still collide.
func ingestSeedDocs(mem *memory.Store, dir, repoTag, author, targetDir string) []string {
	if mem == nil {
		return nil
	}
	var paths []string
	for _, g := range seedDocGlobs {
		matches, err := filepath.Glob(filepath.Join(dir, g))
		if err != nil {
			continue
		}
		paths = append(paths, matches...)
	}
	if len(paths) > maxSeedDocFiles {
		log.Printf("seed docs: %d files matched under %s — capping at %d (a repo's doc corpus should be curated, not bulk)", len(paths), dir, maxSeedDocFiles)
		paths = paths[:maxSeedDocFiles]
	}
	tagSum := sha256.Sum256([]byte(repoTag))
	tagHash := hex.EncodeToString(tagSum[:4])
	var slugs []string
	for _, p := range paths {
		body, err := readCapped(p, maxSeedDocBytes)
		if err != nil {
			log.Printf("seed docs: skipping %s: %v", p, err)
			continue
		}
		base := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
		name := repoTag + "-" + tagHash + "-" + base
		slug, _, _, err := mem.Add(name, body, firstLine(body), "lesson", repoTag, targetDir, false, author)
		if err != nil {
			log.Printf("seed docs: add %s: %v", p, err)
			continue
		}
		slugs = append(slugs, slug)
	}
	return slugs
}

// readCapped reads p, erroring (so the caller skips) when the content exceeds
// limit bytes. The read itself is bounded by limit+1 — a huge file never lands
// in memory even transiently, so a hostile repo can't create memory pressure
// on the brain through the seed-doc path.
func readCapped(p string, limit int) (string, error) {
	f, err := os.Open(p) // #nosec G304 -- path comes from filepath.Glob over the mission's own cloned working copy (dir), not attacker-controlled beyond repo content already treated as advisory-only
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return "", err
	}
	if len(b) > limit {
		return "", fmt.Errorf("exceeds the %d-byte seed-doc cap", limit)
	}
	return string(b), nil
}

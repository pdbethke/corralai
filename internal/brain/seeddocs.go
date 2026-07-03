// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/pdbethke/corralai/internal/memory"
)

// seedDocGlobs are the paths (relative to a cloned repo's root) ingestSeedDocs
// scans for the CORRAL.md convention: the repo's own developer-doc corpus (see
// CORRAL.md at the repo root for the convention itself).
var seedDocGlobs = []string{"CORRAL.md", "docs/corral/*.md"}

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
// a gate.
func ingestSeedDocs(mem *memory.Store, dir, repoTag, author, targetDir string) {
	if mem == nil {
		return
	}
	var paths []string
	for _, g := range seedDocGlobs {
		matches, err := filepath.Glob(filepath.Join(dir, g))
		if err != nil {
			continue
		}
		paths = append(paths, matches...)
	}
	for _, p := range paths {
		b, err := os.ReadFile(p) // #nosec G304 -- path comes from filepath.Glob over the mission's own cloned working copy (dir), not attacker-controlled beyond repo content already treated as advisory-only
		if err != nil {
			continue
		}
		body := string(b)
		name := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
		_, _, _, _ = mem.Add(name, body, firstLine(body), "lesson", repoTag, targetDir, false, author)
	}
}

// SPDX-License-Identifier: Elastic-2.0

package controlspec

import (
	"embed"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// bundleFS embeds the starter control-spec bundles shipped with corralai —
// the control owner's ready-made standard libraries, loaded by name via LoadBundle.
//
// HONESTY NOTE: bundles/asvs-l1.json is a faithful PARAPHRASE of OWASP ASVS
// 4.0.3 Level 1 requirements, written for this bundle format — it is not a
// verbatim reproduction of the published standard. Verify each Intent string
// against the official ASVS 4.0.3 text (github.com/OWASP/ASVS) before this
// bundle (or any bundle derived from it) ships as a real, control-owner-facing
// control library.
//
//go:embed bundles/*.json
var bundleFS embed.FS

// LoadBundle reads an embedded standard bundle by name (e.g. "asvs-l1").
func LoadBundle(name string) (Bundle, error) {
	data, err := bundleFS.ReadFile("bundles/" + name + ".json")
	if err != nil {
		return Bundle{}, fmt.Errorf("controlspec: load bundle %q: %w", name, err)
	}
	var b Bundle
	if err := json.Unmarshal(data, &b); err != nil {
		return Bundle{}, fmt.Errorf("controlspec: parse bundle %q: %w", name, err)
	}
	return b, nil
}

// ImportBundle creates one owner-scoped Goal per requirement in b, stamping
// each with now (the caller-supplied clock — ImportBundle never calls
// time.Now() itself, which keeps it deterministic under test). It is
// idempotent: SaveGoal upserts on (owner, id), so re-importing the same
// bundle for the same owner overwrites the existing goals rather than
// duplicating them. Returns the number of goals written.
//
// The "asvs-" id prefix below is specific to this single ASVS bundle; a
// later multi-bundle plan can derive the prefix from b.Standard instead.
func ImportBundle(s *Store, owner string, b Bundle, now time.Time) (int, error) {
	std := strings.TrimSpace(b.Standard + " " + b.Version)
	n := 0
	for _, r := range b.Requirements {
		g := Goal{
			ID:        "asvs-" + strings.ToLower(r.Ref),
			Owner:     owner,
			Standard:  std,
			Ref:       r.Ref,
			Intent:    r.Intent,
			Level:     r.Level,
			Mode:      r.Mode,
			CreatedTS: now,
		}
		if err := s.SaveGoal(g); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

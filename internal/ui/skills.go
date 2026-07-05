// SPDX-License-Identifier: Elastic-2.0

package ui

import (
	"net/http"
	"strings"

	"github.com/pdbethke/corralai/internal/artifacts"
)

// skillView is one fleet skill as the UI wants it: a stable display name
// derived from its path, a best-effort one-line description lifted from the
// SKILL.md frontmatter, and the path itself for anyone who wants the raw
// artifact. Content is never shipped here — the Skills tab and the agent
// inspector only need to say a skill EXISTS and what it's for, not render it.
type skillView struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Description string `json:"description,omitempty"`
}

// skillNameFromPath derives the display name Warp-style: "skills/deploy/SKILL.md"
// -> "deploy". Falls back to the full path for anything that doesn't match the
// expected "skills/<name>/SKILL.md" (or "skills/<name>.md") shape rather than
// hiding an oddly-placed skill from the list.
func skillNameFromPath(path string) string {
	trimmed := strings.TrimPrefix(path, "skills/")
	if trimmed == path { // no "skills/" prefix at all — show as-is
		return path
	}
	trimmed = strings.TrimSuffix(trimmed, "/SKILL.md")
	trimmed = strings.TrimSuffix(trimmed, ".md")
	trimmed = strings.Trim(trimmed, "/")
	if trimmed == "" {
		return path
	}
	return trimmed
}

// skillDescription does a minimal, deliberately-not-clever read of the
// frontmatter `description:` line ("---\nname: x\ndescription: y\n---").
// It does not parse YAML generally (multi-line/quoted/escaped values are left
// as-is, trimmed of surrounding quotes) — good enough for the one-liners
// skills actually write, and cheap to reason about. Returns "" on anything
// that doesn't look like frontmatter or has no description field.
func skillDescription(content []byte) string {
	s := string(content)
	if !strings.HasPrefix(s, "---") {
		return ""
	}
	rest := s[3:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return ""
	}
	fm := rest[:end]
	for _, line := range strings.Split(fm, "\n") {
		line = strings.TrimSpace(line)
		after, ok := strings.CutPrefix(line, "description:")
		if !ok {
			continue
		}
		d := strings.TrimSpace(after)
		d = strings.Trim(d, `"'`)
		return d
	}
	return ""
}

// toSkillViews maps live 'skill' artifacts to the UI's shape, in path order.
func toSkillViews(arts []artifacts.Artifact) []skillView {
	out := make([]skillView, 0, len(arts))
	for _, a := range arts {
		out = append(out, skillView{
			Name:        skillNameFromPath(a.Path),
			Path:        a.Path,
			Description: skillDescription(a.Content),
		})
	}
	return out
}

// skills serves the fleet's shared skill set — GET /api/skills. Read-only,
// same bearer+allowlist gate as every other GET in this package (see the
// auth-pattern note on Handler); no per-handler check needed. nil store =>
// empty list, never a 500 (degrade-never-block, same posture as every other
// optional dependency in Server).
func (s *Server) skills(w http.ResponseWriter, _ *http.Request) {
	out := []skillView{}
	if s.artifacts != nil {
		if arts, err := s.artifacts.ListKind("skill"); err == nil {
			out = toSkillViews(arts)
		}
	}
	writeJSON(w, map[string]any{"skills": out})
}

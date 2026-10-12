// SPDX-License-Identifier: Elastic-2.0

package repoindex

import (
	"path/filepath"
	"strings"

	plugins "github.com/pdbethke/corralai/internal/lang"
)

// langForExt names path's language. An extension a corral plugin audits is
// answered by lang.Detect — the plugin's own claim — so the indexer and the
// auditor cannot disagree about a file (they did: .cjs was audited as
// javascript and indexed as nothing). Everything else (languages corral can
// parse but does not audit, and grammar-only extensions such as .tsx) comes
// from ownExtLang. The extension is lower-cased first, as it always was.
// Returns "" for an unknown or missing extension.
func langForExt(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == "" {
		return ""
	}
	if p, ok := plugins.Detect("x" + ext); ok {
		return p.Name()
	}
	return ownExtLang[ext]
}

// ownExtLang maps lower-case extensions no plugin claims to the language
// name whose grammar parses them. TestOwnExtensionsAreNotPluginClaimed keeps
// it disjoint from every plugin's extensions.
var ownExtLang = map[string]string{
	".tsx":  "typescript",
	".jsx":  "javascript",
	".java": "java",
	".c":    "c",
	".h":    "c",
	".cc":   "cpp",
	".cpp":  "cpp",
	".cxx":  "cpp",
	".hpp":  "cpp",
	".cs":   "csharp",
	".sh":   "bash",
	".bash": "bash",
}

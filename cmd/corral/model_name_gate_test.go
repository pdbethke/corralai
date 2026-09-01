// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestNoUnverifiedModelNameIsPublished is the static gate docs/design/
// model-registry.md promises: "no vendor-shaped model name appears anywhere
// outside the registry — the same mechanism that keeps corral's GitHub
// Action version pins from rotting" (see
// TestDocsNeverAdvertiseAnUncutActionTag in action_test.go, which this is
// modelled on).
//
// It exists because manual checking failed twice in one day: a documented
// example (`gemini-3.6-pro`) that has never existed burned two hours of CI,
// a registry example (`gpt-5`) went unverified for want of a key, and a
// production daemon ran a critic below the project's own model-recency
// floor. Every one was a plausible-sounding name that nothing refused.
//
// Scope is every TRACKED text file the repo ships — found with `git
// ls-files`, so untracked scratch files can never make this gate lie green —
// filtered to the extensions listed in modelNameGateExtensions. Untracked
// dirs (.git, .worktrees, node_modules, build output) are excluded by
// construction: `git ls-files` never lists them.
//
// A hit is allowed only by an entry in testdata/model-names-allowed.txt. An
// "ok" entry allows the name anywhere. A "prose-only" entry allows it only
// where the surrounding text reads as prose, not a recommendation — see
// looksLikeARecommendation for exactly what that means and what it misses.
func TestNoUnverifiedModelNameIsPublished(t *testing.T) {
	repoRoot := repoRootForModelGate(t)
	allow, err := loadModelNameAllowlist(filepath.Join(repoRoot, "testdata", "model-names-allowed.txt"))
	if err != nil {
		t.Fatalf("loading testdata/model-names-allowed.txt: %v", err)
	}

	files, err := trackedModelGateFiles(t, repoRoot)
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	// A walk that silently found nothing would pass green forever.
	if len(files) < 50 {
		t.Fatalf("git ls-files returned only %d scannable files — the scope is broken, not the docs", len(files))
	}

	var failures int
	for _, rel := range files {
		b, err := os.ReadFile(filepath.Join(repoRoot, rel))
		if err != nil {
			t.Fatalf("reading %s: %v", rel, err)
		}
		for _, v := range modelNameViolations(rel, string(b), allow) {
			failures++
			t.Error(v)
		}
	}
	if failures == 0 && len(allow) == 0 {
		t.Fatal("allowlist loaded empty — the gate cannot be meaningfully green")
	}
}

// modelNameViolations scans one file's content for vendor-shaped model names
// and reports every one that either isn't in allow, or is allowlisted
// prose-only but appears in a position that reads as a recommendation. relPath
// is used both for the error text and to pick the file's fence/with-block
// rules (see fencedCodeLines and withBlockLines).
//
// This is the whole gate's logic, factored out of TestNoUnverifiedModelNameIsPublished
// so the real-repo scan and the fixture-driven behavior tests below run the
// exact same code path.
func modelNameViolations(relPath, content string, allow map[string]allowlistEntry) []string {
	lines := strings.Split(content, "\n")
	fenced := fencedCodeLines(relPath, lines)
	inWith := withBlockLines(relPath, lines)

	var out []string
	for i, line := range lines {
		for _, m := range modelNameGatePattern.FindAllStringIndex(line, -1) {
			// Trailing "." is sentence punctuation, not part of a name
			// ("...stay on qwen."); a real tag never ends in a bare dot.
			name := strings.TrimRight(line[m[0]:m[1]], ".")
			entry, ok := allow[name]
			if !ok {
				out = append(out, fmt.Sprintf(
					"%s:%d: unverified model-shaped name %q is not in testdata/model-names-allowed.txt.\n"+
						"  Verify it against its provider's own model listing (or `corral doctor` once the live gate lands),\n"+
						"  then add a line to testdata/model-names-allowed.txt dated today with the evidence — or remove it.",
					relPath, i+1, name))
				continue
			}
			if entry.proseOnly && looksLikeARecommendation(line, m[0], fenced[i], inWith[i]) {
				out = append(out, fmt.Sprintf(
					"%s:%d: %q is allowlisted PROSE-ONLY (%s) but appears in a position that reads as a recommendation — "+
						"inside a fenced code block, right after a model:/-model /\"model\": key, or in a workflow `with:` value.\n"+
						"  If this name is now genuinely verified-served, update its allowlist entry with the date and evidence.\n"+
						"  If it is still unverified, this line must not be typed as something to run.",
					relPath, i+1, name, entry.reason))
			}
		}
	}
	return out
}

// modelNameGateExtensions are the tracked-file extensions this gate scans.
var modelNameGateExtensions = map[string]bool{
	".md": true, ".mdx": true, ".astro": true, ".yml": true, ".yaml": true,
	".json": true, ".go": true, ".sh": true, ".txt": true,
}

// modelNameGatePattern matches a vendor-shaped model name. It is
// deliberately broad — over-matching costs one allowlist line, under-matching
// costs what a phantom name has already cost this project twice in one day.
//
// gemini-/claude-/gpt-/o1-/o3-/phi- require a hyphen after the vendor stem so
// the pattern does not swallow every plain-English mention of "Claude" (this
// repo's own commit trailers say "Claude Fable 5", with no hyphen) or "GPT"
// used as a common noun. qwen/llama/mistral/mixtral/deepseek/gemma have no
// such requirement because real names in those families sometimes have no
// separator (qwen3, gemma4, llama3). \b keeps `\bllama` from matching inside
// an unrelated identifier like "ollamareq" (RE2's \b is a \w-class boundary,
// and the 'o' before "llama" there is a word character, so no boundary
// exists at that position).
var modelNameGatePattern = regexp.MustCompile(
	`\b((?:gemini|claude|gpt|o1|o3|phi)-[A-Za-z0-9._:-]*[A-Za-z0-9]|(?:qwen|llama|mistral|mixtral|deepseek|gemma)[A-Za-z0-9._:-]*)`,
)

// modelKeyPattern recognizes the line-local shapes that make a match read as
// a recommendation rather than prose: a YAML/JSON key ending in "model"
// immediately before the match (`writer-model:`, `"model":`), or a
// command-line `-model`/`--model`-shaped flag immediately before it. It is
// checked against the text of the line up to the match's start.
var modelKeyPattern = regexp.MustCompile(`(?i)(?:[\w-]*model"?\s*[:=]\s*"?|-{1,2}[\w-]*model\s+"?)$`)

// withKeyPattern recognizes the start of a composite-action `with:` block in
// a workflow YAML file, so values inside it count as recommendations even
// when their key doesn't happen to say "model".
var withKeyPattern = regexp.MustCompile(`^(\s*)with:\s*$`)

// looksLikeARecommendation decides whether the match at byte offset start on
// line reads as telling a reader (or CI) to actually run this model, as
// opposed to discussing it.
//
// The rule, at minimum, per the task: fail when the name is (1) inside a
// fenced code block, (2) immediately after a model:/-model /"model": key, or
// (3) inside a workflow `with:` block. All three are checked structurally,
// not by guessing at English — a line is either inside a ``` fence or it
// isn't, either preceded by a model-shaped key or it isn't, either inside an
// indented `with:` block or it isn't.
//
// WHAT THIS MISSES: prose that recommends a model without any of those three
// shapes — "you should now use gemini-3.6-pro for the critic seat" in plain
// sentence form, or a shell one-liner outside a fenced block
// (`corral certify --critic-model gemini-3.6-pro ...` typed directly into a
// paragraph rather than a code fence or a `-model` flag with no preceding
// dash). It also cannot see recommendations that span multiple lines (a
// YAML value continued with `>` or `|`), and it does not understand Markdown
// tables or inline code spans (single backticks) as a fourth surface — a
// prose-only name inside a single-backtick inline code span reads as plain
// prose to this check even though a reader would take it as a copy-pasteable
// value.
func looksLikeARecommendation(line string, start int, inFence, inWith bool) bool {
	if inFence {
		return true
	}
	if inWith {
		return true
	}
	before := line[:start]
	return modelKeyPattern.MatchString(before)
}

// fencedCodeLines returns, for each line index, whether that line falls
// inside a ``` fenced code block. Only meaningful for prose-shaped files
// (.md/.mdx/.astro); for other extensions every line is false, since a code
// fence marker is not a documentation-fence concept there.
func fencedCodeLines(relPath string, lines []string) []bool {
	out := make([]bool, len(lines))
	ext := strings.ToLower(filepath.Ext(relPath))
	if ext != ".md" && ext != ".mdx" && ext != ".astro" {
		return out
	}
	in := false
	for i, l := range lines {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "```") {
			in = !in
			out[i] = true // the fence marker line itself doesn't carry a name
			continue
		}
		out[i] = in
	}
	return out
}

// withBlockLines returns, for each line index, whether that line falls
// inside a composite-action `with:` block: any line indented deeper than a
// `with:` key, until a line at or above that indent ends the block. Only
// meaningful for YAML files.
func withBlockLines(relPath string, lines []string) []bool {
	out := make([]bool, len(lines))
	ext := strings.ToLower(filepath.Ext(relPath))
	if ext != ".yml" && ext != ".yaml" {
		return out
	}
	inWith := false
	withIndent := -1
	for i, l := range lines {
		trimmed := strings.TrimSpace(l)
		indent := len(l) - len(strings.TrimLeft(l, " "))
		if inWith && trimmed != "" {
			if indent <= withIndent {
				inWith = false
			}
		}
		if m := withKeyPattern.FindStringSubmatch(l); m != nil {
			inWith = true
			withIndent = len(m[1])
			out[i] = false // the `with:` line itself names nothing
			continue
		}
		// A `#` comment inside a `with:` block (e.g. explaining why a value
		// was chosen) is prose, not the value itself — only an actual
		// `key: value` line reads as a recommendation.
		out[i] = inWith && !strings.HasPrefix(trimmed, "#")
	}
	return out
}

// allowlistEntry is one parsed line of testdata/model-names-allowed.txt.
type allowlistEntry struct {
	proseOnly bool
	reason    string
}

// loadModelNameAllowlist parses the pipe-delimited allowlist file. A name
// may appear on more than one line (documenting more than one place it's
// fine); the last "ok" entry wins over an earlier one, but any "prose-only"
// entry makes the name prose-only unless a later line for the same name says
// "ok" — in short, prose-only is the stricter state, and a bare re-listing as
// "ok" is what graduates a name once it becomes genuinely verified-served.
func loadModelNameAllowlist(path string) (map[string]allowlistEntry, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]allowlistEntry{}
	for lineNo, raw := range strings.Split(string(b), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "|", 3)
		if len(parts) != 3 {
			return nil, errAllowlistLine(path, lineNo+1, raw)
		}
		name := strings.TrimSpace(parts[0])
		flag := strings.ToLower(strings.TrimSpace(parts[1]))
		reason := strings.TrimSpace(parts[2])
		out[name] = allowlistEntry{proseOnly: flag == "prose-only", reason: reason}
	}
	return out, nil
}

func errAllowlistLine(path string, lineNo int, raw string) error {
	return fmt.Errorf("%s:%d: expected `name | flag | reason`, got: %s", path, lineNo, raw)
}

// repoRootForModelGate returns the repo root, two levels up from
// cmd/corral where this test file lives.
func repoRootForModelGate(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolving repo root: %v", err)
	}
	return root
}

// trackedModelGateFiles asks git for every tracked file, filtered to the
// extensions this gate cares about. Using `git ls-files` — rather than a
// filesystem walk — is the whole reason an untracked scratch file (a local
// experiment, a downloaded fixture) can never make this gate lie.
func trackedModelGateFiles(t *testing.T, repoRoot string) ([]string, error) {
	t.Helper()
	out, err := runGit(t, repoRoot, "ls-files")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, rel := range strings.Split(out, "\n") {
		rel = strings.TrimSpace(rel)
		if rel == "" {
			continue
		}
		if !modelNameGateExtensions[strings.ToLower(filepath.Ext(rel))] {
			continue
		}
		files = append(files, rel)
	}
	return files, nil
}

// TestModelNameGateFixtures exercises modelNameViolations directly against
// small in-memory fixtures — no git, no real repo files — to pin down the
// four behaviors the task requires: an unlisted name fails, the same name
// allowlisted passes, a prose-only name in a recommendation position fails,
// and the same prose-only name in plain prose passes.
func TestModelNameGateFixtures(t *testing.T) {
	okAllow := map[string]allowlistEntry{
		"gemini-3.6-flash": {reason: "verified-served fixture"},
	}
	proseAllow := map[string]allowlistEntry{
		"gemini-3.6-pro": {proseOnly: true, reason: "NEVER EXISTED — fixture"},
	}

	tests := []struct {
		name       string
		relPath    string
		content    string
		allow      map[string]allowlistEntry
		wantFail   bool
		wantSubstr string
	}{
		{
			name:       "unlisted model name fails",
			relPath:    "docs/example.md",
			content:    "Run with --critic-model gemini-9.9-ultra for best results.\n",
			allow:      okAllow,
			wantFail:   true,
			wantSubstr: `"gemini-9.9-ultra" is not in testdata/model-names-allowed.txt`,
		},
		{
			name:     "the same name in the allowlist passes",
			relPath:  "docs/example.md",
			content:  "Run with --critic-model gemini-3.6-flash for best results.\n",
			allow:    okAllow,
			wantFail: false,
		},
		{
			name:       "prose-only name inside a fenced code block fails",
			relPath:    "docs/example.md",
			content:    "Some prose.\n\n```bash\ncorral certify --critic-model gemini-3.6-pro\n```\n",
			allow:      proseAllow,
			wantFail:   true,
			wantSubstr: `"gemini-3.6-pro" is allowlisted PROSE-ONLY`,
		},
		{
			name:     "the same prose-only name in a sentence passes",
			relPath:  "docs/example.md",
			content:  "A documented example named `gemini-3.6-pro` for a month never existed.\n",
			allow:    proseAllow,
			wantFail: false,
		},
		{
			name:       "prose-only name after a model: key fails",
			relPath:    "action.yml",
			content:    "      derive-model: gemini-3.6-pro\n",
			allow:      proseAllow,
			wantFail:   true,
			wantSubstr: `"gemini-3.6-pro" is allowlisted PROSE-ONLY`,
		},
		{
			name:       "prose-only name in a workflow with: block fails",
			relPath:    ".github/workflows/example.yml",
			content:    "      - uses: ./\n        with:\n          writer-model: gemini-3.6-pro\n",
			allow:      proseAllow,
			wantFail:   true,
			wantSubstr: `"gemini-3.6-pro" is allowlisted PROSE-ONLY`,
		},
		{
			name:    "prose-only name in a with: block COMMENT still passes",
			relPath: ".github/workflows/example.yml",
			content: "      - uses: ./\n        with:\n          # the name chosen, gemini-3.6-pro, has never existed\n          writer-model: gemini-3.7-flash\n",
			allow: map[string]allowlistEntry{
				"gemini-3.6-pro":   {proseOnly: true, reason: "NEVER EXISTED — fixture"},
				"gemini-3.7-flash": {reason: "verified-served fixture"},
			},
			wantFail: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := modelNameViolations(tt.relPath, tt.content, tt.allow)
			if tt.wantFail && len(got) == 0 {
				t.Fatalf("expected a violation, got none")
			}
			if !tt.wantFail && len(got) != 0 {
				t.Fatalf("expected no violation, got: %v", got)
			}
			if tt.wantFail && tt.wantSubstr != "" {
				found := false
				for _, v := range got {
					if strings.Contains(v, tt.wantSubstr) {
						found = true
					}
				}
				if !found {
					t.Fatalf("expected a violation containing %q, got: %v", tt.wantSubstr, got)
				}
			}
		})
	}
}

// TestModelNameAllowlistLoads makes sure the real allowlist file parses and
// is non-trivial in size, independent of whether the gate's scan currently
// passes — a parse error here means the FILE is broken, not the repo.
func TestModelNameAllowlistLoads(t *testing.T) {
	repoRoot := repoRootForModelGate(t)
	allow, err := loadModelNameAllowlist(filepath.Join(repoRoot, "testdata", "model-names-allowed.txt"))
	if err != nil {
		t.Fatalf("loading testdata/model-names-allowed.txt: %v", err)
	}
	if len(allow) < 50 {
		t.Fatalf("allowlist has only %d entries — expected the repo's real seeded set", len(allow))
	}
	entry, ok := allow["gemini-3.6-pro"]
	if !ok || !entry.proseOnly {
		t.Fatalf(`expected "gemini-3.6-pro" to be allowlisted prose-only, got %+v (ok=%v)`, entry, ok)
	}
}

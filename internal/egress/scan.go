// SPDX-License-Identifier: Elastic-2.0

// Package egress is the forge-agnostic OUTPUT gate: before the brain commits
// the herd's produced code and opens a PR, this package scans the changed
// files for things that must not ship — committed secrets first (the
// highest-value, most deterministic check), plus opportunistic advisory
// checks (Go dependency vulnerabilities, obviously-incompatible license
// files). It runs regardless of which forge the PR lands on (GitHub, Gitea,
// GitLab, self-hosted) — corral fences the AGENTS; this is the floor that
// also vets what they SHIP.
package egress

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/sandbox"
)

// Severity levels for a Finding. "block" means the finding is loud AND must
// stop the auto-PR (a detected secret is the worst egress leak — it must not
// leave). "advisory" is surfaced but does not withhold the push/PR.
const (
	SeverityBlock    = "block"
	SeverityAdvisory = "advisory"
)

// Finding is one egress-scan hit on a changed file.
type Finding struct {
	Path     string // path relative to the scanned working copy
	Line     int    // 1-based; 0 when not line-addressable (e.g. a dep audit finding)
	Rule     string // human-readable rule/check name
	Sample   string // redacted excerpt — safe to log, never the raw secret
	Severity string // SeverityBlock | SeverityAdvisory
}

// maxScanBytes bounds per-file secret scanning so a huge generated/binary blob
// in the diff can't stall the mission-engine tick.
const maxScanBytes = 2 << 20 // 2MB

// secretRule is one curated, high-signal secret-detection pattern. The set is
// gitleaks-style (named vendor formats + private-key blocks) rather than a
// generic "looks like a password" heuristic, to keep false positives — which
// would block a clean mission's PR — rare.
type secretRule struct {
	name string
	re   *regexp.Regexp
}

var secretRules = []secretRule{
	{"AWS Access Key ID", regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)},
	{"AWS Secret Access Key", regexp.MustCompile(`(?i)aws_secret_access_key\s*[:=]\s*['"]?[A-Za-z0-9/+=]{40}['"]?`)},
	{"GitHub Token", regexp.MustCompile(`\bgh[poutsr]_[A-Za-z0-9]{36,255}\b`)},
	{"GitLab Personal Access Token", regexp.MustCompile(`\bglpat-[A-Za-z0-9\-_]{20,}\b`)},
	{"Slack Token", regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}\b`)},
	{"Google API Key", regexp.MustCompile(`\bAIza[0-9A-Za-z\-_]{35}\b`)},
	{"Stripe Live Secret Key", regexp.MustCompile(`\bsk_live_[0-9a-zA-Z]{16,}\b`)},
	{"Anthropic API Key", regexp.MustCompile(`\bsk-ant-[A-Za-z0-9\-_]{20,}\b`)},
	{"OpenAI API Key", regexp.MustCompile(`\bsk-[A-Za-z0-9]{32,}\b`)},
	{"Private Key Block", regexp.MustCompile(`-----BEGIN (RSA |EC |OPENSSH |DSA |PGP )?PRIVATE KEY-----`)},
	{"Generic Bearer/JWT-shaped Secret", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`)},
}

// Scan is the egress-scan entry point: it runs every check against the given
// changed files, rooted at dir (the mission's working copy). It never returns
// an error for a single check failing — a scan degrades gracefully (advisory
// checks that can't run are simply skipped) rather than blocking a mission on
// tooling noise. The only hard error is failing to enumerate files, which
// callers already have (files is passed in, not discovered here).
func Scan(ctx context.Context, dir string, files []string) []Finding {
	var out []Finding
	out = append(out, scanSecrets(dir, files)...)
	out = append(out, scanLicense(dir, files)...)
	out = append(out, scanGoVuln(ctx, dir, files)...)
	return out
}

// scanSecrets reads each changed file (skipping ones that no longer exist —
// e.g. deletions in the diff — or exceed maxScanBytes) and applies the
// curated secret-detection rules line by line.
func scanSecrets(dir string, files []string) []Finding {
	var out []Finding
	for _, rel := range files {
		full := filepath.Join(dir, rel)
		info, err := os.Stat(full)
		if err != nil || info.IsDir() {
			continue
		}
		if info.Size() > maxScanBytes {
			out = append(out, Finding{Path: rel, Rule: "unscanned-large-file",
				Sample: "file exceeds scan size limit — not scanned for secrets", Severity: SeverityBlock})
			continue
		}
		f, err := os.Open(full) // #nosec G304 -- dir is the mission's own working copy; rel comes from the mission's own git diff, not external input
		if err != nil {
			continue
		}
		lineNo := 0
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for scanner.Scan() {
			lineNo++
			line := scanner.Text()
			for _, r := range secretRules {
				if loc := r.re.FindStringIndex(line); loc != nil {
					out = append(out, Finding{
						Path: rel, Line: lineNo, Rule: r.name,
						Sample: redact(line[loc[0]:loc[1]]), Severity: SeverityBlock,
					})
				}
			}
		}
		if err := scanner.Err(); err != nil {
			out = append(out, Finding{Path: rel, Rule: "unscanned-remainder",
				Sample: "line too long to scan — remainder of file not scanned: " + err.Error(), Severity: SeverityBlock})
		}
		_ = f.Close()
	}
	return out
}

// ScanText runs the curated secret rules over the ADDED lines of a `git log -p`
// patch (the output of repo.Engine.DiffAddedLines). It is the history-aware
// companion to scanSecrets: scanSecrets reads the CURRENT working tree, so a
// secret committed in an earlier phase and then deleted (clean final tree) is
// missed — yet the push ships the whole branch history, so the secret leaves.
// ScanText walks every commit's added lines, catching a secret even when a
// later hunk removes it.
//
// It is HUNK-STRUCTURE-AWARE rather than prefix-guessing, which closes a
// header-spoof bypass (audit F1): a `+++ `/`--- ` line is a file header ONLY in
// the file-header region (before a file's first `@@`). Inside a hunk BODY every
// `+`-prefixed line is ADDED CONTENT — so an added line whose own content begins
// with `++ ` renders as `+++ <secret>` and MUST still be scanned (strip exactly
// one leading `+`), not mistaken for a `+++ b/…` header. State: a `diff --git `
// line starts a new file (expecting headers), an `@@ ` line switches to
// hunk-body mode, `-`/` `/`\` lines in a hunk body are skipped.
//
// The path is best-effort from the file-header-region `+++ b/…` line; the line
// number is unknown (0) because a history patch has no single on-disk line.
// Findings are SeverityBlock — a detected secret must not ship.
//
// It ALSO closes a binary-blob evasion (audit pass-4 F1): a binary file both
// ADDED and DELETED within base..HEAD renders only as `Binary files … differ`
// (no `+`-content lines to scan) AND is gone from the final tree (invisible to
// the working-tree scan) — so a secret in its bytes would ship in the pushed
// history undetected. ScanText tracks, per path, whether the range added it and
// whether it deleted it (both derivable from the `Binary files …` line's
// /dev/null side), and emits a `binary-in-history` block finding for any path
// that was BOTH added AND deleted (net-removed → unscannable). A binary only
// added-and-kept, only deleted (pre-existing), or modified stays in the final
// tree and is NOT flagged — keeping this low-noise.
func ScanText(text string) []Finding {
	var out []Finding
	curPath := ""
	curBinPath := ""              // path from the current file's `diff --git` line (for binary classification)
	bin := map[string]*binState{} // path -> whether the range added and/or deleted it as a binary
	inHunk := false               // false = file-header region; true = hunk body (added lines are content)
	scanner := bufio.NewScanner(strings.NewReader(text))
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "diff --git "):
			// New file: reset to the file-header region (headers not yet seen).
			inHunk = false
			curPath = ""
			// Binary diffs carry NO `+++`/`---`/`@@` lines — capture the path here
			// (the `diff --git a/<p> b/<p>` line) so a `Binary files …` line below
			// can be attributed to it.
			curBinPath = parseDiffGitBPath(line)
			continue
		case strings.HasPrefix(line, "@@ "):
			// Hunk boundary: everything until the next diff/@@ is hunk body.
			inHunk = true
			continue
		case strings.HasPrefix(line, "Binary files ") && strings.HasSuffix(line, " differ"):
			// Binary diff metadata (never a `+`/`-`/space-prefixed hunk content line,
			// so it can't be spoofed by added content). Classify by the /dev/null side.
			lp, added, deleted := parseBinaryFiles(line)
			if added || deleted {
				p := curBinPath
				if p == "" {
					p = lp
				}
				if p != "" {
					st := bin[p]
					if st == nil {
						st = &binState{}
						bin[p] = st
					}
					st.added = st.added || added
					st.deleted = st.deleted || deleted
				}
			}
			continue
		}
		if !inHunk {
			// File-header region: "+++ b/path" (or "+++ /dev/null") is a header,
			// not content. Track the path; skip "--- a/…" and all other metadata.
			if strings.HasPrefix(line, "+++ ") {
				curPath = parsePatchPath(line)
			}
			continue
		}
		// Hunk body: a leading '+' means ADDED CONTENT (including a "+++ …" line
		// that is really "++ …" content). '-'/' '/'\' are removed/context/no-newline.
		if !strings.HasPrefix(line, "+") {
			continue
		}
		added := line[1:] // strip exactly one leading '+'
		for _, r := range secretRules {
			if loc := r.re.FindStringIndex(added); loc != nil {
				out = append(out, Finding{
					Path: curPath, Line: 0, Rule: r.name,
					Sample: redact(added[loc[0]:loc[1]]), Severity: SeverityBlock,
				})
			}
		}
	}
	if err := scanner.Err(); err != nil {
		out = append(out, Finding{Path: curPath, Rule: "unscanned-remainder",
			Sample: "line too long to scan — remainder of patch not scanned: " + err.Error(), Severity: SeverityBlock})
	}
	// A binary both ADDED and DELETED in the range is net-removed: never a `+`-line
	// to secret-scan, and gone from the final tree (working-tree scan can't see it).
	// Flag it for manual review. Sorted for deterministic finding order.
	evaded := make([]string, 0, len(bin))
	for p, st := range bin {
		if st.added && st.deleted {
			evaded = append(evaded, p)
		}
	}
	sort.Strings(evaded)
	for _, p := range evaded {
		out = append(out, Finding{
			Path: p, Line: 0, Rule: "binary-in-history",
			Sample:   "a binary file was added then removed in this branch's history and could not be scanned for secrets — review manually",
			Severity: SeverityBlock,
		})
	}
	return out
}

// binState tracks, for one path, whether base..HEAD added it and/or deleted it as
// a binary blob — the two facts that together identify the history-evasion case.
type binState struct {
	added   bool
	deleted bool
}

// parseDiffGitBPath best-effort extracts the b-side path from a `diff --git a/<p>
// b/<p>` line. Uses the LAST " b/" so a path containing " b/" still resolves; the
// add and delete commits emit an identical `diff --git` line, so the key matches.
// Returns "" if no b-side is found.
func parseDiffGitBPath(line string) string {
	rest := strings.TrimPrefix(line, "diff --git ")
	if i := strings.LastIndex(rest, " b/"); i >= 0 {
		return strings.TrimSpace(rest[i+len(" b/"):])
	}
	return ""
}

// parseBinaryFiles classifies a `Binary files <A> and <B> differ` line by which
// side is /dev/null: `/dev/null and b/<p>` = ADD, `a/<p> and /dev/null` = DELETE,
// both real = MODIFY (neither). The returned path is the non-/dev/null side with
// its a//b/ prefix stripped (a fallback for when the `diff --git` path is absent).
func parseBinaryFiles(line string) (path string, added, deleted bool) {
	mid := strings.TrimSuffix(strings.TrimPrefix(line, "Binary files "), " differ")
	// The a-side (left) is /dev/null for an ADD; the b-side (right) is /dev/null
	// for a DELETE. Anchor on the /dev/null side via prefix/suffix rather than
	// splitting on " and " — a filename can itself contain " and " (e.g.
	// `x and y.bin`), which would otherwise misclassify a delete as a MODIFY and
	// let a crafted-name binary evade the add+delete block. Check the ADD (a-side
	// /dev/null) form first so an added file whose name ends in ` and /dev/null`
	// isn't mistaken for a delete.
	switch {
	case strings.HasPrefix(mid, "/dev/null and "):
		return strings.TrimPrefix(strings.TrimPrefix(mid, "/dev/null and "), "b/"), true, false
	case strings.HasSuffix(mid, " and /dev/null"):
		return strings.TrimPrefix(strings.TrimSuffix(mid, " and /dev/null"), "a/"), false, true
	default:
		return "", false, false // both real (MODIFY) or unparseable — not an add/delete evasion
	}
}

// parsePatchPath extracts the file path from a unified-diff "+++ b/path" header,
// stripping the "+++ " prefix and a leading "b/" if present. Returns "" for
// "/dev/null" (a deletion's new-side header).
func parsePatchPath(header string) string {
	p := strings.TrimSpace(strings.TrimPrefix(header, "+++ "))
	if p == "/dev/null" {
		return ""
	}
	p = strings.TrimPrefix(p, "b/")
	return p
}

// redact reduces a matched secret to a safe-to-log excerpt: first/last 4
// characters plus a length marker. Never logs the raw secret.
func redact(match string) string {
	if len(match) <= 10 {
		return "[redacted]"
	}
	return fmt.Sprintf("%s...%s (redacted, %d chars)", match[:4], match[len(match)-4:], len(match))
}

// disallowedLicenseMarkers are strong-copyleft license identifiers that are
// obviously incompatible with corral's Elastic-2.0 licensing if a herd
// accidentally vendors or adds one. This is a cheap, high-signal heuristic —
// not a full SPDX/license-compatibility engine.
var disallowedLicenseMarkers = []string{
	"GNU GENERAL PUBLIC LICENSE",
	"GNU AFFERO GENERAL PUBLIC LICENSE",
	"GNU LESSER GENERAL PUBLIC LICENSE",
}

// licenseFileRe matches common license filenames (LICENSE, LICENSE.txt,
// COPYING, COPYING.md, case-insensitive), the only files this check inspects.
var licenseFileRe = regexp.MustCompile(`(?i)^(LICENSE|LICENCE|COPYING)(\.[A-Za-z0-9]+)?$`)

// scanLicense flags a newly-added/modified license-shaped file whose content
// carries a strong-copyleft marker. Advisory: license compatibility is a
// judgment call for a human, not something an automated gate should block on.
func scanLicense(dir string, files []string) []Finding {
	var out []Finding
	for _, rel := range files {
		if !licenseFileRe.MatchString(filepath.Base(rel)) {
			continue
		}
		full := filepath.Join(dir, rel)
		b, err := os.ReadFile(full) // #nosec G304 -- dir is the mission's own working copy; rel comes from the mission's own git diff
		if err != nil {
			continue
		}
		upper := strings.ToUpper(string(b))
		for _, marker := range disallowedLicenseMarkers {
			if strings.Contains(upper, marker) {
				out = append(out, Finding{
					Path: rel, Rule: "Incompatible license added (" + marker + ")",
					Sample: "file added: " + rel, Severity: SeverityAdvisory,
				})
				break
			}
		}
	}
	return out
}

// govulnLine matches govulncheck's per-vulnerability text-output header, e.g.
// "Vulnerability #1: GO-2024-1234".
var govulnLine = regexp.MustCompile(`(?m)^Vulnerability #\d+:\s*(\S+)`)

// scanGoVuln opportunistically runs `govulncheck ./...` in dir when (a) the
// changed set touches a Go module (go.mod or a .go file is present) and (b)
// the govulncheck binary is on PATH. Neither corral nor this scan requires
// govulncheck to be installed — its absence, a non-Go ecosystem, or a scan
// timeout are all silent no-ops (advisory-only, best-effort). A per-ecosystem
// gap: only Go dependency vulnerabilities are audited; other package managers
// (npm, pip, etc.) get no dependency check yet.
func scanGoVuln(ctx context.Context, dir string, files []string) []Finding {
	if !touchesGoModule(files) {
		return nil
	}
	bin, err := exec.LookPath("govulncheck")
	if err != nil {
		return nil // not installed — opportunistic, not required
	}
	runCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, bin, "./...") // #nosec G204 -- bin resolved via LookPath, fixed arg
	cmd.Dir = dir
	// govulnEnv scrubs the brain's secret env before this runs against
	// untrusted mission source (audit M): no cmd.Env here previously meant
	// govulncheck inherited CORRAL_TOKEN and every other brain secret, and
	// GOTOOLCHAIN=auto would let hostile go.mod content fetch+execute an
	// arbitrary Go toolchain outside the jail. Residual: go/packages still
	// type-checks the target code as part of vuln analysis, and this still
	// runs on the brain host rather than inside the bwrap jail — running
	// govulncheck fully inside the jail is the deeper fix, deferred.
	cmd.Env = govulnEnv()
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	_ = cmd.Run() // non-zero exit is expected when vulnerabilities are found; we parse output either way
	matches := govulnLine.FindAllStringSubmatch(out.String(), -1)
	if len(matches) == 0 {
		return nil
	}
	findings := make([]Finding, 0, len(matches))
	for _, m := range matches {
		findings = append(findings, Finding{
			Rule: "govulncheck: " + m[1], Sample: m[1], Severity: SeverityAdvisory,
		})
	}
	return findings
}

// govulnEnv builds the hardened, secret-free environment scanGoVuln runs
// govulncheck under: sandbox.MinimalEnv() (PATH/HOME/LANG/LC_ALL/TMPDIR only
// — no CORRAL_TOKEN or other brain secrets) plus flags that freeze the
// toolchain and forbid module/cgo mutation, since the mission workdir this
// runs against is untrusted (hostile go.mod/#cgo content, outside the jail).
func govulnEnv() []string {
	return append(sandbox.MinimalEnv(),
		"GOTOOLCHAIN=local", // never fetch+exec a different Go toolchain
		"GOFLAGS=-mod=readonly",
		"CGO_ENABLED=0", // no cgo compiler invocation
		// No-network hardening for tooling run against untrusted mission source:
		// GOPROXY=off forbids module fetches (govulncheck/go list can't pull an
		// attacker-named module) and GOSUMDB=off avoids sum-db network lookups.
		// Fail-safe: a missing dep just makes the advisory vuln scan skip.
		// (Replaces the long-removed no-op GONOSUMDB.)
		"GOPROXY=off",
		"GOSUMDB=off",
	)
}

func touchesGoModule(files []string) bool {
	for _, f := range files {
		if filepath.Base(f) == "go.mod" || strings.HasSuffix(f, ".go") {
			return true
		}
	}
	return false
}

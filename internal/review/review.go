// SPDX-License-Identifier: Elastic-2.0

// Package review is corral as a reviewer: a cold model is handed a scope of
// the repository at a commit and told to assume it is wrong; what it
// returns is an OPINION — prose — carrying structured FINDINGS, each with a
// tier and, above HYPOTHESIS, a reproduction script the run executes
// against the tree. corral never signs the opinion. It records the
// reproductions — the script, what it printed, how it exited — and a
// finding is REPRODUCED on the record only if its script demonstrated the
// claim; a script that did not is demoted to CODE-READ, out loud. The
// review is one ledger entry beside the audits; a human's confirm/refute
// is another (docs/design/adversarial-review.md).
package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pdbethke/corralai/internal/clip"
	"github.com/pdbethke/corralai/internal/reviewrecord"
	"github.com/pdbethke/corralai/internal/treedirs"
)

// The record's types live in reviewrecord (stdlib-only, so a ledger can be
// read in the browser); these aliases keep every review name working.
type (
	Finding    = reviewrecord.Finding
	Review     = reviewrecord.Review
	Refutation = reviewrecord.Refutation
)

// The tiers, as the design names them (declared in reviewrecord).
const (
	TierReproduced = reviewrecord.TierReproduced
	TierCodeRead   = reviewrecord.TierCodeRead
	TierHypothesis = reviewrecord.TierHypothesis
)

// MinSound is the sound list below which a review with no findings is
// recorded as unknown coverage: three items is the least a reviewer that
// actually looked can name.
const MinSound = 3

// CoverageNote is the rule: no findings and fewer than MinSound sound items
// means nothing is known about what the reviewer examined. Set on the
// review by the run and printed; never a gate.
func CoverageNote(r Review) string {
	if len(r.Findings) > 0 || len(r.Sound) >= MinSound {
		return ""
	}
	return fmt.Sprintf("unknown: no findings and %d item(s) checked and found sound (fewer than %d) — a blanket approval, not a review; nothing here is evidence the scope is sound", len(r.Sound), MinSound)
}

// Scope is the files a reviewer is shown: every regular source file under
// the scope path, sorted, up to maxBytes of content in total. Files past
// the cap are listed by name only, and the review records that it was
// truncated — a reviewer told "here is the router" must be told which
// files of it it did not see.
type Scope struct {
	Files     []string
	Contents  map[string]string
	Bytes     int
	Truncated bool
	Unshown   []string
}

// skipDir is the tree a reviewer never needs and a brief cannot afford.
var skipDir = treedirs.NotReviewed()

// LoadScope walks root/scope and reads what fits.
func LoadScope(root, scope string, maxBytes int) (Scope, error) {
	scope = strings.TrimPrefix(filepath.Clean(scope), "./")
	if scope == "." {
		scope = ""
	}
	base := filepath.Join(root, scope)
	if err := insideRoot(root, base); err != nil {
		return Scope{}, fmt.Errorf("review: scope %q: %w", scope, err)
	}
	st, err := os.Stat(base)
	if err != nil {
		return Scope{}, fmt.Errorf("review: scope %q: %w", scope, err)
	}
	var files []string
	if !st.IsDir() {
		files = []string{scope}
	} else {
		err = filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if skipDir[d.Name()] || (strings.HasPrefix(d.Name(), ".") && path != base) {
					return filepath.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			files = append(files, filepath.ToSlash(rel))
			return nil
		})
		if err != nil {
			return Scope{}, fmt.Errorf("review: walking %s: %w", scope, err)
		}
	}
	sort.Strings(files)
	out := Scope{Contents: map[string]string{}}
	for _, f := range files {
		raw, rerr := os.ReadFile(filepath.Join(root, f)) // #nosec G304 -- a file under the repository the operator named
		if rerr != nil || !isText(raw) {
			continue
		}
		if out.Bytes+len(raw) > maxBytes {
			out.Truncated = true
			out.Unshown = append(out.Unshown, f)
			continue
		}
		out.Files = append(out.Files, f)
		out.Contents[f] = string(raw)
		out.Bytes += len(raw)
	}
	if len(out.Files) == 0 {
		return out, errors.New("review: the scope holds no readable text files")
	}
	return out, nil
}

func isText(b []byte) bool {
	n := len(b)
	if n > 512 {
		n = 512
	}
	for _, c := range b[:n] {
		if c == 0 {
			return false
		}
	}
	return true
}

// Reproducer runs one script against the tree and reports what happened.
// It is the seam the substrate hides behind: the jail by default, a detached
// worktree on the host under --substrate workspace (cmd/corral/review.go).
type Reproducer interface {
	Run(ctx context.Context, script string) (stdout string, exitCode int, err error)
}

// Reproduce runs every finding's script and records the outcome. The only
// direction is DOWN: a REPRODUCED claim whose script did not exit 0 — or
// had no script, or could not run — becomes CODE-READ on the record, with
// Demoted saying which; a CODE-READ or HYPOTHESIS claim with a script that
// happens to pass is NOT promoted, because the reviewer did not claim it.
func Reproduce(ctx context.Context, rep Reproducer, r *Review) {
	for i := range r.Findings {
		f := &r.Findings[i]
		if f.Tier == "" {
			f.Tier = f.Declared
		}
		if f.Declared != TierReproduced {
			continue
		}
		if strings.TrimSpace(f.Script) == "" {
			f.Tier, f.Demoted = TierCodeRead, "declared REPRODUCED with no script"
			continue
		}
		out, code, err := rep.Run(ctx, f.Script)
		f.Stdout = clip.Tail(out, 4000)
		if err != nil {
			// Not demonstrated — but not disproved either: nothing ran.
			f.Tier, f.Unrun = TierCodeRead, err.Error()
			f.Demoted = "not run (harness): " + err.Error()
			continue
		}
		if code > 128 && code <= 128+64 {
			// The jail reports a script killed by signal N as exit 128+N:
			// nothing was measured, so it grades nobody, as a harness
			// failure does (review recheck reads the same rule).
			f.Tier, f.Unrun = TierCodeRead, fmt.Sprintf("killed by signal %d (exit %d)", code-128, code)
			f.Demoted = "not run: " + f.Unrun
			continue
		}
		f.ExitCode = &code
		if code != 0 {
			f.Tier, f.Demoted = TierCodeRead, fmt.Sprintf("the script exited %d — it did not demonstrate the claim", code)
		}
	}
}

// reply is the JSON shape the brief asks the reviewer for.
type reply struct {
	Opinion  string `json:"opinion"`
	Findings []struct {
		Claim    string `json:"claim"`
		Tier     string `json:"tier"`
		File     string `json:"file"`
		Line     int    `json:"line"`
		Severity string `json:"severity"`
		Script   string `json:"script"`
	} `json:"findings"`
	Sound []string `json:"sound"`
}

// Parse reads the reviewer's reply — a JSON object, bare or in a fence —
// into findings with ids and declared tiers normalised. An unknown tier is
// recorded as HYPOTHESIS: the reviewer asserted nothing the run can check.
func Parse(text string) (opinion string, findings []Finding, sound []string, err error) {
	// The review's shape is its FINDINGS (or sound) — an object with only
	// an opinion key is any prose-shaped literal, and one before the
	// payload replaced the payload (review a906b2676dca#R2).
	js := extractJSON(text, "findings", "sound")
	if js == "" {
		return "", nil, nil, errors.New("review: the reviewer's reply holds no review object (one with an opinion, findings or sound key)")
	}
	var rp reply
	if uerr := json.Unmarshal([]byte(js), &rp); uerr != nil {
		return "", nil, nil, fmt.Errorf("review: the reviewer's reply is not the requested shape: %w", uerr)
	}
	for i, f := range rp.Findings {
		tier := strings.ToUpper(strings.TrimSpace(f.Tier))
		tier = strings.ReplaceAll(tier, "_", "-")
		switch tier {
		case TierReproduced, TierCodeRead, TierHypothesis:
		default:
			tier = TierHypothesis
		}
		findings = append(findings, Finding{
			ID: fmt.Sprintf("R%d", i+1), Claim: strings.TrimSpace(f.Claim), Declared: tier, Tier: tier,
			File: strings.TrimSpace(f.File), Line: f.Line, Severity: strings.ToLower(strings.TrimSpace(f.Severity)),
			Script: strings.TrimSpace(f.Script),
		})
	}
	return strings.TrimSpace(rp.Opinion), findings, rp.Sound, nil
}

// extractJSON returns the JSON object in text: a fenced ```json block
// first, else the first balanced {...} that actually parses as an object.
// It used to take the first '{' in the text, which is wrong the moment the
// model writes a brace in its prose before the payload — found by `corral
// review` on this file, and the verifier could not refute it.
// extractJSON finds the reply's object: a ```json fence first, else the
// first balanced {...} that parses AND is the requested SHAPE — carries at
// least one of the named keys. Any parseable object used to do, so a JSON
// literal in the prose before the payload ({"tier":"REPRODUCED"}) was
// taken as the review and the findings behind it silently dropped (review
// 61dc210a39fd#R3).
func extractJSON(text string, anyKey ...string) string {
	if i := strings.Index(text, "```json"); i >= 0 {
		rest := text[i+len("```json"):]
		if j := strings.Index(rest, "```"); j >= 0 {
			if c := strings.TrimSpace(rest[:j]); hasShape(c, anyKey) {
				return c
			}
		}
	}
	for start := strings.Index(text, "{"); start >= 0; {
		if c := balancedFrom(text, start); c != "" && hasShape(c, anyKey) {
			return c
		}
		next := strings.Index(text[start+1:], "{")
		if next < 0 {
			break
		}
		start += 1 + next
	}
	return ""
}

// hasShape reports whether c is a JSON object carrying one of the keys
// (any object when no keys are named).
func hasShape(c string, anyKey []string) bool {
	if !strings.HasPrefix(c, "{") {
		return false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(c), &m) != nil {
		return false
	}
	if len(anyKey) == 0 {
		return true
	}
	for _, k := range anyKey {
		if _, ok := m[k]; ok {
			return true
		}
	}
	return false
}

// insideRoot refuses a path that resolves outside root — a scope with ".."
// or through a symlink read files outside the checkout and Brief shipped
// their bytes to the provider (review 61dc210a39fd#R4). Symlinks are
// resolved on both sides so the comparison is of real places.
func insideRoot(root, path string) error {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("repository root: %w", err)
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		if os.IsNotExist(err) {
			return err
		}
		return fmt.Errorf("resolving: %w", err)
	}
	realRoot, _ = filepath.Abs(realRoot)
	realPath, _ = filepath.Abs(realPath)
	if realPath != realRoot && !strings.HasPrefix(realPath, realRoot+string(filepath.Separator)) {
		return fmt.Errorf("outside the repository (%s) — a scope must be inside the checkout under review", realPath)
	}
	return nil
}

// balancedFrom returns the {...} run beginning at start, string-aware, or
// "" when it never closes.
func balancedFrom(text string, start int) string {
	depth, inStr, esc := 0, false, false
	for i := start; i < len(text); i++ {
		c := text[i]
		switch {
		case esc:
			esc = false
		case inStr && c == '\\':
			esc = true
		case c == '"':
			inStr = !inStr
		case inStr:
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return text[start : i+1]
			}
		}
	}
	return ""
}

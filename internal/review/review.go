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
	"time"
)

// The tiers, as the design names them. Declared by the reviewer; RECORDED by
// the run, which only ever demotes.
const (
	TierReproduced = "REPRODUCED"
	TierCodeRead   = "CODE-READ"
	TierHypothesis = "HYPOTHESIS"
)

// Finding is one claim. Declared is the tier the reviewer asserted; Tier is
// the tier the record carries after the run — equal to Declared, or lower,
// with Demoted saying why. A finding's identity within its review is ID
// (R1, R2, …); across the record it is the review entry's hash plus that id.
type Finding struct {
	ID       string `json:"id"`
	Claim    string `json:"claim"`
	Declared string `json:"declared"`
	Tier     string `json:"tier"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Severity string `json:"severity,omitempty"`
	// Script is a POSIX sh script the run executed from the repository root
	// at the reviewed commit. The contract the brief states: exit 0 if and
	// only if the defect is demonstrated, and print the evidence. Stdout is
	// the tail of what it printed (stderr folded in); ExitCode is how it
	// ended, nil when it never ran (no script, or the run refused it).
	Script   string `json:"script,omitempty"`
	Stdout   string `json:"stdout,omitempty"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Demoted  string `json:"demoted,omitempty"`
}

// Review is the record of one review: what was reviewed, by which model,
// on what substrate, the findings as recorded, the sound list, and the
// opinion — the prose, which is carried and never signed on its own.
type Review struct {
	Repo          string    `json:"repo"`
	Commit        string    `json:"commit"`
	Scope         string    `json:"scope"`
	ReviewerModel string    `json:"reviewer_model"`
	Substrate     string    `json:"substrate"`
	StartedAt     time.Time `json:"started_at"`
	Findings      []Finding `json:"findings"`
	// Sound is what the reviewer looked at and could not break. Required:
	// absence of findings in a subsystem nobody looked at is not evidence,
	// and this is what makes the review's scope legible.
	Sound   []string `json:"sound"`
	Opinion string   `json:"opinion"`
	// FilesShown is what the reviewer was actually given, so a reader can
	// tell "found nothing in X" from "never saw X".
	FilesShown   []string `json:"files_shown"`
	BytesShown   int      `json:"bytes_shown"`
	Truncated    bool     `json:"truncated,omitempty"`
	InputTokens  int64    `json:"input_tokens,omitempty"`
	OutputTokens int64    `json:"output_tokens,omitempty"`
}

// Counts summarises the record's tiers.
func (r Review) Counts() (reproduced, codeRead, hypothesis int) {
	for _, f := range r.Findings {
		switch f.Tier {
		case TierReproduced:
			reproduced++
		case TierCodeRead:
			codeRead++
		default:
			hypothesis++
		}
	}
	return
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
var skipDir = map[string]bool{".git": true, "node_modules": true, "vendor": true, ".corral": true, "testdata": true, "dist": true, "build": true, ".venv": true, "__pycache__": true}

// LoadScope walks root/scope and reads what fits.
func LoadScope(root, scope string, maxBytes int) (Scope, error) {
	scope = strings.TrimPrefix(filepath.Clean(scope), "./")
	if scope == "." {
		scope = ""
	}
	base := filepath.Join(root, scope)
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
// It is the seam the substrate hides behind: a detached worktree on the
// workspace substrate today, the jail when it accepts a script.
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
		f.Stdout = tail(out, 4000)
		if err != nil {
			f.Tier, f.Demoted = TierCodeRead, "the script could not run: "+err.Error()
			continue
		}
		f.ExitCode = &code
		if code != 0 {
			f.Tier, f.Demoted = TierCodeRead, fmt.Sprintf("the script exited %d — it did not demonstrate the claim", code)
		}
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
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
	js := extractJSON(text)
	if js == "" {
		return "", nil, nil, errors.New("review: the reviewer's reply holds no JSON object")
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

// extractJSON returns the first balanced {...} object in text, fence or not.
func extractJSON(text string) string {
	start := strings.Index(text, "{")
	if start < 0 {
		return ""
	}
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

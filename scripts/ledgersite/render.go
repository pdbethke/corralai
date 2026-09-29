// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/review"
)

// Outcome is what EXECUTION said about one claim. The four values are kept
// apart on purpose: a script nobody could run is not a claim that failed, and
// neither is a claim with no script. Collapsing them is how a could-not-measure
// becomes a measured zero.
type Outcome string

const (
	OutcomeReproduced Outcome = "reproduced" // ran, exit 0 — the defect was demonstrated
	OutcomeStood      Outcome = "not-shown"  // ran, non-zero — did not demonstrate
	OutcomeUnrun      Outcome = "never-ran"  // the harness could not run it; grades nobody
	OutcomeNoScript   Outcome = "no-script"  // nothing to run was recorded
)

func outcomeOf(script, unrun string, exit *int) (Outcome, string) {
	switch {
	case unrun != "":
		return OutcomeUnrun, unrun
	case strings.TrimSpace(script) == "":
		return OutcomeNoScript, ""
	case exit == nil:
		return OutcomeUnrun, "no exit code recorded"
	case *exit == 0:
		return OutcomeReproduced, ""
	default:
		return OutcomeStood, fmt.Sprintf("exit %d", *exit)
	}
}

// FindingView is one finding plus what execution and a person each said.
type FindingView struct {
	review.Finding
	Outcome     Outcome
	OutcomeNote string
	RefOutcome  Outcome
	RefNote     string
	Verdict     string // "confirmed" / "refuted" / "" when no person has ruled
	VerdictBy   string
	VerdictWhy  string
	Ref         string // "<entry hash>#<finding id>"
}

// EntryView is one review entry as a page.
type EntryView struct {
	Hash       string
	Short      string
	File       string
	Pushed     time.Time
	Review     *review.Review
	Findings   []FindingView
	Reproduced int
	Confirmed  int
	Refuted    int
	CommitURL  string
	ShortSHA   string
	// Decorrelated reports whether a verifier seat ran on a DIFFERENT model
	// than the reviewer. False with no verifier at all — which is not a
	// failure, only an absence, and the page says which it is.
	Decorrelated bool
	HasVerifier  bool
	// Vendor fields are "" when the seat's vendor could not be resolved.
	ReviewerVendor string
	VerifierVendor string
	CrossVendor    bool
	ChainHashOK    bool
	ChainLinkOK    bool
	Signed         bool
	SigOK          bool
	ChainProblem   string
	Genesis        bool
}

// SiteView is the whole record.
type SiteView struct {
	Generated     time.Time
	LedgerEntries int
	Reviews       []EntryView
	Adjudications int
	Retracted     int
	LegacyScans   int
	Findings      int
	Reproduced    int
	Confirmed     int
	Refuted       int
	ChainProblems int
	// SigsChecked is false when no public key was supplied. The page must
	// then say signatures were NOT CHECKED — never imply they passed, and
	// never imply they failed. (`corral verify` sets this standard: "NOTHING
	// was verified ... That is not a pass.")
	// CrossVendor counts reviews whose verifier resolved to a DIFFERENT
	// vendor than the reviewer. VendorUndetermined counts reviews where at
	// least one seat's vendor could not be resolved: those are excluded from
	// CrossVendor rather than assumed, so the number can only understate.
	CrossVendor        int
	CrossModel         int
	VendorUndetermined int
	Vendors            []string
	SigsChecked        bool
	SigsOK             int
	SigsTotal          int
	First, Last        time.Time
	Models             []string
	VerifierOnly       []string
	Tools              []string
	Scopes             []string
	Repo               string
	RepoURL            string
}

func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// repoWebURL turns the recorded remote into a browsable base, so a commit is
// one click from the code it judged. Unrecognized remotes yield "" and the
// page prints the SHA as text rather than guessing a host.
func repoWebURL(remote string) string {
	r := strings.TrimSpace(remote)
	r = strings.TrimSuffix(r, ".git")
	switch {
	case strings.HasPrefix(r, "git@github.com:"):
		return "https://github.com/" + strings.TrimPrefix(r, "git@github.com:")
	case strings.HasPrefix(r, "https://github.com/"):
		return r
	}
	return ""
}

func buildView(entries []auditpush.LedgerEntry, checks []auditpush.ChainCheck, sigsChecked bool) SiteView {
	byFile := map[string]auditpush.ChainCheck{}
	for _, c := range checks {
		byFile[filepath.Base(c.File)] = c
	}
	verdicts := auditpush.Adjudications(entries)
	retracted := auditpush.Retracted(entries)

	v := SiteView{
		Generated:     time.Now().UTC(),
		LedgerEntries: len(entries),
		Retracted:     len(retracted),
		SigsChecked:   sigsChecked,
	}
	models := map[string]bool{}
	verifiers := map[string]bool{}
	tools := map[string]bool{}
	scopes := map[string]bool{}
	vendors := map[string]bool{}

	for _, e := range entries {
		if v.First.IsZero() || e.Pushed.Before(v.First) {
			v.First = e.Pushed
		}
		if e.Pushed.After(v.Last) {
			v.Last = e.Pushed
		}
		c := byFile[filepath.Base(e.File)]
		if c.Problem != "" || !c.HashOK || !c.LinkOK {
			v.ChainProblems++
		}
		if c.Signed {
			v.SigsTotal++
			if c.SigOK {
				v.SigsOK++
			}
		}
		switch e.Kind {
		case auditpush.KindAdjudication:
			v.Adjudications++
			continue
		case auditpush.KindScan:
			v.LegacyScans++
			continue
		case auditpush.KindReview:
		default:
			continue
		}
		if e.Review == nil {
			continue
		}
		r := e.Review
		ev := EntryView{
			Hash: e.Hash, Short: shortHash(e.Hash), File: filepath.Base(e.File),
			Pushed: e.Pushed, Review: r,
			ChainHashOK: c.HashOK, ChainLinkOK: c.LinkOK, Signed: c.Signed,
			SigOK: c.SigOK, ChainProblem: c.Problem, Genesis: c.Genesis,
		}
		if base := repoWebURL(r.Repo); base != "" && r.Commit != "" {
			ev.CommitURL = base + "/commit/" + r.Commit
			if v.RepoURL == "" {
				v.RepoURL, v.Repo = base, r.Repo
			}
		}
		if len(r.Commit) > 10 {
			ev.ShortSHA = r.Commit[:10]
		} else {
			ev.ShortSHA = r.Commit
		}
		ev.HasVerifier = strings.TrimSpace(r.VerifierModel) != ""
		ev.Decorrelated = ev.HasVerifier && r.VerifierModel != r.ReviewerModel
		if ev.Decorrelated {
			v.CrossModel++
		}
		ev.ReviewerVendor = vendorOfSeat(r.ReviewerModel)
		if ev.ReviewerVendor != "" {
			vendors[ev.ReviewerVendor] = true
		}
		if ev.HasVerifier {
			ev.VerifierVendor = vendorOfSeat(r.VerifierModel)
			if ev.VerifierVendor != "" {
				vendors[ev.VerifierVendor] = true
			}
			switch {
			case ev.ReviewerVendor == "" || ev.VerifierVendor == "":
				v.VendorUndetermined++
			case ev.ReviewerVendor != ev.VerifierVendor:
				ev.CrossVendor = true
				v.CrossVendor++
			}
		}

		if r.ReviewerModel != "" {
			models[r.ReviewerModel] = true
		}
		if ev.HasVerifier {
			verifiers[r.VerifierModel] = true
		}
		if r.ReviewerTool != "" {
			tools[r.ReviewerTool] = true
		}
		if r.Scope != "" {
			scopes[r.Scope] = true
		}

		for _, f := range r.Findings {
			fv := FindingView{Finding: f, Ref: e.Hash + "#" + f.ID}
			fv.Outcome, fv.OutcomeNote = outcomeOf(f.Script, f.Unrun, f.ExitCode)
			if f.Refutation != nil {
				fv.RefOutcome, fv.RefNote = outcomeOf(f.Refutation.Script, f.Refutation.Unrun, f.Refutation.ExitCode)
			}
			if a, ok := verdicts[fv.Ref]; ok {
				fv.Verdict, fv.VerdictBy, fv.VerdictWhy = a.Verdict, a.By, a.Reason
			}
			switch fv.Outcome {
			case OutcomeReproduced:
				ev.Reproduced++
				v.Reproduced++
			}
			switch fv.Verdict {
			case auditpush.VerdictConfirmed:
				ev.Confirmed++
				v.Confirmed++
			case auditpush.VerdictRefuted:
				ev.Refuted++
				v.Refuted++
			}
			v.Findings++
			ev.Findings = append(ev.Findings, fv)
		}
		v.Reviews = append(v.Reviews, ev)
	}

	sort.Slice(v.Reviews, func(i, j int) bool { return v.Reviews[i].Pushed.After(v.Reviews[j].Pushed) })
	v.Models = keys(models)
	v.VerifierOnly = keys(verifiers)
	v.Tools = keys(tools)
	v.Scopes = keys(scopes)
	v.Vendors = keys(vendors)
	return v
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func writeSite(out string, v SiteView) error {
	// Writes go through os.Root, the same defence the workspace substrate
	// uses: every path is resolved INSIDE out, so a symlink planted in the
	// output directory cannot redirect a write anywhere else. 0o750 rather
	// than 0o755 — nothing here needs to be world-readable on the machine
	// that builds it; the deploy copies the files out.
	if err := os.MkdirAll(out, 0o750); err != nil {
		return err
	}
	root, err := os.OpenRoot(out)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.Mkdir("entry", 0o750); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}

	funcs := template.FuncMap{
		"short":    shortHash,
		"hasPre":   func(s string) bool { return strings.TrimSpace(s) != "" },
		"date":     func(t time.Time) string { return t.UTC().Format("2006-01-02") },
		"stamp":    func(t time.Time) string { return t.UTC().Format("2006-01-02 15:04 UTC") },
		"join":     func(s []string) string { return strings.Join(s, ", ") },
		"lower":    strings.ToLower,
		"entryURL": func(e EntryView) string { return "entry/" + e.Short + ".html" },
	}

	idx, err := template.New("index").Funcs(funcs).Parse(indexTmpl)
	if err != nil {
		return err
	}
	if err := writeThrough(root, "index.html", idx, v); err != nil {
		return err
	}

	ent, err := template.New("entry").Funcs(funcs).Parse(entryTmpl)
	if err != nil {
		return err
	}
	for _, e := range v.Reviews {
		// The name is a hash prefix, so anything outside hex is a record this
		// program does not understand — refuse rather than write it.
		if !isHex(e.Short) {
			return fmt.Errorf("entry %q: name is not a hex hash prefix", e.Short)
		}
		data := struct {
			EntryView
			Site SiteView
		}{e, v}
		if err := writeThrough(root, "entry/"+e.Short+".html", ent, data); err != nil {
			return err
		}
	}
	return nil
}

// writeThrough renders tmpl into name, resolved inside root.
func writeThrough(root *os.Root, name string, tmpl *template.Template, data any) error {
	f, err := root.Create(name)
	if err != nil {
		return err
	}
	if err := tmpl.Execute(f, data); err != nil {
		f.Close()
		return fmt.Errorf("%s: %w", name, err)
	}
	return f.Close()
}

func isHex(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

// seatModel splits a review seat ("codex:gpt-6-astra") into the model half.
// A bare seat ("claude-code", "gemini-3.6-flash") is returned whole. This
// mirrors agentSeat in cmd/corral/review_agent.go, which is in package main
// and so cannot be imported; the split is one line and the two must agree.
func seatModel(seat string) string {
	if i := strings.Index(seat, ":"); i >= 0 {
		return seat[i+1:]
	}
	return seat
}

func seatTool(seat string) string {
	if i := strings.Index(seat, ":"); i >= 0 {
		return seat[:i]
	}
	return seat
}

// toolVendor maps the agentic CLIs review_agent.go can drive to the vendor
// whose model they run BY DEFAULT, for seats recorded with no explicit model
// ("codex", "claude-code"). agentbackend.VendorOf deliberately answers only
// about MODEL names, so this is an extension for tools rather than a second
// copy of that rule — and it is consulted only after VendorOf declines.
var toolVendor = map[string]string{
	"claude-code": "anthropic",
	"codex":       "openai",
	"antigravity": "google",
}

// vendorOfSeat resolves a seat to a vendor, or "" when it cannot. Returning
// "" matters: an undetermined vendor is excluded from the cross-vendor count
// rather than guessed into it, so the headline can only understate.
func vendorOfSeat(seat string) string {
	if v := agentbackend.VendorOf(seatModel(seat)); v != "" {
		return v
	}
	return toolVendor[seatTool(seat)]
}

// SPDX-License-Identifier: Elastic-2.0

// Package recordview renders a corral ledger as pages: the index, a page
// per review and per audit, the chain and the verdict queue. It is the ONE
// renderer for the record. The public site writes its pages to disk
// (WriteStatic) and `corral ui` serves the same pages on request (Handler),
// so the two cannot drift the way scripts/ledgersite and uiweb/index.html
// did (spec 2026-10-18-corral-app-design.md §5).
//
// Every rule about what the record MEANS comes from internal/auditpush.
// This package adds no second opinion about it.
package recordview

import (
	"fmt"
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

// ChainView is what the chain check said about one entry, and where the
// entry sits. Review and scan pages share it, so both say the same thing
// about a hash, a link and a signature.
type ChainView struct {
	Hash   string
	Short  string
	File   string
	Pushed time.Time
	// Verified is false when the verifying pass never saw this entry (it
	// was appended while the ledger was read). The chain fields below are
	// then unmeasured, and every page says "not verified" instead of them.
	Verified    bool
	ChainHashOK bool
	ChainLinkOK bool
	Signed      bool
	SigOK       bool
	// Signer is who the chain says held the signing key at this entry
	// ("root" or a registered name), resolved from the chain rather than
	// taken from the entry's own KeyID. Only SigOK says it was checked.
	Signer       string
	ChainProblem string
	Genesis      bool

	// Pos is the entry's 1-based place in the chain. Kind is "scan" for a
	// run's own record, else the entry's kind. KeyID is the signer name
	// the entry carries; it is self-reported unless the Note says the
	// format covers it. Note is VerifyLedgerDir's caveat, in words.
	// KeyGiven is false when no public key was supplied, so a signed
	// entry's signature is unchecked, never "verified" and never "failed".
	Pos      int
	Kind     string
	KeyID    string
	Note     string
	KeyGiven bool
}

// EntryView is one review entry as a page.
type EntryView struct {
	ChainView
	Review     *review.Review
	Findings   []FindingView
	Reproduced int
	Confirmed  int
	Refuted    int
	CommitURL  string
	ShortSHA   string
	// Retracted is true when a retraction in force names this review. The
	// page stays, so a link to it does not break, and says why it no longer
	// stands. Its findings are left out of the record's totals.
	Retracted       bool
	RetractedReason string
	// Decorrelated reports whether a verifier seat ran on a DIFFERENT model
	// than the reviewer. False with no verifier at all — which is not a
	// failure, only an absence, and the page says which it is.
	Decorrelated bool
	HasVerifier  bool
	// Vendor fields are "" when the seat's vendor could not be resolved.
	ReviewerVendor string
	VerifierVendor string
	CrossVendor    bool
}

// Site is the whole record.
type Site struct {
	// Nav is the menu a site build puts at the top of every page, so a
	// reader who lands on a record page from a link can reach the rest of
	// the site and learn what corral is. corral ui leaves it empty: one
	// person's own ledger needs no site menu.
	Nav []NavLink

	// Write renders the adjudication controls. Only corral ui --write sets
	// it, per request; a static build never does.
	Write bool

	Generated time.Time
	// HasLedger is false when no ledger directory stands behind this site
	// (corral ui pointed at a warehouse): it then makes no claim about a chain.
	HasLedger     bool
	LedgerEntries int
	// Seal is the warehouse's newest kill-rate-bearing row per file, handed
	// in by the caller (see WithSeal). SealRead distinguishes "not read
	// here" (static build: no section) from "read, and no rows"; SealErr is
	// a failed read, rendered as one.
	Seal     []SealRow
	SealRead bool
	SealErr  string
	// PubKeyHex is the key the signatures were checked against, printed in
	// the "verify it yourself" command so a reader can run the same check.
	// Empty when the page was rendered without one.
	PubKeyHex string
	Reviews   []EntryView
	// Chain is every entry of every kind, newest first: the chain itself,
	// with each entry's check. Scan and review pages hold the detail.
	Chain []ChainView
	// Scans is every certify scan entry, newest first, retracted ones
	// included and marked: a retracted scan stays in the chain, so its page
	// stays too and says why it no longer stands.
	Scans         []ScanView
	Retracted     int
	Findings      int
	Reproduced    int
	Confirmed     int
	Refuted       int
	ChainProblems int
	// Unverified counts entries the verifying pass never saw (the ledger
	// changed between the read and the check). They are not chain problems:
	// nothing measured them, so the page says "not verified", not failed.
	Unverified int
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

// Build turns the ledger's entries and VerifyLedgerDir's checks of them
// into a Site. sigsChecked says whether a public key was given to the checks.
func Build(entries []auditpush.LedgerEntry, checks []auditpush.ChainCheck, sigsChecked bool) Site {
	paired := PairChecks(entries, checks)
	// Only verdicts that STAND: LiveEntries drops a retracted adjudication
	// and an adjudication of a retracted review. Adjudications over every
	// entry counted both (found 2026-10-18 by comparing this page with
	// corral ui, which already read the live set).
	verdicts := auditpush.Adjudications(auditpush.LiveEntries(entries))
	retracted := auditpush.Retracted(entries)
	live := map[string]bool{}
	for _, e := range auditpush.LiveEntries(entries) {
		live[e.Hash] = true
	}

	v := Site{
		Generated:     time.Now().UTC(),
		HasLedger:     true,
		LedgerEntries: len(entries),
		Retracted:     len(retracted),
		SigsChecked:   sigsChecked,
	}
	models := map[string]bool{}
	verifiers := map[string]bool{}
	tools := map[string]bool{}
	scopes := map[string]bool{}
	vendors := map[string]bool{}

	for i, e := range entries {
		if v.First.IsZero() || e.Pushed.Before(v.First) {
			v.First = e.Pushed
		}
		if e.Pushed.After(v.Last) {
			v.Last = e.Pushed
		}
		c := paired[i]
		switch {
		case !c.Verified:
			v.Unverified++
		case c.Problem != "" || !c.HashOK || !c.LinkOK:
			v.ChainProblems++
		}
		if c.Signed {
			v.SigsTotal++
			if c.SigOK {
				v.SigsOK++
			}
		}
		kind := e.Kind
		if e.IsScan() {
			kind = "scan"
		}
		cv := ChainView{
			Hash: e.Hash, Short: shortHash(e.Hash), File: filepath.Base(e.File),
			Pushed: e.Pushed, Verified: c.Verified,
			ChainHashOK: c.HashOK, ChainLinkOK: c.LinkOK, Signed: c.Signed,
			SigOK: c.SigOK, Signer: c.Signer, ChainProblem: c.Problem, Genesis: c.Genesis,
			Pos: i + 1, Kind: kind, KeyID: c.KeyID, Note: c.Note, KeyGiven: sigsChecked,
		}
		// The chain shows every entry (it IS the chain), but one that does
		// not stand says so there, whatever its kind. Only the chain row
		// carries it: an entry's own page has its retraction banner.
		row := cv
		if rt, gone := retracted[e.Hash]; gone {
			row.Note = withNote(row.Note, "RETRACTED: "+rt.Reason)
		} else if !live[e.Hash] && e.Kind == auditpush.KindAdjudication {
			row.Note = withNote(row.Note, "RETRACTED with the review it adjudicates")
		}
		v.Chain = append(v.Chain, row)
		if e.IsScan() {
			var by *auditpush.LedgerEntry
			if rt, ok := retracted[e.Hash]; ok {
				by = &rt
			}
			v.Scans = append(v.Scans, buildScanView(e, cv, by))
			continue
		}
		switch e.Kind {
		case auditpush.KindReview:
		default:
			continue
		}
		if e.Review == nil {
			continue
		}
		r := e.Review
		ev := EntryView{ChainView: cv, Review: r}
		if rt, ok := retracted[e.Hash]; ok {
			ev.Retracted, ev.RetractedReason = true, rt.Reason
		}
		if base := repoWebURL(r.Repo); base != "" && r.Commit != "" {
			ev.CommitURL = base + "/commit/" + r.Commit
			if v.RepoURL == "" {
				v.RepoURL, v.Repo = base, r.Repo
			}
		}
		ev.ShortSHA = shortSHA(r.Commit)
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
			if fv.Outcome == OutcomeReproduced {
				ev.Reproduced++
				if !ev.Retracted {
					v.Reproduced++
				}
			}
			switch fv.Verdict {
			case auditpush.VerdictConfirmed:
				ev.Confirmed++
				if !ev.Retracted {
					v.Confirmed++
				}
			case auditpush.VerdictRefuted:
				ev.Refuted++
				if !ev.Retracted {
					v.Refuted++
				}
			}
			if !ev.Retracted {
				v.Findings++
			}
			ev.Findings = append(ev.Findings, fv)
		}
		v.Reviews = append(v.Reviews, ev)
	}

	sort.Slice(v.Chain, func(i, j int) bool { return v.Chain[i].Pos > v.Chain[j].Pos })
	sort.Slice(v.Reviews, func(i, j int) bool { return v.Reviews[i].Pushed.After(v.Reviews[j].Pushed) })
	sort.Slice(v.Scans, func(i, j int) bool { return v.Scans[i].Pushed.After(v.Scans[j].Pushed) })
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

// vendorOfSeat resolves a seat to a vendor, or "" when it cannot. Returning
// "" matters: an undetermined vendor is excluded from the cross-vendor count
// rather than guessed into it, so the headline can only understate.
func vendorOfSeat(seat string) string {
	// RecordedVendorOf, not VendorOf: a signed historical entry was run by
	// the built-in agent it names, whatever CORRALAI_AGENT_* this renderer's
	// own environment defines.
	return agentbackend.RecordedVendorOf(seat)
}

// AwaitingView is one finding waiting for a person's verdict.
type AwaitingView struct {
	Review  EntryView
	Finding FindingView
}

// Awaiting is every finding of a review that stands with no verdict that
// stands: reproduced findings first (they are the ones execution backed),
// then newest review first, as Reviews is already ordered.
func (s Site) Awaiting() []AwaitingView {
	var repro, rest []AwaitingView
	for _, r := range s.Reviews {
		if r.Retracted {
			continue
		}
		for _, f := range r.Findings {
			if f.Verdict != "" {
				continue
			}
			a := AwaitingView{Review: r, Finding: f}
			if f.Outcome == OutcomeReproduced {
				repro = append(repro, a)
			} else {
				rest = append(rest, a)
			}
		}
	}
	return append(repro, rest...)
}

// withNote adds a standing note (a retraction) to the verifier's note
// rather than replacing it: the verifier's caveat (a legacy entry's
// self-reported signer name) qualifies the signature chip beside it, and
// overwriting it left the chip unqualified on exactly the retracted legacy
// entries (review 786e8a0c675e#R2). Moved here from corral ui's retired
// ledger view.
func withNote(verifierNote, note string) string {
	if verifierNote == "" {
		return note
	}
	return note + " · " + verifierNote
}

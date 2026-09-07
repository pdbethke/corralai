// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"crypto/ed25519"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/review"
)

// The ledger half of `corral ui`: what the seal cannot show. The seal is
// the newest verdict per file; the ledger is everything else the
// directory holds — the chain itself (is it intact, who signed what), the
// reviews with their findings, the verifier's refutations, and a person's
// adjudications. A record nobody can read is not a record.

type uiLedger struct {
	Dir      string      `json:"dir"`
	Entries  []uiEntry   `json:"entries"`
	Problems int         `json:"problems"`
	Reviews  []uiReview  `json:"reviews"`
	Retracts []uiRetract `json:"retractions"`
	Verified string      `json:"verified_against"` // what signatures were checked against, "" when nothing
	Checked  time.Time   `json:"checked_at"`
}

type uiEntry struct {
	Pos      int       `json:"pos"`
	Kind     string    `json:"kind"`
	When     time.Time `json:"when"`
	Commit   string    `json:"commit"`
	Hash     string    `json:"hash"`
	Signed   bool      `json:"signed"`
	Verified bool      `json:"verified"`
	KeyID    string    `json:"keyid"`
	Note     string    `json:"note"`
	Problem  string    `json:"problem"`
	// What a scan entry is about, for the chain table.
	Repo    string `json:"repo,omitempty"`
	Audited int    `json:"audited,omitempty"`
	Files   int    `json:"files,omitempty"`
	Mutants int    `json:"mutants,omitempty"`
}

type uiRetract struct {
	Pos      int    `json:"pos"`
	Retracts string `json:"retracts"`
	Reason   string `json:"reason"`
}

type uiReview struct {
	Pos             int         `json:"pos"`
	Hash            string      `json:"hash"`
	When            time.Time   `json:"when"`
	Repo            string      `json:"repo"`
	Commit          string      `json:"commit"`
	Scope           string      `json:"scope"`
	Reviewer        string      `json:"reviewer"`
	Verifier        string      `json:"verifier,omitempty"`
	Reproduced      int         `json:"reproduced"`
	CodeRead        int         `json:"code_read"`
	Hypothesis      int         `json:"hypothesis"`
	Opinion         string      `json:"opinion"`
	VerifierOpinion string      `json:"verifier_opinion,omitempty"`
	Sound           []string    `json:"sound"`
	FilesShown      int         `json:"files_shown"`
	Truncated       bool        `json:"truncated"`
	Statement       string      `json:"statement_sha256,omitempty"`
	Findings        []uiFinding `json:"findings"`
}

type uiFinding struct {
	review.Finding
	Adjudication *auditpush.Adjudication `json:"adjudication,omitempty"`
}

// readUILedger builds the ledger view: one walk of the chain (the same
// VerifyLedgerDir the CLI runs, against the local certify key when there
// is one), then the reviews with adjudications applied.
func readUILedger(dir string) (uiLedger, error) {
	var pub ed25519.PublicKey
	verified := ""
	if priv, err := loadLocalCertifyKeyIfConfigured(); err == nil {
		pub, verified = priv.Public().(ed25519.PublicKey), "the local certify key"
	}
	checks, err := auditpush.VerifyLedgerDir(dir, pub)
	if err != nil {
		return uiLedger{}, err
	}
	entries, err := auditpush.ReadLedgerDir(dir)
	if err != nil {
		return uiLedger{}, err
	}
	out := uiLedger{Dir: dir, Verified: verified, Checked: time.Now().UTC()}
	adj := auditpush.Adjudications(entries)
	retracted := auditpush.Retracted(entries)
	for i, e := range entries {
		c := auditpush.ChainCheck{}
		if i < len(checks) {
			c = checks[i]
		}
		kind := e.Kind
		if kind == auditpush.KindScan {
			kind = "scan"
		}
		u := uiEntry{Pos: i + 1, Kind: kind, When: e.Pushed, Commit: e.Bundle.Scan.Commit, Hash: e.Hash,
			Signed: c.Signed, Verified: c.SigOK, KeyID: c.KeyID, Note: c.Note, Problem: c.Problem}
		if c.Problem != "" {
			out.Problems++
		}
		switch e.Kind {
		case auditpush.KindScan:
			u.Repo, u.Audited, u.Files, u.Mutants = e.Bundle.Scan.Repo, e.Bundle.Scan.Audited, len(e.Bundle.Files), len(e.Bundle.Mutants)
			if r, gone := retracted[e.Hash]; gone {
				u.Note = "RETRACTED: " + r.Reason
			}
		case auditpush.KindRetract:
			out.Retracts = append(out.Retracts, uiRetract{Pos: i + 1, Retracts: e.Retracts, Reason: e.Reason})
		case auditpush.KindReview:
			if e.Review != nil {
				r := *e.Review
				rep, cr, hy := r.Counts()
				v := uiReview{Pos: i + 1, Hash: e.Hash, When: e.Pushed, Repo: r.Repo, Commit: r.Commit, Scope: r.Scope,
					Reviewer: r.ReviewerModel, Verifier: r.VerifierModel, Reproduced: rep, CodeRead: cr, Hypothesis: hy,
					Opinion: r.Opinion, VerifierOpinion: r.VerifierOpinion, Sound: r.Sound, FilesShown: len(r.FilesShown),
					Truncated: r.Truncated, Statement: r.StatementSHA256}
				u.Commit = r.Commit
				for _, f := range r.Findings {
					uf := uiFinding{Finding: f}
					if a, ok := adj[e.Hash+"#"+f.ID]; ok {
						a := a
						uf.Adjudication = &a
					}
					v.Findings = append(v.Findings, uf)
				}
				out.Reviews = append(out.Reviews, v)
			}
		}
		out.Entries = append(out.Entries, u)
	}
	// Newest first, the way a person reads a log.
	for i, j := 0, len(out.Entries)-1; i < j; i, j = i+1, j-1 {
		out.Entries[i], out.Entries[j] = out.Entries[j], out.Entries[i]
	}
	for i, j := 0, len(out.Reviews)-1; i < j; i, j = i+1, j-1 {
		out.Reviews[i], out.Reviews[j] = out.Reviews[j], out.Reviews[i]
	}
	return out, nil
}

// uiLedgerDir is the directory the UI was pointed at, "" when --db named a
// warehouse file or md: — the ledger sections then have nothing to show
// and say so.
func uiLedgerDir(target string) string {
	t := strings.TrimSpace(target)
	if t == "" || strings.HasPrefix(t, "md:") || !auditpush.IsLedgerDir(t) {
		return ""
	}
	return strings.TrimRight(t, "/")
}

// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"strings"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/recordview"
)

// uiSite is what corral ui hands recordview: on every request, the ledger
// (when the target is a ledger directory) read afresh, the seal from the
// same reader `corral seal` uses, and whether this server may write. The
// pages themselves are recordview's, the same ones the public record
// page is built from.
func uiSite(st sealReader, ledgerDir, ledgerRepo string, self []string, write bool) func() (recordview.Site, error) {
	return func() (recordview.Site, error) {
		s := recordview.Site{}
		if ledgerDir != "" {
			// Explicit key only: never the local certify key, which did not
			// sign a ledger someone else wrote.
			pub, _, err := auditpush.ResolveLedgerPubKey("")
			if err != nil {
				return recordview.Site{}, err
			}
			trust, err := ledgerTrust(pub, ledgerRepo)
			if err != nil {
				return recordview.Site{}, err
			}
			if s, err = recordview.LoadTrust(ledgerDir, trust); err != nil {
				return recordview.Site{}, err
			}
			// Every load, like the rest of the page: the Languages page is
			// computed from the entries this request read. An empty self
			// renders the page's refusal, never an unfiltered table.
			s = s.WithLanguages(lang.Names(), runnerLanguages(), self, "--self")
		}
		rows, err := st.SealRows(context.Background(), "")
		s = s.WithSeal(sealRowsForView(rows), err)
		s.Write = write
		return s, nil
	}
}

// runnerLanguages is the runner table's ID to languages, which the Languages
// page needs to say which language a run exercised. Not shared with
// scripts/ledgersite: recordview must not import lang, and two lines are not
// worth a package.
func runnerLanguages() map[string][]string {
	m := map[string][]string{}
	for _, r := range lang.Runners() {
		m[r.ID] = r.Languages
	}
	return m
}

// sealRowsForView converts the warehouse's rows to the page's. Every field
// the page shows is carried, and a test holds it to that.
func sealRowsForView(rows []sealRow) []recordview.SealRow {
	out := make([]recordview.SealRow, 0, len(rows))
	for _, r := range rows {
		v := recordview.SealRow{Repo: r.Repo, Path: r.Path, KillRate: r.KillRate, Survivors: r.Survivors,
			ProvenMissed: r.ProvenMissed, Caveat: r.caveat()}
		if r.AuditedAt != nil {
			v.AuditedAt = *r.AuditedAt
		}
		out = append(out, v)
	}
	return out
}

// uiLedgerDir is the directory the UI was pointed at, "" when --db named a
// warehouse file or md: — the record pages then carry the seal alone and
// make no claim about a chain.
func uiLedgerDir(target string) string {
	t := strings.TrimSpace(target)
	if t == "" || strings.HasPrefix(t, "md:") || !auditpush.IsLedgerDir(t) {
		return ""
	}
	return strings.TrimRight(t, "/")
}

// SPDX-License-Identifier: Elastic-2.0

package auditpush

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
)

// PushPlan is what a ledger push would (or did) write, entry by entry.
type PushPlan struct {
	Scans, Reviews, Adjudications int // entries to push
	Skipped                       int // already in the target (by entry hash)
	Retracted                     int // scan entries a later entry retracted — not the record, not pushed
	NotPushable                   int // retractions and checkpoints: chain facts, no warehouse row
	Files, Findings               int // rows those entries carry
}

// PushLedgerDir pushes a ledger directory's record to a warehouse the
// operator owns: every scan entry (retracted ones left out — they are not
// the record), every review and adjudication entry, skipping any entry
// the target already holds by its hash. Retractions and checkpoints are
// facts about the chain, not rows, and are counted, not pushed. Scripts
// and source follow withSource, the same custody switch --push-source is.
// dryRun plans and writes nothing.
func PushLedgerDir(dir, target string, withSource, dryRun bool) (PushPlan, error) {
	entries, err := ReadLedgerDir(dir)
	if err != nil {
		return PushPlan{}, err
	}
	var db *sql.DB
	have := map[string]bool{}
	if dryRun {
		// A plan writes nothing — not even the file, not even a table. A
		// target that does not exist holds nothing; one that does is read
		// as it is, missing tables meaning nothing of that kind yet.
		if strings.HasPrefix(target, "md:") || fileExists(target) {
			ro, err := openWarehouseReadOnly(target)
			if err != nil {
				return PushPlan{}, err
			}
			defer ro.Close()
			if have, err = pushedEntryHashes(ro); err != nil {
				return PushPlan{}, err
			}
		}
	} else {
		var err error
		if db, err = openWarehouseForWrite(target); err != nil {
			return PushPlan{}, err
		}
		defer db.Close()
		if have, err = pushedEntryHashes(db); err != nil {
			return PushPlan{}, err
		}
	}
	// Retracted entries of EVERY kind stay out of a warehouse — a retracted
	// review's rows used to push while its scan's did not (ed079ca08965#R3).
	live := map[string]bool{}
	for _, e := range LiveEntries(entries) {
		live[e.Hash] = true
	}
	var plan PushPlan
	for _, e := range entries {
		switch e.Kind {
		case KindRetract, KindCheckpoint:
			plan.NotPushable++
			continue
		}
		if have[e.Hash] {
			plan.Skipped++
			continue
		}
		if !live[e.Hash] {
			plan.Retracted++
			continue
		}
		switch e.Kind {
		case KindScan:
			plan.Scans++
			plan.Files += len(e.Bundle.Files)
			if dryRun {
				continue
			}
			b := e.Bundle
			// --push-source can only carry source the ENTRY actually holds.
			// This stamped withSource unconditionally, so an entry written
			// with its source already blanked was pushed carrying the
			// custody claim "our code left the box" for bytes that never
			// existed in it — the row asserting a custody nobody can
			// produce. Found by a cold review, 2026-09-08 (R4).
			carriesSource := e.Bundle.SourcePushed
			b.SourcePushed, b.Scan.SourcePushed = withSource && carriesSource, withSource && carriesSource
			b.Scan.EntryHash = e.Hash
			b = stampLink(b)
			BlankUnpushedSource(&b)
			CanonicalizeForWarehouse(&b)
			if err := requireStatements(b); err != nil {
				return plan, err
			}
			if _, err := insertBundle(db, b, e.Pushed, e.ScanUID); err != nil {
				return plan, fmt.Errorf("auditpush: pushing entry %.12s: %w", e.Hash, err)
			}
		case KindReview:
			plan.Reviews++
			if e.Review != nil {
				plan.Findings += len(e.Review.Findings)
			}
			if dryRun {
				continue
			}
			if _, err := pushReviewEntryTx(db, e, withSource); err != nil {
				return plan, err
			}
		case KindAdjudication:
			plan.Adjudications++
			if dryRun {
				continue
			}
			if _, err := pushReviewEntryTx(db, e, withSource); err != nil {
				return plan, err
			}
		}
	}
	return plan, nil
}

// pushedEntryHashes is every ledger entry the warehouse already holds:
// scans by corral_scans.entry_hash, reviews by review_uid, adjudications
// by entry_hash. A scan pushed by an older corral (no entry_hash) is not
// recognised and would be pushed again — said in the verb's help.
func pushedEntryHashes(db *sql.DB) (map[string]bool, error) {
	have := map[string]bool{}
	for _, q := range []string{
		`SELECT entry_hash FROM corral_scans WHERE entry_hash IS NOT NULL`,
		`SELECT review_uid FROM corral_reviews WHERE review_uid IS NOT NULL`,
		`SELECT entry_hash FROM corral_adjudications WHERE entry_hash IS NOT NULL`,
	} {
		rows, err := db.Query(q)
		if err != nil {
			// No table of this kind yet, or a table an older corral wrote
			// before the column existed: nothing held that this push can
			// recognise (a write opens the target with EnsureSchema, so the
			// column is there by the time rows are added).
			if strings.Contains(err.Error(), "does not exist") || strings.Contains(err.Error(), "not found in FROM clause") {
				continue
			}
			return nil, fmt.Errorf("auditpush: reading what the warehouse holds: %w", err)
		}
		for rows.Next() {
			var h string
			if err := rows.Scan(&h); err != nil {
				rows.Close()
				return nil, err
			}
			have[h] = true
		}
		rows.Close()
	}
	return have, nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// openWarehouseReadOnly attaches target read-only as "warehouse" — for a
// plan, which must not create a file or a table.
func openWarehouseReadOnly(target string) (*sql.DB, error) {
	db, err := sql.Open("duckdb", "")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if strings.HasPrefix(target, "md:") {
		if _, err := db.Exec("INSTALL motherduck; LOAD motherduck;"); err != nil {
			db.Close()
			return nil, fmt.Errorf("auditpush: load motherduck extension: %w", err)
		}
	}
	attach := fmt.Sprintf("ATTACH '%s' AS warehouse (READ_ONLY)", strings.ReplaceAll(target, "'", "''"))
	if strings.HasPrefix(target, "md:") {
		attach = fmt.Sprintf("ATTACH '%s' AS warehouse", strings.ReplaceAll(target, "'", "''"))
	}
	if _, err := db.Exec(attach + "; USE warehouse"); err != nil {
		db.Close()
		return nil, fmt.Errorf("auditpush: attach %q: %w", target, err)
	}
	return db, nil
}

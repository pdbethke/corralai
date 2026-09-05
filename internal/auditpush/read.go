// SPDX-License-Identifier: Elastic-2.0

package auditpush

import (
	"database/sql"
	"fmt"
)

// ReadBundle reconstructs the Bundle PushBundle wrote for one (repo, scanID)
// — the exact inverse of its five INSERTs, column for column — straight
// from the warehouse. It exists for `corral verify --db`: a verifier
// holding only the pushed warehouse (not the original ledger the run
// produced) needs to rebuild the SAME Go-typed structure PushBundle hashed,
// so warehouseRowsSHA256 (cmd/corral/certify_repo.go) can be recomputed
// from it and checked against the statement's claim — the same function,
// never a second, hand-rolled hash.
//
// FAITHFULNESS: every nullable column here inverts the exact write-side
// rule (see insertFileRow and PushBundle) — e.g. kill_rate reads back as
// *float64 nil exactly when the row is Uncovered or was never measured,
// matching insertFileRow's `if r.KillRate != nil && !r.Uncovered`. This is
// PROVABLY faithful for any bundle this package's own writer produced (the
// two directions are inverses by construction); it is not a claim that an
// arbitrary hand-built Row survives the round trip unchanged — see
// warehouseRowsSHA256's own doc for the two theoretical edge cases
// (Uncovered with a non-nil KillRate; a non-nil TestsPerMutant with
// PerMutant false) that a REAL run's own writers never produce.
//
// ORDERING CAVEAT, disclosed rather than hidden: PushBundle inserts each
// grain's rows in ONE transaction, in the bundle's own slice order, and the
// hash this feeds (json.Marshal of the Bundle) is order-sensitive over
// those slices. This function selects with NO ORDER BY, relying on DuckDB
// returning an untouched, append-only table's rows in insertion order —
// true for a warehouse nothing has compacted or rewritten since the push,
// which is exactly the case `corral verify` exists to check (has THIS push
// been altered). A warehouse an operator has VACUUMed could show a hash
// mismatch that is not evidence of tampering; see cmd/corral/verify.go's
// print line, which says so rather than calling every mismatch forgery.
//
// WHICH PUSH. The tables are append-only, so a scan pushed twice — the run
// itself and a later `corral scans push`, or two attempts of one workflow —
// is two sets of rows, and a run with no --record pushes them all under
// scan_id 0, where every such run of a repo shares one key. Keying a read
// on (repo, scan_id) therefore unioned every Action push of a repo into one
// "bundle" that no statement ever hashed. Each push stamps its OWN scan_uid
// on every row it writes (see scanUID), so a read keyed on the uid returns
// exactly one push. Callers locate the push they mean with LocateScans and
// read it with ReadBundleForScan; ReadBundle (repo, scan_id) remains for
// rows an older corral wrote with no scan_uid at all.
func ReadBundle(db *sql.DB, repo string, scanID int64) (Bundle, error) {
	scan, ok, err := readScanRow(db, "repo = ? AND scan_id = ?", repo, scanID)
	if err != nil {
		return Bundle{}, fmt.Errorf("auditpush: reading corral_scans: %w", err)
	}
	return readGrains(db, scan, ok, "repo = ? AND scan_id = ?", repo, scanID)
}

// ReadBundleForScan reads back the ONE push scan names — by its scan_uid,
// or, for a row written before scan_uid existed, by (repo, scan_id) as
// ReadBundle does.
func ReadBundleForScan(db *sql.DB, scan ScanRow) (Bundle, error) {
	if scan.ScanUID == "" {
		return ReadBundle(db, scan.Repo, scan.ScanID)
	}
	return readGrains(db, scan, true, "scan_uid = ?", scan.ScanUID)
}

// LocateScans lists every push in the warehouse that could be the one a
// statement describes: rows stamped with statementSHA256 (the exact key —
// PushBundle writes the statement's own hash onto every row of a push made
// with --attest), or, when the caller has no statement bytes to hash, rows
// for (repo, scanID). scanID 0 is not an id (it is every run pushed without
// --record) and is never used as a key on its own.
//
// Newest first, so a caller that must pick one has the latest push at the
// head; the PushedBy field says whether each is the run's own push or a
// later backfill.
func LocateScans(db *sql.DB, repo string, scanID int64, statementSHA256 string) ([]ScanRow, error) {
	if statementSHA256 != "" {
		out, err := locateScansWhere(db, "statement_sha256 = ?", statementSHA256)
		if err != nil || len(out) > 0 {
			return out, err
		}
		// Nothing stamped with this statement: the rows may predate the
		// stamp, or the push may have been made without --attest and the
		// statement written afterwards. (repo, scan_id) is the next key.
	}
	if repo != "" && scanID != 0 {
		return locateScansWhere(db, "repo = ? AND scan_id = ?", repo, scanID)
	}
	return nil, nil
}

func locateScansWhere(db *sql.DB, where string, args ...any) ([]ScanRow, error) {
	rows, err := db.Query("SELECT scan_uid FROM corral_scans WHERE "+where+" ORDER BY ts DESC", args...) // #nosec G202 -- where is one of two constant clauses in LocateScans; every value is a bound parameter
	if err != nil {
		return nil, fmt.Errorf("auditpush: locating scans: %w", err)
	}
	var uids []sql.NullString
	for rows.Next() {
		var uid sql.NullString
		if err := rows.Scan(&uid); err != nil {
			rows.Close()
			return nil, fmt.Errorf("auditpush: locating scans: %w", err)
		}
		uids = append(uids, uid)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("auditpush: locating scans: %w", err)
	}
	var out []ScanRow
	for _, uid := range uids {
		var scan ScanRow
		var ok bool
		var err error
		if uid.Valid && uid.String != "" {
			scan, ok, err = readScanRow(db, "scan_uid = ?", uid.String)
		} else {
			// A pre-scan_uid row: (repo, scan_id) is the only key it has.
			scan, ok, err = readScanRow(db, where, args...)
		}
		if err != nil {
			return nil, fmt.Errorf("auditpush: reading corral_scans: %w", err)
		}
		if ok {
			out = append(out, scan)
		}
	}
	return out, nil
}

func readGrains(db *sql.DB, scan ScanRow, ok bool, where string, args ...any) (Bundle, error) {
	files, err := readFileRows(db, where, args...)
	if err != nil {
		return Bundle{}, fmt.Errorf("auditpush: reading corral_audits: %w", err)
	}
	mutants, err := readMutantRows(db, where, args...)
	if err != nil {
		return Bundle{}, fmt.Errorf("auditpush: reading corral_mutants: %w", err)
	}
	calls, err := readModelCallRows(db, where, args...)
	if err != nil {
		return Bundle{}, fmt.Errorf("auditpush: reading corral_model_calls: %w", err)
	}
	events, err := readEventRows(db, where, args...)
	if err != nil {
		return Bundle{}, fmt.Errorf("auditpush: reading corral_events: %w", err)
	}
	b := Bundle{Files: files, Mutants: mutants, Calls: calls, Events: events}
	if ok {
		b.Scan = scan
	}
	// SourcePushed mirrors the scan header's own record of it — a bundle
	// with no scan row (nothing to read: --record was never given, or
	// nothing was ever pushed for this repo/scan) leaves it false, the
	// honest default.
	b.SourcePushed = scan.SourcePushed
	return b, nil
}

// nullFloat64 converts sql.NullFloat64 to *float64: the pointer-is-absence
// convention every Row/ScanRow field above already uses.
func nullFloat64(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	f := v.Float64
	return &f
}

func nullInt64(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}

func nullInt(v sql.NullInt64) *int {
	if !v.Valid {
		return nil
	}
	n := int(v.Int64)
	return &n
}

func nullBoolPtr(v sql.NullBool) *bool {
	if !v.Valid {
		return nil
	}
	b := v.Bool
	return &b
}

func readScanRow(db *sql.DB, where string, args ...any) (ScanRow, bool, error) {
	row := db.QueryRow(`SELECT
		repo, run_url, scan_id, commit_sha, corral_version, substrate,
		host, cores, trees_requested, diff_base, candidates, audited, passed,
		total_ms, input_tokens, output_tokens, model_calls,
		source_pushed, statement_sha256, selection_ms, selection_reused_from,
		rekor_log_index, rekor_uuid, started_at, pushed_by, scan_uid
	   FROM corral_scans WHERE `+where, args...) // #nosec G202 -- where is a constant clause chosen by this package's own callers; every value is a bound parameter

	var s ScanRow
	var cores, treesRequested sql.NullInt64
	var totalMS, selectionMS, selectionReusedFrom, rekorLogIndex sql.NullInt64
	var rekorUUID, pushedBy, scanUID sql.NullString
	var scanPassed sql.NullBool
	var scanStarted sql.NullTime
	if err := row.Scan(
		&s.Repo, &s.RunURL, &s.ScanID, &s.Commit, &s.CorralVersion, &s.Substrate,
		&s.Host, &cores, &treesRequested, &s.DiffBase, &s.Candidates, &s.Audited, &scanPassed,
		&totalMS, &s.InputTokens, &s.OutputTokens, &s.ModelCalls,
		&s.SourcePushed, &s.StatementSHA256, &selectionMS, &selectionReusedFrom,
		&rekorLogIndex, &rekorUUID, &scanStarted, &pushedBy, &scanUID,
	); err != nil {
		if err == sql.ErrNoRows {
			return ScanRow{}, false, nil
		}
		return ScanRow{}, false, err
	}
	if scanPassed.Valid {
		v := scanPassed.Bool
		s.Passed = &v
	}
	if scanStarted.Valid {
		t := scanStarted.Time
		s.StartedAt = &t
	}
	s.Cores = int(cores.Int64)
	s.TreesRequested = int(treesRequested.Int64)
	s.TotalMillis = nullInt64(totalMS)
	s.SelectionMillis = nullInt64(selectionMS)
	s.SelectionReusedFrom = nullInt64(selectionReusedFrom)
	s.RekorLogIndex = nullInt64(rekorLogIndex)
	s.RekorUUID = rekorUUID.String
	s.PushedBy = pushedBy.String
	s.ScanUID = scanUID.String
	return s, true, nil
}

func readFileRows(db *sql.DB, where string, args ...any) ([]Row, error) {
	rows, err := db.Query(`SELECT
		repo, commit_sha, path, lang,
		kill_rate, survivors, proven_missed,
		timed_out, test_writer_failed, pool_test_unsound,
		audited, candidates, mutants_planted, models_by_role,
		min_kill_rate, max_proven_missed, passed, statement_sha256, run_url,
		test_selection, selected_tests, suite_tests, selection_fallback, uncovered,
		writer_mode,
		per_mutant, tests_per_mutant_min, tests_per_mutant_median, tests_per_mutant_max,
		trees, concurrency_note, shared_dirs, scan_id,
		disposition, reason, preflight_state, evidence, detail, status,
		cache_hit, reused_from_scan_id, cache_key, parent_sha256,
		mutants_graded, mutants_invalid, mutants_timed_out,
		regions_total, regions_probed, dropped_regions, vacuous_findings,
		authored_test_not_collected, baseline_failed, suite_baseline_ms,
		proven_mutant_ids, challenger_jaccard, challenger_kappa,
		challenger_sufficient, goals_derived, goal_reused,
		selection_ms, generation_ms, pool_ms, dev_pass_ms, authored_pass_ms,
		critic_ms, total_ms, mutant_ms_median, mutant_ms_max,
		authored_test, verdict_json, prompt_shape, covering_tests, import_only, started_at,
		mutant_budget, mutant_budget_rule, complexity,
		symbols, symbols_probed, decisions, decisions_probed,
		challenger_mutants, challenger_survived_writer, challenger_survived_shadow, challenger_union, challenger_shared
	   FROM corral_audits WHERE `+where, args...) // #nosec G202 -- where is a constant clause from readGrains; every value is a bound parameter
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Row
	for rows.Next() {
		var r Row
		var killRate, minKillRate sql.NullFloat64
		var writerMode, concurrencyNote, sharedDirs sql.NullString
		var pmMin, pmMedian, pmMax sql.NullInt64
		var trees sql.NullInt64
		var reason, cacheKey, parentSHA256, droppedRegions, provenMutantIDs sql.NullString
		var detail, status sql.NullString
		var maxProvenMissed sql.NullInt64
		var reusedFromScanID sql.NullInt64
		var mutantsTimedOut sql.NullInt64
		var suiteBaselineMillis sql.NullInt64
		var challengerJaccard, challengerKappa sql.NullFloat64
		var challengerSufficient sql.NullBool
		var goalReused sql.NullBool
		var selectionMillis, generationMillis, poolMillis, devPassMillis, authoredPassMillis sql.NullInt64
		var criticMillis, totalMillis, mutantMillisMedian, mutantMillisMax sql.NullInt64
		var authoredTest, verdictJSON, promptShape sql.NullString
		var mutantBudget, complexity sql.NullInt64
		var mutantBudgetRule sql.NullString
		var symbols, symbolsProbed, decisions, decisionsProbed sql.NullInt64
		var chMutants, chSurvW, chSurvS, chUnion, chShared sql.NullInt64
		var coveringTests sql.NullInt64
		var importOnly sql.NullBool
		var rowPassed sql.NullBool
		var rowStarted sql.NullTime

		if err := rows.Scan(
			&r.Repo, &r.Commit, &r.Path, &r.Lang,
			&killRate, &r.Survivors, &r.ProvenMissed,
			&r.TimedOut, &r.TestWriterFailed, &r.PoolTestUnsound,
			&r.Audited, &r.Candidates, &r.MutantsPlanted, &r.ModelsByRole,
			&minKillRate, &maxProvenMissed, &rowPassed, &r.StatementSHA256, &r.RunURL,
			&r.TestSelection, &r.SelectedTests, &r.SuiteTests, &r.SelectionFallback, &r.Uncovered,
			&writerMode,
			&r.PerMutant, &pmMin, &pmMedian, &pmMax,
			&trees, &concurrencyNote, &sharedDirs, &r.ScanID,
			&r.Disposition, &reason, &r.PreflightState, &r.Evidence, &detail, &status,
			&r.CacheHit, &reusedFromScanID, &cacheKey, &parentSHA256,
			&r.MutantsGraded, &r.MutantsInvalid, &mutantsTimedOut,
			&r.RegionsTotal, &r.RegionsProbed, &droppedRegions, &r.VacuousFindings,
			&r.AuthoredTestNotCollected, &r.BaselineFailed, &suiteBaselineMillis,
			&provenMutantIDs, &challengerJaccard, &challengerKappa,
			&challengerSufficient, &r.GoalsDerived, &goalReused,
			&selectionMillis, &generationMillis, &poolMillis, &devPassMillis, &authoredPassMillis,
			&criticMillis, &totalMillis, &mutantMillisMedian, &mutantMillisMax,
			&authoredTest, &verdictJSON, &promptShape, &coveringTests, &importOnly, &rowStarted,
			&mutantBudget, &mutantBudgetRule, &complexity,
			&symbols, &symbolsProbed, &decisions, &decisionsProbed,
			&chMutants, &chSurvW, &chSurvS, &chUnion, &chShared,
		); err != nil {
			return nil, err
		}
		if rowStarted.Valid {
			t := rowStarted.Time
			r.StartedAt = &t
		}

		r.KillRate = nullFloat64(killRate)
		r.MinKillRate = nullFloat64(minKillRate)
		r.MaxProvenMissed = nullInt(maxProvenMissed)
		r.WriterMode = writerMode.String
		if rowPassed.Valid {
			v := rowPassed.Bool
			r.Passed = &v
		}
		if pmMin.Valid && pmMedian.Valid && pmMax.Valid {
			r.TestsPerMutant = &TestsPerMutantSpread{Min: int(pmMin.Int64), Median: int(pmMedian.Int64), Max: int(pmMax.Int64)}
		}
		r.Trees = int(trees.Int64)
		r.ConcurrencyNote = concurrencyNote.String
		r.SharedDirs = sharedDirs.String
		r.Reason = reason.String
		r.Detail = detail.String
		r.Status = status.String
		r.ReusedFromScanID = nullInt64(reusedFromScanID)
		r.CacheKey = cacheKey.String
		r.ParentSHA256 = parentSHA256.String
		r.MutantsTimedOut = nullInt(mutantsTimedOut)
		r.DroppedRegions = droppedRegions.String
		r.SuiteBaselineMillis = nullInt64(suiteBaselineMillis)
		r.ProvenMutantIDs = provenMutantIDs.String
		r.ChallengerJaccard = nullFloat64(challengerJaccard)
		r.ChallengerKappa = nullFloat64(challengerKappa)
		r.ChallengerSufficient = nullBoolPtr(challengerSufficient)
		r.GoalReused = nullBoolPtr(goalReused)
		r.SelectionMillis = nullInt64(selectionMillis)
		r.GenerationMillis = nullInt64(generationMillis)
		r.PoolMillis = nullInt64(poolMillis)
		r.DevPassMillis = nullInt64(devPassMillis)
		r.AuthoredPassMillis = nullInt64(authoredPassMillis)
		r.CriticMillis = nullInt64(criticMillis)
		r.TotalMillis = nullInt64(totalMillis)
		r.MutantMillisMedian = nullInt64(mutantMillisMedian)
		r.MutantMillisMax = nullInt64(mutantMillisMax)
		r.AuthoredTest = authoredTest.String
		r.VerdictJSON = verdictJSON.String
		r.PromptShape = promptShape.String
		r.CoveringTests = nullInt(coveringTests)
		r.ImportOnly = nullBoolPtr(importOnly)
		r.MutantBudget = nullInt(mutantBudget)
		r.MutantBudgetRule = mutantBudgetRule.String
		r.Complexity = nullInt(complexity)
		r.Symbols, r.SymbolsProbed = nullInt(symbols), nullInt(symbolsProbed)
		r.Decisions, r.DecisionsProbed = nullInt(decisions), nullInt(decisionsProbed)
		r.ChallengerMutants, r.ChallengerSurvivedWriter, r.ChallengerSurvivedShadow = nullInt(chMutants), nullInt(chSurvW), nullInt(chSurvS)
		r.ChallengerUnion, r.ChallengerShared = nullInt(chUnion), nullInt(chShared)

		out = append(out, r)
	}
	return out, rows.Err()
}

func readMutantRows(db *sql.DB, where string, args ...any) ([]MutantRow, error) {
	rows, err := db.Query(`SELECT
		repo, run_url, scan_id, path, mutant_id, parent_sha256, outcome,
		invalid_reason, proven, proven_by_authored_alone, tests_run,
		selection_rule, duration_ms, killed_by, span_start, span_end, code,
		statement_sha256, shape, generator_model
	   FROM corral_mutants WHERE `+where, args...) // #nosec G202 -- where is a constant clause from readGrains; every value is a bound parameter
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []MutantRow
	for rows.Next() {
		var m MutantRow
		var parentSHA256, invalidReason, selectionRule, killedBy, code sql.NullString
		var durationMillis sql.NullInt64
		var spanStart, spanEnd sql.NullInt64
		var shape, generatorModel sql.NullString
		if err := rows.Scan(
			&m.Repo, &m.RunURL, &m.ScanID, &m.Path, &m.MutantID, &parentSHA256, &m.Outcome,
			&invalidReason, &m.Proven, &m.ProvenByAuthoredAlone, &m.TestsRun,
			&selectionRule, &durationMillis, &killedBy, &spanStart, &spanEnd, &code,
			&m.StatementSHA256, &shape, &generatorModel,
		); err != nil {
			return nil, err
		}
		m.ParentSHA256 = parentSHA256.String
		m.Shape, m.GeneratorModel = shape.String, generatorModel.String
		m.InvalidReason = invalidReason.String
		m.SelectionRule = selectionRule.String
		m.DurationMillis = nullInt64(durationMillis)
		m.KilledBy = killedBy.String
		m.SpanStart = int(spanStart.Int64)
		m.SpanEnd = int(spanEnd.Int64)
		m.Code = code.String
		out = append(out, m)
	}
	return out, rows.Err()
}

func readModelCallRows(db *sql.DB, where string, args ...any) ([]ModelCallRow, error) {
	rows, err := db.Query(`SELECT
		repo, run_url, scan_id, path, role, model, calls, retries,
		input_tokens, output_tokens, cached_input_tokens,
		cache_write_input_tokens, wall_ms,
		statement_sha256
	   FROM corral_model_calls WHERE `+where, args...) // #nosec G202 -- where is a constant clause from readGrains; every value is a bound parameter
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ModelCallRow
	for rows.Next() {
		var c ModelCallRow
		var cachedInputTokens, cacheWriteInputTokens sql.NullInt64
		if err := rows.Scan(
			&c.Repo, &c.RunURL, &c.ScanID, &c.Path, &c.Role, &c.Model, &c.Calls, &c.Retries,
			&c.InputTokens, &c.OutputTokens, &cachedInputTokens,
			&cacheWriteInputTokens, &c.WallMillis,
			&c.StatementSHA256,
		); err != nil {
			return nil, err
		}
		c.CachedInputTokens = nullInt64(cachedInputTokens)
		c.CacheWriteInputTokens = nullInt64(cacheWriteInputTokens)
		out = append(out, c)
	}
	return out, rows.Err()
}

func readEventRows(db *sql.DB, where string, args ...any) ([]EventRow, error) {
	rows, err := db.Query(`SELECT
		ts, repo, run_url, scan_id, path, seq, kind, actor, subject, model,
		duration_ms, detail, statement_sha256
	   FROM corral_events WHERE `+where, args...) // #nosec G202 -- where is a constant clause from readGrains; every value is a bound parameter
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []EventRow
	for rows.Next() {
		var e EventRow
		var detail sql.NullString
		var durationMillis sql.NullInt64
		if err := rows.Scan(
			&e.TS, &e.Repo, &e.RunURL, &e.ScanID, &e.Path, &e.Seq, &e.Kind, &e.Actor, &e.Subject, &e.Model,
			&durationMillis, &detail, &e.StatementSHA256,
		); err != nil {
			return nil, err
		}
		e.DurationMillis = nullInt64(durationMillis)
		e.Detail = detail.String
		out = append(out, e)
	}
	return out, rows.Err()
}

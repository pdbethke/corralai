// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/modelcorr"
	"github.com/pdbethke/corralai/internal/reposcan"
	"github.com/pdbethke/corralai/internal/scanstore"
)

// intPtr is the one helper this package's tests use for a nullable *int
// fixture value (scanstore.ModelCall.Retries, advpool.ModelCall.Retries) —
// so a measured (non-nil) retries count in a test literal does not need its
// own named local everywhere it appears.
func intPtr(v int) *int     { return &v }
func i64ptr(v int64) *int64 { return &v }

// twoFileLedgerRows builds the ledger rows for a scan that audited one file
// and refused another — the shape the warehouse used to lose. repoDir holds
// the audited file so ParentSHA256 is a real hash of real bytes.
func twoFileLedgerRows(t *testing.T) (string, []scanstore.File, []scanstore.Mutant) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package p\n\nfunc F() int { return 1 }\n"), 0o600); err != nil {
		t.Fatalf("write the audited file: %v", err)
	}
	// The refused file exists on disk too — it is a real source file corral
	// walked and would not grade, and its parent_sha256 IS a read of these
	// bytes, because it has no mutants to take one from.
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("package p\n\nfunc G() int { return 2 }\n"), 0o600); err != nil {
		t.Fatalf("write the rejected file: %v", err)
	}
	results := []reposcan.FileResult{
		{
			// Provenance ends " (reused)" and GoalReused is true — a REAL,
			// non-default value, so the field-for-field walk below actually
			// exercises the goal-cache column rather than comparing nil to
			// nil (see reposcan.GoalWasReused).
			Job: reposcan.Job{Path: "a.go", Lang: "go", Goal: reposcan.Goal{
				Text: "F never returns a negative number", Provenance: "derived:claude-x@v1.2.3 (reused)",
			}, GoalReused: true},
			Gradable: true,
			Verdict: advpool.Verdict{
				DevKillRate: 0.5, Survivors: 2, ProvenMissed: 1,
				// A real pair, non-nil and Sufficient, so the field-for-field
				// walk below actually exercises the Challenger columns
				// rather than comparing nil to nil.
				ChallengerAgreement: &modelcorr.Pair{
					ModelA: "writer-a", ModelB: "writer-b",
					Mutants: 8, SurvivedA: 4, SurvivedB: 3,
					SharedSurvivors: 3, UnionSurvivors: 4,
					Jaccard: 0.75, Sufficient: true,
					Kappa: 0.4, KappaDefined: true,
				},
				MutantsTotal: 8, MutantsInvalid: 1,
				RegionsTotal: 2, RegionsProbed: 2,
				// A real value, non-empty, so the field-for-field walk below
				// actually exercises PromptShape rather than comparing "" to
				// "" — see PromptShape's own doc for what the value means.
				PromptShape: "chunk",
				// DISTINCT from PromptShape above, and non-empty, so the
				// reflection walk below proves each value reaches its OWN
				// warehouse column: two adjacent strings that happen to
				// match would let a transposition pass.
				WriterMode: advpool.WriterModePerSurvivor,
				Status:     "needs-review", AuthoredTest: "func TestX(t *testing.T) {}",
				ProvenMutantIDs: []string{"m2"},
				TestSelection: advpool.TestSelection{
					Method: "coverage-lines", Selected: 4, Of: 41, PerMutant: true,
					TestsPerMutant: &advpool.TestsPerMutantSpread{Min: 3, Median: 4, Max: 9},
				},
				Concurrency: advpool.Concurrency{Trees: 6, Note: "probe passed"},
				// The generator hashed the exact bytes it mutated, and every
				// mutant of one file agrees. That hash — not a re-read of the
				// checkout — is the file's parent_sha256.
				DevKilledMutants: []advpool.MutantRef{
					{ID: "m1", ParentSHA256: auditedParentSHA, TestsRun: 4, Rule: "lines", Duration: 54 * time.Second,
						// A REAL id, so the walk below actually walks
						// killed_by: an empty one compares "" to "" and a
						// dropped mapping passes.
						KilledBy: "tests/test_a.py::test_scale"},
				},
				DevSurvivedMutants: []advpool.MutantRef{
					{ID: "m2", ParentSHA256: auditedParentSHA, TestsRun: 3, Rule: "lines", Duration: 3 * time.Minute},
					{ID: "m3", ParentSHA256: auditedParentSHA, TestsRun: 5, Rule: "lines"},
				},
				// The clock. Set on the fixture so the field-for-field walk
				// below actually WALKS the timing columns: a mapping that
				// dropped them would otherwise compare nil to nil and pass.
				Timing: advpool.Timing{
					Selection: 92 * time.Second, Generation: 4*time.Minute + 10*time.Second,
					Pool: 12 * time.Second, DevPass: 35*time.Minute + 4*time.Second,
					AuthoredPass: 109 * time.Second, Total: 43*time.Minute + 13*time.Second,
				},
				MutantDurationMedian: 54 * time.Second,
				MutantDurationMax:    3 * time.Minute,
			},
		},
		{
			Job:      reposcan.Job{Path: "b.go", Lang: "go"},
			Gradable: false,
			Reason:   reposcan.ReasonBaselineFailed,
			Detail:   "the suite did not pass on unmutated code",
		},
	}
	return dir, buildScanFileRows(results, nil, reposcan.CoverageMap{}, "", dir, io.Discard),
		buildScanMutantRows(0, results)
}

// auditedParentSHA is the hash the mutant generator recorded for a.go's
// bytes. Deliberately NOT the sha256 of what twoFileLedgerRows writes to
// disk: the two must not be allowed to coincide, or a test would pass with
// the file re-read at record time — which is exactly the bug (the workspace
// substrate overlays the file during the audit, so a later re-read is not
// guaranteed to be the graded bytes).
const auditedParentSHA = "1111111111111111111111111111111111111111111111111111111111111111"

// TestBundleIsTheLedgerRowForRow is the whole point of having ONE mapping:
// the pushed rows are the ledger's rows, not a second derivation of the
// report. Two properties, and the first is a regression the report path
// could not satisfy at all —
//
//   - the file corral REFUSED is in the bundle, with its disposition and its
//     reason. The old report-derived path pushed only r.Weakest, so every
//     rejected file was simply absent from the warehouse, and "is this repo
//     covered?" could not be asked of it;
//   - no field is dropped on the way. Walked by REFLECTION over the field
//     names rather than asserted one at a time, because a hand-written list
//     of assertions is exactly what stops covering the struct the day
//     somebody adds a field to it.
func TestBundleIsTheLedgerRowForRow(t *testing.T) {
	_, files, _ := twoFileLedgerRows(t)
	if len(files) != 2 {
		t.Fatalf("expected two ledger rows, got %d", len(files))
	}

	scan := scanstore.Scan{Repo: "o/r", Commit: "deadbeef", Audited: 1, Candidates: 2}
	b := buildBundle(scan, 11, files, nil, nil, nil, auditpush.Link{}, false,
		"o/r", "deadbeef", "", bundleMeta{ModelsByRole: `{"writer":"m"}`, Passed: false})

	if len(b.Files) != 2 {
		t.Fatalf("bundle carries %d file row(s), want both dispositions", len(b.Files))
	}
	var rejected *auditpush.Row
	for i := range b.Files {
		if b.Files[i].Path == "b.go" {
			rejected = &b.Files[i]
		}
	}
	if rejected == nil {
		t.Fatal("the REJECTED file is missing from the bundle — the warehouse would show a repo as clean where corral never graded it")
	}
	if rejected.Disposition != "rejected" || rejected.Reason != reposcan.ReasonBaselineFailed {
		t.Errorf("rejected row = %+v, want disposition/reason to survive the mapping", *rejected)
	}
	if rejected.KillRate != nil {
		t.Errorf("a rejected file must carry no kill rate, got %v", *rejected.KillRate)
	}

	// deliberatelyUnmapped names every scanstore.File field that has an
	// identically-named auditpush.Row field this test does NOT compare, with
	// the reason. Anything not listed here must match exactly.
	deliberatelyUnmapped := map[string]string{
		// The ledger column predates the NULL-not-zero rule and is a plain
		// int64; the warehouse column is nullable. Converted on purpose by
		// nilIfZeroMillis — see buildAuditRows.
		"SuiteBaselineMillis": "int64 in the ledger, *int64 (NULL when unmeasured) in the warehouse",
		// The ledger keeps three nullable ints; the warehouse keeps one
		// pointer-to-struct, so an unmeasured spread is absent rather than
		// three zeros.
		"TestsPerMutantMin":    "folded into Row.TestsPerMutant",
		"TestsPerMutantMedian": "folded into Row.TestsPerMutant",
		"TestsPerMutantMax":    "folded into Row.TestsPerMutant",
		// The ledger's canonical per-file roster; the warehouse column holds
		// the scan-wide roster JSON, which is a different (and older) claim.
		"ModelsByRole": "Row.ModelsByRole is the scan-wide roster JSON, not the per-file KV",
		// Renamed at the boundary, and compared explicitly below.
		"MutantsTotal": "lands in Row.MutantsPlanted",
		// Local-only bookkeeping with no warehouse column at all.
		"Gradable":    "implied by Disposition",
		"ComputedAt":  "no warehouse column",
		"MutantsFrom": "no warehouse column",
	}

	for _, f := range files {
		var row auditpush.Row
		for _, r := range b.Files {
			if r.Path == f.Path {
				row = r
			}
		}
		lv, rv := reflect.ValueOf(f), reflect.ValueOf(row)
		lt := lv.Type()
		for i := 0; i < lt.NumField(); i++ {
			name := lt.Field(i).Name
			if _, skip := deliberatelyUnmapped[name]; skip {
				continue
			}
			rf := rv.FieldByName(name)
			if !rf.IsValid() {
				// t.Errorf, not a silent continue: this walk exists to catch
				// a ledger field that never reaches the warehouse, and
				// skipping every field with no matching name skipped exactly
				// that case. A field with no column belongs on the
				// deliberately-unmapped list, WITH the reason.
				t.Errorf("scanstore.File.%s has no warehouse column of the same name, and is not on the deliberately-unmapped list", name)
				continue
			}
			if rf.Type() != lv.Field(i).Type() {
				t.Errorf("%s: field %s is %s in the ledger and %s in the warehouse, and is not on the deliberately-unmapped list",
					f.Path, name, lv.Field(i).Type(), rf.Type())
				continue
			}
			if !reflect.DeepEqual(lv.Field(i).Interface(), rf.Interface()) {
				t.Errorf("%s: field %s was dropped or changed on the way to the warehouse: ledger %v, row %v",
					f.Path, name, lv.Field(i).Interface(), rf.Interface())
			}
		}
		// PLANTED, not graded. advpool's MutantsTotal is the count that
		// reached grading (compile-gate rejects excluded), so filing it under
		// mutants_planted understated every run that produced an invalid
		// mutant — and it duplicated mutants_graded exactly, which is how the
		// column stopped saying anything.
		wantPlanted := f.MutantsGraded + f.MutantsInvalid
		if row.MutantsPlanted != wantPlanted {
			t.Errorf("%s: mutants_planted = %d, want graded+invalid = %d", f.Path, row.MutantsPlanted, wantPlanted)
		}
	}

	// The audited row's validity key is the GENERATOR's hash of the bytes it
	// mutated, not a re-read of the checkout.
	for _, r := range b.Files {
		if r.Path == "a.go" && r.ParentSHA256 != auditedParentSHA {
			t.Errorf("the audited row's parent_sha256 = %q, want the mutants' own parent hash %q", r.ParentSHA256, auditedParentSHA)
		}
	}
}

// TestBuildModelCallRowsIsFieldForField is TestBundleIsTheLedgerRowForRow's
// sibling for the money grain: every scanstore.ModelCall field, walked by
// reflection with REAL (nonzero, mutually distinct) values so a mapping bug
// that dropped or swapped a field could not hide behind two zeros comparing
// equal.
//
// Run twice — once with Retries nil (the value every producer in this
// codebase actually sets, since nothing has a retry loop to observe) and
// once with a measured non-nil value — so the mapping is proven for BOTH
// states of the nullable column, not just the common one. A mapping that
// coerced nil to a stored 0 (or vice versa) would pass a fixture that only
// ever exercised one of the two.
func TestBuildModelCallRowsIsFieldForField(t *testing.T) {
	retriesCases := []struct {
		name    string
		retries *int
	}{
		{"nil (unmeasured)", nil},
		{"measured", intPtr(3)},
	}
	for _, tc := range retriesCases {
		t.Run(tc.name, func(t *testing.T) {
			// The two cache counters are set NON-NIL here on purpose: the
			// reflection walk below compares field values, and a nil on both
			// sides compares equal whether the mapping carries the field or
			// silently drops it. A real, distinct value is the only way this
			// fixture can catch a dropped column.
			call := scanstore.ModelCall{
				ScanID: 11, Path: "a.go", Role: "mutant-generator", Model: "m-1",
				Calls: 24, Retries: tc.retries, InputTokens: 900_000, OutputTokens: 31_000,
				CachedInputTokens: i64ptr(760_000), CacheWriteInputTokens: i64ptr(38_000),
				WallMillis: 45_000,
			}
			rows := buildModelCallRows([]scanstore.ModelCall{call}, 11, bundleMeta{Repo: "o/r", RunURL: "https://ci/1"})
			if len(rows) != 1 {
				t.Fatalf("buildModelCallRows returned %d row(s), want 1", len(rows))
			}
			row := rows[0]

			lv, rv := reflect.ValueOf(call), reflect.ValueOf(row)
			lt := lv.Type()
			// deliberatelyUnmappedCall names every scanstore.ModelCall field with no
			// identically-named, identically-typed auditpush.ModelCallRow field.
			deliberatelyUnmappedCall := map[string]string{}
			for i := 0; i < lt.NumField(); i++ {
				name := lt.Field(i).Name
				if _, skip := deliberatelyUnmappedCall[name]; skip {
					continue
				}
				rf := rv.FieldByName(name)
				if !rf.IsValid() {
					t.Errorf("scanstore.ModelCall.%s has no warehouse column of the same name", name)
					continue
				}
				if !reflect.DeepEqual(lv.Field(i).Interface(), rf.Interface()) {
					t.Errorf("%s was dropped or changed on the way to the warehouse: ledger %v, row %v",
						name, lv.Field(i).Interface(), rf.Interface())
				}
			}
			// Nullability is checked explicitly as well as by value: a
			// mapping that coerced a measured count to 0, or a nil to 0,
			// would still be "equal" under a sloppier comparison.
			if row.CachedInputTokens == nil || *row.CachedInputTokens != 760_000 {
				t.Errorf("row.CachedInputTokens = %v, want a measured 760000", row.CachedInputTokens)
			}
			if row.CacheWriteInputTokens == nil || *row.CacheWriteInputTokens != 38_000 {
				t.Errorf("row.CacheWriteInputTokens = %v, want a measured 38000", row.CacheWriteInputTokens)
			}
			if (row.Retries == nil) != (tc.retries == nil) {
				t.Errorf("row.Retries nil-ness = %v, want %v (nil must stay nil, a measured value must survive)",
					row.Retries == nil, tc.retries == nil)
			}
			if row.Retries != nil && tc.retries != nil && *row.Retries != *tc.retries {
				t.Errorf("row.Retries = %d, want %d", *row.Retries, *tc.retries)
			}
			if row.Repo != "o/r" || row.RunURL != "https://ci/1" {
				t.Errorf("row = %+v, want the scan-wide Repo/RunURL from meta", row)
			}
		})
	}
}

// TestBuildEventRowsIsFieldForField is TestBuildModelCallRowsIsFieldForField's
// sibling for the tape grain: every scanstore.Event field, walked by
// reflection with REAL (nonzero, mutually distinct) values so a mapping bug
// that dropped or swapped a field could not hide behind two zeros comparing
// equal.
func TestBuildEventRowsIsFieldForField(t *testing.T) {
	dur := int64(4500)
	event := scanstore.Event{
		ScanID: 11, Path: "a.go", Seq: 7, TS: time.Date(2026, 8, 30, 9, 0, 0, 0, time.UTC),
		Kind: "phase_authored_pass", Actor: "corral-advpool", Subject: "target.go",
		Model: "m-1", DurationMillis: &dur, Detail: `{"duration_ms":4500}`,
	}
	rows := buildEventRows([]scanstore.Event{event}, 11, bundleMeta{Repo: "o/r", RunURL: "https://ci/1"})
	if len(rows) != 1 {
		t.Fatalf("buildEventRows returned %d row(s), want 1", len(rows))
	}
	row := rows[0]

	lv, rv := reflect.ValueOf(event), reflect.ValueOf(row)
	lt := lv.Type()
	// deliberatelyUnmappedEvent names every scanstore.Event field with no
	// identically-named, identically-typed auditpush.EventRow field.
	// Empty on purpose: every scanstore.Event field, TS included, has an
	// identically-named EventRow field the walk below compares. corral_events.ts
	// used to be the PUSH time, which made the tape's own clock unrecoverable
	// from the warehouse — see TestEventTimestampsAreTheEventsOwn.
	deliberatelyUnmappedEvent := map[string]string{}
	for i := 0; i < lt.NumField(); i++ {
		name := lt.Field(i).Name
		if _, skip := deliberatelyUnmappedEvent[name]; skip {
			continue
		}
		rf := rv.FieldByName(name)
		if !rf.IsValid() {
			t.Errorf("scanstore.Event.%s has no warehouse column of the same name", name)
			continue
		}
		if !reflect.DeepEqual(lv.Field(i).Interface(), rf.Interface()) {
			t.Errorf("%s was dropped or changed on the way to the warehouse: ledger %v, row %v",
				name, lv.Field(i).Interface(), rf.Interface())
		}
	}
	if row.Repo != "o/r" || row.RunURL != "https://ci/1" {
		t.Errorf("row = %+v, want the scan-wide Repo/RunURL from meta", row)
	}
}

// TestAuditedParentSHAIsTheMutantsOwn is the validity key stated as the one
// question it has to answer: "is this verdict still current for HEAD?" is
// `parent_sha256 == sha256(HEAD:path)`, and that only works if the file's
// hash and its mutants' hashes are THE SAME BYTES. Two sources for one
// number is how they stop agreeing — and re-reading the checkout at record
// time is not even a second source, it is a different one: the workspace
// substrate writes the mutant into the file and restores it, so a re-read
// races the audit it is supposed to describe.
func TestAuditedParentSHAIsTheMutantsOwn(t *testing.T) {
	_, files, mutants := twoFileLedgerRows(t)
	scan := scanstore.Scan{Repo: "o/r", Commit: "deadbeef", Audited: 1, Candidates: 2}
	b := buildBundle(scan, 11, files, mutants, nil, nil, auditpush.Link{}, false,
		"o/r", "deadbeef", "", bundleMeta{Passed: false})

	target := filepath.Join(t.TempDir(), "w.duckdb")
	if _, err := pushBundle(target, b); err != nil {
		t.Fatalf("pushBundle: %v", err)
	}

	rows := queryRows(t, target, `SELECT parent_sha256 FROM corral_audits WHERE path = 'a.go'`)
	if len(rows) != 1 {
		t.Fatalf("got %d audit row(s) for a.go, want 1", len(rows))
	}
	fileSHA, _ := rows[0][0].(string)
	if fileSHA == "" {
		t.Fatal("the audited file's parent_sha256 is empty")
	}
	mutantRows := queryRows(t, target, `SELECT DISTINCT parent_sha256 FROM corral_mutants WHERE path = 'a.go'`)
	if len(mutantRows) != 1 {
		t.Fatalf("a.go's mutants report %d distinct parent hashes, want 1", len(mutantRows))
	}
	if mutantRows[0][0] != fileSHA {
		t.Errorf("corral_audits.parent_sha256 = %v but corral_mutants.parent_sha256 = %v — the file and its mutants disagree about which bytes were audited",
			fileSHA, mutantRows[0][0])
	}

	// A file the scan never graded has no mutants to take a hash from, so
	// its hash IS a read of the checkout — and it must be present, because
	// "never audited" is a state the seal reader has to be able to report.
	rej := queryRows(t, target, `SELECT parent_sha256 FROM corral_audits WHERE path = 'b.go'`)
	if len(rej) != 1 {
		t.Fatalf("got %d audit row(s) for the rejected file, want 1", len(rej))
	}
	if sha, _ := rej[0][0].(string); len(sha) != 64 {
		t.Errorf("a rejected file's parent_sha256 = %v, want the sha256 of its bytes on disk", rej[0][0])
	}
}

// TestDisagreeingMutantParentsRecordNoHash: if a file's mutants do not agree
// on which bytes they came from, there is no single answer to "what was
// audited" — and a hash picked from the first mutant would silently make a
// stale verdict look live. Record nothing, and say so on stderr.
func TestDisagreeingMutantParentsRecordNoHash(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package p\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	results := []reposcan.FileResult{{
		Job: reposcan.Job{Path: "a.go", Lang: "go"}, Gradable: true,
		Verdict: advpool.Verdict{
			DevKilledMutants:   []advpool.MutantRef{{ID: "m1", ParentSHA256: "aaaa"}},
			DevSurvivedMutants: []advpool.MutantRef{{ID: "m2", ParentSHA256: "bbbb"}},
		},
	}}
	var stderr bytes.Buffer
	files := buildScanFileRows(results, nil, reposcan.CoverageMap{}, "", dir, &stderr)
	if len(files) != 1 {
		t.Fatalf("got %d row(s), want 1", len(files))
	}
	if files[0].ParentSHA256 != "" {
		t.Errorf("parent_sha256 = %q, want empty when the mutants disagree", files[0].ParentSHA256)
	}
	if !strings.Contains(stderr.String(), "a.go") {
		t.Errorf("the disagreement must be disclosed on stderr, got %q", stderr.String())
	}
}

// TestPushSourceCarriesTheAuthoredTestButNoMutantCode walks the REAL path —
// ledger rows built from a report, mapped, pushed — and pins the help text
// to what actually happens. --push-source sends the authored test and the
// verdict JSON. It does NOT send mutant code, because nothing keeps mutant
// source in the ledger for it to send, and a flag whose help promises bytes
// it never carries is a custody claim in the wrong direction.
func TestPushSourceCarriesTheAuthoredTestButNoMutantCode(t *testing.T) {
	_, files, mutants := twoFileLedgerRows(t)
	scan := scanstore.Scan{Repo: "o/r", Commit: "deadbeef", Audited: 1, Candidates: 2}

	for _, tc := range []struct {
		name         string
		sourcePushed bool
		wantAuthored bool
	}{
		{"default", false, false},
		{"--push-source", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := buildBundle(scan, 11, files, mutants, nil, nil, auditpush.Link{},
				tc.sourcePushed, "o/r", "deadbeef", "", bundleMeta{})
			target := filepath.Join(t.TempDir(), "w.duckdb")
			if _, err := pushBundle(target, b); err != nil {
				t.Fatalf("pushBundle: %v", err)
			}
			got := queryRows(t, target, `SELECT count(*) FROM corral_audits WHERE authored_test IS NOT NULL`)
			present := got[0][0].(int64) > 0
			if present != tc.wantAuthored {
				t.Errorf("authored_test present = %v, want %v", present, tc.wantAuthored)
			}
			code := queryRows(t, target, `SELECT count(*) FROM corral_mutants WHERE code IS NOT NULL`)
			if code[0][0].(int64) != 0 {
				t.Errorf("%v mutant row(s) carry code — the ledger keeps no mutant source, so the help text must not promise it",
					code[0][0])
			}
		})
	}
}

// TestWarehouseRowsSHA256HashesWhatIsPushed is the third-party check the
// signed statement exists for: a verifier holding the statement and the
// warehouse recomputes the hash FROM THE ROWS THEY CAN SEE. In default mode
// the source columns are NULL in the warehouse, so a hash taken over the
// in-memory rows WITH their source would be one no verifier could ever
// reproduce — the statement would carry a number that never matches.
func TestWarehouseRowsSHA256HashesWhatIsPushed(t *testing.T) {
	_, files, mutants := twoFileLedgerRows(t)
	scan := scanstore.Scan{Repo: "o/r", Commit: "deadbeef", Audited: 1, Candidates: 2}
	b := buildBundle(scan, 11, files, mutants, nil, nil, auditpush.Link{}, false,
		"o/r", "deadbeef", "", bundleMeta{})
	// The fixture really does carry source, or this proves nothing.
	if b.Files[0].AuthoredTest == "" && b.Files[1].AuthoredTest == "" {
		t.Fatal("fixture carries no authored test; the test cannot distinguish the two hashes")
	}

	signed, err := warehouseRowsSHA256(b)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}

	target := filepath.Join(t.TempDir(), "w.duckdb")
	if _, err := pushBundle(target, b); err != nil {
		t.Fatalf("pushBundle: %v", err)
	}
	// Rebuild the bundle's source-bearing fields from what the warehouse
	// ACTUALLY holds, then re-hash. This is the verifier's move.
	readBack := b
	readBack.Files = append([]auditpush.Row(nil), b.Files...)
	for i := range readBack.Files {
		rows := queryRows(t, target,
			`SELECT coalesce(authored_test, ''), coalesce(verdict_json, '') FROM corral_audits WHERE path = '`+readBack.Files[i].Path+`'`)
		readBack.Files[i].AuthoredTest, _ = rows[0][0].(string)
		readBack.Files[i].VerdictJSON, _ = rows[0][1].(string)
	}
	readBack.Mutants = append([]auditpush.MutantRow(nil), b.Mutants...)
	for i := range readBack.Mutants {
		rows := queryRows(t, target,
			`SELECT coalesce(code, '') FROM corral_mutants WHERE mutant_id = '`+readBack.Mutants[i].MutantID+`'`)
		readBack.Mutants[i].Code, _ = rows[0][0].(string)
	}
	verified, err := warehouseRowsSHA256(readBack)
	if err != nil {
		t.Fatalf("re-hash: %v", err)
	}
	if verified != signed {
		t.Errorf("a verifier rehashing the pushed rows gets %s, but the statement signs %s — the statement hashes bytes the warehouse never received",
			verified, signed)
	}
}

// TestSpreadPointersDoNotAliasTheVerdict: the three ledger columns are
// pointers, and handing back &s.Min would leave the ledger row pointing into
// the verdict it was derived from — a later mutation of the verdict would
// silently rewrite a recorded number, and a recorded number that can change
// after the fact is not a record.
func TestSpreadPointersDoNotAliasTheVerdict(t *testing.T) {
	src := &advpool.TestsPerMutantSpread{Min: 3, Median: 4, Max: 9}
	min, median, max := spreadMin(src), spreadMedian(src), spreadMax(src)
	src.Min, src.Median, src.Max = 100, 200, 300
	if *min != 3 || *median != 4 || *max != 9 {
		t.Errorf("the recorded spread followed the verdict: %d/%d/%d, want 3/4/9", *min, *median, *max)
	}
}

// TestWarehouseRowsSHA256IsDeterministic is the property the signed
// statement rests on: warehouseRowsSha256 is only verifiable if a third
// party rebuilding the rows from the same scan gets the same hash. Nothing
// in the mapping may depend on map iteration order, a clock or a pointer
// address.
func TestWarehouseRowsSHA256IsDeterministic(t *testing.T) {
	_, files, mutants := twoFileLedgerRows(t)
	scan := scanstore.Scan{Repo: "o/r", Commit: "deadbeef", Audited: 1, Candidates: 2}
	meta := bundleMeta{ModelsByRole: `{"writer":"m"}`, Passed: true}
	// Every grain, not just the files: the hash covers the whole bundle, and
	// a determinism claim that exercises one table is not the claim.
	calls := []scanstore.ModelCall{{Path: "a.go", Role: "test-writer", Model: "w-1", Calls: 2, Retries: intPtr(1), InputTokens: 900, OutputTokens: 210, WallMillis: 4100}}
	events := []scanstore.Event{{Path: "a.go", Seq: 1, Kind: "phase-start", Actor: "driver"}}

	first := buildBundle(scan, 11, files, mutants, calls, events, auditpush.Link{}, false, "o/r", "deadbeef", "", meta)
	second := buildBundle(scan, 11, files, mutants, calls, events, auditpush.Link{}, false, "o/r", "deadbeef", "", meta)

	a, err := warehouseRowsSHA256(first)
	if err != nil {
		t.Fatalf("hash the first bundle: %v", err)
	}
	b, err := warehouseRowsSHA256(second)
	if err != nil {
		t.Fatalf("hash the second bundle: %v", err)
	}
	if a != b {
		t.Errorf("warehouseRowsSha256 is not deterministic: %s != %s", a, b)
	}

	// And the statement's hash must not depend on the statement's own hash:
	// stamping a Link changes nothing.
	withLink := first
	withLink.Link = auditpush.Link{ScanID: 11, StatementSHA256: "deadbeef"}
	c, err := warehouseRowsSHA256(withLink)
	if err != nil {
		t.Fatalf("hash the linked bundle: %v", err)
	}
	if c != a {
		t.Error("warehouseRowsSha256 changed when a statement hash was attached — the statement's hash would then depend on itself")
	}
}

// killed_by has to survive BOTH hops — the verdict's MutantRef into the
// ledger row, and the ledger row into the warehouse bundle. It is the one
// column that says which test was awake, and a drop anywhere on that path
// leaves the warehouse with a kill it cannot attribute.
func TestKilledByReachesTheWarehouseRow(t *testing.T) {
	_, _, mutants := twoFileLedgerRows(t)

	var killed *scanstore.Mutant
	for i := range mutants {
		if mutants[i].MutantID == "m1" {
			killed = &mutants[i]
		}
		if mutants[i].Outcome == "survived" && mutants[i].KilledBy != "" {
			t.Errorf("survivor %s carries killed_by %q — nothing caught it", mutants[i].MutantID, mutants[i].KilledBy)
		}
	}
	if killed == nil {
		t.Fatal("the killed mutant is missing from the ledger rows")
	}
	if killed.KilledBy != "tests/test_a.py::test_scale" {
		t.Fatalf("ledger row killed_by = %q, want the verdict's own id", killed.KilledBy)
	}

	rows := buildMutantRows(mutants, 7, bundleMeta{Repo: "acme/widgets", RunURL: "https://example.test/1"})
	for _, r := range rows {
		if r.MutantID != "m1" {
			continue
		}
		if r.KilledBy != "tests/test_a.py::test_scale" {
			t.Fatalf("bundle row killed_by = %q, want the ledger's own id", r.KilledBy)
		}
		return
	}
	t.Fatal("the killed mutant is missing from the bundle rows")
}

// TestEventTimestampsAreTheEventsOwn: corral_events.ts must be WHEN THE BEAT
// HAPPENED, not when the push ran.
//
// Every row of a push carried one identical `now`, so the warehouse could
// order a tape by seq but could never say how long anything took between two
// beats, or when in the run a beat fell — the tape's own clock was thrown
// away at the door, and a warehouse whose whole purpose is "what did this
// audit cost, and where" cannot answer either question from a column that
// only records the upload.
func TestEventTimestampsAreTheEventsOwn(t *testing.T) {
	first := time.Date(2026, 8, 30, 9, 0, 0, 0, time.UTC)
	second := first.Add(7 * time.Minute)
	events := []scanstore.Event{
		{ScanID: 11, Path: "a.go", Seq: 1, TS: first, Kind: "pool_subject", Detail: `{}`},
		{ScanID: 11, Path: "a.go", Seq: 2, TS: second, Kind: "pool_verdict", Detail: `{}`},
	}
	b := buildBundle(scanstore.Scan{Repo: "o/r", Commit: "deadbeef"}, 11, nil, nil, nil, events,
		auditpush.Link{}, false, "o/r", "deadbeef", "", bundleMeta{})
	target := filepath.Join(t.TempDir(), "w.duckdb")
	if _, err := pushBundle(target, b); err != nil {
		t.Fatalf("pushBundle: %v", err)
	}

	rows := queryRows(t, target, `SELECT seq, ts FROM corral_events ORDER BY seq`)
	if len(rows) != 2 {
		t.Fatalf("corral_events holds %d row(s), want 2", len(rows))
	}
	got := make([]time.Time, 2)
	for i, r := range rows {
		ts, ok := r[1].(time.Time)
		if !ok {
			t.Fatalf("row %d: ts = %T, want a timestamp", i, r[1])
		}
		got[i] = ts.UTC()
	}
	if !got[0].Equal(first) {
		t.Errorf("event 1 ts = %s, want the beat's own time %s (push time would be now)", got[0], first)
	}
	if !got[1].Equal(second) {
		t.Errorf("event 2 ts = %s, want the beat's own time %s", got[1], second)
	}
	if got[0].Equal(got[1]) {
		t.Error("both events landed on the same ts — the tape's own clock was replaced by the push's")
	}
}

// THE VERIFIER'S ACTUAL MOVE, not a rehearsal of it.
//
// TestWarehouseRowsSHA256HashesWhatIsPushed above copies the in-memory bundle
// and patches three source fields by hand before re-hashing. That is not what
// `corral verify --db` does — it calls auditpush.ReadBundle — and the
// difference hid a defect for the whole life of the feature: an event's
// timestamp is signed at Go's precision and zone (nanoseconds, local) and comes
// back from DuckDB at the warehouse's (microseconds, UTC), so every real scan,
// all of which carry events, failed the rows-hash check with a message blaming
// the operator's warehouse.
//
// This pushes a bundle with EVERY row type populated — an event with a
// nanosecond local-zone timestamp, an uncovered file whose kill rate the writer
// stores as NULL — reads it back through the real reader, and re-hashes. Any
// transform the writer introduces later breaks this test by name instead of
// breaking verification silently for every operator.
func TestVerifyRowsHashSurvivesTheRealReader(t *testing.T) {
	_, files, mutants := twoFileLedgerRows(t)
	if len(files) < 2 {
		t.Fatal("fixture must carry at least two files so one can be uncovered")
	}
	// An uncovered file with a kill rate set in memory — the writer stores
	// NULL for it, so a naive hash of the in-memory value cannot match.
	kr := 0.0
	files[1].KillRate = &kr
	files[1].Uncovered = true

	scan := scanstore.Scan{Repo: "o/r", Commit: "deadbeef", Audited: 1, Candidates: 2}
	// Local zone, nanoseconds: the way production stamps a beat.
	local := time.Date(2026, 9, 3, 15, 54, 50, 700624350, time.FixedZone("EDT", -4*3600))
	ev := buildScanEvent(files[0].Path, 1, local, "pool_subject", "s", map[string]any{"k": "v"})
	b := buildBundle(scan, 11, files, mutants, nil, []scanstore.Event{ev}, auditpush.Link{}, false,
		"o/r", "deadbeef", "", bundleMeta{})

	signed, err := warehouseRowsSHA256(b)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	target := filepath.Join(t.TempDir(), "w.duckdb")
	if _, err := pushBundle(target, b); err != nil {
		t.Fatalf("pushBundle: %v", err)
	}
	db, err := sql.Open("duckdb", target)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	readBack, err := auditpush.ReadBundle(db, "o/r", 11)
	if err != nil {
		t.Fatalf("ReadBundle: %v", err)
	}
	if len(readBack.Events) == 0 {
		t.Fatal("the reader returned no events — this test is not exercising the field that was broken")
	}
	verified, err := warehouseRowsSHA256(readBack)
	if err != nil {
		t.Fatalf("re-hash: %v", err)
	}
	if verified != signed {
		t.Errorf("the statement signs %s but a verifier re-hashing what ReadBundle returns gets %s.\n"+
			"Something the writer transforms is not canonicalised before hashing — see auditpush.CanonicalizeForWarehouse.\n"+
			"event ts signed: %s   read back: %s",
			signed, verified, ev.TS.Format(time.RFC3339Nano), readBack.Events[0].TS.Format(time.RFC3339Nano))
	}
}

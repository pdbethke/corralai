// SPDX-License-Identifier: Elastic-2.0

package auditpush

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
)

// custodySet names, field by field, every bundle field that holds SOURCE and
// is therefore withheld unless the run opted in with --push-source. It is
// written out here rather than derived so that adding a field to
// BlankUnpushedSource without deciding it belongs in the custody set fails
// this test, and adding one to the set without blanking it fails it too.
var custodySet = map[string][]string{
	"Files":   {"AuthoredTest", "VerdictJSON"},
	"Mutants": {"Code"},
	// Evidence quotes the flagged test.
	"CriticFindings": {"Evidence"},
}

// fillStrings sets every string field of the struct v points at to a unique
// non-empty marker, so "was blanked" and "was never set" cannot be confused.
func fillStrings(v reflect.Value, prefix string) {
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		if f.Kind() == reflect.String && f.CanSet() {
			f.SetString(fmt.Sprintf("%s-%s", prefix, v.Type().Field(i).Name))
		}
	}
}

func custodyFixture() Bundle {
	var row Row
	fillStrings(reflect.ValueOf(&row).Elem(), "row")
	var mut MutantRow
	fillStrings(reflect.ValueOf(&mut).Elem(), "mutant")
	var call ModelCallRow
	fillStrings(reflect.ValueOf(&call).Elem(), "call")
	var ev EventRow
	fillStrings(reflect.ValueOf(&ev).Elem(), "event")
	var scan ScanRow
	fillStrings(reflect.ValueOf(&scan).Elem(), "scan")
	var seat SeatRow
	fillStrings(reflect.ValueOf(&seat).Elem(), "seat")
	var att MutantAttemptRow
	fillStrings(reflect.ValueOf(&att).Elem(), "attempt")
	var cf CriticFindingRow
	fillStrings(reflect.ValueOf(&cf).Elem(), "finding")
	return Bundle{Scan: scan, Files: []Row{row}, Mutants: []MutantRow{mut}, Calls: []ModelCallRow{call}, Events: []EventRow{ev},
		Seats: []SeatRow{seat}, MutantAttempts: []MutantAttemptRow{att}, CriticFindings: []CriticFindingRow{cf}}
}

// TestBlankUnpushedSourceIsTheOneCustodySet pins the rule that used to exist
// twice — once in the warehouse WRITER and once in the statement HASHER — as
// one exported function both call. Two copies of a custody rule is one copy
// that gets a field added to it and one that does not, and the hasher's
// divergence is the silent kind: the signed statement carries a number no
// verifier can reproduce from the rows they can see.
func TestBlankUnpushedSourceIsTheOneCustodySet(t *testing.T) {
	t.Run("withheld", func(t *testing.T) {
		b := custodyFixture()
		b.SourcePushed = false
		before := custodyFixture()
		BlankUnpushedSource(&b)

		assertCustody(t, b, before, true)
	})

	t.Run("--push-source", func(t *testing.T) {
		b := custodyFixture()
		b.SourcePushed = true
		before := custodyFixture()
		BlankUnpushedSource(&b)

		assertCustody(t, b, before, false)
	})

	// The caller's own slices must not be mutated: PushBundle hands the
	// bundle straight on from a caller that goes on to hash it.
	t.Run("does not mutate the caller's rows", func(t *testing.T) {
		b := custodyFixture()
		b.SourcePushed = false
		aliased := b.Files
		BlankUnpushedSource(&b)
		if aliased[0].AuthoredTest == "" {
			t.Error("BlankUnpushedSource blanked the caller's own slice in place")
		}
	})
}

// assertCustody walks Files and Mutants field by field: every field named in
// custodySet must be empty when blanked is true and untouched when it is
// false, and every OTHER string field must be untouched either way.
func assertCustody(t *testing.T, got, before Bundle, blanked bool) {
	t.Helper()
	pairs := []struct {
		name          string
		got, original reflect.Value
	}{
		{"Files", reflect.ValueOf(got.Files[0]), reflect.ValueOf(before.Files[0])},
		{"Mutants", reflect.ValueOf(got.Mutants[0]), reflect.ValueOf(before.Mutants[0])},
		{"Calls", reflect.ValueOf(got.Calls[0]), reflect.ValueOf(before.Calls[0])},
		{"Events", reflect.ValueOf(got.Events[0]), reflect.ValueOf(before.Events[0])},
		{"Scan", reflect.ValueOf(got.Scan), reflect.ValueOf(before.Scan)},
		{"Seats", reflect.ValueOf(got.Seats[0]), reflect.ValueOf(before.Seats[0])},
		{"MutantAttempts", reflect.ValueOf(got.MutantAttempts[0]), reflect.ValueOf(before.MutantAttempts[0])},
		{"CriticFindings", reflect.ValueOf(got.CriticFindings[0]), reflect.ValueOf(before.CriticFindings[0])},
	}
	for _, p := range pairs {
		inSet := map[string]bool{}
		for _, n := range custodySet[p.name] {
			inSet[n] = true
		}
		for i := 0; i < p.got.NumField(); i++ {
			name := p.got.Type().Field(i).Name
			if p.got.Field(i).Kind() != reflect.String {
				continue
			}
			g, o := p.got.Field(i).String(), p.original.Field(i).String()
			switch {
			case inSet[name] && blanked:
				if g != "" {
					t.Errorf("%s.%s = %q, want it withheld without --push-source", p.name, name, g)
				}
			default:
				if g != o {
					t.Errorf("%s.%s = %q, want %q — a field outside the custody set was changed", p.name, name, g, o)
				}
			}
		}
	}
}

// TestPushBundleWithholdsEveryCustodyField is the writer's half: the rule is
// enforced by PushBundle, not by whoever built the bundle, so a caller that
// forgets to blank a field cannot leak it.
func TestPushBundleWithholdsEveryCustodyField(t *testing.T) {
	for _, sourcePushed := range []bool{false, true} {
		name := "withheld"
		if sourcePushed {
			name = "--push-source"
		}
		t.Run(name, func(t *testing.T) {
			b := Bundle{
				Files:        []Row{{Repo: "o/r", Path: "a.go", AuthoredTest: "AUTHORED", VerdictJSON: `{"v":1}`}},
				Mutants:      []MutantRow{{Repo: "o/r", Path: "a.go", MutantID: "m1", Outcome: "killed", Code: "MUTANT"}},
				SourcePushed: sourcePushed,
			}
			target := filepath.Join(t.TempDir(), "w.duckdb")
			if _, err := PushBundle(target, b); err != nil {
				t.Fatalf("PushBundle: %v", err)
			}
			// And the caller's rows survive the push unblanked: the writer
			// withholds, it does not confiscate.
			if b.Files[0].AuthoredTest != "AUTHORED" || b.Mutants[0].Code != "MUTANT" {
				t.Error("PushBundle mutated the caller's own rows")
			}

			db, err := sql.Open("duckdb", target)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			for _, q := range []struct{ query, field string }{
				{`SELECT count(*) FROM corral_audits WHERE authored_test IS NOT NULL`, "authored_test"},
				{`SELECT count(*) FROM corral_audits WHERE verdict_json IS NOT NULL`, "verdict_json"},
				{`SELECT count(*) FROM corral_mutants WHERE code IS NOT NULL`, "mutants.code"},
			} {
				var n int64
				if err := db.QueryRow(q.query).Scan(&n); err != nil {
					t.Fatalf("%s: %v", q.field, err)
				}
				if (n > 0) != sourcePushed {
					t.Errorf("%s present = %v, want %v", q.field, n > 0, sourcePushed)
				}
			}
		})
	}
}

// TestCriticEvidenceIsSource: a critic finding's Evidence quotes code, so it
// is withheld unless the run pushed source, and nothing else on the row is.
// (assertCustody above also walks every other string field of the three
// telemetry rows and fails if one is blanked.)
func TestCriticEvidenceIsSource(t *testing.T) {
	for _, pushed := range []bool{false, true} {
		b := Bundle{CriticFindings: []CriticFindingRow{{Path: "a.go", Evidence: "func TestX() {}"}}, SourcePushed: pushed}
		BlankUnpushedSource(&b)
		if got := b.CriticFindings[0].Evidence != ""; got != pushed {
			t.Errorf("SourcePushed=%v: evidence kept = %v", pushed, got)
		}
		if b.CriticFindings[0].Path != "a.go" {
			t.Error("a non-source field of the finding was blanked")
		}
	}
	orig := []CriticFindingRow{{Evidence: "E"}}
	b := Bundle{CriticFindings: orig}
	BlankUnpushedSource(&b)
	if orig[0].Evidence != "E" {
		t.Error("BlankUnpushedSource blanked the caller's own slice in place")
	}
}

// TestPushBundleIgnoresTelemetryRows: the warehouse has no table for the
// seat, attempt and finding rows yet (Phase 4); a bundle carrying them must
// push cleanly and the counts must not claim they were written.
func TestPushBundleIgnoresTelemetryRows(t *testing.T) {
	b := Bundle{
		Files:          []Row{{Repo: "o/r", Path: "a.go"}},
		Seats:          []SeatRow{{Repo: "o/r", Model: "m"}},
		MutantAttempts: []MutantAttemptRow{{Repo: "o/r", MutantID: "m1"}},
		CriticFindings: []CriticFindingRow{{Repo: "o/r", Evidence: "E"}},
	}
	n, err := PushBundle(filepath.Join(t.TempDir(), "w.duckdb"), b)
	if err != nil {
		t.Fatalf("PushBundle: %v", err)
	}
	if n.Files != 1 {
		t.Errorf("Files = %d, want 1", n.Files)
	}
}

// TestBundleWithoutTelemetryHashesAsBefore: the new fields are omitempty, so
// a bundle that carries none marshals to exactly the bytes it did before
// they existed. The fixture is a literal, written before the fields were.
func TestBundleWithoutTelemetryHashesAsBefore(t *testing.T) {
	const before = `{"Scan":{"ScanUID":"","Repo":"","RunURL":"","ScanID":0,"Commit":"","CorralVersion":"","Substrate":"","Host":"","Cores":0,"TreesRequested":0,"DiffBase":"","Scope":"","Candidates":0,"Audited":0,"Passed":null,"StartedAt":null,"TotalMillis":null,"SelectionMillis":null,"SelectionReused":false,"ModelCalls":0,"SourcePushed":false,"StatementSHA256":"","RekorLogIndex":null,"RekorUUID":"","PushedBy":"","EntryHash":"","EngineVersion":"","ModelSet":"","Top":0,"AllCandidates":false,"TotalFiles":0,"PreflightRan":false,"PreflightNote":"","FinishedAt":null,"Identity":{}},"Files":null,"Mutants":null,"Calls":null,"Events":null,"Link":{"ScanID":0,"StatementSHA256":"","Require":false},"SourcePushed":false}`
	got, err := json.Marshal(Bundle{})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != before {
		t.Errorf("a bundle with no telemetry no longer marshals as it did:\n got %s\nwant %s", got, before)
	}
}
